package stacktrace

import (
	"runtime/debug"
	"strings"
	"testing"
)

func TestSanitize(t *testing.T) {
	raw := debug.Stack()

	result := Sanitize(raw, "github.com/infracost/cli/")

	if result == "" {
		t.Fatal("Sanitize returned empty string for valid stack trace")
	}

	for _, line := range strings.Split(result, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.Contains(trimmed, ".go:") {
			continue
		}
		if strings.Contains(trimmed, "/Users/") || strings.Contains(trimmed, "/home/") || strings.Contains(trimmed, "C:\\") {
			t.Errorf("sanitized stack trace still contains an absolute path: %s", trimmed)
		}
	}
}

func TestSanitizeStripsImportPrefix(t *testing.T) {
	raw := debug.Stack()

	result := Sanitize(raw, "github.com/infracost/cli/")

	if strings.Contains(result, "github.com/infracost/cli/") {
		t.Errorf("sanitized stack trace still contains the module import prefix")
	}
}

func TestSanitizeInvalidInput(t *testing.T) {
	result := Sanitize([]byte("not a valid stack trace"))

	if result != "" {
		t.Errorf("expected empty string for invalid input, got: %s", result)
	}
}