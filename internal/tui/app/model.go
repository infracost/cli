// Package app holds the root Bubble Tea model wiring for the TUI. The root
// model owns global state (cfg, current view, terminal size) and dispatches
// messages to view-specific submodels under internal/tui/views.
package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/infracost/cli/internal/cache"
	"github.com/infracost/cli/internal/config"
	"github.com/infracost/cli/internal/format"
	"github.com/infracost/cli/internal/tui/styles"
	"github.com/infracost/cli/internal/tui/views"
	"github.com/infracost/cli/internal/ui"
	"github.com/infracost/cli/version"
	"golang.org/x/oauth2"
)

// View identifies which top-level pane is currently rendered.
type View int

const (
	ViewLoading View = iota
	ViewMain
	ViewPicker
	ViewError
)

// cacheLoadedMsg carries the result of the initial cache lookup so the
// model can transition out of ViewLoading. Output is nil on cache miss.
type cacheLoadedMsg struct {
	output    *format.Output
	createdAt time.Time
	cwd       string
}

// Option configures a Model at construction time. Functional options keep
// NewModel's required-arg signature small as we layer in optional state
// (source token, picker config, etc.).
type Option func(*Model)

// WithSource provides a pre-resolved oauth2 token source so scan commands
// don't have to re-authenticate (which would prompt huh and deadlock the
// TUI on /dev/tty).
func WithSource(s oauth2.TokenSource) Option {
	return func(m *Model) { m.source = s }
}

// Model is the Bubble Tea root model.
type Model struct {
	ctx     context.Context
	cfg     *config.Config
	version string
	source  oauth2.TokenSource

	view View

	width  int
	height int

	cwd       string
	output    *format.Output
	cacheAge  time.Duration
	fromCache bool

	// Scan progress / failure state. stage is shown in the status bar
	// while a scan is in flight; scanErr drives ViewError when set.
	stage   string
	scanErr error

	// spinner ticks while the loading view is up so the user gets a
	// visual signal that work is happening rather than wondering whether
	// we hung. Owned by the root model so its tick command can be threaded
	// through Update.
	spinner spinner.Model

	// allRows is the full row set from the active Output before filtering /
	// sorting; we keep it to recompute on filter or sort changes without
	// re-walking the Output tree on every keystroke.
	allRows []views.ResourceRow

	sortMode SortMode

	// projectFilter scopes the list to a single project's resources when
	// non-empty. Cycled by Tab. "" means "all projects" (the default for
	// single-project scans).
	projectFilter string

	// Filter state. filtering=true while the user types; filterInput is
	// the textinput model that owns the cursor; filterExpr is the most
	// recently committed expression applied to the list.
	filtering   bool
	filterInput textinput.Model
	filterExpr  string

	// helpOpen is true when the user has pressed ? and the help overlay
	// is taking the place of the main view. Toggled off by ? or esc.
	helpOpen bool

	list   views.List
	detail views.Detail

	quitting bool
}

// NewModel constructs the root model. ctx is the parent cancellation
// context used to abort any background work on quit. opts lets callers
// inject pre-resolved state (e.g. a token source from tui.Run).
func NewModel(ctx context.Context, cfg *config.Config, ver string, opts ...Option) Model {
	if ver == "" {
		ver = version.Version
	}
	ti := textinput.New()
	// Don't set a placeholder: bubbles/textinput renders the cursor on
	// top of the placeholder's first character when the input is empty
	// and focused, which made the long placeholder visually look like
	// a stray "f" floating in an otherwise empty field.
	ti.Placeholder = ""
	ti.Prompt = "/ "
	ti.PromptStyle = lipgloss.NewStyle().Foreground(ui.BrandColor).Bold(true)

	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(ui.BrandColor)

	m := Model{
		ctx:         ctx,
		cfg:         cfg,
		version:     ver,
		view:        ViewLoading,
		list:        views.NewList(),
		detail:      views.NewDetail(),
		filterInput: ti,
		spinner:     sp,
		stage:       "Checking cache...",
	}
	for _, opt := range opts {
		opt(&m)
	}
	return m
}

// Init implements tea.Model. Kicks off the cache lookup so the user lands
// on real data as fast as possible when there's a fresh cached scan, and
// starts the spinner ticking so the loading view doesn't look frozen.
func (m Model) Init() tea.Cmd {
	return tea.Batch(loadCacheCmd(m.cfg), m.spinner.Tick)
}

// Update implements tea.Model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.resize()
		return m, nil

	case cacheLoadedMsg:
		m.cwd = msg.cwd
		if msg.output != nil {
			m.output = msg.output
			m.fromCache = true
			if !msg.createdAt.IsZero() {
				m.cacheAge = time.Since(msg.createdAt)
			}
			m.allRows = views.RowsFromOutput(msg.output)
			m.applyRowsToList()
			m.syncDetail()
			m.stage = ""
			m.view = ViewMain
			// Output is now set; the summary box adds rows to the chrome,
			// so recompute pane sizes — without this the list/detail
			// viewports would be sized for an output-less layout and
			// could overflow into the area the summary now occupies.
			m.resize()
			return m, nil
		}
		// Cache miss → kick off a scan so the user lands on real data
		// without manual intervention.
		m.stage = "Scanning " + m.projectLabel() + "... (this can take a moment)"
		return m, scanCmd(m.ctx, m.cfg, m.source, msg.cwd, false)

	case scanStartedMsg:
		m.stage = "Scanning..."
		return m, nil

	case scanDoneMsg:
		m.output = msg.output
		m.cwd = msg.cwd
		m.fromCache = msg.fromCache
		m.cacheAge = 0
		m.stage = ""
		m.allRows = views.RowsFromOutput(msg.output)
		m.applyRowsToList()
		m.syncDetail()
		m.view = ViewMain
		m.resize() // recompute pane heights now that summary box claims rows
		return m, nil

	case scanErrMsg:
		m.scanErr = msg.err
		m.stage = ""
		m.view = ViewError
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case tea.KeyMsg:
		// Filter input mode swallows most keys. Esc or Enter exits.
		if m.filtering {
			switch msg.String() {
			case "esc":
				m.filtering = false
				m.filterInput.SetValue("")
				m.filterExpr = ""
				m.applyRowsToList()
				m.resize() // give the list its row back
				return m, nil
			case "enter":
				m.filtering = false
				m.resize() // give the list its row back
				return m, nil
			}
			var cmd tea.Cmd
			m.filterInput, cmd = m.filterInput.Update(msg)
			m.filterExpr = m.filterInput.Value()
			m.applyRowsToList()
			return m, cmd
		}

		// Help overlay is modal — most keys close it; only quit takes
		// priority. Handle this before any view-specific dispatch so
		// help can be toggled from any state.
		if m.helpOpen {
			switch msg.String() {
			case "q", "ctrl+c":
				m.quitting = true
				return m, tea.Quit
			case "?", "esc":
				m.helpOpen = false
				return m, nil
			}
			// Swallow everything else while help is open so background
			// keystrokes don't move the underlying list cursor.
			return m, nil
		}

		switch msg.String() {
		case "q", "ctrl+c":
			m.quitting = true
			return m, tea.Quit
		case "?":
			m.helpOpen = true
			return m, nil
		case "/":
			if m.view == ViewMain && m.output != nil {
				m.filtering = true
				m.filterInput.Focus()
				m.resize() // shrink list by one row for the inline input
				return m, textinput.Blink
			}
		case "esc":
			if m.filterExpr != "" {
				m.filterInput.SetValue("")
				m.filterExpr = ""
				m.applyRowsToList()
				m.resize() // give the list its row back now that the banner is gone
				return m, nil
			}
		case "s":
			if m.view == ViewMain && m.output != nil {
				m.sortMode = m.sortMode.next()
				m.applyRowsToList()
				return m, nil
			}
		case "tab":
			if m.view == ViewMain && m.output != nil && len(m.output.Projects) > 1 {
				m.cycleProject()
				m.applyRowsToList()
				return m, nil
			}
		case "r":
			if m.view == ViewMain || m.view == ViewError {
				m.stage = "Scanning..."
				// Stay on ViewMain so the previous results remain
				// visible while the new scan runs — switching to
				// ViewLoading would blank out half the screen and
				// leak two simultaneous spinners (one in the body,
				// one in the status bar). Refresh just flips the
				// status bar into a busy indicator until the new
				// data arrives.
				if m.view == ViewError {
					m.view = ViewMain
				}
				return m, scanCmd(m.ctx, m.cfg, m.source, m.cwd, true)
			}
		}
		if m.view == ViewMain {
			cmd := m.list.Update(msg)
			m.syncDetail()
			return m, cmd
		}
	}
	return m, nil
}

// applyRowsToList re-derives the list view from allRows, filterExpr,
// projectFilter, and sortMode. Cheap enough for typical scans; the filter
// view itself debounces internally for very large outputs.
func (m *Model) applyRowsToList() {
	rows := views.FilterRows(m.allRows, m.output, m.filterExpr)
	if m.projectFilter != "" {
		scoped := make([]views.ResourceRow, 0, len(rows))
		for _, r := range rows {
			if r.Project == m.projectFilter {
				scoped = append(scoped, r)
			}
		}
		rows = scoped
	}
	rows = applySort(rows, m.sortMode)
	currency := ""
	if m.output != nil {
		currency = m.output.Currency
	}
	m.list.SetData(rows, currency)
	m.syncDetail()
}

// cycleProject advances projectFilter to the next project in the active
// Output, looping back to "all projects" after the last entry. No-op for
// single-project scans.
func (m *Model) cycleProject() {
	if m.output == nil || len(m.output.Projects) <= 1 {
		return
	}
	// "" sentinel for "all projects" comes first so users can always get
	// back to the unfiltered view by tabbing past the last project.
	names := []string{""}
	for _, p := range m.output.Projects {
		names = append(names, p.ProjectName)
	}
	cur := 0
	for i, n := range names {
		if n == m.projectFilter {
			cur = i
			break
		}
	}
	m.projectFilter = names[(cur+1)%len(names)]
}

// syncDetail keeps the right-pane in lockstep with the list cursor. The
// detail pane consumes the whole row so it has access not just to the
// ResourceOutput but also to the failing-policy facts pre-computed by
// RowsFromOutput.
func (m *Model) syncDetail() {
	if m.output == nil {
		m.detail.SetRow(nil, "")
		return
	}
	m.detail.SetRow(m.list.Selected(), m.output.Currency)
}

// View implements tea.Model.
func (m Model) View() string {
	if m.quitting {
		return ""
	}

	header := views.RenderHeader(m.version)
	statusbar := views.RenderStatusBar(views.StatusBarData{
		Project:     m.projectLabel(),
		CacheAge:    m.cacheAge,
		FromCache:   m.fromCache,
		Stage:       m.stage,
		Filter:      m.filterExpr,
		SortLabel:   m.sortMode.label(),
		SpinnerView: m.spinner.View(),
	}, m.width)

	if m.helpOpen {
		help := views.RenderHelp(m.width)
		return m.frame([]string{header, help}, statusbar)
	}

	switch m.view {
	case ViewLoading:
		stage := m.stage
		if stage == "" {
			stage = "Working..."
		}
		body := "  " + m.spinner.View() + " " + stage
		return m.frame([]string{header, body}, statusbar)
	case ViewMain:
		var sections []string
		sections = append(sections, header)
		if m.output != nil && m.width > 4 {
			summary := views.RenderSummary(m.output, m.projectFilter, m.width-4)
			titled := m.summaryTitle() + "\n" + summary
			sections = append(sections, styles.PaneBorderAccent().Width(m.width-2).Render(titled))
		}

		// Mirror the resize() arithmetic — list rounds up so it owns the
		// extra column on odd widths.
		listWidth := (m.width + 1) / 2
		detailWidth := m.width - listWidth
		listInnerWidth := listWidth - 2
		detailInnerWidth := detailWidth - 2

		var body string
		if m.output == nil {
			// Even with nothing loaded, keep the panes 50/50 so the layout
			// doesn't reflow once data arrives. The list pane shows the
			// empty hint; the detail pane stays blank.
			body = lipgloss.JoinHorizontal(lipgloss.Top,
				styles.PaneBorder().Width(listInnerWidth).Render(views.EmptyHint()),
				styles.PaneBorder().Width(detailInnerWidth).Render(""),
			)
		} else {
			listContent := m.list.View()
			switch {
			case m.filtering:
				// While typing: prepend the live input.
				listContent = m.filterInput.View() + "\n" + listContent
			case m.filterExpr != "":
				// After committing: same row, but a static banner
				// showing the active filter and the clear hint so the
				// user always knows the list is filtered and how to
				// reset it.
				listContent = views.RenderFilterStatus(m.filterExpr, listInnerWidth) + "\n" + listContent
			}
			body = lipgloss.JoinHorizontal(lipgloss.Top,
				styles.PaneBorder().Width(listInnerWidth).Render(listContent),
				styles.PaneBorder().Width(detailInnerWidth).Render(m.detail.View()),
			)
		}
		sections = append(sections, body)
		return m.frame(sections, statusbar)
	case ViewError:
		return m.frame([]string{header, views.RenderError(m.scanErr)}, statusbar)
	}
	return header
}

// frame stitches the top sections together, pads the result so the total
// height equals the screen height minus one (for the statusbar), then
// appends the statusbar pinned to the last row. This guarantees the
// status bar never floats — it always sits flush with the terminal's
// bottom edge regardless of how much content is above it.
func (m Model) frame(top []string, statusbar string) string {
	body := lipgloss.JoinVertical(lipgloss.Left, top...)
	if m.height <= 0 {
		return body + "\n" + statusbar
	}
	used := lipgloss.Height(body)
	target := m.height - 1 // reserve last row for statusbar
	if used < target {
		body += strings.Repeat("\n", target-used)
	}
	return body + "\n" + statusbar
}

// resize recomputes pane dimensions from the latest WindowSizeMsg.
// Vertical layout: banner | summary-box | (list | detail) | status-bar.
// The status bar is always pinned to the bottom row; the list/detail
// panes expand to fill whatever's left after the fixed-height pieces.
//
// Both list/detail panes are wrapped in rounded borders, so we shrink
// the inner viewports by 2 rows (top/bottom border) and 2 cols
// (left/right border). When the user activates the filter, the list's
// inner area shrinks by one more row to make room for the inline input
// row at the top of the pane.
func (m *Model) resize() {
	headerHeight := ui.BannerHeight(m.version)
	summaryHeight := 0
	if m.output != nil {
		// Top + bottom border (2) + "Summary" label (1) + at least one
		// content row (1) + JoinVertical's between-block newline (1).
		summaryHeight = 5
	}
	bodyHeight := m.height - headerHeight - summaryHeight - 1 // 1 row for status bar
	if bodyHeight < 3 {
		bodyHeight = 3
	}
	// Round up so the list — the primary content pane — gets the larger
	// half on odd terminal widths. With strict floor division the list
	// would be 1 cell narrower than the detail pane on every odd width,
	// which read as the list's right border drifting left of center.
	listWidth := (m.width + 1) / 2
	detailWidth := m.width - listWidth
	if detailWidth < 4 {
		detailWidth = 4
	}

	listInnerWidth := listWidth - 2
	listInnerHeight := bodyHeight - 2
	if m.filtering || m.filterExpr != "" {
		// One row goes to the inline filter UI — either the live input
		// while typing, or the static "/<expr>   esc to clear" banner
		// once the filter is committed.
		listInnerHeight--
	}
	if listInnerHeight < 1 {
		listInnerHeight = 1
	}

	m.list.SetSize(listInnerWidth, listInnerHeight)
	m.detail.SetSize(detailWidth-2, bodyHeight-2)

	// Size the filter input to fit within the list pane (minus the
	// prompt). Prompt is "/ " = 2 cells.
	if w := listInnerWidth - 2; w > 0 {
		m.filterInput.Width = w
	}
}

// summaryTitle returns the bold heading rendered at the top of the
// summary box. The suffix tells the user which scope the displayed
// stats cover so they can correlate the numbers with the project
// switcher state without having to read the status bar:
//
//   - Single-project scans: just "Summary".
//   - Multi-project scans, no filter: "Summary: all projects".
//   - Multi-project scans, scoped to one project via Tab: "Summary:
//     <project name>".
func (m Model) summaryTitle() string {
	const base = "Summary"
	if m.output == nil {
		return styles.Bold().Render(base)
	}
	if m.projectFilter != "" {
		return styles.Bold().Render(base+": ") + styles.Bold().Foreground(ui.BrandColor).Render(m.projectFilter)
	}
	if len(m.output.Projects) > 1 {
		return styles.Bold().Render(base+": ") + styles.Muted().Render("all projects")
	}
	return styles.Bold().Render(base)
}

// projectLabel returns a short, human-friendly label for the active
// project shown in the status bar. When the user has tabbed into a
// specific project it shows that project's name; otherwise it falls back
// to the cwd basename. format.Output.ProjectName itself isn't great for
// chrome (the scanner uses the full path with separators replaced by
// hyphens) but in a project switcher it's exactly what the user is
// targeting, so we use it verbatim there.
func (m Model) projectLabel() string {
	if m.projectFilter != "" {
		// Add a multi-project hint so the user can see they're scoped
		// to one of N projects rather than viewing the whole scan.
		if m.output != nil && len(m.output.Projects) > 1 {
			return m.projectFilter + " (tab to switch)"
		}
		return m.projectFilter
	}
	if m.output != nil && len(m.output.Projects) > 1 {
		return fmt.Sprintf("all projects (%d, tab to switch)", len(m.output.Projects))
	}
	if m.cwd != "" {
		return filepath.Base(m.cwd)
	}
	return ""
}

// loadCacheCmd asynchronously loads any cached scan output for cwd plus
// the manifest entry's CreatedAt so the status bar can render cache age.
func loadCacheCmd(cfg *config.Config) tea.Cmd {
	return func() tea.Msg {
		cwd, err := os.Getwd()
		if err != nil {
			return cacheLoadedMsg{}
		}
		abs, err := filepath.Abs(cwd)
		if err != nil {
			abs = cwd
		}
		out, err := cfg.Cache.ForPath(abs)
		if err != nil {
			return cacheLoadedMsg{cwd: abs}
		}
		var createdAt time.Time
		if m, err := cfg.Cache.LoadManifest(); err == nil {
			if entry, ok := m.Entries[cache.Key(abs)]; ok {
				createdAt = entry.CreatedAt
			}
		}
		return cacheLoadedMsg{output: out, createdAt: createdAt, cwd: abs}
	}
}

