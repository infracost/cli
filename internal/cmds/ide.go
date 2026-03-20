package cmds

import (
	"errors"
	"fmt"
	"os/exec"

	"github.com/charmbracelet/huh"
	"github.com/infracost/cli/internal/config"
	"github.com/infracost/cli/pkg/auth/browser"
	"github.com/spf13/cobra"
)

type ide struct {
	name       string
	binaries   []string // CLI binaries to look for on PATH
	installCmd func(bin string) *exec.Cmd
	url        string // marketplace/install URL fallback
	hint       string // message shown before opening the URL
	manual     string // manual instructions (instead of URL) e.g. neovim
	enabled    bool   // temporarily disable IDEs under development
}

var supportedIDEs = []ide{
	{
		name:     "VS Code",
		binaries: []string{"code", "codium"},
		installCmd: func(bin string) *exec.Cmd {
			return exec.Command(bin, "--install-extension", "infracost.infracost")
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

func IDE(_ *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ide",
		Short: "Manage IDE integrations",
	}
	cmd.AddCommand(ideSetup())
	return cmd
}

func ideSetup() *cobra.Command {
	return &cobra.Command{
		Use:   "setup",
		Short: "Install the Infracost extension for your IDE",
		RunE: func(_ *cobra.Command, _ []string) error {
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

			return installIDE(enabledIDEs[selected])
		},
	}
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

		fmt.Printf("Installing Infracost extension via %s...\n", bin)
		cmd := i.installCmd(path)
		cmd.Stdout = nil
		cmd.Stderr = nil
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("installing extension: %w", err)
		}

		fmt.Println("Infracost extension installed successfully.")
		return nil
	}

	if i.url != "" {
		if len(i.binaries) > 0 {
			fmt.Printf("Could not find a CLI for %s on your PATH.\n", i.name)
		}
		if i.hint != "" {
			fmt.Println(i.hint)
		}
		fmt.Printf("Opening %s in your browser...\n", i.url)
		if err := browser.Open(i.url); err != nil {
			fmt.Printf("Failed to open browser. Visit the URL manually:\n  %s\n", i.url)
		}
		return nil
	}

	return fmt.Errorf("no install method available for %s", i.name)
}
