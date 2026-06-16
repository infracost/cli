package cmds

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/infracost/cli/internal/api"
	"github.com/infracost/cli/internal/api/dashboard"
	"github.com/infracost/cli/internal/api/events"
	"github.com/infracost/cli/internal/config"
	"github.com/infracost/cli/internal/scanner"
	"github.com/infracost/cli/internal/ui"
	"github.com/infracost/cli/internal/vcs"
	"github.com/infracost/cli/pkg/logging"
	"github.com/infracost/proto/gen/go/infracost/parser/event"
	"github.com/spf13/cobra"
	"golang.org/x/oauth2"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"
)

// PoliciesInput is the parsed input for `policies`. Same shape for the CLI
// wrapper and the MCP `policies` tool — both populate it and call the pure
// Policies function.
type PoliciesInput struct {
	// Path is the directory whose VCS repository / branch determine which
	// per-branch and per-project policies apply. Empty falls through to
	// the current working directory.
	Path string `json:"path,omitempty" jsonschema:"Directory whose VCS repo + branch are used to resolve which policies apply. Optional; defaults to the MCP server's working directory."`
	// FinOpsOnly drops the tagging policy list from the result and skips
	// the matching API call. Mutually exclusive with TaggingOnly.
	FinOpsOnly bool `json:"finops_only,omitempty" jsonschema:"List only FinOps policies. Mutually exclusive with tagging_only."`
	// TaggingOnly drops the FinOps policy list. Mutually exclusive with
	// FinOpsOnly.
	TaggingOnly bool `json:"tagging_only,omitempty" jsonschema:"List only tagging policies. Mutually exclusive with finops_only."`
	// Providers narrows FinOps policy lookup to the given list of cloud
	// providers (e.g. ["aws"]). Reduces which provider plugins are
	// downloaded. Empty means all providers.
	Providers []string `json:"providers,omitempty" jsonschema:"Limit FinOps policy lookup to the given providers (aws, azure, google). Empty means all providers."`
}

// PoliciesResult is the typed output of `policies`. Used by both CLI
// `--json` / `--llm` rendering and the MCP `policies` tool. The
// scanner.FinOpsPolicy / scanner.TaggingPolicy types embed proto
// messages — keeping those in the wire format would leak internal
// proto field names to consumers, so the pure function projects them to
// the clean shapes below.
type PoliciesResult struct {
	FinopsPolicies  []FinopsPolicyEntry  `json:"finops_policies"`
	TaggingPolicies []TaggingPolicyEntry `json:"tagging_policies"`
}

// FinopsPolicyEntry is one FinOps policy from the org. When the org has
// per-instance custom settings (renamed, custom message, custom JSON
// settings), they override the policy defaults — ID / Name / Description
// always reflect the effective values the policy will use at scan time.
type FinopsPolicyEntry struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	// Provider is one of "aws", "azure", "google".
	Provider string `json:"provider"`
	// BranchFilter / ProjectFilter / TagFilter scope which scans the
	// policy applies to. Nil filters mean "match anything", matching the
	// human renderer's default.
	BranchFilter  *PolicyStringFilter `json:"branch_filter,omitempty"`
	ProjectFilter *PolicyStringFilter `json:"project_filter,omitempty"`
	TagFilter     *PolicyMapFilter    `json:"tag_filter,omitempty"`
	// CustomSettings is the policy's per-instance JSON settings string,
	// pretty-printed for readability. Empty when the policy uses
	// defaults. Carried opaquely — the shape is policy-specific.
	CustomSettings string `json:"custom_settings,omitempty"`
}

// TaggingPolicyEntry is one tagging policy from the org.
type TaggingPolicyEntry struct {
	ID             string                `json:"id"`
	Name           string                `json:"name"`
	Message        string                `json:"message"`
	BranchFilter   *PolicyStringFilter   `json:"branch_filter,omitempty"`
	ProjectFilter  *PolicyStringFilter   `json:"project_filter,omitempty"`
	ResourceFilter *PolicyStringFilter   `json:"resource_filter,omitempty"`
	Requirements   []TagRequirementEntry `json:"requirements"`
}

// PolicyStringFilter is the include/exclude shape used by branch /
// project / resource filters. At most one of Include / Exclude is set.
type PolicyStringFilter struct {
	Include []string `json:"include,omitempty"`
	Exclude []string `json:"exclude,omitempty"`
}

// PolicyMapFilter is the include/exclude shape used by tag filters, where
// matches are keyed by tag name. At most one of Include / Exclude is set.
type PolicyMapFilter struct {
	Include map[string]string `json:"include,omitempty"`
	Exclude map[string]string `json:"exclude,omitempty"`
}

// TagRequirementEntry is one tag requirement on a tagging policy. The Type
// field is the requirement kind — "any" / "regex" / "list" — and the
// matching value fields (ValueRegex / AllowedValues) are populated only
// when the type uses them.
type TagRequirementEntry struct {
	Key           string   `json:"key"`
	Mandatory     bool     `json:"mandatory"`
	Type          string   `json:"type"`
	ValueRegex    string   `json:"value_regex,omitempty"`
	AllowedValues []string `json:"allowed_values,omitempty"`
}

// Policies resolves the target directory's run parameters, lists every
// FinOps + tagging policy the org has configured for the resolved branch /
// project, and returns the typed result. Authentication and org
// resolution are the caller's responsibility — `source` and `cfg.OrgID`
// must be populated before calling Policies.
//
// Failing to fetch run parameters is non-fatal: policies are still listed,
// just without per-branch / per-project filtering applied. This matches
// the previous CLI behavior where users could `infracost policies` from
// outside a repo and still see the full available set.
func Policies(ctx context.Context, cfg *config.Config, source oauth2.TokenSource, in PoliciesInput) (PoliciesResult, error) {
	var zero PoliciesResult

	if in.FinOpsOnly && in.TaggingOnly {
		return zero, fmt.Errorf("cannot specify both --finops-only and --tagging-only")
	}

	providers, err := resolveProviderFilter(in.Providers, in.TaggingOnly)
	if err != nil {
		return zero, err
	}

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
	var runParameters *dashboard.RunParameters
	if rp, err := client.RunParameters(ctx, repositoryURL, branchName); err != nil {
		logging.Warnf("Failed to fetch runParameters, gathering policies without them: %s", err.Error())
	} else {
		if cfg.Org == "" {
			cfg.OrgID = rp.OrganizationID
		}
		runParameters = &rp
	}

	events.RegisterMetadata("orgId", cfg.OrgID)
	events.RegisterMetadata("repoId", repositoryURL)
	events.RegisterMetadata("branchId", branchName)

	s := &scanner.Scanner{
		Plugins:         &cfg.Plugins,
		Logging:         cfg.Logging,
		Dashboard:       cfg.Dashboard,
		Currency:        cfg.Currency,
		PricingEndpoint: cfg.PricingEndpoint,
	}
	finops, tagging, err := s.ListPolicies(ctx, runParameters, providers)
	if err != nil {
		return zero, fmt.Errorf("failed to list policies: %w", err)
	}

	result := PoliciesResult{
		FinopsPolicies:  []FinopsPolicyEntry{},
		TaggingPolicies: []TaggingPolicyEntry{},
	}
	if !in.TaggingOnly {
		for _, p := range finops {
			result.FinopsPolicies = append(result.FinopsPolicies, toFinopsPolicyEntry(p))
		}
	}
	if !in.FinOpsOnly {
		for _, p := range tagging {
			result.TaggingPolicies = append(result.TaggingPolicies, toTaggingPolicyEntry(p))
		}
	}
	return result, nil
}

// PoliciesCmd builds the cobra command. Parses flags into PoliciesInput,
// authenticates, resolves the active org, then calls the pure Policies
// function under a spinner and dispatches the result.
func PoliciesCmd(cfg *config.Config) *cobra.Command {
	var in PoliciesInput
	cmd := &cobra.Command{
		Use:   "policies",
		Short: "List all available FinOps and tagging policies",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				in.Path = args[0]
			}

			source, err := cfg.Auth.Token(cmd.Context())
			if err != nil {
				return fmt.Errorf("failed to log in: %w", err)
			}
			if err := resolveOrg(cmd.Context(), cfg, source); err != nil {
				return err
			}

			var result PoliciesResult
			if err := ui.RunWithSpinnerErr(cmd.Context(), "Loading policies...", "Policies loaded", func(ctx context.Context) error {
				var pErr error
				result, pErr = Policies(ctx, cfg, source, in)
				return pErr
			}); err != nil {
				return err
			}
			return writeStructured(cfg, os.Stdout, result, policiesRenderers())
		},
	}

	cmd.Flags().BoolVarP(&in.FinOpsOnly, "finops-only", "f", false, "Only list FinOps policies")
	cmd.Flags().BoolVarP(&in.TaggingOnly, "tagging-only", "t", false, "Only list tagging policies")
	cmd.Flags().StringSliceVarP(&in.Providers, "providers", "p", nil, "Limit FinOps policy lookup to the given providers (aws, azure, google); reduces which provider plugins are downloaded")

	return cmd
}

func policiesRenderers() Renderers[PoliciesResult] {
	return Renderers[PoliciesResult]{
		Human: renderPoliciesHuman,
		JSON:  renderPoliciesJSON,
		LLM:   renderPoliciesLLM,
	}
}

func renderPoliciesHuman(w io.Writer, r PoliciesResult) error {
	// Clean separation from log lines, matching the previous renderer.
	_, _ = fmt.Fprintln(w)

	// The pure function already filters by FinopsOnly / TaggingOnly; the
	// renderer just skips an empty slice's heading when neither was
	// requested but the API returned nothing.
	if len(r.FinopsPolicies) > 0 || (r.FinopsPolicies != nil && !skipFinopsHeading(r)) {
		ui.Heading("FinOps Policies")
		_, _ = fmt.Fprintln(w)
		if len(r.FinopsPolicies) == 0 {
			_, _ = fmt.Fprintln(w, ui.Muted("No FinOps policies found"))
		}
		for _, p := range r.FinopsPolicies {
			_, _ = fmt.Fprintf(w, "%s%s%s %s %s\n", ui.Muted("["), ui.Accent(p.Provider), ui.Muted("]"), ui.Bold(ui.Accent(p.Name)), ui.Mutedf("(%s)", p.ID))
			_, _ = fmt.Fprintln(w, p.Description)
			_, _ = fmt.Fprintf(w, "\n  %s\n", ui.Bold(ui.Muted("Applies to")))
			_, _ = fmt.Fprintf(w, "    - %s\n", stringFilterHuman("branches", p.BranchFilter))
			_, _ = fmt.Fprintf(w, "    - %s\n", stringFilterHuman("projects", p.ProjectFilter))
			_, _ = fmt.Fprintf(w, "    - %s\n", mapFilterHuman("resources", "with tags", p.TagFilter))
			if p.CustomSettings != "" {
				indented := strings.ReplaceAll(p.CustomSettings, "\n", "\n  ")
				_, _ = fmt.Fprintf(w, "\n  %s\n    %s\n", ui.Bold(ui.Muted("Custom settings")), ui.Code(indented))
			}
			_, _ = fmt.Fprintln(w)
		}
		_, _ = fmt.Fprintln(w)
	}

	if len(r.TaggingPolicies) > 0 || (r.TaggingPolicies != nil && !skipTaggingHeading(r)) {
		ui.Heading("Tagging Policies")
		_, _ = fmt.Fprintln(w)
		if len(r.TaggingPolicies) == 0 {
			_, _ = fmt.Fprintln(w, ui.Muted("No tagging policies found"))
		}
		for _, p := range r.TaggingPolicies {
			_, _ = fmt.Fprintf(w, "%s%s%s %s  %s\n", ui.Muted("["), ui.Accent("CUSTOM"), ui.Muted("]"), ui.Bold(ui.Accent(p.Name)), ui.Mutedf("(%s)", p.ID))
			_, _ = fmt.Fprintln(w, p.Message)

			_, _ = fmt.Fprintf(w, "\n  %s\n", ui.Bold(ui.Muted("Applies to")))
			_, _ = fmt.Fprintf(w, "    - %s\n", stringFilterHuman("branches", p.BranchFilter))
			_, _ = fmt.Fprintf(w, "    - %s\n", stringFilterHuman("projects", p.ProjectFilter))
			_, _ = fmt.Fprintf(w, "    - %s\n", stringFilterHuman("resources", p.ResourceFilter))

			_, _ = fmt.Fprintf(w, "\n  %s\n", ui.Bold(ui.Muted("Requirements")))
			for _, req := range p.Requirements {
				if req.Mandatory {
					_, _ = fmt.Fprintf(w, "  - Tag %s must be set. ", ui.Accent(req.Key))
				} else {
					_, _ = fmt.Fprintf(w, "  - Tag %s is not required, but if set: ", ui.Accent(req.Key))
				}
				switch req.Type {
				case "any":
					_, _ = fmt.Fprintf(w, "It may use %s.", ui.Italic("any value"))
				case "regex":
					_, _ = fmt.Fprintf(w, "It may use values matching the regex %s.", ui.Code(req.ValueRegex))
				case "list":
					_, _ = fmt.Fprintf(w, "It may use %s:\n", ui.Italic("any of the following values"))
					values := req.AllowedValues
					if len(values) > 5 {
						values = values[:5]
						_, _ = fmt.Fprintf(w, "    (showing first 5 of %d values)\n", len(req.AllowedValues))
					}
					for _, v := range values {
						_, _ = fmt.Fprintf(w, "    - %s\n", v)
					}
				default:
					_, _ = fmt.Fprintf(w, "It has an unknown requirement type %s.", req.Type)
				}
				_, _ = fmt.Fprintln(w)
			}
			_, _ = fmt.Fprintln(w)
		}
		_, _ = fmt.Fprintln(w)
	}

	return nil
}

// skipFinopsHeading reports whether the FinOps section was suppressed by the
// caller (--tagging-only on the CLI, TaggingOnly on the MCP tool). The pure
// Policies function returns a nil FinopsPolicies slice in that case; the
// renderer uses that signal to skip the heading entirely.
func skipFinopsHeading(r PoliciesResult) bool {
	return r.FinopsPolicies == nil
}

func skipTaggingHeading(r PoliciesResult) bool {
	return r.TaggingPolicies == nil
}

func renderPoliciesJSON(w io.Writer, r PoliciesResult) error {
	body, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to write JSON output: %w", err)
	}
	_, err = fmt.Fprintln(w, string(body))
	return err
}

func renderPoliciesLLM(w io.Writer, r PoliciesResult) error {
	// Policies has no large repeating tabular section, so the LLM and JSON
	// renderers emit the same flat shape. TOON's strength is dedupe over
	// arrays of objects with shared keys; there's not much to dedupe here.
	return renderPoliciesJSON(w, r)
}

// toFinopsPolicyEntry projects a raw scanner.FinOpsPolicy to the clean
// MCP/JSON shape. Custom Settings overrides apply when present (name,
// description, id) so the surfaced values are the ones the policy will
// actually use at scan time.
func toFinopsPolicyEntry(p scanner.FinOpsPolicy) FinopsPolicyEntry {
	entry := FinopsPolicyEntry{
		ID:          p.Slug,
		Name:        p.Name,
		Description: p.Description,
		Provider:    p.Provider,
	}
	if p.Settings != nil {
		if p.Settings.Name != "" {
			entry.Name = p.Settings.Name
		}
		if p.Settings.Message != "" {
			entry.Description = p.Settings.Message
		}
		entry.ID = p.Settings.Id
		entry.BranchFilter = stringFilterFromProto(p.Settings.BranchFilter)
		entry.ProjectFilter = stringFilterFromProto(p.Settings.ProjectFilter)
		entry.TagFilter = mapFilterFromProto(p.Settings.TagFilter)
		entry.CustomSettings = prettyCustomSettings(p.Settings.Settings)
	}
	return entry
}

// toTaggingPolicyEntry projects a raw scanner.TaggingPolicy to the clean
// shape.
func toTaggingPolicyEntry(p scanner.TaggingPolicy) TaggingPolicyEntry {
	entry := TaggingPolicyEntry{
		ID:             p.Id,
		Name:           p.Name,
		Message:        p.Message,
		BranchFilter:   stringFilterFromProto(p.BranchFilter),
		ProjectFilter:  stringFilterFromProto(p.ProjectFilter),
		ResourceFilter: stringFilterFromProto(p.ResourceFilter),
		Requirements:   make([]TagRequirementEntry, 0, len(p.Requirements)),
	}
	for _, req := range p.Requirements {
		entry.Requirements = append(entry.Requirements, TagRequirementEntry{
			Key:           req.Key,
			Mandatory:     req.Mandatory,
			Type:          requirementTypeString(req.Type),
			ValueRegex:    req.ValueRegex,
			AllowedValues: req.AllowedValues,
		})
	}
	return entry
}

// stringFilterFromProto returns nil for filters that aren't set, so the
// JSON output omits them entirely rather than emitting an empty object.
func stringFilterFromProto(f *event.StringFilter) *PolicyStringFilter {
	if f == nil || (len(f.Include) == 0 && len(f.Exclude) == 0) {
		return nil
	}
	return &PolicyStringFilter{Include: f.Include, Exclude: f.Exclude}
}

func mapFilterFromProto(f *event.MapFilter) *PolicyMapFilter {
	if f == nil || (len(f.Include) == 0 && len(f.Exclude) == 0) {
		return nil
	}
	return &PolicyMapFilter{Include: f.Include, Exclude: f.Exclude}
}

// requirementTypeString maps the proto enum to a stable lowercase string
// the MCP wire format can advertise unambiguously.
func requirementTypeString(t event.TagPolicyRequirement_Type) string {
	switch t {
	case event.TagPolicyRequirement_ANY:
		return "any"
	case event.TagPolicyRequirement_REGEX:
		return "regex"
	case event.TagPolicyRequirement_LIST:
		return "list"
	default:
		return "unknown"
	}
}

// prettyCustomSettings pretty-prints the per-instance settings JSON the
// org may have attached to a FinOps policy. Empty / "{}" are returned as
// "" so callers can omit them. Best-effort: if the JSON doesn't parse, we
// return it verbatim.
func prettyCustomSettings(raw string) string {
	if raw == "" {
		return ""
	}
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return raw
	}
	body, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return raw
	}
	pretty := string(body)
	if pretty == "{}" {
		return ""
	}
	return pretty
}

func resolveProviderFilter(names []string, taggingOnly bool) ([]string, error) {
	if taggingOnly {
		return []string{}, nil
	}
	if len(names) == 0 {
		return nil, nil
	}
	known := map[string]struct{}{
		"aws":     {},
		"azure":   {},
		"azurerm": {},
		"google":  {},
	}
	out := make([]string, 0, len(names))
	seen := map[string]bool{}
	for _, name := range names {
		key := strings.ToLower(strings.TrimSpace(name))
		if _, ok := known[key]; !ok {
			return nil, fmt.Errorf("unknown provider %q (must be one of: aws, azure, google)", name)
		}
		if key == "azure" {
			key = "azurerm"
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, key)
	}
	return out, nil
}

// stringFilterHuman renders a [PolicyStringFilter] for the human view.
// Returns a "All branches" / "Only X branches" / "All branches except X"
// phrasing, matching the previous renderer's tone.
func stringFilterHuman(plural string, f *PolicyStringFilter) string {
	if f == nil || (len(f.Include) == 0 && len(f.Exclude) == 0) {
		return fmt.Sprintf("%s %s", ui.Bold("All"), plural)
	}
	coloredIncludes := make([]string, len(f.Include))
	for i, include := range f.Include {
		coloredIncludes[i] = ui.Positive(include)
	}
	coloredExcludes := make([]string, len(f.Exclude))
	for i, exclude := range f.Exclude {
		coloredExcludes[i] = ui.Danger(exclude)
	}
	if len(f.Exclude) == 0 {
		return fmt.Sprintf("%s %s %s", ui.Bold("Only"), joinListWithOxfordComma(coloredIncludes), plural)
	}
	if len(f.Include) == 0 {
		return fmt.Sprintf("%s %s except %s", ui.Bold("All"), plural, joinListWithOxfordComma(coloredExcludes))
	}
	return fmt.Sprintf("%s %s; but not %s", joinListWithOxfordComma(coloredIncludes), cases.Title(language.English).String(plural), joinListWithOxfordComma(coloredExcludes))
}

// mapFilterHuman renders a [PolicyMapFilter] (used for tag-keyed
// filtering) for the human view.
func mapFilterHuman(plural, requirement string, f *PolicyMapFilter) string {
	if f == nil || (len(f.Include) == 0 && len(f.Exclude) == 0) {
		return fmt.Sprintf("%s %s", ui.Bold("All"), plural)
	}
	var matchType string
	var src map[string]string
	if len(f.Include) > 0 {
		matchType = ui.Positive("matching")
		src = f.Include
	} else {
		matchType = ui.Danger("not matching")
		src = f.Exclude
	}
	var matches []string
	keys := make([]string, 0, len(src))
	for k := range src {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		matches = append(matches, fmt.Sprintf("%s=%s", ui.Accent(k), ui.Positive(src[k])))
	}
	return fmt.Sprintf("%s %s %s %s of %s", cases.Title(language.English).String(plural), requirement, matchType, ui.Bold("all"), joinListWithOxfordComma(matches))
}

func joinListWithOxfordComma(items []string) string {
	if len(items) == 0 {
		return ""
	}
	if len(items) == 1 {
		return items[0]
	}
	if len(items) == 2 {
		return items[0] + " and " + items[1]
	}
	return strings.Join(items[:len(items)-1], ", ") + ", and " + items[len(items)-1]
}