package inspect

import (
	"fmt"
	"sort"
	"strings"
)

// Per-view canonical field names. Lookups are case-sensitive and the
// names are also used as JSON keys when --json/--llm is set, so consumers
// can rely on a stable schema across renders.
var (
	fieldsTopSavings        = []string{"address", "policy", "policy_slug", "project", "yearly_savings", "description"}
	fieldsFilteredResources = []string{"address", "type", "project", "monthly_cost", "is_free"}

	// Human defaults trim the machine-oriented columns from the tables a
	// person reads when they don't pass --fields. The full sets above stay
	// selectable via --fields and unchanged for --json/--llm.
	//   policy_slug — a machine key that duplicates the human policy name.
	//   is_free     — redundant with a $0 monthly_cost for a reader.
	fieldsTopSavingsHuman        = []string{"address", "policy", "project", "yearly_savings", "description"}
	fieldsFilteredResourcesHuman = []string{"address", "type", "project", "monthly_cost"}

	// Summary fields are the scalar metrics on Summary. Lists like
	// project_details / failing_policy_list aren't projectable here —
	// they need a dedicated view with --fields support of their own.
	fieldsSummary = []string{
		"projects", "projects_with_errors",
		"resources", "costed_resources", "free_resources",
		"monthly_cost", "total_yearly_savings",
		"finops_policies", "failing_policies", "distinct_failing_finops_resources",
		"tagging_policies", "failing_tagging_policies", "distinct_failing_tagging_resources",
		"guardrails", "triggered_guardrails",
		"budgets", "over_budget",
		"critical_diagnostics", "warning_diagnostics",
	}
)

// validateFields checks that every requested field exists in `available`,
// preserving the user's ordering and returning an actionable error for
// the first unknown field.
func validateFields(requested, available []string) ([]string, error) {
	if len(requested) == 0 {
		return available, nil
	}
	allowed := make(map[string]struct{}, len(available))
	for _, f := range available {
		allowed[f] = struct{}{}
	}
	out := make([]string, 0, len(requested))
	for _, f := range requested {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		if _, ok := allowed[f]; !ok {
			sorted := append([]string(nil), available...)
			sort.Strings(sorted)
			return nil, fmt.Errorf(
				"unknown field %q. Available fields: %s",
				f, strings.Join(sorted, ", "))
		}
		out = append(out, f)
	}
	return out, nil
}

// effectiveFields normalizes the requested field list against an
// available set, applying the --addresses-only shortcut as
// Fields=["address"] when AddressesOnly is set and Fields isn't.
// Returns the validated, ordered list to project against.
func effectiveFields(opts Options, available []string) ([]string, error) {
	requested := opts.Fields
	if len(requested) == 0 && opts.AddressesOnly {
		requested = []string{"address"}
	}
	return validateFields(requested, available)
}
