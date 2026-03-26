package scanner

import (
	"github.com/infracost/go-proto/pkg/rat"
	"github.com/infracost/proto/gen/go/infracost/provider"
)

var hoursInMonth = rat.New(730)

// TotalMonthlyCostFromResources computes the total monthly cost by summing
// all cost components across all resources (including child resources).
func TotalMonthlyCostFromResources(resources []*provider.Resource) *rat.Rat {
	total := rat.Zero
	for _, r := range resources {
		total = total.Add(ResourceMonthlyCost(r))
	}
	return total
}

// ResourceMonthlyCost computes the monthly cost of a single resource,
// including its child resources.
func ResourceMonthlyCost(r *provider.Resource) *rat.Rat {
	cost := rat.Zero
	if r.Costs != nil {
		for _, c := range r.Costs.Components {
			cost = cost.Add(ComponentMonthlyCost(c))
		}
	}
	for _, child := range r.ChildResources {
		cost = cost.Add(ResourceMonthlyCost(child))
	}
	return cost
}

// ComponentMonthlyCost computes the monthly cost of a single cost component,
// normalizing hourly prices to monthly and applying discounts.
func ComponentMonthlyCost(c *provider.CostComponent) *rat.Rat {
	if c.PeriodPrice == nil || c.Quantity == nil {
		return rat.Zero
	}
	if c.PeriodPrice.Price == nil {
		return rat.Zero
	}

	price := ApplyDiscount(rat.FromProto(c.PeriodPrice.Price), rat.FromProto(c.DiscountRate))
	_, monthlyQty := ConvertQuantityByPeriod(rat.FromProto(c.Quantity), c.PeriodPrice.Period)

	return monthlyQty.Mul(price)
}

// ConvertQuantityByPeriod normalizes a quantity to both hourly and monthly
// based on the price period.
func ConvertQuantityByPeriod(qty *rat.Rat, period provider.Period) (hourly, monthly *rat.Rat) {
	switch period {
	case provider.Period_MONTH:
		return qty.Div(hoursInMonth), qty
	case provider.Period_HOUR:
		return qty, qty.Mul(hoursInMonth)
	default:
		return rat.Zero, rat.Zero
	}
}

// ApplyDiscount applies a discount rate to a price if the rate is greater than zero.
func ApplyDiscount(price *rat.Rat, discountRate *rat.Rat) *rat.Rat {
	if discountRate != nil && discountRate.GreaterThan(rat.Zero) {
		return price.Mul(rat.New(1).Sub(discountRate))
	}
	return price
}