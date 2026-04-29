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

type ide struct {
	name       string
	binaries   []string                    // CLI binaries to look for on PATH
	installCmd func(bin string) *exec.Cmd  // CLI-based install
	check      func(bin string) (bool, error) // returns true if infracost extension is installed
	url        string                       // marketplace/install URL fallback
	hint       string                       // message shown before opening the URL
	manual     string                       // manual instructions (instead of URL) e.g. neovim
	enabled    bool                         // temporarily disable IDEs under development
}

var supportedIDEs = []ide{
	{
		name:     "VS Code",
		binaries: []string{"code", "codium"},
		installCmd: func(bin string) *exec.Cmd {
			return exec.Command(bin, "--install-extension", "infracost.infracost")
		},
		check: func(bin string) (bool, error) {
			var out bytes.Buffer
			cmd := exec.Command(bin, "--list-extensions") //nolint:gosec // bin is resolved from PATH
			cmd.Stdout = &out
			cmd.Stderr = &out
			if err := cmd.Run(); err != nil {
				return false, err
			}
			return strings.Contains(out.String(), "infracost.infracost"), nil
		},
		enabled: true,
		url:     "https://marketplace.visualstudio.com/items?itemName=infracost.infracost",
	},
	{
		name:    "JetBrains (IntelliJ, GoLand, etc.)",
		url:     "https://plugins.jetbrains.com/plugin/24761-infracost",
		enabled: true,
		hint:    "Click the \"Install\" button on the plugin page, then follow the prompts in your IDE.",
	},
	{
		name:     "Zed",
		binaries: []string{"zed"},
		installCmd: func(bin string) *exec.Cmd {
			return exec.Command(bin, "extension", "install", "infracost")
		},
		url: "https://zed.dev/extensions?query=infracost",
	},
	{
		name:    "Neovim",
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
			return RunIDESetup(false)
		},
	}
}

// RunIDESetup is the core logic for `infracost ide setup`, callable from the
// unified `infracost setup` flow (DEV-230). When skippable is true, a "Skip"
// option is appended to the selection list.
func RunIDESetup(skippable bool) error {
	if !ui.IsInteractive() {
		return nil
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
		Run()
	if err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return nil
		}
		return fmt.Errorf("selecting IDE: %w", err)
	}

	if selected < 0 {
		return nil
	}

	return installIDE(enabledIDEs[selected])
}

func installIDE(i ide) error {
	if i.manual != "" {
		fmt.Println(i.manual)
		return nil
	}

	for _, bin := range i.binaries {
		path, err := exec.LookPath(bin)
		if err != nil {
			continue
		}

		var actionErr error
		if err := ui.RunWithSpinner(fmt.Sprintf("Installing Infracost extension via %s...", bin), "Infracost extension installed", func() {
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

	if i.url != "" {
		if len(i.binaries) > 0 {
			ui.Warnf("Could not find a CLI for %s on your PATH.", i.name)
		}
		if i.hint != "" {
			fmt.Println(i.hint)
		}
		fmt.Printf("  %s\n", i.url)
		if ui.PressEnter("\nPress Enter to open in your browser...") {
			if err := browser.Open(i.url); err != nil {
				ui.Failf("Failed to open browser. Visit the URL manually:\n   %s", i.url)
			} else {
				ui.Successf("Opened %s in your browser.", i.url)
			}
		}
		return nil
	}

	return fmt.Errorf("no install method available for %s", i.name)
}
