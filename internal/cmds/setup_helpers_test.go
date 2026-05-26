package cmds

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSuggestedOrgName(t *testing.T) {
	tests := []struct {
		email string
		want  string
	}{
		// Company domains turn into a capitalized stub.
		{"alice@acme.io", "Acme"},
		{"alice@infracost.io", "Infracost"},
		{"bob@multi.word.tld", "Multi"},

		// Personal email providers shouldn't be defaulted — typing
		// "Gmail" or "Outlook" as your org name is worse than empty.
		{"alice@gmail.com", ""},
		{"alice@Gmail.com", ""},
		{"alice@hotmail.co.uk", ""},
		{"alice@yahoo.com", ""},
		{"alice@icloud.com", ""},
		{"alice@protonmail.com", ""},

		// Malformed inputs fall back to empty.
		{"", ""},
		{"no-at-sign", ""},
		{"trailing@", ""},
	}

	for _, tt := range tests {
		t.Run(tt.email, func(t *testing.T) {
			assert.Equal(t, tt.want, suggestedOrgName(tt.email))
		})
	}
}