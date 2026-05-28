package cmds

import (
	"fmt"

	"github.com/infracost/cli/internal/config"
)

// ensureAgentsEnabled gates the Agents-backed surfaces (findings / tasks /
// actions, and their MCP tool equivalents) on the active organization's
// agentsEnabled flag. The dashboard derives that flag from the coast-access
// entitlement and returns it on the currentUser query; resolveOrg (and setOrg
// for mid-session MCP org switches) resolves it onto cfg before these
// functions run.
//
// When the org isn't enabled we return a friendly, actionable error pointing
// at the Agents waitlist rather than letting the downstream Agents API reject
// the call with an opaque error — Agents is in early access, so most orgs will
// hit this path until they're switched on.
func ensureAgentsEnabled(cfg *config.Config) error {
	if cfg.AgentsEnabled {
		return nil
	}
	return errAgentsNotEnabled(cfg.OrgSlug)
}

// errAgentsNotEnabled builds the early-access message shown when the active
// org doesn't have Agents turned on. When the org slug is known it deep-links
// to that org's Agents page (which surfaces the waitlist signup); otherwise it
// falls back to the dashboard root.
func errAgentsNotEnabled(slug string) error {
	url := "https://dashboard.infracost.io"
	if slug != "" {
		url = fmt.Sprintf("https://dashboard.infracost.io/org/%s/agents", slug)
	}
	return fmt.Errorf(
		"this organization doesn't have Infracost Agents enabled yet — it's currently in early access. "+
			"Join the waitlist at %s, or contact the Infracost team to get set up",
		url,
	)
}
