package cmds

import (
	"context"
	"strings"
	"time"

	"github.com/infracost/cli/internal/api"
	"github.com/infracost/cli/internal/config"
)

// pushRunError reports a failed command run as an infracost-error event.
// The message is truncated; ToValidUTF8 drops a rune the cut may have split.
func pushRunError(cfg *config.Config, outputFormat string, startTime time.Time, runErr error) {
	if runErr == nil {
		return
	}

	msg := runErr.Error()
	if len(msg) > 200 {
		msg = strings.ToValidUTF8(msg[:200], "")
	}

	ctx := context.Background()
	eventsClient := cfg.Events.Client(api.Client(ctx, cfg.Auth.TokenFromCache(ctx), cfg.OrgID))
	eventsClient.Push(ctx, "infracost-error",
		"error", msg,
		"runSeconds", time.Since(startTime).Seconds(),
		"outputFormat", outputFormat,
	)
}

// Where an organization was created from. Bounded labels: the event is counted
// by trigger, never read as free text.
const (
	orgCreateTriggerCommand = "org-create"
	orgCreateTriggerLogin   = "login"
	orgCreateTriggerCISetup = "ci-setup"
)

// pushOrgCreated reports an organization created from the CLI. Orgs are
// otherwise only made in the dashboard, so without this the fix for a CLI
// signup with no org is invisible in the funnel it is meant to move.
func pushOrgCreated(ctx context.Context, cfg *config.Config, trigger string) {
	eventsClient := cfg.Events.Client(api.Client(ctx, cfg.Auth.TokenFromCache(ctx), cfg.OrgID))
	eventsClient.Push(ctx, "cloud-org-create",
		"source", "cli",
		"trigger", trigger,
	)
}

// loginOutcome is what a login reports, beyond the installId every event
// already carries.
type loginOutcome struct {
	// orgCount is how many organizations the user belongs to after the login,
	// so a zero-org signup is countable and the fix for it is measurable.
	orgCount int
	// createdOrg records that this login made one.
	createdOrg bool
}

// pushLoginOutcome reports the end of an `infracost auth login`. It exists so
// the "not logged in" errors a CLI emits before this point can be joined to
// the login that follows on the same install — they carry no user identity,
// and until now there was no login event to join them to.
func pushLoginOutcome(ctx context.Context, cfg *config.Config, outcome *loginOutcome, startTime time.Time, loginErr error) {
	result := "success"
	if loginErr != nil {
		result = "error"
	}

	// Detached from the caller's context, which is already cancelled when a
	// login is abandoned — the abandoned ones are the interesting half.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), loginEventTimeout)
	defer cancel()

	eventsClient := cfg.Events.Client(api.Client(ctx, cfg.Auth.TokenFromCache(ctx), cfg.OrgID))
	eventsClient.Push(ctx, "infracost-auth-login",
		"outcome", result,
		"orgCount", outcome.orgCount,
		"createdOrg", outcome.createdOrg,
		"durationSeconds", time.Since(startTime).Seconds(),
	)
}

// loginEventTimeout bounds the outcome event on the way out of a command that
// may have been waiting on a browser. A single POST to the events endpoint
// costs ~0.3s on a fast link, most of it TLS, so a tighter bound would drop
// the event on any slow or proxied connection — which is the login this exists
// to count.
const loginEventTimeout = 3 * time.Second
