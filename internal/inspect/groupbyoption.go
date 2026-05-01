package inspect

import (
	"fmt"
	"slices"
	"strings"
)

// GroupByOption is a valid value for the --group-by flag. Each option is also
// the column key used to look the value up on a tableRow inside groupby.go,
// so the same constant is used for both validation and lookup.
type GroupByOption string

const (
	GroupByType      GroupByOption = "type"
	GroupByProvider  GroupByOption = "provider"
	GroupByProject   GroupByOption = "project"
	GroupByResource  GroupByOption = "resource"
	GroupByFile      GroupByOption = "file"
	GroupByPolicy    GroupByOption = "policy"
	GroupByGuardrail GroupByOption = "guardrail"
	GroupByBudget    GroupByOption = "budget"
)

// ValidGroupByOptions enumerates every accepted --group-by value. Order is
// the order shown in help text.
var ValidGroupByOptions = []GroupByOption{
	GroupByType,
	GroupByProvider,
	GroupByProject,
	GroupByResource,
	GroupByFile,
	GroupByPolicy,
	GroupByGuardrail,
	GroupByBudget,
}

// ValidateGroupBy returns an error if any value is not a recognised
// GroupByOption, or if combinations of values are not compatible.
func ValidateGroupBy(values []string) error {
	for _, v := range values {
		if !slices.Contains(ValidGroupByOptions, GroupByOption(v)) {
			return fmt.Errorf("invalid --group-by value %q (valid: %s)", v, GroupByOptionsHelp())
		}
	}

	// policy, guardrail and budget each route to a different row collector,
	// so at most one may appear in a single --group-by.
	exclusive := []GroupByOption{GroupByPolicy, GroupByGuardrail, GroupByBudget}
	var found []string
	for _, e := range exclusive {
		if slices.Contains(values, string(e)) {
			found = append(found, string(e))
		}
	}
	if len(found) > 1 {
		return fmt.Errorf("--group-by values %s cannot be combined; pick one", strings.Join(found, ", "))
	}

	// guardrail/budget rows only carry their own dimension plus status/limit
	// columns, so combining them with resource-context dims (type, provider,
	// project, resource, file) silently produces empty cells.
	resourceContext := []GroupByOption{GroupByType, GroupByProvider, GroupByProject, GroupByResource, GroupByFile}
	for _, anchor := range []GroupByOption{GroupByGuardrail, GroupByBudget} {
		if !slices.Contains(values, string(anchor)) {
			continue
		}
		for _, rc := range resourceContext {
			if slices.Contains(values, string(rc)) {
				return fmt.Errorf("--group-by %s cannot be combined with %s; %s rows do not carry resource context", anchor, rc, anchor)
			}
		}
	}

	return nil
}

// GroupByOptionsHelp returns the comma-separated list of valid options for
// use in CLI help text and error messages.
func GroupByOptionsHelp() string {
	parts := make([]string, len(ValidGroupByOptions))
	for i, o := range ValidGroupByOptions {
		parts[i] = string(o)
	}
	return strings.Join(parts, ", ")
}
