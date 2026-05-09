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
	"github.com/infracost/cli/internal/tui/discovery"
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

// FocusedPane identifies which of ViewMain's split panes (list or
// detail) currently receives nav keys. Only meaningful in ViewMain;
// the picker and loading views own the keyboard themselves.
type FocusedPane int

const (
	FocusList FocusedPane = iota
	FocusDetail
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

	// session accumulates telemetry across the lifetime of the TUI; see
	// session.go for the field semantics.
	session sessionStats

	list   views.List
	detail views.Detail

	// focusedPane is which split-view pane currently receives nav
	// keys. Default is FocusList — the user lands on the resource
	// list and can drive the cursor there. Pressing enter shifts to
	// FocusDetail so they can scroll through long policy text;
	// pressing esc returns focus to the list. Border styling on
	// each pane mirrors this so the active pane is visually
	// obvious.
	focusedPane FocusedPane

	// Picker state. The picker is shown when bare-`infracost` is
	// invoked outside an IaC directory; the discovery walker fills
	// the project list in the background as it explores $HOME.
	picker            views.Picker
	pickerProjects    []discovery.Project
	discoveryCh       <-chan discovery.Project
	discoveryCancel   context.CancelFunc
	discoveryFinished bool

	// enteredViaPicker remembers whether the user got into ViewMain
	// by selecting from the empty-state picker (vs. landing directly
	// because cwd was an IaC dir). Drives the esc-key fallback that
	// returns to the picker — pressing esc inside an IaC dir's main
	// view has no useful "back", so we don't offer the affordance
	// there.
	enteredViaPicker bool

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
		picker:      views.NewPicker(),
		filterInput: ti,
		spinner:     sp,
		stage:       "Checking cache...",
		session:     sessionStats{startTime: time.Now()},
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
			// Force a clear so the loading-view's status bar doesn't
			// linger via tea's diff-render optimization (which can keep
			// the previous frame's last row visible above the new
			// statusbar when the body line counts shift).
			return m, tea.ClearScreen
		}
		// Cache miss. If the cwd looks like an IaC project, kick off a
		// scan so the user lands on real data. Otherwise drop into the
		// empty-state picker and start the $HOME discovery walker so
		// they can pick somewhere else without leaving the TUI.
		if discovery.IsIaCProject(msg.cwd) {
			m.stage = "Scanning " + m.projectLabel() + "... (this can take a moment)"
			return m, scanCmd(m.ctx, m.cfg, m.source, msg.cwd, false)
		}
		// Mutate via enterPicker BEFORE evaluating the return tuple —
		// Go doesn't specify the evaluation order between bare `m` and
		// the `m.enterPicker()` call when both appear in the same
		// return statement, so writing `return m, m.enterPicker()`
		// can ship the pre-mutation snapshot of m to bubbletea (with
		// stage/view still in their cacheLoaded state).
		//
		// tea.ClearScreen flushes the alt-screen buffer so the
		// previous frame's loading-view content can't show through
		// via diff-render skipping.
		cmd := m.enterPicker()
		return m, tea.Batch(tea.ClearScreen, cmd)

	case discoveryFoundMsg:
		m.pickerProjects = append(m.pickerProjects, msg.project)
		m.session.projectsDiscovered++
		// Chain the next read so the picker keeps populating live.
		return m, readDiscoveryCmd(m.discoveryCh)

	case discoveryDoneMsg:
		m.discoveryFinished = true
		return m, nil

	case scanStartedMsg:
		m.stage = "Scanning..."
		return m, nil

	case scanDoneMsg:
		prevView := m.view
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
		// Cache hits don't run the scanner, so they aren't a "scan
		// the user observed" — only fresh scans bump the counter.
		if !msg.fromCache {
			m.session.scansRun++
		}
		if prevView != ViewMain {
			// Coming from ViewLoading (or an error retry); clear the
			// alt screen so the prior layout's status bar can't leak
			// through tea's diff render.
			return m, tea.ClearScreen
		}
		return m, nil

	case scanErrMsg:
		m.scanErr = msg.err
		m.stage = ""
		m.view = ViewError
		return m, nil

	case authResolvedMsg:
		// `infracost auth login` finished. If it itself errored, surface
		// the error and stay on ViewError so the user can try again.
		if msg.err != nil {
			m.scanErr = fmt.Errorf("auth login failed: %w", msg.err)
			m.view = ViewError
			return m, nil
		}
		// Re-resolve credentials and retry the scan that triggered the
		// recovery. orgresolve here is safe because login just walked
		// the user through org selection, so the cache is populated.
		fresh, err := refreshCredentials(m.ctx, m.cfg)
		if err != nil {
			m.scanErr = err
			m.view = ViewError
			return m, nil
		}
		if ts, ok := fresh.(oauth2.TokenSource); ok {
			m.source = ts
		}
		m.scanErr = nil
		m.view = ViewLoading
		m.stage = "Retrying scan..."
		return m, scanCmd(m.ctx, m.cfg, m.source, m.cwd, true)

	case editorOpenedMsg:
		// Surface editor failures (missing $EDITOR, bad path, etc.) in
		// the status bar without leaving the main view — these aren't
		// scan-fatal so blocking the rest of the UI behind ViewError
		// would be heavy-handed. The stage clears on the next user
		// action that updates it (refresh, sort, filter).
		if msg.err != nil {
			m.stage = "Editor: " + msg.err.Error()
		}
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
			case "q":
				m.session.terminatedReason = "q"
				m.quitting = true
				return m, tea.Quit
			case "ctrl+c":
				m.session.terminatedReason = "ctrlC"
				m.quitting = true
				return m, tea.Quit
			case "?", "esc":
				m.helpOpen = false
				// ClearScreen on close so the next frame draws on a
				// fresh alt-screen — without it, repeated ?/esc
				// cycles drifted the status bar up the screen as
				// tea's diff renderer kept stale rows from prior
				// frames around.
				return m, tea.ClearScreen
			}
			// Swallow everything else while help is open so background
			// keystrokes don't move the underlying list cursor.
			return m, nil
		}

		// The picker is its own modal-feeling view; route nav + select
		// through it before the global key handlers so j/k/G don't
		// also bleed through to the (non-rendered) list pane.
		if m.view == ViewPicker {
			switch msg.String() {
			case "q":
				m.session.terminatedReason = "q"
				m.quitting = true
				return m, tea.Quit
			case "ctrl+c":
				m.session.terminatedReason = "ctrlC"
				m.quitting = true
				return m, tea.Quit
			case "esc":
				// Clear a committed filter even after the user has
				// navigated away from the input box. Mirrors the
				// main-view esc precedence so the hint shown next
				// to the filter banner ("esc to clear") behaves the
				// same in both contexts.
				if m.filterExpr != "" {
					m.filterInput.SetValue("")
					m.filterExpr = ""
					return m, nil
				}
				// No "back" from the picker — esc is a no-op when no
				// filter is active.
				return m, nil
			case "?":
				m.helpOpen = true
				return m, tea.ClearScreen
			case "/":
				// `/` works in the picker too: filter projects by
				// substring on name or path. Reuses the same filter
				// input model as the main view, so esc/clear behave
				// identically.
				m.filtering = true
				m.filterInput.Focus()
				m.session.filterUsed = true
				return m, textinput.Blink
			case "enter":
				// Re-resolve the cursor's project against the *filtered*
				// list the user is actually looking at — otherwise the
				// cursor index would point into the unfiltered slice.
				projects := views.FilterPickerProjects(m.pickerProjects, m.filterExpr)
				if len(projects) == 0 {
					return m, nil
				}
				idx := m.picker.Cursor()
				if idx < 0 || idx >= len(projects) {
					return m, nil
				}
				selected := projects[idx]
				m.cwd = selected.Path
				m.stage = "Scanning " + selected.Name + "... (this can take a moment)"
				m.view = ViewLoading
				m.cancelDiscovery()
				// Reset the filter on view transition. Picker filter
				// and resource-list filter share the same input model,
				// so leaving the picker's expression in place would
				// pre-populate the resource list with the picker's
				// search term.
				m.clearFilter()
				// ClearScreen on the picker → loading transition for
				// the same reason as cacheLoaded → picker: avoids the
				// previous picker frame's status-bar row leaking
				// through tea's line-skip optimization.
				return m, tea.Batch(
					tea.ClearScreen,
					scanCmd(m.ctx, m.cfg, m.source, selected.Path, false),
				)
			}
			// Pass through to the picker for nav keys. Use the
			// filtered count so j/k can't move past the visible
			// rows — cursor indexes the filtered slice in enter.
			projects := views.FilterPickerProjects(m.pickerProjects, m.filterExpr)
			m.picker.Update(msg, len(projects))
			return m, nil
		}

		switch msg.String() {
		case "q":
			m.session.terminatedReason = "q"
			m.quitting = true
			m.cancelDiscovery()
			return m, tea.Quit
		case "ctrl+c":
			m.session.terminatedReason = "ctrlC"
			m.quitting = true
			m.cancelDiscovery()
			return m, tea.Quit
		case "?":
			m.helpOpen = true
			// Same ClearScreen rationale as the close path: make sure
			// the help overlay gets a clean canvas instead of inheriting
			// any cells diff-skipped from the underlying view.
			return m, tea.ClearScreen
		case "/":
			if m.view == ViewMain && m.output != nil {
				m.filtering = true
				m.filterInput.Focus()
				m.resize() // shrink list by one row for the inline input
				m.session.filterUsed = true
				return m, textinput.Blink
			}
		case "esc":
			// Esc precedence (most-specific first):
			//   1. Detail pane focused → return focus to the list.
			//      The filter banner belongs to the list pane; while
			//      the user is reading the detail, the list filter
			//      isn't what they're operating on, so esc should
			//      first back out of the focus they entered.
			//   2. Clear an active filter — only when the list (or
			//      filter input) has focus. Honors the inline banner
			//      hint shown next to the filter expression.
			//   3. Came in via the project picker → go back there.
			//   4. Otherwise: no-op.
			if m.view == ViewMain && m.focusedPane == FocusDetail {
				m.focusedPane = FocusList
				m.detail.SetFocused(false)
				return m, nil
			}
			if m.filterExpr != "" {
				m.filterInput.SetValue("")
				m.filterExpr = ""
				m.applyRowsToList()
				m.resize()
				return m, nil
			}
			if m.enteredViaPicker && (m.view == ViewMain || m.view == ViewError) {
				m.backToPicker()
				return m, tea.ClearScreen
			}
		case "s":
			if m.view == ViewMain && m.output != nil {
				m.sortMode = m.sortMode.next()
				m.applyRowsToList()
				m.session.sortChanged = true
				return m, nil
			}
		case "tab":
			// Only cycle projects when the resource list pane has
			// focus — otherwise the keystroke would silently change
			// the underlying scope while the user is reading the
			// detail pane, which is jarring. The detail pane has
			// no other use for tab today, so we just no-op there.
			if m.view == ViewMain && m.focusedPane == FocusList && m.output != nil && len(m.output.Projects) > 1 {
				m.cycleProject()
				m.applyRowsToList()
				m.session.projectSwitches++
				return m, nil
			}
		case "e":
			if m.view == ViewMain {
				if sel := m.list.Selected(); sel != nil && sel.Resource != nil {
					return m, openEditorCmd(
						m.cwd,
						sel.Resource.Metadata.Filename,
						sel.Resource.Metadata.StartLine,
					)
				}
			}
		case "enter":
			// Enter in ViewMain shifts focus to the detail pane so
			// the user can scroll through long policy descriptions
			// without bumping the list cursor. Esc returns focus to
			// the list. No-op when the detail is empty (no
			// resources / no selection).
			if m.view == ViewMain && m.focusedPane == FocusList {
				if m.list.Selected() != nil {
					m.focusedPane = FocusDetail
					m.detail.SetFocused(true)
				}
				return m, nil
			}
		case "a":
			// Auth recovery: run `infracost auth login` in a child
			// process via tea.Exec, then retry the failed scan. Only
			// offered from the error view to keep the key out of the
			// main hot-keys hash.
			if m.view == ViewError && isAuthError(m.scanErr) {
				return m, runAuthLoginCmd()
			}
		case "r":
			if m.view == ViewMain || m.view == ViewError {
				m.stage = "Scanning..."
				m.session.refreshes++
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
			// Route nav keys to the focused pane: detail viewport
			// scrolls, or list cursor moves. Selection sync only
			// happens when the list moves — scrolling the detail
			// shouldn't disturb which resource is being shown.
			if m.focusedPane == FocusDetail {
				cmd := m.detail.Update(msg)
				return m, cmd
			}
			prev := m.list.Selected()
			cmd := m.list.Update(msg)
			cur := m.list.Selected()
			if cur != prev {
				m.session.detailOpened++
			}
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
	// Build the status bar from view-specific state. Picker doesn't
	// belong to any single project, so it gets none of the
	// project-scoped labels (project name, cache age, sort) — those
	// would still be carrying over from whichever project the user
	// just escaped out of, which is confusing. ViewLoading suppresses
	// stage too because the body already renders the spinner +
	// "Scanning <project>..." prominently.
	bar := views.StatusBarData{
		Project:     m.projectLabel(),
		CacheAge:    m.cacheAge,
		FromCache:   m.fromCache,
		Stage:       m.stage,
		Filter:      m.filterExpr,
		SortLabel:   m.sortMode.label(),
		SpinnerView: m.spinner.View(),
	}
	if m.view == ViewLoading {
		bar.Stage = "" // spinner+stage rendered in body for ViewLoading
	}
	if m.view == ViewPicker {
		bar.Project = ""
		bar.FromCache = false
		bar.CacheAge = 0
		bar.SortLabel = ""
		bar.Shortcuts = "/ filter · ↑↓ nav · enter select · ? help · q quit"
	}
	statusbar := views.RenderStatusBar(bar, m.width)

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
			// Focus styling: thick white border on the active pane,
			// muted rounded on the inactive pane. When the detail
			// side has focus we also fade the list pane's border
			// AND content via the ANSI faint attribute — the list
			// is just background context at that point and shouldn't
			// compete with what the user is reading on the right.
			listBorder := styles.PaneBorderFocused()
			detailBorder := styles.PaneBorder()
			detailContent := m.detail.View()
			if m.focusedPane == FocusDetail {
				detailBorder = styles.PaneBorderFocused()
				listBorder = styles.PaneBorderDimmed()
				listContent = styles.Dimmed().Render(listContent)
			}
			body = lipgloss.JoinHorizontal(lipgloss.Top,
				listBorder.Width(listInnerWidth).Render(listContent),
				detailBorder.Width(detailInnerWidth).Render(detailContent),
			)
		}
		sections = append(sections, body)
		return m.frame(sections, statusbar)
	case ViewError:
		return m.frame([]string{header, views.RenderError(m.scanErr, isAuthError(m.scanErr), m.width)}, statusbar)
	case ViewPicker:
		// Filter picker projects on the fly so the user can / through
		// the discovery list without us having to maintain a parallel
		// "filtered list" field on the model — recomputing per render
		// is cheap given the bounded project count.
		projects := views.FilterPickerProjects(m.pickerProjects, m.filterExpr)
		var filterRow string
		switch {
		case m.filtering:
			filterRow = m.filterInput.View()
		case m.filterExpr != "":
			filterRow = views.RenderFilterStatus(m.filterExpr, m.width-4)
		}
		body := styles.PaneBorderAccent().Width(m.width - 2).Render(
			m.picker.View(projects, !m.discoveryFinished, filterRow, len(m.pickerProjects)),
		)
		return m.frame([]string{header, body}, statusbar)
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
	bodyHeight := max(m.height-headerHeight-summaryHeight-1, 3) // 1 row for status bar
	// Round up so the list — the primary content pane — gets the larger
	// half on odd terminal widths. With strict floor division the list
	// would be 1 cell narrower than the detail pane on every odd width,
	// which read as the list's right border drifting left of center.
	listWidth := (m.width + 1) / 2
	detailWidth := max(m.width-listWidth, 4)

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

	// Picker spans the full body width. Subtract 2 for the surrounding
	// pane border, plus another 2 from the height for the top/bottom
	// border rows so the bordered box fits between the banner and the
	// status bar instead of pushing content under the banner.
	pickerInnerW := m.width - 2
	pickerInnerH := m.height - headerHeight - 1 - 2
	if pickerInnerW < 16 {
		pickerInnerW = 16
	}
	if pickerInnerH < 5 {
		pickerInnerH = 5
	}
	m.picker.SetSize(pickerInnerW, pickerInnerH)
}

// enterPicker transitions the model into the empty-state picker view
// and starts the background $HOME walker. The walker is canceled
// when the user picks a project (or quits) so its goroutine exits
// promptly rather than racing with shutdown.
//
// Picker dimensions are owned by resize() — we just construct a fresh
// picker here and call resize so the same arithmetic that places the
// list/detail panes in ViewMain places the picker box in ViewPicker.
func (m *Model) enterPicker() tea.Cmd {
	m.view = ViewPicker
	m.stage = ""
	m.session.pickerOpened++
	m.enteredViaPicker = true
	m.picker = views.NewPicker()
	m.resize()

	ch, cancel := startDiscovery(m.ctx)
	m.discoveryCh = ch
	m.discoveryCancel = cancel
	m.discoveryFinished = false
	m.pickerProjects = nil
	return readDiscoveryCmd(ch)
}

// backToPicker returns the user from a project's main/error view to
// the empty-state picker, preserving the previously discovered
// project list so they don't have to wait for the walker again. We
// only invoke this when enteredViaPicker is true — pressing esc
// inside a normal cwd-is-an-IaC-dir session has no useful "back",
// so the affordance is absent there.
func (m *Model) backToPicker() {
	m.view = ViewPicker
	m.scanErr = nil
	m.stage = ""
	m.session.pickerOpened++
	// Reset the filter so the picker doesn't open with the
	// resource-list filter expression pre-populated. The two views
	// share the same input model, so we own the lifecycle here.
	m.clearFilter()
	// Keep pickerProjects + discoveryFinished as-is so the user sees
	// the same list they picked from, with the same "X projects
	// found" footer (no spurious "still searching" hint).
	m.resize()
}

// clearFilter resets the filter input + committed expression state.
// Used on view transitions where carrying the previous view's filter
// across would surface the wrong default ("filter projects by foo"
// reading like "filter resources by foo", or vice versa).
func (m *Model) clearFilter() {
	m.filtering = false
	m.filterInput.SetValue("")
	m.filterInput.Blur()
	m.filterExpr = ""
}

// cancelDiscovery stops the walker goroutine. Safe to call when the
// walker isn't running — both fields are nil-checked.
func (m *Model) cancelDiscovery() {
	if m.discoveryCancel != nil {
		m.discoveryCancel()
		m.discoveryCancel = nil
	}
	m.discoveryCh = nil
	m.discoveryFinished = true
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

