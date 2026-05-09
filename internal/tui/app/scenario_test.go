package app

import (
	"context"
	"errors"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/infracost/cli/internal/config"
	"github.com/infracost/cli/internal/format"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Scenario tests cover the Model state machine end-to-end without
// needing teatest / a PTY. Each test synthesizes the messages a real
// session would produce and asserts on the resulting model state.
//
// This intentionally exercises Update by value-receiver — the same
// way bubbletea calls it — and the results round-trip through the
// tea.Model interface so we catch any "mutations didn't make it
// back" bugs (we shipped one of those during development).

// newTestModel builds a Model with width/height set as if a
// WindowSizeMsg had already arrived. cfg is a real but otherwise
// empty Config — fine for tests that don't trigger code paths
// requiring it.
func newTestModel(t *testing.T) Model {
	t.Helper()
	m := NewModel(context.Background(), &config.Config{}, "test")
	m.width = 100
	m.height = 50
	m.resize()
	return m
}

// step pushes one message through Update and returns the next model
// + cmd, asserting the updated value can be cast back to Model. The
// runtime always passes Model values to Update, so the returned
// tea.Model should always satisfy this.
func step(t *testing.T, m Model, msg tea.Msg) (Model, tea.Cmd) {
	t.Helper()
	next, cmd := m.Update(msg)
	mn, ok := next.(Model)
	require.True(t, ok, "Update returned %T, want Model", next)
	return mn, cmd
}

// TestInit_StartsCacheCheckAndSpinner verifies the bootstrap sequence
// — loadCacheCmd to populate the initial view, plus the spinner tick
// so the loading state animates instead of looking frozen.
func TestInit_StartsCacheCheckAndSpinner(t *testing.T) {
	m := newTestModel(t)
	cmd := m.Init()

	assert.NotNil(t, cmd, "Init should return a Cmd batch")
	assert.Equal(t, "Checking cache...", m.stage)
	assert.Equal(t, ViewLoading, m.view)
}

// TestCacheHit_TransitionsToMain covers the happy path: bare
// `infracost` in an IaC directory with a fresh cache hit lands on
// ViewMain immediately, with output populated and stage cleared.
func TestCacheHit_TransitionsToMain(t *testing.T) {
	m := newTestModel(t)

	out := &format.Output{
		Currency: "USD",
		Projects: []format.ProjectOutput{{ProjectName: "demo"}},
	}
	m, _ = step(t, m, cacheLoadedMsg{
		output: out,
		cwd:    "/tmp/demo",
	})

	assert.Equal(t, ViewMain, m.view)
	assert.Same(t, out, m.output)
	assert.True(t, m.fromCache)
	assert.Equal(t, "", m.stage, "stage should clear once we have data")
}

// TestQ_QuitsAndRecordsReason: the q key should both ask tea to quit
// and stamp the session telemetry with how the user exited so the
// downstream event distinguishes voluntary quits from crashes.
func TestQ_QuitsAndRecordsReason(t *testing.T) {
	m := newTestModel(t)
	m.view = ViewMain

	m, cmd := step(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})

	assert.True(t, m.quitting)
	assert.Equal(t, "q", m.session.terminatedReason)
	assert.NotNil(t, cmd, "q should return tea.Quit")
}

// TestCtrlC_QuitsAndRecordsReason mirrors TestQ but for ^C, which
// uses a different reason code so the analytics can split user vs
// signal exits.
func TestCtrlC_QuitsAndRecordsReason(t *testing.T) {
	m := newTestModel(t)
	m.view = ViewMain

	m, _ = step(t, m, tea.KeyMsg{Type: tea.KeyCtrlC})

	assert.True(t, m.quitting)
	assert.Equal(t, "ctrlC", m.session.terminatedReason)
}

// TestHelpToggle_OpenAndClose: ? opens the help overlay; ? or esc
// closes it. helpOpen drives which view the renderer picks, so the
// flag is the only thing the test needs to assert on.
func TestHelpToggle_OpenAndClose(t *testing.T) {
	m := newTestModel(t)
	m.view = ViewMain

	m, _ = step(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	assert.True(t, m.helpOpen)

	m, _ = step(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	assert.False(t, m.helpOpen, "esc should close the help overlay")
}

// TestHelpOpen_SwallowsBackgroundKeys ensures the underlying view
// can't be driven through the modal — pressing s while help is open
// must not advance the sort mode behind the user's back.
func TestHelpOpen_SwallowsBackgroundKeys(t *testing.T) {
	m := newTestModel(t)
	m.view = ViewMain
	m.helpOpen = true
	startSort := m.sortMode

	m, _ = step(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})

	assert.Equal(t, startSort, m.sortMode, "s should be swallowed while help is open")
	assert.True(t, m.helpOpen, "non-quit keys shouldn't close the overlay")
}

// TestSortCycle_StepsThroughThreeModesAndWraps drives the s key four
// times to walk the full cycle and back to the start. Combined with
// the unit test on SortMode.next this covers both the wiring and the
// cycle math.
func TestSortCycle_StepsThroughThreeModesAndWraps(t *testing.T) {
	m := newTestModel(t)
	m.view = ViewMain
	m.output = &format.Output{Currency: "USD"} // s requires output

	require.Equal(t, SortByCostDesc, m.sortMode)

	m, _ = step(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	assert.Equal(t, SortByAddressAsc, m.sortMode)

	m, _ = step(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	assert.Equal(t, SortByTypeAsc, m.sortMode)

	m, _ = step(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	assert.Equal(t, SortByCostDesc, m.sortMode)

	assert.True(t, m.session.sortChanged, "sort changes should bump telemetry")
}

// TestFilterFlow_TypeCommitClear walks the user through opening the
// filter input, typing characters, committing with enter, and
// clearing with esc. Mirrors the way the resource list is meant to
// be filtered in real usage.
func TestFilterFlow_TypeCommitClear(t *testing.T) {
	m := newTestModel(t)
	m.view = ViewMain
	m.output = &format.Output{Currency: "USD"}

	// /
	m, _ = step(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	require.True(t, m.filtering)
	assert.True(t, m.session.filterUsed)

	// Type "rds" — bubbles textinput accepts each rune via Update.
	for _, r := range "rds" {
		m, _ = step(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	assert.Equal(t, "rds", m.filterExpr)

	// Enter commits — leaves filtering mode but keeps the expression
	// active so the inline banner shows it.
	m, _ = step(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	assert.False(t, m.filtering)
	assert.Equal(t, "rds", m.filterExpr)

	// Esc clears the committed filter (precedence: filter clear
	// beats back-to-picker).
	m, _ = step(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	assert.Equal(t, "", m.filterExpr)
}

// TestEscBackToPicker_OnlyWhenEnteredViaPicker checks the navigation
// guard: pressing esc in ViewMain should *only* return to the picker
// if the user originally got there from the picker. A user who
// landed in ViewMain from cwd-is-IaC shouldn't get teleported into a
// random project list.
func TestEscBackToPicker_OnlyWhenEnteredViaPicker(t *testing.T) {
	t.Run("from-picker → returns", func(t *testing.T) {
		m := newTestModel(t)
		m.view = ViewMain
		m.enteredViaPicker = true

		m, _ = step(t, m, tea.KeyMsg{Type: tea.KeyEsc})

		assert.Equal(t, ViewPicker, m.view)
	})

	t.Run("direct-cwd → no-op", func(t *testing.T) {
		m := newTestModel(t)
		m.view = ViewMain
		m.enteredViaPicker = false

		m, _ = step(t, m, tea.KeyMsg{Type: tea.KeyEsc})

		assert.Equal(t, ViewMain, m.view, "esc should be a no-op when there's no picker to return to")
	})
}

// TestScanErr_TransitionsToErrorView: a scan failure surfaces the
// error overlay rather than crashing the program. authError shape
// detection is covered separately by TestIsAuthError; this just
// asserts the view dispatches.
func TestScanErr_TransitionsToErrorView(t *testing.T) {
	m := newTestModel(t)
	m.view = ViewLoading

	m, _ = step(t, m, scanErrMsg{err: errors.New("scan failed")})

	assert.Equal(t, ViewError, m.view)
	assert.Equal(t, "scan failed", m.scanErr.Error())
	assert.Equal(t, "", m.stage, "stage should clear when we transition to error")
}

// TestRefresh_BumpsCounterAndStartsLoading: r kicks off a fresh scan,
// keeps the user on a sensible view (ViewMain or, if they were on
// the error screen, transitioned back), and bumps the refresh
// counter for telemetry.
func TestRefresh_BumpsCounterAndStartsLoading(t *testing.T) {
	m := newTestModel(t)
	m.view = ViewMain
	m.output = &format.Output{Currency: "USD"}

	startCount := m.session.refreshes

	m, cmd := step(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})

	assert.Equal(t, startCount+1, m.session.refreshes)
	assert.Equal(t, "Scanning...", m.stage)
	assert.NotNil(t, cmd, "r should return a scan command")
}

// TestScanDoneFromCache_DoesNotBumpScanCounter: a cache-hit scanDone
// shouldn't count as "the user observed a fresh scan" — only fresh
// runs bump the counter so the analytics distinguish cold-load from
// active scanning.
func TestScanDoneFromCache_DoesNotBumpScanCounter(t *testing.T) {
	m := newTestModel(t)
	m.view = ViewLoading
	startCount := m.session.scansRun

	m, _ = step(t, m, scanDoneMsg{
		output:    &format.Output{Currency: "USD"},
		cwd:       "/tmp",
		fromCache: true,
	})

	assert.Equal(t, startCount, m.session.scansRun, "cache hits don't count as scans")
	assert.Equal(t, ViewMain, m.view)
}

func TestScanDoneFresh_BumpsScanCounter(t *testing.T) {
	m := newTestModel(t)
	m.view = ViewLoading
	startCount := m.session.scansRun

	m, _ = step(t, m, scanDoneMsg{
		output:    &format.Output{Currency: "USD"},
		cwd:       "/tmp",
		fromCache: false,
	})

	assert.Equal(t, startCount+1, m.session.scansRun)
}
