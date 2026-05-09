package views_test

import "github.com/infracost/go-proto/pkg/rat"

// parseRatExt mirrors the package-internal parseRat for golden test
// fixtures, which live in views_test (external) rather than views.
// Panics on parse failure since this is test-setup code.
func parseRatExt(s string) *rat.Rat {
	r, err := rat.NewFromString(s)
	if err != nil {
		panic("parseRatExt: " + s + ": " + err.Error())
	}
	return r
}
