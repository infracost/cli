package views_test

import (
	"testing"

	"github.com/infracost/cli/internal/tui/discovery"
	"github.com/infracost/cli/internal/tui/views"
	"github.com/stretchr/testify/assert"
)

func samplePickerProjects() []discovery.Project {
	return []discovery.Project{
		{Name: "infra", Path: "/Users/me/Development/infra"},
		{Name: "demo-cdk", Path: "/Users/me/Development/demo-cdk"},
		{Name: "experiments", Path: "/Users/me/personal/experiments"},
	}
}

func projectNames(projects []discovery.Project) []string {
	out := make([]string, len(projects))
	for i, p := range projects {
		out[i] = p.Name
	}
	return out
}

func TestFilterPickerProjects_EmptyReturnsAll(t *testing.T) {
	all := samplePickerProjects()
	got := views.FilterPickerProjects(all, "")
	assert.Equal(t, projectNames(all), projectNames(got))
}

func TestFilterPickerProjects_WhitespaceReturnsAll(t *testing.T) {
	all := samplePickerProjects()
	got := views.FilterPickerProjects(all, "   ")
	assert.Equal(t, projectNames(all), projectNames(got))
}

func TestFilterPickerProjects_MatchesName(t *testing.T) {
	got := views.FilterPickerProjects(samplePickerProjects(), "demo")
	assert.Equal(t, []string{"demo-cdk"}, projectNames(got))
}

func TestFilterPickerProjects_MatchesPath(t *testing.T) {
	// "personal" appears in the path but not the name — picker
	// filter should pick it up regardless.
	got := views.FilterPickerProjects(samplePickerProjects(), "personal")
	assert.Equal(t, []string{"experiments"}, projectNames(got))
}

func TestFilterPickerProjects_CaseInsensitive(t *testing.T) {
	got := views.FilterPickerProjects(samplePickerProjects(), "DEMO")
	assert.Equal(t, []string{"demo-cdk"}, projectNames(got))
}

func TestFilterPickerProjects_NoMatchesReturnsEmpty(t *testing.T) {
	got := views.FilterPickerProjects(samplePickerProjects(), "nonexistent")
	assert.Empty(t, got)
}
