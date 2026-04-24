package cmds

import (
	"errors"
	"fmt"

	"github.com/charmbracelet/huh"
	"github.com/infracost/cli/internal/config"
	"github.com/infracost/cli/internal/ui"
	"github.com/spf13/cobra"
)

// requireUserLogin returns an error if the user is authenticated via a service
// account token rather than an interactive login. Setup commands need a real
// user identity (for org resolution, etc.) and cannot operate with tokens.
func requireUserLogin(cfg *config.Config) error {
	if len(cfg.Auth.AuthenticationToken) > 0 {
		return fmt.Errorf("setup requires interactive login, it cannot be used with INFRACOST_CLI_AUTHENTICATION_TOKEN — run 'infracost login' first, then retry")
	}
	return nil
}

func Setup(cfg *config.Config) *cobra.Command {
	return &cobra.Command{
		Use:   "setup",
		Short: "Set up Infracost integrations",
		Long:  "Walk through setting up Infracost for your coding agents, IDE, and CI pipeline.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := requireUserLogin(cfg); err != nil {
				return err
			}

			// Step 1: Login
			ctx := cmd.Context()
			if ts := cfg.Auth.TokenFromCache(ctx); ts != nil {
				ui.Success("Already logged in")
			} else {
				if err := RunLogin(ctx, cfg); err != nil {
					return err
				}
			}

			// Step 2: Agent setup
			if err := RunAgentSetup(cfg, "user", true); err != nil {
				return err
			}

			// Step 3: IDE setup
			if err := RunIDESetup(true); err != nil {
				return err
			}

			// Step 4: CI setup
			if err := runSetupStep("Set up CI integration?", func() error {
				return RunCISetup(ctx, cfg, false, false)
			}); err != nil {
				return err
			}

			fmt.Println()
			ui.Heading("Setup complete.")
			return nil
		},
	}
}

// runSetupStep prompts the user with a yes/no question. If they accept, it
// runs the provided function. If they decline or abort, it skips silently.
func runSetupStep(title string, fn func() error) error {
	if !ui.IsInteractive() {
		return nil
	}

	fmt.Println()

	var confirm bool
	err := huh.NewConfirm().
		Title(title).
		Affirmative("Yes").
		Negative("Skip").
		Value(&confirm).
		Run()
	if err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return nil
		}
		return err
	}

	if !confirm {
		return nil
	}

	return fn()
}
