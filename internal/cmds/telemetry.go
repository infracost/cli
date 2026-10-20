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
