package auth

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const localOrgFile = ".infracost/org"

// ReadLocalOrg reads the org slug from .infracost/org in the given directory.
// Returns empty string if the file doesn't exist.
func ReadLocalOrg(dir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(dir, localOrgFile)) // nolint:gosec // G304: path is constructed from user-controlled working directory, not user input
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("reading local org file: %w", err)
	}

	return strings.TrimSpace(string(data)), nil
}

// WriteLocalOrg writes the org slug to .infracost/org in the given directory.
func WriteLocalOrg(dir, slug string) error {
	path := filepath.Join(dir, localOrgFile)

	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		return fmt.Errorf("creating .infracost directory: %w", err)
	}

	if err := os.WriteFile(path, []byte(slug+"\n"), 0600); err != nil {
		return fmt.Errorf("writing local org file: %w", err)
	}

	return nil
}
