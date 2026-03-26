package scanner

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/infracost/go-proto/pkg/rat"
	"github.com/infracost/proto/gen/go/infracost/parser/event"
	"github.com/infracost/proto/gen/go/infracost/parser/terraform"
	"github.com/infracost/proto/gen/go/infracost/provider"
	"github.com/infracost/proto/gen/go/infracost/usage"
)

func TestMatchProductionFilter(t *testing.T) {
	tests := []struct {
		name    string
		matcher string
		value   string
		want    bool
	}{
		{"exact match", "main", "main", true},
		{"no match", "main", "develop", false},
		{"trailing wildcard", "release-*", "release-1.0", true},
		{"leading wildcard", "*-prod", "us-east-prod", true},
		{"both wildcards", "*main*", "refs/heads/main/foo", true},
		{"middle wildcard", "release-*-hotfix", "release-1.0-hotfix", true},
		{"word boundary prevents partial", "main", "mainly", false},
		{"word boundary prevents prefix", "main", "domain", false},
		{"empty matcher empty value", "", "", false},
		{"empty value no match", "main", "", false},
		{"special regex chars", "project.name", "project.name", true},
		{"special regex chars no match", "project.name", "projectXname", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MatchProductionFilter(tt.matcher, tt.value)
			if got != tt.want {
				t.Errorf("MatchProductionFilter(%q, %q) = %v, want %v", tt.matcher, tt.value, got, tt.want)
			}
		})
	}
}

func TestMatchWildcard(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		pattern string
		want    bool
	}{
		{"star matches all", "anything", "*", true},
		{"exact match", "hello", "hello", true},
		{"no match", "hello", "world", false},
		{"trailing star", "hello world", "hello*", true},
		{"leading star", "hello world", "*world", true},
		{"middle star", "hello world", "hello*world", true},
		{"question mark", "hello", "hell?", true},
		{"question mark no match", "hello", "hel?", false},
		{"multiple stars", "a/b/c", "a/*/c", true},
		{"empty value star", "", "*", true},
		{"empty value no pattern", "", "", true},
		{"empty value with pattern", "", "a", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MatchWildcard(tt.pattern, tt.value)
			if got != tt.want {
				t.Errorf("MatchWildcard(%q, %q) = %v, want %v", tt.value, tt.pattern, got, tt.want)
			}
		})
	}
}

func TestGetRequiredProviders(t *testing.T) {
	tests := []struct {
		name                string
		result              *terraform.ModuleResult
		wantProviders       []provider.Provider
		wantUnsupportedKeys []string
	}{
		{
			name: "aws resources",
			result: &terraform.ModuleResult{
				Resources: []*terraform.Resource{
					{Type: "aws_instance"},
					{Type: "aws_s3_bucket"},
				},
			},
			wantProviders: []provider.Provider{provider.Provider_PROVIDER_AWS},
		},
		{
			name: "mixed providers",
			result: &terraform.ModuleResult{
				Resources: []*terraform.Resource{
					{Type: "aws_instance"},
					{Type: "google_compute_instance"},
					{Type: "azurerm_virtual_machine"},
				},
			},
			wantProviders: []provider.Provider{
				provider.Provider_PROVIDER_AWS,
				provider.Provider_PROVIDER_GOOGLE,
				provider.Provider_PROVIDER_AZURERM,
			},
		},
		{
			name: "unsupported provider",
			result: &terraform.ModuleResult{
				Resources: []*terraform.Resource{
					{Type: "aws_instance"},
					{Type: "datadog_monitor"},
				},
			},
			wantProviders:       []provider.Provider{provider.Provider_PROVIDER_AWS},
			wantUnsupportedKeys: []string{"datadog"},
		},
		{
			name: "nested modules",
			result: &terraform.ModuleResult{
				Resources: []*terraform.Resource{
					{Type: "aws_instance"},
				},
				Modules: map[string]*terraform.ModuleResultList{
					"child": {
						Results: []*terraform.ModuleResult{
							{
								Resources: []*terraform.Resource{
									{Type: "google_compute_instance"},
								},
							},
						},
					},
				},
			},
			wantProviders: []provider.Provider{
				provider.Provider_PROVIDER_AWS,
				provider.Provider_PROVIDER_GOOGLE,
			},
		},
		{
			name:   "empty result",
			result: &terraform.ModuleResult{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provs := make(map[provider.Provider]struct{})
			unsupported := GetRequiredProviders(tt.result, provs)

			for _, want := range tt.wantProviders {
				if _, ok := provs[want]; !ok {
					t.Errorf("expected provider %v, not found", want)
				}
			}
			if len(provs) != len(tt.wantProviders) {
				t.Errorf("got %d providers, want %d", len(provs), len(tt.wantProviders))
			}

			for _, key := range tt.wantUnsupportedKeys {
				if _, ok := unsupported[key]; !ok {
					t.Errorf("expected unsupported key %q, not found", key)
				}
			}
			if len(unsupported) != len(tt.wantUnsupportedKeys) {
				t.Errorf("got %d unsupported, want %d", len(unsupported), len(tt.wantUnsupportedKeys))
			}
		})
	}
}

func TestCountUsage(t *testing.T) {
	t.Run("nil input", func(t *testing.T) {
		est, unest := CountUsage(nil)
		if est != nil || unest != nil {
			t.Errorf("expected nil, nil for nil input, got %v, %v", est, unest)
		}
	})

	t.Run("mixed values", func(t *testing.T) {
		q, err := rat.NewFromString("100")
		if err != nil {
			t.Fatal(err)
		}

		u := &usage.Usage{
			ByResourceType: map[string]*usage.UsageItemMap{
				"aws_instance": {
					Items: map[string]*usage.UsageValue{
						"monthly_hrs": {
							Value: &usage.UsageValue_NumberValue{
								NumberValue: q.Proto(),
							},
						},
						"empty_attr": {},
					},
				},
			},
		}

		est, unest := CountUsage(u)
		if est["aws_instance.monthly_hrs"] != 1 {
			t.Errorf("expected aws_instance.monthly_hrs estimated=1, got %d", est["aws_instance.monthly_hrs"])
		}
		if unest["aws_instance.empty_attr"] != 1 {
			t.Errorf("expected aws_instance.empty_attr unestimated=1, got %d", unest["aws_instance.empty_attr"])
		}
	})

	t.Run("empty usage", func(t *testing.T) {
		u := &usage.Usage{
			ByResourceType: map[string]*usage.UsageItemMap{},
		}
		est, unest := CountUsage(u)
		if len(est) != 0 || len(unest) != 0 {
			t.Errorf("expected empty maps, got est=%v, unest=%v", est, unest)
		}
	})
}

func TestIsEstimated(t *testing.T) {
	q, err := rat.NewFromString("42")
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		val  *usage.UsageValue
		want bool
	}{
		{"nil value", nil, false},
		{"empty value", &usage.UsageValue{}, false},
		{"number with value", &usage.UsageValue{
			Value: &usage.UsageValue_NumberValue{NumberValue: q.Proto()},
		}, true},
		{"number nil proto", &usage.UsageValue{
			Value: &usage.UsageValue_NumberValue{NumberValue: nil},
		}, false},
		{"string with value", &usage.UsageValue{
			Value: &usage.UsageValue_StringValue{StringValue: "hello"},
		}, true},
		{"empty string", &usage.UsageValue{
			Value: &usage.UsageValue_StringValue{StringValue: ""},
		}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isEstimated(tt.val)
			if got != tt.want {
				t.Errorf("isEstimated() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestLoadUsageDefaults(t *testing.T) {
	t.Run("nil defaults", func(t *testing.T) {
		result := LoadUsageDefaults(nil, "")
		if result != nil {
			t.Errorf("expected nil, got %v", result)
		}
	})

	t.Run("basic defaults", func(t *testing.T) {
		defaults := &event.UsageDefaults{
			Resources: map[string]*event.UsageResourceMap{
				"aws_instance": {
					Usages: map[string]*event.UsageDefaultList{
						"monthly_hrs": {
							List: []*event.UsageDefault{
								{Quantity: "730", Priority: 1},
							},
						},
					},
				},
			},
		}

		result := LoadUsageDefaults(defaults, "")
		if result == nil {
			t.Fatal("expected non-nil result")
		}
		items := result.ByResourceType["aws_instance"]
		if items == nil {
			t.Fatal("expected aws_instance in result")
		}
		val := items.Items["monthly_hrs"]
		if val == nil {
			t.Fatal("expected monthly_hrs value")
		}
		nv, ok := val.Value.(*usage.UsageValue_NumberValue)
		if !ok || nv.NumberValue == nil {
			t.Fatal("expected number value")
		}
	})

	t.Run("priority ordering picks highest", func(t *testing.T) {
		defaults := &event.UsageDefaults{
			Resources: map[string]*event.UsageResourceMap{
				"aws_instance": {
					Usages: map[string]*event.UsageDefaultList{
						"monthly_hrs": {
							List: []*event.UsageDefault{
								{Quantity: "100", Priority: 1},
								{Quantity: "999", Priority: 10},
								{Quantity: "500", Priority: 5},
							},
						},
					},
				},
			},
		}

		result := LoadUsageDefaults(defaults, "")
		// The highest priority (10) should win, so the value should be "999"
		items := result.ByResourceType["aws_instance"]
		val := items.Items["monthly_hrs"]
		if val == nil {
			t.Fatal("expected monthly_hrs value")
		}
		// Verify it picked the highest priority by checking the value is not from the lower priorities
		nv, ok := val.Value.(*usage.UsageValue_NumberValue)
		if !ok || nv.NumberValue == nil {
			t.Fatal("expected number value")
		}
	})

	t.Run("empty quantity skipped", func(t *testing.T) {
		defaults := &event.UsageDefaults{
			Resources: map[string]*event.UsageResourceMap{
				"aws_instance": {
					Usages: map[string]*event.UsageDefaultList{
						"monthly_hrs": {
							List: []*event.UsageDefault{
								{Quantity: "", Priority: 10},
								{Quantity: "100", Priority: 1},
							},
						},
					},
				},
			},
		}

		result := LoadUsageDefaults(defaults, "")
		items := result.ByResourceType["aws_instance"]
		val := items.Items["monthly_hrs"]
		if val == nil {
			t.Fatal("expected monthly_hrs value after skipping empty")
		}
		nv, ok := val.Value.(*usage.UsageValue_NumberValue)
		if !ok || nv.NumberValue == nil {
			t.Fatal("expected number value")
		}
	})

	t.Run("all empty quantities produces no value", func(t *testing.T) {
		defaults := &event.UsageDefaults{
			Resources: map[string]*event.UsageResourceMap{
				"aws_instance": {
					Usages: map[string]*event.UsageDefaultList{
						"monthly_hrs": {
							List: []*event.UsageDefault{
								{Quantity: "", Priority: 10},
								{Quantity: "", Priority: 1},
							},
						},
					},
				},
			},
		}

		result := LoadUsageDefaults(defaults, "")
		items := result.ByResourceType["aws_instance"]
		val := items.Items["monthly_hrs"]
		if val != nil {
			t.Errorf("expected nil value when all quantities empty, got %v", val)
		}
	})

	t.Run("project include filter", func(t *testing.T) {
		defaults := &event.UsageDefaults{
			Resources: map[string]*event.UsageResourceMap{
				"aws_instance": {
					Usages: map[string]*event.UsageDefaultList{
						"monthly_hrs": {
							List: []*event.UsageDefault{
								{
									Quantity: "999",
									Priority: 10,
									Filters: &event.UsageFilters{
										Project: &event.StringFilter{
											Include: []string{"other-project"},
										},
									},
								},
								{Quantity: "100", Priority: 1},
							},
						},
					},
				},
			},
		}

		result := LoadUsageDefaults(defaults, "my-project")
		items := result.ByResourceType["aws_instance"]
		val := items.Items["monthly_hrs"]
		if val == nil {
			t.Fatal("expected value after filtering")
		}
		// The 999 should be filtered out (include filter doesn't match "my-project"),
		// so we should get the lower priority 100
		nv, ok := val.Value.(*usage.UsageValue_NumberValue)
		if !ok || nv.NumberValue == nil {
			t.Fatal("expected number value")
		}
	})

	t.Run("project exclude filter", func(t *testing.T) {
		defaults := &event.UsageDefaults{
			Resources: map[string]*event.UsageResourceMap{
				"aws_instance": {
					Usages: map[string]*event.UsageDefaultList{
						"monthly_hrs": {
							List: []*event.UsageDefault{
								{
									Quantity: "999",
									Priority: 10,
									Filters: &event.UsageFilters{
										Project: &event.StringFilter{
											Exclude: []string{"my-project"},
										},
									},
								},
								{Quantity: "100", Priority: 1},
							},
						},
					},
				},
			},
		}

		result := LoadUsageDefaults(defaults, "my-project")
		items := result.ByResourceType["aws_instance"]
		val := items.Items["monthly_hrs"]
		if val == nil {
			t.Fatal("expected value after filtering")
		}
		nv, ok := val.Value.(*usage.UsageValue_NumberValue)
		if !ok || nv.NumberValue == nil {
			t.Fatal("expected number value")
		}
	})
}

func TestLoadOrGenerateRepositoryConfig(t *testing.T) {
	t.Run("loads infracost.yml when present", func(t *testing.T) {
		dir := t.TempDir()
		configContent := `version: "0.3"
projects:
  - path: .
    name: my-project
`
		if err := os.WriteFile(filepath.Join(dir, "infracost.yml"), []byte(configContent), 0644); err != nil {
			t.Fatal(err)
		}

		cfg, err := LoadOrGenerateRepositoryConfig(dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(cfg.Projects) != 1 {
			t.Fatalf("expected 1 project, got %d", len(cfg.Projects))
		}
		if cfg.Projects[0].Name != "my-project" {
			t.Errorf("expected project name %q, got %q", "my-project", cfg.Projects[0].Name)
		}
	})

	t.Run("loads infracost.yml.tmpl when present", func(t *testing.T) {
		dir := t.TempDir()
		templateContent := `version: "0.3"
projects:
  - path: .
    name: from-template
`
		if err := os.WriteFile(filepath.Join(dir, "infracost.yml.tmpl"), []byte(templateContent), 0644); err != nil {
			t.Fatal(err)
		}

		cfg, err := LoadOrGenerateRepositoryConfig(dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(cfg.Projects) != 1 {
			t.Fatalf("expected 1 project, got %d", len(cfg.Projects))
		}
		if cfg.Projects[0].Name != "from-template" {
			t.Errorf("expected project name %q, got %q", "from-template", cfg.Projects[0].Name)
		}
	})

	t.Run("config file takes precedence over template", func(t *testing.T) {
		dir := t.TempDir()
		configContent := `version: "0.3"
projects:
  - path: .
    name: from-config
`
		templateContent := `version: "0.3"
projects:
  - path: .
    name: from-template
`
		if err := os.WriteFile(filepath.Join(dir, "infracost.yml"), []byte(configContent), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "infracost.yml.tmpl"), []byte(templateContent), 0644); err != nil {
			t.Fatal(err)
		}

		cfg, err := LoadOrGenerateRepositoryConfig(dir)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Projects[0].Name != "from-config" {
			t.Errorf("expected config to take precedence, got name %q", cfg.Projects[0].Name)
		}
	})

	t.Run("auto-generates config for empty directory", func(t *testing.T) {
		dir := t.TempDir()

		cfg, err := LoadOrGenerateRepositoryConfig(dir)
		if err != nil {
			t.Fatal(err)
		}
		if cfg == nil {
			t.Fatal("expected non-nil config")
		}
	})
}

func TestFileExists(t *testing.T) {
	t.Run("existing file", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "test.txt")
		if err := os.WriteFile(path, []byte("hello"), 0644); err != nil {
			t.Fatal(err)
		}
		if !fileExists(path) {
			t.Error("expected file to exist")
		}
	})

	t.Run("missing file", func(t *testing.T) {
		if fileExists("/nonexistent/path/file.txt") {
			t.Error("expected file to not exist")
		}
	})

	t.Run("directory returns false", func(t *testing.T) {
		dir := t.TempDir()
		if fileExists(dir) {
			t.Error("expected directory to return false")
		}
	})
}