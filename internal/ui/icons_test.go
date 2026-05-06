package ui

import (
	"bytes"
	"strings"
	"sync"
	"testing"
)

// resetIconDetection forces detectIconProtocol to run again on its next
// call and drops any cached encoded icons. sync.Once has no public reset,
// so we replace it wholesale.
func resetIconDetection(t *testing.T) {
	t.Helper()
	iconOnce = sync.Once{}
	iconCap = iconNone
	iconCacheMu.Lock()
	iconCache = make(map[string]string)
	iconCacheMu.Unlock()
}

func TestDetectIconProtocol(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want iconProtocol
	}{
		{
			name: "no terminal hints → none",
			env:  map[string]string{},
			want: iconNone,
		},
		{
			name: "iTerm via LC_TERMINAL",
			env:  map[string]string{"LC_TERMINAL": "iterm2"},
			want: iconITerm,
		},
		{
			name: "Kitty via KITTY_WINDOW_ID",
			env:  map[string]string{"KITTY_WINDOW_ID": "1"},
			want: iconKitty,
		},
		{
			name: "tmux blocks even when iTerm capable",
			env:  map[string]string{"LC_TERMINAL": "iterm2", "TMUX": "/tmp/tmux"},
			want: iconNone,
		},
		{
			name: "INFRACOST_ICONS=on overrides tmux block",
			env:  map[string]string{"LC_TERMINAL": "iterm2", "TMUX": "/tmp/tmux", "INFRACOST_ICONS": "on"},
			want: iconITerm,
		},
		{
			name: "INFRACOST_ICONS=off forces none",
			env:  map[string]string{"LC_TERMINAL": "iterm2", "INFRACOST_ICONS": "off"},
			want: iconNone,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, k := range []string{"LC_TERMINAL", "KITTY_WINDOW_ID", "TMUX", "STY", "TERM", "TERM_PROGRAM", "INFRACOST_ICONS"} {
				t.Setenv(k, "")
			}
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			resetIconDetection(t)
			if got := detectIconProtocol(); got != tc.want {
				t.Fatalf("detectIconProtocol() = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestRenderIconUnknownSlug(t *testing.T) {
	t.Setenv("LC_TERMINAL", "iterm2")
	resetIconDetection(t)
	var buf bytes.Buffer
	if err := renderIcon(&buf, "definitely-not-a-real-slug"); err != nil {
		t.Fatalf("renderIcon returned error for missing slug: %v", err)
	}
	if buf.Len() != 0 {
		t.Fatalf("expected no output for missing slug, got %d bytes", buf.Len())
	}
}

func TestRenderIconITermEmitsHeader(t *testing.T) {
	t.Setenv("LC_TERMINAL", "iterm2")
	t.Setenv("INFRACOST_ICONS", "")
	t.Setenv("TMUX", "")
	t.Setenv("STY", "")
	resetIconDetection(t)
	var buf bytes.Buffer
	if err := renderIcon(&buf, "claude"); err != nil {
		t.Fatalf("renderIcon: %v", err)
	}
	if !strings.HasPrefix(buf.String(), "\x1b]1337;File=") {
		t.Fatalf("expected iTerm image header prefix, got %q", buf.String()[:min(40, buf.Len())])
	}
}

func TestRenderIconKittyEmitsHeader(t *testing.T) {
	t.Setenv("KITTY_WINDOW_ID", "1")
	t.Setenv("INFRACOST_ICONS", "")
	t.Setenv("TMUX", "")
	t.Setenv("STY", "")
	t.Setenv("LC_TERMINAL", "")
	resetIconDetection(t)
	var buf bytes.Buffer
	if err := renderIcon(&buf, "claude"); err != nil {
		t.Fatalf("renderIcon: %v", err)
	}
	if !strings.HasPrefix(buf.String(), "\x1b_G") {
		t.Fatalf("expected Kitty image header prefix, got %q", buf.String()[:min(40, buf.Len())])
	}
}
