package browser

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenCommand_WindowsUsesRundll32(t *testing.T) {
	url := "https://login.infracost.io/authorize?audience=a&scope=b+c"

	cmd, err := openCommand("windows", url)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	exe := filepath.Base(cmd.Path)
	if !strings.EqualFold(exe, "rundll32") && !strings.EqualFold(exe, "rundll32.exe") {
		t.Fatalf("expected rundll32 executable, got %q", cmd.Path)
	}

	if len(cmd.Args) != 3 {
		t.Fatalf("expected 3 args, got %d (%v)", len(cmd.Args), cmd.Args)
	}

	if cmd.Args[1] != "url.dll,FileProtocolHandler" {
		t.Fatalf("expected URL handler argument, got %q", cmd.Args[1])
	}

	if cmd.Args[2] != url {
		t.Fatalf("expected full URL to be preserved, got %q", cmd.Args[2])
	}
}

func TestOpenCommand_UnsupportedPlatform(t *testing.T) {
	_, err := openCommand("plan9", "https://example.com")
	if err == nil {
		t.Fatal("expected error for unsupported platform")
	}
}
