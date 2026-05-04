package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// resolveInfracostBin guarantees the cell subprocesses see an `infracost`
// binary that matches the code in this repo (so `--llm` and the dedup
// schema changes are present). If the user supplied an explicit path via
// --infracost-bin we use it; otherwise we walk up from cwd to the repo
// root, run `go build -o <cache>/bin/infracost ./`, and return that path.
//
// The caller prepends filepath.Dir(returnedPath) to PATH so any bash
// subprocess (i.e. anything claude runs) finds this binary first when it
// types `infracost`.
func resolveInfracostBin(explicit, cacheRoot string) (string, error) {
	if explicit != "" {
		abs, err := filepath.Abs(explicit)
		if err != nil {
			return "", err
		}
		if _, err := os.Stat(abs); err != nil {
			return "", fmt.Errorf("--infracost-bin %s: %w", abs, err)
		}
		return abs, nil
	}

	repoRoot, err := findRepoRoot()
	if err != nil {
		return "", fmt.Errorf("locate repo root for go build: %w", err)
	}

	binDir := filepath.Join(cacheRoot, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return "", err
	}
	binPath := filepath.Join(binDir, "infracost")

	fmt.Printf("Building infracost from %s → %s\n", repoRoot, binPath)
	cmd := exec.Command("go", "build", "-o", binPath, "./")
	cmd.Dir = repoRoot
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("go build infracost: %w", err)
	}
	return binPath, nil
}

// findRepoRoot walks up from the current working directory looking for a
// go.mod. The bench is expected to be invoked from the repo root via
// `go run ./tools/llmbench`, but we walk anyway so it works from a sibling
// shell prompt under tools/ or anywhere else inside the repo.
func findRepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no go.mod found walking up from %s", dir)
		}
		dir = parent
	}
}
