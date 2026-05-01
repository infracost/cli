package inspect

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"
	"sort"
	"strings"

	"golang.org/x/text/cases"
	"golang.org/x/text/language"

	"github.com/infracost/cli/internal/format"
	"github.com/infracost/go-proto/pkg/rat"
)

type tableRow struct {
	Columns map[string]string `json:"columns"`
	Cost    *rat.Rat          `json:"cost,omitempty"`
	Count   int               `json:"count,omitempty"`
}

var detailColumns = []string{"kind", string(GroupByResource), string(GroupByFile), "message"}

func WriteGroupBy(w io.Writer, data *format.Output, opts Options) error {
	hasPolicyDim := slices.Contains(opts.GroupBy, string(GroupByPolicy))
	hasBudgetDim := slices.Contains(opts.GroupBy, string(GroupByBudget))
	hasGuardrailDim := slices.Contains(opts.GroupBy, string(GroupByGuardrail))

	var rows []tableRow
	switch {
	case hasBudgetDim:
		rows = collectBudgetRows(data)
	case hasGuardrailDim:
		rows = collectGuardrailRows(data)
	case hasPolicyDim:
		rows = collectPolicyRows(data)
	default:
		rows = collectResourceRows(data)
	}

	if opts.Resource != "" {
		rows = filterRowsByResource(rows, opts.Resource)
	}

	dims := opts.GroupBy
	aggregate := !hasPolicyDim && !hasBudgetDim && !hasGuardrailDim

	if aggregate {
		rows = aggregateRows(rows, dims)
	}

	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Cost != nil && rows[j].Cost != nil {
			return rows[i].Cost.GreaterThan(rows[j].Cost)
		}
		if len(dims) > 0 {
			return rows[i].Columns[dims[0]] < rows[j].Columns[dims[0]]
		}
		return false
	})

	if opts.Top > 0 && opts.Top < len(rows) {
		rows = rows[:opts.Top]
	}

	if opts.JSON {
		b, err := json.MarshalIndent(rows, "", "  ")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(w, string(b))
		return err
	}

	caser := cases.Title(language.English)

	// Determine columns based on the view type.
	var extraCols []string
	switch {
	case hasBudgetDim:
		extraCols = []string{"status", "actual spend", "limit", "message"}
	case hasGuardrailDim:
		extraCols = []string{"status", "Monthly Cost"}
	case hasPolicyDim:
		extraCols = detailColumns
	}

	headers := make([]string, 0, len(dims)+len(extraCols)+2)
	for _, dim := range dims {
		headers = append(headers, caser.String(dim))
	}
	if aggregate {
		headers = append(headers, "Count", "Monthly Cost")
	} else {
		for _, col := range extraCols {
			headers = append(headers, caser.String(col))
		}
	}

	return writeTable(w, headers, func(add func(row []string)) {
		for _, r := range rows {
			var vals []string
			for _, dim := range dims {
				vals = append(vals, r.Columns[dim])
			}
			if aggregate {
				vals = append(vals, fmt.Sprintf("%d", r.count()), "$"+r.Cost.StringFixed(2))
			} else {
				for _, col := range extraCols {
					if col == "Monthly Cost" && r.Cost != nil {
						vals = append(vals, "$"+r.Cost.StringFixed(2))
					} else {
						vals = append(vals, r.Columns[col])
					}
				}
			}
			add(vals)
		}
	})
}

func WriteGuardrailDetail(w io.Writer, data *format.Output, opts Options) error {
	for _, gr := range data.GuardrailResults {
		if matchesPolicy(gr.GuardrailName, gr.GuardrailID, opts.Guardrail) {
			return writeGuardrailDetail(w, data.Currency, gr)
		}
	}
	return fmt.Errorf("guardrail %q not found", opts.Guardrail)
}

func WriteBudgetDetail(w io.Writer, data *format.Output, opts Options) error {
	for _, br := range data.BudgetResults {
		if matchesPolicy(br.BudgetName, br.BudgetID, opts.Budget) {
			return writeBudgetDetail(w, data, br)
		}
	}
	return fmt.Errorf("budget %q not found", opts.Budget)
}

func WritePolicyDetail(w io.Writer, data *format.Output, opts Options) error {
	if opts.Resource != "" {
		return writePolicyResourceDetail(w, data, opts)
	}
	return writePolicyOverview(w, data, opts)
}

func collectResourceRows(data *format.Output) []tableRow {
	var rows []tableRow
	for _, p := range data.Projects {
		for _, r := range p.Resources {
			rows = append(rows, tableRow{
				Columns: map[string]string{
					string(GroupByProject):  p.ProjectName,
					string(GroupByType):     r.Type,
					string(GroupByProvider): InferProvider(r.Type),
					string(GroupByResource): r.Name,
					string(GroupByFile):     formatFileLoc(r.Metadata.Filename, r.Metadata.StartLine),
				},
				Cost: ResourceCost(&r),
			})
		}
	}
	return rows
}

func collectPolicyRows(data *format.Output) []tableRow {
	var rows []tableRow
	for _, p := range data.Projects {
		metaByName := make(map[string]format.ResourceMetadata, len(p.Resources))
		for _, r := range p.Resources {
			metaByName[r.Name] = r.Metadata
		}

		for _, f := range p.FinopsResults {
			for _, fr := range f.FailingResources {
				meta := metaByName[fr.Name]
				rows = append(rows, tableRow{
					Columns: map[string]string{
						string(GroupByProject):  p.ProjectName,
						string(GroupByPolicy):   f.PolicyName,
						"kind":                  "finops",
						string(GroupByType):     resourceTypeFromAddress(fr.Name),
						string(GroupByProvider): InferProvider(resourceTypeFromAddress(fr.Name)),
						string(GroupByResource): fr.Name,
						string(GroupByFile):     formatFileLoc(meta.Filename, meta.StartLine),
						"message":               f.PolicyMessage,
					},
				})
			}
		}
		for _, t := range p.TaggingResults {
			for _, tr := range t.FailingResources {
				rows = append(rows, tableRow{
					Columns: map[string]string{
						string(GroupByProject):  p.ProjectName,
						string(GroupByPolicy):   t.PolicyName,
						"kind":                  "tagging",
						string(GroupByType):     tr.ResourceType,
						string(GroupByProvider): InferProvider(tr.ResourceType),
						string(GroupByResource): tr.Address,
						string(GroupByFile):     formatFileLoc(tr.Path, tr.Line),
						"message":               t.Message,
					},
				})
			}
		}
	}

	return rows
}

func collectGuardrailRows(data *format.Output) []tableRow {
	rows := make([]tableRow, 0, len(data.GuardrailResults))
	for _, gr := range data.GuardrailResults {
		status := "not triggered"
		if gr.Triggered {
			status = "TRIGGERED"
		}
		rows = append(rows, tableRow{
			Columns: map[string]string{
				string(GroupByGuardrail): gr.GuardrailName,
				"status":                 status,
			},
			Cost: gr.TotalMonthlyCost,
		})
	}
	return rows
}

func collectBudgetRows(data *format.Output) []tableRow {
	rows := make([]tableRow, 0, len(data.BudgetResults))
	for _, br := range data.BudgetResults {
		status := "under"
		if br.OverBudget {
			status = "OVER"
		}
		row := tableRow{
			Columns: map[string]string{
				string(GroupByBudget): br.BudgetName,
				"status":              status,
				"limit":               "$" + br.Amount.StringFixed(2),
				"actual spend":        "$" + br.CurrentCost.StringFixed(2),
			},
			Cost: br.CurrentCost,
		}
		if br.CustomOverrunMessage != "" {
			row.Columns["message"] = br.CustomOverrunMessage
		}
		rows = append(rows, row)
	}
	return rows
}

func formatBudgetTagScope(tags []format.BudgetTagOutput) string {
	parts := make([]string, len(tags))
	for i, t := range tags {
		parts[i] = fmt.Sprintf("%s=%s", t.Key, t.Value)
	}
	return strings.Join(parts, ", ")
}

func filterRowsByResource(rows []tableRow, resource string) []tableRow {
	var filtered []tableRow
	for _, r := range rows {
		if strings.HasSuffix(r.Columns[string(GroupByResource)], resource) {
			filtered = append(filtered, r)
		}
	}
	return filtered
}

func aggregateRows(rows []tableRow, dims []string) []tableRow {
	type aggData struct {
		columns map[string]string
		cost    *rat.Rat
		count   int
	}
	groups := map[string]*aggData{}
	var order []string

	for _, r := range rows {
		key := compositeKey(r, dims)
		if g, ok := groups[key]; ok {
			g.count++
			if r.Cost != nil {
				g.cost = g.cost.Add(r.Cost)
			}
		} else {
			cost := rat.Zero
			if r.Cost != nil {
				cost = r.Cost
			}
			cols := make(map[string]string, len(dims))
			for _, d := range dims {
				cols[d] = r.Columns[d]
			}
			groups[key] = &aggData{columns: cols, cost: cost, count: 1}
			order = append(order, key)
		}
	}

	result := make([]tableRow, 0, len(groups))
	for _, key := range order {
		g := groups[key]
		result = append(result, tableRow{Columns: g.columns, Cost: g.cost, Count: g.count})
	}
	return result
}

func compositeKey(r tableRow, dims []string) string {
	parts := make([]string, len(dims))
	for i, d := range dims {
		parts[i] = r.Columns[d]
	}
	return strings.Join(parts, "\x00")
}

func (r tableRow) count() int {
	if r.Count > 0 {
		return r.Count
	}
	return 1
}

func writePolicyOverview(w io.Writer, data *format.Output, opts Options) error {
	for _, p := range data.Projects {
		for _, f := range p.FinopsResults {
			if !matchesPolicy(f.PolicyName, f.PolicySlug, opts.Policy) {
				continue
			}
			_, _ = fmt.Fprintf(w, "Policy: %s", f.PolicyName)
			if f.PolicyMessage != "" {
				_, _ = fmt.Fprintf(w, " — %s", f.PolicyMessage)
			}
			_, _ = fmt.Fprintln(w)
			_, _ = fmt.Fprintln(w)

			if len(f.FailingResources) == 0 {
				_, _ = fmt.Fprintln(w, "No failing resources.")
				return nil
			}

			metaByName := make(map[string]format.ResourceMetadata, len(p.Resources))
			for _, r := range p.Resources {
				metaByName[r.Name] = r.Metadata
			}

			return writeTable(w, []string{"Project", "Resource", "File", "Issues"}, func(add func(row []string)) {
				for _, fr := range f.FailingResources {
					meta := metaByName[fr.Name]
					issues := fmt.Sprintf("%d issue", len(fr.Issues))
					if len(fr.Issues) != 1 {
						issues += "s"
					}
					add([]string{p.ProjectName, fr.Name, formatFileLoc(meta.Filename, meta.StartLine), issues})
				}
			})
		}
	}

	for _, p := range data.Projects {
		for _, t := range p.TaggingResults {
			if !matchesPolicy(t.PolicyName, "", opts.Policy) {
				continue
			}
			_, _ = fmt.Fprintf(w, "Policy: %s", t.PolicyName)
			if t.Message != "" {
				_, _ = fmt.Fprintf(w, " — %s", t.Message)
			}
			_, _ = fmt.Fprintln(w)
			_, _ = fmt.Fprintln(w)

			if len(t.FailingResources) == 0 {
				_, _ = fmt.Fprintln(w, "No failing resources.")
				return nil
			}

			err := writeTable(w, []string{"Project", "Resource", "File", "Issues"}, func(add func(row []string)) {
				for _, tr := range t.FailingResources {
					issueCount := len(tr.MissingMandatoryTags) + len(tr.InvalidTags)
					issues := fmt.Sprintf("%d issue", issueCount)
					if issueCount != 1 {
						issues += "s"
					}
					add([]string{p.ProjectName, tr.Address, formatFileLoc(tr.Path, tr.Line), issues})
				}
			})
			if err != nil {
				return err
			}

			tagValues := collectTagValidValues(t.FailingResources)
			if len(tagValues) > 0 {
				_, _ = fmt.Fprintln(w)
				for _, tv := range tagValues {
					_, _ = fmt.Fprintf(w, "Tag %q valid values: %s\n", tv.key, strings.Join(tv.values, ", "))
				}
			}
			return nil
		}
	}

	return fmt.Errorf("policy %q not found", opts.Policy)
}

func writePolicyResourceDetail(w io.Writer, data *format.Output, opts Options) error {
	for _, p := range data.Projects {
		for _, f := range p.FinopsResults {
			if !matchesPolicy(f.PolicyName, f.PolicySlug, opts.Policy) {
				continue
			}
			for _, fr := range f.FailingResources {
				if fr.Name != opts.Resource {
					continue
				}
				_, _ = fmt.Fprintf(w, "Policy: %s\n", f.PolicyName)
				_, _ = fmt.Fprintf(w, "Resource: %s\n", fr.Name)

				for _, r := range p.Resources {
					if r.Name == fr.Name && r.Metadata.Filename != "" {
						_, _ = fmt.Fprintf(w, "File: %s\n", formatFileLoc(r.Metadata.Filename, r.Metadata.StartLine))
						writeSnippet(w, r.Metadata.Filename, r.Metadata.StartLine, r.Metadata.EndLine)
						break
					}
				}

				_, _ = fmt.Fprintln(w)
				for _, issue := range fr.Issues {
					_, _ = fmt.Fprintf(w, "  Issue: %s\n", issue.Description)
					if issue.MonthlySavings != nil && !issue.MonthlySavings.IsZero() {
						_, _ = fmt.Fprintf(w, "  Savings: $%s/mo\n", issue.MonthlySavings.StringFixed(2))
					}
					if issue.Address != "" {
						_, _ = fmt.Fprintf(w, "  Address: %s\n", issue.Address)
					}
					if issue.Attribute != "" {
						_, _ = fmt.Fprintf(w, "  Attribute: %s\n", issue.Attribute)
					}
					_, _ = fmt.Fprintln(w)
				}
				return nil
			}
		}
	}

	for _, p := range data.Projects {
		for _, t := range p.TaggingResults {
			if !matchesPolicy(t.PolicyName, "", opts.Policy) {
				continue
			}
			for _, tr := range t.FailingResources {
				if tr.Address != opts.Resource {
					continue
				}
				_, _ = fmt.Fprintf(w, "Policy: %s\n", t.PolicyName)
				_, _ = fmt.Fprintf(w, "Resource: %s\n", tr.Address)
				if tr.Path != "" {
					_, _ = fmt.Fprintf(w, "File: %s\n", formatFileLoc(tr.Path, tr.Line))
					writeSnippet(w, tr.Path, tr.Line, 0)
				}
				_, _ = fmt.Fprintln(w)

				if len(tr.MissingMandatoryTags) > 0 {
					_, _ = fmt.Fprintf(w, "  Missing mandatory tags: %s\n", strings.Join(tr.MissingMandatoryTags, ", "))
				}
				for _, inv := range tr.InvalidTags {
					msg := fmt.Sprintf("  Invalid tag %q", inv.Key)
					if inv.Value != "" {
						msg += fmt.Sprintf(": value %q", inv.Value)
					}
					if inv.ValidRegex != "" {
						msg += fmt.Sprintf(" does not match regex %q", inv.ValidRegex)
					}
					if inv.Message != "" {
						msg += " — " + inv.Message
					}
					_, _ = fmt.Fprintln(w, msg)
					if len(inv.ValidValues) > 0 {
						_, _ = fmt.Fprintf(w, "    Valid values: %s\n", strings.Join(inv.ValidValues, ", "))
					}
					if inv.Suggestion != "" {
						_, _ = fmt.Fprintf(w, "    Suggestion: %s\n", inv.Suggestion)
					}
				}
				_, _ = fmt.Fprintln(w)
				return nil
			}
		}
	}

	return fmt.Errorf("resource %q not found for policy %q", opts.Resource, opts.Policy)
}

func writeGuardrailDetail(w io.Writer, currency string, gr format.GuardrailOutput) error {
	_, _ = fmt.Fprintf(w, "Guardrail: %s\n", gr.GuardrailName)
	if gr.TotalMonthlyCost != nil {
		_, _ = fmt.Fprintf(w, "Total monthly cost: %s%s\n", currencySymbol(currency), gr.TotalMonthlyCost.StringFixed(2))
	}
	if gr.Triggered {
		_, _ = fmt.Fprintln(w, "Status: TRIGGERED")
	} else {
		_, _ = fmt.Fprintln(w, "Status: not triggered")
	}
	return nil
}

func writeBudgetDetail(w io.Writer, data *format.Output, br format.BudgetOutput) error {
	sym := currencySymbol(data.Currency)
	_, _ = fmt.Fprintf(w, "Budget: %s\n", br.BudgetName)
	if len(br.Tags) > 0 {
		_, _ = fmt.Fprintf(w, "Scope: %s\n", formatBudgetTagScope(br.Tags))
	}
	_, _ = fmt.Fprintf(w, "Limit: %s%s\n", sym, br.Amount.StringFixed(2))
	_, _ = fmt.Fprintf(w, "Actual spend: %s%s\n", sym, br.CurrentCost.StringFixed(2))

	if br.OverBudget {
		overBy := br.CurrentCost.Sub(br.Amount)
		_, _ = fmt.Fprintf(w, "Status: OVER by %s%s\n", sym, overBy.StringFixed(2))
	} else {
		remaining := br.Amount.Sub(br.CurrentCost)
		pct := remaining.Div(br.Amount).Mul(rat.New(100))
		_, _ = fmt.Fprintf(w, "Status: %s%s remaining (%s%% left)\n", sym, remaining.StringFixed(2), pct.StringFixed(1))
	}

	if br.CustomOverrunMessage != "" {
		_, _ = fmt.Fprintf(w, "Message: %s\n", br.CustomOverrunMessage)
	}

	// Show resources in this scan that match the budget's tags.
	matching := collectMatchingResources(data, br.Tags)
	if len(matching) > 0 {
		_, _ = fmt.Fprintln(w)
		_, _ = fmt.Fprintln(w, "Resources in this scan matching budget tags:")
		_ = writeTable(w, []string{"Type", "Count", "Monthly Cost"}, func(add func([]string)) {
			for _, m := range matching {
				add([]string{m.resourceType, fmt.Sprintf("%d", m.count), sym + m.cost.StringFixed(2)})
			}
		})
	}

	// Show FinOps policy violations on matching resources.
	savings := collectBudgetSavings(data, br.Tags)
	if len(savings) > 0 {
		_, _ = fmt.Fprintln(w)
		_, _ = fmt.Fprintln(w, "FinOps policy violations on matching resources:")
		for _, s := range savings {
			_, _ = fmt.Fprintf(w, "  %s: up to %s%s/mo (%d %s)\n", s.policyName, sym, s.savings.StringFixed(2), s.resourceCount, pluralize("resource", s.resourceCount))
		}
	}

	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "Actual spend is based on cloud billing data for all resources")
	_, _ = fmt.Fprintln(w, "matching this budget's tags across the organization.")

	return nil
}

type matchingResourceGroup struct {
	resourceType string
	count        int
	cost         *rat.Rat
}

func collectMatchingResources(data *format.Output, budgetTags []format.BudgetTagOutput) []matchingResourceGroup {
	groups := map[string]*matchingResourceGroup{}
	var order []string

	for _, p := range data.Projects {
		for _, r := range p.Resources {
			if !resourceMatchesBudgetTags(r, budgetTags) {
				continue
			}
			cost := ResourceCost(&r)
			if g, ok := groups[r.Type]; ok {
				g.count++
				g.cost = g.cost.Add(cost)
			} else {
				groups[r.Type] = &matchingResourceGroup{resourceType: r.Type, count: 1, cost: cost}
				order = append(order, r.Type)
			}
		}
	}

	result := make([]matchingResourceGroup, 0, len(order))
	for _, t := range order {
		result = append(result, *groups[t])
	}
	return result
}

type budgetSaving struct {
	policyName    string
	savings       *rat.Rat
	resourceCount int
}

func collectBudgetSavings(data *format.Output, budgetTags []format.BudgetTagOutput) []budgetSaving {
	// Build set of resource names that match the budget tags.
	matchingNames := map[string]bool{}
	for _, p := range data.Projects {
		for _, r := range p.Resources {
			if resourceMatchesBudgetTags(r, budgetTags) {
				matchingNames[r.Name] = true
			}
		}
	}

	if len(matchingNames) == 0 {
		return nil
	}

	// Find finops savings on those resources.
	var results []budgetSaving
	for _, p := range data.Projects {
		for _, f := range p.FinopsResults {
			savings := rat.Zero
			count := 0
			for _, fr := range f.FailingResources {
				if !matchingNames[fr.Name] {
					continue
				}
				count++
				for _, iss := range fr.Issues {
					if iss.MonthlySavings != nil && iss.MonthlySavings.GreaterThanZero() {
						savings = savings.Add(iss.MonthlySavings)
					}
				}
			}
			if count > 0 && savings.GreaterThanZero() {
				results = append(results, budgetSaving{
					policyName:    f.PolicyName,
					savings:       savings,
					resourceCount: count,
				})
			}
		}
	}
	return results
}

func resourceMatchesBudgetTags(r format.ResourceOutput, budgetTags []format.BudgetTagOutput) bool {
	if len(r.Tags) == 0 {
		return false
	}
	for _, bt := range budgetTags {
		if v, ok := r.Tags[bt.Key]; !ok || v != bt.Value {
			return false
		}
	}
	return true
}

func pluralize(word string, count int) string {
	if count == 1 {
		return word
	}
	return word + "s"
}

func currencySymbol(code string) string {
	switch code {
	case "USD":
		return "$"
	case "EUR":
		return "€"
	case "GBP":
		return "£"
	default:
		return code + " "
	}
}

func formatFileLoc(filename string, line int) string {
	if filename == "" {
		return ""
	}
	if line > 0 {
		return fmt.Sprintf("%s:%d", filename, line)
	}
	return filename
}

func writeSnippet(w io.Writer, filename string, startLine, endLine int) {
	if startLine <= 0 {
		return
	}

	const contextLines = 3
	from := startLine
	to := endLine
	if to <= 0 {
		to = from + contextLines
	}

	if from > contextLines {
		from -= contextLines
	} else {
		from = 1
	}

	lines, err := readLines(filename, from, to)
	if err != nil {
		return
	}

	_, _ = fmt.Fprintln(w)
	for i, line := range lines {
		lineNum := from + i
		marker := " "
		if lineNum == startLine {
			marker = ">"
		}
		_, _ = fmt.Fprintf(w, "  %s %4d | %s\n", marker, lineNum, line)
	}
}

func readLines(filename string, from, to int) ([]string, error) {
	f, err := os.Open(filename) //nolint:gosec // filename is from trusted internal data
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	var lines []string
	scanner := bufio.NewScanner(f)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		if lineNum > to {
			break
		}
		if lineNum >= from {
			lines = append(lines, scanner.Text())
		}
	}
	return lines, scanner.Err()
}

func matchesPolicy(name, slug, query string) bool {
	return strings.EqualFold(name, query) || strings.EqualFold(slug, query)
}

func resourceTypeFromAddress(addr string) string {
	parts := strings.Split(addr, ".")
	if len(parts) >= 2 {
		return parts[len(parts)-2]
	}
	return addr
}

type tagValidValues struct {
	key    string
	values []string
}

func collectTagValidValues(resources []format.FailingTaggingResourceOutput) []tagValidValues {
	seen := map[string]map[string]bool{}
	var order []string

	for _, r := range resources {
		for _, inv := range r.InvalidTags {
			if len(inv.ValidValues) == 0 {
				continue
			}
			if seen[inv.Key] == nil {
				seen[inv.Key] = map[string]bool{}
				order = append(order, inv.Key)
			}
			for _, v := range inv.ValidValues {
				seen[inv.Key][v] = true
			}
		}
	}

	result := make([]tagValidValues, 0, len(order))
	for _, key := range order {
		vals := make([]string, 0, len(seen[key]))
		for v := range seen[key] {
			vals = append(vals, v)
		}
		sort.Strings(vals)
		result = append(result, tagValidValues{key: key, values: vals})
	}
	return result
}
