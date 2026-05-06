package cmds

import (
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
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
			if agentName == "" {
				renderAgentSkipNotice()
			}

			// Step 3: IDE setup
			ideName, err := RunIDESetup(true)
			if err != nil {
				return err
			}
			if ideName == "" {
				renderIdeSkipNotice()
			}

			// Step 4: CI setup. runSetupStep returns nil whether the user
			// confirmed or declined; track the user's intent through the
			// closure so we know whether to render a skip notice.
			ciSkipped := true
			if err := runSetupStep("Set up CI integration?", func() error {
				ciSkipped = false
				return RunCISetup(ctx, cfg, false, false)
			}); err != nil {
				return err
			}
			if ciSkipped {
				renderCiSkipNotice()
			}

			fmt.Println()
			fmt.Print(ui.GradientCard(setupCompleteContent(agentName, ideName)))
			return nil
		},
	}
}

// Skip notices stay on screen once rendered — earlier attempts to
// surgically remove them when the next step completed proved unreliable
// across terminals (cursor-position queries time out, bubbletea's exit
// state varies). Leaving them visible is the safer default; the user
// retains a record of which steps they skipped and how to run each one
// later.

func renderAgentSkipNotice() {
	renderSkipNotice("AI coding agents",
		"To install AI coding agent integration later, run "+ui.Code("infracost agent setup")+".")
}

func renderIdeSkipNotice() {
	renderSkipNotice("IDE",
		"To install IDE integration later, run "+ui.Code("infracost ide setup")+".")
}

func renderCiSkipNotice() {
	renderSkipNotice("CI",
		"To set up CI integration later, cd into a Terraform, CloudFormation, or CDK project and run "+ui.Code("infracost ci setup")+".")
}

func renderSkipNotice(name, content string) {
	fmt.Println()
	fmt.Print(ui.InstructionsCard("Set up "+name+" later", content))
}

// setupCompleteContent assembles the celebration card body: the bold
// gradient "Setup complete." line, a "What's next?" subhead, and the
// tailored CTA steps. Returned as a single string so the caller can drop
// it into ui.GradientCard.
func setupCompleteContent(agentName, ideName string) string {
	var b strings.Builder
	b.WriteString(ui.Bold(ui.Gradient("Setup complete.")))
	b.WriteString("\n\n")
	b.WriteString(ui.Bold(ui.Brand("What's next?")))
	b.WriteByte('\n')
	b.WriteString(nextStepsContent(agentName, ideName))
	return b.String()
}

// nextStepsContent renders the post-setup CTA as a multi-line string.
// The recommendation is tailored to whichever integration the user just
// installed — the agent path (chat in your coding agent) and the IDE
// path (inline cost estimates) deliver a faster aha moment than the
// bare CLI for users who installed those tools. Falls back to "cd +
// infracost scan" when nothing was installed.
func nextStepsContent(agentName, ideName string) string {
	// Inside the gradient card we collapse the cyan info/code highlight
	// into the heading's brand purple so the box reads as one palette
	// (gradient border + brand accents) rather than three competing
	// hues. Per-service brand colours on the product names stay since
	// they're each option's identity, not a generic highlight.
	arrow := "  " + ui.Brand("→") + "  "
	var b strings.Builder
	step := func(format string, args ...any) {
		b.WriteString(arrow)
		fmt.Fprintf(&b, format, args...)
		b.WriteByte('\n')
	}

	switch {
	case agentName != "":
		slug := agentIconSlug(agentName)
		step("cd into a Terraform, CloudFormation, or CDK project")
		step("Open %s%s and ask it %s", iconPrefix(slug), ui.Bold(ui.Service(slug, agentName)), ui.Brand(`"How much does this project cost?"`))
	case ideName != "":
		slug := ideIconSlug(ideName)
		step("Open a Terraform, CloudFormation, or CDK project in %s%s", iconPrefix(slug), ui.Bold(ui.Service(slug, ideName)))
		step("Open the Infracost extension from the toolbar and make sure you're logged in — you'll see cost estimates inline with your code")
	default:
		step("cd into a Terraform, CloudFormation, or CDK project")
		step("Run %s to see your costs and any policy violations", ui.Brand("infracost scan"))
	}
	return b.String()
}

// iconPrefix returns "<icon> " when the active terminal can render the
// named brand icon, "" otherwise. Designed to be concatenated directly
// in front of the service name so the CTA reads naturally on terminals
// without image support ("Open Claude Code") and gets a subtle brand
// mark on terminals that do ("Open <logo> Claude Code"). The image
// escape's display width is recognised by ui.PrintableWidth so the
// line measures correctly inside a wrapped/bordered card.
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
