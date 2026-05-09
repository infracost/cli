package app

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/infracost/cli/internal/format"
	"github.com/stretchr/testify/assert"
)

// Tests that pin down the focus + tab + esc interactions, since
// these are easy to get wrong as the model evolves.

func multiProjectOutput() *format.Output {
	return &format.Output{
		Currency: "USD",
		Projects: []format.ProjectOutput{
			{ProjectName: "web"},
			{ProjectName: "api"},
		},
	}
}

func TestTab_OnlySwitchesProjectsWhenListFocused(t *testing.T) {
	m := newTestModel(t)
	m.view = ViewMain
	m.output = multiProjectOutput()

	// List focused → tab cycles.
	require := assert.Equal
	startFilter := m.projectFilter
	m, _ = step(t, m, tea.KeyMsg{Type: tea.KeyTab})
	require(t, "web", m.projectFilter, "tab from list-focus should cycle to first project")
	_ = startFilter

	// Force focus to the detail pane and tab again — projectFilter
	// should NOT advance because tab is a no-op when detail is
	// focused.
	m.focusedPane = FocusDetail
	before := m.projectFilter
	m, _ = step(t, m, tea.KeyMsg{Type: tea.KeyTab})
	require(t, before, m.projectFilter, "tab from detail-focus should not change project filter")
}

func TestTab_NoopOnSingleProject(t *testing.T) {
	m := newTestModel(t)
	m.view = ViewMain
	m.output = &format.Output{
		Projects: []format.ProjectOutput{{ProjectName: "only"}},
	}

	m, _ = step(t, m, tea.KeyMsg{Type: tea.KeyTab})

	// Single-project scans don't need a switcher; tab should be a
	// silent no-op rather than cycle into "" / "only" / "" / ...
	assert.Equal(t, "", m.projectFilter)
	assert.Equal(t, 0, m.session.projectSwitches)
}

func TestEsc_FromDetailFocus_ReturnsFocusToList(t *testing.T) {
	m := newTestModel(t)
	m.view = ViewMain
	m.output = multiProjectOutput()
	m.focusedPane = FocusDetail
	m.detail.SetFocused(true)

	m, _ = step(t, m, tea.KeyMsg{Type: tea.KeyEsc})

	assert.Equal(t, FocusList, m.focusedPane)
	assert.False(t, m.detail.Focused(), "detail's Focused() flag should clear too")
}

func TestEsc_FocusReturnTakesPrecedenceOverFilterClear(t *testing.T) {
	// If both a filter is committed AND the detail pane is focused,
	// esc should shift focus back to the list FIRST. The filter
	// belongs to the list pane — while the user is reading detail
	// content, the filter isn't what they're operating on. A second
	// esc (now with the list focused) clears the filter.
	m := newTestModel(t)
	m.view = ViewMain
	m.output = multiProjectOutput()
	m.focusedPane = FocusDetail
	m.detail.SetFocused(true)
	m.filterExpr = "rds"

	m, _ = step(t, m, tea.KeyMsg{Type: tea.KeyEsc})

	assert.Equal(t, FocusList, m.focusedPane, "first esc should return focus to the list")
	assert.Equal(t, "rds", m.filterExpr, "filter shouldn't clear while we're moving focus")

	// Second esc, now list-focused, clears the committed filter.
	m, _ = step(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	assert.Equal(t, "", m.filterExpr)
}
