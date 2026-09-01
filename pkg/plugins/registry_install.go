package plugins

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/Masterminds/semver/v3"
	"github.com/infracost/cli/internal/ui"
	"github.com/infracost/cli/pkg/logging"
	"github.com/infracost/cli/pkg/plugins/registry"
	"github.com/infracost/cli/version"
	pb "github.com/infracost/proto/gen/go/infracost/plugin"
)

// TrustFunc gates the download of an unofficial (`official: false`) registry
// entry. InstallRegistryEntry calls it once, only after it has determined that
// at least one component must actually be downloaded and before any bytes are
// fetched. Official entries: the callback is still invoked (the trust gate
// returns true immediately for them), so a no-op install never prompts. A
// (false, nil) return is a clean decline — nothing is installed and no error is
// raised.
type TrustFunc func(e *registry.Entry) (proceed bool, err error)

// ComponentInstall records the outcome for one component of a registry entry
// install: which component it is and where its binary lives on disk.
type ComponentInstall struct {
	Component registry.Component
	Path      string
}

// RegistryInstallResult describes what an InstallRegistryEntry call did. It is
// human-facing only — the command layer renders it; there is no --json.
type RegistryInstallResult struct {
	// Entry is the registry entry that was installed.
	Entry *registry.Entry
	// Version is the shared release version every component was installed at.
	// Empty for a required-set ensure, whose version is CLI-managed.
	Version string
	// Pinned is true when an explicit @<version> was requested.
	Pinned bool
	// Required is true when the entry is in the compiled-in required set and was
	// handled by the ensure/pre-warm path rather than the staged installer.
	Required bool
	// NoOp is true when every component was already current — nothing was
	// downloaded and no provenance was written.
	NoOp bool
	// Declined is true when the trust gate returned a clean decline.
	Declined bool
	// Installed lists the components installed (or reinstalled) this run.
	Installed []ComponentInstall
	// Current lists the components left untouched because they were already at
	// the resolved version.
	Current []ComponentInstall
}

// InstallRegistryEntry installs every component of a registry entry, staged and
// all-or-nothing. wantVersion pins an exact version ("" means resolve the
// entry's latest shared version). trust gates unofficial entries and is invoked
// once, only when a download is actually required.
//
// It refuses when a developer plugin-dir override is in effect, when the entry
// declares a component type this CLI can't install, when the current platform
// isn't supported by every component, and when the entry's minCliVersion is
// newer than this CLI. Installing a required-set name is an ensure/pre-warm and
// never records provenance or a durable pin.
func (c *Config) InstallRegistryEntry(ctx context.Context, e *registry.Entry, wantVersion string, trust TrustFunc) (*RegistryInstallResult, error) {
	if c.Dir != "" {
		return nil, fmt.Errorf("plugin installs are disabled while INFRACOST_CLI_PLUGIN_DIR is set (%s) — plugins are loaded from that directory; unset it to manage plugins automatically", c.Dir)
	}

	if !e.Installable() {
		return nil, fmt.Errorf("plugin %q cannot be installed: %s", e.Name, e.UninstallableReason())
	}

	goos, goarch := runtime.GOOS, runtime.GOARCH
	if unsupported := e.UnsupportedComponents(goos, goarch); len(unsupported) > 0 {
		return nil, unsupportedPlatformError(e, unsupported, goos, goarch)
	}

	if err := checkMinCLIVersion(e); err != nil {
		return nil, err
	}

	if isRequiredName(e.Name) {
		return c.ensureRequiredEntry(ctx, e, wantVersion)
	}

	return c.newRegistryStager().install(ctx, e, wantVersion, trust)
}

// unsupportedPlatformError refuses the whole entry, naming each component that
// does not support the current platform along with the platforms it does.
func unsupportedPlatformError(e *registry.Entry, unsupported []registry.Component, goos, goarch string) error {
	parts := make([]string, 0, len(unsupported))
	for _, c := range unsupported {
		parts = append(parts, fmt.Sprintf("%s (supports: %s)", c.BinaryName, strings.Join(c.Platforms, ", ")))
	}
	return fmt.Errorf("plugin %q cannot be installed on %s/%s — unsupported component(s): %s", e.Name, goos, goarch, strings.Join(parts, "; "))
}

// checkMinCLIVersion enforces an entry's minCliVersion against the running CLI.
// A CLI whose version is not a parseable release ("dev" builds) passes with a
// debug note — a developer build must never be blocked from installing.
func checkMinCLIVersion(e *registry.Entry) error {
	if e.MinCLIVersion == "" {
		return nil
	}

	cur, err := semver.NewVersion(version.Version)
	if err != nil {
		logging.Debugf("CLI version %q is not a release version — skipping minCliVersion (%s) check for %s", version.Version, e.MinCLIVersion, e.Name)
		return nil
	}

	minVer, err := semver.NewVersion(e.MinCLIVersion)
	if err != nil {
		return fmt.Errorf("plugin %q declares an invalid minCliVersion %q", e.Name, e.MinCLIVersion)
	}

	if cur.LessThan(minVer) {
		return fmt.Errorf("plugin %q requires Infracost CLI %s or newer, but this CLI is %s — upgrade the CLI to install it", e.Name, e.MinCLIVersion, version.Version)
	}
	return nil
}

// ensureRequiredEntry handles an install request that names a compiled-in
// required plugin. It never records provenance and never creates a durable pin
// — required versions stay controlled by the compiled defaults and
// INFRACOST_CLI_PLUGIN_<KEY>_VERSION. An explicit @<version> is refused because
// the CLI can't honour it; a plain install ensures every missing required
// component is present, leaving already-installed ones untouched.
func (c *Config) ensureRequiredEntry(ctx context.Context, e *registry.Entry, wantVersion string) (*RegistryInstallResult, error) {
	reqs := requiredPluginsForName(e.Name)
	if len(reqs) == 0 {
		return nil, fmt.Errorf("internal error: %q is required but has no components", e.Name)
	}

	if wantVersion != "" {
		return nil, fmt.Errorf("cannot install a specific version of the built-in plugin %q — its version is managed by the CLI; run `infracost plugin update %s`, or set INFRACOST_CLI_PLUGIN_%s_VERSION", e.Name, e.Name, strings.ToUpper(reqs[0].Key))
	}

	dir := c.PluginDir()
	mgr := NewManager(ManagerOptions{Dir: dir, Cache: c.Cache, BaseURL: c.BaseURL, AutoUpdate: true})

	result := &RegistryInstallResult{Entry: e, Required: true}
	for _, r := range reqs {
		path := filepath.Join(dir, pluginBinaryName(r.Name))
		existed := flatPluginBinaryExists(path)

		installedPath, err := mgr.Install(ctx, r.Name, requiredPluginVersion(r.Key))
		if err != nil {
			return nil, err
		}

		comp, _ := e.Component(r.Type)
		ci := ComponentInstall{Component: comp, Path: installedPath}
		if existed {
			result.Current = append(result.Current, ci)
		} else {
			result.Installed = append(result.Installed, ci)
		}
	}
	result.NoOp = len(result.Installed) == 0
	return result, nil
}

// componentStatus classifies one component of an entry against what is on disk.
type componentStatus int

const (
	// statusMissing: no binary present — install it.
	statusMissing componentStatus = iota
	// statusOutdated: a binary is present but at the wrong version — reinstall.
	statusOutdated
	// statusCurrent: a binary is present at the resolved version (or is a dev
	// build) — leave it untouched.
	statusCurrent
)

// registryStager performs staged, all-or-nothing installs of registry entries.
// It layers on top of the existing download/verify/unpack helpers in install.go
// and carries the probing seams so tests can drive the handshake and version
// checks without spawning real plugin subprocesses.
type registryStager struct {
	// cacheDir is where component binaries and the provenance state file live.
	cacheDir string
	// goos/goarch select the artifact platform. Overridable in tests.
	goos, goarch string
	// httpClient performs version resolution and archive downloads.
	httpClient *http.Client
	// probe runs the post-stage handshake against a staged temp binary.
	probe func(path string) probeResult
	// queryInfo reads an installed binary's reported name/version/type.
	queryInfo func(ctx context.Context, path string) (*pb.GetPluginInfoResponse, error)
	// now stamps provenance records; overridable in tests.
	now func() time.Time
}

func (c *Config) newRegistryStager() *registryStager {
	if c.newStager != nil {
		return c.newStager()
	}
	return &registryStager{
		cacheDir:   c.pluginStateDir(),
		goos:       runtime.GOOS,
		goarch:     runtime.GOARCH,
		httpClient: pluginHTTPClient,
		probe:      probePluginBinary,
		queryInfo:  queryPluginInfo,
		now:        time.Now,
	}
}

// install stages and commits every component of a (non-required) registry entry.
func (s *registryStager) install(ctx context.Context, e *registry.Entry, wantVersion string, trust TrustFunc) (*RegistryInstallResult, error) {
	pinned := wantVersion != ""

	resolvedVersion := wantVersion
	if resolvedVersion == "" {
		v, err := e.ResolveVersion(ctx, s.httpClient, s.goos, s.goarch)
		if err != nil {
			return nil, err
		}
		resolvedVersion = v
	}

	state := loadState(s.cacheDir)

	var toInstall []registry.Component
	result := &RegistryInstallResult{Entry: e, Version: resolvedVersion, Pinned: pinned}
	for _, comp := range e.Components {
		status, path, err := s.classify(e, comp, resolvedVersion, state)
		if err != nil {
			return nil, err
		}
		if status == statusCurrent {
			result.Current = append(result.Current, ComponentInstall{Component: comp, Path: path})
			continue
		}
		toInstall = append(toInstall, comp)
	}

	// Everything already current: no prompt, no download, no provenance write.
	if len(toInstall) == 0 {
		result.NoOp = true
		return result, nil
	}

	// Trust gate — only now that a download is genuinely required.
	if trust != nil {
		proceed, err := trust(e)
		if err != nil {
			return nil, err
		}
		if !proceed {
			result.Declined = true
			return result, nil
		}
	}

	// Stage every component to a sibling temp file. Nothing is committed until
	// all components download, verify, unpack, and pass the handshake.
	type stagedComponent struct {
		comp registry.Component
		tmp  string // staged temp path
		dest string // final on-disk path
	}
	var staged []stagedComponent
	cleanup := func() {
		for _, sc := range staged {
			_ = os.Remove(sc.tmp)
		}
	}

	for _, comp := range toInstall {
		var tmp string
		err := ui.RunWithSpinnerErr(ctx,
			fmt.Sprintf("Downloading %s %s (%s)...", e.Name, resolvedVersion, comp.Type),
			fmt.Sprintf("Downloaded %s %s (%s)", e.Name, resolvedVersion, comp.Type),
			func(ctx context.Context) error {
				p, err := s.stage(ctx, e, comp, resolvedVersion)
				if err != nil {
					return err
				}
				tmp = p
				return nil
			})
		if err != nil {
			cleanup()
			return nil, err
		}
		staged = append(staged, stagedComponent{comp: comp, tmp: tmp, dest: s.binaryPath(comp)})

		if err := s.handshake(tmp, e, comp, resolvedVersion); err != nil {
			cleanup()
			return nil, err
		}
	}

	// All staged and verified — commit each into place.
	for _, sc := range staged {
		if err := removeExistingPluginPath(sc.dest); err != nil {
			cleanup()
			return nil, err
		}
		if err := renameWithRetry(sc.tmp, sc.dest); err != nil {
			cleanup()
			return nil, fmt.Errorf("failed to install %s component %s: %w", e.Name, sc.comp.BinaryName, err)
		}
		result.Installed = append(result.Installed, ComponentInstall{Component: sc.comp, Path: sc.dest})
	}

	// Provenance is advisory metadata; a write failure must not fail an
	// otherwise-successful install.
	if err := s.writeProvenance(e, resolvedVersion, pinned); err != nil {
		logging.Warnf("installed %s but failed to record how it was installed: %v", e.Name, err)
	}

	return result, nil
}

// classify inspects the on-disk state of one component against the resolved
// version. A binary already occupying the component's path that this CLI did
// not install (no provenance record and a reported name other than the entry's)
// is a collision and refuses the install.
func (s *registryStager) classify(e *registry.Entry, comp registry.Component, resolvedVersion string, state *State) (componentStatus, string, error) {
	path := s.binaryPath(comp)
	if !flatPluginBinaryExists(path) {
		return statusMissing, path, nil
	}

	info, qErr := s.query(path)

	ours := recordOwnsComponent(state.find(e.Name), comp.BinaryName)
	if !ours && qErr == nil && info.GetName() == e.Name {
		// Installed by an older CLI before provenance existed: it reports our
		// name, so it is ours to manage.
		ours = true
	}
	if !ours {
		return 0, path, fmt.Errorf("cannot install %s: a different plugin binary already exists at %s — remove it first, then re-run the install", e.Name, path)
	}

	if qErr != nil {
		logging.Debugf("could not read installed %s version at %s (%v) — will reinstall", e.Name, path, qErr)
		return statusOutdated, path, nil
	}

	switch info.GetVersion() {
	case devPluginVersion:
		// A locally-built dev binary is never overwritten.
		return statusCurrent, path, nil
	case resolvedVersion:
		return statusCurrent, path, nil
	default:
		return statusOutdated, path, nil
	}
}

// stage downloads, verifies, unpacks, and chmods one component into a sibling
// temp file next to its final destination, returning the temp path. On any
// failure it removes whatever partial temp file it created and returns "".
func (s *registryStager) stage(ctx context.Context, e *registry.Entry, comp registry.Component, resolvedVersion string) (string, error) {
	archiveURL := comp.DownloadURL(resolvedVersion, s.goos, s.goarch)
	checksumURL := comp.ChecksumsURL(resolvedVersion, s.goos, s.goarch)

	sha, err := fetchSHA256(ctx, checksumURL)
	if err != nil {
		return "", s.downloadError(e, comp, resolvedVersion, checksumURL, err)
	}

	archivePath, err := downloadAndVerify(ctx, archiveURL, sha)
	if err != nil {
		return "", s.downloadError(e, comp, resolvedVersion, archiveURL, err)
	}
	defer func() { _ = os.Remove(archivePath) }()

	dest := s.binaryPath(comp)
	if err := os.MkdirAll(filepath.Dir(dest), 0o750); err != nil {
		return "", fmt.Errorf("failed to create plugin directory: %w", err)
	}

	tmp := dest + ".staged"
	if err := s.unpack(archiveURL, archivePath, tmp, comp); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("failed to unpack %s component %s: %w", e.Name, comp.BinaryName, err)
	}

	if err := os.Chmod(tmp, 0o750); err != nil { //nolint:gosec // G302: plugin binary must be executable
		_ = os.Remove(tmp)
		return "", fmt.Errorf("failed to make plugin binary executable: %w", err)
	}

	return tmp, nil
}

// unpack extracts the entry's expected binary from the archive, choosing the
// unpacker by the archive URL suffix.
func (s *registryStager) unpack(archiveURL, archivePath, dest string, comp registry.Component) error {
	entryName := s.binaryFileName(comp)
	switch {
	case strings.HasSuffix(archiveURL, ".zip"):
		return unpackZip(archivePath, dest, entryName)
	case strings.HasSuffix(archiveURL, ".tar.gz"):
		return unpackTarGz(archivePath, dest, entryName)
	default:
		return fmt.Errorf("unsupported archive format in %s", archiveURL)
	}
}

// handshake runs the Phase 3 checklist primitives against a staged temp binary
// and requires its reported identity to match the registry entry/component and
// the resolved version. Any mismatch aborts the whole entry.
func (s *registryStager) handshake(tmp string, e *registry.Entry, comp registry.Component, resolvedVersion string) error {
	pr := s.probe(tmp)
	if pr.handshakeErr != nil {
		return fmt.Errorf("staged %s component of %s failed the plugin handshake: %w", comp.Type, e.Name, pr.handshakeErr)
	}
	if pr.infoErr != nil {
		return fmt.Errorf("staged %s component of %s failed its metadata check: %w", comp.Type, e.Name, pr.infoErr)
	}
	if pr.info == nil {
		return fmt.Errorf("staged %s component of %s returned no plugin info", comp.Type, e.Name)
	}

	if name := pr.info.GetName(); name != e.Name {
		return fmt.Errorf("staged component reports name %q but the registry entry is %q — refusing to install", name, e.Name)
	}

	wantType := componentPluginType(comp.Type)
	if got := pr.info.GetType(); got != wantType {
		return fmt.Errorf("staged component of %s reports type %s but the registry declares it a %s — refusing to install", e.Name, pluginTypeString(got), comp.Type)
	}

	if v := pr.info.GetVersion(); v != resolvedVersion {
		return fmt.Errorf("staged %s component of %s reports version %q but %q was expected — refusing to install", comp.Type, e.Name, v, resolvedVersion)
	}

	return nil
}

// writeProvenance records one entry-level provenance record listing every
// component of the entry at the shared installed version.
func (s *registryStager) writeProvenance(e *registry.Entry, version string, pinned bool) error {
	state := loadState(s.cacheDir)
	comps := make([]StateComponent, 0, len(e.Components))
	for _, c := range e.Components {
		comps = append(comps, StateComponent{Type: c.Type, BinaryName: c.BinaryName})
	}
	state.upsert(StateRecord{
		Name:        e.Name,
		Version:     version,
		Components:  comps,
		Pinned:      pinned,
		Official:    e.Official,
		Author:      e.Author,
		InstalledAt: s.now().UTC(),
	})
	return state.save(s.cacheDir)
}

// query reads an installed binary's reported info, bounded by the shared
// query timeout.
func (s *registryStager) query(path string) (*pb.GetPluginInfoResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), queryPluginInfoTimeout)
	defer cancel()
	return s.queryInfo(ctx, path)
}

// binaryFileName is the on-disk/archive filename for a component on the stager's
// target platform (with a .exe suffix on Windows).
func (s *registryStager) binaryFileName(comp registry.Component) string {
	if s.goos == "windows" {
		return comp.BinaryName + ".exe"
	}
	return comp.BinaryName
}

func (s *registryStager) binaryPath(comp registry.Component) string {
	return filepath.Join(s.cacheDir, s.binaryFileName(comp))
}

// downloadError wraps a fetch/download failure with a clear, version-oriented
// message, keeping the resolved URL behind --debug.
func (s *registryStager) downloadError(e *registry.Entry, comp registry.Component, version, url string, cause error) error {
	logging.Debugf("plugin %q component %q download failed at %s: %v", e.Name, comp.BinaryName, url, cause)
	return fmt.Errorf("failed to download %s component %s at version %s: %w — check that this version exists (run with --debug to see the URL)", e.Name, comp.BinaryName, version, cause)
}

// componentPluginType maps a registry component type string to the plugin
// protobuf enum. Entries are validated installable before reaching here, so the
// type is always parser or provider.
func componentPluginType(typ string) pb.PluginType {
	switch typ {
	case registry.ComponentTypeParser:
		return pb.PluginType_PARSER
	case registry.ComponentTypeProvider:
		return pb.PluginType_PROVIDER
	default:
		return pb.PluginType_PLUGIN_TYPE_UNKNOWN
	}
}

// recordOwnsComponent reports whether a provenance record claims the given
// component binary.
func recordOwnsComponent(rec *StateRecord, binaryName string) bool {
	if rec == nil {
		return false
	}
	for _, c := range rec.Components {
		if c.BinaryName == binaryName {
			return true
		}
	}
	return false
}
