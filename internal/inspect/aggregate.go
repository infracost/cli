package inspect

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/infracost/cli/internal/format"
	"github.com/infracost/go-proto/pkg/rat"
)

// FinopsTopSavingsItem is the per-issue row produced by --top-savings.
// Carries enough context for the user (or an LLM consumer) to act on the
// finding without a follow-up drill-in: which resource, which policy,
// what's the saving, and the issue description.
type FinopsTopSavingsItem struct {
	Address        string   `json:"address"`
	PolicyName     string   `json:"policy_name"`
	PolicySlug     string   `json:"policy_slug,omitempty"`
	Project        string   `json:"project"`
	MonthlySavings *rat.Rat `json:"monthly_savings"`
	Description    string   `json:"description,omitempty"`
}

// totalFinopsSavings sums MonthlySavings across every FinOps issue in the
// scan, ignoring nil savings values.
func totalFinopsSavings(data *format.Output) *rat.Rat {
	total := rat.Zero
	for _, p := range data.Projects {
		for _, fp := range p.FinopsResults {
			for _, fr := range fp.FailingResources {
				for _, iss := range fr.Issues {
					if iss.MonthlySavings == nil {
						continue
					}
					total = total.Add(iss.MonthlySavings)
				}
			}
		}
	}
	return total
}

// TopSavingsResult is the typed return of [TopSavingsFor]. The total is
// the sum of monthly_savings across the whole filtered scan (not just
// the top-N), so MCP callers can show both "top items" and "total
// available savings" without making a separate call. Currency is
// carried on the envelope so the rat.Rat money values are
// interpretable without context.
type TopSavingsResult struct {
	Currency            string                 `json:"currency"`
	TotalMonthlySavings *rat.Rat               `json:"total_monthly_savings"`
	Items               []FinopsTopSavingsItem `json:"items"`
}

// TopSavingsFor applies the inspect filter pipeline and then returns
// the top-N FinOps savings opportunities plus the total potential
// monthly savings across the filtered scan. Pairs with the
// `inspect_top_savings` MCP tool — narrower than the failing-panorama
// triage view: this is the cost-prioritization angle only. Triggered
// guardrails and over-budget items live in [FailingPanorama] /
// BuildFailingPanorama.
func TopSavingsFor(data *format.Output, opts Options, n int) (TopSavingsResult, error) {
	if err := ParseFilter(opts.Filter, &opts); err != nil {
		return TopSavingsResult{}, err
	}
	data = Filter(data, opts)
	return TopSavingsResult{
		Currency:            data.Currency,
		TotalMonthlySavings: totalFinopsSavings(data),
		Items:               topFinopsSavings(data, n),
	}, nil
}

// topFinopsSavings returns the top-N FinOps issues by monthly savings,
// sorted desc. Ties broken by resource address for determinism.
func topFinopsSavings(data *format.Output, n int) []FinopsTopSavingsItem {
	var rows []FinopsTopSavingsItem
	for _, p := range data.Projects {
		for _, fp := range p.FinopsResults {
			for _, fr := range fp.FailingResources {
				for _, iss := range fr.Issues {
					rows = append(rows, FinopsTopSavingsItem{
						Address:        fr.Name,
						PolicyName:     fp.PolicyName,
						PolicySlug:     fp.PolicySlug,
						Project:        p.ProjectName,
						MonthlySavings: iss.MonthlySavings,
						Description:    iss.Description,
					})
				}
			}
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		ai, aj := rat.Zero, rat.Zero
		if rows[i].MonthlySavings != nil {
			ai = rows[i].MonthlySavings
		}
		if rows[j].MonthlySavings != nil {
			aj = rows[j].MonthlySavings
		}
		if !ai.Equals(aj) {
			return ai.GreaterThan(aj)
		}
		return rows[i].Address < rows[j].Address
	})
	if n > 0 && n < len(rows) {
		rows = rows[:n]
	}
	return rows
}

// WriteTotalSavings prints a single scalar — the sum of monthly_savings
// across every FinOps issue. Honors --json and --llm by emitting a small
// `{"total_monthly_savings": "<value>", "currency": "<code>"}` payload.
func WriteTotalSavings(w io.Writer, data *format.Output, opts Options) error {
	total := totalFinopsSavings(data)
	if opts.Structured() {
		payload := struct {
			TotalMonthlySavings *rat.Rat `json:"total_monthly_savings"`
			Currency            string   `json:"currency"`
		}{
			TotalMonthlySavings: total,
			Currency:            data.Currency,
		}
		return writeStructured(w, payload, opts)
	}
	_, err := fmt.Fprintf(w, "Total potential monthly savings: %s\n", humanMoney(total, data.Currency))
	return err
}

// WriteTopSavings prints the top-N FinOps issues by monthly_savings.
// Honors --fields / --addresses-only (column projection) and
// --json/--llm (structured list, projected if --fields is set).
func WriteTopSavings(w io.Writer, data *format.Output, n int, opts Options) error {
	rows := topFinopsSavings(data, n)
	fields, err := effectiveFields(opts, fieldsTopSavings)
	if err != nil {
		return err
	}

	if opts.Structured() {
		// Projected structured output: emit a list of {field: value}
		// objects. Without --fields this is the full struct, with
		// --fields it's just the requested keys preserving the
		// caller's --fields order via orderedFields.
		if len(opts.Fields) == 0 && !opts.AddressesOnly {
			return writeStructured(w, rows, opts)
		}
		out := make([]orderedFields, 0, len(rows))
		for _, r := range rows {
			out = append(out, orderedFromMap(projectTopSavingsRow(r, fields, data.Currency), fields))
		}
		return writeStructured(w, out, opts)
	}

	if len(rows) == 0 {
		_, err := fmt.Fprintln(w, "No FinOps issues found.")
		return err
	}

	// Single-column shortcut: addresses (or any one field) → one value
	// per line, no header. This is the muscle-memory shape from the
	// previous --addresses-only behavior.
	if len(fields) == 1 {
		for _, r := range rows {
			row := projectTopSavingsRow(r, fields, data.Currency)
			if _, err := fmt.Fprintln(w, row[fields[0]]); err != nil {
				return err
			}
		}
		return nil
	}

	// Multi-column or default: TSV with a header row. Header lets the
	// model use `awk -F'\t'` confidently without guessing column order.
	if _, err := fmt.Fprintln(w, tsvHeader(fields)); err != nil {
		return err
	}
	for _, r := range rows {
		row := projectTopSavingsRow(r, fields, data.Currency)
		if _, err := fmt.Fprintln(w, strings.Join(projectRow(row, fields), "\t")); err != nil {
			return err
		}
	}
	return nil
}

// projectTopSavingsRow returns a map from canonical field name to the
// rendered string value for one FinopsTopSavingsItem. Keep this in sync
// with fieldsTopSavings.
func projectTopSavingsRow(r FinopsTopSavingsItem, _ []string, currency string) map[string]string {
	return map[string]string{
		"address":         r.Address,
		"policy":          r.PolicyName,
		"policy_slug":     r.PolicySlug,
		"project":         r.Project,
		"monthly_savings": humanMoney(r.MonthlySavings, currency),
		"description":     r.Description,
	}
}

// writeAddressesOnly prints a deduplicated list of addresses, one per line,
// with no surrounding chrome. Used by --addresses-only on any inspect view
// that produces a resource list.
func writeAddressesOnly(w io.Writer, addrs []string) error {
	for _, a := range addrs {
		if _, err := fmt.Fprintln(w, a); err != nil {
			return err
		}
	}
	return nil
}

// hasResourceFilter reports whether any of the resource-shaped predicate
// flags are set, in which case Run dispatches to WriteFilteredResources.
func (o Options) hasResourceFilter() bool {
	return o.MissingTag != "" || o.InvalidTag != "" || o.MinCost > 0 || o.MaxCost > 0
}

// WriteFilteredResources prints the resource-shaped predicate result. By
// default it's a simple newline-separated address list. --fields lets the
// caller project additional columns (type, project, monthly_cost,
// is_free) without piping through cut or jq.
func WriteFilteredResources(w io.Writer, data *format.Output, opts Options) error {
	rows := selectFilteredResources(data, opts)
	fields, err := effectiveFields(opts, fieldsFilteredResources)
	if err != nil {
		return err
	}

	if opts.Structured() {
		// Default structured form is a flat list of addresses + count
		// (preserves prior behavior). With --fields we project to a list
		// of objects with just the requested keys.
		if len(opts.Fields) == 0 && !opts.AddressesOnly {
			addrs := make([]string, 0, len(rows))
			for _, r := range rows {
				addrs = append(addrs, r["address"])
			}
			payload := struct {
				Addresses []string `json:"addresses"`
				Count     int      `json:"count"`
			}{Addresses: addrs, Count: len(addrs)}
			return writeStructured(w, payload, opts)
		}
		out := make([]orderedFields, 0, len(rows))
		for _, r := range rows {
			out = append(out, orderedFromMap(r, fields))
		}
		return writeStructured(w, out, opts)
	}

	if len(rows) == 0 {
		_, err := fmt.Fprintln(w, "No resources match the filter.")
		return err
	}

	// Default text mode: address-per-line for backward compat with the
	// pre-fields behavior (and matches user muscle memory for a "give me
	// the list" query). Multi-field requests get a TSV with header.
	if len(fields) == 1 {
		for _, r := range rows {
			if _, err := fmt.Fprintln(w, r[fields[0]]); err != nil {
				return err
			}
		}
		return nil
	}
	if _, err := fmt.Fprintln(w, tsvHeader(fields)); err != nil {
		return err
	}
	for _, r := range rows {
		if _, err := fmt.Fprintln(w, strings.Join(projectRow(r, fields), "\t")); err != nil {
			return err
		}
	}
	return nil
}

// ResourceRow is one entry in [ResourcesResult]. Synthetic tagging
// failures (addresses that match a tagging-policy failure but don't
// appear in any project's resource list) populate only the Address
// field; the other fields stay zero / nil.
type ResourceRow struct {
	Address     string   `json:"address"`
	Type        string   `json:"type,omitempty"`
	Project     string   `json:"project,omitempty"`
	MonthlyCost *rat.Rat `json:"monthly_cost,omitempty"`
	IsFree      bool     `json:"is_free,omitempty"`
}

// ResourcesResult is the typed return of [ResourcesFor]. Currency
// travels on the envelope so MCP callers don't need to interpret the
// rat.Rat MonthlyCost strings without context. Count is the length of
// the Resources slice, exposed up front so an LLM can read "did this
// match anything?" without parsing the list.
type ResourcesResult struct {
	Currency  string        `json:"currency"`
	Resources []ResourceRow `json:"resources"`
	Count     int           `json:"count"`
}

// ResourcesFor applies the inspect filter pipeline and the
// resource-shaped predicates (MissingTag / InvalidTag / MinCost /
// MaxCost) and returns the deduped, alphabetically-sorted list of
// matching resources. Pairs with the `inspect_resources` MCP tool's
// flat mode.
//
// selectFilteredResources (used by the CLI's --json / TSV renderer)
// projects this typed result to its existing []map[string]string shape
// so the two surfaces stay byte-identical.
func ResourcesFor(data *format.Output, opts Options) ResourcesResult {
	if err := ParseFilter(opts.Filter, &opts); err == nil {
		data = Filter(data, opts)
	}

	seen := map[string]struct{}{}
	var rows []ResourceRow

	add := func(row ResourceRow) {
		if row.Address == "" {
			return
		}
		if _, ok := seen[row.Address]; ok {
			return
		}
		seen[row.Address] = struct{}{}
		rows = append(rows, row)
	}

	rowFor := func(p format.ProjectOutput, r format.ResourceOutput) ResourceRow {
		return ResourceRow{
			Address:     r.Name,
			Type:        r.Type,
			Project:     p.ProjectName,
			MonthlyCost: ResourceCost(&r),
			IsFree:      r.IsFree,
		}
	}
	resByAddress := map[string]struct {
		project  format.ProjectOutput
		resource format.ResourceOutput
	}{}
	for _, p := range data.Projects {
		for _, r := range p.Resources {
			resByAddress[r.Name] = struct {
				project  format.ProjectOutput
				resource format.ResourceOutput
			}{p, r}
		}
	}
	emit := func(addr string) {
		if addr == "" {
			return
		}
		if hit, ok := resByAddress[addr]; ok {
			add(rowFor(hit.project, hit.resource))
			return
		}
		// Tagging failures may include addresses we don't have a
		// matching resource record for (synthetic entries); fall back
		// to address-only.
		add(ResourceRow{Address: addr})
	}

	if opts.MissingTag != "" {
		for _, p := range data.Projects {
			for _, r := range p.Resources {
				v, ok := r.Tags[opts.MissingTag]
				if !ok || v == "" {
					add(rowFor(p, r))
				}
			}
		}
	}

	if opts.InvalidTag != "" {
		for _, p := range data.Projects {
			for _, t := range p.TaggingResults {
				for _, fr := range t.FailingResources {
					for _, inv := range fr.InvalidTags {
						if inv.Key == opts.InvalidTag && inv.Value != "" {
							emit(fr.Address)
							break
						}
					}
				}
			}
		}
	}

	if opts.MinCost > 0 || opts.MaxCost > 0 {
		for _, p := range data.Projects {
			for _, r := range p.Resources {
				cost := ResourceCost(&r).Float64()
				if opts.MinCost > 0 && cost < opts.MinCost {
					continue
				}
				if opts.MaxCost > 0 && cost > opts.MaxCost {
					continue
				}
				add(rowFor(p, r))
			}
		}
	}

	sort.Slice(rows, func(i, j int) bool { return rows[i].Address < rows[j].Address })
	return ResourcesResult{Currency: data.Currency, Resources: rows, Count: len(rows)}
}

// selectFilteredResources projects [ResourcesFor]'s typed result to the
// []map[string]string shape the CLI's --json and TSV renderers consume.
func selectFilteredResources(data *format.Output, opts Options) []map[string]string {
	result := ResourcesFor(data, opts)
	out := make([]map[string]string, 0, len(result.Resources))
	for _, r := range result.Resources {
		row := map[string]string{"address": r.Address}
		if r.Type != "" {
			row["type"] = r.Type
		}
		if r.Project != "" {
			row["project"] = r.Project
		}
		if r.MonthlyCost != nil {
			row["monthly_cost"] = humanMoney(r.MonthlyCost, result.Currency)
			row["is_free"] = fmt.Sprintf("%v", r.IsFree)
		}
		out = append(out, row)
	}
	return out
}
