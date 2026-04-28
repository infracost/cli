package cmds

import (
	"os"
	"strings"

	"github.com/liamg/tml"
	"github.com/spf13/cobra"
)

// ApplyHelpStyles bolds the section headings in cobra's usage/help output
// when stdout is a terminal. Subcommands inherit the modified template
// from the root command.
func ApplyHelpStyles(cmd *cobra.Command) {
	info, err := os.Stdout.Stat()
	if err != nil || (info.Mode()&os.ModeCharDevice) == 0 {
		return
	}

	headings := []string{
		"Usage:",
		"Aliases:",
		"Examples:",
		"Available Commands:",
		"Flags:",
		"Global Flags:",
		"Additional help topics:",
	}
	tmpl := cmd.UsageTemplate()
	for _, h := range headings {
		tmpl = strings.Replace(tmpl, h, tml.Sprintf("<bold>%s</bold>", h), 1)
	}
	cmd.SetUsageTemplate(tmpl)
}
