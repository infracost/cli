package cmds

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/infracost/cli/internal/api"
	"github.com/infracost/cli/internal/api/dashboard"
	"github.com/infracost/cli/internal/config"
	"github.com/infracost/cli/internal/format/toon"
	"github.com/infracost/cli/internal/ui"
	"github.com/spf13/cobra"
)

// WhoAmIInput is the parsed flag set for `whoami`. Currently empty; kept as
// a named type so the pure-function signature matches the project-wide
// `func X(ctx, cfg, Input) (Output, error)` pattern.
type WhoAmIInput struct{}

// WhoAmIResult is the typed result of `whoami`. JSON-marshalable; the same
// shape is reused by the MCP server in a follow-up.
type WhoAmIResult struct {
	Name              string      `json:"name"`
	Email             string      `json:"email"`
	Organizations     []WhoAmIOrg `json:"organizations"`
	SelectedOrgSlug   string      `json:"selected_org_slug,omitempty"`
	SelectedOrgSource string      `json:"selected_org_source,omitempty"` // "flag" | "repo" | "global"
}

// WhoAmIOrg is one organization the user belongs to.
type WhoAmIOrg struct {
	Slug string `json:"slug"`
	Role string `json:"role"` // "owner" | "member"
}

// WhoAmI fetches the current user and selected-org context and returns the
// typed result. The cobra command and the MCP server are both thin wrappers
// over this function.
func WhoAmI(ctx context.Context, cfg *config.Config, _ WhoAmIInput) (WhoAmIResult, error) {
	var zero WhoAmIResult
	source, err := cfg.Auth.Token(ctx)
	if err != nil {
		return zero, fmt.Errorf("authenticating: %w", err)
	}

	client := cfg.Dashboard.Client(api.Client(ctx, source, cfg.OrgID))
	user, err := client.CurrentUser(ctx)
	if err != nil {
		return zero, fmt.Errorf("fetching current user: %w", err)
	}

	cached := cacheUser(cfg, user)
	currentSlug, _, orgSrc := currentOrgSlug(cfg, cached.Organizations, cached.SelectedOrgID)

	orgs := make([]WhoAmIOrg, 0, len(user.Organizations))
	for _, org := range user.Organizations {
		orgs = append(orgs, WhoAmIOrg{Slug: org.Slug, Role: whoamiRole(org)})
	}

	return WhoAmIResult{
		Name:              user.Name,
		Email:             user.Email,
		Organizations:     orgs,
		SelectedOrgSlug:   currentSlug,
		SelectedOrgSource: orgSourceLabel(orgSrc),
	}, nil
}

func whoamiRole(org dashboard.Organization) string {
	for _, r := range org.Roles {
		if r.ID == "organization_owner" {
			return "owner"
		}
	}
	return "member"
}

// WhoAmICmd builds the cobra command. It parses flags into a WhoAmIInput,
// calls the pure WhoAmI function, then dispatches the result to one of the
// renderers based on --json / --llm.
func WhoAmICmd(cfg *config.Config) *cobra.Command {
	return &cobra.Command{
		Use:   "whoami",
		Short: "Show the current user",
		RunE: func(cmd *cobra.Command, _ []string) error {
			result, err := WhoAmI(cmd.Context(), cfg, WhoAmIInput{})
			if err != nil {
				return err
			}
			return writeStructured(cfg, os.Stdout, result, whoamiRenderers())
		},
	}
}

func whoamiRenderers() Renderers[WhoAmIResult] {
	return Renderers[WhoAmIResult]{
		Human: renderWhoAmIHuman,
		JSON:  renderWhoAmIJSON,
		LLM:   renderWhoAmILLM,
	}
}

func renderWhoAmIHuman(w io.Writer, r WhoAmIResult) error {
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintf(w, "  %s  %s\n", ui.Muted("Name:"), r.Name)
	_, _ = fmt.Fprintf(w, "  %s %s\n", ui.Muted("Email:"), r.Email)

	_, _ = fmt.Fprintln(w)
	ui.Heading("Organizations")
	_, _ = fmt.Fprintln(w)
	for _, org := range r.Organizations {
		var marker, suffix string
		if strings.EqualFold(org.Slug, r.SelectedOrgSlug) {
			marker = "  " + ui.Positive("✔") + "  "
			switch r.SelectedOrgSource {
			case "repo":
				suffix = "  " + ui.Muted("← set for this repo")
			case "flag":
				suffix = "  " + ui.Muted("← --org flag")
			case "global":
				suffix = "  " + ui.Muted("← active")
			}
		} else {
			marker = "  -  "
		}
		_, _ = fmt.Fprintf(w, "%s%s %s%s\n", marker, ui.Accent(org.Slug), ui.Mutedf("(%s)", org.Role), suffix)
	}

	if r.SelectedOrgSlug == "" && len(r.Organizations) > 1 {
		_, _ = fmt.Fprintln(w)
		ui.Warn("No organization selected. Subsequent commands will fail with 'no organization selected'.")
		ui.Hint(2, "Pick one with one of:")
		ui.Hint(4, "pass --org <slug> on each command")
		ui.Hint(4, "set INFRACOST_CLI_ORG=<slug> in the environment")
		ui.Hint(4, "run 'infracost org switch <slug>' to save it globally")
		ui.Hint(4, "run 'infracost org switch <slug> --repo' to pin it to this repository")
	}
	return nil
}

func renderWhoAmIJSON(w io.Writer, r WhoAmIResult) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

func renderWhoAmILLM(w io.Writer, r WhoAmIResult) error {
	if err := toon.MarshalTo(w, r); err != nil {
		return err
	}
	_, err := fmt.Fprintln(w)
	return err
}