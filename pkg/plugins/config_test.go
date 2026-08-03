package plugins

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	pb "github.com/infracost/proto/gen/go/infracost/plugin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func createTestTarGz(t *testing.T, dir string, fileName string, content []byte) string {
	t.Helper()
	archivePath := filepath.Join(dir, "test.tar.gz")
	f, err := os.Create(archivePath)
	require.NoError(t, err)

	gw := gzip.NewWriter(f)
	tw := tar.NewWriter(gw)

	require.NoError(t, tw.WriteHeader(&tar.Header{
		Name: fileName,
		Size: int64(len(content)),
		Mode: 0600,
	}))
	_, err = tw.Write(content)
	require.NoError(t, err)

	require.NoError(t, tw.Close())
	require.NoError(t, gw.Close())
	require.NoError(t, f.Close())
	return archivePath
}

func createTestZip(t *testing.T, dir string, fileName string, content []byte) string {
	t.Helper()
	archivePath := filepath.Join(dir, "test.zip")
	f, err := os.Create(archivePath)
	require.NoError(t, err)

	zw := zip.NewWriter(f)
	w, err := zw.Create(fileName)
	require.NoError(t, err)
	_, err = w.Write(content)
	require.NoError(t, err)

	require.NoError(t, zw.Close())
	require.NoError(t, f.Close())
	return archivePath
}

func createPluginArchive(t *testing.T, dir, archiveName, binaryName string, content []byte) string {
	t.Helper()
	if strings.HasSuffix(archiveName, ".zip") {
		return createTestZip(t, dir, binaryName, content)
	}
	return createTestTarGz(t, dir, binaryName, content)
}

func fileSHA256(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// stubInfoFn returns a queryInfo function that reports the given version
// regardless of which binary path is queried.
func stubInfoFn(version string) func(context.Context, string) (*pb.GetPluginInfoResponse, error) {
	return func(context.Context, string) (*pb.GetPluginInfoResponse, error) {
		return &pb.GetPluginInfoResponse{Version: version}, nil
	}
}

func TestListPlugins(t *testing.T) {
	dir := t.TempDir()
	terraformPath := filepath.Join(dir, pluginBinaryName("infracost-parser-terraform"))
	require.NoError(t, os.WriteFile(terraformPath, []byte("binary"), 0o700))

	cfg := &Config{Dir: dir}
	items := cfg.List()
	require.GreaterOrEqual(t, len(items), len(requiredPlugins))

	// The terraform entry is the first required entry. The fake binary
	// can't be spawned so version reports as "unknown" and the name
	// falls back to the required entry's DisplayName.
	terraform := items[0]
	assert.Equal(t, "terraform", terraform.Key)
	assert.Equal(t, "infracost/terraform", terraform.Name)
	assert.Equal(t, pluginTypeParser, terraform.Type)
	assert.True(t, terraform.Installed)
	assert.True(t, terraform.Required)
	assert.Equal(t, "unknown", terraform.Version)

	// Other required plugins are not installed.
	assert.False(t, items[1].Installed)
	assert.True(t, items[1].Required)
}

func TestListPlugins_IncludesExtraPlugins(t *testing.T) {
	dir := t.TempDir()
	// Create a third-party plugin not in the required set. It won't connect
	// (it's just a regular file, not a real plugin binary) but it should
	// still show up in the list as a non-required, installed entry.
	extraName := "infracost-plugin-custom"
	extraPath := filepath.Join(dir, pluginBinaryName(extraName))
	require.NoError(t, os.WriteFile(extraPath, []byte("binary"), 0o700))

	cfg := &Config{Dir: dir}
	items := cfg.List()

	var extra *ListItem
	for i := range items {
		if items[i].Name == extraName || items[i].Key == pluginBinaryName(extraName) {
			extra = &items[i]
			break
		}
	}
	require.NotNil(t, extra, "expected discovered extra plugin in list")
	assert.False(t, extra.Required)
	assert.True(t, extra.Installed)
}

func TestListPlugins_IgnoresLegacySidecars(t *testing.T) {
	dir := t.TempDir()
	terraformPath := filepath.Join(dir, pluginBinaryName("infracost-parser-terraform"))
	require.NoError(t, os.WriteFile(terraformPath, []byte("binary"), 0o700))
	// Legacy sidecars left over from a prior CLI version should not appear
	// as their own list entries.
	require.NoError(t, os.WriteFile(terraformPath+".sha256", []byte("abc"), 0o600))
	require.NoError(t, os.WriteFile(terraformPath+".version", []byte("1.0.0"), 0o600))

	cfg := &Config{Dir: dir}
	items := cfg.List()

	for _, item := range items {
		assert.NotContains(t, item.Path, ".sha256")
		assert.NotContains(t, item.Path, ".version")
	}
}

func TestUnpackTarGz(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		dir := t.TempDir()
		content := []byte("binary-content-here")
		archive := createTestTarGz(t, dir, "my-plugin", content)
		dest := filepath.Join(dir, "extracted")

		err := unpackTarGz(archive, dest, "my-plugin")
		require.NoError(t, err)

		got, err := os.ReadFile(dest)
		require.NoError(t, err)
		assert.Equal(t, content, got)
	})

	t.Run("missing entry", func(t *testing.T) {
		dir := t.TempDir()
		archive := createTestTarGz(t, dir, "other-file", []byte("data"))
		dest := filepath.Join(dir, "extracted")

		err := unpackTarGz(archive, dest, "my-plugin")
		assert.ErrorContains(t, err, "not found in archive")
	})
}

func TestUnpackZip(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		dir := t.TempDir()
		content := []byte("zip-binary-content")
		archive := createTestZip(t, dir, "my-plugin.exe", content)
		dest := filepath.Join(dir, "extracted")

		err := unpackZip(archive, dest, "my-plugin.exe")
		require.NoError(t, err)

		got, err := os.ReadFile(dest)
		require.NoError(t, err)
		assert.Equal(t, content, got)
	})

	t.Run("missing entry", func(t *testing.T) {
		dir := t.TempDir()
		archive := createTestZip(t, dir, "other.exe", []byte("data"))
		dest := filepath.Join(dir, "extracted")

		err := unpackZip(archive, dest, "my-plugin.exe")
		assert.ErrorContains(t, err, "not found in zip")
	})
}

func TestDownloadAndVerify(t *testing.T) {
	payload := []byte("test-download-payload")
	correctSHA := sha256HexString(payload)

	t.Run("correct SHA passes", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write(payload)
		}))
		defer srv.Close()

		path, err := downloadAndVerify(context.Background(), srv.URL, correctSHA)
		require.NoError(t, err)
		defer func() { _ = os.Remove(path) }()

		got, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Equal(t, payload, got)
	})

	t.Run("wrong SHA fails", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write(payload)
		}))
		defer srv.Close()

		path, err := downloadAndVerify(context.Background(), srv.URL, "0000000000000000000000000000000000000000000000000000000000000000")
		if path != "" {
			defer func() { _ = os.Remove(path) }()
		}
		assert.ErrorContains(t, err, "SHA256 mismatch")
	})

	t.Run("empty SHA skips check", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write(payload)
		}))
		defer srv.Close()

		path, err := downloadAndVerify(context.Background(), srv.URL, "")
		require.NoError(t, err)
		defer func() { _ = os.Remove(path) }()

		got, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Equal(t, payload, got)
	})

	t.Run("non-200 status fails", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		defer srv.Close()

		path, err := downloadAndVerify(context.Background(), srv.URL, "")
		if path != "" {
			defer func() { _ = os.Remove(path) }()
		}
		assert.ErrorContains(t, err, "unexpected HTTP status")
	})
}

func sha256HexString(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func TestFetchSHA256(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("abc123  data.tar.gz\n"))
		}))
		defer srv.Close()

		got, err := fetchSHA256(context.Background(), srv.URL)
		require.NoError(t, err)
		assert.Equal(t, "abc123", got)
	})

	t.Run("non-200 status fails", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		defer srv.Close()

		_, err := fetchSHA256(context.Background(), srv.URL)
		assert.ErrorContains(t, err, "unexpected HTTP status")
	})

	t.Run("empty response fails", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
		defer srv.Close()

		_, err := fetchSHA256(context.Background(), srv.URL)
		assert.ErrorContains(t, err, "empty checksum response")
	})
}

func TestPluginArtifactURL(t *testing.T) {
	m := &Manager{opts: ManagerOptions{BaseURL: "https://example.com/bucket/"}}
	got := m.pluginArtifactURL("test-plugin", "linux", "amd64", "latest", "data.tar.gz")
	assert.Equal(t, "https://example.com/bucket/test-plugin/linux/amd64/latest/data.tar.gz", got)
}

func TestInstall(t *testing.T) {
	pluginName := "test-plugin"
	binaryName := pluginBinaryName(pluginName)
	archiveName := pluginArchiveName()
	pluginContent := []byte("fake-binary-data")

	t.Run("specific version already cached", func(t *testing.T) {
		cacheDir := t.TempDir()
		binaryPath := filepath.Join(cacheDir, binaryName)
		require.NoError(t, os.WriteFile(binaryPath, pluginContent, 0750))

		m := NewManager(ManagerOptions{Cache: cacheDir, AutoUpdate: true})
		m.queryInfo = stubInfoFn("1.0.0")

		got, err := m.Install(context.Background(), pluginName, "1.0.0")
		require.NoError(t, err)
		assert.Equal(t, binaryPath, got)
	})

	t.Run("auto-update disabled returns existing flat binary", func(t *testing.T) {
		cacheDir := t.TempDir()
		binaryPath := filepath.Join(cacheDir, binaryName)
		require.NoError(t, os.WriteFile(binaryPath, pluginContent, 0750))

		m := NewManager(ManagerOptions{Cache: cacheDir, AutoUpdate: false})
		got, err := m.Install(context.Background(), pluginName, "")
		require.NoError(t, err)
		assert.Equal(t, binaryPath, got)
	})

	t.Run("dev version skips auto-update", func(t *testing.T) {
		cacheDir := t.TempDir()
		binaryPath := filepath.Join(cacheDir, binaryName)
		require.NoError(t, os.WriteFile(binaryPath, pluginContent, 0750))

		// Even with AutoUpdate on and a server that would happily resolve
		// a newer version, a plugin reporting "dev" should be left alone.
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			t.Errorf("server should not be contacted for dev build")
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer srv.Close()

		m := NewManager(ManagerOptions{Cache: cacheDir, BaseURL: srv.URL, AutoUpdate: true})
		m.queryInfo = stubInfoFn("dev")

		got, err := m.Install(context.Background(), pluginName, "")
		require.NoError(t, err)
		assert.Equal(t, binaryPath, got)
	})

	t.Run("replaces old nested cache directory with flat binary", func(t *testing.T) {
		archiveDir := t.TempDir()
		archivePath := createPluginArchive(t, archiveDir, archiveName, binaryName, pluginContent)
		archiveSHA := fileSHA256(t, archivePath)
		archiveData, err := os.ReadFile(archivePath)
		require.NoError(t, err)

		expectedArchivePath := fmt.Sprintf("/%s/%s/%s/latest/%s", pluginName, runtime.GOOS, runtime.GOARCH, archiveName)
		expectedVersionPath := fmt.Sprintf("/%s/%s/%s/latest/version", pluginName, runtime.GOOS, runtime.GOARCH)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case expectedVersionPath:
				_, _ = w.Write([]byte("0.0.1\n"))
			case expectedArchivePath + ".sha256":
				_, _ = w.Write([]byte(archiveSHA + "\n"))
			case expectedArchivePath:
				_, _ = w.Write(archiveData)
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		defer srv.Close()

		cacheDir := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(cacheDir, pluginName, runtime.GOOS+"_"+runtime.GOARCH, "latest"), 0750))

		m := NewManager(ManagerOptions{Cache: cacheDir, BaseURL: srv.URL, AutoUpdate: true})
		got, err := m.Install(context.Background(), pluginName, "")
		require.NoError(t, err)
		assert.Equal(t, filepath.Join(cacheDir, binaryName), got)

		info, err := os.Stat(got)
		require.NoError(t, err)
		assert.False(t, info.IsDir())
	})

	t.Run("successful download and install latest", func(t *testing.T) {
		archiveDir := t.TempDir()
		archivePath := createPluginArchive(t, archiveDir, archiveName, binaryName, pluginContent)
		archiveSHA := fileSHA256(t, archivePath)
		archiveData, err := os.ReadFile(archivePath)
		require.NoError(t, err)

		expectedArchivePath := fmt.Sprintf("/%s/%s/%s/latest/%s", pluginName, runtime.GOOS, runtime.GOARCH, archiveName)
		expectedVersionPath := fmt.Sprintf("/%s/%s/%s/latest/version", pluginName, runtime.GOOS, runtime.GOARCH)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case expectedVersionPath:
				_, _ = w.Write([]byte("0.0.1\n"))
			case expectedArchivePath + ".sha256":
				_, _ = w.Write([]byte(archiveSHA + "  " + archiveName + "\n"))
			case expectedArchivePath:
				_, _ = w.Write(archiveData)
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		defer srv.Close()

		cacheDir := t.TempDir()
		m := NewManager(ManagerOptions{Cache: cacheDir, BaseURL: srv.URL, AutoUpdate: true})
		got, err := m.Install(context.Background(), pluginName, "")
		require.NoError(t, err)

		expected := filepath.Join(cacheDir, binaryName)
		assert.Equal(t, expected, got)

		data, err := os.ReadFile(got)
		require.NoError(t, err)
		assert.Equal(t, pluginContent, data)

		// No sidecar files should be written.
		_, err = os.Stat(got + ".sha256")
		assert.True(t, os.IsNotExist(err), "expected .sha256 sidecar not to be written")
		_, err = os.Stat(got + ".version")
		assert.True(t, os.IsNotExist(err), "expected .version sidecar not to be written")
	})

	t.Run("successful download and install specific version", func(t *testing.T) {
		archiveDir := t.TempDir()
		archivePath := createPluginArchive(t, archiveDir, archiveName, binaryName, pluginContent)
		archiveSHA := fileSHA256(t, archivePath)
		archiveData, err := os.ReadFile(archivePath)
		require.NoError(t, err)

		expectedArchivePath := fmt.Sprintf("/%s/%s/%s/2.0.0/%s", pluginName, runtime.GOOS, runtime.GOARCH, archiveName)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case expectedArchivePath + ".sha256":
				_, _ = w.Write([]byte(archiveSHA + "\n"))
			case expectedArchivePath:
				_, _ = w.Write(archiveData)
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		defer srv.Close()

		cacheDir := t.TempDir()
		m := NewManager(ManagerOptions{Cache: cacheDir, BaseURL: srv.URL, AutoUpdate: true})
		got, err := m.Install(context.Background(), pluginName, "2.0.0")
		require.NoError(t, err)

		expected := filepath.Join(cacheDir, binaryName)
		assert.Equal(t, expected, got)
	})

	t.Run("latest cached with matching version skips checksum and download", func(t *testing.T) {
		cacheDir := t.TempDir()
		binaryPath := filepath.Join(cacheDir, binaryName)
		require.NoError(t, os.WriteFile(binaryPath, pluginContent, 0750))

		checksumFetches := 0
		downloads := 0
		expectedArchivePath := fmt.Sprintf("/%s/%s/%s/latest/%s", pluginName, runtime.GOOS, runtime.GOARCH, archiveName)
		expectedVersionPath := fmt.Sprintf("/%s/%s/%s/latest/version", pluginName, runtime.GOOS, runtime.GOARCH)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case expectedVersionPath:
				_, _ = w.Write([]byte("0.0.1\n"))
			case expectedArchivePath + ".sha256":
				checksumFetches++
				w.WriteHeader(http.StatusInternalServerError)
			case expectedArchivePath:
				downloads++
				w.WriteHeader(http.StatusInternalServerError)
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		defer srv.Close()

		m := NewManager(ManagerOptions{Cache: cacheDir, BaseURL: srv.URL, AutoUpdate: true})
		m.queryInfo = stubInfoFn("0.0.1")

		got, err := m.Install(context.Background(), pluginName, "")
		require.NoError(t, err)
		assert.Equal(t, binaryPath, got)
		assert.Equal(t, 0, checksumFetches)
		assert.Equal(t, 0, downloads)
	})

	t.Run("version not found fails", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		defer srv.Close()

		m := NewManager(ManagerOptions{Cache: t.TempDir(), BaseURL: srv.URL, AutoUpdate: true})
		_, err := m.Install(context.Background(), pluginName, "")
		assert.ErrorContains(t, err, "failed to fetch plugin version")
	})
}

func TestEnsureInstalled(t *testing.T) {
	t.Run("plugin dir override skips downloads", func(t *testing.T) {
		dir := t.TempDir()
		c := &Config{Dir: dir, BaseURL: "http://127.0.0.1:1", AutoUpdate: true}

		mgr, err := c.EnsurePlugins(context.Background())
		require.NoError(t, err)
		require.NotNil(t, mgr)
	})

	t.Run("installs plugins into flat directory", func(t *testing.T) {
		archiveName := pluginArchiveName()
		archiveData := make(map[string][]byte)
		archiveSHA := make(map[string]string)
		archiveDir := t.TempDir()
		for _, required := range requiredPlugins {
			archivePath := createPluginArchive(t, archiveDir, archiveName+"-"+required.Name, pluginBinaryName(required.Name), []byte(required.Name))
			data, err := os.ReadFile(archivePath)
			require.NoError(t, err)
			archiveData[required.Name] = data
			archiveSHA[required.Name] = fileSHA256(t, archivePath)
		}

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
			if len(parts) != 5 || parts[1] != runtime.GOOS || parts[2] != runtime.GOARCH || parts[3] != "latest" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			name := parts[0]
			if _, ok := archiveData[name]; !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			if parts[4] == "version" {
				_, _ = w.Write([]byte("0.0.1\n"))
				return
			}
			if parts[4] == archiveName+".sha256" {
				_, _ = w.Write([]byte(archiveSHA[name] + "\n"))
				return
			}
			if parts[4] == archiveName {
				_, _ = w.Write(archiveData[name])
				return
			}
			w.WriteHeader(http.StatusNotFound)
		}))
		defer srv.Close()

		cacheDir := t.TempDir()
		// Every required plugin is downloaded unconditionally.
		c := &Config{Cache: cacheDir, BaseURL: srv.URL, AutoUpdate: true}
		_, err := c.EnsurePlugins(context.Background())
		require.NoError(t, err)

		for _, required := range requiredPlugins {
			path := filepath.Join(cacheDir, pluginBinaryName(required.Name))
			data, err := os.ReadFile(path)
			require.NoError(t, err)
			assert.Equal(t, []byte(required.Name), data)
		}
	})
}

func TestUpdatePlugins(t *testing.T) {
	t.Run("plugin dir override returns an error", func(t *testing.T) {
		c := &Config{Dir: t.TempDir(), BaseURL: "http://127.0.0.1:1"}

		err := c.UpdatePlugins(context.Background())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "INFRACOST_CLI_PLUGIN_DIR")
	})

	t.Run("updates all required plugins ignoring AutoUpdate", func(t *testing.T) {
		archiveName := pluginArchiveName()
		archiveData := make(map[string][]byte)
		archiveSHA := make(map[string]string)
		archiveDir := t.TempDir()
		for _, required := range requiredPlugins {
			archivePath := createPluginArchive(t, archiveDir, archiveName+"-"+required.Name, pluginBinaryName(required.Name), []byte(required.Name+"-v2"))
			data, err := os.ReadFile(archivePath)
			require.NoError(t, err)
			archiveData[required.Name] = data
			archiveSHA[required.Name] = fileSHA256(t, archivePath)
		}

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
			if len(parts) != 5 || parts[1] != runtime.GOOS || parts[2] != runtime.GOARCH || parts[3] != "latest" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			name := parts[0]
			if _, ok := archiveData[name]; !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			switch parts[4] {
			case "version":
				_, _ = w.Write([]byte("0.0.2\n"))
			case archiveName + ".sha256":
				_, _ = w.Write([]byte(archiveSHA[name] + "\n"))
			case archiveName:
				_, _ = w.Write(archiveData[name])
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		defer srv.Close()

		cacheDir := t.TempDir()
		// AutoUpdate deliberately left false: UpdatePlugins must force it on and
		// still download every required plugin.
		c := &Config{Cache: cacheDir, BaseURL: srv.URL, AutoUpdate: false}
		require.NoError(t, c.UpdatePlugins(context.Background()))

		for _, required := range requiredPlugins {
			path := filepath.Join(cacheDir, pluginBinaryName(required.Name))
			data, err := os.ReadFile(path)
			require.NoError(t, err)
			assert.Equal(t, []byte(required.Name+"-v2"), data)
		}
	})
}

func TestProviderPluginsReturnsAll(t *testing.T) {
	providers := []*ProviderPlugin{
		{Info: &pb.GetPluginInfoResponse{Name: "infracost/aws"}},
		{Info: &pb.GetPluginInfoResponse{Name: "infracost/kubernetes"}},
	}
	c := &Config{LoadProviderPlugins: func(context.Context) ([]*ProviderPlugin, error) { return providers, nil }}

	got, err := c.ProviderPlugins(context.Background())
	require.NoError(t, err)

	names := make([]string, len(got))
	for i, p := range got {
		names[i] = p.Info.GetName()
	}
	assert.ElementsMatch(t, []string{"infracost/aws", "infracost/kubernetes"}, names)
}

func TestRequiredPluginVersionEnv(t *testing.T) {
	t.Setenv("INFRACOST_CLI_PLUGIN_TERRAFORM_VERSION", "v9.9.9")
	assert.Equal(t, "v9.9.9", requiredPluginVersion("terraform"))
	assert.Equal(t, "", requiredPluginVersion("not-set"))
}

func TestProcess(t *testing.T) {
	t.Run("sets Cache when empty", func(t *testing.T) {
		c := &Config{}
		c.Process()
		assert.NotEmpty(t, c.Cache)
	})

	t.Run("preserves Cache when set", func(t *testing.T) {
		c := &Config{Cache: "/my/cache"}
		c.Process()
		assert.Equal(t, "/my/cache", c.Cache)
	})
}
