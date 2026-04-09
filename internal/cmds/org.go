package cmds

import (
	"context"
	"fmt"

	"github.com/infracost/cli/internal/api"
	"github.com/infracost/cli/internal/api/dashboard"
	"github.com/infracost/cli/internal/config"
	"github.com/infracost/cli/pkg/auth"
	"github.com/infracost/cli/pkg/logging"
	"golang.org/x/oauth2"
)

// resolveOrg resolves cfg.Org (slug or ID from the --org flag) into cfg.OrgID.
// If --org is not set, this is a no-op. If the user cache is missing, it fetches
// and caches the current user using the provided token source.
func resolveOrg(ctx context.Context, cfg *config.Config, source oauth2.TokenSource) error {
	if cfg.Org == "" {
		return nil
	}

	uc, err := cfg.Auth.LoadUserCache()
	if err != nil {
		logging.WithError(err).Msg("failed to load user cache, fetching fresh data")
		uc = nil
	}

	if uc == nil || len(uc.Organizations) == 0 || uc.IsStale() {
		// No cached user data or stale — fetch it. Use empty org ID since
		// we're just calling CurrentUser which doesn't require one.
		client := cfg.Dashboard.Client(api.Client(ctx, source, ""))
		fresh, fetchErr := fetchAndCacheUser(ctx, cfg, client)
		if fetchErr != nil {
			// If we have stale data, use it rather than failing.
			if uc != nil && len(uc.Organizations) > 0 {
				logging.WithError(fetchErr).Msg("failed to refresh user cache, using stale data")
			} else {
				return fmt.Errorf("fetching user data: %w", fetchErr)
			}
		} else {
			uc = fresh
		}
	}

	orgID, _, err := auth.ResolveOrgID(cfg.Org, uc.Organizations)
	if err != nil {
		return err
	}

	cfg.OrgID = orgID
	return nil
}

func fetchAndCacheUser(ctx context.Context, cfg *config.Config, client dashboard.Client) (*auth.UserCache, error) {
	user, err := client.CurrentUser(ctx)
	if err != nil {
		return nil, err
	}

	orgs := make([]auth.CachedOrganization, len(user.Organizations))
	for i, org := range user.Organizations {
		orgs[i] = auth.CachedOrganization{
			ID:   org.ID,
			Name: org.Name,
			Slug: org.Slug,
		}
	}

	uc := &auth.UserCache{
		ID:            user.ID,
		Name:          user.Name,
		Email:         user.Email,
		Organizations: orgs,
	}

	if err := cfg.Auth.SaveUserCache(uc); err != nil {
		logging.WithError(err).Msg("failed to save user cache")
	}

	return uc, nil
}
