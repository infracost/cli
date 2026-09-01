package cmds

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/infracost/cli/internal/config"
	"github.com/infracost/cli/internal/ui"
	"github.com/infracost/cli/pkg/plugins"
	"github.com/spf13/cobra"
)

// errPluginValidationFailed is returned when at least one binary fails a
// check, so the process exits non-zero. The checklist is already printed by
// the time this is returned, so the message stays terse.
var errPluginValidationFailed = errors.New("plugin validation failed")

func pluginsValidateCmd(cfg *config.Config) *cobra.Command {
	var asJSON bool

	cmd := &cobra.Command{
		Use:   "validate [path]",
		Short: "Validate a plugin binary against the CLI's plugin expectations",
		Long: `Validate a plugin binary against the CLI's plugin expectations.

With a path argument, validates that single binary. With no argument, validates
every plugin binary in the plugin directory. Each binary is checked for an
executable file, a successful go-plugin handshake, well-formed metadata, and its
type-specific RPC surface.

Exits non-zero if any check fails.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			var results []plugins.ValidationResult

			if len(args) == 1 {
				// Explicit path: still collision-check against whatever is in
				// the plugin directory, since that's where discovery loads from.
				results = []plugins.ValidationResult{plugins.ValidateBinary(args[0], cfg.Plugins.PluginDir())}
			} else {
				dir := cfg.Plugins.PluginDir()
				var err error
				results, err = plugins.ValidateDir(dir)
				if err != nil {
					return fmt.Errorf("failed to read plugin directory %s: %w", dir, err)
				}
				if len(results) == 0 {
					if asJSON {
						return printPluginValidateJSON(results)
					}
					fmt.Printf("No plugins found in %s\n", dir)
					return nil
				}
			}

			if asJSON {
				return printPluginValidateJSON(results)
			}

			return printPluginValidateHuman(results)
		},
	}

	cmd.Flags().BoolVar(&asJSON, "json", false, "Output the validation checklist as JSON")
	return cmd
}

// pluginValidateJSON is the stable --json envelope: an overall ok plus one
// checklist per validated binary.
type pluginValidateJSON struct {
	OK      bool                       `json:"ok"`
	Results []plugins.ValidationResult `json:"results"`
}

func printPluginValidateJSON(results []plugins.ValidationResult) error {
	out := pluginValidateJSON{OK: allValidationsOK(results), Results: results}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		return err
	}
	if !out.OK {
		return errPluginValidationFailed
	}
	return nil
}

func printPluginValidateHuman(results []plugins.ValidationResult) error {
	for i, res := range results {
		if i > 0 {
			fmt.Println()
		}
		fmt.Printf("%s %s\n", ui.Bold("Validating"), ui.Accent(res.Path))
		for _, c := range res.Checks {
			line := renderCheckLine(c)
			fmt.Println(line)
		}
		if res.OK() {
			ui.Success("Passed")
		} else {
			ui.Fail("Failed")
		}
	}

	if !allValidationsOK(results) {
		return errPluginValidationFailed
	}
	return nil
}

func renderCheckLine(c plugins.CheckResult) string {
	var symbol string
	switch c.Status {
	case plugins.CheckPass:
		symbol = ui.Positive("✔")
	case plugins.CheckFail:
		symbol = ui.Danger("✗")
	case plugins.CheckWarn:
		symbol = ui.Caution("!")
	case plugins.CheckSkip:
		symbol = ui.Muted("–")
	default:
		symbol = " "
	}

	line := fmt.Sprintf("  %s  %s", symbol, c.Name)
	if c.Detail != "" {
		line += ui.Muted(" — " + c.Detail)
	}
	return line
}

func allValidationsOK(results []plugins.ValidationResult) bool {
	for _, r := range results {
		if !r.OK() {
			return false
		}
	}
	return true
}
