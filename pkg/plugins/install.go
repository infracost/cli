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
	"time"

	"github.com/infracost/cli/internal/cache"
	"github.com/infracost/cli/internal/ui"
	"github.com/infracost/cli/pkg/logging"
)

// maxPluginSize is the maximum allowed size for an extracted plugin binary (1 GB).
const maxPluginSize = 1 << 30

// devPluginVersion marks a binary as a locally-built dev build. When the
// installed plugin reports this version we never auto-update — the user
// dropped a custom build in the cache directory on purpose.
const devPluginVersion = "dev"

// queryPluginInfoTimeout caps how long we wait for an installed binary to
// respond to GetPluginInfo when checking its current version during install.
const queryPluginInfoTimeout = 30 * time.Second

// Install ensures the named plugin is present in the cache directory at the
// requested version. wantVersion may be empty to mean "latest". Returns the
// path to the installed binary.
func (m *Manager) Install(pluginName, wantVersion string) (string, error) {
	logging.Debugf("ensuring plugin %q is available", pluginName)

	binaryName := pluginBinaryName(pluginName)
	binaryPath := filepath.Join(m.opts.Cache, binaryName)
	installed := flatPluginBinaryExists(binaryPath)

	if installed && !m.opts.AutoUpdate {
		logging.Debugf("plugin %q using existing flat-installed binary at %s (auto-update disabled)", pluginName, binaryPath)
		return binaryPath, nil
	}

	currentVersion := ""
	if installed {
		ctx, cancel := context.WithTimeout(context.Background(), queryPluginInfoTimeout)
		info, err := m.queryInfo(ctx, binaryPath)
		cancel()
		if err != nil {
			logging.Debugf("failed to query installed plugin %q version: %v — will re-download", pluginName, err)
		} else {
			currentVersion = info.GetVersion()
			if currentVersion == devPluginVersion {
				logging.Debugf("plugin %q is a dev build at %s — skipping auto-update", pluginName, binaryPath)
				return binaryPath, nil
			}
		}
	}

	downloadVersion := wantVersion
	if downloadVersion == "" {
		downloadVersion = "latest"
	}

	if installed && currentVersion != "" && downloadVersion != "latest" && currentVersion == downloadVersion {
		logging.Debugf("plugin %q version %s already installed at %s", pluginName, downloadVersion, binaryPath)
		return binaryPath, nil
	}

	resolvedVersion := downloadVersion
	if downloadVersion == "latest" {
		v, err := fetchPluginVersion(m.pluginVersionURL(pluginName, runtime.GOOS, runtime.GOARCH, downloadVersion))
		if err != nil {
			return "", fmt.Errorf("failed to fetch plugin version: %w", err)
		}
		resolvedVersion = v
	}

	if installed && currentVersion != "" && currentVersion == resolvedVersion {
		logging.Debugf("plugin %q version %s already installed at %s", pluginName, resolvedVersion, binaryPath)
		return binaryPath, nil
	}

	artifactName := pluginArchiveName()
	artifactURL := m.pluginArtifactURL(pluginName, runtime.GOOS, runtime.GOARCH, downloadVersion, artifactName)

	artifactSHA, err := fetchSHA256(artifactURL + ".sha256")
	if err != nil {
		return "", fmt.Errorf("failed to fetch plugin checksum: %w", err)
	}

	logging.Infof("downloading plugin %q version %s for %s/%s", pluginName, downloadVersion, runtime.GOOS, runtime.GOARCH)

	if err := ui.RunWithSpinnerErr(context.Background(), fmt.Sprintf("Downloading %s %s...", pluginName, downloadVersion), fmt.Sprintf("Downloaded %s %s", pluginName, downloadVersion), func(_ context.Context) error {
		logging.Debugf("downloading plugin archive from %s", artifactURL)
		archivePath, err := downloadAndVerify(artifactURL, artifactSHA)
		if err != nil {
			return fmt.Errorf("failed to download plugin %q: %w", pluginName, err)
		}
		defer func() { _ = os.Remove(archivePath) }()

		if err := os.MkdirAll(filepath.Dir(binaryPath), 0o750); err != nil {
			return fmt.Errorf("failed to create plugin directory: %w (use INFRACOST_CLI_PLUGIN_CACHE_DIRECTORY to change the location or INFRACOST_CLI_PLUGIN_DIR for local plugins)", err)
		}

		tmpBinary := binaryPath + ".tmp"
		defer func() { _ = os.Remove(tmpBinary) }()

		switch {
		case strings.HasSuffix(artifactName, ".tar.gz"):
			err = unpackTarGz(archivePath, tmpBinary, pluginName)
		case strings.HasSuffix(artifactName, ".zip"):
			err = unpackZip(archivePath, tmpBinary, binaryName)
		default:
			err = fmt.Errorf("unsupported archive format for %s", artifactName)
		}
		if err != nil {
			return fmt.Errorf("failed to unpack plugin %q: %w", pluginName, err)
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

		return nil
	}); err != nil {
		return "", err
	}

	logging.Infof("installed plugin %q to %s", pluginName, binaryPath)
	return binaryPath, nil
}

func pluginArchiveName() string {
	if runtime.GOOS == "windows" {
		return "data.zip"
	}
	return "data.tar.gz"
}

// pluginBinaryName returns the binary filename for the given plugin name,
// appending .exe on Windows.
func pluginBinaryName(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

func (m *Manager) pluginArtifactURL(plugin, goos, goarch, version, name string) string {
	return fmt.Sprintf("%s/%s/%s/%s/%s/%s", strings.TrimRight(m.opts.BaseURL, "/"), plugin, goos, goarch, version, name)
}

func (m *Manager) pluginVersionURL(plugin, goos, goarch, version string) string {
	return fmt.Sprintf("%s/%s/%s/%s/%s/version", strings.TrimRight(m.opts.BaseURL, "/"), plugin, goos, goarch, version)
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

// renameWithRetry retries os.Rename a few times to tolerate transient locks
// from AV scanners on Windows.
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
	return cache.PluginsDir()
}
