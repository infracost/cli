package plugins

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/infracost/cli/pkg/logging"
	"github.com/infracost/cli/pkg/plugins/registry"
)

// UpdateStatus is the outcome for a single entry in an update run.
type UpdateStatus string

const (
	// UpdateStatusUpdated: at least one component was downloaded and committed.
	UpdateStatusUpdated UpdateStatus = "updated"
	// UpdateStatusUpToDate: every component was already at the resolved version.
	UpdateStatusUpToDate UpdateStatus = "up-to-date"
	// UpdateStatusSkippedPinned: update-all skipped an entry pinned to a version.
	UpdateStatusSkippedPinned UpdateStatus = "skipped-pinned"
	// UpdateStatusSkippedDev: a component is a local dev build, so the whole
	// entry was left untouched.
	UpdateStatusSkippedDev UpdateStatus = "skipped-dev"
	// UpdateStatusSkippedUnofficial: the trust gate declined (interactive No, or
	// non-interactive without --allow-unofficial in update-all).
	UpdateStatusSkippedUnofficial UpdateStatus = "skipped-unofficial"
	// UpdateStatusSkippedRemoved: a recorded entry is no longer in the registry.
	UpdateStatusSkippedRemoved UpdateStatus = "skipped-removed"
	// UpdateStatusSkippedMissing: update-all skipped an entry whose recorded
	// component binary is gone (an explicit update reinstalls it instead).
	UpdateStatusSkippedMissing UpdateStatus = "skipped-missing"
	// UpdateStatusFailed: resolution, download, or verification failed.
	UpdateStatusFailed UpdateStatus = "failed"
)

// EntryUpdate is the result of trying to update one entry (a registry entry or a
// built-in required plugin). It is human-facing only — the command layer renders
// it; there is no --json for update.
type EntryUpdate struct {
	// Name is the registry/display name of the entry.
	Name string
	// Status is what happened to the entry.
	Status UpdateStatus
	// FromVersion is the version recorded before the update (may be empty).
	FromVersion string
	// ToVersion is the resolved version after a successful update or up-to-date
	// check (empty on a skip or failure that never resolved a version).
	ToVersion string
	// Components lists the components (re)installed this run.
	Components []ComponentInstall
	// Err is set when Status is UpdateStatusFailed.
	Err error
	// Detail carries a status-specific note (e.g. the missing component's name).
	Detail string
}

// UpdateResult aggregates an update run across every managed entry.
type UpdateResult struct {
	// Entries is the per-entry outcome, in processing order. Successful required
	// plugins are not listed (their download lines are the UX); only required
	// failures appear.
	Entries []EntryUpdate
	// Unmanaged lists hand-copied binaries with no provenance — reported, never
	// touched.
	Unmanaged []string
}

// Failed reports whether any entry failed to update, so the command can exit
// non-zero.
func (r *UpdateResult) Failed() bool {
	for _, e := range r.Entries {
		if e.Status == UpdateStatusFailed {
			return true
		}
	}
	return false
}

// devOverrideUpdateError refuses an update while a dev-override plugin directory
// is in effect, mirroring the install/uninstall refusal shape.
func devOverrideUpdateError(dir string) error {
	return fmt.Errorf("plugin updates are disabled while INFRACOST_CLI_PLUGIN_DIR is set (%s) — plugins are loaded from that directory; unset it to manage plugins automatically", dir)
}

// RequiredDisplayNameFor maps a user identifier (key, registry name, or
// component binary name, tolerating .exe) to a built-in required plugin's
// display name. It is the exported wrapper the command layer uses to route a
// name to the required-set update path before touching the registry.
func RequiredDisplayNameFor(input string) (string, bool) {
	return requiredDisplayNameFor(input)
}

// IsRequiredName reports whether name is a built-in required plugin's display
// name. Exported for the command layer.
func IsRequiredName(name string) bool {
	return isRequiredName(name)
}

// HasRegistryInstalls reports whether the provenance state records any
// registry-installed (non-required) entry. The command uses it to decide
// whether update-all needs to fetch the registry at all — with only the
// built-in set present, the no-registry-installs path behaves exactly as before.
func (c *Config) HasRegistryInstalls() bool {
	for _, rec := range c.LoadState().Records {
		if !isRequiredName(rec.Name) {
			return true
		}
	}
	return false
}

// UpdatePlugins forces every required plugin to be re-checked against the
// release host and updated to its latest (or user-pinned) version, ignoring the
// AutoUpdate setting. It is retained for callers and tests that only need the
// required set refreshed; the richer UpdateAll drives the `plugin update`
// command. A dev-override directory refuses; otherwise the first required-plugin
// failure is returned (all are attempted first).
func (c *Config) UpdatePlugins(ctx context.Context) error {
	if c.Dir != "" {
		return devOverrideUpdateError(c.Dir)
	}

	mgr := NewManager(ManagerOptions{
		Dir:        c.PluginDir(),
		Cache:      c.Cache,
		BaseURL:    c.BaseURL,
		AutoUpdate: true,
	})
	defer mgr.Close()

	for _, f := range c.updateRequiredSet(ctx, mgr) {
		return f.Err
	}
	return nil
}

// UpdateAll updates everything the CLI manages: the built-in required set plus
// every provenance-recorded registry entry, to their latest versions. reg is
// the loaded registry manifest (nil with regErr when it could not be loaded, or
// nil with no error when the caller determined no registry entries are
// installed). trust gates unofficial entries.
//
// One entry's failure never aborts the rest: failures are collected into the
// result and surfaced via Failed(). It refuses under a dev-override directory.
func (c *Config) UpdateAll(ctx context.Context, reg *registry.Registry, regErr error, trust TrustFunc) (*UpdateResult, error) {
	if c.Dir != "" {
		return nil, devOverrideUpdateError(c.Dir)
	}

	res := &UpdateResult{}

	// Required set first — preserves the existing behavior and spinner UX. Only
	// failures are surfaced as entries; successes show their "Downloaded" lines.
	mgr := NewManager(ManagerOptions{
		Dir:        c.PluginDir(),
		Cache:      c.Cache,
		BaseURL:    c.BaseURL,
		AutoUpdate: true,
	})
	defer mgr.Close()
	res.Entries = append(res.Entries, c.updateRequiredSet(ctx, mgr)...)

	// Provenance-recorded registry entries.
	res.Entries = append(res.Entries, c.updateRecordedEntries(ctx, reg, regErr, trust)...)

	// Hand-copied binaries with no provenance — reported, never touched.
	res.Unmanaged = c.discoverUnmanaged()

	return res, nil
}

// updateRequiredSet re-checks and updates every built-in required plugin via a
// Manager, collecting per-plugin failures instead of aborting on the first one.
// Successful updates return no entry — their nested "Downloaded" spinner lines
// are the UX. Only failures are returned.
func (c *Config) updateRequiredSet(ctx context.Context, mgr *Manager) []EntryUpdate {
	mgr.removeLegacyPlugins()

	var failures []EntryUpdate
	for _, r := range requiredPlugins {
		if _, err := mgr.Install(ctx, r.Name, requiredPluginVersion(r.Key)); err != nil {
			failures = append(failures, EntryUpdate{
				Name:   r.DisplayName,
				Status: UpdateStatusFailed,
				Err:    err,
			})
		}
	}
	return failures
}

// updateRecordedEntries updates every non-required provenance-recorded entry to
// its latest version. Pins are respected (skipped), entries no longer in the
// registry are skipped with a warning (never uninstalled), and per-entry
// failures are collected rather than aborting the run.
func (c *Config) updateRecordedEntries(ctx context.Context, reg *registry.Registry, regErr error, trust TrustFunc) []EntryUpdate {
	state := c.LoadState()
	stager := c.newRegistryStager()

	var out []EntryUpdate
	for i := range state.Records {
		rec := &state.Records[i]

		// Required names never carry provenance (dropRequired), but be defensive.
		if isRequiredName(rec.Name) {
			continue
		}

		if rec.Pinned {
			out = append(out, EntryUpdate{
				Name:        rec.Name,
				Status:      UpdateStatusSkippedPinned,
				FromVersion: rec.Version,
			})
			continue
		}

		if reg == nil {
			err := regErr
			if err == nil {
				err = fmt.Errorf("the plugin registry is unavailable")
			}
			out = append(out, EntryUpdate{Name: rec.Name, Status: UpdateStatusFailed, FromVersion: rec.Version, Err: err})
			continue
		}

		entry := reg.ByName(rec.Name)
		if entry == nil {
			out = append(out, EntryUpdate{Name: rec.Name, Status: UpdateStatusSkippedRemoved, FromVersion: rec.Version})
			continue
		}

		out = append(out, stager.updateEntry(ctx, entry, rec, trust, true))
	}
	return out
}

// UpdateEntry updates a single non-required registry entry to its latest
// version. An explicit update reinstalls a missing component and clears any
// version pin. It refuses under a dev-override directory and errors if the entry
// is not currently installed.
func (c *Config) UpdateEntry(ctx context.Context, e *registry.Entry, trust TrustFunc) (*UpdateResult, error) {
	if c.Dir != "" {
		return nil, devOverrideUpdateError(c.Dir)
	}

	rec := c.LoadState().find(e.Name)
	if rec == nil {
		return nil, fmt.Errorf("%s is not installed — run `infracost plugin install %s` to install it", e.Name, e.Name)
	}

	eu := c.newRegistryStager().updateEntry(ctx, e, rec, trust, false)
	res := &UpdateResult{Entries: []EntryUpdate{eu}}
	if eu.Status == UpdateStatusFailed {
		return res, eu.Err
	}
	return res, nil
}

// UpdateRequiredEntry updates a single built-in required plugin (every component
// sharing its display name) to its latest or env-pinned version, via a Manager.
// It refuses under a dev-override directory. Single-entry error behavior: the
// first component failure aborts.
func (c *Config) UpdateRequiredEntry(ctx context.Context, displayName string) error {
	if c.Dir != "" {
		return devOverrideUpdateError(c.Dir)
	}

	reqs := requiredPluginsForName(displayName)
	if len(reqs) == 0 {
		return fmt.Errorf("internal error: %q is required but has no components", displayName)
	}

	mgr := NewManager(ManagerOptions{
		Dir:        c.PluginDir(),
		Cache:      c.Cache,
		BaseURL:    c.BaseURL,
		AutoUpdate: true,
	})
	defer mgr.Close()

	mgr.removeLegacyPlugins()
	for _, r := range reqs {
		if _, err := mgr.Install(ctx, r.Name, requiredPluginVersion(r.Key)); err != nil {
			return err
		}
	}
	return nil
}

// discoverUnmanaged lists hand-copied plugin binaries in the plugin directory
// that belong neither to the built-in required set nor to any provenance record.
// They are reported by update-all so the user knows the CLI won't manage them.
func (c *Config) discoverUnmanaged() []string {
	entries, err := os.ReadDir(c.PluginDir())
	if err != nil {
		return nil
	}

	recorded := map[string]bool{}
	for _, rec := range c.LoadState().Records {
		for _, comp := range rec.Components {
			recorded[comp.BinaryName] = true
		}
	}

	var out []string
	for _, e := range entries {
		if e.IsDir() || isPluginSidecar(e.Name()) {
			continue
		}
		base := strings.TrimSuffix(e.Name(), ".exe")
		if isRequiredBinaryName(base) || recorded[base] {
			continue
		}
		out = append(out, base)
	}
	return out
}

// updateEntry stages an update of one registry entry to its latest version,
// all-or-nothing. updateAll selects update-all rules (skip a missing recorded
// component) vs explicit rules (reinstall it, and clear a stale pin). It never
// returns an error; a failure is captured in the returned EntryUpdate.
func (s *registryStager) updateEntry(ctx context.Context, e *registry.Entry, rec *StateRecord, trust TrustFunc, updateAll bool) EntryUpdate {
	eu := EntryUpdate{Name: e.Name, FromVersion: rec.Version}

	if !e.Installable() {
		eu.Status = UpdateStatusFailed
		eu.Err = fmt.Errorf("plugin %q cannot be updated: %s", e.Name, e.UninstallableReason())
		return eu
	}
	if unsupported := e.UnsupportedComponents(s.goos, s.goarch); len(unsupported) > 0 {
		eu.Status = UpdateStatusFailed
		eu.Err = unsupportedPlatformError(e, unsupported, s.goos, s.goarch)
		return eu
	}
	if err := checkMinCLIVersion(e); err != nil {
		eu.Status = UpdateStatusFailed
		eu.Err = err
		return eu
	}

	// A dev component pins the whole entry — never overwrite a local dev build.
	if s.entryHasDevComponent(e) {
		eu.Status = UpdateStatusSkippedDev
		return eu
	}

	// Update-all skips an entry whose recorded component binary is gone; an
	// explicit update falls through and reinstalls the full entry.
	if updateAll {
		if missing, ok := s.recordedComponentMissing(rec); ok {
			eu.Status = UpdateStatusSkippedMissing
			eu.Detail = missing
			return eu
		}
	}

	result, err := s.install(ctx, e, "", trust)
	if err != nil {
		eu.Status = UpdateStatusFailed
		eu.Err = err
		return eu
	}
	if result.Declined {
		eu.Status = UpdateStatusSkippedUnofficial
		return eu
	}

	eu.ToVersion = result.Version
	eu.Components = result.Installed

	if result.NoOp {
		eu.Status = UpdateStatusUpToDate
		// Explicit update clears a pin even when already at the latest version:
		// install writes no provenance on a no-op, so clear it directly.
		if !updateAll && rec.Pinned {
			if err := s.clearPin(e.Name); err != nil {
				logging.Warnf("updated %s but failed to clear its version pin: %v", e.Name, err)
			}
		}
		return eu
	}

	// A real install wrote provenance with pinned=false (wantVersion=""), which
	// already clears any prior pin.
	eu.Status = UpdateStatusUpdated
	return eu
}

// entryHasDevComponent reports whether any installed component of the entry is a
// local dev build. Missing or unreadable components are treated as not-dev.
func (s *registryStager) entryHasDevComponent(e *registry.Entry) bool {
	for _, comp := range e.Components {
		path := s.binaryPath(comp)
		if !flatPluginBinaryExists(path) {
			continue
		}
		info, err := s.query(path)
		if err != nil {
			continue
		}
		if info.GetVersion() == devPluginVersion {
			return true
		}
	}
	return false
}

// recordedComponentMissing reports the first recorded component binary that is
// absent on disk, if any.
func (s *registryStager) recordedComponentMissing(rec *StateRecord) (string, bool) {
	for _, comp := range rec.Components {
		path := s.binaryPath(registry.Component{BinaryName: comp.BinaryName})
		if !flatPluginBinaryExists(path) {
			return comp.BinaryName, true
		}
	}
	return "", false
}

// clearPin flips a recorded entry's Pinned flag off and persists the change. A
// missing or already-unpinned record is a no-op.
func (s *registryStager) clearPin(name string) error {
	state := loadState(s.cacheDir)
	rec := state.find(name)
	if rec == nil || !rec.Pinned {
		return nil
	}
	rec.Pinned = false
	return state.save(s.cacheDir)
}
