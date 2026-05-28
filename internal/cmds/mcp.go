package cmds

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/infracost/cli/internal/api/events"
	"github.com/infracost/cli/internal/cache"
	"github.com/infracost/cli/internal/config"
	"github.com/infracost/cli/internal/format"
	"github.com/infracost/cli/internal/inspect"
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
			// Long-lived MCP sessions outlive a single oauth refresh
			// token. Another process (a CLI invocation, a web login)
			// can rotate the token while we hold a stale in-memory
			// snapshot; WrapWithReload re-reads the on-disk cache on
			// invalid_grant so the next tool call recovers
			// transparently instead of failing until the user bounces
			// the server.
			source = cfg.Auth.WrapWithReload(cmd.Context(), source)
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

	guardrailsSchema, err := guardrailsToolOutputSchema()
	if err != nil {
		panic(err)
	}
	mcp.AddTool(srv,
		&mcp.Tool{
			Name: "guardrails",
			Description: "List the cost guardrails the active organization has configured for the directory's resolved VCS " +
				"repo + branch. Each entry includes the guardrail's id, name, optional message, scope (\"repo\" or " +
				"\"project\"), thresholds (total_threshold / increase_threshold / increase_percent_threshold — at most one " +
				"per guardrail in practice), configured actions (pr_comment / block_pr), and the project filter when the " +
				"guardrail is project-scoped.",
			OutputSchema: guardrailsSchema,
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, in GuardrailsInput) (*mcp.CallToolResult, GuardrailsResult, error) {
			result, err := Guardrails(ctx, cfg, source, in)
			if err != nil {
				return nil, GuardrailsResult{}, err
			}
			return nil, result, nil
		})

	registerAgentsMCPTools(srv, cfg, source)

	registerInspectMCPTools(srv, store)

	doctorSchema, err := doctorToolOutputSchema()
	if err != nil {
		panic(err)
	}
	mcp.AddTool(srv,
		&mcp.Tool{
			Name: "doctor",
			Description: "Run the same diagnostic checks `infracost doctor` runs and return the typed report — auth, " +
				"config, agent / IDE integrations. Use this to verify the user's setup is healthy before suggesting " +
				"workflows that depend on it. The MCP variant omits the CLI's --fix flag: auto-remediation is " +
				"destructive and should be done by the user themselves via `infracost doctor --fix`.",
			OutputSchema: doctorSchema,
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, in MCPDoctorInput) (*mcp.CallToolResult, DoctorOutput, error) {
			report, err := Doctor(ctx, cfg, DoctorInput{
				Verbose:     in.Verbose,
				Bundle:      in.Bundle,
				CheckAgents: in.CheckAgents,
				CheckIDE:    in.CheckIDE,
			})
			if err != nil {
				return nil, DoctorOutput{}, err
			}
			out := DoctorOutput{Report: report}
			if in.Bundle {
				b := buildDoctorBundle(cfg)
				out.Bundle = &b
			}
			return nil, out, nil
		})
}

// registerAgentsMCPTools attaches the Agents-backed tools (findings /
// tasks / actions) to srv. The tools are always registered so an agent
// can discover them; each handler gates on the active org's agentsEnabled
// flag via ensureAgentsEnabled and returns a friendly early-access /
// waitlist message when the org isn't switched on.
func registerAgentsMCPTools(srv *mcp.Server, cfg *config.Config, source oauth2.TokenSource) {
	findingsListSchema, err := findingsListToolOutputSchema()
	if err != nil {
		panic(err)
	}
	mcp.AddTool(srv,
		&mcp.Tool{
			Name: "findings_list",
			Description: "List FinOps findings for the active organization. Each row carries id, title, summary, severity, " +
				"status, estimated monthly saving, and a task count. Pass statuses / efforts to narrow; pass cursor to page. " +
				"Use findings_get to drill into a specific finding and see its tasks.",
			OutputSchema: findingsListSchema,
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, in FindingsListInput) (*mcp.CallToolResult, FindingsListResult, error) {
			result, err := ListFindings(ctx, cfg, source, in)
			if err != nil {
				return nil, FindingsListResult{}, err
			}
			return nil, result, nil
		})

	findingsGetSchema, err := findingsGetToolOutputSchema()
	if err != nil {
		panic(err)
	}
	mcp.AddTool(srv,
		&mcp.Tool{
			Name: "findings_get",
			Description: "Fetch a single FinOps finding by id. Returns the finding header plus its nested tasks, actions, and " +
				"events — including each task's action_description and code snippet. To draft a PR or ticket for a task, " +
				"call preview_fix; to actually create it after the user confirms, call create_fix.",
			OutputSchema: findingsGetSchema,
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, in FindingsGetInput) (*mcp.CallToolResult, FindingsGetResult, error) {
			result, err := GetFinding(ctx, cfg, source, in)
			if err != nil {
				return nil, FindingsGetResult{}, err
			}
			return nil, result, nil
		})

	previewFixSchema, err := previewFixToolOutputSchema()
	if err != nil {
		panic(err)
	}
	mcp.AddTool(srv,
		&mcp.Tool{
			Name: "preview_fix",
			Description: "Ask Agents to draft a PR or ticket for a task without creating it. No side effects — the LLM produces " +
				"a {type, config} blob the agent can show to the user. To actually open the PR / create the ticket the agent " +
				"must then call create_fix with the same finding_id / task_id, the type, and the config returned here " +
				"(possibly edited by the user). type defaults to open_pr and accepts open_pr or create_ticket only — manual " +
				"actions aren't draftable by the LLM and must go through create_fix directly.",
			OutputSchema: previewFixSchema,
			Annotations: &mcp.ToolAnnotations{
				ReadOnlyHint: true,
				Title:        "Preview a fix (LLM-drafted PR/ticket)",
			},
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, in PreviewFixInput) (*mcp.CallToolResult, PreviewFixResult, error) {
			result, err := PreviewFix(ctx, cfg, source, in)
			if err != nil {
				return nil, PreviewFixResult{}, err
			}
			return nil, result, nil
		})

	createFixSchema, err := createFixToolOutputSchema()
	if err != nil {
		panic(err)
	}
	createFixInputSchema, err := createFixToolInputSchema()
	if err != nil {
		panic(err)
	}
	createFixDestructive := true
	mcp.AddTool(srv,
		&mcp.Tool{
			Name: "create_fix",
			Description: "Submit a previously-drafted fix to Agents, actually creating the PR / ticket / manual action. This is " +
				"destructive — it opens a real PR or creates a real ticket against the connected integration. The expected " +
				"flow is: call preview_fix, show the {type, config} draft to the user, get explicit confirmation (and any " +
				"edits to the config blob), then call create_fix with finding_id, task_id, type (open_pr | create_ticket | " +
				"manual), and config. Returns the new action_id.",
			InputSchema:  createFixInputSchema,
			OutputSchema: createFixSchema,
			Annotations: &mcp.ToolAnnotations{
				DestructiveHint: &createFixDestructive,
				Title:           "Create a fix (open PR / ticket)",
			},
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, in CreateFixInput) (*mcp.CallToolResult, CreateFixResult, error) {
			result, err := CreateFix(ctx, cfg, source, in)
			if err != nil {
				return nil, CreateFixResult{}, err
			}
			return nil, result, nil
		})

	updateFindingStatusSchema, err := updateFindingStatusToolOutputSchema()
	if err != nil {
		panic(err)
	}
	updateFindingStatusDestructive := true
	mcp.AddTool(srv,
		&mcp.Tool{
			Name: "update_finding_status",
			Description: "Set a finding's status: open, resolved, or dismissed. Dismissing with a non-empty reason " +
				"emits an AgentLearning so the same finding isn't re-raised on future scans. Use this to close the " +
				"loop after a finding has been fixed (resolved) or determined to be a false positive (dismissed). " +
				"Returns the updated finding.",
			OutputSchema: updateFindingStatusSchema,
			Annotations: &mcp.ToolAnnotations{
				DestructiveHint: &updateFindingStatusDestructive,
				Title:           "Update a finding's status",
			},
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, in UpdateFindingStatusInput) (*mcp.CallToolResult, UpdateFindingStatusResult, error) {
			result, err := UpdateFindingStatus(ctx, cfg, source, in)
			if err != nil {
				return nil, UpdateFindingStatusResult{}, err
			}
			return nil, result, nil
		})

	updateTaskStatusSchema, err := updateTaskStatusToolOutputSchema()
	if err != nil {
		panic(err)
	}
	updateTaskStatusDestructive := true
	mcp.AddTool(srv,
		&mcp.Tool{
			Name: "update_task_status",
			Description: "Update a task's resolution. status='confirm' says \"I did Agents' suggested change\" — " +
				"advances every linked open manual action to 'done' so the verification cascade can pick them up. " +
				"status='correct' says \"I solved the problem a different way\" — dismisses the task with reason as " +
				"the dismissed_reason, cascade-dismisses any draft/open actions linked to it, and emits an " +
				"AgentLearning(source=correction). status='dismiss' says \"we're not going to do this task\" — " +
				"dismisses the task with reason as the dismissed_reason and emits an " +
				"AgentLearning(source=task_dismissed). Reason is required for correct, recommended for dismiss " +
				"(needed to emit the learning), and ignored for confirm. Pick correct vs dismiss based on what the " +
				"user actually did: did they take an alternative action (correct), or are they declining the task " +
				"outright (dismiss)? The two values feed different learning signals to the agent.",
			OutputSchema: updateTaskStatusSchema,
			Annotations: &mcp.ToolAnnotations{
				DestructiveHint: &updateTaskStatusDestructive,
				Title:           "Confirm / correct / dismiss a task",
			},
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, in UpdateTaskStatusInput) (*mcp.CallToolResult, UpdateTaskStatusResult, error) {
			result, err := UpdateTaskStatus(ctx, cfg, source, in)
			if err != nil {
				return nil, UpdateTaskStatusResult{}, err
			}
			return nil, result, nil
		})

	retryActionSchema, err := retryActionToolOutputSchema()
	if err != nil {
		panic(err)
	}
	retryActionDestructive := true
	mcp.AddTool(srv,
		&mcp.Tool{
			Name: "retry_action",
			Description: "Re-queue a failed action (PR / ticket creation that errored on the worker) for another " +
				"attempt. The action must currently be in the 'failed' state — read action_status from the parent " +
				"finding before calling. Returns {ok: true} on accept.",
			OutputSchema: retryActionSchema,
			Annotations: &mcp.ToolAnnotations{
				DestructiveHint: &retryActionDestructive,
				Title:           "Retry a failed action",
			},
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, in RetryActionInput) (*mcp.CallToolResult, RetryActionResult, error) {
			result, err := RetryAction(ctx, cfg, source, in)
			if err != nil {
				return nil, RetryActionResult{}, err
			}
			return nil, result, nil
		})
}

// inspectScanData loads a cached scan result for the active MCP session.
// When path is empty it returns the most recent scan in the session's
// MemoryStore; when set it resolves the path to an absolute form and
// looks up the matching entry. The empty default supports the common
// "scan, then inspect" flow; passing a path lets an agent that ran
// multiple scans target a specific one. Returns a clear, actionable
// error when no matching scan is cached so the LLM can relay "call
// scan first" back to the user.
func inspectScanData(store cache.Store, path string) (*format.Output, error) {
	if path == "" {
		data, err := store.Latest(true)
		if err != nil || data == nil {
			return nil, fmt.Errorf("no scan results available in this MCP session — call the `scan` tool first")
		}
		return data, nil
	}
	absPath, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("invalid path %q: %w", path, err)
	}
	data, err := store.ForPathAllowStale(absPath)
	if err != nil || data == nil {
		return nil, fmt.Errorf("no scan results cached for %s — call the `scan` tool against that directory first", absPath)
	}
	return data, nil
}

// registerInspectMCPTools attaches the inspect_* MCP tools to srv. Each
// is a thin wrapper over a pure function in internal/inspect; the wrapper
// loads the latest scan from the session's cache (failing fast if none),
// translates the input struct into inspect.Options, and returns the
// typed result.
func registerInspectMCPTools(srv *mcp.Server, store cache.Store) {
	summarySchema, err := inspectSummaryToolOutputSchema()
	if err != nil {
		panic(err)
	}
	mcp.AddTool(srv,
		&mcp.Tool{
			Name: "inspect_summary",
			Description: "Summarize the latest scan: headline counts (resources, costed/free, monthly cost), per-policy and " +
				"per-domain failing counts, diagnostic counts, the per-project breakdown, total potential monthly savings, " +
				"and drill-in lists for every failing policy, triggered guardrail, and over-budget item. Use this as the " +
				"first call after a scan to triage what needs attention — no follow-up tools required for the headline view.",
			OutputSchema: summarySchema,
		},
		func(_ context.Context, _ *mcp.CallToolRequest, in InspectSummaryInput) (*mcp.CallToolResult, inspect.Summary, error) {
			data, err := inspectScanData(store, in.Path)
			if err != nil {
				return nil, inspect.Summary{}, err
			}
			opts := inspect.Options{
				Project:   in.Project,
				Provider:  in.Provider,
				CostsOnly: in.CostsOnly,
				Failing:   in.Failing,
				Filter:    in.Filter,
			}
			result, err := inspect.SummaryFor(data, opts)
			if err != nil {
				return nil, inspect.Summary{}, err
			}
			return nil, result, nil
		})

	failingSchema, err := inspectFailingToolOutputSchema()
	if err != nil {
		panic(err)
	}
	mcp.AddTool(srv,
		&mcp.Tool{
			Name: "inspect_failing",
			Description: "Return a flat panorama of everything that's currently failing in the latest scan: failing policy / " +
				"resource pairings (FinOps + tagging), triggered guardrails, and over-budget items. Use this when the user " +
				"asks \"what's wrong?\" — it's the triage view, narrower than inspect_summary which also carries the " +
				"headline counts.",
			OutputSchema: failingSchema,
		},
		func(_ context.Context, _ *mcp.CallToolRequest, in InspectFailingInput) (*mcp.CallToolResult, inspect.FailingPanorama, error) {
			data, err := inspectScanData(store, in.Path)
			if err != nil {
				return nil, inspect.FailingPanorama{}, err
			}
			opts := inspect.Options{
				Project:  in.Project,
				Provider: in.Provider,
				Filter:   in.Filter,
			}
			result, err := inspect.FailingPanoramaFor(data, opts)
			if err != nil {
				return nil, inspect.FailingPanorama{}, err
			}
			return nil, result, nil
		})

	diagnosticsSchema, err := inspectDiagnosticsToolOutputSchema()
	if err != nil {
		panic(err)
	}
	mcp.AddTool(srv,
		&mcp.Tool{
			Name: "inspect_diagnostics",
			Description: "Return every per-project parse / lint diagnostic from the latest scan with severity (critical / " +
				"warning / info), prefix label, message, and source location (filename:line). Use when an `inspect_summary` " +
				"call shows a non-zero diagnostic count and the agent needs to know what specifically went wrong. Defaults " +
				"to returning every severity; set critical_only=true to filter.",
			OutputSchema: diagnosticsSchema,
		},
		func(_ context.Context, _ *mcp.CallToolRequest, in InspectDiagnosticsInput) (*mcp.CallToolResult, InspectDiagnosticsResult, error) {
			data, err := inspectScanData(store, in.Path)
			if err != nil {
				return nil, InspectDiagnosticsResult{}, err
			}
			opts := inspect.Options{Project: in.Project}
			filtered := inspect.Filter(data, opts)
			entries := inspect.CollectDiagnostics(filtered, !in.CriticalOnly)
			return nil, InspectDiagnosticsResult{Count: len(entries), Diagnostics: entries}, nil
		})

	resourcesSchema, err := inspectResourcesToolOutputSchema()
	if err != nil {
		panic(err)
	}
	mcp.AddTool(srv,
		&mcp.Tool{
			Name: "inspect_resources",
			Description: "List or group resources from the latest scan. Without group_by, returns one row per resource " +
				"matching the predicate filters (missing_tag / invalid_tag / min_cost / max_cost / resource address). " +
				"With group_by set, returns one row per aggregation key — same dimensions and pipeline as `inspect " +
				"--group-by`: type, provider, project, file, policy, budget, guardrail. The response's mode field tells " +
				"the agent which payload (resources or groups) is populated.",
			OutputSchema: resourcesSchema,
		},
		func(_ context.Context, _ *mcp.CallToolRequest, in InspectResourcesInput) (*mcp.CallToolResult, InspectResourcesResult, error) {
			data, err := inspectScanData(store, in.Path)
			if err != nil {
				return nil, InspectResourcesResult{}, err
			}

			if err := inspect.ValidateGroupBy(in.GroupBy); err != nil {
				return nil, InspectResourcesResult{}, err
			}

			opts := inspect.Options{
				Project:    in.Project,
				Provider:   in.Provider,
				Resource:   in.Resource,
				Filter:     in.Filter,
				CostsOnly:  in.CostsOnly,
				MissingTag: in.MissingTag,
				InvalidTag: in.InvalidTag,
				MinCost:    in.MinCost,
				MaxCost:    in.MaxCost,
				GroupBy:    in.GroupBy,
				Top:        in.Top,
			}

			if len(in.GroupBy) > 0 {
				grouped, err := inspect.GroupedFor(data, opts)
				if err != nil {
					return nil, InspectResourcesResult{}, err
				}
				return nil, InspectResourcesResult{
					Mode:     "grouped",
					Currency: grouped.Currency,
					GroupBy:  grouped.GroupBy,
					Groups:   grouped.Groups,
					Count:    len(grouped.Groups),
				}, nil
			}

			flat := inspect.ResourcesFor(data, opts)
			return nil, InspectResourcesResult{
				Mode:      "flat",
				Currency:  flat.Currency,
				Resources: flat.Resources,
				Count:     flat.Count,
			}, nil
		})

	topSavingsSchema, err := inspectTopSavingsToolOutputSchema()
	if err != nil {
		panic(err)
	}
	mcp.AddTool(srv,
		&mcp.Tool{
			Name: "inspect_top_savings",
			Description: "Return the top N FinOps savings opportunities from the latest scan, sorted by monthly_savings " +
				"desc. The response also carries total_monthly_savings — the sum across the entire filtered scan, not " +
				"just the top-N — so an agent can show both \"top items\" and \"total available\" without a follow-up " +
				"call. Cost-prioritization view; for guardrail/budget triage use inspect_failing.",
			OutputSchema: topSavingsSchema,
		},
		func(_ context.Context, _ *mcp.CallToolRequest, in InspectTopSavingsInput) (*mcp.CallToolResult, inspect.TopSavingsResult, error) {
			data, err := inspectScanData(store, in.Path)
			if err != nil {
				return nil, inspect.TopSavingsResult{}, err
			}
			n := in.N
			if n <= 0 {
				n = 10
			}
			opts := inspect.Options{
				Project:  in.Project,
				Provider: in.Provider,
				Filter:   in.Filter,
			}
			result, err := inspect.TopSavingsFor(data, opts, n)
			if err != nil {
				return nil, inspect.TopSavingsResult{}, err
			}
			return nil, result, nil
		})

	policyDetailSchema, err := inspectPolicyDetailToolOutputSchema()
	if err != nil {
		panic(err)
	}
	mcp.AddTool(srv,
		&mcp.Tool{
			Name: "inspect_policy_detail",
			Description: "Drill into one policy from the latest scan: the policy's identity (name / slug / kind / message) " +
				"plus every resource currently failing it, with file:line locations and (for finops) the per-issue " +
				"savings or (for tagging) the per-resource missing + invalid tags. Use this when an `inspect_summary` " +
				"or `policies` call surfaces an interesting policy and the agent needs to know which resources to fix.",
			OutputSchema: policyDetailSchema,
		},
		func(_ context.Context, _ *mcp.CallToolRequest, in InspectPolicyDetailInput) (*mcp.CallToolResult, inspect.PolicyDetail, error) {
			if in.Policy == "" {
				return nil, inspect.PolicyDetail{}, fmt.Errorf("policy is required")
			}
			data, err := inspectScanData(store, in.Path)
			if err != nil {
				return nil, inspect.PolicyDetail{}, err
			}
			opts := inspect.Options{
				Policy:   in.Policy,
				Resource: in.Resource,
				Project:  in.Project,
				Filter:   in.Filter,
			}
			result, err := inspect.PolicyDetailFor(data, opts)
			if err != nil {
				return nil, inspect.PolicyDetail{}, err
			}
			return nil, result, nil
		})

	budgetDetailSchema, err := inspectBudgetDetailToolOutputSchema()
	if err != nil {
		panic(err)
	}
	mcp.AddTool(srv,
		&mcp.Tool{
			Name: "inspect_budget_detail",
			Description: "Drill into one budget from the latest scan: the budget itself (amount, current spend, over-budget " +
				"flag, period, tag scope) plus the resources in this scan whose tags match its scope and the FinOps " +
				"savings available on those resources. Differs from the `budgets` tool, which lists configured budgets " +
				"without any post-scan numbers attached.",
			OutputSchema: budgetDetailSchema,
		},
		func(_ context.Context, _ *mcp.CallToolRequest, in InspectBudgetDetailInput) (*mcp.CallToolResult, inspect.BudgetDetail, error) {
			if in.Budget == "" {
				return nil, inspect.BudgetDetail{}, fmt.Errorf("budget is required")
			}
			data, err := inspectScanData(store, in.Path)
			if err != nil {
				return nil, inspect.BudgetDetail{}, err
			}
			opts := inspect.Options{
				Budget:  in.Budget,
				Project: in.Project,
				Filter:  in.Filter,
			}
			result, err := inspect.BudgetDetailFor(data, opts)
			if err != nil {
				return nil, inspect.BudgetDetail{}, err
			}
			return nil, result, nil
		})

	guardrailDetailSchema, err := inspectGuardrailDetailToolOutputSchema()
	if err != nil {
		panic(err)
	}
	mcp.AddTool(srv,
		&mcp.Tool{
			Name: "inspect_guardrail_detail",
			Description: "Drill into one guardrail from the latest scan: the matching guardrail entry with its triggered " +
				"flag and total monthly cost rolled up by the scan. Differs from the `guardrails` tool, which lists " +
				"configured guardrails without any post-scan triggered state. Mirrors the CLI's --guardrail X --json " +
				"output — no extra aggregation, just the guardrail itself.",
			OutputSchema: guardrailDetailSchema,
		},
		func(_ context.Context, _ *mcp.CallToolRequest, in InspectGuardrailDetailInput) (*mcp.CallToolResult, format.GuardrailOutput, error) {
			if in.Guardrail == "" {
				return nil, format.GuardrailOutput{}, fmt.Errorf("guardrail is required")
			}
			data, err := inspectScanData(store, in.Path)
			if err != nil {
				return nil, format.GuardrailOutput{}, err
			}
			opts := inspect.Options{
				Guardrail: in.Guardrail,
				Project:   in.Project,
				Filter:    in.Filter,
			}
			result, err := inspect.GuardrailDetailFor(data, opts)
			if err != nil {
				return nil, format.GuardrailOutput{}, err
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
	// resolveOrg pipeline again. applyActiveOrgByID also refreshes the
	// resolved slug + agentsEnabled flag so the Agents gate reflects the
	// newly-selected org rather than the one resolved at startup.
	cfg.Org = in.Slug
	applyActiveOrgByID(cfg, uc.Organizations, orgID)

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
