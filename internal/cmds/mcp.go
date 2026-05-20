package cmds

import (
	"context"
	"fmt"

	"github.com/infracost/cli/internal/api/events"
	"github.com/infracost/cli/internal/cache"
	"github.com/infracost/cli/internal/config"
	"github.com/infracost/cli/pkg/auth"
	"github.com/infracost/cli/version"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"
	"golang.org/x/oauth2"
)

// MCPCmd builds the `infracost mcp` cobra command — a Model Context
// Protocol stdio server. Authentication and the active organization are
// resolved once at startup and cached for the life of the session; tools
// (registered in registerMCPTools) read those shared values rather than
// re-resolving on every call.
func MCPCmd(cfg *config.Config) *cobra.Command {
	return &cobra.Command{
		Use:   "mcp",
		Short: "Run as a Model Context Protocol stdio server",
		Long: `Run as a Model Context Protocol (MCP) stdio server.

Exposes the same operations as the top-level CLI commands as MCP tools,
backed by the same Go functions. Intended to be launched by an MCP-aware
client (an IDE assistant, for example) which communicates over
stdin/stdout.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// Resolve the active organization up front. MCP clients
			// aren't interactive, so falling through to a per-tool
			// "no organization selected" error would surface mid-
			// session; failing at startup gives the LLM a single,
			// actionable signal to relay back to the user.
			source, err := cfg.Auth.Token(cmd.Context())
			if err != nil {
				return fmt.Errorf("infracost mcp cannot start: authenticating: %w", err)
			}
			if err := resolveOrg(cmd.Context(), cfg, source); err != nil {
				return fmt.Errorf("infracost mcp cannot start: %w", err)
			}

			srv := mcp.NewServer(&mcp.Implementation{
				Name:    "infracost",
				Version: version.Version,
			}, nil)

			// Capture the post-startup telemetry baseline so every
			// request gets the same starting state. Middlewares below
			// restore this baseline before each request and then
			// label per-tool calls so events emitted while serving a
			// tool can be correlated to it (e.g. command=mcp-scan).
			baseline := events.Snapshot()
			srv.AddReceivingMiddleware(
				resetMetadataMiddleware(baseline),
				labelMCPCommandMiddleware(),
			)

			store := cache.NewMemoryStore()
			registerMCPTools(srv, cfg, source, store)

			return srv.Run(cmd.Context(), &mcp.StdioTransport{})
		},
	}
}

// resetMetadataMiddleware restores the supplied telemetry-metadata
// baseline before every incoming MCP request. Pairs with
// [events.Snapshot] / [events.Restore] so per-request mutations
// (orgId, repoId, command label, …) don't leak between tool calls in a
// long-running MCP session.
func resetMetadataMiddleware(baseline map[string]interface{}) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			events.Restore(baseline)
			return next(ctx, method, req)
		}
	}
}

// labelMCPCommandMiddleware tags every `tools/call` request with a
// `command=mcp-<toolname>` metadata entry so telemetry emitted while
// serving the tool is attributable to it (mirrors the per-command
// label the cobra CLI sets in PersistentPreRun).
func labelMCPCommandMiddleware() mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method == "tools/call" {
				if params, ok := req.GetParams().(*mcp.CallToolParamsRaw); ok && params.Name != "" {
					events.RegisterMetadata("command", "mcp-"+params.Name)
				}
			}
			return next(ctx, method, req)
		}
	}
}

// registerMCPTools attaches infracost's MCP tools to srv. Each tool is a
// thin wrapper over the pure Go function the matching CLI command also
// uses, so MCP output and CLI --json output stay in lockstep. The auth
// source and cache store are pre-prepared at startup and threaded into
// every handler so individual tools don't need to re-derive them.
//
// fetch_orgs and set_org let an agent introspect and change the active
// organization mid-session. Domain tools (scan, price, inspect_*, etc.)
// are added by the typed-result refactor PRs.
func registerMCPTools(srv *mcp.Server, cfg *config.Config, source oauth2.TokenSource, store cache.Store) {
	mcp.AddTool(srv,
		&mcp.Tool{
			Name:        "fetch_orgs",
			Description: "List the Infracost organizations the authenticated user belongs to. Marks the org the MCP session is currently operating against.",
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ FetchOrgsInput) (*mcp.CallToolResult, FetchOrgsResult, error) {
			result, err := fetchOrgs(ctx, cfg, source)
			if err != nil {
				return nil, FetchOrgsResult{}, err
			}
			return nil, result, nil
		})

	mcp.AddTool(srv,
		&mcp.Tool{
			Name:        "set_org",
			Description: "Change the active Infracost organization for this MCP session. Subsequent tool calls run against the new org; the selection is also persisted to the user cache so it sticks across sessions.",
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, in SetOrgInput) (*mcp.CallToolResult, SetOrgResult, error) {
			result, err := setOrg(ctx, cfg, source, in)
			if err != nil {
				return nil, SetOrgResult{}, err
			}
			return nil, result, nil
		})

	mcp.AddTool(srv,
		&mcp.Tool{
			Name:        "whoami",
			Description: "Show the authenticated Infracost user and the organization the MCP server is operating against.",
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, in WhoAmIInput) (*mcp.CallToolResult, WhoAmIResult, error) {
			result, err := WhoAmI(ctx, cfg, in)
			if err != nil {
				return nil, WhoAmIResult{}, err
			}
			return nil, result, nil
		})

	scanSchema, err := scanToolOutputSchema()
	if err != nil {
		panic(err)
	}
	mcp.AddTool(srv,
		&mcp.Tool{
			Name: "scan",
			Description: "Scan an Infrastructure-as-Code directory for monthly cost, FinOps and tagging policy violations, " +
				"triggered guardrails, over-budget items, and parse diagnostics. Returns the same headline summary the " +
				"`infracost scan` CLI prints to a human (no per-resource detail). For drill-in detail use the policies, " +
				"guardrails, budgets, or inspect_* tools.",
			OutputSchema: scanSchema,
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, in ScanInput) (*mcp.CallToolResult, MCPScanOutput, error) {
			if in.Currency == "" {
				in.Currency = cfg.Currency
			}
			result, err := Scan(ctx, cfg, source, store, in, "mcp")
			if err != nil {
				return nil, MCPScanOutput{}, err
			}
			return nil, toMCPScanOutput(result), nil
		})

	priceSchema, err := priceToolOutputSchema()
	if err != nil {
		panic(err)
	}
	mcp.AddTool(srv,
		&mcp.Tool{
			Name: "price",
			Description: "Price a snippet of Infrastructure-as-Code without writing it to disk. The agent passes the raw " +
				"Terraform source in the `iac` field and gets back monthly cost plus a per-resource breakdown for the " +
				"resources it just sent. Use this when you have a small piece of IaC in hand and want to know what it " +
				"would cost; for whole-directory scans use the `scan` tool instead.",
			OutputSchema: priceSchema,
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, in PriceInput) (*mcp.CallToolResult, MCPPriceOutput, error) {
			if in.Currency == "" {
				in.Currency = cfg.Currency
			}
			result, err := Price(ctx, cfg, source, store, in, "mcp")
			if err != nil {
				return nil, MCPPriceOutput{}, err
			}
			return nil, toMCPPriceOutput(result), nil
		})

	policiesSchema, err := policiesToolOutputSchema()
	if err != nil {
		panic(err)
	}
	mcp.AddTool(srv,
		&mcp.Tool{
			Name: "policies",
			Description: "List the FinOps and tagging policies the active organization has configured for a given directory's " +
				"VCS branch / project. Returns enough detail per policy (id, name, description, scope filters, tagging " +
				"requirements) for the agent to decide which to drill into via the inspect tools. Pass finops_only or " +
				"tagging_only to narrow the result; pass providers to limit FinOps policy lookup to specific cloud providers.",
			OutputSchema: policiesSchema,
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, in PoliciesInput) (*mcp.CallToolResult, PoliciesResult, error) {
			result, err := Policies(ctx, cfg, source, in)
			if err != nil {
				return nil, PoliciesResult{}, err
			}
			return nil, result, nil
		})

	budgetsSchema, err := budgetsToolOutputSchema()
	if err != nil {
		panic(err)
	}
	mcp.AddTool(srv,
		&mcp.Tool{
			Name: "budgets",
			Description: "List the cost budgets configured for the active organization. Each entry includes the budget's id, " +
				"name, amount, current spend, over-budget flag, period (started_at / ended_at, YYYY-MM-DD), the tag selector " +
				"that scopes which resources count against it, and whether PR-comment alerts are enabled. Org-wide — not " +
				"scoped by directory or branch.",
			OutputSchema: budgetsSchema,
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, in BudgetsInput) (*mcp.CallToolResult, BudgetsResult, error) {
			result, err := Budgets(ctx, cfg, source, in)
			if err != nil {
				return nil, BudgetsResult{}, err
			}
			return nil, result, nil
		})
}

// FetchOrgsInput is the input shape for fetch_orgs. Currently empty —
// the tool surfaces every org on the authenticated user, not a query.
type FetchOrgsInput struct{}

// FetchOrgsResult lists the organizations the authenticated user
// belongs to and identifies which one the MCP session is operating
// against.
type FetchOrgsResult struct {
	Orgs         []OrgInfo `json:"orgs"`
	SelectedSlug string    `json:"selected_slug,omitempty"`
	// SelectedSource records how the active org was chosen — "flag",
	// "repo", "global", or "" when no org is set. Same values as the
	// CLI's whoami output.
	SelectedSource string `json:"selected_source,omitempty"`
}

// OrgInfo is one entry inside [FetchOrgsResult.Orgs].
type OrgInfo struct {
	ID   string `json:"id"`
	Slug string `json:"slug"`
	Name string `json:"name"`
	Role string `json:"role"` // "owner" | "member"
}

// SetOrgInput selects an organization by its slug.
type SetOrgInput struct {
	Slug string `json:"slug" jsonschema:"Organization slug to switch to. Must match one of the slugs returned by fetch_orgs."`
}

// SetOrgResult describes the org the MCP session is now operating
// against.
type SetOrgResult struct {
	ID   string `json:"id"`
	Slug string `json:"slug"`
	Name string `json:"name"`
}

func fetchOrgs(ctx context.Context, cfg *config.Config, source oauth2.TokenSource) (FetchOrgsResult, error) {
	uc, err := ensureUserCache(ctx, cfg, source)
	if err != nil {
		return FetchOrgsResult{}, err
	}
	out := FetchOrgsResult{Orgs: []OrgInfo{}}
	if uc == nil {
		return out, nil
	}
	slug, _, src := currentOrgSlug(cfg, uc.Organizations, uc.SelectedOrgID)
	out.SelectedSlug = slug
	out.SelectedSource = orgSourceLabel(src)
	for _, org := range uc.Organizations {
		out.Orgs = append(out.Orgs, OrgInfo{
			ID:   org.ID,
			Slug: org.Slug,
			Name: org.Name,
			Role: cachedRole(org),
		})
	}
	return out, nil
}

func setOrg(ctx context.Context, cfg *config.Config, source oauth2.TokenSource, in SetOrgInput) (SetOrgResult, error) {
	if in.Slug == "" {
		return SetOrgResult{}, fmt.Errorf("slug is required")
	}
	uc, err := ensureUserCache(ctx, cfg, source)
	if err != nil {
		return SetOrgResult{}, err
	}
	if uc == nil || len(uc.Organizations) == 0 {
		return SetOrgResult{}, fmt.Errorf("no organizations available for the authenticated user")
	}
	orgID, name, err := auth.ResolveOrgID(in.Slug, uc.Organizations)
	if err != nil {
		return SetOrgResult{}, err
	}

	// Update the in-memory cfg so subsequent tool calls in this session
	// operate against the new org without going through the whole
	// resolveOrg pipeline again.
	cfg.Org = in.Slug
	cfg.OrgID = orgID

	// Persist so the next MCP session (or CLI invocation) starts on
	// the same org, mirroring what `infracost org switch <slug>` does.
	uc.SelectedOrgID = orgID
	if err := cfg.Auth.SaveUserCache(uc); err != nil {
		return SetOrgResult{}, fmt.Errorf("saving org selection: %w", err)
	}
	return SetOrgResult{ID: orgID, Slug: in.Slug, Name: name}, nil
}

// orgSourceLabel maps an internal orgSource enum to the string label
// used in MCP output. Mirrors the CLI's whoami "← active" annotations.
func orgSourceLabel(s orgSource) string {
	switch s {
	case orgSourceFlag:
		return "flag"
	case orgSourceRepo:
		return "repo"
	case orgSourceGlobal:
		return "global"
	default:
		return ""
	}
}

// cachedRole picks the most-elevated known role for a cached org so the
// MCP output is a single string rather than the raw role-list.
func cachedRole(org auth.CachedOrganization) string {
	for _, r := range org.Roles {
		if r == "organization_owner" {
			return "owner"
		}
	}
	return "member"
}