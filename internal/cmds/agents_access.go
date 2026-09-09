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
// When the org isn't enabled we say so up front rather than letting the
// downstream Agents API reject the call with an opaque error.
func ensureAgentsEnabled(cfg *config.Config) error {
	if cfg.AgentsEnabled {
		return nil
	}
	return errAgentsNotEnabled(cfg.OrgSlug)
}

// errAgentsNotEnabled builds the message shown when the active org has AI
// features switched off. Infracost sets that by hand for customers who can't
// send data to a model, and nothing in the dashboard exposes it — so this
// deliberately offers no link: there is no page where the caller, admin or
// not, could turn it back on.
func errAgentsNotEnabled(slug string) error {
	org := "this organization"
	if slug != "" {
		org = fmt.Sprintf("organization %q", slug)
	}
	return fmt.Errorf(
		"%s has AI features turned off, so Infracost Agents is unavailable. "+
			"Infracost manages that setting - contact the Infracost team if you think it should be on",
		org,
	)
}
