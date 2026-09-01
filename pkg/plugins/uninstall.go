package plugins

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/infracost/cli/pkg/logging"
)

// ErrPluginNotInstalled is returned by ResolveUninstall when the given name
// matches nothing the CLI manages or discovered on disk — no required plugin,
// no provenance record, and no unmanaged binary. Callers use errors.Is to
// detect it and enrich the message (e.g. "exists in the registry but is not
// installed").
var ErrPluginNotInstalled = errors.New("plugin is not installed")

// UninstallComponent is one binary an uninstall will remove.
type UninstallComponent struct {
	// Type is "parser"/"provider", or "" for an unmanaged binary whose type
	// isn't recorded.
	Type string
	// BinaryName is the on-disk binary filename without any .exe suffix.
	BinaryName string
	// Path is the full on-disk path (with .exe on Windows).
	Path string
	// Present is whether the binary existed on disk at resolution time.
	Present bool
}

// UninstallTarget is the resolved set of components one uninstall will remove,
// along with how the CLI knows about them. It is produced by ResolveUninstall
// and consumed by Uninstall; the command layer renders it and drives the
// required-plugin confirmation.
type UninstallTarget struct {
	// Name is the registry/display name of the owning entry, or the binary
	// name for an unmanaged plugin.
	Name string
	// Required is true when the entry is in the compiled-in auto-managed set.
	Required bool
	// Unmanaged is true for a hand-copied binary with no registry ownership.
	Unmanaged bool
	// HasRecord is true when a provenance record backs this target and must be
	// cleaned up on uninstall (independently of whether the binaries survive).
	HasRecord bool
	// Components is every component the uninstall will remove.
	Components []UninstallComponent
	// NamedComponent is set to the binary name the user typed when they named a
	// single component of a multi-component entry, so the command can state
	// that the whole entry (all components) will be removed.
	NamedComponent string
}

// Actionable reports whether there is anything to uninstall: at least one
// component binary is present, or a provenance record exists to clean up. A
// required plugin that isn't installed resolves to a target but is not
// actionable — the CLI would just re-download it.
func (t *UninstallTarget) Actionable() bool {
	if t.HasRecord {
		return true
	}
	for _, c := range t.Components {
		if c.Present {
			return true
		}
	}
	return false
}

// UninstallResult describes what an Uninstall call did. It is human-facing
// only — the command layer renders it.
type UninstallResult struct {
	// Target is the resolved target that was uninstalled.
	Target *UninstallTarget
	// Removed lists the component binaries actually deleted this run.
	Removed []UninstallComponent
	// Missing lists components whose binary was already gone (idempotent
	// cleanup still removed any sidecars and, if present, the record).
	Missing []UninstallComponent
	// RecordRemoved is true when a provenance record was dropped.
	RecordRemoved bool
}

// ResolveUninstall resolves a user-supplied plugin identifier to the set of
// things an uninstall would remove, using purely local state — no network. It
// matches, in order: the compiled-in required set (by display name, key,
// binary name, or legacy name); a provenance record (by entry name or a
// component binary name); a discovered unmanaged binary in the plugin
// directory. Naming a single component of a multi-component entry resolves to
// the whole entry with NamedComponent set.
//
// On no local match it returns ErrPluginNotInstalled so the caller can decide
// whether to enrich the message using the registry.
func (c *Config) ResolveUninstall(input string) (*UninstallTarget, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil, fmt.Errorf("no plugin name provided")
	}

	if displayName, ok := requiredDisplayNameFor(input); ok {
		return c.requiredTarget(displayName, input), nil
	}

	if t := c.recordTarget(input); t != nil {
		return t, nil
	}

	if t := c.unmanagedTarget(input); t != nil {
		return t, nil
	}

	return nil, fmt.Errorf("%w: %q", ErrPluginNotInstalled, input)
}

// requiredDisplayNameFor maps a user identifier to a required plugin's registry
// display name, tolerating a trailing .exe on binary names. It returns false
// when the input names no required plugin.
func requiredDisplayNameFor(input string) (string, bool) {
	base := strings.TrimSuffix(input, ".exe")
	for _, r := range requiredPlugins {
		if r.DisplayName == input {
			return r.DisplayName, true
		}
		if r.Key == input || r.Name == input || r.Name == base {
			return r.DisplayName, true
		}
		if r.LegacyName != "" && (r.LegacyName == input || r.LegacyName == base) {
			return r.DisplayName, true
		}
	}
	return "", false
}

// requiredTarget builds an uninstall target for a required plugin display name,
// listing every component (parser and/or provider) that shares it.
func (c *Config) requiredTarget(displayName, input string) *UninstallTarget {
	reqs := requiredPluginsForName(displayName)
	dir := c.PluginDir()
	t := &UninstallTarget{Name: displayName, Required: true}
	for _, r := range reqs {
		path := filepath.Join(dir, pluginBinaryName(r.Name))
		t.Components = append(t.Components, UninstallComponent{
			Type:       r.Type,
			BinaryName: r.Name,
			Path:       path,
			Present:    flatPluginBinaryExists(path),
		})
	}
	if len(reqs) > 1 {
		base := strings.TrimSuffix(input, ".exe")
		for _, r := range reqs {
			if r.Name == input || r.Name == base {
				t.NamedComponent = r.Name
				break
			}
		}
	}
	return t
}

// recordTarget builds an uninstall target from a provenance record matching the
// input by entry name or by one of its component binary names. It returns nil
// when no record matches.
func (c *Config) recordTarget(input string) *UninstallTarget {
	state := loadState(c.pluginStateDir())

	rec := state.find(input)
	named := ""
	if rec == nil {
		base := strings.TrimSuffix(input, ".exe")
	search:
		for i := range state.Records {
			for _, comp := range state.Records[i].Components {
				if comp.BinaryName == input || comp.BinaryName == base {
					rec = &state.Records[i]
					if len(state.Records[i].Components) > 1 {
						named = comp.BinaryName
					}
					break search
				}
			}
		}
	}
	if rec == nil {
		return nil
	}

	dir := c.PluginDir()
	t := &UninstallTarget{Name: rec.Name, HasRecord: true, NamedComponent: named}
	for _, comp := range rec.Components {
		path := filepath.Join(dir, pluginBinaryName(comp.BinaryName))
		t.Components = append(t.Components, UninstallComponent{
			Type:       comp.Type,
			BinaryName: comp.BinaryName,
			Path:       path,
			Present:    flatPluginBinaryExists(path),
		})
	}
	return t
}

// unmanagedTarget looks for a hand-copied binary in the plugin directory whose
// filename matches the input (tolerating a trailing .exe). It returns nil when
// nothing matches. Required binaries are handled earlier, so a required binary
// found here is ignored to avoid mis-classifying it as unmanaged.
func (c *Config) unmanagedTarget(input string) *UninstallTarget {
	dir := c.PluginDir()
	base := strings.TrimSuffix(input, ".exe")

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	for _, e := range entries {
		if e.IsDir() || isPluginSidecar(e.Name()) {
			continue
		}
		nameBase := strings.TrimSuffix(e.Name(), ".exe")
		if nameBase != base {
			continue
		}
		if isRequiredBinaryName(nameBase) {
			return nil
		}
		path := filepath.Join(dir, e.Name())
		return &UninstallTarget{
			Name:      nameBase,
			Unmanaged: true,
			Components: []UninstallComponent{{
				BinaryName: nameBase,
				Path:       path,
				Present:    true,
			}},
		}
	}
	return nil
}

// isRequiredBinaryName reports whether a binary filename (without .exe) belongs
// to the compiled-in required set (current or legacy name).
func isRequiredBinaryName(name string) bool {
	for _, r := range requiredPlugins {
		if r.Name == name || (r.LegacyName != "" && r.LegacyName == name) {
			return true
		}
	}
	return false
}

// Uninstall removes every present component binary of the target, each binary's
// sidecar files, and the provenance record if one exists. It refuses under an
// INFRACOST_CLI_PLUGIN_DIR dev override. It is idempotent: a component whose
// binary is already gone is reported as missing but still has its sidecars
// cleaned, and a record with no surviving binaries is still dropped.
func (c *Config) Uninstall(target *UninstallTarget) (*UninstallResult, error) {
	if c.Dir != "" {
		return nil, devOverrideUninstallError(c.Dir)
	}

	res := &UninstallResult{Target: target}
	for _, comp := range target.Components {
		if !flatPluginBinaryExists(comp.Path) {
			removeSidecars(comp.Path)
			res.Missing = append(res.Missing, comp)
			continue
		}
		if err := removeWithRetry(comp.Path); err != nil {
			return nil, fmt.Errorf("failed to remove plugin binary %s: %w", comp.Path, err)
		}
		removeSidecars(comp.Path)
		res.Removed = append(res.Removed, comp)
	}

	if target.HasRecord {
		state := loadState(c.pluginStateDir())
		if state.remove(target.Name) {
			if err := state.save(c.pluginStateDir()); err != nil {
				// The binaries are gone; a stale record is advisory only, so a
				// state-write failure must not fail the uninstall.
				logging.Warnf("uninstalled %s but failed to update the plugin state file: %v", target.Name, err)
			} else {
				res.RecordRemoved = true
			}
		}
	}

	return res, nil
}

// devOverrideUninstallError refuses an uninstall while a dev-override plugin
// directory is in effect, mirroring the install/update refusal shape.
func devOverrideUninstallError(dir string) error {
	return fmt.Errorf("plugin uninstalls are disabled while INFRACOST_CLI_PLUGIN_DIR is set (%s) — plugins are loaded from that directory; unset it to manage plugins automatically", dir)
}

// removeSidecars best-effort deletes the `.sha256` and `.version` sidecars for
// a binary path. On Windows the path carries a `.exe` suffix; the exe-stripped
// base is tried too so either naming convention is cleaned up. Missing files
// are not an error (removeWithRetry treats not-exist as success).
func removeSidecars(binaryPath string) {
	bases := []string{binaryPath}
	if trimmed := strings.TrimSuffix(binaryPath, ".exe"); trimmed != binaryPath {
		bases = append(bases, trimmed)
	}
	for _, b := range bases {
		for _, suffix := range []string{".sha256", ".version"} {
			_ = removeWithRetry(b + suffix)
		}
	}
}

// removeWithRetry retries os.Remove a few times to tolerate transient locks
// from AV scanners or in-flight scans on Windows, mirroring renameWithRetry. A
// file that is already absent is treated as success so removal is idempotent.
func removeWithRetry(path string) error {
	const maxAttempts = 5
	const retryDelay = 500 * time.Millisecond

	var lastErr error
	for i := 0; i < maxAttempts; i++ {
		err := os.Remove(path)
		if err == nil || errors.Is(err, os.ErrNotExist) {
			return nil
		}
		lastErr = err
		if i < maxAttempts-1 {
			time.Sleep(retryDelay * time.Duration(i+1))
		}
	}
	return lastErr
}
