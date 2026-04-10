package cmds

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/charmbracelet/huh"
	"github.com/infracost/cli/internal/api"
	"github.com/infracost/cli/internal/api/dashboard"
	"github.com/infracost/cli/internal/config"
	"github.com/infracost/cli/pkg/auth"
	"github.com/infracost/cli/pkg/logging"
	"golang.org/x/oauth2"
)

// resolveOrg resolves the active organization into cfg.OrgID using the
// following priority chain:
//  1. --org flag / INFRACOST_CLI_ORG env var
//  2. .infracost/org file in the working directory
//  3. SelectedOrgID saved in the user cache (from `infracost org switch`)
//
// If no org context is found and the user belongs to multiple orgs, a warning
// is written to stderr advising them to set one explicitly.
// If the user belongs to exactly one org, it is used automatically.
func resolveOrg(ctx context.Context, cfg *config.Config, source oauth2.TokenSource) error {
	uc, err := ensureUserCache(ctx, cfg, source)
	if err != nil {
		return err
	}

	// Nothing to resolve if user has no orgs.
	if uc == nil || len(uc.Organizations) == 0 {
		return nil
	}

	// Priority 1: explicit --org flag or INFRACOST_CLI_ORG env var.
	if cfg.Org != "" {
		orgID, _, err := auth.ResolveOrgID(cfg.Org, uc.Organizations)
		if err != nil {
			return err
		}
		cfg.OrgID = orgID
		return nil
	}

	// Priority 2: local .infracost/org file.
	if wd, wdErr := os.Getwd(); wdErr == nil {
		if slug, readErr := auth.ReadLocalOrg(wd); readErr == nil && slug != "" {
			orgID, _, resolveErr := auth.ResolveOrgID(slug, uc.Organizations)
			if resolveErr == nil {
				cfg.OrgID = orgID
				return nil
			}
			logging.WithError(resolveErr).Msg("local .infracost/org references unknown org, ignoring")
		}
	}

	// Priority 3: SelectedOrgID from user cache.
	if uc.SelectedOrgID != "" {
		for _, org := range uc.Organizations {
			if org.ID == uc.SelectedOrgID {
				cfg.OrgID = org.ID
				return nil
			}
		}
		logging.Warnf("cached selectedOrgID %s not found in org list, ignoring", uc.SelectedOrgID)
	}

	// No org context set — if single org, use it silently.
	if len(uc.Organizations) == 1 {
		cfg.OrgID = uc.Organizations[0].ID
		return nil
	}

	// Multiple orgs, no selection — prompt in TTY, warn otherwise.
	info, statErr := os.Stdin.Stat()
	if statErr == nil && (info.Mode()&os.ModeCharDevice) != 0 {
		slug, pickErr := pickOrg(uc.Organizations, cfg, "", defaultPickOrgTitle)
		if pickErr == nil {
			for _, org := range uc.Organizations {
				if org.Slug == slug {
					cfg.OrgID = org.ID
					uc.SelectedOrgID = org.ID
					if saveErr := cfg.Auth.SaveUserCache(uc); saveErr != nil {
						logging.WithError(saveErr).Msg("failed to save org selection")
					}
					return nil
				}
			}
		} else if !errors.Is(pickErr, huh.ErrUserAborted) {
			logging.WithError(pickErr).Msg("failed to prompt for org selection")
		}
	}

	fmt.Fprintf(os.Stderr, "warning: you belong to %d organizations but none are selected.\n", len(uc.Organizations))
	fmt.Fprintf(os.Stderr, "         Run 'infracost org switch' to select one, or set INFRACOST_CLI_ORG.\n")

	return nil
}

// ensureUserCache loads the user cache, refreshing from the API if stale or missing.
func ensureUserCache(ctx context.Context, cfg *config.Config, source oauth2.TokenSource) (*auth.UserCache, error) {
	uc, err := cfg.Auth.LoadUserCache()
	if err != nil {
		logging.WithError(err).Msg("failed to load user cache, fetching fresh data")
		uc = nil
	}

	if uc == nil || len(uc.Organizations) == 0 || uc.IsStale() {
		client := cfg.Dashboard.Client(api.Client(ctx, source, ""))
		fresh, fetchErr := fetchAndCacheUser(ctx, cfg, client)
		if fetchErr != nil {
			if uc != nil && len(uc.Organizations) > 0 {
				logging.WithError(fetchErr).Msg("failed to refresh user cache, using stale data")
				return uc, nil
			}
			return nil, fmt.Errorf("fetching user data: %w", fetchErr)
		}
		return fresh, nil
	}

	return uc, nil
}

func fetchAndCacheUser(ctx context.Context, cfg *config.Config, client dashboard.Client) (*auth.UserCache, error) {
	user, err := client.CurrentUser(ctx)
	if err != nil {
		return nil, err
	}

	return cacheUser(cfg, user), nil
}

func cacheUser(cfg *config.Config, user dashboard.CurrentUser) *auth.UserCache {
	orgs := make([]auth.CachedOrganization, len(user.Organizations))
	for i, org := range user.Organizations {
		roles := make([]string, len(org.Roles))
		for j, r := range org.Roles {
			roles[j] = r.ID
		}
		orgs[i] = auth.CachedOrganization{
			ID:    org.ID,
			Name:  org.Name,
			Slug:  org.Slug,
			Roles: roles,
		}
	}

	uc := &auth.UserCache{
		ID:            user.ID,
		Name:          user.Name,
		Email:         user.Email,
		Organizations: orgs,
	}

	// Preserve any existing org selection across cache refreshes.
	if existing, err := cfg.Auth.LoadUserCache(); err == nil && existing != nil {
		uc.SelectedOrgID = existing.SelectedOrgID
	}

	if err := cfg.Auth.SaveUserCache(uc); err != nil {
		logging.WithError(err).Msg("failed to save user cache")
	}

	return uc
}
