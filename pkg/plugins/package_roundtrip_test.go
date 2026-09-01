package plugins

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	pb "github.com/infracost/proto/gen/go/infracost/plugin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// identityProbe decides a component's reported identity from the binary's file
// name, so it works for both the temp path validate extracts to and the
// ".staged" path install probes. name/version match the packaged release.
func identityProbe(name, version string) func(string) probeResult {
	return func(path string) probeResult {
		base := filepath.Base(path)
		base = strings.TrimSuffix(base, ".staged")
		base = strings.TrimSuffix(base, ".exe")
		typ, ok := ComponentTypeForBinaryName(base)
		if !ok {
			return probeResult{handshakeErr: assertErr("no identity for " + base)}
		}
		pr := probeResult{info: &pb.GetPluginInfoResponse{Name: name, Type: componentPluginType(typ), Version: version}}
		if typ == "parser" {
			pr.parserConfig = &pb.GetParserConfigResponse{}
		}
		return pr
	}
}

// TestPackageReleaseRoundTrip packages a dual-component release, then feeds the
// generated tree back through release validation and a staged install — the
// end-to-end guarantee the packaging command exists to provide. It also
// re-packages to prove the archives (and their checksums) are reproducible.
func TestPackageReleaseRoundTrip(t *testing.T) {
	const name = "acme/roundtrip"
	const version = "1.2.3"
	goos, goarch := runtime.GOOS, runtime.GOARCH

	out := filepath.Join(t.TempDir(), "dist")
	srv := httptest.NewServer(http.FileServer(http.Dir(out)))
	t.Cleanup(srv.Close)

	build := t.TempDir()
	parserPath := writeBinary(t, build, "infracost-parser-acme", []byte("parser-bytes"))
	providerPath := writeBinary(t, build, "infracost-provider-acme", []byte("provider-bytes"))

	opts := PackageOptions{
		Name:    name,
		BaseURL: srv.URL,
		OutDir:  out,
		stat:    func(string) error { return nil },
		probe: packageProbes(map[string]probeResult{
			parserPath:   parserProbe(name, version),
			providerPath: providerProbe(name, version),
		}),
		Components: []PackageComponentInput{
			{Type: "parser", BinaryName: "infracost-parser-acme", Builds: []PackageBuild{{GOOS: goos, GOARCH: goarch, Path: parserPath}}},
			{Type: "provider", BinaryName: "infracost-provider-acme", Builds: []PackageBuild{{GOOS: goos, GOARCH: goarch, Path: providerPath}}},
		},
	}

	res, err := PackageRelease(context.Background(), opts)
	require.NoError(t, err)
	require.Len(t, res.Artifacts, 2)

	// --- Round-trip 1: release validation against the served tree. ---
	rv := &releaseValidator{
		httpClient: pluginHTTPClient,
		goos:       goos,
		goarch:     goarch,
		probe:      identityProbe(name, version),
		stat:       func(string) error { return nil },
	}
	vres, err := rv.validate(context.Background(), res.Entry, ReleaseOptions{}, nil)
	require.NoError(t, err)
	assert.True(t, vres.OK(), "packaged release should pass validate --release: %+v", vres)
	assert.Equal(t, version, vres.Version)

	// --- Round-trip 2: staged install from the served tree. ---
	stager := &registryStager{
		cacheDir:   t.TempDir(),
		goos:       goos,
		goarch:     goarch,
		httpClient: pluginHTTPClient,
		probe:      identityProbe(name, version),
		queryInfo: func(context.Context, string) (*pb.GetPluginInfoResponse, error) {
			return &pb.GetPluginInfoResponse{Name: name, Version: version}, nil
		},
		now: time.Now,
	}
	ires, err := stager.install(context.Background(), res.Entry, "", nil)
	require.NoError(t, err)
	require.NotNil(t, ires)
	assert.Len(t, ires.Installed, 2, "both components install from the packaged tree")
	for _, ci := range ires.Installed {
		assert.FileExists(t, ci.Path)
	}

	// --- Reproducibility: repackaging yields identical archive checksums. ---
	out2 := filepath.Join(t.TempDir(), "dist")
	opts2 := opts
	opts2.OutDir = out2
	res2, err := PackageRelease(context.Background(), opts2)
	require.NoError(t, err)
	require.Len(t, res2.Artifacts, 2)

	shaByBin := map[string]string{}
	for _, a := range res.Artifacts {
		shaByBin[a.BinaryName] = a.SHA256
	}
	for _, a := range res2.Artifacts {
		assert.Equal(t, shaByBin[a.BinaryName], a.SHA256,
			"repackaged %s archive must have an identical checksum", a.BinaryName)
	}
}
