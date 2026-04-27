package scanner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/hashicorp/go-hclog"
	"github.com/infracost/cli/internal/api/dashboard"
	"github.com/infracost/cli/internal/config"
	"github.com/infracost/cli/internal/format"
	"github.com/infracost/cli/internal/trace"
	"github.com/infracost/cli/pkg/logging"
	"github.com/infracost/cli/pkg/plugins"
	pkgscanner "github.com/infracost/cli/pkg/scanner"
	repoconfig "github.com/infracost/config"
	goprotoevent "github.com/infracost/go-proto/pkg/event"
	"github.com/infracost/proto/gen/go/infracost/parser/event"
	"github.com/infracost/proto/gen/go/infracost/provider"
	"google.golang.org/protobuf/encoding/protojson"

	"golang.org/x/oauth2"
)

var (
	pj = protojson.UnmarshalOptions{
		DiscardUnknown: true,
	}
)

type Scanner struct {
	plugins         *plugins.Config
	logging         logging.Config
	dashboard       dashboard.Config
	currency        string
	pricingEndpoint string
}

func NewScanner(config *config.Config) *Scanner {
	return &Scanner{
		plugins:         &config.Plugins,
		logging:         config.Logging,
		dashboard:       config.Dashboard,
		currency:        config.Currency,
		pricingEndpoint: config.PricingEndpoint,
	}
}

type FinOpsPolicy struct {
	*provider.FinopsPolicy
	Settings *event.FinopsPolicySettings
	Provider string
}

type TaggingPolicy struct {
	*event.TagPolicy
}

func (s *Scanner) ListPolicies(ctx context.Context, runParameters *dashboard.RunParameters, providers []provider.Provider) ([]FinOpsPolicy, []TaggingPolicy, error) {

	var tagPolicies []*event.TagPolicy
	var finopsPolicySettings []*event.FinopsPolicySettings
	var hasRunParameters bool

	if runParameters != nil {
		tagPolicies = make([]*event.TagPolicy, 0, len(runParameters.TagPolicies))
		for _, p := range runParameters.TagPolicies {
			policy := new(event.TagPolicy)
			if err := pj.Unmarshal(p, policy); err != nil {
				return nil, nil, fmt.Errorf("failed to unmarshal tag policy: %w", err)
			}
			tagPolicies = append(tagPolicies, policy)
		}

		finopsPolicySettings = make([]*event.FinopsPolicySettings, 0, len(runParameters.FinopsPolicies))
		for _, p := range runParameters.FinopsPolicies {
			policy := new(event.FinopsPolicySettings)
			if err := pj.Unmarshal(p, policy); err != nil {
				return nil, nil, fmt.Errorf("failed to unmarshal FinOps policy: %w", err)
			}
			finopsPolicySettings = append(finopsPolicySettings, policy)
		}

		hasRunParameters = true
	}

	pluginLoaders := map[provider.Provider]func(hclog.Level) (provider.ProviderServiceClient, func(), error){
		provider.Provider_PROVIDER_AWS:     s.plugins.Providers.LoadAWS,
		provider.Provider_PROVIDER_GOOGLE:  s.plugins.Providers.LoadGoogle,
		provider.Provider_PROVIDER_AZURERM: s.plugins.Providers.LoadAzurerm,
	}

	if providers == nil {
		providers = []provider.Provider{
			provider.Provider_PROVIDER_AWS,
			provider.Provider_PROVIDER_GOOGLE,
			provider.Provider_PROVIDER_AZURERM,
		}
	}

	var finOpsPolicies []FinOpsPolicy
	for _, prov := range providers {
		pluginLoader, ok := pluginLoaders[prov]
		if !ok {
			continue
		}
		if err := s.plugins.EnsureProvider(prov); err != nil {
			logging.WithError(err).Msgf("failed to ensure provider %s", prov)
			continue
		}
		providerFinopsPolicies, err := s.plugins.Providers.ListFinopsPolicies(ctx, pluginLoader)
		if err != nil {
			logging.WithError(err).Msgf("failed to list FinOps policies for provider %s", prov)
			continue
		}
		for _, policy := range providerFinopsPolicies {
			var settings *event.FinopsPolicySettings
			if hasRunParameters {
				var enabled bool
				for _, s := range finopsPolicySettings {
					if s.Slug == policy.Slug {
						enabled = true
						settings = s
						break
					}
				}
				if !enabled {
					continue
				}
			}
			finOpsPolicies = append(finOpsPolicies, FinOpsPolicy{
				FinopsPolicy: policy,
				Settings:     settings,
				Provider:     strings.TrimPrefix(prov.String(), "PROVIDER_"),
			})
		}
	}

	var outputTagPolicies []TaggingPolicy
	for _, p := range tagPolicies {
		outputTagPolicies = append(outputTagPolicies, TaggingPolicy{TagPolicy: p})
	}

	sort.Slice(finOpsPolicies, func(i, j int) bool {
		return finOpsPolicies[i].Slug < finOpsPolicies[j].Slug
	})

	sort.Slice(outputTagPolicies, func(i, j int) bool {
		a := outputTagPolicies[i]
		b := outputTagPolicies[j]
		if a.Name == b.Name {
			return a.Id < b.Id
		}
		return a.Name < b.Name
	})

	return finOpsPolicies, outputTagPolicies, nil
}

func (s *Scanner) Scan(ctx context.Context, runParameters dashboard.RunParameters, absoluteDirectory, branchName string, tokenSource oauth2.TokenSource) (*format.Result, error) {
	var result format.Result

	token, err := tokenSource.Token()
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve access token: %w", err)
	}

	repositoryName := runParameters.RepositoryName

	usageDefaults := new(event.UsageDefaults)
	if err := pj.Unmarshal(runParameters.UsageDefaults, usageDefaults); err != nil {
		return nil, fmt.Errorf("failed to unmarshal usage defaults: %w", err)
	}

	var repoConfigOpts []repoconfig.GenerationOption
	if len(repositoryName) > 0 {
		repoConfigOpts = append(repoConfigOpts, repoconfig.WithRepoName(repositoryName))
	}
	if len(branchName) > 0 {
		repoConfigOpts = append(repoConfigOpts, repoconfig.WithBranch(branchName))
	}

	repoConfig, err := pkgscanner.LoadOrGenerateRepositoryConfig(absoluteDirectory, repoConfigOpts...)
	if err != nil {
		return nil, fmt.Errorf("repository configuration error: %w", err)
	}
	result.Config = repoConfig
	if s.currency != "" {
		result.Config.Currency = s.currency
	}
	if result.Config.Currency == "" {
		result.Config.Currency = "USD"
	}

	// load the repo-level usage file if it exists, merging on top of the API defaults
	repoUsage := pkgscanner.LoadUsageDefaults(usageDefaults, "")
	if repoConfig.UsageFilePath != "" {
		usagePath := filepath.Join(absoluteDirectory, repoConfig.UsageFilePath)
		if stat, err := os.Stat(usagePath); err == nil && !stat.IsDir() {
			f, err := os.Open(usagePath) // #nosec G304
			if err != nil {
				return nil, fmt.Errorf("failed to open usage file %q: %w", usagePath, err)
			}
			u, err := pkgscanner.LoadUsageData(f, repoUsage)
			_ = f.Close()
			if err != nil {
				return nil, fmt.Errorf("failed to load usage data from %q: %w", usagePath, err)
			}
			repoUsage = u
		}
	}

	result.EstimatedUsageCounts, result.UnestimatedUsageCounts = pkgscanner.CountUsage(repoUsage)

	productionFilters := make([]*event.ProductionFilter, 0, len(runParameters.ProductionFilters))
	for _, f := range runParameters.ProductionFilters {
		filter := new(event.ProductionFilter)
		if err := pj.Unmarshal(f, filter); err != nil {
			return nil, fmt.Errorf("failed to unmarshal production filter: %w", err)
		}
		productionFilters = append(productionFilters, filter)
	}

	tagPolicies := make([]*event.TagPolicy, 0, len(runParameters.TagPolicies))
	for _, p := range runParameters.TagPolicies {
		policy := new(event.TagPolicy)
		if err := pj.Unmarshal(p, policy); err != nil {
			return nil, fmt.Errorf("failed to unmarshal tag policy: %w", err)
		}
		tagPolicies = append(tagPolicies, policy)
	}

	finopsPolicies := make([]*event.FinopsPolicySettings, 0, len(runParameters.FinopsPolicies))
	for _, p := range runParameters.FinopsPolicies {
		policy := new(event.FinopsPolicySettings)
		if err := pj.Unmarshal(p, policy); err != nil {
			return nil, fmt.Errorf("failed to unmarshal FinOps policy: %w", err)
		}
		finopsPolicies = append(finopsPolicies, policy)
	}

	cacheDir := filepath.Join(os.TempDir(), ".infracost", "cache")
	if err := os.MkdirAll(cacheDir, 0700); err != nil {
		return nil, fmt.Errorf("failed to create cache directory: %w", err)
	}

	for _, project := range repoConfig.Projects {
		projectResult, err := pkgscanner.ScanProject(ctx, &pkgscanner.ScanProjectOptions{
			RootDir:           absoluteDirectory,
			CacheDir:          cacheDir,
			RepoConfig:        repoConfig,
			Project:           project,
			AccessToken:       token.AccessToken,
			BranchName:        branchName,
			RepositoryName:    repositoryName,
			OrgID:             runParameters.OrganizationID,
			PricingEndpoint:   s.pricingEndpoint,
			Currency:          result.Config.Currency,
			TraceID:           trace.ID,
			ProductionFilters: productionFilters,
			FinopsPolicies:    finopsPolicies,
			TagPolicies:       tagPolicies,
			UsageDefaults:     usageDefaults,
			RepoUsage:         repoUsage,
			Plugins:           s.plugins,
			Logging:           s.logging,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to scan project %q: %w", project.Name, err)
		}

		result.Projects = append(result.Projects, &format.ProjectResult{
			Config:           projectResult.Config,
			Diagnostics:      projectResult.Diagnostics,
			Resources:        projectResult.Resources,
			FinopsResults:    projectResult.FinopsResults,
			TagPolicyResults: projectResult.TagPolicyResults,
		})
	}

	// Unmarshal guardrails, keeping only those with an absolute total threshold.
	var guardrails []*event.Guardrail
	for _, raw := range runParameters.Guardrails {
		g := new(event.Guardrail)
		if err := pj.Unmarshal(raw, g); err != nil {
			return nil, fmt.Errorf("failed to unmarshal guardrail: %w", err)
		}
		if g.TotalThreshold != nil {
			guardrails = append(guardrails, g)
		}
	}

	if len(guardrails) > 0 {
		headProjects := make([]pkgscanner.ProjectResult, 0, len(result.Projects))
		for _, p := range result.Projects {
			headProjects = append(headProjects, pkgscanner.ProjectResult{
				Name:             p.Config.Name,
				TotalMonthlyCost: pkgscanner.TotalMonthlyCostFromResources(p.Resources),
			})
		}
		result.GuardrailResults = pkgscanner.EvaluateGuardrails(guardrails, nil, headProjects)
	}

	// Unmarshal budgets and evaluate against scan resources.
	var budgets []*event.Budget
	for _, raw := range runParameters.Budgets {
		b := new(event.Budget)
		if err := pj.Unmarshal(raw, b); err != nil {
			return nil, fmt.Errorf("failed to unmarshal budget: %w", err)
		}
		budgets = append(budgets, b)
	}

	if len(budgets) > 0 {
		var costInfos []goprotoevent.ResourceCostInfo
		for _, p := range result.Projects {
			costInfos = append(costInfos, pkgscanner.ResourceCostInfos(p.Resources)...)
		}
		result.BudgetResults = goprotoevent.Budgets(budgets).Evaluate(costInfos)
	}

	return &result, nil
}
