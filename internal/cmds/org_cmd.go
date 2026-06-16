package cmds

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/infracost/cli/internal/api"
	"github.com/infracost/cli/internal/api/dashboard"
	"github.com/infracost/cli/internal/config"
	"github.com/infracost/cli/internal/ui"
	"github.com/infracost/cli/pkg/auth"
	"github.com/infracost/cli/pkg/logging"
	"github.com/spf13/cobra"
	"golang.org/x/oauth2"
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
	cmd.AddCommand(orgCreate(cfg))
	return cmd
}

func orgCreate(cfg *config.Config) *cobra.Command {
	return &cobra.Command{
		Use:   "create [name]",
		Short: "Create a new organization",
		Long: "Create a new Infracost organization and make it the active org. " +
			"Useful right after signing up if you don't already belong to an org — " +
			"for example, when `infracost setup` reports you have no organizations.",
		Example: `  # Create an org interactively
  $ infracost org create

  # Create an org with a specific name (works without a TTY)
  $ infracost org create "Acme Corp"`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			source, err := cfg.Auth.Token(ctx)
			if err != nil {
				return fmt.Errorf("authenticating: %w", err)
			}

			var name string
			if len(args) == 1 {
				name = strings.TrimSpace(args[0])
			}
			if name == "" {
				if !ui.IsInteractive() {
					return fmt.Errorf("no organization name provided and no interactive terminal available — pass the name as an argument (e.g. 'infracost org create \"Acme Corp\"')")
				}
				name, err = promptForOrgName("")
				if err != nil {
					if errors.Is(err, huh.ErrUserAborted) {
						return nil
					}
					return err
				}
			}

			org, err := createOrgAndCache(ctx, cfg, source, name)
			if err != nil {
				return err
			}

			ui.Successf("Created organization %s (%s)", ui.Accent(org.Slug), org.Name)
			return nil
		},
	}
}

// promptForOrgName asks the user for an organization name. The optional
// suggestion is offered as the default; the user can accept it or type
// their own. Empty input re-prompts via huh's validator.
func promptForOrgName(suggestion string) (string, error) {
	name := suggestion
	err := huh.NewInput().
		Title("What should we call your organization?").
		Description("This is shown to your teammates and in the dashboard.").
		Value(&name).
		Validate(func(s string) error {
			if strings.TrimSpace(s) == "" {
				return fmt.Errorf("name cannot be empty")
			}
			return nil
		}).
		WithTheme(ui.BrandTheme()).
		Run()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(name), nil
}

// createOrgAndCache creates an organization via the dashboard API, then
// refreshes the local user cache and pins the new org as the selected
// one so subsequent CLI calls in this session use it without further
// prompting.
func createOrgAndCache(ctx context.Context, cfg *config.Config, source oauth2.TokenSource, name string) (dashboard.Organization, error) {
	client := cfg.Dashboard.Client(api.Client(ctx, source, ""))

	org, err := client.CreateOrganization(ctx, name)
	if err != nil {
		return dashboard.Organization{}, fmt.Errorf("creating organization: %w", err)
	}

	uc, err := fetchAndCacheUser(ctx, cfg, client)
	if err != nil {
		// The org was created server-side; just warn so the next CLI run
		// picks it up from the refreshed CurrentUser call.
		logging.WithError(err).Msg("failed to refresh user cache after creating organization")
		return org, nil
	}
	uc.SelectedOrgID = org.ID
	if err := cfg.Auth.SaveUserCache(uc); err != nil {
		logging.WithError(err).Msg("failed to save org selection after creating organization")
	}
	cfg.OrgID = org.ID

	return org, nil
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

			fmt.Println()
			ui.Heading("Organizations")
			fmt.Println()

			for _, org := range uc.Organizations {
				var marker string
				var suffix string
				if strings.EqualFold(org.Slug, currentSlug) {
					marker = "  " + ui.Positive("✔") + "  "
					if source == orgSourceRepo {
						suffix = "  " + ui.Muted("← set for this repo")
					}
				} else {
					marker = "     " // align with "  ✔  "
				}
				slug := ui.Accent(fmt.Sprintf("%-20s", org.Slug))
				role := ui.Muted(fmt.Sprintf("(%s)", orgRole(org)))
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
		WithTheme(ui.BrandTheme()).
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
