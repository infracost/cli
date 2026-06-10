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
	platform := runtime.GOOS + "_" + runtime.GOARCH
	pluginName := "test-plugin"
	binaryName := pluginBinaryName(pluginName)
	archiveName := pluginArchiveName()
	pluginContent := []byte("fake-binary-data")

	t.Run("specific version already cached", func(t *testing.T) {
		cacheDir := t.TempDir()
		binaryDir := filepath.Join(cacheDir, pluginName, platform, "1.0.0")
		require.NoError(t, os.MkdirAll(binaryDir, 0750))
		binaryPath := filepath.Join(binaryDir, binaryName)
		require.NoError(t, os.WriteFile(binaryPath, pluginContent, 0750))

		c := &Config{Cache: cacheDir}
		got, err := c.Ensure(pluginName, "1.0.0")
		require.NoError(t, err)
		assert.Equal(t, binaryPath, got)
	})

	t.Run("auto-update disabled returns latest cached semver", func(t *testing.T) {
		cacheDir := t.TempDir()
		for _, ver := range []string{"0.1.0", "0.3.0", "0.2.0"} {
			dir := filepath.Join(cacheDir, pluginName, platform, ver)
			require.NoError(t, os.MkdirAll(dir, 0750))
			require.NoError(t, os.WriteFile(filepath.Join(dir, binaryName), []byte(ver), 0750))
		}

		c := &Config{Cache: cacheDir, AutoUpdate: false}
		got, err := c.Ensure(pluginName, "")
		require.NoError(t, err)
		assert.Equal(t, filepath.Join(cacheDir, pluginName, platform, "0.3.0", binaryName), got)
	})

	t.Run("successful download and install latest", func(t *testing.T) {
		archiveDir := t.TempDir()
		archivePath := createPluginArchive(t, archiveDir, archiveName, binaryName, pluginContent)
		archiveSHA := fileSHA256(t, archivePath)
		archiveData, err := os.ReadFile(archivePath)
		require.NoError(t, err)

		expectedArchivePath := fmt.Sprintf("/%s/%s/%s/latest/%s", pluginName, runtime.GOOS, runtime.GOARCH, archiveName)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
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

		expected := filepath.Join(cacheDir, pluginName, platform, "latest", binaryName)
		assert.Equal(t, expected, got)

		data, err := os.ReadFile(got)
		require.NoError(t, err)
		assert.Equal(t, pluginContent, data)
		assert.Equal(t, archiveSHA, cachedPluginSHA(got))
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

		expected := filepath.Join(cacheDir, pluginName, platform, "2.0.0", binaryName)
		assert.Equal(t, expected, got)
	})

	t.Run("latest cached with matching checksum skips download", func(t *testing.T) {
		cacheDir := t.TempDir()
		binaryDir := filepath.Join(cacheDir, pluginName, platform, "latest")
		require.NoError(t, os.MkdirAll(binaryDir, 0750))
		binaryPath := filepath.Join(binaryDir, binaryName)
		require.NoError(t, os.WriteFile(binaryPath, pluginContent, 0750))
		require.NoError(t, os.WriteFile(binaryPath+".sha256", []byte("abc123\n"), 0600))

		downloads := 0
		expectedArchivePath := fmt.Sprintf("/%s/%s/%s/latest/%s", pluginName, runtime.GOOS, runtime.GOARCH, archiveName)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case expectedArchivePath + ".sha256":
				_, _ = w.Write([]byte("abc123\n"))
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
		assert.Equal(t, 0, downloads)
	})

	t.Run("checksum not found fails", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		defer srv.Close()

		c := &Config{Cache: t.TempDir(), BaseURL: srv.URL, AutoUpdate: true}
		_, err := c.Ensure(pluginName, "")
		assert.ErrorContains(t, err, "failed to fetch plugin checksum")
	})
}

func TestEnsureParser(t *testing.T) {
	t.Run("already set returns nil", func(t *testing.T) {
		c := &Config{}
		c.Parser.Plugin = "/some/path"

		err := c.EnsureParser()
		assert.NoError(t, err)
		assert.Equal(t, "/some/path", c.Parser.Plugin)
	})

	t.Run("not set calls Ensure", func(t *testing.T) {
		platform := runtime.GOOS + "_" + runtime.GOARCH
		pluginName := parserPluginName
		binaryName := pluginBinaryName(pluginName)
		archiveName := pluginArchiveName()
		content := []byte("parser-binary")

		archiveDir := t.TempDir()
		archivePath := createPluginArchive(t, archiveDir, archiveName, binaryName, content)
		archiveSHA := fileSHA256(t, archivePath)
		archiveData, err := os.ReadFile(archivePath)
		require.NoError(t, err)

		expectedArchivePath := fmt.Sprintf("/%s/%s/%s/latest/%s", pluginName, runtime.GOOS, runtime.GOARCH, archiveName)
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

		err = c.EnsureParser()
		require.NoError(t, err)
		assert.Equal(t, filepath.Join(cacheDir, pluginName, platform, "latest", binaryName), c.Parser.Plugin)
	})
}

func TestEnsureProvider(t *testing.T) {
	t.Run("override set returns nil", func(t *testing.T) {
		c := &Config{}
		c.Providers.AWS = "/custom/aws"

		err := c.EnsureProvider(proto.Provider_PROVIDER_AWS)
		assert.NoError(t, err)
		assert.Equal(t, "/custom/aws", c.Providers.AWS)
	})

	t.Run("unknown provider returns error", func(t *testing.T) {
		content := []byte("provider-binary")
		pluginName := "infracost-provider-plugin-"
		binaryName := pluginBinaryName(pluginName)
		archiveName := pluginArchiveName()

		archiveDir := t.TempDir()
		archivePath := createPluginArchive(t, archiveDir, archiveName, binaryName, content)
		archiveSHA := fileSHA256(t, archivePath)
		archiveData, err := os.ReadFile(archivePath)
		require.NoError(t, err)

		expectedArchivePath := fmt.Sprintf("/%s/%s/%s/latest/%s", pluginName, runtime.GOOS, runtime.GOARCH, archiveName)
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

		c := &Config{Cache: t.TempDir(), BaseURL: srv.URL, AutoUpdate: true}
		err = c.EnsureProvider(proto.Provider_PROVIDER_UNSPECIFIED)
		assert.ErrorContains(t, err, "unknown provider")
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
