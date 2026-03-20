package plugins

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
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

func TestLoadManifest(t *testing.T) {
	manifest := Manifest{
		Plugins: map[string]Plugin{
			"test-plugin": {Latest: "1.0.0"},
		},
	}

	t.Run("success", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(manifest)
		}))
		defer srv.Close()

		c := &Config{ManifestURL: srv.URL}
		got, err := c.loadManifest()
		require.NoError(t, err)
		assert.Equal(t, "1.0.0", got.Plugins["test-plugin"].Latest)
	})

	t.Run("caching", func(t *testing.T) {
		calls := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			calls++
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(manifest)
		}))
		defer srv.Close()

		c := &Config{ManifestURL: srv.URL}
		first, err := c.loadManifest()
		require.NoError(t, err)
		second, err := c.loadManifest()
		require.NoError(t, err)

		assert.Same(t, first, second)
		assert.Equal(t, 1, calls)
	})

	t.Run("non-200 status fails", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer srv.Close()

		c := &Config{ManifestURL: srv.URL}
		_, err := c.loadManifest()
		assert.ErrorContains(t, err, "failed to fetch plugin manifest")
	})

	t.Run("invalid JSON fails", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("not json"))
		}))
		defer srv.Close()

		c := &Config{ManifestURL: srv.URL}
		_, err := c.loadManifest()
		assert.Error(t, err)
	})
}

func TestEnsure(t *testing.T) {
	platform := runtime.GOOS + "_" + runtime.GOARCH
	pluginName := "test-plugin"
	pluginContent := []byte("fake-binary-data")

	t.Run("specific version already cached", func(t *testing.T) {
		cacheDir := t.TempDir()
		binaryDir := filepath.Join(cacheDir, pluginName, platform, "1.0.0")
		require.NoError(t, os.MkdirAll(binaryDir, 0750))
		binaryPath := filepath.Join(binaryDir, pluginName)
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
			require.NoError(t, os.WriteFile(filepath.Join(dir, pluginName), []byte(ver), 0750))
		}

		c := &Config{Cache: cacheDir, AutoUpdate: false}
		got, err := c.Ensure(pluginName, "")
		require.NoError(t, err)
		assert.Equal(t, filepath.Join(cacheDir, pluginName, platform, "0.3.0", pluginName), got)
	})

	t.Run("plugin not in manifest", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(Manifest{Plugins: map[string]Plugin{}})
		}))
		defer srv.Close()

		c := &Config{Cache: t.TempDir(), ManifestURL: srv.URL, AutoUpdate: true}
		_, err := c.Ensure("nonexistent", "")
		assert.ErrorContains(t, err, "not found in manifest")
	})

	t.Run("version not in manifest", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(Manifest{
				Plugins: map[string]Plugin{
					pluginName: {Latest: "1.0.0", Versions: map[string]Version{}},
				},
			})
		}))
		defer srv.Close()

		c := &Config{Cache: t.TempDir(), ManifestURL: srv.URL, AutoUpdate: true}
		_, err := c.Ensure(pluginName, "9.9.9")
		assert.ErrorContains(t, err, "version \"9.9.9\" not found")
	})

	t.Run("no artifact for platform", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(Manifest{
				Plugins: map[string]Plugin{
					pluginName: {
						Latest: "1.0.0",
						Versions: map[string]Version{
							"1.0.0": {Artifacts: map[string]Artifact{}},
						},
					},
				},
			})
		}))
		defer srv.Close()

		c := &Config{Cache: t.TempDir(), ManifestURL: srv.URL, AutoUpdate: true}
		_, err := c.Ensure(pluginName, "")
		assert.ErrorContains(t, err, "no artifact for")
	})

	t.Run("successful download and install tar.gz", func(t *testing.T) {
		archiveDir := t.TempDir()
		archivePath := createTestTarGz(t, archiveDir, pluginName, pluginContent)
		archiveSHA := fileSHA256(t, archivePath)
		archiveData, err := os.ReadFile(archivePath)
		require.NoError(t, err)

		var srvURL string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/manifest.json":
				_ = json.NewEncoder(w).Encode(Manifest{
					Plugins: map[string]Plugin{
						pluginName: {
							Latest: "2.0.0",
							Versions: map[string]Version{
								"2.0.0": {
									Artifacts: map[string]Artifact{
										platform: {
											URL:  srvURL + "/download",
											SHA:  archiveSHA,
											Name: pluginName + ".tar.gz",
										},
									},
								},
							},
						},
					},
				})
			case "/download":
				_, _ = w.Write(archiveData)
			}
		}))
		defer srv.Close()
		srvURL = srv.URL

		cacheDir := t.TempDir()
		c := &Config{Cache: cacheDir, ManifestURL: srv.URL + "/manifest.json", AutoUpdate: true}
		got, err := c.Ensure(pluginName, "")
		require.NoError(t, err)

		expected := filepath.Join(cacheDir, pluginName, platform, "2.0.0", pluginName)
		assert.Equal(t, expected, got)

		data, err := os.ReadFile(got)
		require.NoError(t, err)
		assert.Equal(t, pluginContent, data)
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
		content := []byte("parser-binary")

		archiveDir := t.TempDir()
		archivePath := createTestTarGz(t, archiveDir, "infracost-parser-plugin", content)
		archiveSHA := fileSHA256(t, archivePath)
		archiveData, err := os.ReadFile(archivePath)
		require.NoError(t, err)

		var srvURL string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/manifest.json":
				_ = json.NewEncoder(w).Encode(Manifest{
					Plugins: map[string]Plugin{
						"infracost-parser-plugin": {
							Latest: "1.0.0",
							Versions: map[string]Version{
								"1.0.0": {
									Artifacts: map[string]Artifact{
										platform: {
											URL:  srvURL + "/download",
											SHA:  archiveSHA,
											Name: "infracost-parser-plugin.tar.gz",
										},
									},
								},
							},
						},
					},
				})
			case "/download":
				_, _ = w.Write(archiveData)
			}
		}))
		defer srv.Close()
		srvURL = srv.URL

		cacheDir := t.TempDir()
		c := &Config{Cache: cacheDir, ManifestURL: srv.URL + "/manifest.json", AutoUpdate: true}

		err = c.EnsureParser()
		require.NoError(t, err)
		assert.NotEmpty(t, c.Parser.Plugin)
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
		platform := runtime.GOOS + "_" + runtime.GOARCH
		content := []byte("provider-binary")
		pluginName := "infracost-provider-plugin-"

		archiveDir := t.TempDir()
		archivePath := createTestTarGz(t, archiveDir, pluginName, content)
		archiveSHA := fileSHA256(t, archivePath)
		archiveData, err := os.ReadFile(archivePath)
		require.NoError(t, err)

		var srvURL string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/manifest.json":
				_ = json.NewEncoder(w).Encode(Manifest{
					Plugins: map[string]Plugin{
						pluginName: {
							Latest: "1.0.0",
							Versions: map[string]Version{
								"1.0.0": {
									Artifacts: map[string]Artifact{
										platform: {
											URL:  srvURL + "/download",
											SHA:  archiveSHA,
											Name: pluginName + ".tar.gz",
										},
									},
								},
							},
						},
					},
				})
			case "/download":
				_, _ = w.Write(archiveData)
			}
		}))
		defer srv.Close()
		srvURL = srv.URL

		c := &Config{Cache: t.TempDir(), ManifestURL: srv.URL + "/manifest.json", AutoUpdate: true}
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