package scanner

import (
	"slices"

	goprotoevent "github.com/infracost/go-proto/pkg/event"
	"github.com/infracost/go-proto/pkg/rat"
	"github.com/infracost/proto/gen/go/infracost/parser/event"
	"github.com/infracost/proto/gen/go/infracost/usage"
)

// LoadUsageDefaults converts API usage defaults into the proto usage format,
// filtered by project name if specified.
func LoadUsageDefaults(defaults *event.UsageDefaults, projectName string) *usage.Usage {
	if defaults == nil {
		return nil
	}

	byResourceType := make(map[string]*usage.UsageItemMap, len(defaults.Resources))
	for resourceType, value := range defaults.Resources {
		resourceTypes := make(map[string]*usage.UsageValue, len(value.Usages))
		for attr, value := range value.Usages {
			list := make([]*event.UsageDefault, len(value.List))
			copy(list, value.List)
			slices.SortFunc(list, func(a, b *event.UsageDefault) int {
				if a.Priority > b.Priority {
					return -1
				}
				if a.Priority < b.Priority {
					return 1
				}
				return 0
			})
		List:
			for _, item := range list {
				if item.Quantity == "" {
					continue
				}

				if !goprotoevent.StringFilterFromProto(item.GetFilters().GetProject()).Matches(projectName) {
					continue List
				}

				if q, err := rat.NewFromString(item.Quantity); err == nil {
					resourceTypes[attr] = &usage.UsageValue{
						Value: &usage.UsageValue_NumberValue{
							NumberValue: q.Proto(),
						},
					}
					break
				}
			}
		}
		byResourceType[resourceType] = &usage.UsageItemMap{
			Items: resourceTypes,
		}
	}
	return &usage.Usage{
		ByResourceType: byResourceType,
	}
}

// CountUsage splits loaded usage data into estimated (non-zero value) and
// unestimated (zero/empty value) counts. If usageData is nil, both returned
// maps are nil (signaling no usage file was loaded).
func CountUsage(usageData *usage.Usage) (estimated, unestimated map[string]int) {
	if usageData == nil {
		return nil, nil
	}
	estimated = make(map[string]int)
	unestimated = make(map[string]int)
	for resourceType, items := range usageData.GetByResourceType() {
		for attr, val := range items.GetItems() {
			key := resourceType + "." + attr
			if isEstimated(val) {
				estimated[key]++
			} else {
				unestimated[key]++
			}
		}
	}
	return estimated, unestimated
}

func isEstimated(v *usage.UsageValue) bool {
	if v == nil {
		return false
	}
	switch val := v.Value.(type) {
	case *usage.UsageValue_NumberValue:
		return val.NumberValue != nil && len(val.NumberValue.GetNumerator()) > 0
	case *usage.UsageValue_StringValue:
		return val.StringValue != ""
	default:
		return false
	}
}
