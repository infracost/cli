package cmds

import (
	"context"
	"fmt"

	"github.com/infracost/cli/internal/api"
	"github.com/infracost/cli/internal/config"
	"github.com/infracost/cli/internal/ui"
	"github.com/infracost/cli/pkg/logging"
	"github.com/spf13/cobra"
)

func Login(cfg *config.Config) *cobra.Command {
	return &cobra.Command{
		Use:   "login",
		Short: "Login to Infracost",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return RunLogin(cmd.Context(), cfg)
		},
	}
}

// RunLogin is the core logic for `infracost login`, callable from the unified
// `infracost setup` flow (DEV-230).
func RunLogin(ctx context.Context, cfg *config.Config) error {
	if len(cfg.Auth.AuthenticationToken) > 0 {
		fmt.Println("Authentication token provided directly, skipping login.")
		return nil
	}

	source, err := cfg.Auth.Token(ctx)
	if err != nil {
		return err
	}
	fmt.Println("Retrieved valid access token.")

	// Always fetch fresh user/org data on login, bypassing the staleness check.
	if err := ui.RunWithSpinnerErr(ctx, "Fetching user data...", "User data loaded", func(ctx context.Context) error {
		client := cfg.Dashboard.Client(api.Client(ctx, source, ""))
		if _, err := fetchAndCacheUser(ctx, cfg, client); err != nil {
			return fmt.Errorf("failed to refresh user cache after login: %w", err)
		}
		return nil
	}); err != nil {
		logging.WithError(err).Msg("failed to refresh user cache after login")
	}

	return resolveOrg(ctx, cfg, source)
}
