package scanner

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/hashicorp/go-hclog"
	"github.com/infracost/cli/pkg/logging"
	"github.com/infracost/go-proto/pkg/flag"
	"github.com/infracost/cli/pkg/plugins"
	repoconfig "github.com/infracost/config"
	"github.com/infracost/go-proto/pkg/diagnostic"
	goprotoevent "github.com/infracost/go-proto/pkg/event"
	providerconv "github.com/infracost/go-proto/pkg/providers"
	"github.com/infracost/go-proto/pkg/rat"
	parserpb "github.com/infracost/proto/gen/go/infracost/parser/api"
	"github.com/infracost/proto/gen/go/infracost/parser/event"
	"github.com/infracost/proto/gen/go/infracost/parser/options"
	"github.com/infracost/proto/gen/go/infracost/parser/terraform"
	"github.com/infracost/proto/gen/go/infracost/provider"
	"github.com/infracost/proto/gen/go/infracost/usage"
)

// ProjectResult holds the outputs for a single project scan.
type ProjectResult struct {
	Name              string
	Config            *repoconfig.Project
	TotalMonthlyCost  *rat.Rat
	Resources         []*provider.Resource
	FinopsResults     []*provider.FinopsPolicyResult
	TagPolicyResults  []goprotoevent.TaggingPolicyResult
	Diagnostics       []*diagnostic.Diagnostic
	RemoteModuleCalls []string
}

// ScanProjectOptions contains all the inputs needed to scan a single project.
type ScanProjectOptions struct {
	RootDir    string
	CacheDir   string
	RepoConfig *repoconfig.Config
	Project    *repoconfig.Project

	AccessToken     string // nolint:gosec // G117: passed to providers, and not exposed
	BranchName      string
	RepositoryName  string
	OrgID           string
	PricingEndpoint string
	Currency        string
	TraceID         string

	ProductionFilters         []*event.ProductionFilter
	FinopsPolicies            []*event.FinopsPolicySettings
	TagPolicies               []*event.TagPolicy
	UsageDefaults             *event.UsageDefaults
	RepoUsage                 *usage.Usage
	PreviousResourceAddresses []string

	Plugins *plugins.Config
	Logging logging.Config
}

// ScanProject scans a single project and returns its resources, costs, and policy results.
func ScanProject(ctx context.Context, opts *ScanProjectOptions) (*ProjectResult, error) {
	absoluteProjectPath := filepath.Clean(filepath.Join(opts.RootDir, opts.Project.Path))

	// Load project-level usage data (overlay on top of repo-level).
	projectUsage := opts.RepoUsage
	if opts.Project.UsageFile != "" && opts.Project.UsageFile != opts.RepoConfig.UsageFilePath {
		usagePath := filepath.Join(opts.RootDir, opts.Project.UsageFile)
		if stat, err := os.Stat(usagePath); err == nil && !stat.IsDir() {
			f, err := os.Open(usagePath) // #nosec G304 -- user-specified usage file in their repo
			if err != nil {
				return nil, fmt.Errorf("failed to open usage file %q: %w", usagePath, err)
			}
			usageDefaults := LoadUsageDefaults(opts.UsageDefaults, opts.Project.Name)
			u, err := LoadUsageData(f, usageDefaults)
			_ = f.Close()
			if err != nil {
				return nil, fmt.Errorf("failed to load usage data from %q: %w", usagePath, err)
			}
			projectUsage = u
		}
	}

	// Evaluate production filters.
	isProduction := EvaluateProductionFilters(opts.ProductionFilters, opts.RepositoryName, opts.BranchName, opts.Project.Name)

	if err := opts.Plugins.EnsureParser(); err != nil {
		return nil, fmt.Errorf("failed to ensure parser plugin: %w", err)
	}

	response, err := opts.Plugins.Parser.Parse(ctx, absoluteProjectPath, opts.RepoConfig, opts.Project, opts.Logging.ToHCLogLevel(), &options.GenericOptions{
		ProjectName:        opts.Project.Name,
		EnvironmentName:    opts.Project.EnvName,
		RepoDirectory:      opts.RootDir,
		TemporaryDirectory: os.TempDir(),
		CacheDirectory:     opts.CacheDir,
		WorkingDirectory:   opts.RootDir,
	})
	if err != nil {
		return nil, fmt.Errorf("parser plugin error: %w (set INFRACOST_CLI_LOG_LEVEL=debug for more details)", err)
	}

	projectResult := &ProjectResult{
		Name:   opts.Project.Name,
		Config: opts.Project,
	}

	// Extract diagnostics from the parser response.
	if response != nil && response.Diagnostics != nil {
		diags := diagnostic.FromProto(response.Diagnostics)
		projectResult.Diagnostics = diags.Unwrap()
		if diags.Critical().Len() > 0 {
			return projectResult, nil
		}
	}

	if response.Result == nil {
		return nil, fmt.Errorf("parser plugin returned no result and no critical diagnostics")
	}

	var requiredProviders []provider.Provider
	switch result := response.Result.Value.(type) {
	case *parserpb.ParseResponseResult_Terraform:
		rps := make(map[provider.Provider]struct{})
		unsupported := GetRequiredProviders(result.Terraform, rps)
		for u := range unsupported {
			logging.Warnf("skipping unsupported provider: %s", u)
		}
		requiredProviders = slices.Collect(maps.Keys(rps))
		projectResult.RemoteModuleCalls = extractRemoteModuleCalls(result.Terraform)
	case *parserpb.ParseResponseResult_Cloudformation:
		requiredProviders = []provider.Provider{provider.Provider_PROVIDER_AWS}
	default:
		return nil, fmt.Errorf("unsupported parse result type: %T", result)
	}

	input := &provider.Input{
		ParseResult:  response,
		AbsolutePath: absoluteProjectPath,
		ProjectInfo: &provider.ProjectInfo{
			Name:         opts.Project.Name,
			BranchName:   opts.BranchName,
			Workspace:    opts.Project.Terraform.Workspace,
			IsProduction: isProduction,
		},
		PreviousResourceAddresses: opts.PreviousResourceAddresses,
		Usage:                     projectUsage,
		FinopsPolicyConfig: &provider.FinopsPolicyConfiguration{
			Policies: opts.FinopsPolicies,
		},
		Features: &provider.Features{
			EnablePriceLookups:         true,
			EnableRecommendations:      true,
			EnableFinopsPolicies:       true,
			EnableEnvironmentalMetrics: true,
		},
		Settings: &provider.Settings{
			Currency:      opts.Currency,
			UseDiskCaches: true,
		},
		Infracost: &provider.Infracost{
			ApiKey:             opts.AccessToken,
			PricingApiEndpoint: opts.PricingEndpoint,
			TraceId:            opts.TraceID,
			OrgId:              &opts.OrgID,
		},
	}

	for _, rp := range requiredProviders {
		if err := opts.Plugins.EnsureProvider(rp); err != nil {
			logging.WithError(err).Msgf("failed to ensure provider %s", rp)
			continue
		}

		var loader func(hclog.Level) (provider.ProviderServiceClient, func(), error)
		switch rp {
		case provider.Provider_PROVIDER_AWS:
			loader = opts.Plugins.Providers.LoadAWS
		case provider.Provider_PROVIDER_GOOGLE:
			loader = opts.Plugins.Providers.LoadGoogle
		case provider.Provider_PROVIDER_AZURERM:
			loader = opts.Plugins.Providers.LoadAzurerm
		default:
			continue
		}

		rs, ps, err := opts.Plugins.Providers.ProcessInput(ctx, rp, input, loader, opts.Logging.ToHCLogLevel())
		if err != nil {
			return nil, fmt.Errorf("failed to execute provider %s: %w", rp, err)
		}
		projectResult.Resources = append(projectResult.Resources, rs...)
		projectResult.FinopsResults = append(projectResult.FinopsResults, ps...)
	}

	// Evaluate tag policies against all provider resources.
	projectResult.TagPolicyResults = goprotoevent.TagPolicies(opts.TagPolicies).EvaluateAgainstResources(projectResult.Resources, input.ProjectInfo)

	// Compute total monthly cost from resources.
	projectResult.TotalMonthlyCost = TotalMonthlyCostFromResources(projectResult.Resources)

	return projectResult, nil
}

// GetRequiredProviders extracts the set of cloud providers required by a Terraform
// module result. Returns the set of unsupported provider prefixes.
func GetRequiredProviders(result *terraform.ModuleResult, providers map[provider.Provider]struct{}) map[string]struct{} {
	unsupported := make(map[string]struct{})
	for _, resource := range result.Resources {
		raw, _, _ := strings.Cut(resource.Type, "_")
		if p := providerconv.ToProto(raw); p != provider.Provider_PROVIDER_UNSPECIFIED {
			providers[p] = struct{}{}
			continue
		}
		unsupported[raw] = struct{}{}
	}
	for _, module := range result.Modules {
		for _, r := range module.Results {
			us := GetRequiredProviders(r, providers)
			for k := range us {
				unsupported[k] = struct{}{}
			}
		}
	}
	return unsupported
}

// extractRemoteModuleCalls extracts unique remote (non-registry) module call
// URLs from a terraform parse result, stripping credentials and query parameters.
// Registry modules are excluded since they don't correspond to git repositories.
func extractRemoteModuleCalls(module *terraform.ModuleResult) []string {
	if module == nil {
		return nil
	}
	seen := make(map[string]struct{})
	for _, call := range collectRemoteModuleCalls(module) {
		seen[call] = struct{}{}
	}
	result := make([]string, 0, len(seen))
	for call := range seen {
		result = append(result, call)
	}
	return result
}

func collectRemoteModuleCalls(module *terraform.ModuleResult) []string {
	var results []string
	if module.LoadData != nil && module.LoadData.Source != nil &&
		module.LoadData.Source.Flags&uint64(flag.RegistryModule) == 0 &&
		module.LoadData.Source.Flags&uint64(flag.RemoteModule) != 0 {
		url := credRedactionRegex.ReplaceAllString(module.LoadData.Source.Base, "://")
		url, _, _ = strings.Cut(url, "?")
		results = append(results, url)
	}
	for _, mods := range module.Modules {
		for _, mod := range mods.Results {
			results = append(results, collectRemoteModuleCalls(mod)...)
		}
	}
	return results
}

var credRedactionRegex = regexp.MustCompile(`(?im)://(.+):(.+)@`)
