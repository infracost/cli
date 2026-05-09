package orgresolve

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/infracost/cli/internal/config"
	"github.com/infracost/cli/internal/ui"
	"github.com/infracost/cli/pkg/auth"
)

// Pick prompts the user to interactively choose an organization. selectedOrgID
// pre-selects an entry when non-empty; pass "" to default to the resolution
// chain's current selection. Use DefaultPickTitle for the standard prompt.
func Pick(orgs []auth.CachedOrganization, cfg *config.Config, selectedOrgID, title string) (string, error) {
	currentSlug, _, _ := CurrentSlug(cfg, orgs, selectedOrgID)

	options := make([]huh.Option[string], len(orgs))
	for i, org := range orgs {
		label := fmt.Sprintf("%-20s (%s)", org.Slug, Role(org))
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
