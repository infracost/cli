package auth

import (
	"errors"
	"fmt"
	"strings"
)

// ResolveOrgID resolves an org flag value (slug or ID) to an organization ID
// using the cached user data. Returns the org ID and the org name.
func ResolveOrgID(orgFlag string, orgs []CachedOrganization) (string, string, error) {
	if orgFlag == "" {
		return "", "", fmt.Errorf("--org was passed an empty value. This usually means the environment\n       variable you're referencing isn't set in this pipeline context.\n\n       Set INFRACOST_CLI_ORG or pass --org explicitly:\n         --org <your-org-slug>")
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
		dist := levenshteinDistance(strings.ToLower(orgFlag), strings.ToLower(org.Slug))
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

// levenshteinDistance computes the edit distance between two strings.
func levenshteinDistance(a, b string) int {
	la, lb := len(a), len(b)
	if la == 0 {
		return lb
	}
	if lb == 0 {
		return la
	}

	// Single-row DP: prev holds the previous row of distances.
	prev := make([]int, lb+1)
	for j := range prev {
		prev[j] = j
	}

	for i := 1; i <= la; i++ {
		curr := make([]int, lb+1)
		curr[0] = i
		for j := 1; j <= lb; j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			curr[j] = min(
				prev[j]+1,      // deletion
				curr[j-1]+1,    // insertion
				prev[j-1]+cost, // substitution
			)
		}
		prev = curr
	}

	return prev[lb]
}
