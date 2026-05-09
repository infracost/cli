package views

import (
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/infracost/cli/internal/format"
	"github.com/infracost/cli/internal/inspect"
	"github.com/infracost/cli/internal/tui/styles"
	"github.com/infracost/cli/internal/ui"
	"github.com/infracost/go-proto/pkg/rat"
)

// ResourceRow is one row in the list — a flattened view of a resource that
// also keeps a pointer back to the structured ResourceOutput so the detail
// pane can drill in without re-walking the Output tree.
type ResourceRow struct {
	Project  string
	Address  string
	Type     string
	FileLoc  string
	Cost     *rat.Rat
	Resource *format.ResourceOutput

	// Issues are the failing FinOps/tagging policies that flagged this
	// resource. Empty for resources with no failures. Drives the ⚠️
	// indicator in the list and the issues section in the detail pane.
	Issues ResourceIssues
}

// ResourceIssues carries the per-resource failing-policy facts the detail
// pane renders. We pre-compute these once at row-build time so navigation
// doesn't have to re-walk the project's policy results.
type ResourceIssues struct {
	Finops  []FailingFinopsPolicy
	Tagging []FailingTaggingPolicy
}

// Any reports whether the resource has at least one failing policy of
// any kind. Equivalent to len(Finops)+len(Tagging) > 0.
func (r ResourceIssues) Any() bool {
	return len(r.Finops) > 0 || len(r.Tagging) > 0
}

// FailingFinopsPolicy bundles a FinOps policy with the issues it raised
// against one specific resource.
type FailingFinopsPolicy struct {
	PolicyName    string
	PolicyMessage string
	Issues        []format.FinopsIssueOutput
}

// FailingTaggingPolicy bundles a tagging policy with the failures it
// raised against one specific resource.
type FailingTaggingPolicy struct {
	PolicyName       string
	PolicyMessage    string
	InvalidTags      []format.InvalidTagOutput
	MissingMandatory []string
}

// List is a scrollable resource list. It owns a manual scroll offset
// rather than a viewport because the top/bottom rows of the visible
// window are special — they get replaced with "N more above/below"
// indicators when scrolling is possible. Doing that with bubbles/viewport
// would require post-processing the rendered viewport string, which is
// brittle.
type List struct {
	rows     []ResourceRow
	currency string

	cursor int
	offset int // index of the first visible row
	width  int
	height int // visible row count
}

// NewList constructs an empty list.
func NewList() List { return List{} }

// SetSize updates the list's pane dimensions; call on tea.WindowSizeMsg.
func (l *List) SetSize(width, height int) {
	l.width = width
	l.height = height
	l.clampOffset()
}

// SetData replaces the row set and resets the cursor.
func (l *List) SetData(rows []ResourceRow, currency string) {
	l.rows = rows
	l.currency = currency
	if l.cursor >= len(l.rows) {
		l.cursor = 0
	}
	l.offset = 0
	l.clampOffset()
}

// Selected returns the row under the cursor, or nil when the list is empty.
func (l *List) Selected() *ResourceRow {
	if len(l.rows) == 0 || l.cursor < 0 || l.cursor >= len(l.rows) {
		return nil
	}
	return &l.rows[l.cursor]
}

// Update applies a key event to the list. Returns nil — the list owns
// its own scroll, so there's nothing to forward.
func (l *List) Update(msg tea.Msg) tea.Cmd {
	km, ok := msg.(tea.KeyMsg)
	if !ok {
		return nil
	}
	switch km.String() {
	case "up", "k":
		if l.cursor > 0 {
			l.cursor--
			l.scrollToCursor()
		}
	case "down", "j":
		if l.cursor < len(l.rows)-1 {
			l.cursor++
			l.scrollToCursor()
		}
	case "home", "g":
		l.cursor = 0
		l.scrollToCursor()
	case "end", "G":
		l.cursor = len(l.rows) - 1
		if l.cursor < 0 {
			l.cursor = 0
		}
		l.scrollToCursor()
	case "pgup":
		l.cursor -= l.height
		if l.cursor < 0 {
			l.cursor = 0
		}
		l.scrollToCursor()
	case "pgdown":
		l.cursor += l.height
		if l.cursor >= len(l.rows) {
			l.cursor = len(l.rows) - 1
		}
		l.scrollToCursor()
	}
	return nil
}

// View renders the list pane as a fixed-height block. The first row is
// replaced with a "↑ N more above" indicator when there's content above
// the visible window; the last row likewise becomes "↓ N more below"
// when there's content below. This makes the scroll affordance obvious
// without dedicated chrome rows that would steal space from the data.
func (l List) View() string {
	if l.width <= 0 || l.height <= 0 {
		return ""
	}
	if len(l.rows) == 0 {
		return styles.Muted().Render("  No resources to display.")
	}

	costWidth := 12
	// Reserve a column for the ⚠️ issue indicator. Width is computed
	// dynamically because emoji presentation can vary by terminal —
	// uniseg (used by ui.PrintableWidth) reports the right value.
	iconWidth := ui.PrintableWidth(issueIcon())
	addressWidth := l.width - costWidth - iconWidth - 3
	if addressWidth < 16 {
		addressWidth = 16
	}

	above := l.offset
	end := l.offset + l.height
	if end > len(l.rows) {
		end = len(l.rows)
	}
	below := len(l.rows) - end

	var lines []string
	for i := l.offset; i < end; i++ {
		lines = append(lines, l.renderRow(l.rows[i], i, addressWidth, costWidth))
	}

	// Pad the visible window to exactly l.height rows so the bordered
	// container around the list always has a stable height regardless of
	// the underlying row count.
	for len(lines) < l.height {
		lines = append(lines, strings.Repeat(" ", l.width))
	}

	// Substitute scroll indicators *after* padding so they always land
	// on the actual top/bottom rows of the rendered block.
	if above > 0 && len(lines) > 0 {
		lines[0] = scrollIndicator("↑ "+pluralize(above, "more resource above", "more resources above"), l.width)
	}
	if below > 0 && len(lines) > 0 {
		lines[len(lines)-1] = scrollIndicator("↓ "+pluralize(below, "more resource below", "more resources below"), l.width)
	}

	return strings.Join(lines, "\n")
}

// renderRow formats one resource row at full width. The selected row is
// highlighted via styles.ListSelected.
func (l List) renderRow(row ResourceRow, idx, addressWidth, costWidth int) string {
	icon := strings.Repeat(" ", ui.PrintableWidth(issueIcon()))
	if row.Issues.Any() {
		icon = issueIconRendered()
	}

	address := inspect.TruncateMiddle(row.Address, addressWidth)
	address = padRight(address, addressWidth)

	cost := inspect.HumanMoney(row.Cost, l.currency)
	costCell := padLeft(cost+"/mo", costWidth)

	line := icon + " " + address + "  " + costCell
	if idx == l.cursor {
		line = styles.ListSelected().Render(line)
	}
	return line
}

// scrollIndicator centers msg within width columns and renders it in a
// muted italic style so it's visually distinct from the data rows.
func scrollIndicator(msg string, width int) string {
	style := lipgloss.NewStyle().Foreground(lipgloss.Color("#737487")).Italic(true)
	rendered := style.Render(msg)
	pad := (width - ui.PrintableWidth(rendered)) / 2
	if pad < 0 {
		pad = 0
	}
	right := width - pad - ui.PrintableWidth(rendered)
	if right < 0 {
		right = 0
	}
	return strings.Repeat(" ", pad) + rendered + strings.Repeat(" ", right)
}

// pluralize returns the singular form when n == 1, otherwise the plural.
// "1 more resource above" reads better than "1 more resources above".
func pluralize(n int, singular, plural string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, singular)
	}
	return fmt.Sprintf("%d %s", n, plural)
}

// scrollToCursor adjusts offset so the cursor stays visible. When
// indicators are showing on the top/bottom rows, the cursor needs to
// fit within the inner [offset+1 : offset+height-1] window so it
// doesn't get hidden behind an indicator.
func (l *List) scrollToCursor() {
	if l.height <= 0 {
		return
	}
	// Reserve a row for the top indicator if there's content above, and
	// for the bottom indicator if there's content below — those rows
	// can't be the cursor.
	topPad := 0
	bottomPad := 0
	if l.offset > 0 {
		topPad = 1
	}
	if l.offset+l.height < len(l.rows) {
		bottomPad = 1
	}
	if l.cursor < l.offset+topPad {
		l.offset = l.cursor - topPad
	}
	if l.cursor >= l.offset+l.height-bottomPad {
		l.offset = l.cursor - l.height + 1 + bottomPad
	}
	l.clampOffset()
}

// clampOffset keeps offset within [0, max]. Called after any change to
// the row count or pane height.
func (l *List) clampOffset() {
	if l.offset < 0 {
		l.offset = 0
	}
	max := len(l.rows) - l.height
	if max < 0 {
		max = 0
	}
	if l.offset > max {
		l.offset = max
	}
}

// padRight pads s on the right with spaces to width visible columns.
func padRight(s string, width int) string {
	pad := width - ui.PrintableWidth(s)
	if pad <= 0 {
		return s
	}
	return s + strings.Repeat(" ", pad)
}

// padLeft pads s on the left with spaces to width visible columns.
func padLeft(s string, width int) string {
	pad := width - ui.PrintableWidth(s)
	if pad <= 0 {
		return s
	}
	return strings.Repeat(" ", pad) + s
}

// RowsFromOutput flattens an Output into a sorted-by-cost-desc row list.
// Sort is stable across re-renders so cursor navigation is predictable.
// Includes every resource (costed and free) — the list view is responsible
// for filtering when the user activates it.
func RowsFromOutput(out *format.Output) []ResourceRow {
	if out == nil {
		return nil
	}
	var rows []ResourceRow
	for _, p := range out.Projects {
		// Build a per-address index of every failing-policy fact in this
		// project once, then attach it to each resource row in the inner
		// loop. Cheaper than walking the policy results per resource and
		// keeps the detail pane from having to do its own lookups.
		issuesByAddr := failingIssuesByResource(p)
		for i := range p.Resources {
			r := &p.Resources[i]
			rows = append(rows, ResourceRow{
				Project:  p.ProjectName,
				Address:  r.Name,
				Type:     r.Type,
				FileLoc:  inspect.FormatFileLoc(r.Metadata.Filename, r.Metadata.StartLine),
				Cost:     inspect.ResourceCost(r),
				Resource: r,
				Issues:   issuesByAddr[r.Name],
			})
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		ci, cj := rows[i].Cost, rows[j].Cost
		switch {
		case ci == nil && cj == nil:
			return rows[i].Address < rows[j].Address
		case ci == nil:
			return false
		case cj == nil:
			return true
		}
		// Descending cost; ties broken by address for determinism.
		if ci.Equals(cj) {
			return rows[i].Address < rows[j].Address
		}
		return ci.GreaterThan(cj)
	})
	return rows
}

// failingIssuesByResource indexes every failing FinOps/tagging fact in
// project p by the resource address it concerns. Each map entry can carry
// multiple FinOps and tagging policies — a single resource sometimes
// fails several policies at once.
func failingIssuesByResource(p format.ProjectOutput) map[string]ResourceIssues {
	out := map[string]ResourceIssues{}
	for _, f := range p.FinopsResults {
		for _, fr := range f.FailingResources {
			ri := out[fr.Name]
			ri.Finops = append(ri.Finops, FailingFinopsPolicy{
				PolicyName:    f.PolicyName,
				PolicyMessage: f.PolicyMessage,
				Issues:        fr.Issues,
			})
			out[fr.Name] = ri
		}
	}
	for _, t := range p.TaggingResults {
		for _, tr := range t.FailingResources {
			ri := out[tr.Address]
			ri.Tagging = append(ri.Tagging, FailingTaggingPolicy{
				PolicyName:       t.PolicyName,
				PolicyMessage:    t.Message,
				InvalidTags:      tr.InvalidTags,
				MissingMandatory: tr.MissingMandatoryTags,
			})
			out[tr.Address] = ri
		}
	}
	return out
}

// EmptyHint returns a styled placeholder string for non-IaC directories.
func EmptyHint() string {
	return fmt.Sprintf(
		"  %s\n  %s",
		styles.Bold().Render("No cached scan for this directory."),
		styles.Muted().Render("Press r to run a scan, or Tab to pick a different project."),
	)
}
