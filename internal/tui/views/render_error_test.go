package views_test

import (
	"errors"
	"testing"

	"github.com/infracost/cli/internal/tui/views"
)

func TestRenderError_NonAuth(t *testing.T) {
	err := errors.New("failed to scan target: parser error in main.tf")
	got := views.RenderError(err, false, 100)
	assertGolden(t, got)
}

func TestRenderError_Auth(t *testing.T) {
	// authError = true should surface the in-TUI `a` recovery
	// affordance at the top of the recovery checklist.
	err := errors.New("api request failed: 401 unauthorized")
	got := views.RenderError(err, true, 100)
	assertGolden(t, got)
}

func TestRenderError_NilReturnsEmpty(t *testing.T) {
	got := views.RenderError(nil, false, 100)
	if got != "" {
		t.Errorf("expected empty string for nil error, got %q", got)
	}
}
