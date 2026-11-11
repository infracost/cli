package cmds

import (
	"context"
	"fmt"
	"time"

	"github.com/infracost/cli/internal/api"
	"github.com/infracost/cli/internal/config"
	"github.com/infracost/cli/internal/ui"
	"github.com/infracost/cli/pkg/auth"
	"github.com/infracost/cli/pkg/logging"
	"github.com/spf13/cobra"
)

func Login(cfg *config.Config) *cobra.Command {
	return &cobra.Command{
		Use:   "login",
		Short: "Log in to Infracost",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return RunLogin(cmd.Context(), cfg)
		},
	}
}

// RunLogin is the core logic for `infracost auth login`, callable from the unified
// `infracost setup` flow (DEV-230).
func RunLogin(ctx context.Context, cfg *config.Config) (err error) {
	if len(cfg.Auth.AuthenticationToken) > 0 {
		fmt.Println("Authentication token provided directly, skipping login.")
		return nil
	}

	// The outcome event carries installId, which the errors from a CLI that
	// could not log in also carry. Without a login event there is nothing on
	// this side to join them to, so a local "not logged in" has no recovery
	// rate at all.
	startTime := time.Now()
	outcome := loginOutcome{}
	defer func() { pushLoginOutcome(ctx, cfg, &outcome, startTime, err) }()

	source, err := cfg.Auth.Token(ctx)
	if err != nil {
		return err
	}
	fmt.Println("Retrieved valid access token.")

	// Always fetch fresh user/org data on login, bypassing the staleness check.
	var uc *auth.UserCache
	if cacheErr := ui.RunWithSpinnerErr(ctx, "Fetching user data...", "User data loaded", func(ctx context.Context) error {
		client := cfg.Dashboard.Client(api.Client(ctx, source, ""))
		fetched, err := fetchAndCacheUser(ctx, cfg, client)
		if err != nil {
			return fmt.Errorf("failed to refresh user cache after login: %w", err)
		}
		uc = fetched
		return nil
	}); cacheErr != nil {
		logging.WithError(cacheErr).Msg("failed to refresh user cache after login")
	}

	if uc != nil {
		outcome.orgCount = len(uc.Organizations)
		outcome.userFetched = true

		// A user who belongs to no organization is the single largest blocker
		// between signing up and a first scan: every later call fails inside
		// the dashboard API instead, which says nothing about how to fix it.
		if len(uc.Organizations) == 0 {
			_, created, orgErr := ensurePersonalOrg(ctx, cfg, source, uc.Name, uc.Email, orgCreateTriggerLogin)
			if orgErr != nil {
				return orgErr
			}
			outcome.createdOrg = created
			if created {
				outcome.orgCount = 1
			}
		}
	}

	return resolveOrg(ctx, cfg, source)
}
