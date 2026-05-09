package cmds

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/infracost/cli/internal/api"
	"github.com/infracost/cli/internal/config"
	"github.com/infracost/cli/internal/orgresolve"
	"github.com/infracost/cli/internal/ui"
	"github.com/infracost/cli/pkg/auth"
	"github.com/infracost/cli/pkg/logging"
	"github.com/spf13/cobra"
)

func Org(cfg *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "org",
		Short: "Manage organizations",
	}
	cmd.AddCommand(orgList(cfg))
	cmd.AddCommand(orgSwitch(cfg))
	cmd.AddCommand(orgCurrent(cfg))
	return cmd
}

func orgList(cfg *config.Config) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List your organizations",
		RunE: func(cmd *cobra.Command, _ []string) error {
			uc, err := ensureOrgCache(cmd, cfg)
			if err != nil {
				return err
			}

			currentSlug, _, source := orgresolve.CurrentSlug(cfg, uc.Organizations, uc.SelectedOrgID)

			fmt.Println()
			ui.Heading("Organizations")
			fmt.Println()

			for _, org := range uc.Organizations {
				var marker string
				var suffix string
				if strings.EqualFold(org.Slug, currentSlug) {
					marker = "  " + ui.Positive("✔") + "  "
					if source == orgresolve.SourceRepo {
						suffix = "  " + ui.Muted("← set for this repo")
					}
				} else {
					marker = "     " // align with "  ✔  "
				}
				slug := ui.Accent(fmt.Sprintf("%-20s", org.Slug))
				role := ui.Muted(fmt.Sprintf("(%s)", orgresolve.Role(org)))
				fmt.Printf("%s%s %s%s\n", marker, slug, role, suffix)
			}

			return nil
		},
	}
}

func orgSwitch(cfg *config.Config) *cobra.Command {
	var repo bool

	cmd := &cobra.Command{
		Use:   "switch [org-slug]",
		Short: "Switch the active organization",
		Example: `  # Pick from a list of your organizations
  $ infracost org switch

  # Switch to a specific organization globally
  $ infracost org switch acme

  # Pin the active organization for this repository only
  $ infracost org switch acme --repo`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			uc, err := ensureOrgCache(cmd, cfg)
			if err != nil {
				return err
			}

			if len(uc.Organizations) == 0 {
				return fmt.Errorf("you don't belong to any organizations")
			}

			var slug string
			if len(args) == 1 {
				slug = args[0]
			} else {
				if !ui.IsInteractive() {
					return fmt.Errorf(
						"no org slug provided and no interactive terminal available — pass the slug as an argument (e.g. 'infracost org switch <slug>'); run 'infracost org list' to see your orgs",
					)
				}
				slug, err = orgresolve.Pick(uc.Organizations, cfg, uc.SelectedOrgID, orgresolve.DefaultPickTitle)
				if err != nil {
					if errors.Is(err, huh.ErrUserAborted) {
						return nil
					}
					return err
				}
			}

			orgID, _, err := auth.ResolveOrgID(slug, uc.Organizations)
			if err != nil {
				return err
			}

			if repo {
				wd, wdErr := os.Getwd()
				if wdErr != nil {
					return fmt.Errorf("getting working directory: %w", wdErr)
				}
				if err := auth.WriteLocalOrg(wd, slug); err != nil {
					return fmt.Errorf("saving local org: %w", err)
				}
				fmt.Printf("Organization switched to %s for this repository.\n", slug)
				return nil
			}

			uc.SelectedOrgID = orgID
			if err := cfg.Auth.SaveUserCache(uc); err != nil {
				return fmt.Errorf("saving org selection: %w", err)
			}

			fmt.Printf("Organization switched to %s.\n", slug)
			return nil
		},
	}

	cmd.Flags().BoolVar(&repo, "repo", false, "Save org selection for the current repository only")

	return cmd
}

func orgCurrent(cfg *config.Config) *cobra.Command {
	return &cobra.Command{
		Use:   "current",
		Short: "Show the current organization",
		RunE: func(cmd *cobra.Command, _ []string) error {
			uc, err := ensureOrgCache(cmd, cfg)
			if err != nil {
				return err
			}

			slug, _, source := orgresolve.CurrentSlug(cfg, uc.Organizations, uc.SelectedOrgID)
			if slug == "" {
				return fmt.Errorf("no organization selected. Run 'infracost org switch' to select one")
			}

			suffix := ""
			switch source {
			case orgresolve.SourceRepo:
				suffix = "  ← set for this repo"
			case orgresolve.SourceFlag:
				suffix = "  ← --org flag"
			}
			fmt.Printf("%s%s\n", slug, suffix)
			return nil
		},
	}
}

// ensureOrgCache always fetches fresh user/org data from the API, caching the
// result. Falls back to stale cached data if the fetch fails.
func ensureOrgCache(cmd *cobra.Command, cfg *config.Config) (*auth.UserCache, error) {
	source, err := cfg.Auth.Token(cmd.Context())
	if err != nil {
		return nil, fmt.Errorf("authenticating: %w", err)
	}

	uc, err := cfg.Auth.LoadUserCache()
	if err != nil {
		uc = nil
	}

	client := cfg.Dashboard.Client(api.Client(cmd.Context(), source, ""))
	fresh, fetchErr := orgresolve.FetchAndCacheUser(cmd.Context(), cfg, client)
	if fetchErr != nil {
		if uc != nil && len(uc.Organizations) > 0 {
			logging.WithError(fetchErr).Msg("failed to refresh org cache, using stale data")
			return uc, nil
		}
		return nil, fmt.Errorf("fetching user data: %w", fetchErr)
	}
	return fresh, nil
}
