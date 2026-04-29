package cmds

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/infracost/cli/internal/config"
	"github.com/infracost/cli/internal/ui"
	"github.com/infracost/cli/pkg/auth/browser"
	"github.com/spf13/cobra"
)

const (
	infracostMarketplace     = "infracost/claude-skills"
	infracostMarketplaceName = "infracost"
	infracostPlugin          = "infracost@infracost"
	infracostSkillsRepo      = "https://github.com/infracost/agent-skills"
)

type agent struct {
	name     string
	binaries []string                       // CLI binaries to look for on PATH
	setup    func(bin, scope string) error  // CLI-based setup
	teardown func(bin, scope string) error  // CLI-based teardown
	check    func(bin string) (bool, error) // returns true if infracost skills are installed
	manual   string                         // manual setup instructions
	remove   string                         // manual remove instructions
	url      string                         // fallback URL to open
	hint     string                         // message shown before opening URL
	enabled  bool
}

func pluginSetup(bin, marketplace, plugin, scope string) error {
	var actionErr error

	if err := ui.RunWithSpinner("Adding Infracost skills marketplace...", "Marketplace added", func() {
		actionErr = runAgentBinary(bin, "plugin", "marketplace", "add", marketplace)
	}); err != nil {
		return err
	}
	if actionErr != nil {
		return fmt.Errorf("adding marketplace: %w", actionErr)
	}

	if err := ui.RunWithSpinner("Installing Infracost plugin...", "Plugin installed", func() {
		actionErr = runAgentBinary(bin, "plugin", "install", "--scope", scope, plugin)
	}); err != nil {
		return err
	}
	if actionErr != nil {
		return fmt.Errorf("installing plugin: %w", actionErr)
	}

	return nil
}

func pluginCheck(bin, name string) (bool, error) {
	var out bytes.Buffer
	cmd := exec.Command(bin, "plugin", "list") //nolint:gosec // bin is user-configured or looked up on PATH
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return false, err
	}
	return strings.Contains(out.String(), name), nil
}

func pluginTeardown(bin, marketplaceName, plugin, scope string) error {
	var errs []error
	var actionErr error

	if err := ui.RunWithSpinner("Uninstalling Infracost plugin...", "Plugin uninstalled", func() {
		actionErr = runAgentBinary(bin, "plugin", "uninstall", "--scope", scope, plugin)
	}); err != nil {
		return err
	}
	if actionErr != nil {
		errs = append(errs, fmt.Errorf("uninstalling plugin: %w", actionErr))
	}

	if err := ui.RunWithSpinner("Removing Infracost skills marketplace...", "Marketplace removed", func() {
		actionErr = runAgentBinary(bin, "plugin", "marketplace", "remove", marketplaceName)
	}); err != nil {
		return err
	}
	if actionErr != nil {
		errs = append(errs, fmt.Errorf("removing marketplace: %w", actionErr))
	}

	return errors.Join(errs...)
}

func runAgentBinary(bin string, args ...string) error {
	var stderr bytes.Buffer
	cmd := exec.Command(bin, args...) //nolint:gosec // bin is user-configured or looked up on PATH
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return fmt.Errorf("%s", msg)
		}
		return err
	}
	return nil
}

var supportedAgents = []agent{
	{
		name:     "Claude Code",
		binaries: []string{"claude"},
		setup: func(bin, scope string) error {
			return pluginSetup(bin, infracostMarketplace, infracostPlugin, scope)
		},
		teardown: func(bin, scope string) error {
			return pluginTeardown(bin, infracostMarketplaceName, infracostPlugin, scope)
		},
		check: func(bin string) (bool, error) {
			return pluginCheck(bin, "infracost")
		},
		enabled: true,
	},
	{
		name:     "GitHub Copilot (CLI)",
		binaries: []string{"copilot"},
		setup: func(bin, scope string) error {
			return pluginSetup(bin, infracostMarketplace, infracostPlugin, scope)
		},
		teardown: func(bin, scope string) error {
			return pluginTeardown(bin, infracostMarketplaceName, infracostPlugin, scope)
		},
		check: func(bin string) (bool, error) {
			return pluginCheck(bin, "infracost")
		},
		enabled: true,
	},
	{
		name:     "GitHub Copilot (VS Code)",
		binaries: []string{"code", "codium"},
		manual: `To install Infracost skills in GitHub Copilot for VS Code:
  1. Open the Command Palette (Cmd+Shift+P / Ctrl+Shift+P)
  2. Run "Chat: Install Plugin From Source"
  3. Enter the repository URL: ` + infracostSkillsRepo + `
  4. Restart VS Code`,
		remove: `To remove Infracost skills from GitHub Copilot for VS Code:
  1. Open the Command Palette (Cmd+Shift+P / Ctrl+Shift+P)
  2. Run "Chat: Uninstall Plugin"
  3. Select the Infracost plugin
  4. Restart VS Code`,
		enabled: true,
	},
	{
		name: "OpenAI Codex",
		manual: `To install Infracost skills in OpenAI Codex, run the following prompt:
  $skill-installer infracost/agent-skills`,
		remove: `To remove Infracost skills from OpenAI Codex, remove the infracost skills from your Codex configuration.`,
		enabled: true,
	},
	{
		name: "Cursor",
		manual: `To install Infracost skills in Cursor:
  1. Open Settings → Rules
  2. Click "+New"
  3. Select "Add from GitHub/GitLab"
  4. Enter the repository URL: ` + infracostSkillsRepo + `.git`,
		remove: `To remove Infracost skills from Cursor:
  1. Open Settings → Rules
  2. Find and delete the Infracost rule`,
		enabled: true,
	},
	{
		name:    "Gemini CLI",
		enabled: false,
	},
}

var validAgentScopes = map[string]struct{}{
	"user":    {},
	"project": {},
	"local":   {},
}

func Agent(cfg *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agent",
		Short: "Manage AI coding agent integrations",
	}
	cmd.AddCommand(agentSetup(cfg))
	cmd.AddCommand(agentRemove(cfg))
	return cmd
}

func agentSetup(cfg *config.Config) *cobra.Command {
	var scope string

	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Install Infracost skills for your AI coding agent",
		RunE: func(_ *cobra.Command, _ []string) error {
			return RunAgentSetup(cfg, scope, false)
		},
	}
	cmd.Flags().StringVar(&scope, "scope", "user", "Installation scope: user (global), project, or local")
	return cmd
}

// RunAgentSetup is the core logic for `infracost agent setup`, callable from
// the unified `infracost setup` flow (DEV-230). When skippable is true, a
// "Skip" option is appended to the selection list.
func RunAgentSetup(cfg *config.Config, scope string, skippable bool) error {
	if _, ok := validAgentScopes[scope]; !ok {
		return fmt.Errorf("invalid scope %q: must be one of user, project, or local", scope)
	}

	selected, err := selectAgent("Which AI coding agent do you use?", skippable)
	if err != nil {
		return err
	}
	if selected == nil {
		return nil
	}

	return setupAgent(cfg, *selected, scope)
}

func agentRemove(cfg *config.Config) *cobra.Command {
	var scope string

	cmd := &cobra.Command{
		Use:   "remove",
		Short: "Remove Infracost skills from your AI coding agent",
		RunE: func(_ *cobra.Command, _ []string) error {
			if _, ok := validAgentScopes[scope]; !ok {
				return fmt.Errorf("invalid scope %q: must be one of user, project, or local", scope)
			}

			selected, err := selectAgent("Which AI coding agent do you want to remove Infracost skills from?", false)
			if err != nil {
				return err
			}
			if selected == nil {
				return nil
			}

			return removeAgent(cfg, *selected, scope)
		},
	}
	cmd.Flags().StringVar(&scope, "scope", "user", "Installation scope: user (global), project, or local")
	return cmd
}

func selectAgent(title string, skippable bool) (*agent, error) {
	if !ui.IsInteractive() {
		return nil, nil
	}

	var enabledAgents []agent
	for _, a := range supportedAgents {
		if a.enabled {
			enabledAgents = append(enabledAgents, a)
		}
	}

	options := make([]huh.Option[int], len(enabledAgents))
	for i, a := range enabledAgents {
		options[i] = huh.NewOption(a.name, i)
	}
	if skippable {
		options = append(options, huh.NewOption("Skip", -1))
	}

	var selected int
	err := huh.NewSelect[int]().
		Title(title).
		Options(options...).
		Value(&selected).
		WithTheme(ui.BrandTheme()).
		Run()
	if err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return nil, nil
		}
		return nil, fmt.Errorf("selecting agent: %w", err)
	}

	if selected < 0 {
		return nil, nil
	}

	result := enabledAgents[selected]
	return &result, nil
}

func agentBinary(cfg *config.Config, a agent) string {
	// For Claude Code, check config for a custom path.
	if a.name == "Claude Code" && cfg.ClaudePath != "" {
		return cfg.ClaudePath
	}
	return ""
}

func resolveAgentBinary(cfg *config.Config, a agent) (string, error) {
	// Check for configured path override.
	if configured := agentBinary(cfg, a); configured != "" {
		if _, err := exec.LookPath(configured); err != nil {
			return "", fmt.Errorf("%s CLI not found at configured path %q", a.name, configured)
		}
		return configured, nil
	}

	// Search PATH for known binaries.
	for _, bin := range a.binaries {
		if path, err := exec.LookPath(bin); err == nil {
			return path, nil
		}
	}

	return "", fmt.Errorf("%s CLI not found on PATH", a.name)
}

func setupAgent(cfg *config.Config, a agent, scope string) error {
	if a.manual != "" {
		fmt.Println(a.manual)
		return nil
	}

	if a.setup == nil {
		return fmt.Errorf("no setup method available for %s", a.name)
	}

	bin, err := resolveAgentBinary(cfg, a)
	if err != nil {
		if a.url != "" {
			ui.Warnf("Could not find a CLI for %s on your PATH.", a.name)
			if a.hint != "" {
				fmt.Println(a.hint)
			}
			fmt.Printf("  %s\n", a.url)
			if ui.PressEnter("\nPress Enter to open in your browser...") {
				if err := browser.Open(a.url); err != nil {
					ui.Failf("Failed to open browser. Visit the URL manually:\n   %s", ui.Accent(a.url))
				} else {
					ui.Successf("Opened %s in your browser.", a.url)
				}
			}
			return nil
		}
		return err
	}

	if err := a.setup(bin, scope); err != nil {
		return err
	}

	ui.Successf("Infracost skills enabled for %s. Restart your agent to activate.", a.name)
	return nil
}

func removeAgent(cfg *config.Config, a agent, scope string) error {
	if a.remove != "" {
		fmt.Println(a.remove)
		return nil
	}

	if a.teardown == nil {
		return fmt.Errorf("no remove method available for %s", a.name)
	}

	bin, err := resolveAgentBinary(cfg, a)
	if err != nil {
		return err
	}

	if err := a.teardown(bin, scope); err != nil {
		return err
	}

	ui.Successf("Infracost skills removed from %s.", a.name)
	return nil
}
