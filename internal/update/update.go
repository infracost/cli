package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
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
	"github.com/google/go-github/v83/github"
	"github.com/infracost/cli/version"
)

const (
	repoOwner = "infracost"
	repoName  = "cli"
)

func Update(ctx context.Context) error {

	currentVersion, _ := semver.NewVersion(version.Version)

	client := newGitHubClient()

	release, _, err := client.Repositories.GetLatestRelease(ctx, repoOwner, repoName)
	if err != nil {
		return fmt.Errorf("failed to fetch latest release: %w", err)
	}

	tag := release.GetTagName()
	latestVersion, err := semver.NewVersion(tag)
	if err != nil {
		return fmt.Errorf("cannot parse release version %q: %w", tag, err)
	}

	if currentVersion != nil && !latestVersion.GreaterThan(currentVersion) {
		fmt.Printf("Already up to date (v%s).\n", currentVersion)
		return nil
	}

	fmt.Printf("Updating %s → v%s...\n", version.Version, latestVersion)

	assetName := expectedAssetName(latestVersion.String())
	var assetID int64
	for _, a := range release.Assets {
		if a.GetName() == assetName {
			assetID = a.GetID()
			break
		}
	}
	if assetID == 0 {
		return fmt.Errorf("no release asset found for %s/%s (expected %s)", runtime.GOOS, runtime.GOARCH, assetName)
	}

	rc, _, err := client.Repositories.DownloadReleaseAsset(ctx, repoOwner, repoName, assetID, &http.Client{Timeout: 60 * time.Second})
	if err != nil {
		return fmt.Errorf("failed to download asset: %w", err)
	}
	assetData, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil {
		return fmt.Errorf("failed to read asset: %w", err)
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
}

func getBinaryNames() []string {
	candidates := []string{"infracost-preview", "infracost"}
	output := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if runtime.GOOS == "windows" {
			candidate += ".exe"
		}
		output = append(output, candidate)
	}
	return output
}

var newGitHubClient = func() *github.Client {
	token, err := findGitHubToken()
	if err == nil && token != "" {
		return github.NewClient(nil).WithAuthToken(token)
	}
	return github.NewClient(nil)
}

func expectedAssetName(ver string) string {
	ext := "tar.gz"
	if runtime.GOOS == "windows" {
		ext = "zip"
	}
	return fmt.Sprintf("infracost-preview_%s_%s_%s.%s", ver, runtime.GOOS, runtime.GOARCH, ext)
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
	tmp, err := os.CreateTemp(dir, ".infracost-preview-update-*")
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
