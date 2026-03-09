package tfutils

import (
	"os/exec"
	"strings"
)

// GetCurrentWorkspace returns the current Terraform workspace for the given directory.
// If it fails to get the workspace, it returns an empty string.
func GetCurrentWorkspace(dir string) string {
	cmd := exec.Command("terraform", "workspace", "show")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}

	return strings.TrimSpace(string(out))
}
