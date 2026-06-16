package ui

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/infracost/cli/pkg/logging"
)

// activeProgram tracks the currently running spinner so nested
// RunWithSpinnerErr calls can route their done lines through the
// parent's Println rather than launching a competing bubbletea
// program on the same stderr.
var (
	activeProgramMu sync.Mutex
	activeProgram   *tea.Program
)

// programWriter routes Write calls to a bubbletea Program's Println so log
// lines are painted above the spinner without being clobbered by its
// frame redraws.
type programWriter struct {
	p *tea.Program
}

func (w programWriter) Write(b []byte) (int, error) {
	// Println adds its own newline; trim ours so we don't double up.
	w.p.Println(strings.TrimRight(string(b), "\n"))
	return len(b), nil
}

// spinnerStyle uses the brand-primary color, picking the dark- or
// light-terminal variant via lipgloss adaptive colors.
var spinnerStyle = lipgloss.NewStyle().Foreground(BrandColor)

// isTTY reports whether stderr is a terminal. Spinners are suppressed when
// output is piped or running in a non-interactive environment.
func isTTY() bool {
	info, err := os.Stderr.Stat()
	if err != nil {
		return false
	}
	return (info.Mode() & os.ModeCharDevice) != 0
}

type errMsg struct{ err error }

type spinnerModel struct {
	spinner   spinner.Model
	title     string
	doneTitle string
	action    func(context.Context) error
	ctx       context.Context
	err       error
	done      bool
}

func (m spinnerModel) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, m.runAction())
}

func (m spinnerModel) runAction() tea.Cmd {
	return func() tea.Msg {
		return errMsg{err: m.action(m.ctx)}
	}
}

func (m spinnerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case errMsg:
		m.err = msg.err
		m.done = true
		return m, tea.Quit
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			m.err = fmt.Errorf("interrupted")
			return m, tea.Quit
		}
	}

	var cmd tea.Cmd
	m.spinner, cmd = m.spinner.Update(msg)
	return m, cmd
}

func (m spinnerModel) View() string {
	if m.done {
		if m.err != nil || m.doneTitle == "" {
			return ""
		}
		return fmt.Sprintf("  %s  %s\n", Positive("✔"), m.doneTitle)
	}
	return "  " + m.spinner.View() + " " + m.title + "\n"
}

// RunWithSpinner displays a spinner on stderr with the given title while
// action runs. On success the spinner line is replaced with a checkmark and
// doneTitle. If stderr is not a TTY the action runs silently.
func RunWithSpinner(title, doneTitle string, action func()) error {
	return RunWithSpinnerErr(context.Background(), title, doneTitle, func(_ context.Context) error {
		action()
		return nil
	})
}

// RunWithSpinnerErr is like RunWithSpinner but the action receives a context
// and may return an error.
func RunWithSpinnerErr(ctx context.Context, title, doneTitle string, action func(context.Context) error) error {
	if !isTTY() {
		return action(ctx)
	}

	// If a spinner is already active, don't start a competing one on the
	// same stderr — run the action directly and emit the done line via
	// the parent's Println so it's painted above its spinner.
	activeProgramMu.Lock()
	parent := activeProgram
	activeProgramMu.Unlock()
	if parent != nil {
		if err := action(ctx); err != nil {
			return err
		}
		if doneTitle != "" {
			parent.Println(fmt.Sprintf("  %s  %s", Positive("✔"), doneTitle))
		}
		return nil
	}

	s := spinner.New(spinner.WithSpinner(spinner.Dot), spinner.WithStyle(spinnerStyle))

	m := spinnerModel{
		spinner:   s,
		title:     title,
		doneTitle: doneTitle,
		action:    action,
		ctx:       ctx,
	}

	p := tea.NewProgram(m,
		tea.WithOutput(os.Stderr),
		tea.WithInput(&bytes.Buffer{}),    // don't read from stdin
		tea.WithoutSignalHandler(),        // avoid signal conflicts in tests / subprocesses
	)

	// Route logs through Println so they appear above the spinner
	// instead of getting clobbered by its redraws on stderr.
	restore := logging.SetOutput(programWriter{p: p})
	defer restore()

	activeProgramMu.Lock()
	activeProgram = p
	activeProgramMu.Unlock()
	defer func() {
		activeProgramMu.Lock()
		activeProgram = nil
		activeProgramMu.Unlock()
	}()

	finalModel, err := p.Run()
	if err != nil {
		return fmt.Errorf("spinner error: %w", err)
	}

	if fm, ok := finalModel.(spinnerModel); ok && fm.err != nil {
		return fm.err
	}

	return nil
}
