package cmds

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/infracost/cli/internal/api"
	"github.com/infracost/cli/internal/config"
	"github.com/infracost/cli/pkg/auth"
	"github.com/infracost/cli/pkg/logging"
	"github.com/spf13/cobra"
)

const defaultPickOrgTitle = "Which organization do you want to use?"

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

			currentSlug, _, source := currentOrgSlug(cfg, uc.Organizations, uc.SelectedOrgID)

			for _, org := range uc.Organizations {
				marker := "  "
				suffix := ""
				if strings.EqualFold(org.Slug, currentSlug) {
					marker = "✔ "
					if source == orgSourceRepo {
						suffix = "  ← set for this repo"
					}
				}
				fmt.Printf("%s%-20s (%s)%s\n", marker, org.Slug, orgRole(org), suffix)
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
				slug, err = pickOrg(uc.Organizations, cfg, uc.SelectedOrgID, defaultPickOrgTitle)
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

			slug, _, source := currentOrgSlug(cfg, uc.Organizations, uc.SelectedOrgID)
			if slug == "" {
				return fmt.Errorf("no organization selected. Run 'infracost org switch' to select one")
			}

			suffix := ""
			switch source {
			case orgSourceRepo:
				suffix = "  ← set for this repo"
			case orgSourceFlag:
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
	fresh, fetchErr := fetchAndCacheUser(cmd.Context(), cfg, client)
	if fetchErr != nil {
		if uc != nil && len(uc.Organizations) > 0 {
			logging.WithError(fetchErr).Msg("failed to refresh org cache, using stale data")
			return uc, nil
		}
		return nil, fmt.Errorf("fetching user data: %w", fetchErr)
	}
	return fresh, nil
}

type orgSource int

const (
	orgSourceNone   orgSource = iota
	orgSourceFlag             // --org flag or INFRACOST_CLI_ORG env var
	orgSourceRepo             // .infracost/org file in working directory
	orgSourceGlobal           // SelectedOrgID in user cache (from org switch)
)

// currentOrgSlug determines the current org slug from the resolution chain:
// --org flag/env → .infracost/org → selectedOrgID from caller.
func currentOrgSlug(cfg *config.Config, orgs []auth.CachedOrganization, selectedOrgID string) (string, string, orgSource) {
	// 1. Explicit --org flag or INFRACOST_CLI_ORG env var.
	if cfg.Org != "" {
		_, name, err := auth.ResolveOrgID(cfg.Org, orgs)
		if err == nil {
			return cfg.Org, name, orgSourceFlag
		}
	}

	// 2. Local .infracost/org file.
	if wd, err := os.Getwd(); err == nil {
		if slug, err := auth.ReadLocalOrg(wd); err == nil && slug != "" {
			if _, name, err := auth.ResolveOrgID(slug, orgs); err == nil {
				return slug, name, orgSourceRepo
			}
		}
	}

	// 3. SelectedOrgID passed by caller.
	if selectedOrgID != "" {
		for _, org := range orgs {
			if org.ID == selectedOrgID {
				return org.Slug, org.Name, orgSourceGlobal
			}
		}
	}

	return "", "", orgSourceNone
}

func pickOrg(orgs []auth.CachedOrganization, cfg *config.Config, selectedOrgID string, title string) (string, error) {
	currentSlug, _, _ := currentOrgSlug(cfg, orgs, selectedOrgID)

	options := make([]huh.Option[string], len(orgs))
	for i, org := range orgs {
		label := fmt.Sprintf("%-20s (%s)", org.Slug, orgRole(org))
		options[i] = huh.NewOption(label, org.Slug)
	}

	// Pre-select the current org if there is one.
	var selected string
	if idx := slices.IndexFunc(orgs, func(o auth.CachedOrganization) bool {
		return strings.EqualFold(o.Slug, currentSlug)
	}); idx >= 0 {
		selected = orgs[idx].Slug
	}

	err := huh.NewSelect[string]().
		Title(title).
		Options(options...).
		Value(&selected).
		Run()
	if err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return "", err
		}
		return "", fmt.Errorf("selecting organization: %w", err)
	}

	return selected, nil
}

func orgRole(org auth.CachedOrganization) string {
	if slices.Contains(org.Roles, "organization_owner") {
		return "owner"
	}
	return "member"
}
