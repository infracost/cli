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
	ageStr := cache.DefaultPruneAge.String()
	cmd := &cobra.Command{
		Use:   "prune",
		Short: "Remove old cache entries and any stray files",
		Long: "Removes cache entries older than --age plus any stray files at the cache root.\n" +
			"Age accepts Go duration syntax (e.g. 24h, 30m, 12h30m) plus shorthand 'd' (days) and 'w' (weeks).",
		RunE: func(_ *cobra.Command, _ []string) error {
			age, err := cache.ParseAge(ageStr)
			if err != nil {
				return err
			}
			cache.Prune(age)
			fmt.Println(ui.Positive("✓ Cache pruned."))
			return nil
		},
	}
	cmd.Flags().StringVar(&ageStr, "age", ageStr, "Minimum age of entries to prune (e.g. 24h, 7d, 2w)")
	return cmd
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
