package views

import (
	"strings"

	"github.com/infracost/cli/internal/tui/discovery"
)

// FilterPickerProjects returns the subset of projects whose name or
// path contains expr (case-insensitive). Used by the picker's `/`
// filter — kept narrow on purpose: project paths are short enough
// that substring matching is unambiguous, and a structured grammar
// like the resource list's key=value would be overkill here.
func FilterPickerProjects(projects []discovery.Project, expr string) []discovery.Project {
	expr = strings.ToLower(strings.TrimSpace(expr))
	if expr == "" {
		return projects
	}
	out := make([]discovery.Project, 0, len(projects))
	for _, p := range projects {
		if strings.Contains(strings.ToLower(p.Name), expr) ||
			strings.Contains(strings.ToLower(p.Path), expr) {
			out = append(out, p)
		}
	}
	return out
}
