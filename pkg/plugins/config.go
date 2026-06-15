package plugins

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/go-hclog"
	"github.com/infracost/cli/internal/ui"
	"github.com/infracost/cli/pkg/config/process"
	"github.com/infracost/cli/pkg/logging"
	"github.com/infracost/cli/pkg/plugins/parser"
	"github.com/infracost/cli/pkg/plugins/providers"
	pluginpb "github.com/infracost/proto/gen/go/infracost/plugin"
	proto "github.com/infracost/proto/gen/go/infracost/provider"
)

var _ process.Processor = (*Config)(nil)

const (
	// maxPluginSize is the maximum allowed size for an extracted plugin binary (1 GB).
	maxPluginSize = 1 << 30

	pluginTypeParser   = "parser"
	pluginTypeProvider = "provider"
)

var pluginSpecs = []pluginSpec{
	{Key: "terraform", Name: "infracost-plugin-terraform", Type: pluginTypeParser},
	{Key: "terragrunt", Name: "infracost-plugin-terragrunt", Type: pluginTypeParser},
	{Key: "cloudformation", Name: "infracost-plugin-cloudformation", Type: pluginTypeParser},
	{Key: "ciscostacks", Name: "infracost-plugin-ciscostacks", Type: pluginTypeParser},
	{Key: "aws", Name: "infracost-plugin-aws", Type: pluginTypeProvider, Provider: proto.Provider_PROVIDER_AWS},
	{Key: "google", Name: "infracost-plugin-google", Type: pluginTypeProvider, Provider: proto.Provider_PROVIDER_GOOGLE},
	{Key: "azure", Name: "infracost-plugin-azure", Type: pluginTypeProvider, Provider: proto.Provider_PROVIDER_AZURERM},
}

type pluginSpec struct {
	Key      string
	Name     string
	Type     string
	Provider proto.Provider
}

type Config struct {
	Providers providers.Config
	Parser    parser.Config

	// BaseURL points to the root URL where plugin archives are hosted.
	BaseURL string `env:"INFRACOST_CLI_PLUGIN_BASE_URL" default:"https://releases.infracost.io"`

	// Cache is where managed plugins should go.
	Cache string `env:"INFRACOST_CLI_PLUGIN_CACHE_DIRECTORY"`

	// Dir is a flat plugin directory override for local plugin development. When set, downloads are skipped.
	Dir string `env:"INFRACOST_CLI_PLUGIN_DIR"`

	// AutoUpdate controls whether plugins are updated to the latest version.
	// When false, an existing flat-installed binary is used if available.
	AutoUpdate bool `env:"INFRACOST_CLI_PLUGIN_AUTO_UPDATE" default:"true"`

	managerMu  sync.Mutex
	ensureOnce sync.Once
	ensureErr  error
	manager    *Manager

	LoadParserPluginForProject func(context.Context, string) (*ParserPlugin, error)
}

func (c *Config) Process() {
	if len(c.Cache) == 0 {
		c.Cache = defaultPluginCachePath()
	}
	c.Providers.LoadAWS = c.providerLoader(proto.Provider_PROVIDER_AWS)
	c.Providers.LoadGoogle = c.providerLoader(proto.Provider_PROVIDER_GOOGLE)
	c.Providers.LoadAzurerm = c.providerLoader(proto.Provider_PROVIDER_AZURERM)
}

func (c *Config) ParserPluginForProject(ctx context.Context, projectTypeOrPluginName string) (*ParserPlugin, error) {
	if c.LoadParserPluginForProject != nil {
		return c.LoadParserPluginForProject(ctx, projectTypeOrPluginName)
	}
	manager, err := c.EnsurePlugins()
	if err != nil {
		return nil, err
	}
	return manager.LoadParserPluginForProject(ctx, projectTypeOrPluginName)
}

func (c *Config) ResetParserPlugins() {
	c.managerMu.Lock()
	manager := c.manager
	c.manager = nil
	c.ensureErr = nil
	c.ensureOnce = sync.Once{}
	c.managerMu.Unlock()

	if manager != nil {
		manager.Close()
	}
}

func (c *Config) Close() {
	c.ResetParserPlugins()
	c.Providers.Close()
}

func (c *Config) EnsurePlugins() (*Manager, error) {
	c.managerMu.Lock()
	defer c.managerMu.Unlock()

	c.ensureOnce.Do(func() {
		c.manager, c.ensureErr = c.ensureAllPlugins()
	})
	return c.manager, c.ensureErr
}

func (c *Config) ensureAllPlugins() (*Manager, error) {
	pluginDir := c.pluginDir()
	if c.Dir != "" {
		return NewManager(pluginDir)
	}

	for _, spec := range pluginSpecs {
		if c.pluginPathOverride(spec) != "" {
			continue
		}

		if _, err := c.Ensure(spec.Name, c.pluginVersion(spec)); err != nil {
			return nil, err
		}
	}

	return NewManager(pluginDir)
}

func (c *Config) pluginDir() string {
	if c.Dir != "" {
		return c.Dir
	}
	if c.Cache == "" {
		return defaultPluginCachePath()
	}
	return c.Cache
}

func (c *Config) providerLoader(provider proto.Provider) func(hclog.Level) (pluginpb.ProviderServiceClient, func(), error) {
	return func(level hclog.Level) (pluginpb.ProviderServiceClient, func(), error) {
		return providers.Connect(c.providerPluginPath(provider), level)
	}
}

func (c *Config) providerPluginPath(provider proto.Provider) string {
	if override, _ := c.providerOverride(provider); override != "" {
		return override
	}
	for _, spec := range pluginSpecs {
		if spec.Type == pluginTypeProvider && spec.Provider == provider {
			return filepath.Join(c.pluginDir(), pluginBinaryName(spec.Name))
		}
	}
	return ""
}

func (c *Config) pluginPathOverride(spec pluginSpec) string {
	if spec.Type != pluginTypeProvider {
		return ""
	}
	override, _ := c.providerOverride(spec.Provider)
	return override
}

func (c *Config) pluginVersion(spec pluginSpec) string {
	if version := os.Getenv("INFRACOST_CLI_PLUGIN_" + strings.ToUpper(spec.Key) + "_VERSION"); version != "" {
		return version
	}

	switch spec.Type {
	case pluginTypeParser:
		return c.Parser.Version
	case pluginTypeProvider:
		_, version := c.providerOverride(spec.Provider)
		return version
	}
	return ""
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

	binaryName := pluginBinaryName(plugin)
	binaryPath := filepath.Join(c.pluginDir(), binaryName)
	installed := flatPluginBinaryExists(binaryPath)

	if installed && !c.AutoUpdate {
		logging.Debugf("plugin %q using existing flat-installed binary at %s (auto-update disabled)", plugin, binaryPath)
		return binaryPath, nil
	}

	downloadVersion := wantVersion
	if downloadVersion == "" {
		downloadVersion = "latest"
	}

	if downloadVersion != "latest" {
		if installed && cachedPluginVersion(binaryPath) == downloadVersion {
			logging.Debugf("plugin %q version %s already installed at %s", plugin, downloadVersion, binaryPath)
			return binaryPath, nil
		}
	}

	artifactName := pluginArchiveName()
	artifactURL := c.pluginArtifactURL(plugin, runtime.GOOS, runtime.GOARCH, downloadVersion, artifactName)
	resolvedVersion := downloadVersion
	if downloadVersion == "latest" {
		var err error
		resolvedVersion, err = fetchPluginVersion(c.pluginVersionURL(plugin, runtime.GOOS, runtime.GOARCH, downloadVersion))
		if err != nil {
			return "", fmt.Errorf("failed to fetch plugin version: %w", err)
		}
	}

	artifactSHA := ""
	if installed {
		if cachedPluginVersion(binaryPath) == resolvedVersion {
			logging.Debugf("plugin %q version %s already installed at %s", plugin, resolvedVersion, binaryPath)
			return binaryPath, nil
		}

		var err error
		artifactSHA, err = fetchSHA256(artifactURL + ".sha256")
		if err != nil {
			return "", fmt.Errorf("failed to fetch plugin checksum: %w", err)
		}
		if cachedPluginSHA(binaryPath) == artifactSHA {
			logging.Debugf("plugin %q already installed at %s", plugin, binaryPath)
			if err := writePluginVersion(binaryPath, resolvedVersion); err != nil {
				return "", err
			}
			return binaryPath, nil
		}
	}

	if artifactSHA == "" {
		var err error
		artifactSHA, err = fetchSHA256(artifactURL + ".sha256")
		if err != nil {
			return "", fmt.Errorf("failed to fetch plugin checksum: %w", err)
		}
	}

	logging.Infof("downloading plugin %q version %s for %s/%s", plugin, downloadVersion, runtime.GOOS, runtime.GOARCH)

	if err := ui.RunWithSpinnerErr(context.Background(), fmt.Sprintf("Downloading %s %s...", plugin, downloadVersion), fmt.Sprintf("Downloaded %s %s", plugin, downloadVersion), func(_ context.Context) error {
		logging.Debugf("downloading plugin archive from %s", artifactURL)
		archivePath, err := downloadAndVerify(artifactURL, artifactSHA)
		if err != nil {
			return fmt.Errorf("failed to download plugin %q: %w", plugin, err)
		}
		defer func() { _ = os.Remove(archivePath) }()

		if err := os.MkdirAll(filepath.Dir(binaryPath), 0o750); err != nil {
			return fmt.Errorf("failed to create plugin directory: %w (use INFRACOST_CLI_PLUGIN_CACHE_DIRECTORY to change the location or INFRACOST_CLI_PLUGIN_DIR for local plugins)", err)
		}

		tmpBinary := binaryPath + ".tmp"
		defer func() { _ = os.Remove(tmpBinary) }()

		switch {
		case strings.HasSuffix(artifactName, ".tar.gz"):
			err = unpackTarGz(archivePath, tmpBinary, plugin)
		case strings.HasSuffix(artifactName, ".zip"):
			err = unpackZip(archivePath, tmpBinary, binaryName)
		default:
			err = fmt.Errorf("unsupported archive format for %s", artifactName)
		}
		if err != nil {
			return fmt.Errorf("failed to unpack plugin %q: %w", plugin, err)
		}

		if err := os.Chmod(tmpBinary, 0o750); err != nil { //nolint:gosec // G302: plugin binary must be executable
			return fmt.Errorf("failed to make plugin binary executable: %w", err)
		}

		if err := removeExistingPluginPath(binaryPath); err != nil {
			return err
		}

		if err := renameWithRetry(tmpBinary, binaryPath); err != nil {
			return fmt.Errorf("failed to install plugin binary: %w", err)
		}

		if err := os.WriteFile(binaryPath+".sha256", []byte(artifactSHA+"\n"), 0o600); err != nil {
			return fmt.Errorf("failed to write plugin checksum: %w", err)
		}
		if err := writePluginVersion(binaryPath, resolvedVersion); err != nil {
			return err
		}
		return nil
	}); err != nil {
		return "", err
	}

	logging.Infof("installed plugin %q to %s", plugin, binaryPath)
	return binaryPath, nil
}

func pluginArchiveName() string {
	if runtime.GOOS == "windows" {
		return "data.zip"
	}
	return "data.tar.gz"
}

func (c *Config) pluginArtifactURL(plugin, goos, goarch, version, name string) string {
	return fmt.Sprintf("%s/%s/%s/%s/%s/%s", strings.TrimRight(c.BaseURL, "/"), plugin, goos, goarch, version, name)
}

func (c *Config) pluginVersionURL(plugin, goos, goarch, version string) string {
	return fmt.Sprintf("%s/%s/%s/%s/%s/version", strings.TrimRight(c.BaseURL, "/"), plugin, goos, goarch, version)
}

func flatPluginBinaryExists(binaryPath string) bool {
	info, err := os.Stat(binaryPath)
	return err == nil && !info.IsDir()
}

func removeExistingPluginPath(binaryPath string) error {
	info, err := os.Stat(binaryPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to inspect existing plugin path: %w", err)
	}
	if info.IsDir() {
		if err := os.RemoveAll(binaryPath); err != nil {
			return fmt.Errorf("failed to replace existing plugin directory: %w", err)
		}
		return nil
	}
	if runtime.GOOS == "windows" {
		if err := os.Remove(binaryPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("failed to replace existing plugin binary: %w", err)
		}
	}
	return nil
}

func cachedPluginSHA(binaryPath string) string {
	data, err := os.ReadFile(binaryPath + ".sha256") //nolint:gosec // G304: sidecar path is derived from the trusted plugin path
	if err != nil {
		return ""
	}

	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

func cachedPluginVersion(binaryPath string) string {
	data, err := os.ReadFile(binaryPath + ".version") //nolint:gosec // G304: sidecar path is derived from the trusted plugin path
	if err != nil {
		return ""
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

func writePluginVersion(binaryPath, version string) error {
	if err := os.WriteFile(binaryPath+".version", []byte(version+"\n"), 0o600); err != nil {
		return fmt.Errorf("failed to write plugin version: %w", err)
	}
	return nil
}

func fetchPluginVersion(rawURL string) (string, error) {
	logging.Debugf("fetching plugin version from %s", rawURL)

	req, err := http.NewRequest("GET", rawURL, nil) //nolint:gosec // G107: URL is from the trusted plugin base URL
	if err != nil {
		return "", fmt.Errorf("failed to create HTTP request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req) //nolint:gosec // G704: request originates from the plugin base URL
	if err != nil {
		return "", fmt.Errorf("HTTP request failed: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected HTTP status: %s", resp.Status)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, 1024))
	if err != nil {
		return "", fmt.Errorf("failed to read plugin version: %w", err)
	}

	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return "", fmt.Errorf("empty plugin version response")
	}
	return fields[0], nil
}

func fetchSHA256(rawURL string) (string, error) {
	logging.Debugf("fetching plugin checksum from %s", rawURL)

	req, err := http.NewRequest("GET", rawURL, nil) //nolint:gosec // G107: URL is from the trusted plugin base URL
	if err != nil {
		return "", fmt.Errorf("failed to create HTTP request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req) //nolint:gosec // G704: request originates from the plugin base URL
	if err != nil {
		return "", fmt.Errorf("HTTP request failed: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected HTTP status: %s", resp.Status)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, 1024))
	if err != nil {
		return "", fmt.Errorf("failed to read checksum: %w", err)
	}

	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return "", fmt.Errorf("empty checksum response")
	}
	return fields[0], nil
}

func downloadAndVerify(rawURL, expectedSHA string) (string, error) {
	req, err := http.NewRequest("GET", rawURL, nil) //nolint:gosec // G107: URL is from the trusted plugin base URL
	if err != nil {
		return "", fmt.Errorf("failed to create HTTP request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req) //nolint:gosec // G704: request originates from the plugin base URL
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

		out, err := os.OpenFile(filepath.Clean(destPath), os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600)
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

	out, err := os.OpenFile(filepath.Clean(destPath), os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600)
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
