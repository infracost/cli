package inspect

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"slices"
	"sort"
	"strings"

	"golang.org/x/text/cases"
	"golang.org/x/text/language"

	"github.com/infracost/cli/internal/format"
	"github.com/infracost/cli/internal/ui"
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
		// `--group-by file` should fold all resources within a file into one
		// group. The file column is populated as "path:line" elsewhere (so
		// detail views can link back to the exact location); strip the line
		// suffix here so it doesn't become part of the aggregation key.
		if slices.Contains(dims, string(GroupByFile)) {
			for i := range rows {
				rows[i].Columns[string(GroupByFile)] = fileWithoutLine(rows[i].Columns[string(GroupByFile)])
			}
		}
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

	if opts.Structured() {
		return writeStructured(w, rows, opts)
	}

	// Group-by policy renders structured blocks instead of a table — the
	// policy/resource/file/message fields are all wide, so columns truncate
	// everything aggressively. Two layouts:
	//   single  --group-by policy        → one block per policy with a
	//                                      bullet list of failing resources
	//                                      (avoids repeating identical
	//                                      messages once per resource).
	//   multi   --group-by policy,X      → one block per pairing, since the
	//                                      extra dim is a real per-row axis.
	if hasPolicyDim {
		if len(dims) == 1 {
			groups := consolidatePolicyRows(rows)
			maxWidth := ui.TerminalContentWidth()
			for i, g := range groups {
				if i > 0 {
					fmt.Fprintln(w)
				}
				writeConsolidatedPolicyBlock(w, g, maxWidth)
			}
		} else {
			writePolicyGroupRows(w, rows, dims)
		}
		return nil
	}

	caser := cases.Title(language.English)

	// Build the column spec. dims are always left-aligned (they're labels).
	// Numeric/currency columns get right-aligned for easier scanning.
	var extraCols []string
	switch {
	case hasBudgetDim:
		extraCols = []string{"status", "actual spend", "limit", "message"}
	case hasGuardrailDim:
		extraCols = []string{"status", "Monthly Cost"}
	case hasPolicyDim:
		extraCols = detailColumns
	}

	cols := make([]tableCol, 0, len(dims)+len(extraCols)+2)
	for _, dim := range dims {
		cols = append(cols, tableCol{header: caser.String(dim)})
	}
	if aggregate {
		cols = append(cols,
			tableCol{header: "Count", right: true},
			tableCol{header: "Monthly Cost", right: true},
		)
	} else {
		for _, col := range extraCols {
			cols = append(cols, tableCol{
				header:        caser.String(col),
				right:         isMoneyCol(col),
				truncateRight: isProseCol(col),
			})
		}
	}

	tableRows := make([][]string, 0, len(rows))
	for _, r := range rows {
		var vals []string
		for _, dim := range dims {
			vals = append(vals, r.Columns[dim])
		}
		if aggregate {
			vals = append(vals, humanInt(r.count()), humanMoney(r.Cost, data.Currency))
		} else {
			for _, col := range extraCols {
				switch {
				case col == "Monthly Cost" && r.Cost != nil:
					vals = append(vals, humanMoney(r.Cost, data.Currency))
				case col == "status":
					vals = append(vals, statusValue(r.Columns[col]))
				default:
					vals = append(vals, r.Columns[col])
				}
			}
		}
		tableRows = append(tableRows, vals)
	}

	// Group-by views render unboxed, so the constraint is just the terminal
	// width (capped at MaxBoxWidth so very wide terminals don't sprawl).
	renderTable(w, cols, tableRows, ui.TerminalContentWidth())
	return nil
}

// writePolicyGroupRows renders the group-by-policy view as a list of
// structured blocks. Each block is:
//
//	<icon>  <Policy name>           [· extra dim values, when multi-group]
//	   <resource> · <file>
//	   <message, wrapped to terminal width>
//
// Blank line between blocks. Empty lines are skipped (e.g. no message → no
// message line). The left indent is 3 spaces so detail/message lines align
// under the policy name (after the 2-cell icon + 1 space).
func writePolicyGroupRows(w io.Writer, rows []tableRow, dims []string) {
	const indent = "   "
	maxWidth := ui.TerminalContentWidth()
	for i, r := range rows {
		if i > 0 {
			fmt.Fprintln(w)
		}

		header := kindIcon(r.Columns["kind"]) + "  " + ui.Bold(r.Columns[string(GroupByPolicy)])
		var extras []string
		for _, d := range dims {
			if d == string(GroupByPolicy) {
				continue
			}
			if v := r.Columns[d]; v != "" {
				extras = append(extras, v)
			}
		}
		if len(extras) > 0 {
			header += " " + ui.Muted("· "+strings.Join(extras, " · "))
		}
		writeWrapped(w, "", header, maxWidth)

		var details []string
		if v := r.Columns[string(GroupByResource)]; v != "" {
			details = append(details, v)
		}
		if v := r.Columns[string(GroupByFile)]; v != "" {
			details = append(details, ui.Muted(v))
		}
		if len(details) > 0 {
			writeWrapped(w, indent, strings.Join(details, ui.Muted(" · ")), maxWidth)
		}

		if msg := r.Columns["message"]; msg != "" {
			budget := 0
			if maxWidth > 0 {
				budget = maxWidth - len(indent)
			}
			wrapped := ui.WrapText(msg, budget)
			for line := range strings.SplitSeq(wrapped, "\n") {
				fmt.Fprintln(w, indent+ui.Muted(line))
			}
		}
	}
}

// consolidatedResourceCap is the per-policy bullet-list cap for
// `--group-by policy`. Beyond this, additional resources collapse into
// "…N more" plus a drill-in hint pointing the user at
// `infracost inspect --policy <name>` for the full list. The drill-in
// command preserves all resources without truncation.
const consolidatedResourceCap = 20

// policyConsolidationGroup is one policy + the resources failing it. Built
// by consolidatePolicyRows from a flat list of policy×resource pairings.
type policyConsolidationGroup struct {
	kind      string
	policy    string
	message   string
	resources []policyConsolidationResource
}

type policyConsolidationResource struct {
	name string
	file string
}

// consolidatePolicyRows groups rows by policy in first-seen order. Used by
// `--group-by policy` so a policy with N failing resources renders as one
// block with N bullets, not N near-duplicate blocks repeating the message.
func consolidatePolicyRows(rows []tableRow) []policyConsolidationGroup {
	groups := map[string]*policyConsolidationGroup{}
	var order []string
	for _, r := range rows {
		key := r.Columns["kind"] + "\x00" + r.Columns[string(GroupByPolicy)]
		res := policyConsolidationResource{
			name: r.Columns[string(GroupByResource)],
			file: r.Columns[string(GroupByFile)],
		}
		if g, ok := groups[key]; ok {
			g.resources = append(g.resources, res)
			continue
		}
		groups[key] = &policyConsolidationGroup{
			kind:      r.Columns["kind"],
			policy:    r.Columns[string(GroupByPolicy)],
			message:   r.Columns["message"],
			resources: []policyConsolidationResource{res},
		}
		order = append(order, key)
	}
	result := make([]policyConsolidationGroup, 0, len(order))
	for _, k := range order {
		result = append(result, *groups[k])
	}
	return result
}

// writeConsolidatedPolicyBlock renders one consolidated policy block:
//
//	<icon>  <Policy name>   (N resources)
//	   <wrapped message>
//
//	   • <resource> · <file>
//	   • <resource> · <file>
//	   ...
//	   …N more
//	   Run `infracost inspect --policy "..."` to see all N resources
//
// The bullet list caps at consolidatedResourceCap; the drill-in hint sends
// the user to `--policy <name>` for the uncapped per-resource view. The
// message renders once for the whole group.
func writeConsolidatedPolicyBlock(w io.Writer, g policyConsolidationGroup, maxWidth int) {
	count := len(g.resources)
	header := kindIcon(g.kind) + "  " + ui.Bold(g.policy)
	header += "  " + ui.Muted(fmt.Sprintf("(%d %s)", count, pluralize("resource", count)))
	writeWrapped(w, "", header, maxWidth)

	if g.message != "" {
		fmt.Fprintln(w)
		budget := 0
		if maxWidth > 0 {
			budget = maxWidth - 3
		}
		for line := range strings.SplitSeq(ui.WrapText(g.message, budget), "\n") {
			fmt.Fprintln(w, "   "+ui.Muted(line))
		}
	}

	if count == 0 {
		return
	}
	fmt.Fprintln(w)

	show := min(count, consolidatedResourceCap)
	for i := range show {
		r := g.resources[i]
		line := ui.Muted("•") + " " + r.name
		if r.file != "" {
			line += " " + ui.Muted("· "+r.file)
		}
		writeWrapped(w, "   ", line, maxWidth)
	}
	if count > show {
		fmt.Fprintf(w, "   %s\n", ui.Muted(fmt.Sprintf("…%d more", count-show)))
		fmt.Fprintf(w, "   %s %s %s\n",
			ui.Muted("Run"),
			ui.Code(fmt.Sprintf("`infracost inspect --policy %q`", g.policy)),
			ui.Muted(fmt.Sprintf("to see all %d resources", count)),
		)
	}
}

// kindIcon returns the 2-cell emoji prefix for a policy kind. Unknown kinds
// get two blank cells so the policy-name column still aligns under the
// header line.
func kindIcon(kind string) string {
	switch kind {
	case "finops":
		return finopsIcon
	case "tagging":
		return taggingIcon
	}
	return "  "
}

// isMoneyCol marks columns whose values are currency-formatted, so they can
// be right-aligned for easier scanning.
func isMoneyCol(col string) bool {
	switch col {
	case "Monthly Cost", "actual spend", "limit":
		return true
	}
	return false
}

// isProseCol marks columns whose values are free-text (a description, an
// error message), where suffix truncation reads more naturally than middle
// truncation. Identifier-shaped columns (resource, file, type) keep the
// default middle truncation so both ends survive a shrink.
func isProseCol(col string) bool {
	switch col {
	case "message":
		return true
	}
	return false
}

// statusValue colorizes the status column for guardrail/budget rows so
// "TRIGGERED" / "OVER" pop in red and the benign cases stay muted.
func statusValue(s string) string {
	switch s {
	case "TRIGGERED":
		return ui.Danger(stopEmoji + " " + s)
	case "OVER":
		return ui.Danger(moneyEmoji + " " + s)
	case "not triggered", "under":
		return ui.Muted(s)
	}
	return s
}

func WriteGuardrailDetail(w io.Writer, data *format.Output, opts Options) error {
	for _, gr := range data.GuardrailResults {
		if matchesPolicy(gr.GuardrailName, gr.GuardrailID, opts.Guardrail) {
			if opts.Structured() {
				return writeStructured(w, gr, opts)
			}
			return writeGuardrailDetail(w, data.Currency, gr)
		}
	}
	return fmt.Errorf("guardrail %q not found", opts.Guardrail)
}

func WriteBudgetDetail(w io.Writer, data *format.Output, opts Options) error {
	for _, br := range data.BudgetResults {
		if matchesPolicy(br.BudgetName, br.BudgetID, opts.Budget) {
			if opts.Structured() {
				return writeStructured(w, buildBudgetDetailJSON(data, br), opts)
			}
			return writeBudgetDetail(w, data, br)
		}
	}
	return fmt.Errorf("budget %q not found", opts.Budget)
}

func WritePolicyDetail(w io.Writer, data *format.Output, opts Options) error {
	if opts.Structured() {
		return writePolicyDetailJSON(w, data, opts)
	}
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
				"limit":               humanMoney(br.Amount, data.Currency),
				"actual spend":        humanMoney(br.CurrentCost, data.Currency),
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
	// FinOps branch — aggregate matched resources across ALL projects, then
	// render once. Each resource becomes a structured block: header with
	// project/resource/file, indented issue descriptions with savings.
	type finopsResource struct {
		project string
		name    string
		file    string
		issues  []format.FinopsIssueOutput
	}
	var (
		finopsName, finopsMessage string
		finopsResources           []finopsResource
		finopsMatched             bool
	)
	for _, p := range data.Projects {
		for _, f := range p.FinopsResults {
			if !matchesPolicy(f.PolicyName, f.PolicySlug, opts.Policy) {
				continue
			}
			finopsMatched = true
			finopsName = f.PolicyName
			finopsMessage = f.PolicyMessage

			metaByName := make(map[string]format.ResourceMetadata, len(p.Resources))
			for _, r := range p.Resources {
				metaByName[r.Name] = r.Metadata
			}
			for _, fr := range f.FailingResources {
				meta := metaByName[fr.Name]
				finopsResources = append(finopsResources, finopsResource{
					project: p.ProjectName,
					name:    fr.Name,
					file:    formatFileLoc(meta.Filename, meta.StartLine),
					issues:  fr.Issues,
				})
			}
		}
	}
	if finopsMatched {
		var inner bytes.Buffer
		writePolicyHeading(&inner, finopsName, finopsMessage)
		if len(finopsResources) == 0 {
			fmt.Fprintln(&inner, ui.Positive("✓ No failing resources."))
		} else {
			maxWidth := ui.ContentWidth()
			for i, r := range finopsResources {
				if i > 0 {
					fmt.Fprintln(&inner)
				}
				writeResourceHeader(&inner, r.project, r.name, r.file, maxWidth)
				for _, issue := range r.issues {
					content := issue.Description
					if issue.MonthlySavings != nil && !issue.MonthlySavings.IsZero() {
						content += " " + ui.Muted(fmt.Sprintf("— savings %s/mo", humanMoney(issue.MonthlySavings, data.Currency)))
					}
					writeWrapped(&inner, "   ", content, maxWidth)
				}
			}
		}
		_, err := fmt.Fprint(w, ui.Box(inner.String()))
		return err
	}

	// Tagging branch — same aggregation pattern. Per-resource block shows
	// missing tags + invalid-tag detail (with the regex/values that were
	// expected). Tag valid-values footer comes after as a quick reference.
	type taggingResource struct {
		project     string
		address     string
		file        string
		missingTags []string
		invalidTags []format.InvalidTagOutput
	}
	var (
		tagName, tagMessage string
		taggingResources    []taggingResource
		tagMatched          bool
		tagSchemas          []format.TagSchemaEntry
	)
	for _, p := range data.Projects {
		for _, t := range p.TaggingResults {
			if !matchesPolicy(t.PolicyName, "", opts.Policy) {
				continue
			}
			tagMatched = true
			tagName = t.PolicyName
			tagMessage = t.Message
			tagSchemas = append(tagSchemas, t.TagSchema...)

			for _, tr := range t.FailingResources {
				taggingResources = append(taggingResources, taggingResource{
					project:     p.ProjectName,
					address:     tr.Address,
					file:        formatFileLoc(tr.Path, tr.Line),
					missingTags: tr.MissingMandatoryTags,
					invalidTags: tr.InvalidTags,
				})
			}
		}
	}
	if tagMatched {
		mergedSchema := mergeTagSchemas(tagSchemas)
		schemaLookup := tagSchemaLookup(mergedSchema)

		var inner bytes.Buffer
		writePolicyHeading(&inner, tagName, tagMessage)
		if len(taggingResources) == 0 {
			fmt.Fprintln(&inner, ui.Positive("✓ No failing resources."))
		} else {
			maxWidth := ui.ContentWidth()
			for i, r := range taggingResources {
				if i > 0 {
					fmt.Fprintln(&inner)
				}
				writeResourceHeader(&inner, r.project, r.address, r.file, maxWidth)
				if len(r.missingTags) > 0 {
					content := ui.Accent("Missing:") + " " + strings.Join(r.missingTags, ", ")
					writeWrapped(&inner, "   ", content, maxWidth)
				}
				for _, inv := range r.invalidTags {
					writeInvalidTagLine(&inner, inv, schemaLookup, maxWidth)
				}
			}
		}

		if len(mergedSchema) > 0 {
			printed := false
			maxWidth := ui.ContentWidth()
			for _, s := range mergedSchema {
				if len(s.ValidValues) == 0 {
					continue
				}
				if !printed {
					fmt.Fprintln(&inner)
					printed = true
				}
				content := fmt.Sprintf("%s valid values: %s", ui.Accent("Tag "+s.Key), strings.Join(s.ValidValues, ", "))
				writeWrapped(&inner, "", content, maxWidth)
			}
		}

		_, err := fmt.Fprint(w, ui.Box(inner.String()))
		return err
	}

	return fmt.Errorf("policy %q not found", opts.Policy)
}

// writeResourceHeader writes the `project · resource · file` line for a
// per-resource block. Resource is bold (the eye anchor); separators and file
// muted; project rendered in default color since it's typically short and
// repeats a lot. Wraps to maxWidth when it would overflow — natural break
// points are the " · " token boundaries.
func writeResourceHeader(w io.Writer, project, resource, file string, maxWidth int) {
	parts := []string{project, ui.Bold(resource)}
	if file != "" {
		parts = append(parts, ui.Muted(file))
	}
	header := strings.Join(parts, ui.Muted(" · "))
	writeWrapped(w, "", header, maxWidth)
}

// writeInvalidTagLine renders one invalid-tag detail line with the offending
// value and why it's invalid (regex mismatch, custom message, etc). Wraps
// to maxWidth so long regex patterns don't overflow narrow terminals. The
// schema lookup carries the per-key validation metadata that lives on
// TaggingOutput.TagSchema (no longer duplicated on each InvalidTag).
func writeInvalidTagLine(w io.Writer, inv format.InvalidTagOutput, schema map[string]format.TagSchemaEntry, maxWidth int) {
	content := fmt.Sprintf("%s %s = %q",
		ui.Accent("Invalid"),
		ui.Accent(inv.Key),
		inv.Value,
	)
	s := schema[inv.Key]
	switch {
	case s.ValidRegex != "":
		content += " " + ui.Muted(fmt.Sprintf("— does not match regex %q", s.ValidRegex))
	case s.Message != "":
		content += " " + ui.Muted("— "+s.Message)
	}
	writeWrapped(w, "   ", content, maxWidth)
	if inv.Suggestion != "" {
		writeWrapped(w, "      ", ui.Muted("Suggestion:")+" "+inv.Suggestion, maxWidth)
	}
}

// writePolicyHeading writes the bold policy title and optional message line,
// followed by a blank line. Used by both the FinOps and Tagging branches.
// The message wraps to the box's content width so long descriptions (often
// containing markdown links) don't overflow the box border.
//
// Each wrapped line is muted independently. Wrapping the entire multi-line
// string with a single ui.Muted() leaves intermediate lines uncolored —
// Box.split-on-newline drops the inline ANSI codes that only sit at the
// start and end of the original string.
func writePolicyHeading(w io.Writer, name, message string) {
	fmt.Fprintln(w, ui.Bold("Policy: "+name))
	if message != "" {
		for line := range strings.SplitSeq(ui.WrapText(message, ui.ContentWidth()), "\n") {
			fmt.Fprintln(w, ui.Muted(line))
		}
	}
	fmt.Fprintln(w)
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
				var inner bytes.Buffer
				writePolicyResourceHeader(&inner, f.PolicyName, fr.Name)

				for _, r := range p.Resources {
					if r.Name == fr.Name && r.Metadata.Filename != "" {
						fmt.Fprintf(&inner, "%s %s\n", ui.Accent("File:"), formatFileLoc(r.Metadata.Filename, r.Metadata.StartLine))
						writeSnippet(&inner, r.Metadata.Filename, r.Metadata.StartLine, r.Metadata.EndLine)
						break
					}
				}

				for _, issue := range fr.Issues {
					fmt.Fprintln(&inner)
					rows := []kvRow{{"Issue", issue.Description}}
					if issue.MonthlySavings != nil && !issue.MonthlySavings.IsZero() {
						rows = append(rows, kvRow{"Savings", humanDollar(issue.MonthlySavings) + "/mo"})
					}
					if issue.Address != "" {
						rows = append(rows, kvRow{"Address", issue.Address})
					}
					if issue.Attribute != "" {
						rows = append(rows, kvRow{"Attribute", issue.Attribute})
					}
					writeKV(&inner, rows)
				}

				_, err := fmt.Fprint(w, ui.Box(inner.String()))
				return err
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
				var inner bytes.Buffer
				writePolicyResourceHeader(&inner, t.PolicyName, tr.Address)

				if tr.Path != "" {
					fmt.Fprintf(&inner, "%s %s\n", ui.Accent("File:"), formatFileLoc(tr.Path, tr.Line))
					writeSnippet(&inner, tr.Path, tr.Line, 0)
				}
				if len(tr.MissingMandatoryTags) > 0 || len(tr.InvalidTags) > 0 {
					fmt.Fprintln(&inner)
				}

				if len(tr.MissingMandatoryTags) > 0 {
					fmt.Fprintf(&inner, "%s %s\n", ui.Accent("Missing mandatory tags:"), strings.Join(tr.MissingMandatoryTags, ", "))
				}
				schemaLookup := tagSchemaLookup(t.TagSchema)
				for _, inv := range tr.InvalidTags {
					s := schemaLookup[inv.Key]
					msg := fmt.Sprintf("%s %q", ui.Accent("Invalid tag"), inv.Key)
					if inv.Value != "" {
						msg += fmt.Sprintf(": value %q", inv.Value)
					}
					if s.ValidRegex != "" {
						msg += fmt.Sprintf(" does not match regex %q", s.ValidRegex)
					}
					if s.Message != "" {
						msg += " — " + s.Message
					}
					fmt.Fprintln(&inner, msg)
					if len(s.ValidValues) > 0 {
						fmt.Fprintf(&inner, "  Valid values: %s\n", strings.Join(s.ValidValues, ", "))
					}
					if inv.Suggestion != "" {
						fmt.Fprintf(&inner, "  Suggestion: %s\n", inv.Suggestion)
					}
				}

				_, err := fmt.Fprint(w, ui.Box(inner.String()))
				return err
			}
		}
	}

	return fmt.Errorf("resource %q not found for policy %q", opts.Resource, opts.Policy)
}

// writePolicyResourceHeader writes the policy + resource title block. It
// deliberately leaves NO trailing blank — callers add separators (snippet's
// leading blank, issue loop's blank) so we don't double up.
func writePolicyResourceHeader(w io.Writer, policy, resource string) {
	fmt.Fprintln(w, ui.Bold("Policy: "+policy))
	fmt.Fprintln(w, ui.Muted("Resource: "+resource))
}

func writeGuardrailDetail(w io.Writer, currency string, gr format.GuardrailOutput) error {
	var inner bytes.Buffer
	fmt.Fprintln(&inner, ui.Bold("Guardrail: "+gr.GuardrailName))
	fmt.Fprintln(&inner)

	rows := []kvRow{}
	if gr.TotalMonthlyCost != nil {
		rows = append(rows, kvRow{"Total monthly cost", humanMoney(gr.TotalMonthlyCost, currency)})
	}
	if gr.Triggered {
		rows = append(rows, kvRow{"Status", ui.Danger(stopEmoji + " TRIGGERED")})
	} else {
		rows = append(rows, kvRow{"Status", ui.Positive("✓ not triggered")})
	}
	writeKV(&inner, rows)

	_, err := fmt.Fprint(w, ui.Box(inner.String()))
	return err
}

func writeBudgetDetail(w io.Writer, data *format.Output, br format.BudgetOutput) error {
	var inner bytes.Buffer
	fmt.Fprintln(&inner, ui.Bold("Budget: "+br.BudgetName))
	fmt.Fprintln(&inner)

	rows := []kvRow{}
	if len(br.Tags) > 0 {
		rows = append(rows, kvRow{"Scope", formatBudgetTagScope(br.Tags)})
	}
	rows = append(rows,
		kvRow{"Limit", humanMoney(br.Amount, data.Currency)},
		kvRow{"Actual spend", humanMoney(br.CurrentCost, data.Currency)},
		kvRow{"Status", budgetStatusValue(br, data.Currency)},
	)
	if br.CustomOverrunMessage != "" {
		rows = append(rows, kvRow{"Message", br.CustomOverrunMessage})
	}
	writeKV(&inner, rows)

	matching := collectMatchingResources(data, br.Tags)
	if len(matching) > 0 {
		fmt.Fprintln(&inner)
		fmt.Fprintln(&inner, ui.Bold("Matching resources"))
		fmt.Fprintln(&inner)
		matchingRows := make([][]string, 0, len(matching))
		for _, m := range matching {
			matchingRows = append(matchingRows, []string{m.resourceType, humanInt(m.count), humanMoney(m.cost, data.Currency)})
		}
		renderTable(&inner, []tableCol{
			{header: "Type"},
			{header: "Count", right: true},
			{header: "Monthly Cost", right: true},
		}, matchingRows, ui.ContentWidth())
	}

	savings := collectBudgetSavings(data, br.Tags)
	if len(savings) > 0 {
		fmt.Fprintln(&inner)
		fmt.Fprintln(&inner, ui.Bold("FinOps violations on matching resources"))
		fmt.Fprintln(&inner)
		for _, s := range savings {
			fmt.Fprintf(&inner, "  %s: up to %s/mo (%s %s)\n",
				ui.Accent(s.policyName),
				humanMoney(s.savings, data.Currency),
				humanInt(s.resourceCount),
				pluralize("resource", s.resourceCount),
			)
		}
	}

	fmt.Fprintln(&inner)
	fmt.Fprintln(&inner, ui.Muted("Actual spend is org-wide cloud billing across all resources"))
	fmt.Fprintln(&inner, ui.Muted("matching this budget's tags — not just the IaC scan."))

	_, err := fmt.Fprint(w, ui.Box(inner.String()))
	return err
}

// budgetStatusValue renders the colored status pill for a budget row.
//   over-budget → red 💸 "OVER by $X"
//   under       → green ✓ "$X remaining (Y% left)"
func budgetStatusValue(br format.BudgetOutput, currency string) string {
	if br.OverBudget {
		overBy := br.CurrentCost.Sub(br.Amount)
		return ui.Danger(fmt.Sprintf("%s OVER by %s", moneyEmoji, humanMoney(overBy, currency)))
	}
	remaining := br.Amount.Sub(br.CurrentCost)
	pct := remaining.Div(br.Amount).Mul(rat.New(100))
	return ui.Positive(fmt.Sprintf("✓ %s remaining (%s%% left)", humanMoney(remaining, currency), pct.StringFixed(1)))
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
	if strings.HasSuffix(word, "y") {
		return word[:len(word)-1] + "ies"
	}
	return word + "s"
}

// fileWithoutLine drops a trailing ":<digits>" line suffix from a file
// location string, e.g. "data/main.tf:42" → "data/main.tf". Strings without
// such a suffix (no colon, or non-numeric tail) are returned unchanged so
// it's safe to call on either form.
func fileWithoutLine(s string) string {
	i := strings.LastIndex(s, ":")
	if i < 0 {
		return s
	}
	suffix := s[i+1:]
	if suffix == "" {
		return s
	}
	for _, c := range suffix {
		if c < '0' || c > '9' {
			return s
		}
	}
	return s[:i]
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

// mergeTagSchemas collapses TagSchema slices from multiple TaggingOutput
// instances (the same policy can appear once per project) into a single
// per-key list. Within a single TaggingOutput each key already appears once,
// but across projects we may see the same key repeated; we union the valid
// values and OR-merge the mandatory flag.
func mergeTagSchemas(schemas []format.TagSchemaEntry) []format.TagSchemaEntry {
	if len(schemas) == 0 {
		return nil
	}
	type acc struct {
		regex     string
		message   string
		mandatory bool
		values    map[string]struct{}
	}
	byKey := map[string]*acc{}
	var order []string
	for _, s := range schemas {
		a, ok := byKey[s.Key]
		if !ok {
			a = &acc{values: map[string]struct{}{}}
			byKey[s.Key] = a
			order = append(order, s.Key)
		}
		if a.regex == "" {
			a.regex = s.ValidRegex
		}
		if a.message == "" {
			a.message = s.Message
		}
		if s.Mandatory {
			a.mandatory = true
		}
		for _, v := range s.ValidValues {
			a.values[v] = struct{}{}
		}
	}
	out := make([]format.TagSchemaEntry, 0, len(order))
	for _, k := range order {
		a := byKey[k]
		entry := format.TagSchemaEntry{
			Key:        k,
			ValidRegex: a.regex,
			Message:    a.message,
			Mandatory:  a.mandatory,
		}
		if len(a.values) > 0 {
			vals := make([]string, 0, len(a.values))
			for v := range a.values {
				vals = append(vals, v)
			}
			sort.Strings(vals)
			entry.ValidValues = vals
		}
		out = append(out, entry)
	}
	return out
}

// tagSchemaLookup builds a key→entry map for fast per-tag schema lookups
// when rendering invalid-tag detail lines.
func tagSchemaLookup(schemas []format.TagSchemaEntry) map[string]format.TagSchemaEntry {
	out := make(map[string]format.TagSchemaEntry, len(schemas))
	for _, s := range schemas {
		out[s.Key] = s
	}
	return out
}
