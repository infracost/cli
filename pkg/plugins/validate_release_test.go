package plugins

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/infracost/cli/pkg/plugins/registry"
	pb "github.com/infracost/proto/gen/go/infracost/plugin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// releaseServerOpts toggles how the harness server misbehaves so the failure
// paths (missing checksum, wrong digest, HEAD-rejecting host) can be exercised.
type releaseServerOpts struct {
	omitSHA    bool // return 404 for the .sha256 sidecar
	badSHA     bool // serve a well-formed but wrong digest (64 zeros)
	rejectHEAD bool // answer HEAD on the archive with 405, forcing the GET fallback
}

// releaseHarness bundles a registry entry, an httptest server serving its
// artifacts, and a releaseValidator wired to injectable seams so --release
// validation can be driven without spawning real plugin subprocesses.
type releaseHarness struct {
	t       *testing.T
	srv     *httptest.Server
	entry   *registry.Entry
	v       *releaseValidator
	version string
	comps   []testComponent
}

// newReleaseHarness builds a server + entry + validator for the given components
// at the given resolved version, restricted to the current platform so the
// executed-binary checks actually run.
func newReleaseHarness(t *testing.T, name string, official bool, version string, opts releaseServerOpts, comps ...testComponent) *releaseHarness {
	t.Helper()

	archives := map[string][]byte{}
	shas := map[string]string{}
	for _, c := range comps {
		sub := t.TempDir()
		entryName := c.binaryName
		if runtime.GOOS == "windows" {
			entryName += ".exe"
		}
		p := createTestTarGz(t, sub, entryName, c.content)
		data, err := os.ReadFile(p)
		require.NoError(t, err)
		archives[c.binaryName] = data
		shas[c.binaryName] = fileSHA256(t, p)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if strings.HasSuffix(path, "/version") {
			_, _ = w.Write([]byte(version + "\n"))
			return
		}
		for bn := range archives {
			base := "/dl/" + bn + "/"
			if !strings.HasPrefix(path, base) {
				continue
			}
			switch {
			case strings.HasSuffix(path, ".sha256"):
				switch {
				case opts.omitSHA:
					w.WriteHeader(http.StatusNotFound)
				case opts.badSHA:
					_, _ = w.Write([]byte(strings.Repeat("0", 64) + "\n"))
				default:
					_, _ = w.Write([]byte(shas[bn] + "\n"))
				}
			case strings.HasSuffix(path, "data.tar.gz"):
				if opts.rejectHEAD && r.Method == http.MethodHead {
					w.WriteHeader(http.StatusMethodNotAllowed)
					return
				}
				_, _ = w.Write(archives[bn])
			default:
				w.WriteHeader(http.StatusNotFound)
			}
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)

	plat := runtime.GOOS + "/" + runtime.GOARCH
	var rc []registry.Component
	for _, c := range comps {
		rc = append(rc, registry.Component{
			Type:       c.typ,
			BinaryName: c.binaryName,
			Platforms:  []string{plat},
			Download:   srv.URL + "/dl/" + c.binaryName + "/{version}/data.tar.gz",
			Checksums:  srv.URL + "/dl/" + c.binaryName + "/{version}/data.tar.gz.sha256",
		})
	}
	entry := &registry.Entry{
		Name:       name,
		Official:   official,
		VersionURL: srv.URL + "/version",
		Components: rc,
	}

	h := &releaseHarness{t: t, srv: srv, entry: entry, version: version, comps: comps}
	h.v = &releaseValidator{
		httpClient: pluginHTTPClient,
		goos:       runtime.GOOS,
		goarch:     runtime.GOARCH,
		stat:       func(string) error { return nil },
		probe:      h.healthyProbe(),
	}
	return h
}

// healthyProbe reports a handshake whose name matches the entry and whose type
// matches the staged component, resolved from the extracted binary's filename.
func (h *releaseHarness) healthyProbe() func(string) probeResult {
	byBase := map[string]testComponent{}
	for _, c := range h.comps {
		base := c.binaryName
		if runtime.GOOS == "windows" {
			base += ".exe"
		}
		byBase[base] = c
	}
	return func(path string) probeResult {
		c, ok := byBase[filepath.Base(path)]
		if !ok {
			return probeResult{handshakeErr: fmt.Errorf("no probe stub for %s", filepath.Base(path))}
		}
		pr := probeResult{info: &pb.GetPluginInfoResponse{Name: h.entry.Name, Type: componentPluginType(c.typ), Version: h.version}}
		if c.typ == registry.ComponentTypeParser {
			pr.parserConfig = &pb.GetParserConfigResponse{}
		}
		return pr
	}
}

// compCheckByID returns the first component check with the given ID.
func compCheckByID(comp ReleaseComponentResult, id string) (CheckResult, bool) {
	return checkResultByID(comp.Checks, id)
}

// checkResultByID returns the first check in the slice with the given ID.
func checkResultByID(checks []CheckResult, id string) (CheckResult, bool) {
	for _, c := range checks {
		if c.ID == id {
			return c, true
		}
	}
	return CheckResult{}, false
}

func TestValidateRelease_HappyPath(t *testing.T) {
	parser := testComponent{typ: registry.ComponentTypeParser, binaryName: "acme-parser-tf", content: []byte("parser-binary")}
	h := newReleaseHarness(t, "acme/tf", true, "1.2.3", releaseServerOpts{}, parser)

	res, err := h.v.validate(context.Background(), h.entry, ReleaseOptions{}, nil)
	require.NoError(t, err)
	assert.True(t, res.OK(), "a healthy official release should pass")
	assert.False(t, res.Incomplete)
	assert.Equal(t, "1.2.3", res.Version)

	// Version resolved from the versionUrl (latest → 1.2.3).
	ver, ok := checkResultByID(res.Checks, CheckReleaseVersion)
	require.True(t, ok)
	assert.Equal(t, CheckPass, ver.Status)
	assert.Contains(t, ver.Detail, "1.2.3")

	require.Len(t, res.Components, 1)
	comp := res.Components[0]
	assert.True(t, comp.OK())

	reach, ok := compCheckByID(comp, CheckReleaseReach)
	require.True(t, ok)
	assert.Equal(t, CheckPass, reach.Status)

	dl, ok := compCheckByID(comp, CheckReleaseDownload)
	require.True(t, ok)
	assert.Equal(t, CheckPass, dl.Status)

	id, ok := compCheckByID(comp, CheckReleaseIdentity)
	require.True(t, ok)
	assert.Equal(t, CheckPass, id.Status)

	// The reused binary checklist ran against the extracted binary.
	hs, ok := compCheckByID(comp, CheckHandshake)
	require.True(t, ok)
	assert.Equal(t, CheckPass, hs.Status)
}

func TestValidateRelease_PinnedVersionSkipsResolution(t *testing.T) {
	parser := testComponent{typ: registry.ComponentTypeParser, binaryName: "acme-parser-tf", content: []byte("parser-binary")}
	h := newReleaseHarness(t, "acme/tf", true, "3.1.4", releaseServerOpts{}, parser)

	res, err := h.v.validate(context.Background(), h.entry, ReleaseOptions{Version: "3.1.4"}, nil)
	require.NoError(t, err)
	assert.True(t, res.OK())

	ver, ok := checkResultByID(res.Checks, CheckReleaseVersion)
	require.True(t, ok)
	assert.Contains(t, ver.Detail, "pinned 3.1.4")
}

func TestValidateRelease_MissingChecksumFails(t *testing.T) {
	parser := testComponent{typ: registry.ComponentTypeParser, binaryName: "acme-parser-tf", content: []byte("parser-binary")}
	h := newReleaseHarness(t, "acme/tf", true, "1.0.0", releaseServerOpts{omitSHA: true}, parser)

	res, err := h.v.validate(context.Background(), h.entry, ReleaseOptions{}, nil)
	require.NoError(t, err)
	assert.False(t, res.OK())

	require.Len(t, res.Components, 1)
	reach, ok := compCheckByID(res.Components[0], CheckReleaseReach)
	require.True(t, ok)
	assert.Equal(t, CheckFail, reach.Status)
	assert.Contains(t, reach.Detail, "checksum sidecar missing")
}

func TestValidateRelease_DigestMismatchReportsBothDigests(t *testing.T) {
	parser := testComponent{typ: registry.ComponentTypeParser, binaryName: "acme-parser-tf", content: []byte("parser-binary")}
	h := newReleaseHarness(t, "acme/tf", true, "1.0.0", releaseServerOpts{badSHA: true}, parser)

	res, err := h.v.validate(context.Background(), h.entry, ReleaseOptions{}, nil)
	require.NoError(t, err)
	assert.False(t, res.OK())

	// A well-formed-but-wrong digest passes reachability but fails download-verify.
	comp := res.Components[0]
	reach, ok := compCheckByID(comp, CheckReleaseReach)
	require.True(t, ok)
	assert.Equal(t, CheckPass, reach.Status)

	dl, ok := compCheckByID(comp, CheckReleaseDownload)
	require.True(t, ok)
	assert.Equal(t, CheckFail, dl.Status)
	// Both the expected (served) and actual (computed) digests are in the message.
	assert.Contains(t, dl.Detail, "SHA256 mismatch")
	assert.Contains(t, dl.Detail, "expected")
	assert.Contains(t, dl.Detail, "got")
	assert.Contains(t, dl.Detail, strings.Repeat("0", 64))
}

func TestValidateRelease_MetadataMismatchFails(t *testing.T) {
	parser := testComponent{typ: registry.ComponentTypeParser, binaryName: "acme-parser-tf", content: []byte("parser-binary")}
	h := newReleaseHarness(t, "acme/tf", true, "1.0.0", releaseServerOpts{}, parser)

	// The extracted binary reports a different plugin name than the manifest.
	h.v.probe = func(string) probeResult {
		return probeResult{
			info:         &pb.GetPluginInfoResponse{Name: "someone-else/tf", Type: pb.PluginType_PARSER, Version: "1.0.0"},
			parserConfig: &pb.GetParserConfigResponse{},
		}
	}

	res, err := h.v.validate(context.Background(), h.entry, ReleaseOptions{}, nil)
	require.NoError(t, err)
	assert.False(t, res.OK())

	id, ok := compCheckByID(res.Components[0], CheckReleaseIdentity)
	require.True(t, ok)
	assert.Equal(t, CheckFail, id.Status)
	assert.Contains(t, id.Detail, "reports name")
}

func TestValidateRelease_VersionMismatchFailsIdentity(t *testing.T) {
	parser := testComponent{typ: registry.ComponentTypeParser, binaryName: "acme-parser-tf", content: []byte("parser-binary")}
	h := newReleaseHarness(t, "acme/tf", true, "1.0.0", releaseServerOpts{}, parser)

	// Reports the manifest name/type but the wrong version.
	h.v.probe = func(string) probeResult {
		return probeResult{
			info:         &pb.GetPluginInfoResponse{Name: "acme/tf", Type: pb.PluginType_PARSER, Version: "9.9.9"},
			parserConfig: &pb.GetParserConfigResponse{},
		}
	}

	res, err := h.v.validate(context.Background(), h.entry, ReleaseOptions{}, nil)
	require.NoError(t, err)
	assert.False(t, res.OK())

	id, ok := compCheckByID(res.Components[0], CheckReleaseIdentity)
	require.True(t, ok)
	assert.Equal(t, CheckFail, id.Status)
	assert.Contains(t, id.Detail, "reports version")
}

func TestValidateRelease_HeadRejectingHostFallsBackToGet(t *testing.T) {
	parser := testComponent{typ: registry.ComponentTypeParser, binaryName: "acme-parser-tf", content: []byte("parser-binary")}
	h := newReleaseHarness(t, "acme/tf", true, "1.0.0", releaseServerOpts{rejectHEAD: true}, parser)

	res, err := h.v.validate(context.Background(), h.entry, ReleaseOptions{}, nil)
	require.NoError(t, err)
	assert.True(t, res.OK(), "a host that 405s HEAD must still validate via the ranged-GET fallback")

	reach, ok := compCheckByID(res.Components[0], CheckReleaseReach)
	require.True(t, ok)
	assert.Equal(t, CheckPass, reach.Status)
}

func TestValidateRelease_UnofficialNonTTYSkipsExecution(t *testing.T) {
	parser := testComponent{typ: registry.ComponentTypeParser, binaryName: "acme-parser-tf", content: []byte("parser-binary")}
	h := newReleaseHarness(t, "acme/tf", false, "1.0.0", releaseServerOpts{}, parser)

	// A non-interactive run without --allow-unofficial: the trust gate warns and
	// declines (proceed=false, err=nil), mirroring confirmUnofficialInstall's
	// trustSkip behaviour.
	trust := func(*registry.Entry) (bool, error) { return false, nil }

	res, err := h.v.validate(context.Background(), h.entry, ReleaseOptions{}, trust)
	require.NoError(t, err)
	assert.False(t, res.OK(), "an incomplete checklist never passes")
	assert.True(t, res.Incomplete)

	comp := res.Components[0]
	// Network reachability still ran.
	reach, ok := compCheckByID(comp, CheckReleaseReach)
	require.True(t, ok)
	assert.Equal(t, CheckPass, reach.Status)
	// Execution was skipped, not failed, and points at the flag.
	dl, ok := compCheckByID(comp, CheckReleaseDownload)
	require.True(t, ok)
	assert.Equal(t, CheckSkip, dl.Status)
	assert.Contains(t, dl.Detail, "--allow-unofficial")
	// No execution checklist rows were added.
	_, hasHandshake := compCheckByID(comp, CheckHandshake)
	assert.False(t, hasHandshake, "a gated component must not run the binary checklist")
}

func TestValidateRelease_UnresolvedVersionStopsWithNoComponents(t *testing.T) {
	parser := testComponent{typ: registry.ComponentTypeParser, binaryName: "acme-parser-tf", content: []byte("parser-binary")}
	h := newReleaseHarness(t, "acme/tf", true, "1.0.0", releaseServerOpts{}, parser)
	// Point the version URL at a dead endpoint so resolution fails.
	h.entry.VersionURL = "http://127.0.0.1:1/version"

	res, err := h.v.validate(context.Background(), h.entry, ReleaseOptions{}, nil)
	require.NoError(t, err)
	assert.False(t, res.OK())

	ver, ok := checkResultByID(res.Checks, CheckReleaseVersion)
	require.True(t, ok)
	assert.Equal(t, CheckFail, ver.Status)
	assert.Empty(t, res.Components, "no artifact URL can be built without a version")
}

func TestValidateRelease_DualComponentIndependentFailure(t *testing.T) {
	parser := testComponent{typ: registry.ComponentTypeParser, binaryName: "acme-parser-k8s", content: []byte("parser-v1")}
	provider := testComponent{typ: registry.ComponentTypeProvider, binaryName: "acme-provider-k8s", content: []byte("provider-v1")}
	h := newReleaseHarness(t, "acme/k8s", true, "2.0.0", releaseServerOpts{}, parser, provider)

	// Only the provider's handshake fails; the parser stays healthy.
	base := provider.binaryName
	if runtime.GOOS == "windows" {
		base += ".exe"
	}
	healthy := h.healthyProbe()
	h.v.probe = func(path string) probeResult {
		if filepath.Base(path) == base {
			return probeResult{handshakeErr: fmt.Errorf("plugin handshake failed: boom")}
		}
		return healthy(path)
	}

	res, err := h.v.validate(context.Background(), h.entry, ReleaseOptions{}, nil)
	require.NoError(t, err)
	assert.False(t, res.OK(), "one failing component fails the entry")
	require.Len(t, res.Components, 2)

	byBinary := map[string]ReleaseComponentResult{}
	for _, c := range res.Components {
		byBinary[c.BinaryName] = c
	}
	assert.True(t, byBinary["acme-parser-k8s"].OK(), "the healthy component still passes independently")
	assert.False(t, byBinary["acme-provider-k8s"].OK())
}
