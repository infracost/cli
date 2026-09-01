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
	"time"

	"github.com/infracost/cli/pkg/plugins/registry"
	"github.com/infracost/cli/version"
	pb "github.com/infracost/proto/gen/go/infracost/plugin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testComponent describes one component to serve and install in a stager test.
type testComponent struct {
	typ        string
	binaryName string
	content    []byte
}

// stageHarness bundles a registry entry, an httptest server serving its
// artifacts, and a stager wired to injectable seams so the staged installer can
// be driven without spawning real plugin subprocesses.
type stageHarness struct {
	t        *testing.T
	srv      *httptest.Server
	entry    *registry.Entry
	stager   *registryStager
	cacheDir string
	version  string
	comps    []testComponent
}

// newStageHarness builds a server + entry + stager for the given components at
// the given resolved version. The default probe/queryInfo report a healthy
// binary whose identity matches the entry; individual tests override them.
func newStageHarness(t *testing.T, name string, official bool, version string, comps ...testComponent) *stageHarness {
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
				_, _ = w.Write([]byte(shas[bn] + "\n"))
			case strings.HasSuffix(path, "data.tar.gz"):
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

	cacheDir := t.TempDir()
	h := &stageHarness{t: t, srv: srv, entry: entry, cacheDir: cacheDir, version: version, comps: comps}
	h.stager = &registryStager{
		cacheDir:   cacheDir,
		goos:       runtime.GOOS,
		goarch:     runtime.GOARCH,
		httpClient: pluginHTTPClient,
		probe:      h.defaultProbe(),
		queryInfo:  h.defaultQueryInfo(),
		now:        time.Now,
	}
	return h
}

// defaultProbe reports a healthy handshake whose name matches the entry and
// whose type matches the staged component (resolved from the staged path).
func (h *stageHarness) defaultProbe() func(string) probeResult {
	byBase := map[string]pb.PluginType{}
	for _, c := range h.comps {
		base := c.binaryName
		if runtime.GOOS == "windows" {
			base += ".exe"
		}
		byBase[base] = componentPluginType(c.typ)
	}
	return func(path string) probeResult {
		base := strings.TrimSuffix(filepath.Base(path), ".staged")
		typ, ok := byBase[base]
		if !ok {
			return probeResult{handshakeErr: fmt.Errorf("no probe stub for %s", base)}
		}
		return probeResult{info: &pb.GetPluginInfoResponse{Name: h.entry.Name, Type: typ, Version: h.version}}
	}
}

// defaultQueryInfo reports an already-installed binary at the harness version,
// owned by the entry.
func (h *stageHarness) defaultQueryInfo() func(context.Context, string) (*pb.GetPluginInfoResponse, error) {
	return func(context.Context, string) (*pb.GetPluginInfoResponse, error) {
		return &pb.GetPluginInfoResponse{Name: h.entry.Name, Version: h.version}, nil
	}
}

func (h *stageHarness) binaryPath(c testComponent) string {
	return h.stager.binaryPath(registry.Component{BinaryName: c.binaryName})
}

// preinstall writes a fake existing binary for the component with the given
// content, as if a prior install had placed it.
func (h *stageHarness) preinstall(c testComponent, content []byte) string {
	h.t.Helper()
	path := h.binaryPath(c)
	require.NoError(h.t, os.MkdirAll(filepath.Dir(path), 0o750))
	require.NoError(h.t, os.WriteFile(path, content, 0o750))
	return path
}

// alwaysProceed is a trust func that records whether it was consulted.
func alwaysProceed(called *bool) TrustFunc {
	return func(*registry.Entry) (bool, error) {
		*called = true
		return true, nil
	}
}

func TestInstallRegistryEntry_HappyPathSingle(t *testing.T) {
	parser := testComponent{typ: registry.ComponentTypeParser, binaryName: "acme-parser-tf", content: []byte("parser-binary-v1")}
	h := newStageHarness(t, "acme/tf", true, "1.2.3", parser)

	var trustCalled bool
	res, err := h.stager.install(context.Background(), h.entry, "", alwaysProceed(&trustCalled))
	require.NoError(t, err)

	assert.False(t, res.NoOp)
	assert.False(t, res.Pinned)
	assert.Equal(t, "1.2.3", res.Version)
	require.Len(t, res.Installed, 1)

	got, err := os.ReadFile(h.binaryPath(parser))
	require.NoError(t, err)
	assert.Equal(t, parser.content, got)

	// One provenance record listing the component.
	st := loadState(h.cacheDir)
	rec := st.find("acme/tf")
	require.NotNil(t, rec)
	assert.Equal(t, "1.2.3", rec.Version)
	assert.False(t, rec.Pinned)
	require.Len(t, rec.Components, 1)
	assert.Equal(t, "acme-parser-tf", rec.Components[0].BinaryName)
}

func TestInstallRegistryEntry_HappyPathDual(t *testing.T) {
	parser := testComponent{typ: registry.ComponentTypeParser, binaryName: "acme-parser-k8s", content: []byte("parser-v1")}
	provider := testComponent{typ: registry.ComponentTypeProvider, binaryName: "acme-provider-k8s", content: []byte("provider-v1")}
	h := newStageHarness(t, "acme/k8s", true, "2.0.0", parser, provider)

	res, err := h.stager.install(context.Background(), h.entry, "", nil)
	require.NoError(t, err)
	require.Len(t, res.Installed, 2)

	for _, c := range []testComponent{parser, provider} {
		got, err := os.ReadFile(h.binaryPath(c))
		require.NoError(t, err)
		assert.Equal(t, c.content, got)
	}

	rec := loadState(h.cacheDir).find("acme/k8s")
	require.NotNil(t, rec)
	assert.Len(t, rec.Components, 2)
}

func TestInstallRegistryEntry_PinnedVersion(t *testing.T) {
	parser := testComponent{typ: registry.ComponentTypeParser, binaryName: "acme-parser-tf", content: []byte("pinned-binary")}
	h := newStageHarness(t, "acme/tf", true, "3.1.4", parser)

	res, err := h.stager.install(context.Background(), h.entry, "3.1.4", nil)
	require.NoError(t, err)
	assert.True(t, res.Pinned)
	assert.Equal(t, "3.1.4", res.Version)

	rec := loadState(h.cacheDir).find("acme/tf")
	require.NotNil(t, rec)
	assert.True(t, rec.Pinned)
	assert.Equal(t, "3.1.4", rec.Version)
}

func TestInstallRegistryEntry_SHAMismatchLeavesNoPartialBinary(t *testing.T) {
	parser := testComponent{typ: registry.ComponentTypeParser, binaryName: "acme-parser-tf", content: []byte("data")}
	h := newStageHarness(t, "acme/tf", true, "1.0.0", parser)

	// Serve a wrong checksum so downloadAndVerify fails.
	h.srv.Close()
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/version"):
			_, _ = w.Write([]byte("1.0.0\n"))
		case strings.HasSuffix(r.URL.Path, ".sha256"):
			_, _ = w.Write([]byte(strings.Repeat("0", 64) + "\n"))
		case strings.HasSuffix(r.URL.Path, "data.tar.gz"):
			_, _ = w.Write([]byte("corrupt"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(h.srv.Close)
	h.entry.VersionURL = h.srv.URL + "/version"
	h.entry.Components[0].Download = h.srv.URL + "/dl/acme-parser-tf/{version}/data.tar.gz"
	h.entry.Components[0].Checksums = h.srv.URL + "/dl/acme-parser-tf/{version}/data.tar.gz.sha256"

	_, err := h.stager.install(context.Background(), h.entry, "", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to download")

	_, statErr := os.Stat(h.binaryPath(parser))
	assert.True(t, os.IsNotExist(statErr), "no binary should be left behind on a checksum failure")
	// No provenance either.
	assert.Nil(t, loadState(h.cacheDir).find("acme/tf"))
}

func TestInstallRegistryEntry_HandshakeFailureLeavesPriorBinariesUnchanged(t *testing.T) {
	parser := testComponent{typ: registry.ComponentTypeParser, binaryName: "acme-parser-k8s", content: []byte("parser-v2")}
	provider := testComponent{typ: registry.ComponentTypeProvider, binaryName: "acme-provider-k8s", content: []byte("provider-v2")}
	h := newStageHarness(t, "acme/k8s", true, "2.0.0", parser, provider)

	// The parser is already installed at an older version (so it is reinstalled).
	oldParser := []byte("parser-v1-OLD")
	parserPath := h.preinstall(parser, oldParser)

	// It reports the old version so classify marks it outdated.
	h.stager.queryInfo = func(context.Context, string) (*pb.GetPluginInfoResponse, error) {
		return &pb.GetPluginInfoResponse{Name: h.entry.Name, Version: "1.0.0"}, nil
	}
	// The provider's handshake fails (wrong reported version).
	h.stager.probe = func(path string) probeResult {
		base := strings.TrimSuffix(filepath.Base(path), ".staged")
		want := provider.binaryName
		if runtime.GOOS == "windows" {
			want += ".exe"
		}
		if base == want {
			return probeResult{info: &pb.GetPluginInfoResponse{Name: h.entry.Name, Type: pb.PluginType_PROVIDER, Version: "9.9.9"}}
		}
		return probeResult{info: &pb.GetPluginInfoResponse{Name: h.entry.Name, Type: pb.PluginType_PARSER, Version: "2.0.0"}}
	}

	_, err := h.stager.install(context.Background(), h.entry, "", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "refusing to install")

	// The prior parser binary is untouched.
	got, err := os.ReadFile(parserPath)
	require.NoError(t, err)
	assert.Equal(t, oldParser, got)
	// The provider was never committed.
	_, statErr := os.Stat(h.binaryPath(provider))
	assert.True(t, os.IsNotExist(statErr))
	// No staged temp files linger.
	_, statErr = os.Stat(parserPath + ".staged")
	assert.True(t, os.IsNotExist(statErr))
}

func TestInstallRegistryEntry_MetadataMismatchFailsWholeEntry(t *testing.T) {
	parser := testComponent{typ: registry.ComponentTypeParser, binaryName: "acme-parser-tf", content: []byte("data")}
	h := newStageHarness(t, "acme/tf", true, "1.0.0", parser)

	// Staged binary reports a different name.
	h.stager.probe = func(string) probeResult {
		return probeResult{info: &pb.GetPluginInfoResponse{Name: "someone-else/tf", Type: pb.PluginType_PARSER, Version: "1.0.0"}}
	}

	_, err := h.stager.install(context.Background(), h.entry, "", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reports name")

	_, statErr := os.Stat(h.binaryPath(parser))
	assert.True(t, os.IsNotExist(statErr))
}

func TestInstallRegistryEntry_AlreadyInstalledNoOp(t *testing.T) {
	parser := testComponent{typ: registry.ComponentTypeParser, binaryName: "acme-parser-tf", content: []byte("current")}
	h := newStageHarness(t, "acme/tf", true, "1.0.0", parser)

	// The binary already exists and reports the resolved version.
	h.preinstall(parser, []byte("current"))

	// Fail the archive download so any attempt to fetch would error the test.
	probeCalled := false
	h.stager.probe = func(string) probeResult {
		probeCalled = true
		return probeResult{}
	}

	var trustCalled bool
	res, err := h.stager.install(context.Background(), h.entry, "", alwaysProceed(&trustCalled))
	require.NoError(t, err)
	assert.True(t, res.NoOp)
	assert.False(t, trustCalled, "no-op must not consult the trust gate")
	assert.False(t, probeCalled, "no-op must not stage or handshake")
	require.Len(t, res.Current, 1)
	assert.Empty(t, res.Installed)
}

func TestInstallRegistryEntry_PartialEntryCompletion(t *testing.T) {
	parser := testComponent{typ: registry.ComponentTypeParser, binaryName: "acme-parser-k8s", content: []byte("parser-current")}
	provider := testComponent{typ: registry.ComponentTypeProvider, binaryName: "acme-provider-k8s", content: []byte("provider-v1")}
	h := newStageHarness(t, "acme/k8s", true, "2.0.0", parser, provider)

	// Parser already present at the resolved version; provider missing.
	parserPath := h.preinstall(parser, []byte("parser-current"))

	var trustCalled bool
	res, err := h.stager.install(context.Background(), h.entry, "", alwaysProceed(&trustCalled))
	require.NoError(t, err)
	assert.True(t, trustCalled, "installing the missing provider requires a download and the trust gate")

	require.Len(t, res.Installed, 1)
	assert.Equal(t, "acme-provider-k8s", res.Installed[0].Component.BinaryName)
	require.Len(t, res.Current, 1)
	assert.Equal(t, "acme-parser-k8s", res.Current[0].Component.BinaryName)

	// Parser left exactly as it was.
	got, err := os.ReadFile(parserPath)
	require.NoError(t, err)
	assert.Equal(t, []byte("parser-current"), got)
	// Provider now installed.
	got, err = os.ReadFile(h.binaryPath(provider))
	require.NoError(t, err)
	assert.Equal(t, provider.content, got)
}

func TestInstallRegistryEntry_DevBinaryNeverOverwritten(t *testing.T) {
	parser := testComponent{typ: registry.ComponentTypeParser, binaryName: "acme-parser-tf", content: []byte("data")}
	h := newStageHarness(t, "acme/tf", true, "1.0.0", parser)

	devContent := []byte("locally-built-dev")
	devPath := h.preinstall(parser, devContent)
	h.stager.queryInfo = func(context.Context, string) (*pb.GetPluginInfoResponse, error) {
		return &pb.GetPluginInfoResponse{Name: h.entry.Name, Version: devPluginVersion}, nil
	}

	res, err := h.stager.install(context.Background(), h.entry, "", nil)
	require.NoError(t, err)
	assert.True(t, res.NoOp, "a dev build counts as current and is never overwritten")

	got, err := os.ReadFile(devPath)
	require.NoError(t, err)
	assert.Equal(t, devContent, got)
}

func TestInstallRegistryEntry_CollisionWithForeignBinary(t *testing.T) {
	parser := testComponent{typ: registry.ComponentTypeParser, binaryName: "acme-parser-tf", content: []byte("data")}
	h := newStageHarness(t, "acme/tf", true, "1.0.0", parser)

	h.preinstall(parser, []byte("hand-copied"))
	// No provenance record, and the binary reports a different name → foreign.
	h.stager.queryInfo = func(context.Context, string) (*pb.GetPluginInfoResponse, error) {
		return &pb.GetPluginInfoResponse{Name: "unrelated/plugin", Version: "0.0.1"}, nil
	}

	_, err := h.stager.install(context.Background(), h.entry, "", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "different plugin binary already exists")
}

// --- Config-level guards and required-set interplay ---

func TestInstallRegistryEntry_RefusesWithPluginDirSet(t *testing.T) {
	c := &Config{Dir: t.TempDir(), Cache: t.TempDir()}
	e := &registry.Entry{Name: "acme/tf", Components: []registry.Component{{Type: registry.ComponentTypeParser, BinaryName: "acme-parser-tf", Platforms: []string{runtime.GOOS + "/" + runtime.GOARCH}}}}

	_, err := c.InstallRegistryEntry(context.Background(), e, "", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "INFRACOST_CLI_PLUGIN_DIR")
}

func TestInstallRegistryEntry_RefusesUnsupportedPlatform(t *testing.T) {
	c := &Config{Cache: t.TempDir()}
	e := &registry.Entry{
		Name: "acme/tf",
		Components: []registry.Component{{
			Type:       registry.ComponentTypeParser,
			BinaryName: "acme-parser-tf",
			Platforms:  []string{"solaris/sparc"},
		}},
	}

	_, err := c.InstallRegistryEntry(context.Background(), e, "", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported component")
	assert.Contains(t, err.Error(), "acme-parser-tf")
}

func TestInstallRegistryEntry_RefusesMinCLIVersion(t *testing.T) {
	orig := version.Version
	version.Version = "0.5.0"
	t.Cleanup(func() { version.Version = orig })

	c := &Config{Cache: t.TempDir()}
	e := &registry.Entry{
		Name:          "acme/tf",
		MinCLIVersion: "9.0.0",
		Components: []registry.Component{{
			Type:       registry.ComponentTypeParser,
			BinaryName: "acme-parser-tf",
			Platforms:  []string{runtime.GOOS + "/" + runtime.GOARCH},
		}},
	}

	_, err := c.InstallRegistryEntry(context.Background(), e, "", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires Infracost CLI")
}

func TestInstallRegistryEntry_DevCLIPassesMinCLIVersion(t *testing.T) {
	// version.Version defaults to "dev" in tests, which is not a release version.
	require.Equal(t, "dev", version.Version)
	e := &registry.Entry{Name: "acme/tf", MinCLIVersion: "9.0.0"}
	assert.NoError(t, checkMinCLIVersion(e))
}

func TestInstallRegistryEntry_RequiredEnsureAndVersionRefusal(t *testing.T) {
	// The terraform parser is a required plugin. Installing it by name is an
	// ensure/pre-warm: it downloads the missing required binary via the manager
	// but writes no provenance record and creates no pin.
	required := requiredPluginsForName("infracost/terraform")
	require.Len(t, required, 1)
	bin := required[0].Name

	archiveName := pluginArchiveName()
	sub := t.TempDir()
	archivePath := createPluginArchive(t, sub, archiveName, pluginBinaryName(bin), []byte("tf-required-binary"))
	archiveData, err := os.ReadFile(archivePath)
	require.NoError(t, err)
	archiveSHA := fileSHA256(t, archivePath)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
		if len(parts) != 5 || parts[0] != bin || parts[1] != runtime.GOOS || parts[2] != runtime.GOARCH || parts[3] != "latest" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		switch parts[4] {
		case "version":
			_, _ = w.Write([]byte("0.0.1\n"))
		case archiveName + ".sha256":
			_, _ = w.Write([]byte(archiveSHA + "\n"))
		case archiveName:
			_, _ = w.Write(archiveData)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	cacheDir := t.TempDir()
	c := &Config{Cache: cacheDir, BaseURL: srv.URL, AutoUpdate: true}
	e := &registry.Entry{
		Name:       "infracost/terraform",
		Official:   true,
		Components: []registry.Component{{Type: registry.ComponentTypeParser, BinaryName: bin, Platforms: []string{runtime.GOOS + "/" + runtime.GOARCH}}},
	}

	res, err := c.InstallRegistryEntry(context.Background(), e, "", nil)
	require.NoError(t, err)
	assert.True(t, res.Required)
	require.Len(t, res.Installed, 1)

	// The binary landed, but no provenance record was written for a required name.
	got, err := os.ReadFile(filepath.Join(cacheDir, pluginBinaryName(bin)))
	require.NoError(t, err)
	assert.Equal(t, []byte("tf-required-binary"), got)
	assert.Nil(t, loadState(cacheDir).find("infracost/terraform"))

	// Requesting a specific version of a built-in plugin is refused.
	_, err = c.InstallRegistryEntry(context.Background(), e, "1.2.3", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "managed by the CLI")
	assert.Nil(t, loadState(cacheDir).find("infracost/terraform"), "a refused pin must not create a record")
}
