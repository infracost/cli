// Package orgresolve resolves which Infracost organization is active for a
// given command invocation. The resolution chain (--org flag → .infracost/org
// → user-cache selection → single-org auto-select → interactive pick) is
// shared between the CLI commands and the TUI.
package orgresolve

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
	"golang.org/x/oauth2"
)

// DefaultPickTitle is the title used by Pick when none is provided.
const DefaultPickTitle = "Which organization do you want to use?"

// Source identifies how the active org slug was determined.
type Source int

const (
	SourceNone   Source = iota
	SourceFlag          // --org flag or INFRACOST_CLI_ORG env var
	SourceRepo          // .infracost/org file in working directory
	SourceGlobal        // SelectedOrgID in user cache (from org switch)
)

// Resolve resolves the active organization into cfg.OrgID using the
// following priority chain:
//  1. --org flag / INFRACOST_CLI_ORG env var
//  2. .infracost/org file in the working directory
//  3. SelectedOrgID saved in the user cache (from `infracost org switch`)
//
// If no org context is found and the user belongs to exactly one org, it is
// used automatically. If they belong to multiple orgs, they are prompted to
// pick one (TTY only); on non-TTY a structured error is returned.
func Resolve(ctx context.Context, cfg *config.Config, source oauth2.TokenSource) error {
	uc, err := EnsureUserCache(ctx, cfg, source)
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

	// Multiple orgs, no selection — prompt in TTY, error otherwise.
	if ui.IsInteractive() {
		slug, pickErr := Pick(uc.Organizations, cfg, "", DefaultPickTitle)
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
		}
		if pickErr != nil && !errors.Is(pickErr, huh.ErrUserAborted) {
			return fmt.Errorf("selecting organization: %w", pickErr)
		}
	}

	return ErrNoOrgSelected(uc.Organizations)
}

// ErrNoOrgSelected returns an actionable error for the multi-org +
// non-interactive case. Listing the slugs inline lets the caller (or an
// agent harness) pick a value to pass back via --org without having to
// run a second command.
func ErrNoOrgSelected(orgs []auth.CachedOrganization) error {
	slugs := make([]string, 0, len(orgs))
	for _, o := range orgs {
		slugs = append(slugs, o.Slug)
	}
	return fmt.Errorf(
		"no organization selected — you belong to %d (%s). Pick one with one of:\n"+
			"  • pass --org <slug>\n"+
			"  • set INFRACOST_CLI_ORG=<slug>\n"+
			"  • run 'infracost org switch <slug>' to save it globally\n"+
			"  • run 'infracost org switch <slug> --repo' to save it for this repo only",
		len(orgs), strings.Join(slugs, ", "),
	)
}

// EnsureUserCache loads the user cache, refreshing from the API if stale or missing.
func EnsureUserCache(ctx context.Context, cfg *config.Config, source oauth2.TokenSource) (*auth.UserCache, error) {
	uc, err := cfg.Auth.LoadUserCache()
	if err != nil {
		logging.WithError(err).Msg("failed to load user cache, fetching fresh data")
		uc = nil
	}

	if uc == nil || len(uc.Organizations) == 0 || uc.IsStale() {
		client := cfg.Dashboard.Client(api.Client(ctx, source, ""))
		fresh, fetchErr := FetchAndCacheUser(ctx, cfg, client)
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

// FetchAndCacheUser fetches the current user from the API, persists it to the
// user cache, and returns the resulting cache snapshot.
func FetchAndCacheUser(ctx context.Context, cfg *config.Config, client dashboard.Client) (*auth.UserCache, error) {
	user, err := client.CurrentUser(ctx)
	if err != nil {
		return nil, err
	}

	return CacheUser(cfg, user), nil
}

// CacheUser converts a dashboard.CurrentUser into an auth.UserCache, preserving
// any existing org selection across cache refreshes, and persists the result.
func CacheUser(cfg *config.Config, user dashboard.CurrentUser) *auth.UserCache {
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

// CurrentSlug determines the current org slug from the resolution chain:
// --org flag/env → .infracost/org → selectedOrgID from caller.
func CurrentSlug(cfg *config.Config, orgs []auth.CachedOrganization, selectedOrgID string) (string, string, Source) {
	// 1. Explicit --org flag or INFRACOST_CLI_ORG env var.
	if cfg.Org != "" {
		_, name, err := auth.ResolveOrgID(cfg.Org, orgs)
		if err == nil {
			return cfg.Org, name, SourceFlag
		}
	}

	// 2. Local .infracost/org file.
	if wd, err := os.Getwd(); err == nil {
		if slug, err := auth.ReadLocalOrg(wd); err == nil && slug != "" {
			if _, name, err := auth.ResolveOrgID(slug, orgs); err == nil {
				return slug, name, SourceRepo
			}
		}
	}

	// 3. SelectedOrgID passed by caller.
	if selectedOrgID != "" {
		for _, org := range orgs {
			if org.ID == selectedOrgID {
				return org.Slug, org.Name, SourceGlobal
			}
		}
	}

	return "", "", SourceNone
}

// Role returns the user-facing role name for an organization based on the
// cached role flags.
func Role(org auth.CachedOrganization) string {
	if slices.Contains(org.Roles, "organization_owner") {
		return "owner"
	}
	return "member"
}
