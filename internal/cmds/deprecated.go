package cmds

import (
	"fmt"
	"strings"

	"github.com/infracost/cli/internal/config"
	"github.com/liamg/tml"
	"github.com/spf13/cobra"
)

// Deprecated returns shims for legacy `infracost` commands that no longer
// exist in this CLI. They are hidden from help output. Most return a
// deprecation error pointing at the replacement; `breakdown` warns and
// then runs `scan` so existing scripts keep working.
func Deprecated(cfg *config.Config) []*cobra.Command {
	cmds := []*cobra.Command{breakdownShim(cfg)}

	removed := []struct{ name, replacement string }{
		{"diff", "Use `infracost scan` instead."},
		{"generate", "Use `infracost setup` instead."},
		{"comment", "Configure an official CI/CD integration with `infracost ci setup` instead."},
		{"configure", "This command has been removed."},
		{"output", "This command has been removed."},
		{"upload", "This command has been removed."},
	}
	for _, l := range removed {
		l := l
		cmds = append(cmds, &cobra.Command{
			Use:                l.name,
			Hidden:             true,
			DisableFlagParsing: true,
			RunE: func(_ *cobra.Command, _ []string) error {
				return fmt.Errorf("`infracost %s` is no longer supported. %s", l.name, l.replacement)
			},
		})
	}
	return cmds
}

func breakdownShim(cfg *config.Config) *cobra.Command {
	return &cobra.Command{
		Use:                "breakdown",
		Hidden:             true,
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, _ = fmt.Fprintln(cmd.ErrOrStderr(), tml.Sprintf("  <lightyellow>!</lightyellow>  `infracost breakdown` is deprecated, running `infracost scan` instead."))

			scan := Scan(cfg)
			scan.SetContext(cmd.Context())
			scan.SetIn(cmd.InOrStdin())
			scan.SetOut(cmd.OutOrStdout())
			scan.SetErr(cmd.ErrOrStderr())
			return scan.RunE(scan, translateBreakdownArgs(args))
		},
	}
}

// translateBreakdownArgs maps legacy `breakdown` args onto `scan`'s
// positional-only interface. `--path X` / `--path=X` becomes the target
// directory; other legacy flags are silently dropped.
func translateBreakdownArgs(args []string) []string {
	var positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--path" && i+1 < len(args):
			positional = append(positional, args[i+1])
			i++
		case strings.HasPrefix(a, "--path="):
			positional = append(positional, strings.TrimPrefix(a, "--path="))
		}
	}
	return positional
}
