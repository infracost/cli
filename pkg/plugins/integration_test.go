package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	pb "github.com/infracost/proto/gen/go/infracost/plugin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/infracost/cli/pkg/plugins/registry"
)

// The end-to-end fixtures pin two release versions so the update path has a real
// v1 -> v2 transition to detect and reinstall.
const (
	e2eV1 = "1.0.0"
	e2eV2 = "2.0.0"
)

// e2eComp is one component of a fixture registry entry.
type e2eComp struct {
	typ        string
	binaryName string
}

// e2eEntry is a fixture registry entry (one official, one unofficial) the
// end-to-end lifecycle drives through install -> list -> resolve -> update ->
// validate -> uninstall.
type e2eEntry struct {
	name     string
	official bool
	author   string
	comps    []e2eComp
}

// e2eWorld is a self-contained fake plugin registry + artifact host. One
// httptest server answers three route families:
//
//	GET /registry.json                                  -> the manifest
//	GET /v/<entry-name>                                 -> the entry's latest version (mutable)
//	GET /dl/<binary>/<version>/data.tar.gz[.sha256]     -> the artifact and its checksum
//
// Every downloaded binary encodes its own identity (name/type/version) in its
// bytes; the stager and validator probe seams read those bytes back, so
// classify/handshake/update genuinely round-trip through real on-disk files
// rather than trusting harness-side bookkeeping.
type e2eWorld struct {
	t        *testing.T
	srv      *httptest.Server
	cfg      *Config
	cacheDir string
	entries  []e2eEntry

	mu       sync.Mutex
	versions map[string]string // entry name -> current latest version
	archives map[string][]byte // "<binary>@<version>" -> tar.gz bytes
	shas     map[string]string // "<binary>@<version>" -> sha256 hex
	manifest []byte
}

// fakePluginContent encodes a plugin's self-reported identity into the binary
// bytes, mirroring what a real plugin subprocess would answer over the
// handshake.
func fakePluginContent(name, typ, version string) []byte {
	return fmt.Appendf(nil, "name=%s\ntype=%s\nversion=%s\n", name, typ, version)
}

// parseFakePlugin reads the identity a fake binary encodes about itself.
func parseFakePlugin(path string) (name, typ, version string, err error) {
	data, err := os.ReadFile(path) //nolint:gosec // test-controlled path
	if err != nil {
		return "", "", "", err
	}
	for line := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch k {
		case "name":
			name = v
		case "type":
			typ = v
		case "version":
			version = v
		}
	}
	return name, typ, version, nil
}

func newE2EWorld(t *testing.T) *e2eWorld {
	t.Helper()

	w := &e2eWorld{
		t:        t,
		cacheDir: t.TempDir(),
		versions: map[string]string{},
		archives: map[string][]byte{},
		shas:     map[string]string{},
		entries: []e2eEntry{
			{
				name:     "infracost/datadog",
				official: true,
				comps: []e2eComp{
					{typ: registry.ComponentTypeParser, binaryName: "infracost-parser-datadog"},
					{typ: registry.ComponentTypeProvider, binaryName: "infracost-provider-datadog"},
				},
			},
			{
				name:     "acme/kubewidget",
				official: false,
				author:   "Acme Corp",
				comps: []e2eComp{
					{typ: registry.ComponentTypeParser, binaryName: "acme-parser-kubewidget"},
					{typ: registry.ComponentTypeProvider, binaryName: "acme-provider-kubewidget"},
				},
			},
		},
	}

	// Pre-build both versions of every component, and seed the latest at v1.
	for _, e := range w.entries {
		w.versions[e.name] = e2eV1
		for _, c := range e.comps {
			for _, ver := range []string{e2eV1, e2eV2} {
				w.buildArchive(e.name, c, ver)
			}
		}
	}

	w.srv = httptest.NewServer(http.HandlerFunc(w.handle))
	t.Cleanup(w.srv.Close)

	w.manifest = w.buildManifest()

	w.cfg = &Config{
		Cache:      w.cacheDir,
		BaseURL:    w.srv.URL,
		AutoUpdate: true,
		newStager:  func() *registryStager { return w.newStager() },
	}
	return w
}

// buildArchive packs one component/version into a tar.gz and records its bytes
// and checksum under "<binary>@<version>".
func (w *e2eWorld) buildArchive(entryName string, c e2eComp, version string) {
	w.t.Helper()
	entryFileName := c.binaryName
	if runtime.GOOS == "windows" {
		entryFileName += ".exe"
	}
	content := fakePluginContent(entryName, c.typ, version)
	p := createTestTarGz(w.t, w.t.TempDir(), entryFileName, content)
	data, err := os.ReadFile(p)
	require.NoError(w.t, err)
	key := c.binaryName + "@" + version
	w.archives[key] = data
	w.shas[key] = fileSHA256(w.t, p)
}

// buildManifest marshals the fixture registry, pointing every URL at this
// server so the real client/stager code drives the whole flow.
func (w *e2eWorld) buildManifest() []byte {
	w.t.Helper()
	plat := runtime.GOOS + "/" + runtime.GOARCH
	reg := registry.Registry{SchemaVersion: registry.SupportedSchemaVersion}
	for _, e := range w.entries {
		entry := registry.Entry{
			Name:       e.name,
			Official:   e.official,
			Author:     e.author,
			VersionURL: w.srv.URL + "/v/" + e.name,
		}
		for _, c := range e.comps {
			entry.Components = append(entry.Components, registry.Component{
				Type:       c.typ,
				BinaryName: c.binaryName,
				Platforms:  []string{plat},
				Download:   w.srv.URL + "/dl/" + c.binaryName + "/{version}/data.tar.gz",
				Checksums:  w.srv.URL + "/dl/" + c.binaryName + "/{version}/data.tar.gz.sha256",
			})
		}
		reg.Plugins = append(reg.Plugins, entry)
	}
	data, err := json.Marshal(reg)
	require.NoError(w.t, err)
	return data
}

func (w *e2eWorld) handle(rw http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	switch {
	case path == "/registry.json":
		_, _ = rw.Write(w.manifest)

	case strings.HasPrefix(path, "/v/"):
		name := strings.TrimPrefix(path, "/v/")
		w.mu.Lock()
		v := w.versions[name]
		w.mu.Unlock()
		if v == "" {
			rw.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = rw.Write([]byte(v + "\n"))

	case strings.HasPrefix(path, "/dl/"):
		parts := strings.SplitN(strings.TrimPrefix(path, "/dl/"), "/", 3)
		if len(parts) != 3 {
			rw.WriteHeader(http.StatusNotFound)
			return
		}
		binaryName, version, file := parts[0], parts[1], parts[2]
		key := binaryName + "@" + version
		w.mu.Lock()
		archive, aok := w.archives[key]
		sha, sok := w.shas[key]
		w.mu.Unlock()
		switch {
		case strings.HasSuffix(file, ".sha256"):
			if !sok {
				rw.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = rw.Write([]byte(sha + "\n"))
		case strings.HasSuffix(file, "data.tar.gz"):
			if !aok {
				rw.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = rw.Write(archive)
		default:
			rw.WriteHeader(http.StatusNotFound)
		}

	default:
		rw.WriteHeader(http.StatusNotFound)
	}
}

// loadRegistry fetches the fixture manifest through the real registry client.
func (w *e2eWorld) loadRegistry(ctx context.Context) (*registry.Registry, error) {
	client := &registry.Client{
		URL:        w.srv.URL + "/registry.json",
		HTTPClient: w.srv.Client(),
		CachePath:  filepath.Join(w.t.TempDir(), "registry-cache.json"),
		TTL:        time.Hour,
	}
	return client.Load(ctx)
}

func (w *e2eWorld) newStager() *registryStager {
	return &registryStager{
		cacheDir:   w.cacheDir,
		goos:       runtime.GOOS,
		goarch:     runtime.GOARCH,
		httpClient: pluginHTTPClient,
		probe:      w.stagerProbe,
		queryInfo:  w.stagerQueryInfo,
		now:        time.Now,
	}
}

// stagerProbe answers the post-stage handshake from the staged binary's bytes.
func (w *e2eWorld) stagerProbe(path string) probeResult {
	name, typ, version, err := parseFakePlugin(path)
	if err != nil {
		return probeResult{handshakeErr: err}
	}
	return probeResult{info: &pb.GetPluginInfoResponse{
		Name:    name,
		Type:    componentPluginType(typ),
		Version: version,
	}}
}

// stagerQueryInfo reads an already-installed binary's reported name/version so
// classify can compare it against the resolved release version.
func (w *e2eWorld) stagerQueryInfo(_ context.Context, path string) (*pb.GetPluginInfoResponse, error) {
	name, _, version, err := parseFakePlugin(path)
	if err != nil {
		return nil, err
	}
	return &pb.GetPluginInfoResponse{Name: name, Version: version}, nil
}

// validatorProbe mirrors stagerProbe but also dispenses a parser config so the
// parser RPC-surface check passes for parser components.
func (w *e2eWorld) validatorProbe(path string) probeResult {
	name, typ, version, err := parseFakePlugin(path)
	if err != nil {
		return probeResult{handshakeErr: err}
	}
	pt := componentPluginType(typ)
	pr := probeResult{info: &pb.GetPluginInfoResponse{Name: name, Type: pt, Version: version}}
	if pt == pb.PluginType_PARSER {
		pr.parserConfig = &pb.GetParserConfigResponse{}
	}
	return pr
}

func (w *e2eWorld) setVersion(name, version string) {
	w.mu.Lock()
	w.versions[name] = version
	w.mu.Unlock()
}

// installedVersion reads the version the on-disk binary reports about itself.
func (w *e2eWorld) installedVersion(binaryName string) string {
	_, _, v, err := parseFakePlugin(filepath.Join(w.cacheDir, pluginBinaryName(binaryName)))
	require.NoError(w.t, err)
	return v
}

// itemsForBinaries filters a List() result to rows whose on-disk binary matches
// one of the given binary names.
func itemsForBinaries(items []ListItem, binaryNames ...string) []ListItem {
	want := map[string]bool{}
	for _, n := range binaryNames {
		want[n] = true
	}
	var out []ListItem
	for _, it := range items {
		base := strings.TrimSuffix(filepath.Base(it.Path), ".exe")
		if want[base] {
			out = append(out, it)
		}
	}
	return out
}

func proceedAlways() TrustFunc {
	return func(*registry.Entry) (bool, error) { return true, nil }
}

// TestPluginLifecycle_EndToEnd exercises the full plugin lifecycle against a
// fixture registry + artifact host: install (official + unofficial, both
// dual-component) -> list -> search/info name resolution -> update (v1 -> v2,
// then a clean no-op) -> validate -> uninstall, asserting the filesystem and
// provenance state at each stage. This is the Phase 12 integration harness.
func TestPluginLifecycle_EndToEnd(t *testing.T) {
	ctx := context.Background()
	w := newE2EWorld(t)

	reg, err := w.loadRegistry(ctx)
	require.NoError(t, err)
	require.Len(t, reg.Plugins, 2)

	official := reg.ByName("infracost/datadog")
	unofficial := reg.ByName("acme/kubewidget")
	require.NotNil(t, official)
	require.NotNil(t, unofficial)

	// --- INSTALL: official dual-component entry ---------------------------
	var officialTrust bool
	res, err := w.cfg.InstallRegistryEntry(ctx, official, "", func(e *registry.Entry) (bool, error) {
		officialTrust = true
		assert.True(t, e.Official)
		return true, nil
	})
	require.NoError(t, err)
	assert.True(t, officialTrust, "trust gate is consulted once a download is required")
	assert.Equal(t, e2eV1, res.Version)
	require.Len(t, res.Installed, 2)
	assert.Equal(t, e2eV1, w.installedVersion("infracost-parser-datadog"))
	assert.Equal(t, e2eV1, w.installedVersion("infracost-provider-datadog"))

	// --- INSTALL: unofficial dual-component entry -------------------------
	// The trust gate must see the entry as unofficial.
	var unofficialTrust bool
	res2, err := w.cfg.InstallRegistryEntry(ctx, unofficial, "", func(e *registry.Entry) (bool, error) {
		unofficialTrust = true
		assert.False(t, e.Official)
		assert.Equal(t, "acme/kubewidget", e.Name)
		return true, nil
	})
	require.NoError(t, err)
	assert.True(t, unofficialTrust)
	require.Len(t, res2.Installed, 2)

	// --- LIST: both entries reflected from provenance, filesystem-backed --
	items := w.cfg.List()

	datadogItems := itemsForBinaries(items, "infracost-parser-datadog", "infracost-provider-datadog")
	require.Len(t, datadogItems, 2)
	for _, it := range datadogItems {
		assert.Equal(t, SourceRegistry, it.Source)
		assert.True(t, it.Official)
		assert.True(t, it.Installed)
		assert.Equal(t, e2eV1, it.Version)
	}

	kwItems := itemsForBinaries(items, "acme-parser-kubewidget", "acme-provider-kubewidget")
	require.Len(t, kwItems, 2)
	for _, it := range kwItems {
		assert.Equal(t, SourceRegistry, it.Source)
		assert.False(t, it.Official)
		assert.Equal(t, "Acme Corp", it.Author)
		assert.True(t, it.Installed)
		assert.Equal(t, e2eV1, it.Version)
	}

	// --- SEARCH/INFO resolution -------------------------------------------
	// The exact resolution + suggestion source every browse command shares.
	byName, err := reg.Resolve("acme/kubewidget", RequiredAliases())
	require.NoError(t, err)
	byBinary, err := reg.Resolve("acme-provider-kubewidget", RequiredAliases())
	require.NoError(t, err)
	assert.Equal(t, byName.Name, byBinary.Name)

	// A .exe-suffixed on-disk name still resolves (Windows filename tolerance).
	byExe, err := reg.Resolve("acme-provider-kubewidget.exe", RequiredAliases())
	require.NoError(t, err)
	assert.Equal(t, "acme/kubewidget", byExe.Name)

	// Unknown names produce the shared "did you mean" suggestion text.
	_, err = reg.Resolve("acme/kubewidgets", RequiredAliases())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found in registry")
	assert.Contains(t, err.Error(), "did you mean")
	assert.Contains(t, err.Error(), "acme/kubewidget")

	// --- UPDATE: bump both entries to v2 ----------------------------------
	w.setVersion("infracost/datadog", e2eV2)
	w.setVersion("acme/kubewidget", e2eV2)

	upOfficial, err := w.cfg.UpdateEntry(ctx, official, proceedAlways())
	require.NoError(t, err)
	require.Len(t, upOfficial.Entries, 1)
	assert.Equal(t, UpdateStatusUpdated, upOfficial.Entries[0].Status)
	assert.Equal(t, e2eV1, upOfficial.Entries[0].FromVersion)
	assert.Equal(t, e2eV2, upOfficial.Entries[0].ToVersion)
	assert.Equal(t, e2eV2, w.installedVersion("infracost-parser-datadog"))
	assert.Equal(t, e2eV2, w.installedVersion("infracost-provider-datadog"))

	upUnofficial, err := w.cfg.UpdateEntry(ctx, unofficial, proceedAlways())
	require.NoError(t, err)
	require.Len(t, upUnofficial.Entries, 1)
	assert.Equal(t, UpdateStatusUpdated, upUnofficial.Entries[0].Status)
	assert.Equal(t, e2eV2, w.installedVersion("acme-provider-kubewidget"))

	// Provenance now records the new shared version.
	rec := w.cfg.LoadState().find("infracost/datadog")
	require.NotNil(t, rec)
	assert.Equal(t, e2eV2, rec.Version)

	// A second update with nothing new is a clean up-to-date no-op.
	again, err := w.cfg.UpdateEntry(ctx, official, proceedAlways())
	require.NoError(t, err)
	require.Len(t, again.Entries, 1)
	assert.Equal(t, UpdateStatusUpToDate, again.Entries[0].Status)

	// --- VALIDATE: every installed binary passes the checklist ------------
	validator := &binaryValidator{stat: statPluginBinary, probe: w.validatorProbe}
	results, err := validator.validateDir(w.cacheDir)
	require.NoError(t, err)
	require.Len(t, results, 4) // 2 entries x 2 components
	for _, r := range results {
		assert.True(t, r.OK(), "validation should pass for %s: %+v", r.Path, r.Checks)
	}

	// --- UNINSTALL: both entries removed, provenance + filesystem cleared --
	for _, name := range []string{"infracost/datadog", "acme/kubewidget"} {
		target, err := w.cfg.ResolveUninstall(name)
		require.NoError(t, err)
		ures, err := w.cfg.Uninstall(target)
		require.NoError(t, err)
		assert.True(t, ures.RecordRemoved)
		assert.Len(t, ures.Removed, 2)
	}

	assert.Nil(t, w.cfg.LoadState().find("infracost/datadog"))
	assert.Nil(t, w.cfg.LoadState().find("acme/kubewidget"))

	for _, bn := range []string{
		"infracost-parser-datadog", "infracost-provider-datadog",
		"acme-parser-kubewidget", "acme-provider-kubewidget",
	} {
		_, statErr := os.Stat(filepath.Join(w.cacheDir, pluginBinaryName(bn)))
		assert.True(t, os.IsNotExist(statErr), "%s should be gone after uninstall", bn)
	}

	// List no longer reports any registry install.
	for _, it := range w.cfg.List() {
		if it.Source == SourceRegistry {
			assert.False(t, it.Installed, "no registry install should remain: %s", it.Name)
		}
	}
}

// TestPluginManagement_ConsistentDevOverrideRefusal verifies install, update,
// and uninstall all refuse with the same message shape when
// INFRACOST_CLI_PLUGIN_DIR (Config.Dir) is set — only the verb differs.
func TestPluginManagement_ConsistentDevOverrideRefusal(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{Dir: dir, Cache: t.TempDir()}
	e := &registry.Entry{
		Name: "acme/kubewidget",
		Components: []registry.Component{{
			Type:       registry.ComponentTypeParser,
			BinaryName: "acme-parser-kubewidget",
			Platforms:  []string{runtime.GOOS + "/" + runtime.GOARCH},
		}},
	}

	_, installErr := cfg.InstallRegistryEntry(context.Background(), e, "", proceedAlways())
	_, updateErr := cfg.UpdateAll(context.Background(), nil, nil, proceedAlways())
	_, uninstallErr := cfg.Uninstall(&UninstallTarget{Name: "acme/kubewidget", HasRecord: true})

	for _, err := range []error{installErr, updateErr, uninstallErr} {
		require.Error(t, err)
		assert.Contains(t, err.Error(), "INFRACOST_CLI_PLUGIN_DIR is set ("+dir+")")
		assert.Contains(t, err.Error(), "unset it to manage plugins automatically")
	}

	assert.Contains(t, installErr.Error(), "plugin installs are disabled")
	assert.Contains(t, updateErr.Error(), "plugin updates are disabled")
	assert.Contains(t, uninstallErr.Error(), "plugin uninstalls are disabled")
}
