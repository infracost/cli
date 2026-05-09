package app

import (
	"context"
	"time"

	"github.com/infracost/cli/internal/api"
	"github.com/infracost/cli/internal/config"
)

// sessionStats accumulates telemetry across a single TUI session and
// is flushed as an "infracost-tui-session" event when the program
// exits. Counts answer "what did the user actually do" — useful for
// product decisions about which features earn their place in the
// chrome (filter, sort, refresh) and which don't.
type sessionStats struct {
	startTime          time.Time
	scansRun           int
	refreshes          int
	filterUsed         bool
	sortChanged        bool
	detailOpened       int
	pickerOpened       int
	projectSwitches    int
	projectsDiscovered int
	terminatedReason   string // "q", "ctrlC", "error", or "" (still running)
}

// SessionTerminatedReason returns the terminatedReason field — used
// by tui.Run to detect "the model never recorded a clean exit" so it
// can stamp an "error" reason before the event flushes.
func (m Model) SessionTerminatedReason() string { return m.session.terminatedReason }

// MarkErrorTermination sets terminatedReason to "error". Called from
// tui.Run when bubbletea returned an error and the model itself didn't
// have a chance to record why it quit.
func (m *Model) MarkErrorTermination() { m.session.terminatedReason = "error" }

// PushSessionEvent flushes the session-summary telemetry event.
// Idempotent — if startTime is the zero value (the model never went
// through ViewMain) we skip the push to avoid recording empty
// sessions caused by setup-flow bailouts.
//
// Called from tui.Run after the bubbletea program returns, so it
// runs after the alt screen has been torn down and stdout/stderr are
// usable again. The event API itself runs synchronously over a
// short context so we don't block shutdown for more than a couple of
// seconds when the network is slow.
func (m Model) PushSessionEvent(cfg *config.Config) {
	if m.session.startTime.IsZero() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	source := cfg.Auth.TokenFromCache(ctx)
	client := cfg.Events.Client(api.Client(ctx, source, cfg.OrgID))

	client.Push(ctx, "infracost-tui-session",
		"durationSeconds", time.Since(m.session.startTime).Seconds(),
		"scansRun", m.session.scansRun,
		"refreshes", m.session.refreshes,
		"filterUsed", m.session.filterUsed,
		"sortChanged", m.session.sortChanged,
		"detailOpened", m.session.detailOpened,
		"pickerOpened", m.session.pickerOpened,
		"projectSwitches", m.session.projectSwitches,
		"projectsDiscovered", m.session.projectsDiscovered,
		"terminatedReason", m.session.terminatedReason,
	)
}
