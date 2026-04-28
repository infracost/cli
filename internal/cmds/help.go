package cmds

import (
	"os"
	"strings"

	"github.com/liamg/tml"
	"github.com/spf13/cobra"
)

// ApplyHelpStyles bolds the section headings in cobra's usage/help output
// when stdout is a terminal, and hides the auto-generated `help` command
// from listings (it remains invokable). Subcommands inherit the modified
// template from the root command.
func ApplyHelpStyles(cmd *cobra.Command) {
	cmd.InitDefaultHelpCmd()
	for _, c := range cmd.Commands() {
		if c.Name() == "help" {
			c.Hidden = true
			break
		}
	}

	tmpl := cmd.UsageTemplate()

	// Cobra's default template force-includes the help command in listings via
	// `(or .IsAvailableCommand (eq .Name "help"))`. Drop the carve-out so the
	// Hidden flag actually hides it.
	tmpl = strings.ReplaceAll(tmpl, `(or .IsAvailableCommand (eq .Name "help"))`, ".IsAvailableCommand")

	info, err := os.Stdout.Stat()
	if err == nil && (info.Mode()&os.ModeCharDevice) != 0 {
		headings := []string{
			"Usage:",
			"Aliases:",
			"Examples:",
			"Available Commands:",
			"Flags:",
			"Global Flags:",
			"Additional help topics:",
		}
		for _, h := range headings {
			tmpl = strings.Replace(tmpl, h, tml.Sprintf("<bold>%s</bold>", h), 1)
		}
	}

	cmd.SetUsageTemplate(tmpl)
}
