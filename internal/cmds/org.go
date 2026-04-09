package cmds

import (
	"context"
	"fmt"

	"github.com/infracost/cli/internal/api/dashboard"
	"github.com/infracost/cli/internal/config"
	"github.com/infracost/cli/pkg/auth"
	"github.com/infracost/cli/pkg/logging"
)

// resolveOrg resolves cfg.Org (slug or ID from the --org flag) into cfg.OrgID.
// If --org is not set, this is a no-op. If the user cache is missing, it fetches
// and caches the current user via the dashboard client.
func resolveOrg(ctx context.Context, cfg *config.Config, client dashboard.Client) error {
	if cfg.Org == "" {
		return nil
	}

	uc, err := cfg.Auth.LoadUserCache()
	if err != nil {
		return fmt.Errorf("loading user cache: %w", err)
	}

	if uc == nil || len(uc.Organizations) == 0 {
		uc, err = fetchAndCacheUser(ctx, cfg, client)
		if err != nil {
			return fmt.Errorf("fetching user data: %w", err)
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
