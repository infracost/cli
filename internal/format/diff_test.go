package format

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/infracost/go-proto/pkg/rat"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func cc(name string, cost float64) CostComponentOutput {
	return CostComponentOutput{
		Name:             name,
		Unit:             "hours",
		Price:            rat.New(cost),
		Quantity:         rat.New(1),
		TotalMonthlyCost: rat.New(cost),
	}
}

func res(name string, components ...CostComponentOutput) ResourceOutput {
	return ResourceOutput{Name: name, Type: "aws_instance", CostComponents: components}
}

func out(projectName string, resources ...ResourceOutput) *Output {
	return &Output{
		Currency: "USD",
		Projects: []ProjectOutput{{ProjectName: projectName, Resources: resources}},
	}
}

func TestDiff(t *testing.T) {
	tests := []struct {
		name            string
		past            *Output
		current         *Output
		wantResources   []string
		wantOps         []DiffOp
		wantProjectDiff string
	}{
		{
			name:            "unchanged resource is omitted",
			past:            out("main", res("aws_instance.web", cc("Instance usage", 10))),
			current:         out("main", res("aws_instance.web", cc("Instance usage", 10))),
			wantResources:   nil,
			wantProjectDiff: "0",
		},
		{
			name:            "added resource",
			past:            out("main"),
			current:         out("main", res("aws_instance.web", cc("Instance usage", 10))),
			wantResources:   []string{"aws_instance.web"},
			wantOps:         []DiffOp{DiffAdded},
			wantProjectDiff: "10",
		},
		{
			name:            "removed resource",
			past:            out("main", res("aws_instance.web", cc("Instance usage", 10))),
			current:         out("main"),
			wantResources:   []string{"aws_instance.web"},
			wantOps:         []DiffOp{DiffRemoved},
			wantProjectDiff: "-10",
		},
		{
			name:            "changed cost",
			past:            out("main", res("aws_instance.web", cc("Instance usage", 10))),
			current:         out("main", res("aws_instance.web", cc("Instance usage", 25))),
			wantResources:   []string{"aws_instance.web"},
			wantOps:         []DiffOp{DiffChanged},
			wantProjectDiff: "15",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := Diff(tt.past, tt.current)
			require.Len(t, d.Projects, 1)

			var names []string
			var ops []DiffOp
			for _, r := range d.Projects[0].Resources {
				names = append(names, r.Name)
				ops = append(ops, r.Op)
			}
			assert.Equal(t, tt.wantResources, names)
			if tt.wantOps != nil {
				assert.Equal(t, tt.wantOps, ops)
			}
			assert.Equal(t, tt.wantProjectDiff, d.Projects[0].DiffTotalMonthlyCost.StringFixed(0))
		})
	}
}

// A resized instance matches on the part before the bracket, so it reads as
// one changed component rather than an add and a remove.
func TestDiffMatchesComponentsOnBracketPrefix(t *testing.T) {
	past := out("main", res("aws_instance.web", cc("Instance usage (m5.large)", 10)))
	current := out("main", res("aws_instance.web", cc("Instance usage (m5.xlarge)", 20)))

	d := Diff(past, current)
	require.Len(t, d.Projects[0].Resources, 1)

	components := d.Projects[0].Resources[0].CostComponents
	require.Len(t, components, 1)
	assert.Equal(t, "Instance usage (m5.large) → Instance usage (m5.xlarge)", components[0].Name)
	assert.Equal(t, DiffChanged, components[0].Op)
	assert.Equal(t, "10", components[0].DiffMonthlyCost.StringFixed(0))
}

func TestDiffSubresources(t *testing.T) {
	parent := func(subCost float64) ResourceOutput {
		r := res("aws_instance.web", cc("Instance usage", 10))
		r.Subresources = []ResourceOutput{res("root_block_device", cc("Storage", subCost))}
		return r
	}

	d := Diff(out("main", parent(5)), out("main", parent(9)))
	require.Len(t, d.Projects[0].Resources, 1)

	top := d.Projects[0].Resources[0]
	assert.Empty(t, top.CostComponents, "the parent's own components did not change")
	require.Len(t, top.Subresources, 1)
	assert.Equal(t, "4", top.Subresources[0].DiffMonthlyCost.StringFixed(0))
	assert.Equal(t, "4", d.Projects[0].DiffTotalMonthlyCost.StringFixed(0))
}

func TestDiffRemovedProject(t *testing.T) {
	past := &Output{Currency: "USD", Projects: []ProjectOutput{
		{ProjectName: "a", Resources: []ResourceOutput{res("aws_instance.web", cc("Instance usage", 10))}},
		{ProjectName: "b", Resources: []ResourceOutput{res("aws_instance.api", cc("Instance usage", 7))}},
	}}
	current := out("a", res("aws_instance.web", cc("Instance usage", 10)))

	d := Diff(past, current)
	require.Len(t, d.Projects, 2)
	assert.Equal(t, "a", d.Projects[0].ProjectName)
	assert.Equal(t, "b", d.Projects[1].ProjectName)
	assert.Equal(t, DiffRemoved, d.Projects[1].Op)
	assert.Equal(t, "-7", d.DiffTotalMonthlyCost.StringFixed(0))
	assert.Equal(t, []string{"a"}, d.UnchangedProjects())
}

// A baseline component whose pair is already taken must read as removed, not
// get paired a second time with the same current component.
func TestDiffDoesNotReuseAMatchedCostComponent(t *testing.T) {
	past := out("main", res("aws_instance.web",
		cc("Instance usage (m5.large)", 10),
		cc("Instance usage (m5.small)", 4),
	))
	current := out("main", res("aws_instance.web", cc("Instance usage (m5.large)", 10)))

	d := Diff(past, current)
	require.Len(t, d.Projects[0].Resources, 1)

	components := d.Projects[0].Resources[0].CostComponents
	require.Len(t, components, 1)
	assert.Equal(t, "Instance usage (m5.small)", components[0].Name)
	assert.Equal(t, DiffRemoved, components[0].Op)
	assert.Equal(t, "-4", components[0].DiffMonthlyCost.StringFixed(0))
	assert.Equal(t, "-4", d.Projects[0].DiffTotalMonthlyCost.StringFixed(0))
}

func TestDiffDuplicateProjectNames(t *testing.T) {
	project := func(path string, cost float64, resourceName string) ProjectOutput {
		return ProjectOutput{
			ProjectName: "main",
			Path:        path,
			Resources:   []ResourceOutput{res(resourceName, cc("Instance usage", cost))},
		}
	}

	past := &Output{Currency: "USD", Projects: []ProjectOutput{
		project("a", 10, "aws_instance.a"),
		project("b", 90, "aws_instance.b"),
	}}
	current := &Output{Currency: "USD", Projects: []ProjectOutput{project("a", 10, "aws_instance.a")}}

	d := Diff(past, current)
	require.Len(t, d.Projects, 2)
	assert.Equal(t, DiffChanged, d.Projects[0].Op)
	assert.Equal(t, DiffRemoved, d.Projects[1].Op)
	assert.Equal(t, "b", d.Projects[1].Path)
	assert.Equal(t, "-90", d.DiffTotalMonthlyCost.StringFixed(0))
}

func TestDiffEmptyBaseline(t *testing.T) {
	d := Diff(&Output{}, out("main", res("aws_instance.web", cc("Instance usage", 10))))
	require.Len(t, d.Projects, 1)
	assert.Equal(t, DiffAdded, d.Projects[0].Op)
	assert.True(t, d.HasChanges())
	assert.Equal(t, "USD", d.Currency)
}

// usageDiff is a diff with one priced component and one usage-driven one, so
// the machine-readable renderers have both cases to encode.
func usageDiff() *DiffOutput {
	past := out("main", res("aws_instance.web", cc("Instance usage", 10)))
	current := out("main", res("aws_instance.web",
		cc("Instance usage", 25),
		CostComponentOutput{Name: "Requests", Unit: "1M requests", Price: rat.New(0.2)},
	))
	return Diff(past, current)
}

func TestDiffOutputToJSON(t *testing.T) {
	var b bytes.Buffer
	require.NoError(t, usageDiff().ToJSON(&b))

	var got struct {
		Currency  string          `json:"currency"`
		DiffTotal json.RawMessage `json:"diff_total_monthly_cost"`
		Projects  []struct {
			ProjectName string `json:"project_name"`
			Resources   []struct {
				Name           string `json:"name"`
				CostComponents []struct {
					Name               string `json:"name"`
					Op                 DiffOp `json:"op"`
					CostDependsOnUsage bool   `json:"cost_depends_on_usage"`
				} `json:"cost_components"`
			} `json:"resources"`
		} `json:"projects"`
	}
	require.NoError(t, json.Unmarshal(b.Bytes(), &got))

	assert.Equal(t, "USD", got.Currency)
	assert.Contains(t, string(got.DiffTotal), "15")
	require.Len(t, got.Projects, 1)
	require.Len(t, got.Projects[0].Resources, 1)

	components := got.Projects[0].Resources[0].CostComponents
	require.Len(t, components, 2)
	assert.Equal(t, "Instance usage", components[0].Name)
	assert.False(t, components[0].CostDependsOnUsage)
	assert.Equal(t, "Requests", components[1].Name)
	assert.True(t, components[1].CostDependsOnUsage, "machine consumers need to see a usage-driven component")
}

func TestDiffOutputToTOON(t *testing.T) {
	var b bytes.Buffer
	require.NoError(t, usageDiff().ToTOON(&b))

	got := b.String()
	assert.Contains(t, got, "currency")
	assert.Contains(t, got, "aws_instance.web")
	assert.Contains(t, got, "cost_depends_on_usage")
}
