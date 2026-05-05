package inspect

import (
	"strings"
	"testing"
)

func TestParseFilter(t *testing.T) {
	cases := []struct {
		name      string
		expr      string
		base      Options // pre-existing values that --filter must not silently override
		want      Options // fields we expect to be populated
		wantErr   string  // substring match against the error; empty means no error
	}{
		{
			name: "empty expression is a noop",
			expr: "",
			want: Options{},
		},
		{
			name: "whitespace-only expression is a noop",
			expr: "   ,  ,",
			want: Options{},
		},
		{
			name: "single policy predicate",
			expr: "policy=Required Tags",
			want: Options{Policy: "Required Tags"},
		},
		{
			name: "single project predicate",
			expr: "project=web-app",
			want: Options{Project: "web-app"},
		},
		{
			name: "single provider predicate",
			expr: "provider=aws",
			want: Options{Provider: "aws"},
		},
		{
			name: "tag.X=missing maps to MissingTag",
			expr: "tag.team=missing",
			want: Options{MissingTag: "team"},
		},
		{
			name: "multiple predicates AND together",
			expr: "policy=Required Tags,provider=aws,tag.team=missing",
			want: Options{Policy: "Required Tags", Provider: "aws", MissingTag: "team"},
		},
		{
			name: "leading/trailing whitespace per predicate is tolerated",
			expr: "  policy=Required Tags ,  provider=aws  ",
			want: Options{Policy: "Required Tags", Provider: "aws"},
		},
		{
			name: "trailing comma is tolerated",
			expr: "policy=Required Tags,",
			want: Options{Policy: "Required Tags"},
		},

		// --- error cases ---
		{
			name:    "predicate without equals errors with grammar hint",
			expr:    "policyX",
			wantErr: "predicates must use 'key=value' form",
		},
		{
			name:    "empty value errors",
			expr:    "policy=",
			wantErr: "value cannot be empty",
		},
		{
			name:    "unknown key errors with supported-keys hint",
			expr:    "kind=finops",
			wantErr: "supported keys are policy, project, provider, tag.<key>=missing",
		},
		{
			name:    "tag.<empty>=missing errors",
			expr:    "tag.=missing",
			wantErr: "tag predicate needs a key",
		},
		{
			name:    "tag.X=non-missing errors with --invalid-tag pointer",
			expr:    "tag.team=frontend",
			wantErr: "only tag.<key>=missing is supported",
		},

		// --- conflict detection (filter must not silently override an explicitly-set flag) ---
		{
			name:    "filter policy conflicts with --policy",
			expr:    "policy=Use GP3",
			base:    Options{Policy: "Required Tags"},
			wantErr: `--filter policy="Use GP3" conflicts with --policy "Required Tags"`,
		},
		{
			name:    "filter project conflicts with --project",
			expr:    "project=web",
			base:    Options{Project: "api"},
			wantErr: `--filter project="web" conflicts with --project "api"`,
		},
		{
			name:    "filter tag.X=missing conflicts with --missing-tag",
			expr:    "tag.team=missing",
			base:    Options{MissingTag: "owner"},
			wantErr: `--filter tag.team=missing conflicts with --missing-tag "owner"`,
		},

		// --- non-conflict: same value is allowed ---
		{
			name: "filter policy matching --policy is fine",
			expr: "policy=Required Tags",
			base: Options{Policy: "Required Tags"},
			want: Options{Policy: "Required Tags"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := tc.base
			err := ParseFilter(tc.expr, &opts)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil; opts=%+v", tc.wantErr, opts)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("error %q does not contain %q", err.Error(), tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			// Compare just the fields ParseFilter is allowed to touch.
			if opts.Policy != tc.want.Policy {
				t.Errorf("Policy = %q, want %q", opts.Policy, tc.want.Policy)
			}
			if opts.Project != tc.want.Project {
				t.Errorf("Project = %q, want %q", opts.Project, tc.want.Project)
			}
			if opts.Provider != tc.want.Provider {
				t.Errorf("Provider = %q, want %q", opts.Provider, tc.want.Provider)
			}
			if opts.MissingTag != tc.want.MissingTag {
				t.Errorf("MissingTag = %q, want %q", opts.MissingTag, tc.want.MissingTag)
			}
		})
	}
}
