package inspect

import "testing"

// TestFieldSpecsCoverAllViews is the guard that keeps human tabular output
// consistent as the command grows: every canonical field a view can surface
// must have an entry in fieldSpecs so it renders through writeRecordTable with
// a defined header and alignment. Add a field to a view without a spec and
// this fails, forcing the new column onto the shared, consistent path.
func TestFieldSpecsCoverAllViews(t *testing.T) {
	// The union of every field name any tabular view can project. Group-by
	// adds its dimension columns plus the synthetic count/monthly_cost and
	// the policy-detail columns carried on GroupedRow.
	viewFields := map[string][]string{
		"top-savings":        fieldsTopSavings,
		"filtered-resources": fieldsFilteredResources,
	}
	for _, o := range ValidGroupByOptions {
		viewFields["group-by"] = append(viewFields["group-by"], string(o))
	}
	viewFields["group-by"] = append(viewFields["group-by"],
		"kind", "message", "status", "limit", "actual spend", "count", "monthly_cost")

	for view, fields := range viewFields {
		for _, f := range fields {
			if _, ok := fieldSpecs[f]; !ok {
				t.Errorf("view %q exposes field %q with no fieldSpecs entry; "+
					"add it to fieldSpecs in render.go so it renders consistently", view, f)
			}
		}
	}
}

func TestFieldHeaderFallback(t *testing.T) {
	// Registered field uses its explicit header.
	if got := fieldHeader("monthly_cost"); got != "Monthly Cost" {
		t.Errorf("fieldHeader(monthly_cost) = %q, want %q", got, "Monthly Cost")
	}
	// Unregistered field falls back to Title case rather than panicking.
	if got := fieldHeader("some_new_field"); got != "Some New Field" {
		t.Errorf("fieldHeader fallback = %q, want %q", got, "Some New Field")
	}
}

func TestHumanizeBool(t *testing.T) {
	cases := map[string]string{"true": "yes", "false": "", "": ""}
	for in, want := range cases {
		if got := fieldDisplay("is_free", in); got != want {
			t.Errorf("fieldDisplay(is_free, %q) = %q, want %q", in, got, want)
		}
	}
}
