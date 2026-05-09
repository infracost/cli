package views

import "github.com/infracost/go-proto/pkg/rat"

// parseRat is a test-only convenience for building rat values inline
// in fixtures without threading errors through every literal.
// Panics on parse failure — fast, loud, only seen if a test fixture
// is malformed, which we'd want to fail noisily.
func parseRat(s string) *rat.Rat {
	r, err := rat.NewFromString(s)
	if err != nil {
		panic("parseRat: " + s + ": " + err.Error())
	}
	return r
}
