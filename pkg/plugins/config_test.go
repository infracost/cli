package plugins

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
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

	"github.com/infracost/cli/pkg/plugins/providers"
	proto "github.com/infracost/proto/gen/go/infracost/provider"
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

		path, err := downloadAndVerify(srv.URL, correctSHA)
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

		path, err := downloadAndVerify(srv.URL, "0000000000000000000000000000000000000000000000000000000000000000")
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

		path, err := downloadAndVerify(srv.URL, "")
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

		path, err := downloadAndVerify(srv.URL, "")
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

		got, err := fetchSHA256(srv.URL)
		require.NoError(t, err)
		assert.Equal(t, "abc123", got)
	})

	t.Run("non-200 status fails", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		defer srv.Close()

		_, err := fetchSHA256(srv.URL)
		assert.ErrorContains(t, err, "unexpected HTTP status")
	})

	t.Run("empty response fails", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
		defer srv.Close()

		_, err := fetchSHA256(srv.URL)
		assert.ErrorContains(t, err, "empty checksum response")
	})
}

func TestPluginArtifactURL(t *testing.T) {
	c := Config{BaseURL: "https://example.com/bucket/"}
	got := c.pluginArtifactURL("test-plugin", "linux", "amd64", "latest", "data.tar.gz")
	assert.Equal(t, "https://example.com/bucket/test-plugin/linux/amd64/latest/data.tar.gz", got)
}

func TestEnsure(t *testing.T) {
	pluginName := "test-plugin"
	binaryName := pluginBinaryName(pluginName)
	archiveName := pluginArchiveName()
	pluginContent := []byte("fake-binary-data")

	t.Run("specific version already cached", func(t *testing.T) {
		cacheDir := t.TempDir()
		binaryPath := filepath.Join(cacheDir, binaryName)
		require.NoError(t, os.WriteFile(binaryPath, pluginContent, 0750))
		require.NoError(t, os.WriteFile(binaryPath+".version", []byte("1.0.0\n"), 0600))

		c := &Config{Cache: cacheDir, AutoUpdate: true}
		got, err := c.Ensure(pluginName, "1.0.0")
		require.NoError(t, err)
		assert.Equal(t, binaryPath, got)
	})

	t.Run("auto-update disabled returns existing flat binary", func(t *testing.T) {
		cacheDir := t.TempDir()
		binaryPath := filepath.Join(cacheDir, binaryName)
		require.NoError(t, os.WriteFile(binaryPath, pluginContent, 0750))

		c := &Config{Cache: cacheDir, AutoUpdate: false}
		got, err := c.Ensure(pluginName, "")
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

		c := &Config{Cache: cacheDir, BaseURL: srv.URL, AutoUpdate: true}
		got, err := c.Ensure(pluginName, "")
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
		c := &Config{Cache: cacheDir, BaseURL: srv.URL, AutoUpdate: true}
		got, err := c.Ensure(pluginName, "")
		require.NoError(t, err)

		expected := filepath.Join(cacheDir, binaryName)
		assert.Equal(t, expected, got)

		data, err := os.ReadFile(got)
		require.NoError(t, err)
		assert.Equal(t, pluginContent, data)
		assert.Equal(t, archiveSHA, cachedPluginSHA(got))
		assert.Equal(t, "0.0.1", cachedPluginVersion(got))
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
		c := &Config{Cache: cacheDir, BaseURL: srv.URL, AutoUpdate: true}
		got, err := c.Ensure(pluginName, "2.0.0")
		require.NoError(t, err)

		expected := filepath.Join(cacheDir, binaryName)
		assert.Equal(t, expected, got)
		assert.Equal(t, "2.0.0", cachedPluginVersion(got))
	})

	t.Run("latest cached with matching version skips checksum and download", func(t *testing.T) {
		cacheDir := t.TempDir()
		binaryPath := filepath.Join(cacheDir, binaryName)
		require.NoError(t, os.WriteFile(binaryPath, pluginContent, 0750))
		require.NoError(t, os.WriteFile(binaryPath+".version", []byte("0.0.1\n"), 0600))

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

		c := &Config{Cache: cacheDir, BaseURL: srv.URL, AutoUpdate: true}
		got, err := c.Ensure(pluginName, "")
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

		c := &Config{Cache: t.TempDir(), BaseURL: srv.URL, AutoUpdate: true}
		_, err := c.Ensure(pluginName, "")
		assert.ErrorContains(t, err, "failed to fetch plugin version")
	})
}

func TestEnsurePlugins(t *testing.T) {
	t.Run("plugin dir override skips downloads and sets flat paths", func(t *testing.T) {
		dir := t.TempDir()
		c := &Config{Dir: dir, BaseURL: "http://127.0.0.1:1", AutoUpdate: true}

		mgr, err := c.EnsurePlugins()
		require.NoError(t, err)
		require.NotNil(t, mgr)

		assert.Equal(t, filepath.Join(dir, pluginBinaryName(parserPluginName)), c.Parser.Plugin)
		assert.Equal(t, filepath.Join(dir, pluginBinaryName("infracost-plugin-aws")), c.Providers.AWS)
		assert.Equal(t, filepath.Join(dir, pluginBinaryName("infracost-plugin-google")), c.Providers.Google)
		assert.Equal(t, filepath.Join(dir, pluginBinaryName("infracost-plugin-azure")), c.Providers.Azure)
	})

	t.Run("installs parser plugins into flat directory", func(t *testing.T) {
		archiveName := pluginArchiveName()
		archiveData := make(map[string][]byte)
		archiveSHA := make(map[string]string)
		archiveDir := t.TempDir()
		for _, spec := range pluginSpecs {
			if spec.Type != pluginTypeParser {
				continue
			}
			archivePath := createPluginArchive(t, archiveDir, archiveName+"-"+spec.Name, pluginBinaryName(spec.Name), []byte(spec.Name))
			data, err := os.ReadFile(archivePath)
			require.NoError(t, err)
			archiveData[spec.Name] = data
			archiveSHA[spec.Name] = fileSHA256(t, archivePath)
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
		c := &Config{Cache: cacheDir, BaseURL: srv.URL, AutoUpdate: true}
		_, err := c.EnsurePlugins()
		require.NoError(t, err)

		for _, spec := range pluginSpecs {
			path := filepath.Join(cacheDir, pluginBinaryName(spec.Name))
			if spec.Type != pluginTypeParser {
				assert.NoFileExists(t, path)
				continue
			}
			data, err := os.ReadFile(path)
			require.NoError(t, err)
			assert.Equal(t, []byte(spec.Name), data)
			assert.Equal(t, archiveSHA[spec.Name], cachedPluginSHA(path))
		}
	})

	t.Run("installs provider plugin on demand", func(t *testing.T) {
		archiveName := pluginArchiveName()
		spec := providerSpec(proto.Provider_PROVIDER_AWS)
		archiveDir := t.TempDir()
		archivePath := createPluginArchive(t, archiveDir, archiveName, pluginBinaryName(spec.Name), []byte(spec.Name))
		archiveSHA := fileSHA256(t, archivePath)
		archiveData, err := os.ReadFile(archivePath)
		require.NoError(t, err)

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			expectedPrefix := fmt.Sprintf("/%s/%s/%s/latest/", spec.Name, runtime.GOOS, runtime.GOARCH)
			switch r.URL.Path {
			case expectedPrefix + "version":
				_, _ = w.Write([]byte("0.0.1\n"))
			case expectedPrefix + archiveName + ".sha256":
				_, _ = w.Write([]byte(archiveSHA + "\n"))
			case expectedPrefix + archiveName:
				_, _ = w.Write(archiveData)
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		defer srv.Close()

		cacheDir := t.TempDir()
		c := &Config{Cache: cacheDir, BaseURL: srv.URL, AutoUpdate: true}
		err = c.EnsureProvider(proto.Provider_PROVIDER_AWS)
		require.NoError(t, err)
		assert.Equal(t, filepath.Join(cacheDir, pluginBinaryName(spec.Name)), c.Providers.AWS)
		assert.NoFileExists(t, filepath.Join(cacheDir, pluginBinaryName(parserPluginName)))
	})

	t.Run("unknown provider returns error", func(t *testing.T) {
		c := &Config{Cache: t.TempDir(), AutoUpdate: true}
		err := c.EnsureProvider(proto.Provider_PROVIDER_UNSPECIFIED)
		assert.ErrorContains(t, err, "unknown provider")
	})
}

func TestPluginVersion(t *testing.T) {
	t.Run("generic per-plugin env wins", func(t *testing.T) {
		t.Setenv("INFRACOST_CLI_PLUGIN_TERRAFORM_VERSION", "v9.9.9")
		c := &Config{}
		assert.Equal(t, "v9.9.9", c.pluginVersion(pluginSpecs[0]))
	})

	t.Run("legacy parser version is parser fallback", func(t *testing.T) {
		c := &Config{}
		c.Parser.Version = "v1.2.3"
		assert.Equal(t, "v1.2.3", c.pluginVersion(pluginSpecs[1]))
	})

	t.Run("legacy provider version is provider fallback", func(t *testing.T) {
		c := &Config{Providers: makeProvidersConfig("", "", "", "v4.5.6", "", "")}
		assert.Equal(t, "v4.5.6", c.pluginVersion(providerSpec(proto.Provider_PROVIDER_AWS)))
	})
}

func TestProviderOverride(t *testing.T) {
	tests := []struct {
		name         string
		config       Config
		provider     proto.Provider
		wantOverride string
		wantVersion  string
	}{
		{
			name:         "AWS",
			config:       Config{Providers: makeProvidersConfig("aws-path", "", "", "v1", "", "")},
			provider:     proto.Provider_PROVIDER_AWS,
			wantOverride: "aws-path",
			wantVersion:  "v1",
		},
		{
			name:         "Google",
			config:       Config{Providers: makeProvidersConfig("", "google-path", "", "", "v2", "")},
			provider:     proto.Provider_PROVIDER_GOOGLE,
			wantOverride: "google-path",
			wantVersion:  "v2",
		},
		{
			name:         "Azure",
			config:       Config{Providers: makeProvidersConfig("", "", "azure-path", "", "", "v3")},
			provider:     proto.Provider_PROVIDER_AZURERM,
			wantOverride: "azure-path",
			wantVersion:  "v3",
		},
		{
			name:         "unknown returns empty",
			config:       Config{},
			provider:     proto.Provider_PROVIDER_UNSPECIFIED,
			wantOverride: "",
			wantVersion:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			override, version := tt.config.providerOverride(tt.provider)
			assert.Equal(t, tt.wantOverride, override)
			assert.Equal(t, tt.wantVersion, version)
		})
	}
}

func makeProvidersConfig(aws, google, azure, awsVer, googleVer, azureVer string) providers.Config {
	return providers.Config{
		AWS: aws, Google: google, Azure: azure,
		AWSVersion: awsVer, GoogleVersion: googleVer, AzureVersion: azureVer,
	}
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
