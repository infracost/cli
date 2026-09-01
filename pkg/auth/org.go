package auth

import (
	"errors"
	"fmt"
	"strings"

	"github.com/infracost/cli/internal/textutil"
)

// ResolveOrgID resolves an org flag value (slug or ID) to an organization ID
// using the cached user data. Returns the org ID and the org name.
func ResolveOrgID(orgFlag string, orgs []CachedOrganization) (string, string, error) {
	if orgFlag == "" {
		return "", "", fmt.Errorf("--org was passed an empty value — set INFRACOST_CLI_ORG or pass '--org <your-org-slug>' explicitly")
	}

	// Try exact match on slug or ID.
	for _, org := range orgs {
		if strings.EqualFold(org.Slug, orgFlag) || org.ID == orgFlag {
			return org.ID, org.Name, nil
		}
	}

	// No match — build error with suggestions.
	return "", "", orgNotFoundError(orgFlag, orgs)
}

func orgNotFoundError(orgFlag string, orgs []CachedOrganization) error {
	var b strings.Builder
	fmt.Fprintf(&b, "'%s' is not an organization you have access to.\n\nYour organizations:\n", orgFlag)

	bestSlug := ""
	bestDist := -1

	for _, org := range orgs {
		fmt.Fprintf(&b, "  %-20s\n", org.Slug)
		dist := textutil.LevenshteinDistance(strings.ToLower(orgFlag), strings.ToLower(org.Slug))
		if bestDist < 0 || dist < bestDist {
			bestDist = dist
			bestSlug = org.Slug
		}
	}

	// Suggest if the best match is reasonably close (within half the length of the input).
	if bestSlug != "" && bestDist <= max(len(orgFlag)/2, 3) {
		fmt.Fprintf(&b, "\nDid you mean '%s'? Run with --org %s to retry.", bestSlug, bestSlug)
	}

	return errors.New(b.String())
}
