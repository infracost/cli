package app

import (
	"testing"

	"github.com/infracost/cli/internal/tui/views"
	"github.com/infracost/go-proto/pkg/rat"
	"github.com/stretchr/testify/assert"
)

// rowsFor builds a fixed corpus the sort tests share. Costs are
// chosen so each sort mode has a unique expected order, including a
// pair of equal costs ($100) so we can verify stability.
func rowsFor() []views.ResourceRow {
	return []views.ResourceRow{
		{Address: "aws_instance.web", Type: "aws_instance", Cost: rat.New(100)},
		{Address: "aws_s3_bucket.logs", Type: "aws_s3_bucket", Cost: rat.New(50)},
		{Address: "aws_rds_cluster.db", Type: "aws_rds_cluster", Cost: rat.New(200)},
		{Address: "aws_lambda.api", Type: "aws_lambda", Cost: rat.New(100)},
		{Address: "aws_kms_key.enc", Type: "aws_kms_key", Cost: nil}, // free
	}
}

func addresses(rows []views.ResourceRow) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Address
	}
	return out
}

func TestApplySort_CostDesc(t *testing.T) {
	got := applySort(rowsFor(), SortByCostDesc)

	// Highest cost first; nil cost (free) sinks to the end. Ties
	// resolve alphabetically by address — instance.web sorts before
	// lambda.api at the equal-$100 tier.
	assert.Equal(t, []string{
		"aws_rds_cluster.db",  // $200
		"aws_instance.web",    // $100
		"aws_lambda.api",      // $100 (tiebreak)
		"aws_s3_bucket.logs",  // $50
		"aws_kms_key.enc",     // free
	}, addresses(got))
}

func TestApplySort_AddressAsc(t *testing.T) {
	got := applySort(rowsFor(), SortByAddressAsc)

	assert.Equal(t, []string{
		"aws_instance.web",
		"aws_kms_key.enc",
		"aws_lambda.api",
		"aws_rds_cluster.db",
		"aws_s3_bucket.logs",
	}, addresses(got))
}

func TestApplySort_TypeAsc(t *testing.T) {
	got := applySort(rowsFor(), SortByTypeAsc)

	// Types are unique here, so the output is just type-sorted.
	assert.Equal(t, []string{
		"aws_instance.web",
		"aws_kms_key.enc",
		"aws_lambda.api",
		"aws_rds_cluster.db",
		"aws_s3_bucket.logs",
	}, addresses(got))
}

func TestApplySort_TypeAsc_StableOnTies(t *testing.T) {
	// Three rows of the same type, in deliberate non-alphabetical
	// order. Stable sort should preserve input order on ties (but
	// since type is the sort key, the tie-break is by address).
	rows := []views.ResourceRow{
		{Address: "aws_instance.c", Type: "aws_instance", Cost: rat.New(1)},
		{Address: "aws_instance.a", Type: "aws_instance", Cost: rat.New(2)},
		{Address: "aws_instance.b", Type: "aws_instance", Cost: rat.New(3)},
	}
	got := applySort(rows, SortByTypeAsc)

	assert.Equal(t, []string{
		"aws_instance.a",
		"aws_instance.b",
		"aws_instance.c",
	}, addresses(got))
}

func TestApplySort_DoesNotMutateInput(t *testing.T) {
	rows := rowsFor()
	original := append([]views.ResourceRow(nil), rows...)

	_ = applySort(rows, SortByCostDesc)

	assert.Equal(t, addresses(original), addresses(rows),
		"applySort should not mutate the input slice")
}

func TestSortMode_Cycle(t *testing.T) {
	// next() should walk through all three modes and return to the
	// starting point so the `s` key gives the user a finite cycle.
	mode := SortByCostDesc
	mode = mode.next()
	assert.Equal(t, SortByAddressAsc, mode)
	mode = mode.next()
	assert.Equal(t, SortByTypeAsc, mode)
	mode = mode.next()
	assert.Equal(t, SortByCostDesc, mode)
}
