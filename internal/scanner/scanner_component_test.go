// Component tests exercise the data flow between the scanner and its mocked
// dependencies (plugins, providers, dashboard). For pure unit tests of
// individual functions, see scanner_test.go.
package scanner

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/hashicorp/go-hclog"
	"github.com/infracost/cli/internal/api/dashboard"
	testingconfig "github.com/infracost/cli/internal/config/testing"
	"github.com/infracost/cli/pkg/auth"
	parserMock "github.com/infracost/cli/pkg/plugins/parser/mocks"
	providerMock "github.com/infracost/cli/pkg/plugins/providers/mocks"
	"github.com/infracost/proto/gen/go/infracost/parser"
	"github.com/infracost/proto/gen/go/infracost/parser/api"
	"github.com/infracost/proto/gen/go/infracost/parser/event"
	"github.com/infracost/proto/gen/go/infracost/parser/terraform"
	"github.com/infracost/proto/gen/go/infracost/provider"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/encoding/protojson"
)

type testScannerOpts struct {
	// Parser mock responses (used by Scan tests).
	parseResponse *api.ParseResponse
	parseErr      error

	// Provider Process mock responses (used by Scan tests).
	awsResources    []*provider.Resource
	awsFinops       []*provider.FinopsPolicyResult
	googleResources []*provider.Resource
	googleFinops    []*provider.FinopsPolicyResult
	azureResources  []*provider.Resource
	azureFinops     []*provider.FinopsPolicyResult

	// Provider ListFinopsPolicies mock responses (used by ListPolicies tests).
	awsPolicies    []*provider.FinopsPolicy
	googlePolicies []*provider.FinopsPolicy
	azurePolicies  []*provider.FinopsPolicy

	// processValidator is called with the input to each provider's Process
	// method, allowing tests to assert on the fields sent to providers.
	processValidator func(*testing.T, *provider.Input)

	currency string
}

// newTestScanner creates a Scanner from the shared test config with mocked
// parser and provider responses. Both ListFinopsPolicies and Process are
// stubbed on each provider mock so the same scanner works for both
// ListPolicies and Scan tests.
func newTestScanner(t *testing.T, opts testScannerOpts) *Scanner {
	t.Helper()

	cfg := testingconfig.Config(t)

	// Set up parser mock when a parse response is configured.
	if opts.parseResponse != nil || opts.parseErr != nil {
		parserClient := parserMock.NewMockParserServiceClient(t)
		parserClient.EXPECT().Initialize(mock.Anything, mock.Anything).Return(&api.InitializeResponse{}, nil)
		parserClient.EXPECT().Parse(mock.Anything, mock.Anything).Return(opts.parseResponse, opts.parseErr)
		cfg.Plugins.Parser.Load = func(hclog.Level) (api.ParserServiceClient, func(), error) {
			return parserClient, func() {}, nil
		}
	}

	// Set up provider mocks — each mock supports both ListFinopsPolicies and Process.
	setupProviderMock := func(
		policies []*provider.FinopsPolicy,
		resources []*provider.Resource,
		finopsResults []*provider.FinopsPolicyResult,
	) func(hclog.Level) (provider.ProviderServiceClient, func(), error) {
		m := providerMock.NewMockProviderServiceClient(t)
		m.EXPECT().ListFinopsPolicies(mock.Anything, mock.Anything).Return(&provider.ListFinopsPoliciesResponse{
			Policies: policies,
		}, nil).Maybe()
		processCall := m.EXPECT().Process(mock.Anything, mock.Anything)
		if opts.processValidator != nil {
			processCall.Run(func(_ context.Context, in *provider.ProcessRequest, _ ...grpc.CallOption) {
				opts.processValidator(t, in.Input)
			})
		}
		processCall.Return(&provider.ProcessResponse{
			Output: &provider.Output{
				Resources:     resources,
				FinopsResults: finopsResults,
			},
		}, nil).Maybe()
		return func(hclog.Level) (provider.ProviderServiceClient, func(), error) {
			return m, func() {}, nil
		}
	}

	cfg.Plugins.Providers.LoadAWS = setupProviderMock(opts.awsPolicies, opts.awsResources, opts.awsFinops)
	cfg.Plugins.Providers.LoadGoogle = setupProviderMock(opts.googlePolicies, opts.googleResources, opts.googleFinops)
	cfg.Plugins.Providers.LoadAzurerm = setupProviderMock(opts.azurePolicies, opts.azureResources, opts.azureFinops)

	if opts.currency != "" {
		cfg.Currency = opts.currency
	}

	return NewScanner(&cfg)
}

// writeTestProject creates a temp directory with an infracost.yml and an empty
// main.tf so the scanner can discover and parse a project. It returns the
// absolute path to the directory. An optional configContent argument overrides
// the default infracost.yml content.
func writeTestProject(t *testing.T, configContent ...string) string {
	t.Helper()

	dir := t.TempDir()

	content := `version: "0.3"
projects:
  - path: .
    name: test-project
`
	if len(configContent) > 0 {
		content = configContent[0]
	}

	if err := os.WriteFile(filepath.Join(dir, "infracost.yml"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.tf"), []byte(""), 0644); err != nil {
		t.Fatal(err)
	}

	return dir
}

func TestListPolicies(t *testing.T) {
	t.Run("nil run parameters returns all policies", func(t *testing.T) {
		s := newTestScanner(t, testScannerOpts{
			awsPolicies: []*provider.FinopsPolicy{
				{Slug: "aws-policy-1", Name: "AWS Policy 1"},
				{Slug: "aws-policy-2", Name: "AWS Policy 2"},
			},
			googlePolicies: []*provider.FinopsPolicy{
				{Slug: "gcp-policy-1", Name: "GCP Policy 1"},
			},
		})

		finops, tags, err := s.ListPolicies(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(tags) != 0 {
			t.Errorf("expected 0 tag policies, got %d", len(tags))
		}
		if len(finops) != 3 {
			t.Fatalf("expected 3 finops policies, got %d", len(finops))
		}
		// verify sorted by slug
		for i := 1; i < len(finops); i++ {
			if finops[i-1].Slug > finops[i].Slug {
				t.Errorf("policies not sorted: %q > %q", finops[i-1].Slug, finops[i].Slug)
			}
		}
	})

	t.Run("run parameters filters to enabled policies", func(t *testing.T) {
		s := newTestScanner(t, testScannerOpts{
			awsPolicies: []*provider.FinopsPolicy{
				{Slug: "aws-enabled", Name: "Enabled"},
				{Slug: "aws-disabled", Name: "Disabled"},
			},
		})

		enabledSettings := &event.FinopsPolicySettings{Slug: "aws-enabled", Name: "Enabled"}
		settingsJSON, err := protojson.Marshal(enabledSettings)
		if err != nil {
			t.Fatal(err)
		}

		runParams := &dashboard.RunParameters{
			FinopsPolicies: []json.RawMessage{settingsJSON},
		}

		finops, _, err := s.ListPolicies(context.Background(), runParams)
		if err != nil {
			t.Fatal(err)
		}
		if len(finops) != 1 {
			t.Fatalf("expected 1 enabled policy, got %d", len(finops))
		}
		if finops[0].Slug != "aws-enabled" {
			t.Errorf("expected slug aws-enabled, got %q", finops[0].Slug)
		}
		if finops[0].Settings == nil {
			t.Error("expected settings to be populated")
		}
	})

	t.Run("tag policies from run parameters", func(t *testing.T) {
		s := newTestScanner(t, testScannerOpts{})

		tagPolicy := &event.TagPolicy{Id: "tp-1", Name: "Require env tag"}
		tagJSON, err := protojson.Marshal(tagPolicy)
		if err != nil {
			t.Fatal(err)
		}

		runParams := &dashboard.RunParameters{
			TagPolicies: []json.RawMessage{tagJSON},
		}

		_, tags, err := s.ListPolicies(context.Background(), runParams)
		if err != nil {
			t.Fatal(err)
		}
		if len(tags) != 1 {
			t.Fatalf("expected 1 tag policy, got %d", len(tags))
		}
		if tags[0].Name != "Require env tag" {
			t.Errorf("expected tag policy name %q, got %q", "Require env tag", tags[0].Name)
		}
	})

	t.Run("tag policies sorted by name then id", func(t *testing.T) {
		s := newTestScanner(t, testScannerOpts{})

		tags := []*event.TagPolicy{
			{Id: "tp-2", Name: "Zebra"},
			{Id: "tp-1", Name: "Alpha"},
			{Id: "tp-3", Name: "Alpha"},
		}

		var rawTags []json.RawMessage
		for _, tp := range tags {
			b, err := protojson.Marshal(tp)
			if err != nil {
				t.Fatal(err)
			}
			rawTags = append(rawTags, b)
		}

		runParams := &dashboard.RunParameters{
			TagPolicies: rawTags,
		}

		_, result, err := s.ListPolicies(context.Background(), runParams)
		if err != nil {
			t.Fatal(err)
		}
		if len(result) != 3 {
			t.Fatalf("expected 3 tag policies, got %d", len(result))
		}
		if result[0].Id != "tp-1" || result[1].Id != "tp-3" || result[2].Id != "tp-2" {
			t.Errorf("unexpected order: %s, %s, %s", result[0].Id, result[1].Id, result[2].Id)
		}
	})
}

// emptyUsageDefaults returns a minimal JSON-encoded UsageDefaults for run parameters.
func emptyUsageDefaults(t *testing.T) json.RawMessage {
	t.Helper()
	b, err := protojson.Marshal(&event.UsageDefaults{})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// awsTerraformParseResponse returns a ParseResponse with terraform resources.
func awsTerraformParseResponse(resourceTypes ...string) *api.ParseResponse {
	resources := make([]*terraform.Resource, len(resourceTypes))
	for i, rt := range resourceTypes {
		resources[i] = &terraform.Resource{Type: rt}
	}
	return &api.ParseResponse{
		Result: &api.ParseResponseResult{
			Value: &api.ParseResponseResult_Terraform{
				Terraform: &terraform.ModuleResult{
					Resources: resources,
				},
			},
		},
	}
}

func TestScan(t *testing.T) {
	t.Run("basic scan with aws resources", func(t *testing.T) {
		dir := writeTestProject(t)
		s := newTestScanner(t, testScannerOpts{
			parseResponse: awsTerraformParseResponse("aws_instance"),
			awsResources: []*provider.Resource{
				{Name: "aws_instance.web", Type: "aws_instance"},
			},
		})

		result, err := s.Scan(context.Background(), dashboard.RunParameters{
			UsageDefaults: emptyUsageDefaults(t),
		}, dir, "main", auth.AuthenticationToken("test-token"))
		if err != nil {
			t.Fatal(err)
		}
		require.NotNil(t, result, "expected non-nil result")
		require.Len(t, result.Projects, 1)
		require.Len(t, result.Projects[0].Resources, 1)
		if result.Projects[0].Resources[0].Name != "aws_instance.web" {
			t.Errorf("expected resource name %q, got %q", "aws_instance.web", result.Projects[0].Resources[0].Name)
		}
	})

	t.Run("repo usage file is loaded", func(t *testing.T) {
		dir := writeTestProject(t, `version: "0.3"
usage_file: usage.yml
projects:
  - path: .
    name: test-project
`)
		usageContent := `version: "0.1"
resource_type_default_usage:
  aws_instance:
    monthly_hrs: 730
`
		if err := os.WriteFile(filepath.Join(dir, "usage.yml"), []byte(usageContent), 0644); err != nil {
			t.Fatal(err)
		}

		s := newTestScanner(t, testScannerOpts{
			parseResponse: awsTerraformParseResponse("aws_instance"),
			awsResources:  []*provider.Resource{{Name: "aws_instance.web", Type: "aws_instance"}},
		})

		result, err := s.Scan(context.Background(), dashboard.RunParameters{
			UsageDefaults: emptyUsageDefaults(t),
		}, dir, "main", auth.AuthenticationToken("test-token"))
		if err != nil {
			t.Fatal(err)
		}
		if result.EstimatedUsageCounts == nil {
			t.Fatal("expected estimated usage counts to be populated")
		}
		if result.EstimatedUsageCounts["aws_instance.monthly_hrs"] != 1 {
			t.Errorf("expected aws_instance.monthly_hrs estimated=1, got %d", result.EstimatedUsageCounts["aws_instance.monthly_hrs"])
		}
	})

	t.Run("scanner currency code takes precedence", func(t *testing.T) {
		dir := writeTestProject(t, `version: "0.3"
currency: GBP
projects:
  - path: .
    name: test-project
`)
		s := newTestScanner(t, testScannerOpts{
			parseResponse: awsTerraformParseResponse("aws_instance"),
			awsResources:  []*provider.Resource{{Name: "aws_instance.web", Type: "aws_instance"}},
			currency:      "EUR",
		})
		
		result, err := s.Scan(context.Background(), dashboard.RunParameters{
			UsageDefaults: emptyUsageDefaults(t),
		}, dir, "main", auth.AuthenticationToken("test-token"))
		if err != nil {
			t.Fatal(err)
		}
		if result.Config.Currency != "EUR" {
			t.Errorf("expected currency EUR, got %q", result.Config.Currency)
		}
	})

	t.Run("critical diagnostics short-circuit before providers", func(t *testing.T) {
		dir := writeTestProject(t)
		s := newTestScanner(t, testScannerOpts{
			parseResponse: &api.ParseResponse{
				Diagnostics: []*parser.Diagnostic{
					{Error: "something went wrong", Critical: true},
				},
				Result: &api.ParseResponseResult{
					Value: &api.ParseResponseResult_Terraform{
						Terraform: &terraform.ModuleResult{
							Resources: []*terraform.Resource{{Type: "aws_instance"}},
						},
					},
				},
			},
		})

		result, err := s.Scan(context.Background(), dashboard.RunParameters{
			UsageDefaults: emptyUsageDefaults(t),
		}, dir, "main", auth.AuthenticationToken("test-token"))
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Projects) != 1 {
			t.Fatalf("expected 1 project, got %d", len(result.Projects))
		}
		proj := result.Projects[0]
		if len(proj.Diagnostics) == 0 {
			t.Fatal("expected diagnostics to be populated")
		}
		// Providers should not have been called, so no resources
		if len(proj.Resources) != 0 {
			t.Errorf("expected 0 resources when critical diagnostic, got %d", len(proj.Resources))
		}
	})

	t.Run("nil parse result returns error", func(t *testing.T) {
		dir := writeTestProject(t)
		s := newTestScanner(t, testScannerOpts{
			parseResponse: &api.ParseResponse{
				Result: nil,
			},
		})

		_, err := s.Scan(context.Background(), dashboard.RunParameters{
			UsageDefaults: emptyUsageDefaults(t),
		}, dir, "main", auth.AuthenticationToken("test-token"))
		if err == nil {
			t.Fatal("expected error for nil parse result")
		}
	})

	t.Run("multiple providers", func(t *testing.T) {
		dir := writeTestProject(t)
		s := newTestScanner(t, testScannerOpts{
			parseResponse: awsTerraformParseResponse("aws_instance", "google_compute_instance"),
			awsResources: []*provider.Resource{
				{Name: "aws_instance.web", Type: "aws_instance"},
			},
			googleResources: []*provider.Resource{
				{Name: "google_compute_instance.vm", Type: "google_compute_instance"},
			},
		})

		result, err := s.Scan(context.Background(), dashboard.RunParameters{
			UsageDefaults: emptyUsageDefaults(t),
		}, dir, "main", auth.AuthenticationToken("test-token"))
		if err != nil {
			t.Fatal(err)
		}
		proj := result.Projects[0]
		if len(proj.Resources) != 2 {
			t.Fatalf("expected 2 resources, got %d", len(proj.Resources))
		}
	})

	t.Run("finops results collected from providers", func(t *testing.T) {
		dir := writeTestProject(t)
		s := newTestScanner(t, testScannerOpts{
			parseResponse: awsTerraformParseResponse("aws_instance"),
			awsResources: []*provider.Resource{
				{Name: "aws_instance.web", Type: "aws_instance"},
			},
			awsFinops: []*provider.FinopsPolicyResult{
				{PolicySlug: "test-policy"},
			},
		})

		result, err := s.Scan(context.Background(), dashboard.RunParameters{
			UsageDefaults: emptyUsageDefaults(t),
		}, dir, "main", auth.AuthenticationToken("test-token"))
		if err != nil {
			t.Fatal(err)
		}
		proj := result.Projects[0]
		if len(proj.FinopsResults) != 1 {
			t.Fatalf("expected 1 finops result, got %d", len(proj.FinopsResults))
		}
		if proj.FinopsResults[0].PolicySlug != "test-policy" {
			t.Errorf("expected slug test-policy, got %q", proj.FinopsResults[0].PolicySlug)
		}
	})

	t.Run("production filters passed to providers", func(t *testing.T) {
		dir := writeTestProject(t)

		filter := &event.ProductionFilter{
			Type:    event.ProductionFilter_BRANCH,
			Value:   "main",
			Include: true,
		}
		filterJSON, err := protojson.Marshal(filter)
		if err != nil {
			t.Fatal(err)
		}

		s := newTestScanner(t, testScannerOpts{
			parseResponse: awsTerraformParseResponse("aws_instance"),
			awsResources:  []*provider.Resource{{Name: "aws_instance.web", Type: "aws_instance"}},
			processValidator: func(t *testing.T, input *provider.Input) {
				t.Helper()
				if input.ProjectInfo == nil {
					t.Fatal("expected ProjectInfo to be set")
				}
				if !input.ProjectInfo.IsProduction {
					t.Error("expected IsProduction to be true for matching branch filter")
				}
			},
		})

		_, err = s.Scan(context.Background(), dashboard.RunParameters{
			UsageDefaults:     emptyUsageDefaults(t),
			ProductionFilters: []json.RawMessage{filterJSON},
		}, dir, "main", auth.AuthenticationToken("test-token"))
		if err != nil {
			t.Fatal(err)
		}
	})

	t.Run("non-matching production filter results in non-production", func(t *testing.T) {
		dir := writeTestProject(t)

		filter := &event.ProductionFilter{
			Type:    event.ProductionFilter_BRANCH,
			Value:   "release-*",
			Include: true,
		}
		filterJSON, err := protojson.Marshal(filter)
		if err != nil {
			t.Fatal(err)
		}

		s := newTestScanner(t, testScannerOpts{
			parseResponse: awsTerraformParseResponse("aws_instance"),
			awsResources:  []*provider.Resource{{Name: "aws_instance.web", Type: "aws_instance"}},
			processValidator: func(t *testing.T, input *provider.Input) {
				t.Helper()
				if input.ProjectInfo.IsProduction {
					t.Error("expected IsProduction to be false for non-matching branch filter")
				}
			},
		})

		_, err = s.Scan(context.Background(), dashboard.RunParameters{
			UsageDefaults:     emptyUsageDefaults(t),
			ProductionFilters: []json.RawMessage{filterJSON},
		}, dir, "main", auth.AuthenticationToken("test-token"))
		if err != nil {
			t.Fatal(err)
		}
	})

	t.Run("finops policy settings passed to providers", func(t *testing.T) {
		dir := writeTestProject(t)

		policySettings := &event.FinopsPolicySettings{
			Slug: "test-finops-policy",
			Name: "Test FinOps Policy",
		}
		policyJSON, err := protojson.Marshal(policySettings)
		if err != nil {
			t.Fatal(err)
		}

		s := newTestScanner(t, testScannerOpts{
			parseResponse: awsTerraformParseResponse("aws_instance"),
			awsResources:  []*provider.Resource{{Name: "aws_instance.web", Type: "aws_instance"}},
			processValidator: func(t *testing.T, input *provider.Input) {
				t.Helper()
				if input.FinopsPolicyConfig == nil {
					t.Fatal("expected FinopsPolicyConfig to be set")
				}
				if len(input.FinopsPolicyConfig.Policies) != 1 {
					t.Fatalf("expected 1 finops policy, got %d", len(input.FinopsPolicyConfig.Policies))
				}
				if input.FinopsPolicyConfig.Policies[0].Slug != "test-finops-policy" {
					t.Errorf("expected slug test-finops-policy, got %q", input.FinopsPolicyConfig.Policies[0].Slug)
				}
			},
		})

		_, err = s.Scan(context.Background(), dashboard.RunParameters{
			UsageDefaults:  emptyUsageDefaults(t),
			FinopsPolicies: []json.RawMessage{policyJSON},
		}, dir, "main", auth.AuthenticationToken("test-token"))
		if err != nil {
			t.Fatal(err)
		}
	})

	t.Run("tag policies evaluated against resources", func(t *testing.T) {
		dir := writeTestProject(t)

		tagPolicy := &event.TagPolicy{
			Id:   "tp-1",
			Name: "Require env tag",
			Requirements: []*event.TagPolicyRequirement{
				{
					Key:  "env",
					Type: event.TagPolicyRequirement_ANY,
				},
			},
		}
		tagJSON, err := protojson.Marshal(tagPolicy)
		if err != nil {
			t.Fatal(err)
		}

		s := newTestScanner(t, testScannerOpts{
			parseResponse: awsTerraformParseResponse("aws_instance"),
			awsResources: []*provider.Resource{
				{
					Name: "aws_instance.web",
					Type: "aws_instance",
					Tagging: &provider.Tagging{
						SupportsTags: true,
						Tags:         []*provider.Tag{{Key: "env", Value: "prod"}},
					},
				},
				{
					Name: "aws_instance.worker",
					Type: "aws_instance",
					Tagging: &provider.Tagging{
						SupportsTags: true,
						// no env tag
					},
				},
			},
		})

		result, err := s.Scan(context.Background(), dashboard.RunParameters{
			UsageDefaults: emptyUsageDefaults(t),
			TagPolicies:   []json.RawMessage{tagJSON},
		}, dir, "main", auth.AuthenticationToken("test-token"))
		if err != nil {
			t.Fatal(err)
		}
		proj := result.Projects[0]
		if len(proj.TagPolicyResults) == 0 {
			t.Fatal("expected tag policy results to be populated")
		}
	})

	t.Run("budgets evaluated against resources", func(t *testing.T) {
		dir := writeTestProject(t)

		budget := &event.Budget{
			Id:        "b-1",
			PrComment: true,
			Tags: []*event.BudgetTag{
				{Key: "env", Value: "production"},
			},
		}
		budgetJSON, err := protojson.Marshal(budget)
		if err != nil {
			t.Fatal(err)
		}

		s := newTestScanner(t, testScannerOpts{
			parseResponse: awsTerraformParseResponse("aws_instance"),
			awsResources: []*provider.Resource{
				{
					Name: "aws_instance.web",
					Type: "aws_instance",
					Tagging: &provider.Tagging{
						SupportsTags: true,
						Tags:         []*provider.Tag{{Key: "env", Value: "production"}},
					},
					Costs: &provider.ResourceCosts{
						Components: []*provider.CostComponent{
							{
								PeriodPrice: &provider.PeriodPrice{
									Price:  (&provider.PeriodPrice{}).GetPrice(), // zero price placeholder
									Period: provider.Period_MONTH,
								},
							},
						},
					},
				},
			},
		})

		result, err := s.Scan(context.Background(), dashboard.RunParameters{
			UsageDefaults: emptyUsageDefaults(t),
			Budgets:       []json.RawMessage{budgetJSON},
		}, dir, "main", auth.AuthenticationToken("test-token"))
		if err != nil {
			t.Fatal(err)
		}
		if len(result.BudgetResults) != 1 {
			t.Fatalf("expected 1 budget result, got %d", len(result.BudgetResults))
		}
		if result.BudgetResults[0].BudgetID != "b-1" {
			t.Errorf("expected budget ID %q, got %q", "b-1", result.BudgetResults[0].BudgetID)
		}
	})

	t.Run("project config populated", func(t *testing.T) {
		dir := writeTestProject(t)
		s := newTestScanner(t, testScannerOpts{
			parseResponse: awsTerraformParseResponse("aws_instance"),
			awsResources:  []*provider.Resource{{Name: "aws_instance.web", Type: "aws_instance"}},
		})

		result, err := s.Scan(context.Background(), dashboard.RunParameters{
			UsageDefaults: emptyUsageDefaults(t),
		}, dir, "main", auth.AuthenticationToken("test-token"))
		if err != nil {
			t.Fatal(err)
		}
		if result.Config == nil {
			t.Fatal("expected result config to be set")
		}
		proj := result.Projects[0]
		if proj.Config == nil {
			t.Fatal("expected project config to be set")
		}
		if proj.Config.Name != "test-project" {
			t.Errorf("expected project name %q, got %q", "test-project", proj.Config.Name)
		}
	})
}
