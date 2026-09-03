package plugins

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/infracost/cli/pkg/plugins/registry"
)

// packageEpoch is the fixed timestamp stamped into every archive entry and gzip
// header so identical inputs produce byte-identical archives (and thus identical
// checksums) regardless of when packaging runs. The Unix epoch is representable
// in the USTAR format without PAX extensions.
var packageEpoch = time.Unix(0, 0).UTC()

// PackageBuild is one built binary for a specific component and platform.
type PackageBuild struct {
	GOOS   string
	GOARCH string
	// Path is the on-disk path to the built binary.
	Path string
}

// platform renders the build's goos/goarch pair.
func (b PackageBuild) platform() string { return b.GOOS + "/" + b.GOARCH }

// PackageComponentInput describes one component (parser or provider) to package,
// with its on-disk binary name and one build per target platform.
type PackageComponentInput struct {
	// Type is "parser" or "provider".
	Type string
	// BinaryName is the component's on-disk/archive binary name, including the
	// parser/provider prefix (e.g. "infracost-parser-acme").
	BinaryName string
	// Builds are the per-platform built binaries.
	Builds []PackageBuild
}

// PackageOptions configures a PackageRelease run.
type PackageOptions struct {
	// Name is the registry entry name in <github-owner>/<github-repository> form.
	Name string
	// Version pins the shared release version. When empty it is read from the
	// current-platform binaries (which must agree).
	Version string
	// OutDir is the output root (default "dist" is applied when empty).
	OutDir string
	// BaseURL is the hosting root used to build the download/checksums templates.
	BaseURL string
	// Flat names artifacts with entry/type/version/os/arch in a single namespace
	// (for hosts like GitHub Releases) instead of the nested per-binary layout.
	Flat bool
	// Force overwrites existing artifacts for the same component/version.
	Force bool
	// Components are the components to package (1 or 2).
	Components []PackageComponentInput

	// Optional manifest metadata. Sensible defaults are derived from Name.
	DisplayName   string
	Description   string
	Author        string
	Homepage      string
	License       string
	MinCLIVersion string

	// goos/goarch identify the current platform; overridable in tests. Empty
	// values default to runtime.GOOS/GOARCH.
	goos, goarch string
	// stat/probe are the validation seams, mirroring binaryValidator. Nil values
	// default to the real implementations.
	stat  func(path string) error
	probe func(path string) probeResult
}

// PackagedArtifact records one emitted archive and its checksum sidecar.
type PackagedArtifact struct {
	Type        string
	BinaryName  string
	GOOS        string
	GOARCH      string
	ArchivePath string
	SHA256Path  string
	SHA256      string
}

// PackageResult describes what PackageRelease produced.
type PackageResult struct {
	// Entry is the generated, schema-validated manifest entry.
	Entry *registry.Entry
	// Version is the shared release version every artifact was packaged at.
	Version string
	// ManifestPath is the written manifest-entry.json path.
	ManifestPath string
	// Artifacts are the emitted archives + checksums, in component/platform order.
	Artifacts []PackagedArtifact
	// VersionFiles are the emitted shared latest-version files.
	VersionFiles []string
	// StaticOnly names the components that had no current-platform binary and so
	// were checked statically only (runtime validation skipped).
	StaticOnly []string
}

// PackageValidationError is returned when a current-platform component fails its
// pre-package binary checklist. It carries the failing checklists so the command
// layer can render them the same way `plugin validate` does.
type PackageValidationError struct {
	Results []ValidationResult
}

func (e *PackageValidationError) Error() string {
	return "one or more components failed pre-package validation"
}

// PackageRelease turns a repository's parser and/or provider builds into a
// release: one deterministic archive + .sha256 per component/platform, a shared
// latest-version file, and a schema-validated manifest-entry.json. Every
// current-platform component is run through the binary validation checklist and
// must report the entry name, its declared type, and the shared version;
// cross-compiled binaries get static checks only. The generated layout is what
// `plugin install` and `plugin validate --release` consume.
//
// Every archive is emitted as a deterministic data.tar.gz — including Windows,
// whose binary simply carries its .exe suffix inside the archive. A single
// registry download template has only {version}/{os}/{arch} placeholders and so
// cannot encode a per-OS archive extension; a uniform tar.gz keeps the generated
// entry installable and its round-trip through validate/install byte-stable.
func PackageRelease(ctx context.Context, opts PackageOptions) (*PackageResult, error) {
	p, err := newPackager(opts)
	if err != nil {
		return nil, err
	}
	return p.run(ctx)
}

// packager holds the validated, defaulted options for one packaging run.
type packager struct {
	opts   PackageOptions
	goos   string
	goarch string
	stat   func(path string) error
	probe  func(path string) probeResult
}

func newPackager(opts PackageOptions) (*packager, error) {
	if opts.goos == "" {
		opts.goos = runtime.GOOS
	}
	if opts.goarch == "" {
		opts.goarch = runtime.GOARCH
	}
	if opts.stat == nil {
		opts.stat = statPluginBinary
	}
	if opts.probe == nil {
		opts.probe = probePluginBinary
	}
	if opts.OutDir == "" {
		opts.OutDir = "dist"
	}
	if err := validatePackageInputs(&opts); err != nil {
		return nil, err
	}
	return &packager{opts: opts, goos: opts.goos, goarch: opts.goarch, stat: opts.stat, probe: opts.probe}, nil
}

// validatePackageInputs enforces the structural rules that don't need the
// binaries themselves: a namespaced name, 1–2 components, at least one build per
// component, no duplicate component types or binary names, and a base URL.
func validatePackageInputs(opts *PackageOptions) error {
	if !registry.ValidEntryName(opts.Name) {
		return fmt.Errorf("invalid --name %q: names must be namespaced as <github-owner>/<github-repository>", opts.Name)
	}
	if opts.BaseURL == "" {
		return fmt.Errorf("a --base-url is required to build the manifest entry's download templates")
	}
	switch {
	case len(opts.Components) == 0:
		return fmt.Errorf("no components to package: provide at least one parser or provider build")
	case len(opts.Components) > 2:
		return fmt.Errorf("an entry may declare at most one parser and one provider (got %d components)", len(opts.Components))
	}

	seenTypes := map[string]struct{}{}
	seenBinaries := map[string]struct{}{}
	for _, c := range opts.Components {
		switch c.Type {
		case registry.ComponentTypeParser, registry.ComponentTypeProvider:
		default:
			return fmt.Errorf("component %q has unsupported type %q (want parser or provider)", c.BinaryName, c.Type)
		}
		if _, dup := seenTypes[c.Type]; dup {
			return fmt.Errorf("more than one %s component was provided; an entry may declare only one", c.Type)
		}
		seenTypes[c.Type] = struct{}{}

		if c.BinaryName == "" {
			return fmt.Errorf("a %s component was provided without a binary name", c.Type)
		}
		if err := validateOutputPathSegment("component binaryName", c.BinaryName); err != nil {
			return err
		}
		if _, dup := seenBinaries[c.BinaryName]; dup {
			return fmt.Errorf("duplicate component binaryName %q", c.BinaryName)
		}
		seenBinaries[c.BinaryName] = struct{}{}

		if len(c.Builds) == 0 {
			return fmt.Errorf("component %q has no platform builds", c.BinaryName)
		}
		seenPlat := map[string]struct{}{}
		for _, b := range c.Builds {
			if b.GOOS == "" || b.GOARCH == "" {
				return fmt.Errorf("component %q has a build with an empty goos/goarch", c.BinaryName)
			}
			if err := validateOutputPathSegment("goos", b.GOOS); err != nil {
				return fmt.Errorf("component %q: %w", c.BinaryName, err)
			}
			if err := validateOutputPathSegment("goarch", b.GOARCH); err != nil {
				return fmt.Errorf("component %q: %w", c.BinaryName, err)
			}
			if _, dup := seenPlat[b.platform()]; dup {
				return fmt.Errorf("component %q has two builds for %s", c.BinaryName, b.platform())
			}
			seenPlat[b.platform()] = struct{}{}
		}
	}
	return nil
}

func (p *packager) run(ctx context.Context) (*PackageResult, error) {
	version, static, err := p.resolveVersionAndValidate()
	if err != nil {
		return nil, err
	}
	if err := validateOutputPathSegment("version", version); err != nil {
		return nil, err
	}

	entry, err := p.buildEntry()
	if err != nil {
		return nil, err
	}

	result := &PackageResult{Entry: entry, Version: version, StaticOnly: static}

	// Refuse an --out that already holds artifacts for the same component/version
	// unless --force, so a re-run can't silently mix builds.
	if !p.opts.Force {
		if err := p.checkNoExistingArtifacts(version); err != nil {
			return nil, err
		}
	}

	for _, c := range p.opts.Components {
		for _, b := range c.Builds {
			art, err := p.emitArchive(c, b, version)
			if err != nil {
				return nil, err
			}
			result.Artifacts = append(result.Artifacts, art)
		}
	}

	versionFiles, err := p.emitVersionFiles(version)
	if err != nil {
		return nil, err
	}
	result.VersionFiles = versionFiles

	manifestPath, err := p.writeManifest(entry)
	if err != nil {
		return nil, err
	}
	result.ManifestPath = manifestPath

	_ = ctx
	return result, nil
}

// versionReport pairs a component with the version its current-platform binary
// reported, for reconcileVersion.
type versionReport struct {
	component string
	version   string
}

// resolveVersionAndValidate runs the pre-package checks: current-platform
// components go through the full binary checklist (name/type/version must match),
// cross-compiled builds get static checks. It returns the shared version and the
// names of components validated statically only.
func (p *packager) resolveVersionAndValidate() (string, []string, error) {
	current := p.goos + "/" + p.goarch

	// Static checks for every cross-compiled build. These never read the version;
	// they guard obviously-broken inputs (missing file, empty file, wrong Windows
	// suffix) before we spend time on the runtime checklist.
	for _, c := range p.opts.Components {
		for _, b := range c.Builds {
			if b.platform() == current {
				continue
			}
			if err := staticBinaryCheck(c.BinaryName, b); err != nil {
				return "", nil, err
			}
		}
	}

	// Runtime checklist for the current-platform build of each component. Collect
	// checklist failures across all components so every problem surfaces at once;
	// identity/version mismatches are hard errors returned immediately.
	var (
		reports  []versionReport
		failures []ValidationResult
		static   []string
	)
	for _, c := range p.opts.Components {
		b, ok := currentBuild(c, current)
		if !ok {
			static = append(static, c.BinaryName)
			continue
		}
		res, identity, ver := p.runtimeCheck(b.Path)
		if !res.OK() || identity == nil {
			failures = append(failures, res)
			continue
		}
		if err := p.matchesEntry(c, identity, ver); err != nil {
			return "", nil, err
		}
		reports = append(reports, versionReport{component: c.BinaryName, version: ver})
	}
	if len(failures) > 0 {
		return "", nil, &PackageValidationError{Results: failures}
	}

	version, err := p.reconcileVersion(reports)
	if err != nil {
		return "", nil, err
	}
	return version, static, nil
}

// currentBuild returns the build matching the current platform, if any.
func currentBuild(c PackageComponentInput, current string) (PackageBuild, bool) {
	for _, b := range c.Builds {
		if b.platform() == current {
			return b, true
		}
	}
	return PackageBuild{}, false
}

// matchesEntry verifies a current-platform binary reports the entry name, its
// declared component type, and a non-empty version.
func (p *packager) matchesEntry(c PackageComponentInput, identity *pluginIdentity, ver string) error {
	if identity.name != p.opts.Name {
		return fmt.Errorf("component %q reports name %q but --name is %q — install would reject this release", c.BinaryName, identity.name, p.opts.Name)
	}
	want := componentPluginType(c.Type)
	if identity.typ != want {
		return fmt.Errorf("component %q reports type %s but was provided as a %s", c.BinaryName, pluginTypeString(identity.typ), c.Type)
	}
	if ver == "" {
		return fmt.Errorf("component %q reported an empty version", c.BinaryName)
	}
	return nil
}

// runtimeCheck runs the binary checklist against path, capturing the reported
// version alongside the checklist and identity.
func (p *packager) runtimeCheck(path string) (ValidationResult, *pluginIdentity, string) {
	var captured probeResult
	bv := &binaryValidator{
		stat: p.stat,
		probe: func(pp string) probeResult {
			captured = p.probe(pp)
			return captured
		},
	}
	res, identity := bv.check(path)
	version := ""
	if captured.info != nil {
		version = captured.info.GetVersion()
	}
	return res, identity, version
}

// reconcileVersion determines the shared release version from the versions the
// current-platform binaries reported and the optional --version flag. A
// disagreement between components, or between a component and the flag, is a hard
// error. When no current-platform binary reported a version, --version is
// required.
func (p *packager) reconcileVersion(reported []versionReport) (string, error) {
	var agreed string
	for _, r := range reported {
		if agreed == "" {
			agreed = r.version
			continue
		}
		if r.version != agreed {
			return "", fmt.Errorf("components disagree on the release version: %s reports %q, but another component reports %q — package one shared version per release", r.component, r.version, agreed)
		}
	}

	if p.opts.Version != "" {
		for _, r := range reported {
			if r.version != p.opts.Version {
				return "", fmt.Errorf("component %s reports version %q but --version is %q — they must match", r.component, r.version, p.opts.Version)
			}
		}
		return p.opts.Version, nil
	}

	if agreed == "" {
		return "", fmt.Errorf("no current-platform binary was provided to read the version from — pass --version to set the shared release version")
	}
	return agreed, nil
}

// buildEntry constructs and schema-validates the manifest entry describing this
// release. official is always false.
func (p *packager) buildEntry() (*registry.Entry, error) {
	owner, repo := splitEntryName(p.opts.Name)

	displayName := p.opts.DisplayName
	if displayName == "" {
		displayName = repo
	}
	author := p.opts.Author
	if author == "" {
		author = owner
	}
	homepage := p.opts.Homepage
	if homepage == "" {
		homepage = "https://github.com/" + p.opts.Name
	}

	components := make([]registry.Component, 0, len(p.opts.Components))
	for _, c := range p.opts.Components {
		components = append(components, registry.Component{
			Type:       c.Type,
			BinaryName: c.BinaryName,
			Platforms:  p.componentPlatforms(c),
			Download:   p.downloadTemplate(c),
			Checksums:  p.checksumsTemplate(c),
		})
	}

	entry := registry.Entry{
		Name:          p.opts.Name,
		DisplayName:   displayName,
		Description:   p.opts.Description,
		Author:        author,
		Official:      false,
		Homepage:      homepage,
		License:       p.opts.License,
		VersionURL:    p.versionTemplate(),
		MinCLIVersion: p.opts.MinCLIVersion,
		Components:    components,
	}

	// Round-trip through the manifest validator so a violation is caught here,
	// before anything is written, rather than by the registry PR CI later.
	data, err := json.Marshal(entry)
	if err != nil {
		return nil, fmt.Errorf("failed to encode manifest entry: %w", err)
	}
	validated, err := registry.ParseEntry(data)
	if err != nil {
		return nil, fmt.Errorf("generated manifest entry is invalid: %w", err)
	}
	return validated, nil
}

// componentPlatforms lists a component's build platforms in sorted order for a
// stable manifest.
func (p *packager) componentPlatforms(c PackageComponentInput) []string {
	plats := make([]string, 0, len(c.Builds))
	for _, b := range c.Builds {
		plats = append(plats, b.platform())
	}
	sort.Strings(plats)
	return plats
}

// emitArchive writes one component/platform archive and its checksum sidecar in
// the chosen layout, returning the artifact record.
func (p *packager) emitArchive(c PackageComponentInput, b PackageBuild, version string) (PackagedArtifact, error) {
	content, err := os.ReadFile(b.Path) //nolint:gosec // G304: path is a user-supplied build artifact to package
	if err != nil {
		return PackagedArtifact{}, fmt.Errorf("failed to read %s build %s: %w", c.BinaryName, b.platform(), err)
	}

	archiveBytes, err := buildDeterministicTarGz(binaryFileNameFor(c.BinaryName, b.GOOS), content)
	if err != nil {
		return PackagedArtifact{}, fmt.Errorf("failed to build archive for %s %s: %w", c.BinaryName, b.platform(), err)
	}

	sum := sha256.Sum256(archiveBytes)
	shaHex := hex.EncodeToString(sum[:])

	archivePath := p.archivePath(c, b, version)
	if err := os.MkdirAll(filepath.Dir(archivePath), 0o750); err != nil {
		return PackagedArtifact{}, fmt.Errorf("failed to create output directory: %w", err)
	}
	if err := os.WriteFile(archivePath, archiveBytes, 0o600); err != nil { //nolint:gosec // G703: every dynamic path segment is validated before emission.
		return PackagedArtifact{}, fmt.Errorf("failed to write archive %s: %w", archivePath, err)
	}

	shaPath := archivePath + ".sha256"
	shaBody := fmt.Sprintf("%s  %s\n", shaHex, filepath.Base(archivePath))
	if err := os.WriteFile(shaPath, []byte(shaBody), 0o600); err != nil { //nolint:gosec // G703: derived only by appending a fixed suffix to archivePath.
		return PackagedArtifact{}, fmt.Errorf("failed to write checksum %s: %w", shaPath, err)
	}

	return PackagedArtifact{
		Type:        c.Type,
		BinaryName:  c.BinaryName,
		GOOS:        b.GOOS,
		GOARCH:      b.GOARCH,
		ArchivePath: archivePath,
		SHA256Path:  shaPath,
		SHA256:      shaHex,
	}, nil
}

// emitVersionFiles writes the shared latest-version file(s) at the path(s) the
// generated versionUrl resolves to.
func (p *packager) emitVersionFiles(version string) ([]string, error) {
	body := []byte(version + "\n")
	var out []string
	for _, path := range p.versionFilePaths() {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			return nil, fmt.Errorf("failed to create output directory: %w", err)
		}
		if err := os.WriteFile(path, body, 0o600); err != nil {
			return nil, fmt.Errorf("failed to write version file %s: %w", path, err)
		}
		out = append(out, path)
	}
	return out, nil
}

// writeManifest writes the manifest-entry.json for the entry.
func (p *packager) writeManifest(entry *registry.Entry) (string, error) {
	data, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to encode manifest entry: %w", err)
	}
	data = append(data, '\n')
	path := filepath.Join(p.opts.OutDir, "manifest-entry.json")
	if err := os.MkdirAll(p.opts.OutDir, 0o750); err != nil {
		return "", fmt.Errorf("failed to create output directory: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", fmt.Errorf("failed to write manifest entry: %w", err)
	}
	return path, nil
}

// checkNoExistingArtifacts refuses when any archive this run would write already
// exists, so a repackage can't silently mix builds without --force.
func (p *packager) checkNoExistingArtifacts(version string) error {
	for _, c := range p.opts.Components {
		for _, b := range c.Builds {
			path := p.archivePath(c, b, version)
			if _, err := os.Stat(path); err == nil {
				return fmt.Errorf("output already contains %s — pass --force to overwrite artifacts for the same component and version", path)
			}
		}
	}
	return nil
}

// --- layout & templates -----------------------------------------------------

// baseURL returns the hosting root without a trailing slash.
func (p *packager) baseURL() string { return strings.TrimRight(p.opts.BaseURL, "/") }

// entrySlug is the flat-namespace prefix "<owner>-<repo>".
func (p *packager) entrySlug() string {
	owner, repo := splitEntryName(p.opts.Name)
	return owner + "-" + repo
}

// versionComponent is the component whose binaryName anchors the shared
// versionUrl template: the parser if present (matching the required-plugin
// convention), otherwise the first declared component.
func (p *packager) versionComponent() PackageComponentInput {
	for _, c := range p.opts.Components {
		if c.Type == registry.ComponentTypeParser {
			return c
		}
	}
	return p.opts.Components[0]
}

// downloadTemplate builds a component's {version}/{os}/{arch} archive template.
func (p *packager) downloadTemplate(c PackageComponentInput) string {
	if p.opts.Flat {
		return fmt.Sprintf("%s/%s-%s-{version}-{os}-{arch}.tar.gz", p.baseURL(), p.entrySlug(), c.Type)
	}
	return fmt.Sprintf("%s/%s/{os}/{arch}/{version}/data.tar.gz", p.baseURL(), c.BinaryName)
}

// checksumsTemplate is the download template's .sha256 sidecar.
func (p *packager) checksumsTemplate(c PackageComponentInput) string {
	return p.downloadTemplate(c) + ".sha256"
}

// versionTemplate builds the entry's shared versionUrl. In the nested layout it
// carries {os}/{arch} (one version file per platform of the anchor component);
// in the flat layout it is a single per-entry file.
func (p *packager) versionTemplate() string {
	if p.opts.Flat {
		return fmt.Sprintf("%s/%s-latest.version", p.baseURL(), p.entrySlug())
	}
	return fmt.Sprintf("%s/%s/{os}/{arch}/latest/version", p.baseURL(), p.versionComponent().BinaryName)
}

// archivePath is the on-disk output path for one component/platform archive,
// mirroring the URL the download template resolves to.
func (p *packager) archivePath(c PackageComponentInput, b PackageBuild, version string) string {
	if p.opts.Flat {
		name := fmt.Sprintf("%s-%s-%s-%s-%s.tar.gz", p.entrySlug(), c.Type, version, b.GOOS, b.GOARCH)
		return filepath.Join(p.opts.OutDir, name)
	}
	return filepath.Join(p.opts.OutDir, c.BinaryName, b.GOOS, b.GOARCH, version, "data.tar.gz")
}

// versionFilePaths lists the on-disk version-file paths the versionUrl resolves
// to. Flat: one shared file. Nested: one per platform of the anchor component.
func (p *packager) versionFilePaths() []string {
	if p.opts.Flat {
		return []string{filepath.Join(p.opts.OutDir, fmt.Sprintf("%s-latest.version", p.entrySlug()))}
	}
	vc := p.versionComponent()
	seen := map[string]struct{}{}
	var out []string
	for _, b := range vc.Builds {
		path := filepath.Join(p.opts.OutDir, vc.BinaryName, b.GOOS, b.GOARCH, "latest", "version")
		if _, dup := seen[path]; dup {
			continue
		}
		seen[path] = struct{}{}
		out = append(out, path)
	}
	return out
}

// --- helpers ----------------------------------------------------------------

// validateOutputPathSegment rejects values that could escape or reshape the
// output tree. filepath.Join treats absolute paths and dot segments specially,
// and backslashes are separators on Windows even when packaging is invoked on
// another platform.
func validateOutputPathSegment(label, value string) error {
	if value == "" || value == "." || value == ".." ||
		strings.ContainsAny(value, `/\\`) || filepath.IsAbs(value) {
		return fmt.Errorf("invalid %s %q: must be a single path segment", label, value)
	}
	return nil
}

// staticBinaryCheck performs the cross-compiled binary checks: the file exists,
// is non-empty, and (for a Windows target) carries the .exe suffix its archive
// entry will require.
func staticBinaryCheck(binaryName string, b PackageBuild) error {
	info, err := os.Stat(b.Path)
	if err != nil {
		return fmt.Errorf("component %q build for %s: %w", binaryName, b.platform(), err)
	}
	if info.IsDir() {
		return fmt.Errorf("component %q build for %s is a directory, not a binary: %s", binaryName, b.platform(), b.Path)
	}
	if info.Size() == 0 {
		return fmt.Errorf("component %q build for %s is empty: %s", binaryName, b.platform(), b.Path)
	}
	if b.GOOS == "windows" && !strings.HasSuffix(strings.ToLower(b.Path), ".exe") {
		return fmt.Errorf("component %q Windows build must be named with a .exe suffix, got %s", binaryName, b.Path)
	}
	return nil
}

// binaryFileNameFor is the archive entry name for a component on a target goos
// (with a .exe suffix on Windows).
func binaryFileNameFor(binaryName, goos string) string {
	if goos == "windows" {
		return binaryName + ".exe"
	}
	return binaryName
}

// splitEntryName splits a validated <owner>/<repo> name into its parts.
func splitEntryName(name string) (owner, repo string) {
	owner, repo, _ = strings.Cut(name, "/")
	return owner, repo
}

// binaryNameParserPrefix / binaryNameProviderPrefix are the conventional
// type-namespacing prefixes for plugin binary names (see pkg/plugins/required.go).
const (
	binaryNameParserPrefix   = "infracost-parser-"
	binaryNameProviderPrefix = "infracost-provider-"
)

// ComponentTypeForBinaryName derives a component type from the conventional
// binary-name prefix (infracost-parser-* → parser, infracost-provider-* →
// provider). ok is false for a name that follows neither convention, letting the
// caller ask for an explicit --binary type instead of guessing.
func ComponentTypeForBinaryName(binaryName string) (typ string, ok bool) {
	switch {
	case strings.HasPrefix(binaryName, binaryNameParserPrefix):
		return registry.ComponentTypeParser, true
	case strings.HasPrefix(binaryName, binaryNameProviderPrefix):
		return registry.ComponentTypeProvider, true
	}
	return "", false
}

// buildDeterministicTarGz packs a single executable named entryName into a
// gzip-compressed tar with every non-content field pinned (epoch mtime, uid/gid
// 0, empty uname/gname, 0755 mode, USTAR format, no gzip name/mtime) so the same
// input always yields byte-identical output.
func buildDeterministicTarGz(entryName string, content []byte) ([]byte, error) {
	var buf bytes.Buffer

	gw, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		return nil, err
	}
	// Leave gw.Header zero-valued: no stored name, no OS byte drift, mtime 0.

	tw := tar.NewWriter(gw)
	hdr := &tar.Header{
		Name:     entryName,
		Mode:     0o755,
		Size:     int64(len(content)),
		ModTime:  packageEpoch,
		Uid:      0,
		Gid:      0,
		Uname:    "",
		Gname:    "",
		Typeflag: tar.TypeReg,
		Format:   tar.FormatUSTAR,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return nil, err
	}
	if _, err := tw.Write(content); err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
