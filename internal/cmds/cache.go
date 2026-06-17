package cmds

import (
	"fmt"

	"github.com/infracost/cli/internal/cache"
	"github.com/infracost/cli/internal/config"
	"github.com/infracost/cli/internal/ui"
	"github.com/spf13/cobra"
)

// CacheCmd returns the `infracost cache` parent command and registers
// its clear/prune/info subcommands. Lives in the maintain group next to
// `doctor` and `update`.
func CacheCmd(_ *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cache",
		Short: "Inspect and manage the on-disk Infracost cache",
	}
	cmd.AddCommand(cacheInfoCmd())
	cmd.AddCommand(cachePruneCmd())
	cmd.AddCommand(cacheClearCmd())
	return cmd
}

func cacheInfoCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "info",
		Short: "Show on-disk size of each Infracost cache",
		RunE: func(_ *cobra.Command, _ []string) error {
			rows := cache.Info()
			labelWidth := 0
			for _, r := range rows {
				if len(r.Label) > labelWidth {
					labelWidth = len(r.Label)
				}
			}
			fmt.Printf("%s %s\n\n", ui.Muted("Cache root:"), ui.Muted(cache.Root()))
			for _, r := range rows {
				// Pad the plain label first — printf's width counts bytes,
				// and ui.Accent's ANSI escapes would otherwise inflate the
				// measured width and eat the padding.
				padded := fmt.Sprintf("%-*s", labelWidth, r.Label)
				fmt.Printf("%s   %s\n", ui.Accent(padded), cache.FormatBytes(r.Bytes))
			}
			return nil
		},
	}
}

func cachePruneCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "prune",
		Short: "Remove cache entries older than 24h and any stray files",
		RunE: func(_ *cobra.Command, _ []string) error {
			cache.Prune()
			fmt.Println(ui.Positive("✓ Cache pruned."))
			return nil
		},
	}
}

func cacheClearCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "clear",
		Short: "Wipe the results and parser caches",
		RunE: func(_ *cobra.Command, _ []string) error {
			cache.Clear()
			fmt.Println(ui.Positive("✓ Cache cleared."))
			return nil
		},
	}
}
