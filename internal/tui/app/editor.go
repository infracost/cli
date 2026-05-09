package app

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// editorOpenedMsg lets Update bump the editorOpens session counter
// after the editor returns. Carries an error so we can surface it in
// the status bar rather than crashing back to the shell when, e.g.,
// $EDITOR is missing or the file path doesn't exist.
type editorOpenedMsg struct{ err error }

// openEditorCmd suspends the bubbletea program, runs $EDITOR with the
// file (and line number, when supported), then resumes. Returns nil
// when there's nothing to open — the caller can ignore the cmd.
//
// Editor argument shape varies by editor: most terminal editors accept
// "+<line>" as the first argument. VS Code and Sublime use a different
// pattern. We special-case the common ones; everything else falls
// through to the +<line> convention.
//
// $EDITOR is parsed as space-separated tokens so users with wrappers
// like `EDITOR="code --wait"` get those flags forwarded.
func openEditorCmd(cwd, file string, line int) tea.Cmd {
	if file == "" {
		return func() tea.Msg {
			return editorOpenedMsg{err: fmt.Errorf("no source file recorded for this resource")}
		}
	}
	if !filepath.IsAbs(file) {
		file = filepath.Join(cwd, file)
	}

	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}
	parts := strings.Fields(editor)
	if len(parts) == 0 {
		return func() tea.Msg {
			return editorOpenedMsg{err: fmt.Errorf("$EDITOR is empty")}
		}
	}
	bin := parts[0]
	args := append([]string(nil), parts[1:]...)

	switch filepath.Base(bin) {
	case "code", "code-insiders", "cursor":
		target := file
		if line > 0 {
			target = fmt.Sprintf("%s:%d", file, line)
		}
		args = append(args, "--goto", target)
	case "subl", "sublime_text":
		target := file
		if line > 0 {
			target = fmt.Sprintf("%s:%d", file, line)
		}
		args = append(args, target)
	default:
		// vi / vim / nvim / nano / emacs / helix all accept "+<line>"
		// before the file path to seek to that line.
		if line > 0 {
			args = append(args, fmt.Sprintf("+%d", line))
		}
		args = append(args, file)
	}

	c := exec.Command(bin, args...)
	return tea.ExecProcess(c, func(err error) tea.Msg {
		return editorOpenedMsg{err: err}
	})
}
