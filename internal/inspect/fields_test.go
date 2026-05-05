package inspect

import (
	"reflect"
	"strings"
	"testing"
)

func TestValidateFields(t *testing.T) {
	available := []string{"address", "policy", "monthly_savings", "project"}

	cases := []struct {
		name      string
		requested []string
		want      []string
		wantErr   string
	}{
		{
			name:      "empty request returns the available set unchanged",
			requested: nil,
			want:      available,
		},
		{
			name:      "single field passes through",
			requested: []string{"address"},
			want:      []string{"address"},
		},
		{
			name:      "multiple fields preserve user-supplied order",
			requested: []string{"monthly_savings", "address", "policy"},
			want:      []string{"monthly_savings", "address", "policy"},
		},
		{
			name:      "leading/trailing whitespace is trimmed",
			requested: []string{"  address  ", " policy "},
			want:      []string{"address", "policy"},
		},
		{
			name:      "empty entries in the request are skipped",
			requested: []string{"address", "", "policy"},
			want:      []string{"address", "policy"},
		},
		{
			name:      "unknown field errors with the available set listed (sorted)",
			requested: []string{"address", "wrongname"},
			wantErr:   `unknown field "wrongname". Available fields: address, monthly_savings, policy, project`,
		},
		{
			name:      "case-sensitive: PolicyName is unknown",
			requested: []string{"PolicyName"},
			wantErr:   `unknown field "PolicyName"`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := validateFields(tc.requested, available)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error %q, got nil; got=%v", tc.wantErr, got)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("error %q does not contain %q", err.Error(), tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestEffectiveFields(t *testing.T) {
	available := []string{"address", "type", "monthly_cost"}

	cases := []struct {
		name string
		opts Options
		want []string
	}{
		{
			name: "neither flag set: full available list",
			opts: Options{},
			want: available,
		},
		{
			name: "explicit Fields wins",
			opts: Options{Fields: []string{"address", "monthly_cost"}},
			want: []string{"address", "monthly_cost"},
		},
		{
			name: "AddressesOnly is treated as Fields=[\"address\"]",
			opts: Options{AddressesOnly: true},
			want: []string{"address"},
		},
		{
			name: "explicit Fields takes precedence over AddressesOnly",
			opts: Options{Fields: []string{"type"}, AddressesOnly: true},
			want: []string{"type"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := effectiveFields(tc.opts, available)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}
