package cmds

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/infracost/cli/internal/api"
	"github.com/infracost/cli/internal/api/events"
	"github.com/infracost/cli/internal/config"
	"github.com/infracost/cli/internal/ui"
	"github.com/infracost/cli/internal/vcs"
	"github.com/infracost/go-proto/pkg/rat"
	"github.com/infracost/proto/gen/go/infracost/parser/event"
	"github.com/spf13/cobra"
	"golang.org/x/oauth2"
	"google.golang.org/protobuf/encoding/protojson"
)

// GuardrailsInput is the parsed input for `guardrails`. Same shape for
// the CLI wrapper and the MCP `guardrails` tool — both populate it and
// call the pure Guardrails function.
type GuardrailsInput struct {
	// Path is the directory whose VCS repository / branch determine which
	// guardrails apply (some are repo-scoped, others project-scoped).
	// Empty falls through to the current working directory.
	Path string `json:"path,omitempty" jsonschema:"Directory whose VCS repo + branch are used to resolve which guardrails apply. Optional; defaults to the MCP server's working directory."`
}

// GuardrailsResult is the typed output of `guardrails`. Used by both CLI
// `--json` / `--llm` rendering and the MCP `guardrails` tool. The
// underlying event.Guardrail proto is projected to GuardrailEntry so
// the wire format uses readable field names.
type GuardrailsResult struct {
	Guardrails []GuardrailEntry `json:"guardrails"`
}

// GuardrailEntry is one guardrail configured for the resolved repo. The
// threshold fields are pre-converted from proto rational numbers to
// rat.Rat so they serialize as number-formatted strings via the standard
// MarshalJSON path; the scope discriminator is mapped to a stable
// lowercase string ("repo" / "project"). At most one of the three
// threshold fields is set per guardrail in practice.
type GuardrailEntry struct {
	ID                       string              `json:"id"`
	Name                     string              `json:"name"`
	Message                  string              `json:"message,omitempty"`
	Scope                    string              `json:"scope"`
	TotalThreshold           *rat.Rat            `json:"total_threshold,omitempty"`
	IncreaseThreshold        *rat.Rat            `json:"increase_threshold,omitempty"`
	IncreasePercentThreshold *rat.Rat            `json:"increase_percent_threshold,omitempty"`
	PrComment                bool                `json:"pr_comment"`
	BlockPr                  bool                `json:"block_pr"`
	// ProjectFilter scopes a project-level guardrail to a subset of
	// projects. Nil for repo-level guardrails and for project-level
	// guardrails that apply to all projects.
	ProjectFilter *PolicyStringFilter `json:"project_filter,omitempty"`
}

// Guardrails resolves the target directory's repo + branch, fetches every
// guardrail the active organization has configured for them, and returns
// the typed result. Authentication and org resolution are the caller's
// responsibility — `source` and `cfg.OrgID` must be populated before
// calling Guardrails.
func Guardrails(ctx context.Context, cfg *config.Config, source oauth2.TokenSource, in GuardrailsInput) (GuardrailsResult, error) {
	var zero GuardrailsResult

	target := in.Path
	if target == "" {
		target = "."
	}
	absoluteDirectory, err := filepath.Abs(filepath.Clean(target))
	if err != nil {
		return zero, fmt.Errorf("failed to get absolute path to target: %w", err)
	}
	if info, err := os.Stat(absoluteDirectory); err != nil {
		if os.IsNotExist(err) {
			return zero, fmt.Errorf("target directory does not exist")
		}
		return zero, fmt.Errorf("failed to get info for target directory: %w", err)
	} else if !info.IsDir() {
		return zero, fmt.Errorf("target is not a directory")
	}

	repositoryURL := vcs.GetRemoteURL(absoluteDirectory)
	branchName := vcs.GetCurrentBranch(absoluteDirectory)

	client := cfg.Dashboard.Client(api.Client(ctx, source, cfg.OrgID))
	runParameters, err := client.RunParameters(ctx, repositoryURL, branchName)
	if err != nil {
		return zero, fmt.Errorf("fetching guardrails: %w", err)
	}
	if cfg.Org == "" {
		cfg.OrgID = runParameters.OrganizationID
	}

	events.RegisterMetadata("orgId", cfg.OrgID)
	events.RegisterMetadata("repoId", repositoryURL)
	events.RegisterMetadata("branchId", branchName)

	pj := protojson.UnmarshalOptions{DiscardUnknown: true}
	result := GuardrailsResult{Guardrails: []GuardrailEntry{}}
	for _, raw := range runParameters.Guardrails {
		g := new(event.Guardrail)
		if err := pj.Unmarshal(raw, g); err != nil {
			return zero, fmt.Errorf("failed to unmarshal guardrail: %w", err)
		}
		result.Guardrails = append(result.Guardrails, toGuardrailEntry(g))
	}
	return result, nil
}

// GuardrailsCmd builds the cobra command. Parses the optional path arg,
// authenticates, resolves the active org, then calls the pure Guardrails
// function under a spinner and dispatches the result.
func GuardrailsCmd(cfg *config.Config) *cobra.Command {
	var in GuardrailsInput
	cmd := &cobra.Command{
		Use:   "guardrails [path]",
		Short: "List cost guardrails for the current repository",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				in.Path = args[0]
			}

			source, err := cfg.Auth.Token(cmd.Context())
			if err != nil {
				return fmt.Errorf("authenticating: %w", err)
			}
			if err := resolveOrg(cmd.Context(), cfg, source); err != nil {
				return err
			}

			var result GuardrailsResult
			if err := ui.RunWithSpinnerErr(cmd.Context(), "Fetching guardrails...", "Guardrails loaded", func(ctx context.Context) error {
				var gErr error
				result, gErr = Guardrails(ctx, cfg, source, in)
				return gErr
			}); err != nil {
				return err
			}
			return writeStructured(cfg, os.Stdout, result, guardrailsRenderers())
		},
	}
	return cmd
}

func guardrailsRenderers() Renderers[GuardrailsResult] {
	return Renderers[GuardrailsResult]{
		Human: renderGuardrailsHuman,
		JSON:  renderGuardrailsJSON,
		LLM:   renderGuardrailsLLM,
	}
}

func renderGuardrailsHuman(w io.Writer, r GuardrailsResult) error {
	_, _ = fmt.Fprintln(w)

	if len(r.Guardrails) == 0 {
		_, _ = fmt.Fprintln(w, ui.Muted("No guardrails configured for this repository."))
		_, _ = fmt.Fprintln(w)
		return nil
	}

	ui.Heading("Cost Guardrails")
	_, _ = fmt.Fprintln(w)
	for _, g := range r.Guardrails {
		writeGuardrailEntry(w, g)
	}
	return nil
}

// writeGuardrailEntry mirrors the previous printGuardrail layout — name +
// id + scope, optional custom message, the configured thresholds, the
// list of actions, and the project filter when scope is "project".
func writeGuardrailEntry(w io.Writer, g GuardrailEntry) {
	_, _ = fmt.Fprintf(w, "%s  %s\n", ui.Bold(ui.Accent(g.Name)), ui.Mutedf("(%s, %s-level)", g.ID, g.Scope))

	if g.Message != "" {
		_, _ = fmt.Fprintf(w, "  %s\n", g.Message)
	}

	_, _ = fmt.Fprintf(w, "\n  %s\n", ui.Bold(ui.Muted("Thresholds")))
	if g.TotalThreshold != nil {
		_, _ = fmt.Fprintf(w, "    - Total monthly cost exceeds %s\n", ui.Cautionf("$%s", formatRat(g.TotalThreshold)))
	}
	if g.IncreaseThreshold != nil {
		_, _ = fmt.Fprintf(w, "    - Cost increase exceeds %s\n", ui.Cautionf("$%s", formatRat(g.IncreaseThreshold)))
	}
	if g.IncreasePercentThreshold != nil {
		_, _ = fmt.Fprintf(w, "    - Cost increase exceeds %s\n", ui.Cautionf("%s%%", formatRat(g.IncreasePercentThreshold)))
	}

	_, _ = fmt.Fprintf(w, "\n  %s\n", ui.Bold(ui.Muted("Actions")))
	var actions []string
	if g.PrComment {
		actions = append(actions, "PR comment")
	}
	if g.BlockPr {
		actions = append(actions, "Block PR")
	}
	if len(actions) == 0 {
		actions = append(actions, "Alert only")
	}
	_, _ = fmt.Fprintf(w, "    %s\n", strings.Join(actions, ", "))

	if g.Scope == "project" && g.ProjectFilter != nil {
		_, _ = fmt.Fprintf(w, "\n  %s\n", ui.Bold(ui.Muted("Applies to")))
		_, _ = fmt.Fprintf(w, "    - %s\n", stringFilterHuman("projects", g.ProjectFilter))
	}

	_, _ = fmt.Fprintln(w)
}

func renderGuardrailsJSON(w io.Writer, r GuardrailsResult) error {
	body, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to write JSON output: %w", err)
	}
	_, err = fmt.Fprintln(w, string(body))
	return err
}

func renderGuardrailsLLM(w io.Writer, r GuardrailsResult) error {
	// Same shape as JSON — small list, no dedupable tabular payload.
	return renderGuardrailsJSON(w, r)
}

// toGuardrailEntry projects a raw event.Guardrail proto to the clean
// MCP/JSON shape. Threshold rationals go through rat.FromProto so the
// wire serialization uses rat.Rat.MarshalJSON; the scope enum is
// reduced to a stable string discriminator.
func toGuardrailEntry(g *event.Guardrail) GuardrailEntry {
	entry := GuardrailEntry{
		ID:                       g.Id,
		Name:                     g.Name,
		Message:                  g.Message,
		Scope:                    guardrailScopeString(g.Scope),
		TotalThreshold:           rat.FromProto(g.TotalThreshold),
		IncreaseThreshold:        rat.FromProto(g.IncreaseThreshold),
		IncreasePercentThreshold: rat.FromProto(g.IncreasePercentThreshold),
		PrComment:                g.PrComment,
		BlockPr:                  g.BlockPr,
	}
	if g.Scope == event.Guardrail_PROJECT {
		entry.ProjectFilter = stringFilterFromProto(g.ProjectFilter)
	}
	return entry
}

// guardrailScopeString maps the proto enum to the lowercase
// discriminator the MCP wire format and the human renderer share.
func guardrailScopeString(s event.Guardrail_Scope) string {
	if s == event.Guardrail_PROJECT {
		return "project"
	}
	return "repo"
}