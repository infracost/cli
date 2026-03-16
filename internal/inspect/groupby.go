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

var detailColumns = []string{"kind", "resource", "file"}

func WriteGroupBy(w io.Writer, data *format.Output, opts Options) error {
	hasPolicyDim := slices.Contains(opts.GroupBy, "policy")

	var rows []tableRow
	if hasPolicyDim {
		rows = collectPolicyRows(data)
	} else {
		rows = collectResourceRows(data)
	}

	if opts.Resource != "" {
		rows = filterRowsByResource(rows, opts.Resource)
	}

	dims := opts.GroupBy
	aggregate := !hasPolicyDim

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
	headers := make([]string, 0, len(dims)+len(detailColumns)+2)
	for _, dim := range dims {
		headers = append(headers, caser.String(dim))
	}
	if aggregate {
		headers = append(headers, "Count", "Monthly Cost")
	} else {
		for _, col := range detailColumns {
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
				for _, col := range detailColumns {
					vals = append(vals, r.Columns[col])
				}
			}
			add(vals)
		}
	})
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
					"project":  p.ProjectName,
					"type":     r.Type,
					"provider": InferProvider(r.Type),
					"resource": r.Name,
					"file":     formatFileLoc(r.Metadata.Filename, r.Metadata.StartLine),
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
						"project":  p.ProjectName,
						"policy":   f.PolicyName,
						"kind":     "finops",
						"type":     resourceTypeFromAddress(fr.Name),
						"provider": InferProvider(resourceTypeFromAddress(fr.Name)),
						"resource": fr.Name,
						"file":     formatFileLoc(meta.Filename, meta.StartLine),
					},
				})
			}
		}
		for _, t := range p.TaggingResults {
			for _, tr := range t.FailingResources {
				rows = append(rows, tableRow{
					Columns: map[string]string{
						"project":  p.ProjectName,
						"policy":   t.PolicyName,
						"kind":     "tagging",
						"type":     tr.ResourceType,
						"provider": InferProvider(tr.ResourceType),
						"resource": tr.Address,
						"file":     formatFileLoc(tr.Path, tr.Line),
					},
				})
			}
		}
	}
	return rows
}

func filterRowsByResource(rows []tableRow, resource string) []tableRow {
	var filtered []tableRow
	for _, r := range rows {
		if strings.HasSuffix(r.Columns["resource"], resource) {
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
					if d := issue.AttributeDetail; d != nil {
						if d.ChangeKind != "" {
							_, _ = fmt.Fprintf(w, "  Change kind: %s\n", d.ChangeKind)
						}
						if d.From != nil {
							writeInstanceTypeDetail(w, "From", d.From)
						}
						if d.To != nil {
							writeInstanceTypeDetail(w, "To", d.To)
						}
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

func writeInstanceTypeDetail(w io.Writer, label string, d *format.EC2InstanceTypeDetail) {
	parts := []string{d.Value}
	if d.VCPUs > 0 {
		parts = append(parts, fmt.Sprintf("%d vCPUs", d.VCPUs))
	}
	if d.MemoryGiB > 0 {
		parts = append(parts, fmt.Sprintf("%g GiB", d.MemoryGiB))
	}
	if d.Arch != "" {
		parts = append(parts, d.Arch)
	}
	if d.NetworkGbps != "" {
		parts = append(parts, d.NetworkGbps+" Gbps")
	}
	_, _ = fmt.Fprintf(w, "  %s: %s\n", label, strings.Join(parts, ", "))
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
