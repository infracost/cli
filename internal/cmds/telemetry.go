package cmds

import (
	"context"
	"strings"
	"time"

	analytics "github.com/infracost/analytics-go"

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

	runSeconds := time.Since(startTime).Seconds()

	ctx := context.Background()
	tracker := cfg.Events.Tracker(api.Client(ctx, cfg.Auth.TokenFromCache(ctx), cfg.OrgID))
	_ = analytics.TrackInfracostError(ctx, tracker, analytics.InfracostErrorProperties{
		Error:        &msg,
		RunSeconds:   &runSeconds,
		OutputFormat: &outputFormat,
	})
}

// loginOutcome is what a login reports, beyond the installId every event
// already carries.
type loginOutcome struct {
	// orgCount is how many organizations the user belongs to after the login,
	// so a zero-org signup is countable and the fix for it is measurable. It
	// is only sent when userFetched is true, since an unread user looks the
	// same as a user in no org.
	orgCount int
	// userFetched records that the org count was read, not assumed.
	userFetched bool
	// createdOrg records that this login made one.
	createdOrg bool
}

// pushLoginOutcome reports the end of an `infracost auth login`. It exists so
// the "not logged in" errors a CLI emits before this point can be joined to
// the login that follows on the same install — they carry no user identity,
// and until now there was no login event to join them to.
func pushLoginOutcome(ctx context.Context, cfg *config.Config, outcome *loginOutcome, startTime time.Time, loginErr error) {
	result := analytics.InfracostAuthLoginOutcome_Success
	if loginErr != nil {
		result = analytics.InfracostAuthLoginOutcome_Error
	}

	// Detached from the caller's context, which is already cancelled when a
	// login is abandoned — the abandoned ones are the interesting half.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), loginEventTimeout)
	defer cancel()

	props := analytics.InfracostAuthLoginProperties{
		Outcome:         result,
		CreatedOrg:      outcome.createdOrg,
		DurationSeconds: time.Since(startTime).Seconds(),
	}
	if outcome.userFetched {
		props.OrgCount = &outcome.orgCount
	}

	tracker := cfg.Events.Tracker(api.Client(ctx, cfg.Auth.TokenFromCache(ctx), cfg.OrgID))
	_ = analytics.TrackInfracostAuthLogin(ctx, tracker, props)
}

// loginEventTimeout bounds the outcome event on the way out of a command that
// may have been waiting on a browser. A single POST to the events endpoint
// costs ~0.3s on a fast link, most of it TLS, so a tighter bound would drop
// the event on any slow or proxied connection — which is the login this exists
// to count.
const loginEventTimeout = 3 * time.Second
