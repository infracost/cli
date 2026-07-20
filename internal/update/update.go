package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
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
		ui.Successf("Already up to date (%s).", info.Current)
		return nil
	}

	latestVersion, _ := semver.NewVersion(info.Latest)

	// A single spinner spans the whole update: it starts as "Updating … → …"
	// and resolves to "Updated to v…" on success. The action must not print to
	// stdout/stderr directly while the spinner owns the terminal — a raw write
	// desyncs bubbletea's line accounting, leaving orphaned spinner frames and
	// swallowing the message. (The sudo-elevation path handles this by pausing
	// the spinner around the prompt.)
	spinnerTitle := fmt.Sprintf("Updating %s → v%s...", version.Version, latestVersion)
	doneTitle := fmt.Sprintf("Updated to v%s.", latestVersion)

	return ui.RunWithSpinnerErr(ctx, spinnerTitle, doneTitle, func(ctx context.Context) error {
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
	execPath, err := resolvedExecutable()
	if err != nil {
		return err
	}

	return replaceBinaryAtPathWith(execPath, newBinary, replaceBinaryAtomically)
}

func replaceBinaryAtPathWith(execPath string, newBinary []byte, replace func(string, os.FileMode, []byte) error) error {
	info, err := os.Stat(execPath)
	if err != nil {
		return err
	}

	mode := info.Mode().Perm()
	if err := replace(execPath, mode, newBinary); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrPermission) {
		return err
	}

	return replaceBinaryWithCommand(execPath, mode, newBinary)
}

func replaceBinaryAtomically(execPath string, mode os.FileMode, newBinary []byte) error {
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
	if err := os.Chmod(tmpPath, mode); err != nil {
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

func replaceBinaryWithCommand(execPath string, mode os.FileMode, newBinary []byte) error {
	tmp, err := os.CreateTemp("", ".infracost-update-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()

	if _, err := tmp.Write(newBinary); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, mode); err != nil {
		return err
	}

	return copyBinaryWithCommand(tmpPath, execPath, mode)
}

var copyBinaryWithCommand = func(srcPath, dstPath string, mode os.FileMode) error {
	if runtime.GOOS == "windows" {
		return fmt.Errorf("permission denied replacing %s; rerun from an elevated shell", dstPath)
	}

	if _, err := exec.LookPath("sudo"); err != nil {
		return fmt.Errorf("permission denied replacing %s and sudo was not found; rerun as a user that can write to this path", dstPath)
	}

	script := `set -e
	tmp=$(mktemp "$1/.infracost-update-XXXXXX")
	trap 'rm -f "$tmp"' EXIT
	cp "$2" "$tmp"
	chmod "$3" "$tmp"
	mv "$tmp" "$4"
	trap - EXIT`
	args := []string{"sh", "-c", script, "sh", filepath.Dir(dstPath), srcPath, fmt.Sprintf("%o", mode), dstPath}

	// sudo writes its password prompt to the terminal and reads the password back
	// from it, so pause any active spinner for the whole exec - otherwise the notice
	// and prompt get painted over by the spinner's redraws. WithSpinnerPaused is a
	// no-op when no spinner is running (e.g. non-TTY / piped output).
	return ui.WithSpinnerPaused(func() error {
		// Leading newline separates the notice from whatever preceded it (the paused
		// spinner line, or prior output) so the password prompt stands on its own.
		fmt.Fprintf(os.Stderr, "\nUpdating %s requires elevated permissions; you may be prompted for your password.\n", dstPath)

		cmd := exec.Command("sudo", args...) //nolint:gosec // paths are local filesystem paths for the running binary and downloaded update
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("copying updated binary with sudo: %w", err)
		}
		return nil
	})
}
