package cmds

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

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
	// version reports the installed Infracost skill version for a
	// configured agent (e.g. "0.1.1"), or "" when the agent is present but
	// the version can't be determined. A nil version func means the agent
	// can't be version-monitored and is skipped by the staleness check.
	version   func(bin string) (string, error)
	manual    string // manual setup instructions
	postSetup string // extra activation instructions shown after a successful scripted setup
	remove    string // manual remove instructions
	url       string // fallback URL to open
	hint      string // message shown before opening URL
	enabled   bool
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

// agentProbeTimeout bounds each version-detection shell-out so a slow or
// hung agent CLI can't wedge the background staleness check.
const agentProbeTimeout = 10 * time.Second

// skillVersionRe matches a semver (optionally v-prefixed) in agent CLI
// output, e.g. "1.2.3" or "v1.2.3".
var skillVersionRe = regexp.MustCompile(`v?(\d+\.\d+\.\d+)`)

// pluginListVersion runs `<bin> <args...>` (an agent's plugin/skill list
// command) and returns the first semver on a line mentioning "infracost".
// Agent CLIs don't share a stable machine-readable format, so this parses
// defensively: a present-but-unparseable version returns "" (treated as
// "unknown", not an error) so the staleness check simply skips it rather
// than warning on a false signal.
func pluginListVersion(bin string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), agentProbeTimeout)
	defer cancel()

	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, bin, args...) //nolint:gosec // bin is resolved from PATH
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return "", err
	}
	for _, line := range strings.Split(out.String(), "\n") {
		if !strings.Contains(strings.ToLower(line), "infracost") {
			continue
		}
		if m := skillVersionRe.FindStringSubmatch(line); m != nil {
			return m[1], nil
		}
	}
	return "", nil
}

// readPluginManifestVersion reads the Infracost plugin's version from a
// checkout/clone rooted at repoRoot (plugins/infracost/.claude-plugin/plugin.json).
// Returns "" when the manifest is absent or has no version.
func readPluginManifestVersion(repoRoot string) string {
	data, err := os.ReadFile(filepath.Join(repoRoot, "plugins", "infracost", ".claude-plugin", "plugin.json")) //nolint:gosec // repoRoot is a clone dir we control
	if err != nil {
		return ""
	}
	var manifest struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return ""
	}
	return strings.TrimSpace(manifest.Version)
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
// copilotVSCodeCloneDir returns the on-disk location of the agent-skills
// checkout that installCopilotVSCodePlugin manages, or "" if the home
// directory can't be resolved.
func copilotVSCodeCloneDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".vscode", "agent-plugins", "github.com", "infracost", "agent-skills")
}

// copilotVSCodeInstalledVersion reads the plugin version from the VS Code
// clone. Returns "" when the plugin isn't installed. This reflects the
// clone we made (VS Code loads the plugin from that path in place, it
// doesn't re-fetch), so it's an accurate installed-version signal.
func copilotVSCodeInstalledVersion() (string, error) {
	dir := copilotVSCodeCloneDir()
	if dir == "" {
		return "", nil
	}
	if _, err := os.Stat(dir); err != nil {
		return "", nil //nolint:nilerr // absent clone == not installed, not an error
	}
	return readPluginManifestVersion(dir), nil
}

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

// duoSkills are the CLI-bound Infracost skills installed for GitLab Duo.
// fix-findings is intentionally excluded — it's backed by Infracost
// Agents (MCP-only) and has no CLI binding, so it can't run on Duo's
// shell-command path.
var duoSkills = []string{"scan", "price-lookup", "iac-generation"}

// duoSkillVersionFile is a marker written under <duoConfigDir>/skills/ at
// install time recording the skill version we copied in. It's how the
// staleness check learns Duo's installed version (Duo has no CLI that
// reports it). The leading dot keeps it out of Duo's skill discovery,
// which only scans skills/<name>/ subdirectories.
const duoSkillVersionFile = ".infracost-skill-version"

// duoConfigDir returns the base directory GitLab Duo reads user-level
// ("global") customization from, following the precedence the Duo CLI
// documents: GLAB_CONFIG_DIR wins, then XDG_CONFIG_HOME/gitlab/duo, then
// the per-OS default (~/.gitlab/duo on Linux/macOS, %APPDATA%\GitLab\duo
// on Windows).
func duoConfigDir() (string, error) {
	if dir := os.Getenv("GLAB_CONFIG_DIR"); dir != "" {
		return dir, nil
	}
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "gitlab", "duo"), nil
	}
	if runtime.GOOS == "windows" {
		if appData := os.Getenv("APPDATA"); appData != "" {
			return filepath.Join(appData, "GitLab", "duo"), nil
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locating home directory: %w", err)
	}
	return filepath.Join(home, ".gitlab", "duo"), nil
}

// installGitLabDuoSkills places the Infracost skills where the GitLab Duo
// CLI discovers user-level skills: <duoConfigDir>/skills/<name>/SKILL.md.
// It shallow-clones agent-skills to a temp dir (Duo reads the copied
// SKILL.md files directly, so there's no need to leave a checkout behind),
// then writes each skill via writeDuoSkills.
//
// Only the user-level *skills* are installed — we deliberately do NOT
// touch <duoConfigDir>/AGENTS.md or chat-rules.md, which are Duo's own
// global-customization files a user may already maintain; overwriting them
// would clobber the user's config. Each skill is self-contained instead
// (see writeDuoSkills).
func installGitLabDuoSkills() error {
	baseDir, err := duoConfigDir()
	if err != nil {
		return err
	}

	var actionErr error
	if err := ui.RunWithSpinner("Installing Infracost skills for GitLab Duo...", "Skills installed", func() {
		tmpDir, err := os.MkdirTemp("", "infracost-agent-skills-")
		if err != nil {
			actionErr = fmt.Errorf("creating temp dir: %w", err)
			return
		}
		defer func() { _ = os.RemoveAll(tmpDir) }()

		// Clone the default branch (main) rather than pinning a release
		// tag, deliberately matching how Claude Code's plugin marketplace
		// consumes this same repo (`plugin install infracost@infracost`
		// tracks the default branch, not the infracost-plugin-v* tags —
		// those drive semver dependency resolution + update-gating only).
		// Releases are cut from main, and the skills are dual-binding so a
		// single main revision serves both Claude (MCP) and Duo (CLI); this
		// keeps every Infracost skill channel on one consistent revision.
		cmd := exec.Command("git", "clone", "--depth=1", infracostSkillsRepo, tmpDir) //nolint:gosec // repo URL is a hardcoded constant; tmpDir is from os.MkdirTemp
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

		actionErr = writeDuoSkills(tmpDir, baseDir)
	}); err != nil {
		return err
	}
	return actionErr
}

// writeDuoSkills copies each CLI-bound skill from a checkout of
// agent-skills (repoDir) into <baseDir>/skills/<name>/. To keep every
// skill self-contained — and to avoid writing anything outside the
// skills tree — the shared command matrix (plugins/infracost/BINDINGS.md)
// is co-located inside each skill dir and the skill body's
// `../../BINDINGS.md` link is rewritten to point at the local copy. Split
// out from installGitLabDuoSkills so the file-placement logic is testable
// without a network clone.
func writeDuoSkills(repoDir, baseDir string) error {
	skillsDir := filepath.Join(baseDir, "skills")

	// BINDINGS.md (the CLI command matrix the Duo skills drive off) only
	// exists once the dual-binding skills are published. If it's absent we
	// still install whatever skills are present rather than hard-failing,
	// and skip both co-locating it and the link rewrite. installed tracks
	// whether we placed anything so a completely empty clone is an error.
	bindings, bindingsErr := os.ReadFile(filepath.Join(repoDir, "plugins", "infracost", "BINDINGS.md")) //nolint:gosec // repoDir is a temp clone of a hardcoded repo

	installed := 0
	for _, name := range duoSkills {
		src := filepath.Join(repoDir, "plugins", "infracost", "skills", name)
		dst := filepath.Join(skillsDir, name)

		skill, err := os.ReadFile(filepath.Join(src, "SKILL.md")) //nolint:gosec // src is under the temp clone
		if err != nil {
			if os.IsNotExist(err) {
				continue // skill not in this revision — skip, don't abort
			}
			return fmt.Errorf("reading %s skill: %w", name, err)
		}

		// Start clean so re-running setup refreshes to the latest revision
		// rather than leaving stale files behind.
		if err := os.RemoveAll(dst); err != nil {
			return fmt.Errorf("clearing %s: %w", dst, err)
		}
		if err := os.MkdirAll(dst, 0o750); err != nil {
			return fmt.Errorf("creating %s: %w", dst, err)
		}

		if bindingsErr == nil {
			// The skill links to the matrix as ../../BINDINGS.md (resolves
			// within the repo layout); point it at the co-located copy.
			skill = bytes.ReplaceAll(skill, []byte("](../../BINDINGS.md)"), []byte("](BINDINGS.md)"))
		}
		if err := os.WriteFile(filepath.Join(dst, "SKILL.md"), skill, 0o600); err != nil { //nolint:gosec // dst is <duoConfigDir>/skills/<name>; name is a hardcoded constant and baseDir is the user's own Duo config dir
			return fmt.Errorf("writing %s skill: %w", name, err)
		}
		if bindingsErr == nil {
			if err := os.WriteFile(filepath.Join(dst, "BINDINGS.md"), bindings, 0o600); err != nil { //nolint:gosec // see above — path is under the user's Duo config dir
				return fmt.Errorf("writing %s bindings: %w", name, err)
			}
		}
		installed++
	}

	if installed == 0 {
		return fmt.Errorf("no Infracost skills found in %s — the agent-skills repository layout may have changed", repoDir)
	}

	// Stamp the installed version so the staleness check can tell whether
	// Duo is behind. Best-effort: if the manifest has no version we just
	// clear any prior stamp so a stale number isn't left behind.
	versionFile := filepath.Join(skillsDir, duoSkillVersionFile)
	if v := readPluginManifestVersion(repoDir); v != "" {
		if err := os.WriteFile(versionFile, []byte(v), 0o600); err != nil { //nolint:gosec // path is under the user's Duo config dir
			return fmt.Errorf("writing version marker: %w", err)
		}
	} else {
		_ = os.Remove(versionFile)
	}
	return nil
}

// duoInstalledVersion reads the version marker written by writeDuoSkills.
// Returns "" when Duo skills aren't installed or the marker is absent
// (e.g. installed before version stamping existed).
func duoInstalledVersion() (string, error) {
	baseDir, err := duoConfigDir()
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(filepath.Join(baseDir, "skills", duoSkillVersionFile)) //nolint:gosec // path is under the user's Duo config dir
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// removeGitLabDuoSkills deletes the skill directories installGitLabDuoSkills
// created. It only removes <duoConfigDir>/skills/<name>/ for the skills we
// install and leaves everything else (including any user-authored AGENTS.md
// or chat-rules.md) untouched.
func removeGitLabDuoSkills() error {
	baseDir, err := duoConfigDir()
	if err != nil {
		return err
	}
	var actionErr error
	if err := ui.RunWithSpinner("Removing Infracost skills from GitLab Duo...", "Skills removed", func() {
		skillsDir := filepath.Join(baseDir, "skills")
		for _, name := range duoSkills {
			if err := os.RemoveAll(filepath.Join(skillsDir, name)); err != nil {
				actionErr = fmt.Errorf("removing %s skill: %w", name, err)
				return
			}
		}
		_ = os.Remove(filepath.Join(skillsDir, duoSkillVersionFile))
	}); err != nil {
		return err
	}
	return actionErr
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
		version: func(bin string) (string, error) {
			return pluginListVersion(bin, "plugin", "list")
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
		version: func(bin string) (string, error) {
			return pluginListVersion(bin, "plugin", "list")
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
		version: func(_ string) (string, error) {
			return copilotVSCodeInstalledVersion()
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
		version: func(bin string) (string, error) {
			return pluginListVersion(bin, "skills", "list")
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
	{
		name: "GitLab Duo",
		icon: "gitlab",
		// No `binaries` — install is filesystem-driven (git clone + copy
		// into ~/.gitlab/duo/skills/), so we run setup unconditionally
		// regardless of whether the Duo CLI is on PATH. The skills sit in
		// the Duo config dir harmlessly until the CLI is installed and run
		// with --enable-global-skills.
		setup: func(_, _ string) error {
			return installGitLabDuoSkills()
		},
		teardown: func(_, _ string) error {
			return removeGitLabDuoSkills()
		},
		check: func(_ string) (bool, error) {
			baseDir, err := duoConfigDir()
			if err != nil {
				return false, err
			}
			_, err = os.Stat(filepath.Join(baseDir, "skills", "scan", "SKILL.md"))
			if err != nil {
				if os.IsNotExist(err) {
					return false, nil
				}
				return false, err
			}
			return true, nil
		},
		version: func(_ string) (string, error) {
			return duoInstalledVersion()
		},
		// Duo only loads user-level skills when it's started with global
		// skills enabled, so the install isn't self-activating — surface
		// the flag as an explicit next step.
		postSetup: fmt.Sprintf(`GitLab Duo only loads user-level ("global") skills when you enable them:

  • Start the Duo CLI with the flag:
      %s
    (or via glab: %s)

  • Or enable it for every session:
      %s

Requires GitLab Duo CLI 8.83.0+ (GitLab 19.0+). User-level skills are an
experimental feature and are read only by the Duo CLI — not the VS Code /
JetBrains extensions or the GitLab web UI.`,
			ui.Code("duo --enable-global-skills"),
			ui.Code("glab duo cli --enable-global-skills"),
			ui.Code("export GITLAB_ENABLE_GLOBAL_SKILLS=true")),
		manual: fmt.Sprintf(`To install Infracost skills in GitLab Duo (user-level):
  1. Install the GitLab Duo CLI (8.83.0+): %s
  2. Clone the skills and copy them into your Duo skills directory:
     %s
     %s
     %s
  3. Start Duo with global skills enabled:
     %s`,
			ui.Code("https://docs.gitlab.com/user/gitlab_duo_cli/"),
			ui.Code("git clone "+infracostSkillsRepo),
			ui.Code("mkdir -p ~/.gitlab/duo/skills"),
			ui.Code("cp -r agent-skills/plugins/infracost/skills/{scan,price-lookup,iac-generation} ~/.gitlab/duo/skills/"),
			ui.Code("duo --enable-global-skills")),
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
	cmd.AddCommand(agentStatus(cfg))
	return cmd
}

func agentStatus(cfg *config.Config) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show which agents have Infracost skills installed and whether they're up to date",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var statuses []AgentStatus
			var latest string
			var probeErr error
			if err := ui.RunWithSpinner("Checking installed agent skills...", "Checked agent skills", func() {
				statuses, latest, probeErr = AgentStatuses(cmd.Context(), cfg)
			}); err != nil {
				return err
			}
			if probeErr != nil {
				return probeErr
			}

			// A successful live probe is the freshest possible signal, so
			// refresh the cache the background nag reads from.
			var stale []StaleAgent
			for _, s := range statuses {
				if s.Stale {
					stale = append(stale, StaleAgent{Name: s.Name, Installed: s.Installed, Latest: s.Latest})
				}
			}
			_ = saveAgentCheckCache(&agentCheckCache{CheckedAt: time.Now(), Latest: latest, Stale: stale})

			w := cmd.OutOrStdout()
			if len(statuses) == 0 {
				_, _ = fmt.Fprintf(w, "No AI agents with Infracost skills detected. Run %s to install them.\n", ui.Code("infracost agent setup"))
				return nil
			}

			_, _ = fmt.Fprintf(w, "Latest Infracost skill version: %s\n\n", ui.Bold(latest))
			for _, s := range statuses {
				if s.Stale {
					_, _ = fmt.Fprintf(w, "  %s  %s — %s (latest %s)\n", ui.Caution("!"), s.Name, s.Installed, ui.Bold(s.Latest))
				} else {
					_, _ = fmt.Fprintf(w, "  %s  %s — %s (up to date)\n", ui.Positive("✔"), s.Name, s.Installed)
				}
			}
			if len(stale) > 0 {
				_, _ = fmt.Fprintf(w, "\nRun %s to upgrade the outdated agents.\n", ui.Code("infracost agent setup"))
			}
			return nil
		},
	}
}

func agentSetup(cfg *config.Config) *cobra.Command {
	var scope string

	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Install Infracost skills for your AI coding agent",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := requireUserLogin(cfg); err != nil {
				return err
			}
			if err := ensureAuthAndOrg(cmd.Context(), cfg); err != nil {
				return err
			}
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
	// A just-installed agent is current by definition; drop any cached
	// "you're behind" result so the staleness nag doesn't linger after a
	// fix (the next background check repopulates it accurately).
	ClearStaleAgentsCache()
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

// resolveAgentBinaryForCheck is a config-optional variant used by the
// staleness check. When cfg is nil (the background nag path, which runs
// concurrently with flag parsing) it resolves purely from PATH, avoiding
// a data race on config fields. When cfg is present (the explicit `agent
// status` command) it honors any configured binary override.
func resolveAgentBinaryForCheck(cfg *config.Config, a agent) (string, error) {
	if cfg != nil {
		return resolveAgentBinary(cfg, a)
	}
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
			// Agents with extra activation steps (e.g. GitLab Duo's
			// --enable-global-skills flag) get a tailored next-step card;
			// the generic "restart to activate" line only applies to the
			// agents that are ready the moment the files are in place.
			if a.postSetup != "" {
				ui.Successf("Infracost skills installed for %s.", a.name)
				fmt.Println()
				fmt.Print(ui.InstructionsCard("Activate Infracost skills in "+a.name, a.postSetup))
			} else {
				ui.Successf("Infracost skills enabled for %s. Restart your agent to activate.", a.name)
			}
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

	// Filesystem-driven agents (e.g. GitLab Duo) have no `binaries` and
	// don't need a CLI to tear down — only resolve a binary for agents
	// whose teardown shells out to one.
	var bin string
	if len(a.binaries) > 0 {
		var err error
		if bin, err = resolveAgentBinary(cfg, a); err != nil {
			return err
		}
	}

	if err := a.teardown(bin, scope); err != nil {
		return err
	}

	// Drop any cached staleness result so a removed agent isn't reported
	// as "outdated" on the next run.
	ClearStaleAgentsCache()
	ui.Successf("Infracost skills removed from %s.", a.name)
	return nil
}
