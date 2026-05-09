package app

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsAuthError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		// nil short-circuits — no error means no auth error.
		{"nil", nil, false},

		// Auth-shaped messages: HTTP status codes and substrings the
		// dashboard / oauth2 layers emit. Casing should not matter.
		{"401 inline", errors.New("api request failed: 401 unauthorized"), true},
		{"403 inline", errors.New("403 forbidden"), true},
		{"unauthorized verb", errors.New("unauthorized: token rejected"), true},
		{"unauthenticated verb", errors.New("unauthenticated request"), true},
		{"token expired", errors.New("Token expired, please log in again"), true},
		{"invalid token", errors.New("invalid token"), true},
		{"login failure prefix", errors.New("failed to log in: oauth2 error"), true},
		{"wrapped", fmt.Errorf("scan failed: %w", errors.New("401 unauthorized")), true},

		// Non-auth errors: scan-shaped, network-shaped, parse-shaped.
		// These shouldn't trigger the in-TUI re-auth offer.
		{"network", errors.New("connection refused"), false},
		{"parse", errors.New("could not parse terraform module"), false},
		{"empty", errors.New(""), false},
		{"scan failure", errors.New("failed to scan target: parser error"), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, isAuthError(tc.err))
		})
	}
}
