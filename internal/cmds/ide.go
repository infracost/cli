package cmds

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/infracost/cli/internal/config"
	"github.com/infracost/cli/internal/ui"
	"github.com/infracost/cli/pkg/auth/browser"
	"github.com/spf13/cobra"
)

type ide struct {
	name        string
	icon        string                          // slug for the embedded brand icon (internal/ui/icons/<slug>.png)
	binaries    []string                        // CLI binaries to look for on PATH
	bundlePaths func() []string                 // OS-specific absolute paths to check when binaries aren't on PATH (e.g. Antigravity's bundled CLI inside the .app)
	installCmd  func(bin string) *exec.Cmd     // CLI-based install
	check       func(bin string) (bool, error) // returns true if infracost extension is installed
	url         string                          // marketplace/install URL fallback
	hint        string                          // message shown before opening the URL
	manual      string                          // manual instructions (used when no scriptable path is available or its CLI isn't found)
	enabled     bool                            // temporarily disable IDEs under development
}

// vscodeFamilyBundlePaths returns OS-specific bundle paths for a
// VS Code-style IDE installed via its standard installer. Used to
// resolve the IDE's CLI when it isn't on $PATH — common when the user
// hasn't run the IDE's "Install 'X' command in PATH" command from the
// command palette. macOS covers both the system (/Applications) and
// per-user (~/Applications) install locations; Windows covers the
// per-user (LOCALAPPDATA\Programs) and machine-wide (Program Files)
// installer targets.
//
// Linux is intentionally left unset because distribution conventions
// (apt/dnf/snap/flatpak/tarball/AppImage) vary too widely to guess
// reliably — Linux users who hit this fall back to the manual card.
//
// appName is the .app / Programs basename (e.g. "Visual Studio Code");
// cliName is the binary name within the bundle (e.g. "code").
func vscodeFamilyBundlePaths(appName, cliName string) func() []string {
	return func() []string {
		switch runtime.GOOS {
		case "darwin":
			paths := []string{
				"/Applications/" + appName + ".app/Contents/Resources/app/bin/" + cliName,
			}
			if home, err := os.UserHomeDir(); err == nil {
				paths = append(paths, filepath.Join(home, "Applications", appName+".app", "Contents/Resources/app/bin", cliName))
			}
			return paths
		case "windows":
			var paths []string
			if local := os.Getenv("LOCALAPPDATA"); local != "" {
				paths = append(paths, filepath.Join(local, "Programs", appName, "bin", cliName+".cmd"))
			}
			if pf := os.Getenv("ProgramFiles"); pf != "" {
				paths = append(paths, filepath.Join(pf, appName, "bin", cliName+".cmd"))
			}
			return paths
		}
		return nil
	}
}

// vscodeFamilyInstall returns an *exec.Cmd that runs the given binary's
// `--install-extension` subcommand for the Infracost extension. Used by
// every VS Code-based IDE (VS Code, VSCodium, Cursor, Windsurf) — they
// all ship a `code`-style CLI that mirrors the same flag.
func vscodeFamilyInstall(bin string) *exec.Cmd {
	return exec.Command(bin, "--install-extension", "infracost.infracost") //nolint:gosec // bin resolved from PATH
}

// vscodeFamilyCheck reports whether the Infracost extension is installed
// in the VS Code-style IDE backed by bin. Same `--list-extensions`
// invocation works across the family.
func vscodeFamilyCheck(bin string) (bool, error) {
	var out bytes.Buffer
	cmd := exec.Command(bin, "--list-extensions") //nolint:gosec // bin resolved from PATH
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return false, err
	}
	return strings.Contains(out.String(), "infracost.infracost"), nil
}

// extensionPanelManual returns the standard "Open the Extensions panel,
// search for Infracost, click Install" instruction string used by every
// IDE that supports the Infracost VS Code extension via UI install only
// (e.g. Antigravity, Eclipse Theia). Wrapped in fmt.Sprintf at package
// init time so ui.Code's escapes are baked in alongside the surrounding
// text.
func extensionPanelManual(name string) string {
	return fmt.Sprintf(`To install the Infracost extension in %s:
  1. Open the Extensions panel
  2. Search for %s
  3. Click %s`,
		name,
		ui.Code("Infracost"),
		ui.Code("Install"))
}

var supportedIDEs = []ide{
	{
		name:        "VS Code",
		icon:        "vscode",
		binaries:    []string{"code"},
		bundlePaths: vscodeFamilyBundlePaths("Visual Studio Code", "code"),
		installCmd:  vscodeFamilyInstall,
		check:       vscodeFamilyCheck,
		enabled:     true,
		url:         "https://marketplace.visualstudio.com/items?itemName=infracost.infracost",
	},
	{
		name:        "Cursor",
		icon:        "cursor",
		binaries:    []string{"cursor"},
		bundlePaths: vscodeFamilyBundlePaths("Cursor", "cursor"),
		installCmd:  vscodeFamilyInstall,
		check:       vscodeFamilyCheck,
		enabled:     true,
		// Cursor's marketplace mirrors VS Code's; same extension ID.
		url: "https://marketplace.visualstudio.com/items?itemName=infracost.infracost",
	},
	{
		name:        "Windsurf",
		icon:        "windsurf",
		binaries:    []string{"windsurf"},
		bundlePaths: vscodeFamilyBundlePaths("Windsurf", "windsurf"),
		installCmd:  vscodeFamilyInstall,
		check:       vscodeFamilyCheck,
		enabled:     true,
		// Windsurf bundles its own marketplace experience inside the
		// Extensions panel; the public-facing fallback is Open VSX.
		url: "https://open-vsx.org/extension/infracost/infracost",
	},
	{
		name:        "VSCodium",
		icon:        "vscodium",
		binaries:    []string{"codium"},
		bundlePaths: vscodeFamilyBundlePaths("VSCodium", "codium"),
		installCmd:  vscodeFamilyInstall,
		check:       vscodeFamilyCheck,
		enabled:     true,
		// VSCodium's default registry is Open VSX (where the Infracost
		// extension is published) rather than the VS Marketplace.
		url: "https://open-vsx.org/extension/infracost/infracost",
	},
	{
		name:     "Google Antigravity",
		icon:     "antigravity",
		binaries: []string{"antigravity"}, // power users may put the bundled CLI on PATH
		// Antigravity ships a `code`-style CLI inside the app bundle.
		// Apple's app sandbox keeps it out of $PATH by default, so we
		// look at the canonical install locations directly. Linux
		// install paths aren't confirmed yet — those users fall through
		// to the manual card.
		bundlePaths: vscodeFamilyBundlePaths("Antigravity", "antigravity"),
		installCmd: vscodeFamilyInstall,
		check:      vscodeFamilyCheck,
		manual:     extensionPanelManual("Google Antigravity"),
		enabled:    true,
	},
	{
		name:    "JetBrains (IntelliJ, GoLand, etc.)",
		icon:    "jetbrains",
		url:     "https://plugins.jetbrains.com/plugin/24761-infracost",
		enabled: true,
		hint:    fmt.Sprintf("In a moment your browser will open the JetBrains plugin page. Click %s there, then follow the prompts in your IDE to complete setup.", ui.Code("Install")),
	},
	{
		name:    "Eclipse Theia",
		icon:    "theia",
		manual:  extensionPanelManual("Eclipse Theia"),
		enabled: true,
	},
	{
		name:     "Zed",
		icon:     "zed",
		binaries: []string{"zed"},
		// Zed isn't a VS Code fork — its CLI lives at a different
		// location inside the bundle and is plainly named "cli". Same
		// per-user vs system fallback as the VS Code family.
		bundlePaths: func() []string {
			if runtime.GOOS != "darwin" {
				return nil
			}
			paths := []string{"/Applications/Zed.app/Contents/MacOS/cli"}
			if home, err := os.UserHomeDir(); err == nil {
				paths = append(paths, filepath.Join(home, "Applications/Zed.app/Contents/MacOS/cli"))
			}
			return paths
		},
		installCmd: func(bin string) *exec.Cmd {
			return exec.Command(bin, "extension", "install", "infracost") //nolint:gosec // bin resolved via lookup
		},
		url: "https://zed.dev/extensions?query=infracost",
	},
	{
		name:    "Neovim",
		icon:    "neovim",
		url:     "https://github.com/infracost/infracost.nvim/blob/main/README.md#installation",
		hint:    "Follow the instructions to configure your Neovim setup",
		enabled: true,
	},
}

func IDE(cfg *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ide",
		Short: "Manage IDE integrations",
	}
	cmd.AddCommand(ideSetup(cfg))
	return cmd
}

func ideSetup(_ *config.Config) *cobra.Command {
	return &cobra.Command{
		Use:   "setup",
		Short: "Install the Infracost extension for your IDE",
		RunE: func(_ *cobra.Command, _ []string) error {
			ideName, err := RunIDESetup(false)
			if err != nil {
				return err
			}
			// Mirror the unified `infracost setup` flow's closing card.
			// Empty name means the user aborted/skipped — nothing to
			// celebrate, no card.
			if ideName != "" {
				fmt.Println()
				fmt.Print(ui.GradientCard(setupCompleteContent("", ideName, false)))
			}
			return nil
		},
	}
}

// RunIDESetup is the core logic for `infracost ide setup`, callable from the
// unified `infracost setup` flow (DEV-230). When skippable is true, a "Skip"
// option is appended to the selection list. Returns the selected IDE's
// display name (empty if the user skipped or aborted) so the unified flow
// can tailor its closing CTA.
func RunIDESetup(skippable bool) (string, error) {
	if !ui.IsInteractive() {
		return "", nil
	}

	var enabledIDEs []ide
	for _, ide := range supportedIDEs {
		if ide.enabled {
			enabledIDEs = append(enabledIDEs, ide)
		}
	}

	options := make([]huh.Option[int], len(enabledIDEs))
	for i, ide := range enabledIDEs {
		options[i] = huh.NewOption(ide.name, i)
	}
	if skippable {
		options = append(options, huh.NewOption("Skip", -1))
	}

	var selected int
	err := huh.NewSelect[int]().
		Title("Which IDE do you use?").
		Options(options...).
		Value(&selected).
		WithTheme(ui.BrandTheme()).
		Run()
	if err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return "", nil
		}
		return "", fmt.Errorf("selecting IDE: %w", err)
	}

	if selected < 0 {
		return "", nil
	}

	if err := installIDE(enabledIDEs[selected]); err != nil {
		return "", err
	}
	return enabledIDEs[selected].name, nil
}

// ideIconSlug returns the icon slug for the IDE matching name, or "" if
// none. Used by the post-setup CTA to inline the brand mark next to the
// service name in static (non-bubbletea) output.
func ideIconSlug(name string) string {
	for _, i := range supportedIDEs {
		if i.name == name {
			return i.icon
		}
	}
	return ""
}

// findIDEBinary resolves a path to the IDE's CLI binary. Looks on
// PATH first (so power users can override) then falls back to the
// per-OS bundle paths declared on the entry. Returns "" if neither
// turns up an executable.
func findIDEBinary(i ide) string {
	for _, bin := range i.binaries {
		if path, err := exec.LookPath(bin); err == nil {
			return path
		}
	}
	if i.bundlePaths != nil {
		for _, p := range i.bundlePaths() {
			if info, err := os.Stat(p); err == nil && !info.IsDir() {
				return p
			}
		}
	}
	return ""
}

func installIDE(i ide) error {
	// Try the scriptable install first when one is available and the
	// IDE's CLI is resolvable. This lets us prefer the automatic path
	// over the manual card for IDEs that have both (e.g. Antigravity:
	// scriptable via the bundled CLI on macOS, manual fallback for
	// platforms we haven't mapped yet).
	if i.installCmd != nil {
		if path := findIDEBinary(i); path != "" {
			var actionErr error
			if err := ui.RunWithSpinner(fmt.Sprintf("Installing Infracost extension via %s...", filepath.Base(path)), "Infracost extension installed", func() {
				cmd := i.installCmd(path)
				cmd.Stdout = nil
				cmd.Stderr = nil
				actionErr = cmd.Run()
			}); err != nil {
				return err
			}
			if actionErr != nil {
				return fmt.Errorf("installing extension: %w", actionErr)
			}
			return nil
		}
	}

	if i.manual != "" {
		// Mirror the agent manual flow: render the steps inside an
		// InstructionsCard, gate progression on a "press enter" prompt,
		// then replace the card with a single checklist line so the
		// setup output stays compact.
		card := ui.InstructionsCard("Setup instructions for "+i.name, i.manual)
		fmt.Println()
		fmt.Print(card)
		rewind := strings.Count(card, "\n") + 3

		if ui.PressEnter("\nPress enter to continue...") {
			ui.EraseLastLines(rewind)
			ui.Successf("Followed setup instructions for %s", i.name)
		}
		return nil
	}

	if i.url != "" {
		if len(i.binaries) > 0 {
			ui.Warnf("Could not find a CLI for %s on your PATH.", i.name)
		}

		var content strings.Builder
		if i.hint != "" {
			content.WriteString(i.hint)
			content.WriteString("\n\n")
		}
		content.WriteString(ui.Code(i.url))

		card := ui.InstructionsCard("Setup instructions for "+i.name, content.String())
		fmt.Println()
		fmt.Print(card)
		// Each \n in the card == one rendered line. The +3 covers the
		// leading blank line, the prompt's leading "\n", and the user's
		// echoed Enter.
		cardRewind := strings.Count(card, "\n") + 3

		if !ui.PressEnter("\nPress enter to open browser...") {
			return nil
		}

		// First prompt acknowledged: wipe the card + prompt, run the
		// browser open, and surface a transient "Opened" checkmark so
		// the user can see the browser actually launched.
		ui.EraseLastLines(cardRewind)
		if err := browser.Open(i.url); err != nil {
			// On failure show the URL — the user needs it again to
			// follow manually now that the card is gone.
			ui.Failf("Failed to open browser. Visit the URL manually:\n   %s", ui.Code(i.url))
			return nil
		}
		ui.Success("Opened browser window")

		// Second prompt: gates progression so the setup flow doesn't
		// race ahead before the user has actually completed the
		// browser-side step. On enter, wipe both the "Opened" line and
		// this prompt, replace with the final checklist line — same
		// pattern as the manual flow's single-prompt cleanup.
		if ui.PressEnter("\nPress enter to continue...") {
			// 3 = the "Opened browser window" line + the prompt's
			// leading "\n" + the user's echoed Enter.
			ui.EraseLastLines(3)
			ui.Successf("Followed setup instructions for %s", i.name)
		}
		return nil
	}

	return fmt.Errorf("no install method available for %s", i.name)
}
