package ui

import (
	"bytes"
	"context"
	"fmt"
	"os"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

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
	finalModel, err := p.Run()
	if err != nil {
		return fmt.Errorf("spinner error: %w", err)
	}

	if fm, ok := finalModel.(spinnerModel); ok && fm.err != nil {
		return fm.err
	}

	return nil
}
