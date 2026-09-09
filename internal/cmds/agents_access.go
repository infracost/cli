package cmds

import (
	"fmt"

	"github.com/infracost/cli/internal/config"
)

// ensureAgentsEnabled gates the Agents-backed surfaces (findings / tasks /
// actions, and their MCP tool equivalents) on the active organization's
// agentsEnabled flag, an alias of the dashboard's org-level aiEnabled switch.
// resolveOrg (and setOrg for mid-session MCP org switches) resolves it onto
// cfg before these functions run.
//
// When the org isn't enabled we return a friendly, actionable error naming the
// org's settings rather than letting the downstream Agents API reject the call
// with an opaque error.
func ensureAgentsEnabled(cfg *config.Config) error {
	if cfg.AgentsEnabled {
		return nil
	}
	return errAgentsNotEnabled(cfg.OrgSlug)
}

// errAgentsNotEnabled builds the message shown when the active org has AI
// features switched off. When the org slug is known it deep-links to that
// org's settings, where an admin can turn them back on; otherwise it falls
// back to the dashboard root.
func errAgentsNotEnabled(slug string) error {
	url := "https://dashboard.infracost.io"
	if slug != "" {
		url = fmt.Sprintf("https://dashboard.infracost.io/org/%s/settings", slug)
	}
	return fmt.Errorf(
		"this organization has AI features turned off, so Infracost Agents is unavailable. "+
			"An org admin can re-enable them at %s, or contact the Infracost team",
		url,
	)
}
