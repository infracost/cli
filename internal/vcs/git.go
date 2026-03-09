package vcs

import (
	"os/exec"
	"strings"
)

// GetCurrentBranch returns the current git branch name for the given directory.
// If it fails to get the branch name, it returns an empty string.
func GetCurrentBranch(dir string) string {
	cmd := exec.Command("git", "branch", "--show-current")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// GetRepoRoot returns the absolute path to the git repository root for the given
// path. The input can be either a file or a directory within the repository.
// If the path is not inside a git repository or an error occurs, it returns an empty string.
func GetRepoRoot(path string) string {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	cmd.Dir = path
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// GetRemoteURL returns the git remote URL for the given directory.
// It defaults to the "origin" remote. If it fails to get the URL, it returns an empty string.
func GetRemoteURL(dir string) string {
	cmd := exec.Command("git", "remote", "get-url", "origin")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
