package cmds

import (
	"regexp"
	"strings"

	"github.com/infracost/cli/internal/ui"
	"github.com/spf13/cobra"
)

var (
	exampleCommentRe = regexp.MustCompile(`^(\s*)#(.*)$`)
	exampleCommandRe = regexp.MustCompile(`^(\s*)(\$) (.+)$`)

	// Matches the flag-token at the start of each line in pflag's
	// FlagUsages output. Captures `-x, --name` pairs and `--name` alone.
	flagTokenRe = regexp.MustCompile(`(?m)^(\s+)(-[a-zA-Z], --[\w-]+|--[\w-]+)`)
)

// colorizeFlags applies the accent color to the flag-name token at the
// start of each line in a FlagUsages block. Padding, TYPE indicators,
// and descriptions are left untouched so column alignment is preserved.
func colorizeFlags(s string) string {
	return flagTokenRe.ReplaceAllStringFunc(s, func(match string) string {
		sub := flagTokenRe.FindStringSubmatch(match)
		return sub[1] + ui.Accent(sub[2])
	})
}

// colorizeExamples post-processes a command's Example block at template
// render time so comment lines (`# ...`) appear muted and command lines
// (`$ ...`) show "$" muted with the command itself in the code color.
// Other lines pass through unchanged. Respects --no-color via ui helpers.
func colorizeExamples(s string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		if m := exampleCommentRe.FindStringSubmatch(line); m != nil {
			lines[i] = m[1] + ui.Muted("#"+m[2])
			continue
		}
		if m := exampleCommandRe.FindStringSubmatch(line); m != nil {
			lines[i] = m[1] + ui.Muted(m[2]) + " " + ui.Code(m[3])
			continue
		}
	}
	return strings.Join(lines, "\n")
}

// ApplyHelpStyles styles cobra's usage/help output with the brand
// palette (section headings, group titles, examples) and hides the
// auto-generated `help` command from listings. Subcommands inherit the
// modified template from the root command.
func ApplyHelpStyles(cmd *cobra.Command) {
	cmd.InitDefaultHelpCmd()
	for _, c := range cmd.Commands() {
		if c.Name() == "help" {
			c.Hidden = true
			break
		}
	}

	cobra.AddTemplateFunc("infracostExamples", colorizeExamples)
	cobra.AddTemplateFunc("infracostAccent", ui.Accent)
	cobra.AddTemplateFunc("infracostFlags", colorizeFlags)

	tmpl := cmd.UsageTemplate()

	// Cobra's default template force-includes the help command in listings via
	// `(or .IsAvailableCommand (eq .Name "help"))`. Drop the carve-out so the
	// Hidden flag actually hides it.
	tmpl = strings.ReplaceAll(tmpl, `(or .IsAvailableCommand (eq .Name "help"))`, ".IsAvailableCommand")

	// Pipe Example through the colorizer.
	tmpl = strings.Replace(tmpl, "{{.Example}}", "{{.Example | infracostExamples}}", 1)

	// Apply the accent color to command names in listings. We pad first
	// (so column alignment is computed on visible text), then color the
	// padded result.
	tmpl = strings.ReplaceAll(tmpl, "{{rpad .Name .NamePadding }}", "{{rpad .Name .NamePadding | infracostAccent}}")

	// Apply the accent color to flag-name tokens inside the Flags and
	// Global Flags blocks (post-formatting, so pflag's column alignment
	// stays correct).
	tmpl = strings.ReplaceAll(tmpl, "{{.LocalFlags.FlagUsages | trimTrailingWhitespaces}}", "{{.LocalFlags.FlagUsages | trimTrailingWhitespaces | infracostFlags}}")
	tmpl = strings.ReplaceAll(tmpl, "{{.InheritedFlags.FlagUsages | trimTrailingWhitespaces}}", "{{.InheritedFlags.FlagUsages | trimTrailingWhitespaces | infracostFlags}}")

	headings := []string{
		"Usage:",
		"Aliases:",
		"Examples:",
		"Available Commands:",
		"Additional Commands:",
		"Flags:",
		"Global Flags:",
		"Additional help topics:",
	}
	for _, h := range headings {
		tmpl = strings.Replace(tmpl, h, ui.Bold(ui.Brand(h)), 1)
	}

	// Brand-color and bold the title of each command group (rendered as
	// `{{.Title}}` in cobra's template).
	for _, g := range cmd.Groups() {
		g.Title = ui.Bold(ui.Brand(g.Title))
	}

	cmd.SetUsageTemplate(tmpl)
}
