package views

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/infracost/cli/internal/tui/discovery"
	"github.com/infracost/cli/internal/tui/styles"
	"github.com/infracost/cli/internal/ui"
)

// Picker is the empty-state project picker shown when the user opens
// the TUI in a directory that doesn't look like an IaC project. It
// displays a live-updating list of candidates discovered by the
// background $HOME walker and lets the user pick one with the cursor.
//
// The picker owns its own cursor and width; the parent model passes
// the discovered project list and pane dimensions on each render so
// the picker stays a pure view component (no scan / I/O concerns
// here — those belong to the model).
type Picker struct {
	cursor int
	width  int
	height int
}

// NewPicker constructs an empty picker.
func NewPicker() Picker { return Picker{} }

// SetSize updates pane dimensions. Called on tea.WindowSizeMsg by the
// parent model.
func (p *Picker) SetSize(width, height int) {
	p.width = width
	p.height = height
}

// Cursor returns the currently selected index, or -1 if the picker
// has no entries.
func (p Picker) Cursor() int { return p.cursor }

// Update applies a key event to the picker. Returns the message back
// when it represents a "selection committed" action so the parent
// model can react. Movement keys are absorbed silently.
func (p *Picker) Update(msg tea.Msg, total int) tea.Cmd {
	km, ok := msg.(tea.KeyMsg)
	if !ok || total == 0 {
		return nil
	}
	switch km.String() {
	case "up", "k":
		if p.cursor > 0 {
			p.cursor--
		}
	case "down", "j":
		if p.cursor < total-1 {
			p.cursor++
		}
	case "home", "g":
		p.cursor = 0
	case "end", "G":
		p.cursor = total - 1
	}
	return nil
}

// View renders the picker. discovering toggles a "still searching"
// hint below the list so the user knows the empty list isn't final.
// filterRow is rendered just below the title — pass the filter input
// view while the user types, the filter-status banner once they
// commit, or "" to omit. The previous in-box "↑↓ navigate · enter
// select · q quit" hint has been removed because the status bar
// already carries those shortcuts.
//
// allCount is the total number of projects discovered, used in the
// footer when the user has filtered to a subset so they can see
// "showing 3 of 17". Pass 0 to display the unfiltered count.
func (p Picker) View(projects []discovery.Project, discovering bool, filterRow string, allCount int) string {
	if p.width <= 0 || p.height <= 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString(styles.Bold().Render("Pick a project to scan"))
	b.WriteString("\n")
	if filterRow != "" {
		b.WriteString(filterRow)
		b.WriteString("\n")
	}
	b.WriteString("\n")

	if len(projects) == 0 {
		switch {
		case allCount > 0:
			b.WriteString(styles.Muted().Italic(true).Render("  No projects match the current filter."))
		case discovering:
			b.WriteString(styles.Muted().Italic(true).Render("  Searching $HOME for IaC projects..."))
		default:
			b.WriteString(styles.Muted().Italic(true).Render("  No IaC projects found under $HOME."))
		}
		return b.String()
	}

	// Reserve a few rows for chrome (title + optional filter row +
	// blank + bottom hint) so projects scroll within the remaining
	// space rather than pushing the footer off the bottom.
	chrome := 4
	if filterRow != "" {
		chrome++
	}
	visible := p.height - chrome
	if visible < 1 {
		visible = 1
	}
	offset := 0
	if p.cursor >= visible {
		offset = p.cursor - visible + 1
	}
	end := offset + visible
	if end > len(projects) {
		end = len(projects)
	}

	for i := offset; i < end; i++ {
		row := p.renderRow(projects[i], i == p.cursor)
		b.WriteString(row)
		b.WriteString("\n")
	}

	b.WriteString("\n")
	switch {
	case discovering:
		b.WriteString(styles.Muted().Italic(true).Render("  Still searching..."))
	case allCount > len(projects):
		b.WriteString(styles.Muted().Render(fmt.Sprintf("  showing %d of %d projects", len(projects), allCount)))
	default:
		b.WriteString(styles.Muted().Render(fmt.Sprintf("  %d projects found", len(projects))))
	}

	return b.String()
}

// renderRow formats one project row. The selected row is highlighted
// in the list-selected style for visual parity with the resource list.
func (p Picker) renderRow(project discovery.Project, selected bool) string {
	pathW := p.width - 4
	if pathW < 16 {
		pathW = 16
	}
	display := project.Path
	// Replace $HOME with "~" for visual brevity. Most picker entries
	// share the home-dir prefix, so collapsing it makes the visible
	// difference (the actual project path) stand out.
	if home, err := homeDir(); err == nil && strings.HasPrefix(display, home) {
		display = "~" + strings.TrimPrefix(display, home)
	}
	if ui.PrintableWidth(display) > pathW {
		display = display[:pathW-1] + "…"
	}

	line := "  " + display
	if selected {
		line = styles.ListSelected().Render(line + strings.Repeat(" ", pathW+2-ui.PrintableWidth(line)))
	}
	return line
}

// homeDir returns the user's home directory; thin wrapper so tests
// can stub it without depending on os.UserHomeDir's caching.
var homeDir = func() (string, error) {
	if h, err := userHomeDir(); err == nil {
		return h, nil
	}
	return "", fmt.Errorf("home directory not available")
}
