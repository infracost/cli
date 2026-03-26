package scanner

import (
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/infracost/proto/gen/go/infracost/parser/event"
)

var preserver = uuid.New().String()

// EvaluateProductionFilters checks whether a project should be marked as production
// based on the configured production filters.
func EvaluateProductionFilters(filters []*event.ProductionFilter, repositoryName, branchName, projectName string) bool {
	for _, filter := range filters {
		switch filter.Type {
		case event.ProductionFilter_BRANCH:
			if MatchProductionFilter(filter.Value, branchName) {
				return filter.Include
			}
		case event.ProductionFilter_PROJECT:
			if MatchProductionFilter(filter.Value, projectName) {
				return filter.Include
			}
		case event.ProductionFilter_REPO:
			if MatchProductionFilter(filter.Value, repositoryName) {
				return filter.Include
			}
		}
	}
	return false
}

// MatchProductionFilter evaluates production filter patterns with wildcard support.
func MatchProductionFilter(matcher, value string) bool {
	escaped := strings.ReplaceAll(matcher, "*", preserver)
	escaped = regexp.QuoteMeta(escaped)

	if !strings.HasSuffix(escaped, preserver) {
		escaped += "\\b"
	}
	if !strings.HasPrefix(escaped, preserver) {
		escaped = "\\b" + escaped
	}
	escaped = strings.ReplaceAll(escaped, preserver, ".*")

	re, err := regexp.Compile(escaped)
	if err != nil {
		return false
	}
	return re.MatchString(value)
}