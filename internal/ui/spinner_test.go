package ui

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// When no spinner is running (the case in tests, where stderr is not a TTY so
// no bubbletea program is ever started) pause/resume must be safe no-ops.
func TestPauseResumeSpinnerNoActiveProgram(t *testing.T) {
	require.NotPanics(t, func() {
		PauseSpinner()
		ResumeSpinner()
		// Resume without a preceding pause, and a double pause, are also fine.
		ResumeSpinner()
		PauseSpinner()
		PauseSpinner()
		ResumeSpinner()
	})
}

func TestWithSpinnerPausedRunsFnAndPropagatesError(t *testing.T) {
	called := false
	wantErr := errors.New("boom")

	gotErr := WithSpinnerPaused(func() error {
		called = true
		return wantErr
	})

	assert.True(t, called, "fn should have been called")
	assert.ErrorIs(t, gotErr, wantErr)
}

func TestWithSpinnerPausedNilError(t *testing.T) {
	assert.NoError(t, WithSpinnerPaused(func() error { return nil }))
}

// A panic in fn must still resume the spinner (via the deferred ResumeSpinner)
// and propagate the panic rather than swallowing it.
func TestWithSpinnerPausedResumesOnPanic(t *testing.T) {
	assert.PanicsWithValue(t, "kaboom", func() {
		_ = WithSpinnerPaused(func() error {
			panic("kaboom")
		})
	})
}
