package plugins

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/infracost/cli/pkg/plugins/registry"
)

// Release-mode check identifiers, stable across releases so --json consumers
// (SDK CI pipelines) can key on them. The per-component execution checklist
// reuses the binary-mode identifiers (CheckBinaryFile, CheckHandshake, ...).
const (
	CheckReleaseSchema   = "release-schema"
	CheckReleaseVersion  = "release-version"
	CheckReleaseReach    = "release-reachable"
	CheckReleaseDownload = "release-download"
	CheckReleaseIdentity = "release-identity-match"
)

// sha256HexRE matches a well-formed lowercase-or-uppercase 64-char SHA256 hex
// digest, used to reject a checksum sidecar whose contents are not a digest.
var sha256HexRE = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)

// ReleaseOptions configures a --release validation run.
type ReleaseOptions struct {
	// Version pins an explicit shared release version; empty resolves the
	// entry's latest via its versionUrl.
	Version string
	// AllPlatforms extends the download+verify execution checks from the current
	// platform to every declared platform of every component.
	AllPlatforms bool
}

// ReleaseComponentResult is the release-validation checklist for one component
// of an entry: its per-platform reachability, download+verify, the executed
// binary checklist (current platform), and the identity match.
type ReleaseComponentResult struct {
	Type       string        `json:"type"`
	BinaryName string        `json:"binaryName"`
	Checks     []CheckResult `json:"checks"`
}

// OK reports whether the component passed: no check failed.
func (r ReleaseComponentResult) OK() bool {
	for _, c := range r.Checks {
		if c.Status == CheckFail {
			return false
		}
	}
	return true
}

// ReleaseValidationResult is the full checklist for a validated release entry.
type ReleaseValidationResult struct {
	// Name is the registry entry name.
	Name string `json:"name"`
	// Version is the resolved shared release version (empty if unresolved).
	Version string `json:"version"`
	// Source records where the entry came from: "file" or "registry". Set by
	// the command layer.
	Source string `json:"source,omitempty"`
	// Checks are the entry-level rows (schema, version resolution).
	Checks []CheckResult `json:"checks"`
	// Components are the per-component checklists in declaration order.
	Components []ReleaseComponentResult `json:"components"`
	// Incomplete is true when execution checks that should have run were gated
	// off (an unconfirmed unofficial entry). An incomplete checklist never
	// passes, even when every check that ran passed.
	Incomplete bool `json:"incomplete"`
}

// OK reports whether the release passed: the checklist is complete and no check
// failed. Warnings (e.g. a name collision) do not fail validation.
func (r *ReleaseValidationResult) OK() bool {
	if r.Incomplete {
		return false
	}
	for _, c := range r.Checks {
		if c.Status == CheckFail {
			return false
		}
	}
	for _, comp := range r.Components {
		if !comp.OK() {
			return false
		}
	}
	return true
}

// releaseValidator carries the network and probing seams so tests can drive
// release validation against an httptest server without spawning real plugin
// subprocesses. The zero value is not usable; use newReleaseValidator.
type releaseValidator struct {
	httpClient   *http.Client
	goos, goarch string
	// probe runs the handshake + info + parser-config steps against a staged
	// binary; stat performs the executable-file check. Both mirror the
	// binaryValidator seams.
	probe func(path string) probeResult
	stat  func(path string) error
}

func newReleaseValidator() *releaseValidator {
	return &releaseValidator{
		httpClient: pluginHTTPClient,
		goos:       runtime.GOOS,
		goarch:     runtime.GOARCH,
		probe:      probePluginBinary,
		stat:       statPluginBinary,
	}
}

// ValidateReleaseEntry validates a published release of a registry entry: it
// resolves the shared version, checks every declared component/platform's
// artifact and checksum sidecar for reachability, and download-verifies then
// executes the current-platform components (every platform with AllPlatforms)
// through the full binary checklist, requiring the reported name/type/version
// to match the manifest.
//
// trust gates execution of an unofficial entry and is invoked once, only when
// there is at least one in-scope execution. A (false, nil) return leaves the
// network reachability checks in place but skips the execution checks and marks
// the result incomplete (which never passes). The returned error is non-nil
// only for a catastrophic trust-callback failure; every reachability/execution
// problem is reported inside the result.
func ValidateReleaseEntry(ctx context.Context, e *registry.Entry, opts ReleaseOptions, trust TrustFunc) (*ReleaseValidationResult, error) {
	return newReleaseValidator().validate(ctx, e, opts, trust)
}

func (v *releaseValidator) validate(ctx context.Context, e *registry.Entry, opts ReleaseOptions, trust TrustFunc) (*ReleaseValidationResult, error) {
	res := &ReleaseValidationResult{Name: e.Name}
	addEntry := func(id, name string, status CheckStatus, detail string) {
		res.Checks = append(res.Checks, CheckResult{ID: id, Name: name, Status: status, Detail: detail})
	}

	// The entry reached us already parsed and validated (a local file went
	// through registry.ParseEntry, a name through the manifest's own validation),
	// so the schema check documents that it held.
	addEntry(CheckReleaseSchema, "manifest schema", CheckPass,
		fmt.Sprintf("%s, %s", e.Capabilities(), officialLabel(e)))

	// Resolve the shared version. Without it no artifact URL can be built, so a
	// resolution failure stops the run with an empty component list.
	version := opts.Version
	if version == "" {
		v2, err := e.ResolveVersion(ctx, v.httpClient, v.goos, v.goarch)
		if err != nil {
			addEntry(CheckReleaseVersion, "version resolved", CheckFail, err.Error())
			return res, nil
		}
		version = v2
		addEntry(CheckReleaseVersion, "version resolved", CheckPass, "latest → "+version)
	} else {
		addEntry(CheckReleaseVersion, "version resolved", CheckPass, "pinned "+version)
	}
	res.Version = version

	current := v.goos + "/" + v.goarch

	// The trust gate is consulted once, and only when at least one component has
	// an in-scope execution — a plugin not built for this platform (without
	// --all-platforms) never runs code, so it never prompts.
	execWanted := false
	for _, comp := range e.Components {
		if len(v.downloadScope(comp, opts.AllPlatforms)) > 0 {
			execWanted = true
			break
		}
	}
	executeAllowed := true
	if execWanted && trust != nil {
		proceed, err := trust(e)
		if err != nil {
			return res, err
		}
		executeAllowed = proceed
	}

	for _, comp := range e.Components {
		cr := v.validateComponent(ctx, e, comp, version, opts, current, executeAllowed)
		if cr.gated {
			res.Incomplete = true
		}
		res.Components = append(res.Components, cr.result)
	}

	return res, nil
}

// componentOutcome pairs a component's checklist with whether its execution was
// gated off by an unconfirmed unofficial entry (which makes the whole run
// incomplete).
type componentOutcome struct {
	result ReleaseComponentResult
	gated  bool
}

func (v *releaseValidator) validateComponent(ctx context.Context, e *registry.Entry, comp registry.Component, version string, opts ReleaseOptions, current string, executeAllowed bool) componentOutcome {
	cr := ReleaseComponentResult{Type: comp.Type, BinaryName: comp.BinaryName}
	add := func(id, name string, status CheckStatus, detail string) {
		cr.Checks = append(cr.Checks, CheckResult{ID: id, Name: name, Status: status, Detail: detail})
	}

	// Reachability for every declared platform: the artifact URL responds and
	// the .sha256 sidecar exists with a well-formed hex digest. Each platform is
	// independent so all actionable failures surface.
	for _, plat := range comp.Platforms {
		goos, goarch := splitPlatform(plat)
		artifactURL := comp.DownloadURL(version, goos, goarch)
		checksumURL := comp.ChecksumsURL(version, goos, goarch)

		if err := v.checkReachable(ctx, artifactURL); err != nil {
			add(CheckReleaseReach, "artifact reachable ("+plat+")", CheckFail, err.Error())
			continue
		}
		sha, err := fetchSHA256(ctx, checksumURL)
		if err != nil {
			add(CheckReleaseReach, "artifact reachable ("+plat+")", CheckFail, "checksum sidecar missing: "+err.Error())
			continue
		}
		if !sha256HexRE.MatchString(sha) {
			add(CheckReleaseReach, "artifact reachable ("+plat+")", CheckFail,
				fmt.Sprintf("checksum is not a well-formed sha256 digest: %q", sha))
			continue
		}
		add(CheckReleaseReach, "artifact reachable ("+plat+")", CheckPass, "artifact + .sha256 present")
	}

	// Execution scope: the current platform, or every platform with
	// --all-platforms. A component not built for this platform contributes only
	// reachability checks unless --all-platforms widens the download scope.
	scope := v.downloadScope(comp, opts.AllPlatforms)
	if len(scope) == 0 {
		add(CheckReleaseDownload, "artifact verified", CheckSkip,
			fmt.Sprintf("not built for %s — pass --all-platforms to verify other platforms", current))
		return componentOutcome{result: cr}
	}

	// The trust gate blocked execution of an unofficial entry. Report the
	// execution as skipped and mark the run incomplete; network checks above
	// still stand.
	if !executeAllowed {
		add(CheckReleaseDownload, "artifact verified", CheckSkip,
			"unofficial plugin not confirmed — pass --allow-unofficial to run execution checks")
		return componentOutcome{result: cr, gated: true}
	}

	for _, plat := range scope {
		goos, goarch := splitPlatform(plat)
		v.executeComponent(ctx, &cr, e, comp, version, goos, goarch, current)
	}
	return componentOutcome{result: cr}
}

// executeComponent downloads + SHA256-verifies one component's artifact for a
// concrete platform and, when that platform is the one we are running on,
// extracts the binary and runs the full binary checklist plus an identity match
// against the manifest. A cross-platform artifact (only reachable under
// --all-platforms) is verified but cannot be run, so its execution is skipped.
func (v *releaseValidator) executeComponent(ctx context.Context, cr *ReleaseComponentResult, e *registry.Entry, comp registry.Component, version, goos, goarch, current string) {
	plat := goos + "/" + goarch
	add := func(id, name string, status CheckStatus, detail string) {
		cr.Checks = append(cr.Checks, CheckResult{ID: id, Name: name + " (" + plat + ")", Status: status, Detail: detail})
	}

	archiveURL := comp.DownloadURL(version, goos, goarch)
	checksumURL := comp.ChecksumsURL(version, goos, goarch)

	sha, err := fetchSHA256(ctx, checksumURL)
	if err != nil {
		add(CheckReleaseDownload, "artifact verified", CheckFail, "failed to fetch checksum: "+err.Error())
		return
	}
	archivePath, err := downloadAndVerify(ctx, archiveURL, sha)
	if err != nil {
		// downloadAndVerify reports both digests on a SHA256 mismatch.
		add(CheckReleaseDownload, "artifact verified", CheckFail, err.Error())
		return
	}
	defer func() { _ = os.Remove(archivePath) }()
	add(CheckReleaseDownload, "artifact verified", CheckPass, "sha256 matches "+shortSHA(sha))

	// Only a binary built for the platform we are running on can actually be
	// executed; a cross-platform artifact is verified but not run.
	if plat != current {
		add(CheckMetadata, "binary execution", CheckSkip, "cannot run a "+plat+" binary on "+current)
		return
	}

	tmpDir, err := os.MkdirTemp("", "infracost-validate-release-*")
	if err != nil {
		add(CheckReleaseDownload, "extract", CheckFail, err.Error())
		return
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	binPath := filepath.Join(tmpDir, v.binaryFileName(comp))
	if err := v.unpack(archiveURL, archivePath, binPath, comp); err != nil {
		add(CheckReleaseDownload, "extract", CheckFail, err.Error())
		return
	}
	if err := os.Chmod(binPath, 0o750); err != nil { //nolint:gosec // G302: plugin binary must be executable
		add(CheckReleaseDownload, "extract", CheckFail, err.Error())
		return
	}

	// Run the full binary checklist, capturing the probe result so the reported
	// version is available for the identity match without probing twice.
	var captured probeResult
	bv := &binaryValidator{
		stat: v.stat,
		probe: func(p string) probeResult {
			captured = v.probe(p)
			return captured
		},
	}
	checklist, identity := bv.check(binPath)
	cr.Checks = append(cr.Checks, checklist.Checks...)

	v.appendIdentityMatch(cr, e, comp, version, identity, captured)
}

// appendIdentityMatch adds the release-identity check: the executed binary's
// reported name/type/version must match the manifest entry, component type, and
// resolved shared version. When the binary never reported valid metadata the
// checklist already recorded that failure, so the match is skipped.
func (v *releaseValidator) appendIdentityMatch(cr *ReleaseComponentResult, e *registry.Entry, comp registry.Component, version string, identity *pluginIdentity, pr probeResult) {
	if identity == nil {
		cr.Checks = append(cr.Checks, CheckResult{
			ID: CheckReleaseIdentity, Name: "identity matches manifest", Status: CheckSkip,
			Detail: "skipped: the binary did not report valid metadata",
		})
		return
	}

	var problems []string
	if identity.name != e.Name {
		problems = append(problems, fmt.Sprintf("reports name %q, manifest is %q", identity.name, e.Name))
	}
	wantType := componentPluginType(comp.Type)
	if identity.typ != wantType {
		problems = append(problems, fmt.Sprintf("reports type %s, manifest declares %s", pluginTypeString(identity.typ), comp.Type))
	}
	if got := pr.info.GetVersion(); got != version {
		problems = append(problems, fmt.Sprintf("reports version %q, expected %q", got, version))
	}

	if len(problems) > 0 {
		cr.Checks = append(cr.Checks, CheckResult{
			ID: CheckReleaseIdentity, Name: "identity matches manifest", Status: CheckFail,
			Detail: strings.Join(problems, "; "),
		})
		return
	}
	cr.Checks = append(cr.Checks, CheckResult{
		ID: CheckReleaseIdentity, Name: "identity matches manifest", Status: CheckPass,
		Detail: fmt.Sprintf("name/type/version match (%s)", version),
	})
}

// downloadScope returns the platforms whose artifacts are download-verified for
// a component: every declared platform with AllPlatforms, otherwise the current
// platform when the component supports it (else none).
func (v *releaseValidator) downloadScope(comp registry.Component, allPlatforms bool) []string {
	if allPlatforms {
		return comp.Platforms
	}
	if comp.SupportsPlatform(v.goos, v.goarch) {
		return []string{v.goos + "/" + v.goarch}
	}
	return nil
}

// checkReachable verifies url responds, using a HEAD request and falling back to
// a ranged, aborted GET (Range: bytes=0-0) when the host rejects HEAD (405/501/
// 403) or HEAD errors. Returns nil when reachable (any 2xx), else an error.
func (v *releaseValidator) checkReachable(ctx context.Context, url string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil) //nolint:gosec // G107: URL comes from the trusted registry manifest
	if err != nil {
		return err
	}
	resp, err := v.httpClient.Do(req) //nolint:gosec // G704: request originates from the registry manifest
	if err == nil {
		_ = resp.Body.Close()
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return nil
		}
		if !headRejected(resp.StatusCode) {
			return fmt.Errorf("unexpected HTTP status %s", resp.Status)
		}
		// A rejected HEAD is not conclusive — fall through to a ranged GET.
	}

	greq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil) //nolint:gosec // G107: URL comes from the trusted registry manifest
	if err != nil {
		return err
	}
	greq.Header.Set("Range", "bytes=0-0")
	gresp, gerr := v.httpClient.Do(greq) //nolint:gosec // G704: request originates from the registry manifest
	if gerr != nil {
		return gerr
	}
	defer func() { _ = gresp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(gresp.Body, 1))
	if gresp.StatusCode >= 200 && gresp.StatusCode < 300 {
		return nil
	}
	return fmt.Errorf("unexpected HTTP status %s", gresp.Status)
}

// headRejected reports whether a HEAD status means the host declines HEAD rather
// than that the artifact is absent, so reachability should retry with a GET.
func headRejected(code int) bool {
	switch code {
	case http.StatusMethodNotAllowed, http.StatusNotImplemented, http.StatusForbidden:
		return true
	default:
		return false
	}
}

// unpack extracts the component's expected binary from the archive, choosing the
// unpacker by the archive URL suffix. It mirrors registryStager.unpack.
func (v *releaseValidator) unpack(archiveURL, archivePath, dest string, comp registry.Component) error {
	entryName := v.binaryFileName(comp)
	switch {
	case strings.HasSuffix(archiveURL, ".zip"):
		return unpackZip(archivePath, dest, entryName)
	case strings.HasSuffix(archiveURL, ".tar.gz"):
		return unpackTarGz(archivePath, dest, entryName)
	default:
		return fmt.Errorf("unsupported archive format in %s", archiveURL)
	}
}

// binaryFileName is the on-disk/archive filename for a component on the
// validator's platform (with a .exe suffix on Windows). Extraction only runs
// for the current platform, so v.goos is the right suffix source.
func (v *releaseValidator) binaryFileName(comp registry.Component) string {
	if v.goos == "windows" {
		return comp.BinaryName + ".exe"
	}
	return comp.BinaryName
}

// splitPlatform splits a "goos/goarch" pair. The manifest validation guarantees
// the two-segment shape, so the fallback is defensive only.
func splitPlatform(p string) (goos, goarch string) {
	goos, goarch, _ = strings.Cut(p, "/")
	return goos, goarch
}

// officialLabel renders an entry's official flag for the schema check detail.
func officialLabel(e *registry.Entry) string {
	if e.Official {
		return "official"
	}
	return "unofficial"
}

// shortSHA truncates a digest for display in a passing check detail.
func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12] + "…"
	}
	return sha
}
