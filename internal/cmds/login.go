package cmds

import (
	"context"
	"fmt"

	"github.com/infracost/cli/internal/config"
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

	return resolveOrg(ctx, cfg, source)
}
