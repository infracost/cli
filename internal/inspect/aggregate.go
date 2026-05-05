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

// selectFilteredResources is the multi-field replacement for
// selectFilteredAddresses. Returns one row (map of canonical field →
// rendered string) per resource matching the predicates, in deterministic
// (alphabetical-by-address) order.
func selectFilteredResources(data *format.Output, opts Options) []map[string]string {
	type rowEntry struct {
		address string
		row     map[string]string
	}
	seen := map[string]struct{}{}
	var rows []rowEntry

	add := func(addr string, row map[string]string) {
		if addr == "" {
			return
		}
		if _, ok := seen[addr]; ok {
			return
		}
		seen[addr] = struct{}{}
		rows = append(rows, rowEntry{address: addr, row: row})
	}

	rowFor := func(p format.ProjectOutput, r format.ResourceOutput) map[string]string {
		cost := ResourceCost(&r)
		return map[string]string{
			"address":      r.Name,
			"type":         r.Type,
			"project":      p.ProjectName,
			"monthly_cost": humanMoney(cost, data.Currency),
			"is_free":      fmt.Sprintf("%v", r.IsFree),
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
			add(addr, rowFor(hit.project, hit.resource))
			return
		}
		// Tagging failures may include addresses we don't have a
		// matching resource record for (synthetic entries); fall back
		// to address-only.
		add(addr, map[string]string{"address": addr})
	}

	if opts.MissingTag != "" {
		for _, p := range data.Projects {
			for _, r := range p.Resources {
				v, ok := r.Tags[opts.MissingTag]
				if !ok || v == "" {
					add(r.Name, rowFor(p, r))
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
				add(r.Name, rowFor(p, r))
			}
		}
	}

	sort.Slice(rows, func(i, j int) bool { return rows[i].address < rows[j].address })
	out := make([]map[string]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.row)
	}
	return out
}
