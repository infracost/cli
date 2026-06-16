package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"

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

func fakeCLIReleaseServer(t *testing.T, latestVersion string, assetContent []byte) *httptest.Server {
	t.Helper()

	assetName := expectedAssetName()
	assetSHA := sha256.Sum256(assetContent)
	assetSHAString := hex.EncodeToString(assetSHA[:])

	mux := http.NewServeMux()
	mux.HandleFunc(fmt.Sprintf("/cli/%s/%s/latest/version", runtime.GOOS, runtime.GOARCH), func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(latestVersion + "\n"))
	})
	mux.HandleFunc(fmt.Sprintf("/cli/%s/%s/%s/%s.sha256", runtime.GOOS, runtime.GOARCH, latestVersion, assetName), func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(assetSHAString + "  " + assetName + "\n"))
	})
	mux.HandleFunc(fmt.Sprintf("/cli/%s/%s/%s/%s", runtime.GOOS, runtime.GOARCH, latestVersion, assetName), func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(assetContent)
	})
	return httptest.NewServer(mux)
}

func withCLIReleaseBaseURL(t *testing.T, url string) {
	t.Helper()
	orig := cliReleaseBaseURL
	cliReleaseBaseURL = func() string { return url + "/cli" }
	t.Cleanup(func() { cliReleaseBaseURL = orig })
}

func TestReplaceBinaryAtPath_FallsBackToCopyOnPermissionError(t *testing.T) {
	origCopy := copyBinaryWithCommand
	t.Cleanup(func() {
		copyBinaryWithCommand = origCopy
	})

	execPath := filepath.Join(t.TempDir(), "infracost")
	if err := os.WriteFile(execPath, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}

	newBinary := []byte("new")
	permissionErr := func(_ string, _ os.FileMode, _ []byte) error {
		return &os.PathError{Op: "open", Path: execPath, Err: os.ErrPermission}
	}

	var copiedFrom, copiedTo string
	var copiedMode os.FileMode
	copyBinaryWithCommand = func(srcPath, dstPath string, mode os.FileMode) error {
		copiedFrom = srcPath
		copiedTo = dstPath
		copiedMode = mode

		data, err := os.ReadFile(srcPath)
		if err != nil {
			return err
		}
		if !bytes.Equal(data, newBinary) {
			t.Fatalf("expected temp binary %q, got %q", newBinary, data)
		}
		return nil
	}

	if err := replaceBinaryAtPathWith(execPath, newBinary, permissionErr); err != nil {
		t.Fatalf("replaceBinaryAtPathWith() returned error: %v", err)
	}
	if copiedFrom == "" || copiedTo != execPath || copiedMode != 0o755 {
		t.Fatalf("unexpected copy call: from=%q to=%q mode=%#o", copiedFrom, copiedTo, copiedMode)
	}
}

func TestReplaceBinaryAtPath_ReturnsAtomicErrorWhenNotPermission(t *testing.T) {
	origCopy := copyBinaryWithCommand
	t.Cleanup(func() {
		copyBinaryWithCommand = origCopy
	})

	execPath := filepath.Join(t.TempDir(), "infracost")
	if err := os.WriteFile(execPath, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}

	invalidErr := func(_ string, _ os.FileMode, _ []byte) error {
		return os.ErrInvalid
	}
	copyBinaryWithCommand = func(_, _ string, _ os.FileMode) error {
		t.Fatal("copyBinaryWithCommand should not be called for non-permission errors")
		return nil
	}

	if err := replaceBinaryAtPathWith(execPath, []byte("new"), invalidErr); err == nil {
		t.Fatal("expected error")
	}
}

func TestCheckLatestVersion(t *testing.T) {
	origVersion := version.Version
	t.Cleanup(func() { version.Version = origVersion })

	version.Version = "0.9.0"
	srv := fakeCLIReleaseServer(t, "v1.0.0", nil)
	defer srv.Close()
	withCLIReleaseBaseURL(t, srv.URL)

	info, err := CheckLatestVersion(context.Background())
	if err != nil {
		t.Fatalf("CheckLatestVersion() returned error: %v", err)
	}
	if info.Current != "0.9.0" || info.Latest != "v1.0.0" || info.UpToDate {
		t.Fatalf("unexpected info: %+v", info)
	}
}

func TestUpdate_NewerVersionAvailable(t *testing.T) {
	origVersion := version.Version
	origReplace := replaceBinary
	t.Cleanup(func() {
		version.Version = origVersion
		replaceBinary = origReplace
	})

	version.Version = "0.0.1"

	binaryContent := []byte("new-binary-content")
	archive := buildTarGz(t, "infracost", binaryContent)
	if runtime.GOOS == "windows" {
		t.Skip("tar.gz test not applicable on windows")
	}

	srv := fakeCLIReleaseServer(t, "v1.0.0", archive)
	defer srv.Close()
	withCLIReleaseBaseURL(t, srv.URL)

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
	origReplace := replaceBinary
	t.Cleanup(func() {
		version.Version = origVersion
		replaceBinary = origReplace
	})

	version.Version = "1.0.0"

	srv := fakeCLIReleaseServer(t, "v1.0.0", nil)
	defer srv.Close()
	withCLIReleaseBaseURL(t, srv.URL)

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
	origReplace := replaceBinary
	t.Cleanup(func() {
		version.Version = origVersion
		replaceBinary = origReplace
	})

	version.Version = "dev"

	binaryContent := []byte("dev-update-binary")
	archive := buildTarGz(t, "infracost", binaryContent)
	if runtime.GOOS == "windows" {
		t.Skip("tar.gz test not applicable on windows")
	}

	srv := fakeCLIReleaseServer(t, "v0.0.1", archive)
	defer srv.Close()
	withCLIReleaseBaseURL(t, srv.URL)

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

func TestUpdate_NoMatchingBinaryInArchive(t *testing.T) {
	origVersion := version.Version
	origReplace := replaceBinary
	t.Cleanup(func() {
		version.Version = origVersion
		replaceBinary = origReplace
	})

	version.Version = "0.0.1"

	archive := buildTarGz(t, "wrong-binary", []byte("nope"))
	if runtime.GOOS == "windows" {
		t.Skip("tar.gz test not applicable on windows")
	}

	srv := fakeCLIReleaseServer(t, "v1.0.0", archive)
	defer srv.Close()
	withCLIReleaseBaseURL(t, srv.URL)

	replaceBinary = func(_ []byte) error {
		t.Fatal("replaceBinary should not be called when no binary matches")
		return nil
	}

	err := Update(context.Background())
	if err == nil {
		t.Fatal("expected error for missing binary")
	}
}
