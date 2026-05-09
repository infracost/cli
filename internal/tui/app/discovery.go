package app

import (
	"context"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/infracost/cli/internal/tui/discovery"
)

// discoveryFoundMsg is posted once per project the walker discovers.
// The model appends it to its picker list and re-issues another
// discoveryReadCmd to read the next entry.
type discoveryFoundMsg struct {
	project discovery.Project
}

// discoveryDoneMsg signals that the walker has finished its sweep.
// The picker stops showing the "still searching" hint and the model
// won't issue any more discoveryReadCmds.
type discoveryDoneMsg struct{}

// startDiscoveryCmd starts the walker in a goroutine, returns the
// channel pair the model uses to drain results, and posts the first
// read command. The walker context is derived from m.ctx so quitting
// the TUI cancels the walk and avoids a goroutine leak.
//
// Returns the read channel so the model can keep issuing reads
// against it until discoveryDoneMsg arrives. The done channel is
// closed by the walker goroutine when the walk finishes; we use a
// `range` over the buffered found channel to know when to emit
// discoveryDoneMsg.
func startDiscovery(parent context.Context) (<-chan discovery.Project, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	ch := make(chan discovery.Project, 32)
	go func() {
		defer close(ch)
		home, err := os.UserHomeDir()
		if err != nil {
			return
		}
		_ = discovery.Walk(ctx, home, func(p discovery.Project) {
			select {
			case ch <- p:
			case <-ctx.Done():
			}
		})
	}()
	return ch, cancel
}

// readDiscoveryCmd reads the next project off the walker's channel.
// On project: returns discoveryFoundMsg with the next read chained
// in by the model's Update. On channel close: returns
// discoveryDoneMsg and the model stops chaining.
func readDiscoveryCmd(ch <-chan discovery.Project) tea.Cmd {
	return func() tea.Msg {
		p, ok := <-ch
		if !ok {
			return discoveryDoneMsg{}
		}
		return discoveryFoundMsg{project: p}
	}
}
