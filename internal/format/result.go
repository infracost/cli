package format

import (
	"github.com/infracost/config"
	"github.com/infracost/go-proto/pkg/diagnostic"
	"github.com/infracost/go-proto/pkg/event"
	"github.com/infracost/proto/gen/go/infracost/provider"
)

type Result struct {
	Config           *config.Config
	Projects         []*ProjectResult
	GuardrailResults []event.GuardrailResult

	// EstimatedUsageCounts tracks usage parameters with non-zero values, keyed
	// by "resourceType.attribute". A nil map means no usage file was loaded; a
	// non-nil (possibly empty) map means a usage file was present.
	EstimatedUsageCounts map[string]int
	// UnestimatedUsageCounts tracks usage parameters with zero/empty values,
	// keyed by "resourceType.attribute". Nil/non-nil semantics match
	// EstimatedUsageCounts.
	UnestimatedUsageCounts map[string]int
}

type ProjectResult struct {
	Diagnostics      []*diagnostic.Diagnostic
	Config           *config.Project
	FinopsResults    []*provider.FinopsPolicyResult
	TagPolicyResults []event.TaggingPolicyResult
	Resources        []*provider.Resource
}
