package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/Masterminds/semver/v3"
	"github.com/infracost/cli/internal/ui"
	"github.com/infracost/cli/version"
)

const defaultCLIReleaseBaseURL = "https://releases.infracost.io/cli"

var cliReleaseBaseURL = func() string {
	if v := os.Getenv("INFRACOST_CLI_UPDATE_BASE_URL"); v != "" {
		return strings.TrimRight(v, "/")
	}
	return defaultCLIReleaseBaseURL
}

// VersionInfo holds the result of a version check against the latest release.
type VersionInfo struct {
	Current  string
	Latest   string
	UpToDate bool
}

// CheckLatestVersion fetches the latest CLI version from the release bucket and
// compares it with the running version. It does not download or install anything.
func CheckLatestVersion(ctx context.Context) (VersionInfo, error) {
	currentVersion, _ := semver.NewVersion(version.Version)

	latestVersionRaw, err := fetchCLIVersion(ctx, "latest")
	if err != nil {
		return VersionInfo{}, err
	}

	latestVersion, err := semver.NewVersion(latestVersionRaw)
	if err != nil {
		return VersionInfo{}, fmt.Errorf("cannot parse release version %q: %w", latestVersionRaw, err)
	}

	upToDate := currentVersion != nil && !latestVersion.GreaterThan(currentVersion)
	return VersionInfo{
		Current:  version.Version,
		Latest:   fmt.Sprintf("v%s", latestVersion),
		UpToDate: upToDate,
	}, nil
}

func fetchCLIVersion(ctx context.Context, releaseVersion string) (string, error) {
	body, err := httpGetContext(ctx, cliVersionURL(releaseVersion))
	if err != nil {
		return "", fmt.Errorf("failed to fetch CLI version: %w", err)
	}
	fields := strings.Fields(string(body))
	if len(fields) == 0 {
		return "", fmt.Errorf("empty CLI version response")
	}
	return fields[0], nil
}

func isCLIReleaseTag(tag string) bool {
	if !strings.HasPrefix(tag, "v") {
		return false
	}
	if strings.Contains(tag, "/") {
		return false
	}
	_, err := semver.NewVersion(tag)
	return err == nil
}

func Update(ctx context.Context) error {
	if method := DetectInstallMethod(); method.ManagedExternally() {
		return fmt.Errorf("this binary was installed via a package manager — run %q to upgrade", strings.TrimPrefix(method.UpgradeCommand(), "$ "))
	}

	var info VersionInfo
	if err := ui.RunWithSpinnerErr(ctx, "Checking for updates...", "Update check complete", func(ctx context.Context) error {
		var err error
		info, err = CheckLatestVersion(ctx)
		return err
	}); err != nil {
		return err
	}

	if info.UpToDate {
		fmt.Printf("Already up to date (%s).\n", info.Current)
		return nil
	}

	latestVersion, _ := semver.NewVersion(info.Latest)
	ui.Stepf("Updating %s → v%s...", version.Version, latestVersion)

	return ui.RunWithSpinnerErr(ctx, "Downloading update...", "Download complete", func(ctx context.Context) error {
		assetName := expectedAssetName()
		assetURL := cliArtifactURL(info.Latest, assetName)

		assetSHA, err := fetchCLISHA256(ctx, info.Latest, assetName)
		if err != nil {
			return err
		}

		assetData, err := downloadAndVerifyCLIAsset(ctx, assetURL, assetSHA)
		if err != nil {
			return err
		}

		for _, binaryName := range getBinaryNames() {
			binaryData, err := extractBinary(assetName, assetData, binaryName)
			if err != nil {
				continue
			}

			if err := replaceBinary(binaryData); err != nil {
				return fmt.Errorf("failed to replace binary: %w", err)
			}

			fmt.Printf("Updated to v%s.\n", latestVersion)
			return nil
		}

		return fmt.Errorf("no suitable binary found in asset %q", assetName)
	})
}

func getBinaryNames() []string {
	candidates := []string{"infracost", "infracost-preview"}
	output := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if runtime.GOOS == "windows" {
			candidate += ".exe"
		}
		output = append(output, candidate)
	}
	return output
}

func expectedAssetName() string {
	if runtime.GOOS == "windows" {
		return "data.zip"
	}
	return "data.tar.gz"
}

func cliVersionURL(releaseVersion string) string {
	return fmt.Sprintf("%s/%s/%s/%s/version", cliReleaseBaseURL(), runtime.GOOS, runtime.GOARCH, releaseVersion)
}

func cliArtifactURL(releaseVersion, assetName string) string {
	return fmt.Sprintf("%s/%s/%s/%s/%s", cliReleaseBaseURL(), runtime.GOOS, runtime.GOARCH, releaseVersion, assetName)
}

func fetchCLISHA256(ctx context.Context, releaseVersion, assetName string) (string, error) {
	body, err := httpGetContext(ctx, cliArtifactURL(releaseVersion, assetName+".sha256"))
	if err != nil {
		return "", fmt.Errorf("failed to fetch CLI checksum: %w", err)
	}
	fields := strings.Fields(string(body))
	if len(fields) == 0 {
		return "", fmt.Errorf("empty CLI checksum response")
	}
	return fields[0], nil
}

func downloadAndVerifyCLIAsset(ctx context.Context, rawURL, expectedSHA string) ([]byte, error) {
	data, err := httpGetContext(ctx, rawURL)
	if err != nil {
		return nil, fmt.Errorf("failed to download asset: %w", err)
	}

	actual := sha256.Sum256(data)
	actualSHA := hex.EncodeToString(actual[:])
	if expectedSHA != "" && actualSHA != expectedSHA {
		return nil, fmt.Errorf("SHA256 mismatch: expected %s, got %s (the download may be corrupted, try again)", expectedSHA, actualSHA)
	}

	return data, nil
}

func httpGetContext(ctx context.Context, rawURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil) //nolint:gosec // G107: URL is from trusted release base URL
	if err != nil {
		return nil, fmt.Errorf("failed to create HTTP request: %w", err)
	}

	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Do(req) //nolint:gosec // G107: URL is from trusted release base URL
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", rawURL, resp.Status)
	}

	return io.ReadAll(resp.Body)
}

func extractBinary(assetName string, data []byte, binaryName string) ([]byte, error) {
	if strings.HasSuffix(assetName, ".zip") {
		return extractFromZip(data, binaryName)
	}
	return extractFromTarGz(data, binaryName)
}

func extractFromTarGz(data []byte, binaryName string) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer func() { _ = gz.Close() }()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if filepath.Base(hdr.Name) == binaryName {
			return io.ReadAll(tr)
		}
	}
	return nil, fmt.Errorf("binary %q not found in archive", binaryName)
}

func extractFromZip(data []byte, binaryName string) ([]byte, error) {
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, err
	}
	for _, f := range r.File {
		if filepath.Base(f.Name) == binaryName {
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			defer func() { _ = rc.Close() }()
			return io.ReadAll(rc)
		}
	}
	return nil, fmt.Errorf("binary %q not found in archive", binaryName)
}

var replaceBinary = func(newBinary []byte) error {
	execPath, err := os.Executable()
	if err != nil {
		return err
	}
	execPath, err = filepath.EvalSymlinks(execPath)
	if err != nil {
		return err
	}

	info, err := os.Stat(execPath)
	if err != nil {
		return err
	}

	// Write new binary to a temp file in the same directory (ensures same filesystem for rename).
	dir := filepath.Dir(execPath)
	tmp, err := os.CreateTemp(dir, ".infracost-update-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()

	if _, err := tmp.Write(newBinary); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}

	// persist current permissions to the new file, so we respect the user's choice of perms
	if err := os.Chmod(tmpPath, info.Mode().Perm()); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}

	// Atomic rename.
	if err := os.Rename(tmpPath, execPath); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}

	return nil
}

var ErrTokenNotFound = fmt.Errorf("github token not found")

func findGitHubToken() (string, error) {
	if tok := os.Getenv("GH_TOKEN"); tok != "" {
		return tok, nil
	}

	if tok := os.Getenv("GITHUB_TOKEN"); tok != "" {
		return tok, nil
	}

	cmd := exec.Command("gh", "auth", "token")
	cmd.Stderr = io.Discard
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(output))
	if token != "" {
		return token, nil
	}

	return "", ErrTokenNotFound
}
