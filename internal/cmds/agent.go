package cmds

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	icon     string                         // slug for the embedded brand icon (internal/ui/icons/<slug>.png)
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
	if actionErr != nil && !isAlreadyConfiguredErr(actionErr) {
		return fmt.Errorf("adding marketplace: %w", actionErr)
	}

	installArgs := []string{"plugin", "install"}
	if scope != "" {
		installArgs = append(installArgs, "--scope", scope)
	}
	installArgs = append(installArgs, plugin)

	if err := ui.RunWithSpinner("Installing Infracost plugin...", "Plugin installed", func() {
		actionErr = runAgentBinary(bin, installArgs...)
	}); err != nil {
		return err
	}
	if actionErr != nil && !isAlreadyConfiguredErr(actionErr) {
		return fmt.Errorf("installing plugin: %w", actionErr)
	}

	return nil
}

// isAlreadyConfiguredErr reports whether err describes a step that's
// already done (marketplace registered, plugin installed, etc.). Setup
// is meant to be idempotent — re-running it after a partial install,
// or installing skills the user already has, should silently no-op
// rather than abort the whole flow. Matches against substrings of the
// error message because each agent CLI phrases this differently
// ("already registered", "already installed", "already exists").
func isAlreadyConfiguredErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	if !strings.Contains(msg, "already") {
		return false
	}
	return strings.Contains(msg, "registered") ||
		strings.Contains(msg, "installed") ||
		strings.Contains(msg, "exists")
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

	uninstallArgs := []string{"plugin", "uninstall"}
	if scope != "" {
		uninstallArgs = append(uninstallArgs, "--scope", scope)
	}
	uninstallArgs = append(uninstallArgs, plugin)

	if err := ui.RunWithSpinner("Uninstalling Infracost plugin...", "Plugin uninstalled", func() {
		actionErr = runAgentBinary(bin, uninstallArgs...)
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

// agentPluginEntry mirrors a record in VS Code's
// ~/.vscode/agent-plugins/installed.json.
type agentPluginEntry struct {
	PluginURI   string `json:"pluginUri"`
	Marketplace string `json:"marketplace"`
}

// agentPluginRegistry is the on-disk shape of the same file. We
// guard against unknown versions to avoid silently corrupting a
// future schema change.
type agentPluginRegistry struct {
	Version   int                `json:"version"`
	Installed []agentPluginEntry `json:"installed"`
}

// installCopilotVSCodePlugin reproduces what VS Code's Command Palette
// "Chat: Install Plugin From Source" command does internally:
//   - git-clone the source repo into ~/.vscode/agent-plugins/github.com/<owner>/<repo>
//   - register it in ~/.vscode/agent-plugins/installed.json with a
//     {pluginUri, marketplace} entry.
//
// The directory layout was reverse-engineered from a working install;
// if `version` in installed.json ever moves past 1, we refuse to
// modify the file rather than silently corrupting whatever new schema
// VS Code has rolled out.
//
// Re-running this will wipe the existing clone and re-clone fresh, and
// update (rather than duplicate) the entry in the registry — so a
// `infracost agent setup` after a previous install pulls the latest
// agent-skills revision instead of leaving a stale checkout in place.
func installCopilotVSCodePlugin() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("locating home directory: %w", err)
	}
	rootDir := filepath.Join(home, ".vscode", "agent-plugins")
	cloneDir := filepath.Join(rootDir, "github.com", "infracost", "agent-skills")
	pluginDir := filepath.Join(cloneDir, "plugins", "infracost")
	registryFile := filepath.Join(rootDir, "installed.json")

	var actionErr error
	if err := ui.RunWithSpinner("Installing Infracost plugin...", "Plugin installed", func() {
		if err := os.MkdirAll(rootDir, 0o750); err != nil {
			actionErr = fmt.Errorf("creating %s: %w", rootDir, err)
			return
		}

		// Always start from a clean clone so re-running setup brings
		// the user up to the current revision rather than leaving a
		// stale checkout on disk.
		if _, err := os.Stat(cloneDir); err == nil {
			if err := os.RemoveAll(cloneDir); err != nil {
				actionErr = fmt.Errorf("removing existing clone at %s: %w", cloneDir, err)
				return
			}
		}

		cmd := exec.Command("git", "clone", "--depth=1", infracostSkillsRepo, cloneDir) //nolint:gosec // repo URL is a hardcoded constant; cloneDir is derived from $HOME
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			if msg := strings.TrimSpace(stderr.String()); msg != "" {
				actionErr = fmt.Errorf("git clone: %s", msg)
			} else {
				actionErr = fmt.Errorf("git clone: %w", err)
			}
			return
		}

		actionErr = updateAgentPluginRegistry(registryFile, pluginDir, infracostSkillsRepo)
	}); err != nil {
		return err
	}
	return actionErr
}

// updateAgentPluginRegistry parses VS Code's agent-plugins/installed.json,
// upserts the Infracost entry (or creates the file if missing), and
// writes it back. Existing entries that match `marketplace` get their
// `pluginUri` refreshed so a path change after a re-clone propagates.
func updateAgentPluginRegistry(file, pluginDir, marketplace string) error {
	reg := agentPluginRegistry{Version: 1}

	if data, err := os.ReadFile(file); err == nil { //nolint:gosec // file is the registry path under $HOME, not user-supplied
		if err := json.Unmarshal(data, &reg); err != nil {
			return fmt.Errorf("parsing %s: %w", file, err)
		}
		if reg.Version != 1 {
			return fmt.Errorf("VS Code agent-plugins registry is version %d (expected 1); refusing to modify — run the manual install instead", reg.Version)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("reading %s: %w", file, err)
	}

	pluginURI := "file://" + pluginDir
	found := false
	for i, e := range reg.Installed {
		if e.Marketplace == marketplace {
			reg.Installed[i].PluginURI = pluginURI
			found = true
			break
		}
	}
	if !found {
		reg.Installed = append(reg.Installed, agentPluginEntry{
			PluginURI:   pluginURI,
			Marketplace: marketplace,
		})
	}

	data, err := json.MarshalIndent(reg, "", "\t")
	if err != nil {
		return fmt.Errorf("encoding registry: %w", err)
	}
	if err := os.WriteFile(file, data, 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", file, err)
	}
	return nil
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
		icon:     "claude",
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
		manual: fmt.Sprintf(`To install Infracost skills in Claude Code:
  1. Install Claude Code: %s
  2. Run the following commands:
     %s
     %s`,
			ui.Code("https://docs.claude.com/en/docs/claude-code/setup"),
			ui.Code("claude plugin marketplace add infracost/agent-skills"),
			ui.Code("claude plugin install infracost@infracost")),
		enabled: true,
	},
	{
		name:     "GitHub Copilot (CLI)",
		icon:     "copilot",
		binaries: []string{"copilot"},
		// Copilot CLI's `plugin install` / `plugin uninstall` don't accept
		// --scope (it has no per-scope concept like Claude Code does);
		// passing "" tells pluginSetup/pluginTeardown to omit the flag.
		setup: func(bin, _ string) error {
			return pluginSetup(bin, infracostMarketplace, infracostPlugin, "")
		},
		teardown: func(bin, _ string) error {
			return pluginTeardown(bin, infracostMarketplaceName, infracostPlugin, "")
		},
		check: func(bin string) (bool, error) {
			return pluginCheck(bin, "infracost")
		},
		manual: fmt.Sprintf(`To install Infracost skills in GitHub Copilot CLI:
  1. Install GitHub Copilot CLI: %s
  2. Run the following commands:
     %s
     %s`,
			ui.Code("https://docs.github.com/en/copilot/concepts/agents/about-copilot-cli"),
			ui.Code("copilot plugin marketplace add infracost/agent-skills"),
			ui.Code("copilot plugin install infracost@infracost")),
		enabled: true,
	},
	{
		name: "GitHub Copilot (VS Code)",
		icon: "copilot",
		// No `binaries` — install is filesystem-driven (git clone +
		// JSON registry update) rather than a CLI shell-out, so we run
		// setup unconditionally regardless of whether `code` is on
		// PATH. If VS Code isn't actually installed, the files sit in
		// ~/.vscode/agent-plugins/ harmlessly until it is.
		setup: func(_, _ string) error {
			return installCopilotVSCodePlugin()
		},
		manual: fmt.Sprintf(`To install Infracost skills in GitHub Copilot for VS Code:
  1. Open the Command Palette (%s / %s)
  2. Run %s
  3. Enter the repository URL: %s
  4. Restart VS Code`,
			ui.Code("Cmd+Shift+P"),
			ui.Code("Ctrl+Shift+P"),
			ui.Code(`"Chat: Install Plugin From Source"`),
			ui.Code(infracostSkillsRepo)),
		remove: `To remove Infracost skills from GitHub Copilot for VS Code:
  1. Open the Command Palette (Cmd+Shift+P / Ctrl+Shift+P)
  2. Run "Chat: Uninstall Plugin"
  3. Select the Infracost plugin
  4. Restart VS Code`,
		enabled: true,
	},
	{
		name:     "OpenAI Codex",
		icon:     "codex",
		binaries: []string{"codex"},
		// `codex exec` runs a single prompt non-interactively and exits.
		// `$skill-installer` is Codex's built-in skill that clones a
		// repo and registers each skill it finds. Args go through Go's
		// exec directly (no shell), so the literal `$` in the prompt
		// passes through as-is.
		setup: func(bin, _ string) error {
			var actionErr error
			if err := ui.RunWithSpinner("Installing Infracost skill...", "Skill installed", func() {
				actionErr = runAgentBinary(bin, "exec", "$skill-installer infracost/agent-skills")
			}); err != nil {
				return err
			}
			if actionErr != nil && !isAlreadyConfiguredErr(actionErr) {
				return fmt.Errorf("installing skill: %w", actionErr)
			}
			return nil
		},
		// Single-quoted in the manual so users running this in bash /
		// zsh / fish all pass the literal `$skill-installer` to codex
		// rather than having their shell try to expand it as a variable.
		manual: fmt.Sprintf(`To install Infracost skills in OpenAI Codex:
  1. Install Codex CLI: %s
  2. Run the following command:
     %s`,
			ui.Code("https://developers.openai.com/codex/cli"),
			ui.Code("codex exec '$skill-installer infracost/agent-skills'")),
		remove:  `To remove Infracost skills from OpenAI Codex, remove the infracost skills from your Codex configuration.`,
		enabled: true,
	},
	{
		name: "Cursor",
		icon: "cursor",
		manual: fmt.Sprintf(`To install Infracost skills in Cursor:
  1. Open an AI chat within Cursor
  2. Send the following prompt:
     %s
     %s`,
			ui.Code("Add the rules from the following repo as global/user skills:"),
			ui.Code(infracostSkillsRepo+".git")),
		remove: `To remove Infracost skills from Cursor:
  1. Open Settings → Rules
  2. Find and delete the Infracost rule`,
		enabled: true,
	},
	{
		name:     "Gemini CLI",
		icon:     "gemini",
		binaries: []string{"gemini"},
		// Gemini CLI manages skills via a different verb namespace from
		// Claude/Copilot's plugin marketplace, so it gets a custom setup
		// rather than going through pluginSetup. Removal isn't documented
		// upstream, so we surface manual instructions for that path.
		setup: func(bin, _ string) error {
			var actionErr error
			if err := ui.RunWithSpinner("Installing Infracost skill...", "Skill installed", func() {
				// `--consent` skips Gemini's interactive confirmation
				// prompt, which would otherwise see EOF on the
				// inherited (closed) stdin and silently cancel the
				// install — exiting 0 with nothing actually installed.
				actionErr = runAgentBinary(bin, "skills", "install", "--consent", infracostSkillsRepo+".git")
			}); err != nil {
				return err
			}
			if actionErr != nil && !isAlreadyConfiguredErr(actionErr) {
				return fmt.Errorf("installing skill: %w", actionErr)
			}
			return nil
		},
		check: func(bin string) (bool, error) {
			var out bytes.Buffer
			cmd := exec.Command(bin, "skills", "list") //nolint:gosec // bin resolved from PATH
			cmd.Stdout = &out
			cmd.Stderr = &out
			if err := cmd.Run(); err != nil {
				return false, err
			}
			return strings.Contains(out.String(), "infracost"), nil
		},
		manual: fmt.Sprintf(`To install Infracost skills in Gemini CLI:
  1. Install Gemini CLI: %s
  2. Run the following command:
     %s`,
			ui.Code("https://geminicli.com/docs/"),
			ui.Code("gemini skills install "+infracostSkillsRepo+".git")),
		remove:  `To remove Infracost skills from Gemini CLI, remove the infracost skills from your Gemini configuration.`,
		enabled: true,
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
			agentName, err := RunAgentSetup(cfg, scope, false)
			if err != nil {
				return err
			}
			// Mirror the unified `infracost setup` flow: a successful
			// install closes with the gradient-bordered "Setup complete"
			// card and a tailored "what's next?" CTA. Skipped/aborted
			// runs produce an empty name, in which case there's nothing
			// to celebrate so we don't render the card.
			if agentName != "" {
				fmt.Println()
				fmt.Print(ui.GradientCard(setupCompleteContent(agentName, "", false)))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&scope, "scope", "user", "Installation scope: user (global), project, or local")
	return cmd
}

// RunAgentSetup is the core logic for `infracost agent setup`, callable from
// the unified `infracost setup` flow (DEV-230). When skippable is true, a
// "Skip" option is appended to the selection list. Returns the selected
// agent's display name (empty if the user skipped or aborted) so the
// unified flow can tailor its closing CTA.
func RunAgentSetup(cfg *config.Config, scope string, skippable bool) (string, error) {
	if _, ok := validAgentScopes[scope]; !ok {
		return "", fmt.Errorf("invalid scope %q: must be one of user, project, or local", scope)
	}

	selected, err := selectAgent("Which AI coding agent do you use?", skippable)
	if err != nil {
		return "", err
	}
	if selected == nil {
		return "", nil
	}

	if err := setupAgent(cfg, *selected, scope); err != nil {
		return "", err
	}
	return selected.name, nil
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

// agentIconSlug returns the icon slug for the agent matching name, or
// "" if no enabled agent has that display name. Used by the post-setup
// CTA to inline the brand mark next to the service name in static
// (non-bubbletea) output.
func agentIconSlug(name string) string {
	for _, a := range supportedAgents {
		if a.name == name {
			return a.icon
		}
	}
	return ""
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
	// Try the scriptable install first when one is available. For
	// agents whose setup shells out to a CLI (Claude, Copilot CLI,
	// Gemini, Codex), a missing binary means we can't run setup and
	// fall through to manual instructions. For agents whose setup is
	// filesystem-driven (Copilot VS Code), `binaries` is empty and we
	// always run setup.
	if a.setup != nil {
		var bin string
		runSetup := true
		if len(a.binaries) > 0 {
			var err error
			if bin, err = resolveAgentBinary(cfg, a); err != nil {
				runSetup = false
			}
		}
		if runSetup {
			if err := a.setup(bin, scope); err != nil {
				return err
			}
			ui.Successf("Infracost skills enabled for %s. Restart your agent to activate.", a.name)
			return nil
		}
	}

	// Manual instructions — used both for tools that have no scriptable
	// install AND as the fallback for scriptable tools whose CLI isn't
	// installed yet. The card pauses on "press enter to continue" then
	// collapses to a single checklist line so subsequent setup steps
	// stay tidy.
	if a.manual != "" {
		card := ui.InstructionsCard("Setup instructions for "+a.name, a.manual)
		fmt.Println()
		fmt.Print(card)
		// Each \n in the card == one rendered line. The cursor sits on the
		// next blank line after the card. The +3 covers the leading blank
		// line, the prompt's leading "\n", and the user's echoed Enter.
		rewind := strings.Count(card, "\n") + 3

		if ui.PressEnter("\nPress enter to continue...") {
			ui.EraseLastLines(rewind)
			ui.Successf("Followed setup instructions for %s", a.name)
		}
		return nil
	}

	// Legacy URL fallback: warn and open a marketplace/install page
	// in the user's browser. Kept for entries that haven't been moved
	// to the manual-instructions style yet.
	if a.url != "" {
		ui.Warnf("Could not find a CLI for %s on your PATH.", a.name)
		if a.hint != "" {
			fmt.Println(a.hint)
		}
		fmt.Printf("  %s\n", ui.Code(a.url))
		if ui.PressEnter("\nPress Enter to open in your browser...") {
			if err := browser.Open(a.url); err != nil {
				ui.Failf("Failed to open browser. Visit the URL manually:\n   %s", ui.Code(a.url))
			} else {
				ui.Successf("Opened %s in your browser.", ui.Code(a.url))
			}
		}
		return nil
	}

	if a.setup == nil {
		return fmt.Errorf("no setup method available for %s", a.name)
	}
	return fmt.Errorf("%s CLI not found on PATH", a.name)
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
