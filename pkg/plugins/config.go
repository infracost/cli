package plugins

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/infracost/cli/pkg/config/process"
	"github.com/infracost/cli/pkg/logging"
	"github.com/infracost/cli/pkg/plugins/parser"
	"github.com/infracost/cli/pkg/plugins/providers"
	providerconv "github.com/infracost/go-proto/pkg/providers"
	proto "github.com/infracost/proto/gen/go/infracost/provider"
	"golang.org/x/mod/semver"
)

var (
	_ process.Processor = (*Config)(nil)
)

// maxPluginSize is the maximum allowed size for an extracted plugin binary (1 GB).
const maxPluginSize = 1 << 30

type Config struct {
	Providers providers.Config
	Parser    parser.Config

	// ManifestURL points to where the plugin manifest can be retrieved.
	ManifestURL string `env:"INFRACOST_CLI_PLUGIN_MANIFEST_URL" default:"https://releases.infracost.io/plugins/manifest.json"`

	// Cache is where the plugins should go.
	Cache string `env:"INFRACOST_CLI_PLUGIN_CACHE_DIRECTORY"`

	// AutoUpdate controls whether plugins are always updated to the latest
	// version. When false, an existing cached version is used if available.
	AutoUpdate bool `env:"INFRACOST_CLI_PLUGIN_AUTO_UPDATE" default:"true"`

	// cached, loaded as needed via loadManifest()
	manifest *Manifest
}

func (c *Config) Process() {
	if len(c.Cache) == 0 {
		c.Cache = defaultPluginCachePath()
	}
}

func (c *Config) EnsureParser() error {
	if c.Parser.Plugin != "" {
		return nil
	}

	path, err := c.Ensure("infracost-parser-plugin", c.Parser.Version)
	if err != nil {
		return err
	}
	c.Parser.Plugin = path
	return nil
}

func (c *Config) EnsureProvider(provider proto.Provider) error {
	override, version := c.providerOverride(provider)
	if override != "" {
		return nil
	}

	path, err := c.Ensure(fmt.Sprintf("infracost-provider-plugin-%s", providerconv.FromProto(provider)), version)
	if err != nil {
		return err
	}

	switch provider {
	case proto.Provider_PROVIDER_GOOGLE:
		c.Providers.Google = path
	case proto.Provider_PROVIDER_AWS:
		c.Providers.AWS = path
	case proto.Provider_PROVIDER_AZURERM:
		c.Providers.Azure = path
	default:
		return fmt.Errorf("unknown provider: %s", providerconv.FromProto(provider))
	}

	return nil
}

func (c *Config) providerOverride(provider proto.Provider) (string, string) {
	switch provider {
	case proto.Provider_PROVIDER_AWS:
		return c.Providers.AWS, c.Providers.AWSVersion
	case proto.Provider_PROVIDER_GOOGLE:
		return c.Providers.Google, c.Providers.GoogleVersion
	case proto.Provider_PROVIDER_AZURERM:
		return c.Providers.Azure, c.Providers.AzureVersion
	default:
		return "", ""
	}
}

// pluginBinaryName returns the binary filename for the given plugin name,
// appending .exe on Windows where executables require the extension.
func pluginBinaryName(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

func (c *Config) Ensure(plugin, wantVersion string) (string, error) {
	logging.Debugf("ensuring plugin %q is available", plugin)

	platform := runtime.GOOS + "_" + runtime.GOARCH
	binaryName := pluginBinaryName(plugin)

	if len(wantVersion) > 0 {
		// The user has requested a specific version.
		want := filepath.Join(c.Cache, plugin, platform, wantVersion, binaryName)
		if _, err := os.Stat(want); err == nil {
			return want, nil
		}
	}

	// When auto-update is disabled and the user hasn't picked a specific version, use the latest cached version if
	// available.
	if len(wantVersion) == 0 && !c.AutoUpdate {
		matches, _ := filepath.Glob(filepath.Join(c.Cache, plugin, platform, "*", binaryName))
		if len(matches) > 0 {
			sort.Slice(matches, func(i, j int) bool {
				vi := "v" + filepath.Base(filepath.Dir(matches[i]))
				vj := "v" + filepath.Base(filepath.Dir(matches[j]))
				return semver.Compare(vi, vj) > 0
			})
			logging.Debugf("plugin %q using cached version at %s (auto-update disabled)", plugin, matches[0])
			return matches[0], nil
		}
	}

	manifest, err := c.loadManifest()
	if err != nil {
		return "", fmt.Errorf("failed to load plugin manifest: %w", err)
	}

	p, ok := manifest.Plugins[plugin]
	if !ok {
		return "", fmt.Errorf("plugin %q not found in manifest at %s", plugin, c.ManifestURL)
	}

	if len(wantVersion) == 0 {
		if p.Latest == "" {
			return "", fmt.Errorf("plugin %q has no latest version defined", plugin)
		}
		wantVersion = p.Latest
	}

	version, ok := p.Versions[wantVersion]
	if !ok {
		return "", fmt.Errorf("plugin %q version %q not found in manifest (omit the version to use the latest)", plugin, wantVersion)
	}

	artifact, ok := version.Artifacts[platform]
	if !ok {
		return "", fmt.Errorf("plugin %q version %q is not available for your platform (%s)", plugin, wantVersion, platform)
	}

	binaryPath := filepath.Join(c.Cache, plugin, platform, wantVersion, binaryName)

	if _, err := os.Stat(binaryPath); err == nil {
		logging.Debugf("plugin %q already cached at %s", plugin, binaryPath)
		return binaryPath, nil
	}

	logging.Infof("downloading plugin %q version %s for %s", plugin, wantVersion, platform)

	archivePath, err := downloadAndVerify(artifact.URL, artifact.SHA)
	if err != nil {
		return "", fmt.Errorf("failed to download plugin %q: %w", plugin, err)
	}
	defer func() { _ = os.Remove(archivePath) }()

	if err := os.MkdirAll(filepath.Dir(binaryPath), 0750); err != nil {
		return "", fmt.Errorf("failed to create plugin cache directory: %w (use INFRACOST_CLI_PLUGIN_CACHE_DIRECTORY to change the location)", err)
	}

	tmpBinary := binaryPath + ".tmp"
	defer func() { _ = os.Remove(tmpBinary) }()

	switch {
	case strings.HasSuffix(artifact.Name, ".tar.gz"):
		err = unpackTarGz(archivePath, tmpBinary, plugin)
	case strings.HasSuffix(artifact.Name, ".zip"):
		err = unpackZip(archivePath, tmpBinary, binaryName)
	default:
		err = fmt.Errorf("unsupported archive format for %s", artifact.Name)
	}
	if err != nil {
		return "", fmt.Errorf("failed to unpack plugin %q: %w", plugin, err)
	}

	if err := os.Chmod(tmpBinary, 0750); err != nil { //nolint:gosec // G302: plugin binary must be executable
		return "", fmt.Errorf("failed to make plugin binary executable: %w", err)
	}

	if err := renameWithRetry(tmpBinary, binaryPath); err != nil {
		return "", fmt.Errorf("failed to install plugin binary: %w", err)
	}

	logging.Infof("installed plugin %q to %s", plugin, binaryPath)
	return binaryPath, nil
}

func (c *Config) loadManifest() (*Manifest, error) {
	if c.manifest != nil {
		return c.manifest, nil
	}

	response, err := http.Get(c.ManifestURL) //nolint:gosec // G107: URL is from config/env, not user input
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = response.Body.Close()
	}()

	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to fetch plugin manifest (%s): %s (check your internet connection, or use INFRACOST_CLI_PLUGIN_MANIFEST_URL to override the manifest endpoint)", c.ManifestURL, response.Status)
	}

	var manifest Manifest
	if err := json.NewDecoder(response.Body).Decode(&manifest); err != nil {
		return nil, err
	}

	c.manifest = &manifest
	return c.manifest, nil
}

func downloadAndVerify(rawURL, expectedSHA string) (string, error) {
	req, err := http.NewRequest("GET", rawURL, nil) //nolint:gosec // G107: URL is from the trusted plugin manifest
	if err != nil {
		return "", fmt.Errorf("failed to create HTTP request: %w", err)
	}

	// GitHub requires this header to download release assets as binary.
	req.Header.Set("Accept", "application/octet-stream")

	resp, err := http.DefaultClient.Do(req) //nolint:gosec // G704: request originates from plugin manifest
	if err != nil {
		return "", fmt.Errorf("HTTP request failed: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected HTTP status: %s", resp.Status)
	}

	tmpFile, err := os.CreateTemp("", "infracost-plugin-*")
	if err != nil {
		return "", fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()

	hasher := sha256.New()
	writer := io.MultiWriter(tmpFile, hasher)

	if _, err := io.Copy(writer, resp.Body); err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath) //nolint:gosec // G703: path is from os.CreateTemp
		return "", fmt.Errorf("failed to download: %w", err)
	}

	if err := tmpFile.Close(); err != nil {
		_ = os.Remove(tmpPath) //nolint:gosec // G703: path is from os.CreateTemp
		return "", fmt.Errorf("failed to close temp file: %w", err)
	}

	if expectedSHA != "" {
		actualSHA := hex.EncodeToString(hasher.Sum(nil))
		if actualSHA != expectedSHA {
			_ = os.Remove(tmpPath) //nolint:gosec // G703: path is from os.CreateTemp
			return "", fmt.Errorf("SHA256 mismatch: expected %s, got %s (the download may be corrupted, try again)", expectedSHA, actualSHA)
		}
	}

	return tmpPath, nil
}

func unpackTarGz(archivePath, destPath, expectedName string) error {
	f, err := os.Open(filepath.Clean(archivePath))
	if err != nil {
		return err
	}
	defer func() {
		_ = f.Close()
	}()

	gzr, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("failed to create gzip reader: %w", err)
	}
	defer func() {
		_ = gzr.Close()
	}()

	tr := tar.NewReader(gzr)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			return fmt.Errorf("expected entry %q not found in archive", expectedName)
		}
		if err != nil {
			return fmt.Errorf("failed to read tar entry: %w", err)
		}

		if filepath.Base(header.Name) != expectedName {
			continue
		}

		out, err := os.OpenFile(filepath.Clean(destPath), os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0600)
		if err != nil {
			return fmt.Errorf("failed to create output file: %w", err)
		}

		if _, err := io.Copy(out, io.LimitReader(tr, maxPluginSize)); err != nil {
			_ = out.Close()
			return fmt.Errorf("failed to extract file: %w", err)
		}

		return out.Close()
	}
}

func unpackZip(archivePath, destPath, expectedName string) error {
	r, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("failed to open zip: %w", err)
	}
	defer func() {
		_ = r.Close()
	}()

	for _, zf := range r.File {
		if filepath.Base(zf.Name) != expectedName {
			continue
		}
		return extractZipEntry(zf, destPath)
	}

	return fmt.Errorf("expected entry %q not found in zip", expectedName)
}

func extractZipEntry(zf *zip.File, destPath string) error {
	f, err := zf.Open()
	if err != nil {
		return fmt.Errorf("failed to open zip entry %q: %w", zf.Name, err)
	}
	defer func() {
		_ = f.Close()
	}()

	out, err := os.OpenFile(filepath.Clean(destPath), os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("failed to create output file: %w", err)
	}

	if _, err := io.Copy(out, io.LimitReader(f, maxPluginSize)); err != nil {
		_ = out.Close()
		return fmt.Errorf("failed to extract file: %w", err)
	}

	return out.Close()
}

// renameWithRetry attempts os.Rename up to 5 times with linear backoff (500ms, 1s, 1.5s, 2s).
// On Windows, antivirus software can briefly lock a newly written executable
// file while scanning it, causing the rename to fail with "access denied" or
// "the process cannot access the file because it is being used by another
// process". Retrying a few times gives the scanner time to finish.
func renameWithRetry(src, dst string) error {
	const maxAttempts = 5
	const retryDelay = 500 * time.Millisecond

	var lastErr error
	for i := 0; i < maxAttempts; i++ {
		err := os.Rename(src, dst)
		if err == nil {
			return nil
		}
		lastErr = err
		if i < maxAttempts-1 {
			time.Sleep(retryDelay * time.Duration(i+1))
		}
	}
	return lastErr
}

func defaultPluginCachePath() string {
	dir, err := os.UserCacheDir()
	if err == nil {
		return filepath.Join(dir, "infracost", "plugins")
	}
	logging.WithError(err).Msg("failed to load user cache dir, falling back to home directory")

	dir, err = os.UserHomeDir()
	if err == nil {
		return filepath.Join(dir, ".infracost", "plugins")
	}

	logging.WithError(err).Msg("pluginCachePath: failed to load user home dir, falling back to current directory")
	return filepath.Join(".infracost", "plugins")
}
