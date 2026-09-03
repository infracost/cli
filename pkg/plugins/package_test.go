package plugins

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/infracost/cli/pkg/plugins/registry"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeBinary writes fake build content to dir/name and returns its path.
func writeBinary(t *testing.T, dir, name string, content []byte) string {
	t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, content, 0o600))
	return path
}

// packageProbes builds a probe seam that returns the given probeResult per path,
// erroring for any unstubbed path so a mis-wired test fails loudly.
func packageProbes(probes map[string]probeResult) func(string) probeResult {
	return func(path string) probeResult {
		if pr, ok := probes[path]; ok {
			return pr
		}
		return probeResult{}
	}
}

// baseOpts returns options with the seams wired and the platform pinned to
// linux/amd64 so layout assertions are host-independent.
func baseOpts(probes map[string]probeResult) PackageOptions {
	return PackageOptions{
		Name:    "acme/tf",
		BaseURL: "https://example.test/plugins",
		goos:    "linux",
		goarch:  "amd64",
		stat:    func(string) error { return nil },
		probe:   packageProbes(probes),
	}
}

func TestPackageReleaseNestedDualComponent(t *testing.T) {
	build := t.TempDir()
	out := filepath.Join(t.TempDir(), "dist")

	parserPath := writeBinary(t, build, "infracost-parser-acme", []byte("parser-linux"))
	providerPath := writeBinary(t, build, "infracost-provider-acme", []byte("provider-linux"))

	opts := baseOpts(map[string]probeResult{
		parserPath:   parserProbe("acme/tf", "1.4.0"),
		providerPath: providerProbe("acme/tf", "1.4.0"),
	})
	opts.OutDir = out
	opts.Components = []PackageComponentInput{
		{Type: "parser", BinaryName: "infracost-parser-acme", Builds: []PackageBuild{{GOOS: "linux", GOARCH: "amd64", Path: parserPath}}},
		{Type: "provider", BinaryName: "infracost-provider-acme", Builds: []PackageBuild{{GOOS: "linux", GOARCH: "amd64", Path: providerPath}}},
	}

	res, err := PackageRelease(context.Background(), opts)
	require.NoError(t, err)

	assert.Equal(t, "1.4.0", res.Version)
	require.NotNil(t, res.Entry)
	assert.False(t, res.Entry.Official, "generated entries are always unofficial")
	assert.Equal(t, "acme/tf", res.Entry.Name)

	// Each component/platform has an archive + sidecar in the nested layout.
	parserArchive := filepath.Join(out, "infracost-parser-acme", "linux", "amd64", "1.4.0", "data.tar.gz")
	providerArchive := filepath.Join(out, "infracost-provider-acme", "linux", "amd64", "1.4.0", "data.tar.gz")
	assert.FileExists(t, parserArchive)
	assert.FileExists(t, parserArchive+".sha256")
	assert.FileExists(t, providerArchive)
	assert.FileExists(t, providerArchive+".sha256")

	// The recorded checksum matches the archive on disk.
	for _, art := range res.Artifacts {
		assert.Equal(t, fileSHA256(t, art.ArchivePath), art.SHA256)
	}

	// The shared version file lives under the parser (anchor) component's tree.
	versionFile := filepath.Join(out, "infracost-parser-acme", "linux", "amd64", "latest", "version")
	assert.FileExists(t, versionFile)
	vb, err := os.ReadFile(versionFile)
	require.NoError(t, err)
	assert.Equal(t, "1.4.0\n", string(vb))

	// The manifest entry is written and re-parses cleanly.
	assert.FileExists(t, res.ManifestPath)
	data, err := os.ReadFile(res.ManifestPath)
	require.NoError(t, err)
	reparsed, err := registry.ParseEntry(data)
	require.NoError(t, err)
	assert.Equal(t, "acme/tf", reparsed.Name)
	assert.Len(t, reparsed.Components, 2)

	// The archive unpacks to the expected binary name and original content.
	dest := filepath.Join(t.TempDir(), "infracost-parser-acme")
	require.NoError(t, unpackTarGz(parserArchive, dest, "infracost-parser-acme"))
	got, err := os.ReadFile(dest)
	require.NoError(t, err)
	assert.Equal(t, "parser-linux", string(got))
}

func TestPackageReleaseDeterministicArchives(t *testing.T) {
	makeRun := func() *PackageResult {
		build := t.TempDir()
		out := filepath.Join(t.TempDir(), "dist")
		parserPath := writeBinary(t, build, "infracost-parser-acme", []byte("stable-bytes"))
		opts := baseOpts(map[string]probeResult{parserPath: parserProbe("acme/tf", "2.0.0")})
		opts.OutDir = out
		opts.Components = []PackageComponentInput{
			{Type: "parser", BinaryName: "infracost-parser-acme", Builds: []PackageBuild{{GOOS: "linux", GOARCH: "amd64", Path: parserPath}}},
		}
		res, err := PackageRelease(context.Background(), opts)
		require.NoError(t, err)
		return res
	}

	first := makeRun()
	second := makeRun()

	require.Len(t, first.Artifacts, 1)
	require.Len(t, second.Artifacts, 1)
	assert.Equal(t, first.Artifacts[0].SHA256, second.Artifacts[0].SHA256,
		"identical inputs must produce byte-identical archives")

	a, err := os.ReadFile(first.Artifacts[0].ArchivePath)
	require.NoError(t, err)
	b, err := os.ReadFile(second.Artifacts[0].ArchivePath)
	require.NoError(t, err)
	assert.Equal(t, a, b)
}

func TestPackageReleaseCrossCompiledStaticChecks(t *testing.T) {
	build := t.TempDir()
	out := filepath.Join(t.TempDir(), "dist")

	linuxPath := writeBinary(t, build, "infracost-parser-acme_linux_amd64", []byte("linux"))
	winPath := writeBinary(t, build, "infracost-parser-acme_windows_amd64.exe", []byte("windows"))

	opts := baseOpts(map[string]probeResult{linuxPath: parserProbe("acme/tf", "1.0.0")})
	opts.OutDir = out
	opts.Components = []PackageComponentInput{
		{Type: "parser", BinaryName: "infracost-parser-acme", Builds: []PackageBuild{
			{GOOS: "linux", GOARCH: "amd64", Path: linuxPath},
			{GOOS: "windows", GOARCH: "amd64", Path: winPath},
		}},
	}

	res, err := PackageRelease(context.Background(), opts)
	require.NoError(t, err)

	// The Windows archive carries the .exe-suffixed binary inside a data.tar.gz.
	winArchive := filepath.Join(out, "infracost-parser-acme", "windows", "amd64", "1.0.0", "data.tar.gz")
	require.FileExists(t, winArchive)
	dest := filepath.Join(t.TempDir(), "infracost-parser-acme.exe")
	require.NoError(t, unpackTarGz(winArchive, dest, "infracost-parser-acme.exe"))

	assert.Len(t, res.Artifacts, 2)
}

func TestPackageReleaseWindowsBuildMissingExeIsHardError(t *testing.T) {
	build := t.TempDir()
	linuxPath := writeBinary(t, build, "infracost-parser-acme", []byte("linux"))
	winPath := writeBinary(t, build, "infracost-parser-acme-windows", []byte("windows")) // no .exe

	opts := baseOpts(map[string]probeResult{linuxPath: parserProbe("acme/tf", "1.0.0")})
	opts.OutDir = filepath.Join(t.TempDir(), "dist")
	opts.Components = []PackageComponentInput{
		{Type: "parser", BinaryName: "infracost-parser-acme", Builds: []PackageBuild{
			{GOOS: "linux", GOARCH: "amd64", Path: linuxPath},
			{GOOS: "windows", GOARCH: "amd64", Path: winPath},
		}},
	}

	_, err := PackageRelease(context.Background(), opts)
	require.Error(t, err)
	assert.Contains(t, err.Error(), ".exe")
}

func TestPackageReleaseNoCurrentPlatformRequiresVersion(t *testing.T) {
	build := t.TempDir()
	winPath := writeBinary(t, build, "infracost-parser-acme.exe", []byte("windows"))

	opts := baseOpts(nil) // current platform is linux/amd64; only a windows build is given
	opts.OutDir = filepath.Join(t.TempDir(), "dist")
	opts.Components = []PackageComponentInput{
		{Type: "parser", BinaryName: "infracost-parser-acme", Builds: []PackageBuild{
			{GOOS: "windows", GOARCH: "amd64", Path: winPath},
		}},
	}

	_, err := PackageRelease(context.Background(), opts)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--version")

	// With an explicit version it packages, reporting the skipped validation.
	opts.Version = "9.9.9"
	res, err := PackageRelease(context.Background(), opts)
	require.NoError(t, err)
	assert.Equal(t, "9.9.9", res.Version)
	assert.Contains(t, res.StaticOnly, "infracost-parser-acme")
}

func TestPackageReleaseVersionFlagDisagreementIsHardError(t *testing.T) {
	build := t.TempDir()
	parserPath := writeBinary(t, build, "infracost-parser-acme", []byte("linux"))

	opts := baseOpts(map[string]probeResult{parserPath: parserProbe("acme/tf", "1.0.0")})
	opts.OutDir = filepath.Join(t.TempDir(), "dist")
	opts.Version = "2.0.0"
	opts.Components = []PackageComponentInput{
		{Type: "parser", BinaryName: "infracost-parser-acme", Builds: []PackageBuild{{GOOS: "linux", GOARCH: "amd64", Path: parserPath}}},
	}

	_, err := PackageRelease(context.Background(), opts)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "1.0.0")
	assert.Contains(t, err.Error(), "2.0.0")
}

func TestPackageReleaseComponentsDisagreeOnVersion(t *testing.T) {
	build := t.TempDir()
	parserPath := writeBinary(t, build, "infracost-parser-acme", []byte("p"))
	providerPath := writeBinary(t, build, "infracost-provider-acme", []byte("pr"))

	opts := baseOpts(map[string]probeResult{
		parserPath:   parserProbe("acme/tf", "1.0.0"),
		providerPath: providerProbe("acme/tf", "1.0.1"),
	})
	opts.OutDir = filepath.Join(t.TempDir(), "dist")
	opts.Components = []PackageComponentInput{
		{Type: "parser", BinaryName: "infracost-parser-acme", Builds: []PackageBuild{{GOOS: "linux", GOARCH: "amd64", Path: parserPath}}},
		{Type: "provider", BinaryName: "infracost-provider-acme", Builds: []PackageBuild{{GOOS: "linux", GOARCH: "amd64", Path: providerPath}}},
	}

	_, err := PackageRelease(context.Background(), opts)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "disagree")
}

func TestPackageReleaseIdentityNameMismatch(t *testing.T) {
	build := t.TempDir()
	parserPath := writeBinary(t, build, "infracost-parser-acme", []byte("p"))

	opts := baseOpts(map[string]probeResult{parserPath: parserProbe("someone/else", "1.0.0")})
	opts.OutDir = filepath.Join(t.TempDir(), "dist")
	opts.Components = []PackageComponentInput{
		{Type: "parser", BinaryName: "infracost-parser-acme", Builds: []PackageBuild{{GOOS: "linux", GOARCH: "amd64", Path: parserPath}}},
	}

	_, err := PackageRelease(context.Background(), opts)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "someone/else")
}

func TestPackageReleaseChecklistFailureReturnsTypedError(t *testing.T) {
	build := t.TempDir()
	parserPath := writeBinary(t, build, "infracost-parser-acme", []byte("p"))

	// A handshake failure makes the checklist fail.
	opts := baseOpts(map[string]probeResult{parserPath: {handshakeErr: assertErr("boom")}})
	opts.OutDir = filepath.Join(t.TempDir(), "dist")
	opts.Components = []PackageComponentInput{
		{Type: "parser", BinaryName: "infracost-parser-acme", Builds: []PackageBuild{{GOOS: "linux", GOARCH: "amd64", Path: parserPath}}},
	}

	_, err := PackageRelease(context.Background(), opts)
	require.Error(t, err)
	var ve *PackageValidationError
	require.ErrorAs(t, err, &ve)
	require.Len(t, ve.Results, 1)
	assert.False(t, ve.Results[0].OK())
}

func TestPackageReleaseInvalidName(t *testing.T) {
	build := t.TempDir()
	parserPath := writeBinary(t, build, "infracost-parser-acme", []byte("p"))
	opts := baseOpts(map[string]probeResult{parserPath: parserProbe("acme", "1.0.0")})
	opts.Name = "not-namespaced"
	opts.Components = []PackageComponentInput{
		{Type: "parser", BinaryName: "infracost-parser-acme", Builds: []PackageBuild{{GOOS: "linux", GOARCH: "amd64", Path: parserPath}}},
	}

	_, err := PackageRelease(context.Background(), opts)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "namespaced")
}

func TestPackageReleaseRejectsPathTraversalSegments(t *testing.T) {
	tests := []struct {
		name       string
		binaryName string
		goos       string
		goarch     string
	}{
		{name: "binary name", binaryName: "../infracost-parser-acme", goos: "linux", goarch: "amd64"},
		{name: "goos", binaryName: "infracost-parser-acme", goos: "../linux", goarch: "amd64"},
		{name: "goarch", binaryName: "infracost-parser-acme", goos: "linux", goarch: `..\amd64`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := baseOpts(nil)
			opts.Components = []PackageComponentInput{{
				Type:       registry.ComponentTypeParser,
				BinaryName: tt.binaryName,
				Builds:     []PackageBuild{{GOOS: tt.goos, GOARCH: tt.goarch, Path: "unused"}},
			}}

			err := validatePackageInputs(&opts)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "single path segment")
		})
	}
}

func TestPackageReleaseRejectsPathTraversalVersion(t *testing.T) {
	build := t.TempDir()
	parserPath := writeBinary(t, build, "infracost-parser-acme", []byte("p"))
	opts := baseOpts(map[string]probeResult{parserPath: parserProbe("acme/tf", "../escape")})
	opts.Version = "../escape"
	opts.Components = []PackageComponentInput{{
		Type:       registry.ComponentTypeParser,
		BinaryName: "infracost-parser-acme",
		Builds:     []PackageBuild{{GOOS: "linux", GOARCH: "amd64", Path: parserPath}},
	}}

	_, err := PackageRelease(context.Background(), opts)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid version")
	assert.Contains(t, err.Error(), "single path segment")
}

func TestPackageReleaseRefusesDirtyOutWithoutForce(t *testing.T) {
	build := t.TempDir()
	out := filepath.Join(t.TempDir(), "dist")
	parserPath := writeBinary(t, build, "infracost-parser-acme", []byte("p"))

	mk := func() PackageOptions {
		o := baseOpts(map[string]probeResult{parserPath: parserProbe("acme/tf", "1.0.0")})
		o.OutDir = out
		o.Components = []PackageComponentInput{
			{Type: "parser", BinaryName: "infracost-parser-acme", Builds: []PackageBuild{{GOOS: "linux", GOARCH: "amd64", Path: parserPath}}},
		}
		return o
	}

	_, err := PackageRelease(context.Background(), mk())
	require.NoError(t, err)

	// Second run into the same out dir is refused...
	_, err = PackageRelease(context.Background(), mk())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--force")

	// ...unless --force is set.
	forced := mk()
	forced.Force = true
	_, err = PackageRelease(context.Background(), forced)
	require.NoError(t, err)
}

func TestPackageReleaseFlatLayout(t *testing.T) {
	build := t.TempDir()
	out := filepath.Join(t.TempDir(), "dist")
	parserPath := writeBinary(t, build, "infracost-parser-acme", []byte("p"))

	opts := baseOpts(map[string]probeResult{parserPath: parserProbe("acme/tf", "3.2.1")})
	opts.OutDir = out
	opts.Flat = true
	opts.Components = []PackageComponentInput{
		{Type: "parser", BinaryName: "infracost-parser-acme", Builds: []PackageBuild{{GOOS: "linux", GOARCH: "amd64", Path: parserPath}}},
	}

	res, err := PackageRelease(context.Background(), opts)
	require.NoError(t, err)

	archive := filepath.Join(out, "acme-tf-parser-3.2.1-linux-amd64.tar.gz")
	assert.FileExists(t, archive)
	assert.FileExists(t, archive+".sha256")
	assert.FileExists(t, filepath.Join(out, "acme-tf-latest.version"))

	// The generated download template is single-namespace and .sha256 tracks it.
	comp := res.Entry.Components[0]
	assert.Contains(t, comp.Download, "acme-tf-parser-{version}-{os}-{arch}.tar.gz")
	assert.Equal(t, comp.Download+".sha256", comp.Checksums)
}

func TestPackageReleaseDuplicateComponentType(t *testing.T) {
	build := t.TempDir()
	p1 := writeBinary(t, build, "infracost-parser-a", []byte("a"))
	p2 := writeBinary(t, build, "infracost-parser-b", []byte("b"))

	opts := baseOpts(nil)
	opts.Version = "1.0.0"
	opts.Components = []PackageComponentInput{
		{Type: "parser", BinaryName: "infracost-parser-a", Builds: []PackageBuild{{GOOS: "windows", GOARCH: "amd64", Path: p1 + ".exe"}}},
		{Type: "parser", BinaryName: "infracost-parser-b", Builds: []PackageBuild{{GOOS: "windows", GOARCH: "amd64", Path: p2 + ".exe"}}},
	}

	_, err := PackageRelease(context.Background(), opts)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "one parser")
}

func TestPackageReleaseManifestIsIndentedJSON(t *testing.T) {
	build := t.TempDir()
	out := filepath.Join(t.TempDir(), "dist")
	parserPath := writeBinary(t, build, "infracost-parser-acme", []byte("p"))

	opts := baseOpts(map[string]probeResult{parserPath: parserProbe("acme/tf", "1.0.0")})
	opts.OutDir = out
	opts.Components = []PackageComponentInput{
		{Type: "parser", BinaryName: "infracost-parser-acme", Builds: []PackageBuild{{GOOS: "linux", GOARCH: "amd64", Path: parserPath}}},
	}

	res, err := PackageRelease(context.Background(), opts)
	require.NoError(t, err)

	data, err := os.ReadFile(res.ManifestPath)
	require.NoError(t, err)
	var pretty map[string]any
	require.NoError(t, json.Unmarshal(data, &pretty))
	assert.Equal(t, false, pretty["official"])
	assert.Contains(t, string(data), "\n  \"name\": \"acme/tf\"", "manifest should be indented")
}

// assertErr is a tiny error helper for probe stubs.
type assertErr string

func (e assertErr) Error() string { return string(e) }
