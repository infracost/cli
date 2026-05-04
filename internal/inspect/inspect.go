package inspect

import (
	"io"

	"github.com/infracost/cli/internal/format"
)

type Options struct {
	Summary   bool
	GroupBy   []string // any of ValidGroupByOptions; validated by ValidateGroupBy
	Policy    string   // filter to a specific policy name/slug
	Budget    string   // filter to a specific budget name/id
	Guardrail string   // filter to a specific guardrail name/id
	Resource  string   // filter to a specific resource address
	Provider  string
	Project   string
	CostsOnly bool
	Failing   bool
	Top       int
	JSON      bool
	LLM       bool

	// Aggregation views (mutually exclusive with each other; --top-savings
	// is the only one that takes a count).
	TotalSavings bool // sum monthly_savings across every FinOps issue
	TopSavings   int  // top N FinOps issues by monthly_savings (0 = disabled)

	// Output modifiers.
	AddressesOnly bool // strip everything except resource addresses

	// Targeted filters (replace common jq/python patterns).
	MissingTag string  // resources missing this tag entirely
	InvalidTag string  // resources where this tag's value is outside the policy's allowed list
	MinCost    float64 // resources with monthly cost ≥ this; 0 = disabled
	MaxCost    float64 // resources with monthly cost ≤ this; 0 = disabled

	// Generic filter expression. Parsed from a comma-separated list of
	// key=value AND'd predicates (see filter.go for the supported grammar).
	Filter string

	// Fields, when non-empty, projects tabular outputs to just those
	// columns in that order. Validated against per-view canonical names
	// before render — unknown fields error out with the available set
	// listed (see fields.go). --addresses-only is an alias for
	// Fields=["address"] applied at flag-parsing time.
	Fields []string
}

// Structured reports whether the caller requested a machine-readable form
// (either --json or --llm).
func (o Options) Structured() bool { return o.JSON || o.LLM }

func Run(w io.Writer, data *format.Output, opts Options) error {
	// Translate any --filter expression into the targeted option fields
	// before we filter / dispatch. This keeps the rest of the inspect
	// pipeline ignorant of the filter syntax — it just sees populated
	// option fields it already knows how to handle.
	if err := ParseFilter(opts.Filter, &opts); err != nil {
		return err
	}
	filtered := Filter(data, opts)

	// Aggregation views run before the policy/budget/guardrail dispatch
	// because they're orthogonal to those — they aggregate over whatever
	// the filter selected, regardless of which detail view would otherwise
	// fire.
	if opts.TotalSavings {
		return WriteTotalSavings(w, filtered, opts)
	}
	if opts.TopSavings > 0 {
		return WriteTopSavings(w, filtered, opts.TopSavings, opts)
	}

	// Resource-shaped predicates (--missing-tag, --invalid-tag,
	// --min-cost, --max-cost) short-circuit to a flat address list so
	// users don't have to combine them with --addresses-only or jq.
	if opts.hasResourceFilter() {
		return WriteFilteredResources(w, filtered, opts)
	}

	if opts.Budget != "" {
		return WriteBudgetDetail(w, filtered, opts)
	}

	if opts.Guardrail != "" {
		return WriteGuardrailDetail(w, filtered, opts)
	}

	if opts.Policy != "" {
		return WritePolicyDetail(w, filtered, opts)
	}

	if len(opts.GroupBy) == 0 {
		switch {
		case opts.Resource != "":
			opts.GroupBy = []string{string(GroupByPolicy)}
		case opts.Failing:
			// --failing alone shows a unified panorama of everything
			// needing attention: failing policies + triggered guardrails +
			// over-budget items. Combining --failing with --group-by X uses
			// the explicit view (and falls through this branch).
			return WriteFailing(w, filtered, opts)
		case opts.Provider != "" || opts.Top > 0 || opts.CostsOnly:
			opts.GroupBy = []string{string(GroupByType)}
		}
	}

	if opts.Summary && opts.Resource == "" {
		return WriteSummary(w, filtered, opts)
	}

	if len(opts.GroupBy) > 0 {
		return WriteGroupBy(w, filtered, opts)
	}

	return WriteSummary(w, filtered, opts)
}
