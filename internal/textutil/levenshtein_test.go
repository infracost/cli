package textutil

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLevenshteinDistance(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"", "", 0},
		{"abc", "", 3},
		{"", "abc", 3},
		{"kitten", "sitting", 3},
		{"acme-corp", "acme-corpp", 1},
		{"acme-corp", "acme-corp", 0},
		{"terraform", "terrafrom", 2},
	}

	for _, tt := range tests {
		t.Run(tt.a+"→"+tt.b, func(t *testing.T) {
			assert.Equal(t, tt.want, LevenshteinDistance(tt.a, tt.b))
		})
	}
}
