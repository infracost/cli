package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"

	"github.com/google/go-github/v83/github"
	"github.com/infracost/cli/version"
)

// buildTarGz creates a tar.gz archive containing a single file with the given name and content.
func buildTarGz(t *testing.T, fileName string, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	if err := tw.WriteHeader(&tar.Header{
		Name: fileName,
		Size: int64(len(content)),
		Mode: 0o755,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// fakeGitHubServer creates a test server that mimics the GitHub release API.
// It returns the server and a cleanup function.
func fakeGitHubServer(t *testing.T, tagName string, assetName string, assetContent []byte) *httptest.Server {
	t.Helper()

	assetID := int64(42)

	mux := http.NewServeMux()

	// GET /api/v3/repos/{owner}/{repo}/releases/latest
	mux.HandleFunc(fmt.Sprintf("/api/v3/repos/%s/%s/releases/latest", repoOwner, repoName), func(w http.ResponseWriter, _ *http.Request) {
		release := &github.RepositoryRelease{
			TagName: github.Ptr(tagName),
			Assets: []*github.ReleaseAsset{
				{
					ID:   github.Ptr(assetID),
					Name: github.Ptr(assetName),
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(release)
	})

	// GET /api/v3/repos/{owner}/{repo}/releases/assets/{id}
	mux.HandleFunc(fmt.Sprintf("/api/v3/repos/%s/%s/releases/assets/%d", repoOwner, repoName, assetID), func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") == "application/octet-stream" {
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(assetContent)
			return
		}
		asset := &github.ReleaseAsset{
			ID:   github.Ptr(assetID),
			Name: github.Ptr(assetName),
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(asset)
	})

	return httptest.NewServer(mux)
}

func TestUpdate_NewerVersionAvailable(t *testing.T) {
	origVersion := version.Version
	origNewClient := newGitHubClient
	origReplace := replaceBinary
	t.Cleanup(func() {
		version.Version = origVersion
		newGitHubClient = origNewClient
		replaceBinary = origReplace
	})

	version.Version = "0.0.1"

	binaryContent := []byte("new-binary-content")
	assetName := expectedAssetName("1.0.0")
	archive := buildTarGz(t, "infracost-preview", binaryContent)
	if runtime.GOOS == "windows" {
		t.Skip("tar.gz test not applicable on windows")
	}

	srv := fakeGitHubServer(t, "v1.0.0", assetName, archive)
	defer srv.Close()

	newGitHubClient = func() *github.Client {
		c, _ := github.NewClient(nil).WithEnterpriseURLs(srv.URL, srv.URL)
		return c
	}

	var replacedWith []byte
	replaceBinary = func(data []byte) error {
		replacedWith = data
		return nil
	}

	err := Update(context.Background())
	if err != nil {
		t.Fatalf("Update() returned error: %v", err)
	}
	if !bytes.Equal(replacedWith, binaryContent) {
		t.Fatalf("expected binary content %q, got %q", binaryContent, replacedWith)
	}
}

func TestUpdate_AlreadyUpToDate(t *testing.T) {
	origVersion := version.Version
	origNewClient := newGitHubClient
	origReplace := replaceBinary
	t.Cleanup(func() {
		version.Version = origVersion
		newGitHubClient = origNewClient
		replaceBinary = origReplace
	})

	version.Version = "1.0.0"

	assetName := expectedAssetName("1.0.0")
	srv := fakeGitHubServer(t, "v1.0.0", assetName, nil)
	defer srv.Close()

	newGitHubClient = func() *github.Client {
		c, _ := github.NewClient(nil).WithEnterpriseURLs(srv.URL, srv.URL)
		return c
	}

	replaceBinary = func(_ []byte) error {
		t.Fatal("replaceBinary should not be called when already up to date")
		return nil
	}

	err := Update(context.Background())
	if err != nil {
		t.Fatalf("Update() returned error: %v", err)
	}
}

func TestUpdate_DevVersionAlwaysUpdates(t *testing.T) {
	origVersion := version.Version
	origNewClient := newGitHubClient
	origReplace := replaceBinary
	t.Cleanup(func() {
		version.Version = origVersion
		newGitHubClient = origNewClient
		replaceBinary = origReplace
	})

	version.Version = "dev"

	binaryContent := []byte("dev-update-binary")
	assetName := expectedAssetName("0.0.1")
	archive := buildTarGz(t, "infracost-preview", binaryContent)
	if runtime.GOOS == "windows" {
		t.Skip("tar.gz test not applicable on windows")
	}

	srv := fakeGitHubServer(t, "v0.0.1", assetName, archive)
	defer srv.Close()

	newGitHubClient = func() *github.Client {
		c, _ := github.NewClient(nil).WithEnterpriseURLs(srv.URL, srv.URL)
		return c
	}

	var called bool
	replaceBinary = func(_ []byte) error {
		called = true
		return nil
	}

	err := Update(context.Background())
	if err != nil {
		t.Fatalf("Update() returned error: %v", err)
	}
	if !called {
		t.Fatal("expected replaceBinary to be called for dev version")
	}
}

func TestUpdate_NoMatchingAsset(t *testing.T) {
	origVersion := version.Version
	origNewClient := newGitHubClient
	origReplace := replaceBinary
	t.Cleanup(func() {
		version.Version = origVersion
		newGitHubClient = origNewClient
		replaceBinary = origReplace
	})

	version.Version = "0.0.1"

	// Serve a release with a mismatched asset name
	srv := fakeGitHubServer(t, "v1.0.0", "wrong-asset-name.tar.gz", nil)
	defer srv.Close()

	newGitHubClient = func() *github.Client {
		c, _ := github.NewClient(nil).WithEnterpriseURLs(srv.URL, srv.URL)
		return c
	}

	replaceBinary = func(_ []byte) error {
		t.Fatal("replaceBinary should not be called when no asset matches")
		return nil
	}

	err := Update(context.Background())
	if err == nil {
		t.Fatal("expected error for missing asset")
	}
}
