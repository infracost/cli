package cmds

import (
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/charmbracelet/huh"
	"github.com/infracost/cli/internal/config"
	"github.com/infracost/cli/internal/ui"
	"github.com/infracost/cli/version"
	"github.com/spf13/cobra"
)

// requireUserLogin returns an error if the user is authenticated via a service
// account token rather than an interactive login. Setup commands need a real
// user identity (for org resolution, etc.) and cannot operate with tokens.
func requireUserLogin(cfg *config.Config) error {
	if len(cfg.Auth.AuthenticationToken) > 0 {
		return fmt.Errorf("setup requires interactive login, it cannot be used with INFRACOST_CLI_AUTHENTICATION_TOKEN — run 'infracost auth login' first, then retry")
	}
	return nil
}

func Setup(cfg *config.Config) *cobra.Command {
	return &cobra.Command{
		Use:   "setup",
		Short: "Set up Infracost integrations",
		Long:  "Walk through setting up Infracost for your coding agents, IDE, and CI pipeline",
		Example: `  # Run the interactive setup walkthrough
  $ infracost setup`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := requireUserLogin(cfg); err != nil {
				return err
			}

			fmt.Println()
			fmt.Print(ui.Banner(version.Version))
			fmt.Println()

			// On Ctrl+C anywhere in the setup flow, print a branded goodbye
			// and exit cleanly. huh's per-prompt abort handling continues to
			// work for navigating to "Skip" inside individual menus; this
			// catches the case where the user wants to bail out entirely.
			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
			go func() {
				<-sigCh
				fmt.Println()
				fmt.Println()
				fmt.Println("  " + ui.Gradient("Setup cancelled. Goodbye!"))
				os.Exit(0)
			}()
			defer signal.Stop(sigCh)

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
			agentName, err := RunAgentSetup(cfg, "user", true)
			if err != nil {
				return err
			}

			// Step 3: IDE setup
			ideName, err := RunIDESetup(true)
			if err != nil {
				return err
			}

			// Step 4: CI setup
			if err := runSetupStep("Set up CI integration?", func() error {
				return RunCISetup(ctx, cfg, false, false)
			}); err != nil {
				return err
			}

			fmt.Println()
			fmt.Println(ui.Bold(ui.Gradient("Setup complete.")))

			fmt.Println()
			ui.Heading("What's next?")
			printNextSteps(agentName, ideName)
			return nil
		},
	}
}

// printNextSteps renders the post-setup CTA. The recommendation is tailored
// to whichever integration the user just installed — the agent path (chat
// in your coding agent) and the IDE path (inline cost estimates) deliver a
// faster aha moment than the bare CLI for users who installed those tools.
// Falls back to "cd + infracost scan" when nothing was installed.
func printNextSteps(agentName, ideName string) {
	switch {
	case agentName != "":
		slug := agentIconSlug(agentName)
		ui.Stepf("cd into a Terraform, CloudFormation, or CDK project")
		ui.Stepf("Open %s%s and ask it %s", iconPrefix(slug), ui.Bold(ui.Service(slug, agentName)), ui.Code(`"How much does this project cost?"`))
	case ideName != "":
		slug := ideIconSlug(ideName)
		ui.Stepf("Open a Terraform, CloudFormation, or CDK project in %s%s", iconPrefix(slug), ui.Bold(ui.Service(slug, ideName)))
		ui.Stepf("Open the Infracost extension from the toolbar and make sure you're logged in — you'll see cost estimates inline with your code")
	default:
		ui.Stepf("cd into a Terraform, CloudFormation, or CDK project")
		ui.Stepf("Run %s to see your costs and any policy violations", ui.Code("infracost scan"))
	}
}

// iconPrefix returns "<icon> " when the active terminal can render the
// named brand icon, "" otherwise. Designed to be concatenated directly
// in front of the service name so the CTA reads naturally on terminals
// without image support ("Open Claude Code") and gets a subtle brand
// mark on terminals that do ("Open <logo> Claude Code").
func iconPrefix(slug string) string {
	if icon := ui.Icon(slug); icon != "" {
		return icon + " "
	}
	return ""
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
		WithTheme(ui.BrandTheme()).
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
