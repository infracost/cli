package inspect

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateGroupBy(t *testing.T) {
	tests := []struct {
		name    string
		input   []string
		wantErr string // empty means must succeed
	}{
		{name: "empty is valid", input: nil},
		{name: "single valid", input: []string{"type"}},
		{name: "multi valid resource-context", input: []string{"project", "provider", "type", "resource", "file"}},
		{name: "policy with resource-context is allowed", input: []string{"policy", "type", "resource"}},
		{
			name:    "unknown value rejected",
			input:   []string{"foo"},
			wantErr: `invalid --group-by value "foo" (valid: type, provider, project, resource, file, policy, guardrail, budget)`,
		},
		{
			name:    "policy and budget mutually exclusive",
			input:   []string{"policy", "budget"},
			wantErr: "--group-by values policy, budget cannot be combined; pick one",
		},
		{
			name:    "policy and guardrail mutually exclusive",
			input:   []string{"policy", "guardrail"},
			wantErr: "--group-by values policy, guardrail cannot be combined; pick one",
		},
		{
			name:    "guardrail and budget mutually exclusive",
			input:   []string{"guardrail", "budget"},
			wantErr: "--group-by values guardrail, budget cannot be combined; pick one",
		},
		{
			name:    "guardrail with resource-context dim rejected",
			input:   []string{"guardrail", "type"},
			wantErr: "--group-by guardrail cannot be combined with type; guardrail rows do not carry resource context",
		},
		{
			name:    "budget with resource-context dim rejected",
			input:   []string{"budget", "provider"},
			wantErr: "--group-by budget cannot be combined with provider; budget rows do not carry resource context",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateGroupBy(tt.input)
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.EqualError(t, err, tt.wantErr)
		})
	}
}

func TestGroupByOptionsHelp(t *testing.T) {
	assert.Equal(t, "type, provider, project, resource, file, policy, guardrail, budget", GroupByOptionsHelp())
}
