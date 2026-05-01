package inspect

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// updateGolden controls whether tests rewrite their golden files instead of
// comparing against them. Pass `-update` to `go test` to refresh every golden
// in this package, or combine with `-run TestX` to refresh just one.
var updateGolden = flag.Bool("update", false, "update inspect golden files in testdata/")

// assertGolden compares got against the golden file for the calling test.
// When -update is passed it overwrites the golden instead of comparing.
func assertGolden(t *testing.T, got string) {
	t.Helper()

	name := strings.ReplaceAll(t.Name(), "/", "_") + ".golden"
	path := filepath.Join("testdata", name)

	if *updateGolden {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatalf("mkdir testdata: %v", err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatalf("write golden %s: %v", path, err)
		}
		return
	}

	wantBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v (run `go test ./internal/inspect/ -update` to create)", path, err)
	}
	want := string(wantBytes)

	if got != want {
		t.Errorf(
			"output does not match %s (run `go test ./internal/inspect/ -update` to refresh)\n--- want ---\n%s--- got ---\n%s",
			path, want, got,
		)
	}
}
