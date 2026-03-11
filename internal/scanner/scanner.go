package scanner

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/hashicorp/go-hclog"
	"github.com/infracost/cli/internal/api/dashboard"
	"github.com/infracost/cli/internal/config"
	"github.com/infracost/cli/internal/format"
	"github.com/infracost/cli/internal/logging"
	"github.com/infracost/cli/internal/tfutils"
	"github.com/infracost/cli/internal/trace"
	"github.com/infracost/cli/pkg/plugins"
	repoconfig "github.com/infracost/config"
	"github.com/infracost/go-proto/pkg/diagnostic"
	goprotoevent "github.com/infracost/go-proto/pkg/event"
	providerconv "github.com/infracost/go-proto/pkg/providers"
	"github.com/infracost/go-proto/pkg/rat"
	parser "github.com/infracost/proto/gen/go/infracost/parser/api"
	"github.com/infracost/proto/gen/go/infracost/parser/event"
	"github.com/infracost/proto/gen/go/infracost/parser/options"
	"github.com/infracost/proto/gen/go/infracost/parser/terraform"
	"github.com/infracost/proto/gen/go/infracost/provider"
	"github.com/infracost/proto/gen/go/infracost/usage"
	"google.golang.org/protobuf/encoding/protojson"

	"golang.org/x/oauth2"
)

var (
	preserver = uuid.New().String()
)

type Scanner struct {
	plugins         plugins.Config
	logging         logging.Config
	dashboard       dashboard.Config
	currency        string
	pricingEndpoint string
}

func NewScanner(config *config.Config) *Scanner {
	return &Scanner{
		plugins:         config.Plugins,
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

func (s *Scanner) ListPolicies(ctx context.Context, runParameters *dashboard.RunParameters) ([]FinOpsPolicy, []TaggingPolicy, error) {

	var tagPolicies []*event.TagPolicy
	var finopsPolicySettings []*event.FinopsPolicySettings
	var hasRunParameters bool

	if runParameters != nil {
		tagPolicies = make([]*event.TagPolicy, 0, len(runParameters.TagPolicies))
		for _, p := range runParameters.TagPolicies {
			policy := new(event.TagPolicy)
			if err := protojson.Unmarshal(p, policy); err != nil {
				return nil, nil, fmt.Errorf("failed to unmarshal tag policy: %w", err)
			}
			tagPolicies = append(tagPolicies, policy)
		}

		finopsPolicySettings = make([]*event.FinopsPolicySettings, 0, len(runParameters.FinopsPolicies))
		for _, p := range runParameters.FinopsPolicies {
			policy := new(event.FinopsPolicySettings)
			if err := protojson.Unmarshal(p, policy); err != nil {
				return nil, nil, fmt.Errorf("failed to unmarshal FinOps policy: %w", err)
			}
			finopsPolicySettings = append(finopsPolicySettings, policy)
		}

		hasRunParameters = true
	}

	// TODO: eventually we should query which plugins are installed and avoid harcoding the list of default providers
	plugins := map[provider.Provider]func(hclog.Level) (provider.ProviderServiceClient, func(), error){
		provider.Provider_PROVIDER_AWS:     s.plugins.Providers.LoadAWS,
		provider.Provider_PROVIDER_GOOGLE:  s.plugins.Providers.LoadGoogle,
		provider.Provider_PROVIDER_AZURERM: s.plugins.Providers.LoadAzurerm,
	}

	var finOpsPolicies []FinOpsPolicy
	for prov, pluginLoader := range plugins {
		providerFinopsPolicies, err := s.plugins.Providers.ListFinopsPolicies(ctx, pluginLoader)
		if err != nil {
			logging.WithError(err).Msgf("failed to list FinOps policies for provider %s", prov)
			continue
		}
		for _, policy := range providerFinopsPolicies {
			var settings *event.FinopsPolicySettings
			// if run params are available, only return policies which are enabled for this org
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
		a := finOpsPolicies[i]
		b := finOpsPolicies[j]
		return a.Slug < b.Slug
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
	if err := protojson.Unmarshal(runParameters.UsageDefaults, usageDefaults); err != nil {
		return nil, fmt.Errorf("failed to unmarshal usage defaults: %w", err)
	}

	var repoConfigOpts []repoconfig.GenerationOption
	if len(repositoryName) > 0 {
		repoConfigOpts = append(repoConfigOpts, repoconfig.WithRepoName(repositoryName))
	}
	if len(branchName) > 0 {
		repoConfigOpts = append(repoConfigOpts, repoconfig.WithBranch(branchName))
	}

	repoConfig, err := LoadOrGenerateRepositoryConfig(absoluteDirectory, repoConfigOpts...)
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
	repoUsage := loadUsageDefaults(usageDefaults, "") // load repo-level usage defaults first, which can be overridden by project-level usage defaults
	if repoConfig.UsageFilePath != "" {
		usagePath := filepath.Join(absoluteDirectory, repoConfig.UsageFilePath)
		if stat, err := os.Stat(usagePath); err == nil && !stat.IsDir() {
			f, err := os.Open(usagePath) // #nosec G304 -- we want to allow users to specify arbitrary usage file paths in their repo, so we need to allow opening files based on user input here.
			if err != nil {
				return nil, fmt.Errorf("failed to open usage file %q: %w", usagePath, err)
			}
			u, err := LoadUsageData(f, repoUsage)
			_ = f.Close()
			if err != nil {
				return nil, fmt.Errorf("failed to load usage data from %q: %w", usagePath, err)
			}
			repoUsage = u
		}
	}

	result.EstimatedUsageCounts, result.UnestimatedUsageCounts = countUsage(repoUsage)

	productionFilters := make([]*event.ProductionFilter, 0, len(runParameters.ProductionFilters))
	for _, f := range runParameters.ProductionFilters {
		filter := new(event.ProductionFilter)
		if err := protojson.Unmarshal(f, filter); err != nil {
			return nil, fmt.Errorf("failed to unmarshal production filter: %w", err)
		}
		productionFilters = append(productionFilters, filter)
	}

	tagPolicies := make([]*event.TagPolicy, 0, len(runParameters.TagPolicies))
	for _, p := range runParameters.TagPolicies {
		policy := new(event.TagPolicy)
		if err := protojson.Unmarshal(p, policy); err != nil {
			return nil, fmt.Errorf("failed to unmarshal tag policy: %w", err)
		}
		tagPolicies = append(tagPolicies, policy)
	}

	finopsPolicies := make([]*event.FinopsPolicySettings, 0, len(runParameters.FinopsPolicies))
	for _, p := range runParameters.FinopsPolicies {
		policy := new(event.FinopsPolicySettings)
		if err := protojson.Unmarshal(p, policy); err != nil {
			return nil, fmt.Errorf("failed to unmarshal FinOps policy: %w", err)
		}
		finopsPolicies = append(finopsPolicies, policy)
	}

	dirScanner := &DirectoryScanner{
		config:            s,
		repoConfig:        repoConfig,
		rootDirectory:     absoluteDirectory,
		cacheDir:          filepath.Join(os.TempDir(), ".infracost", "cache"),
		token:             token,
		productionFilters: productionFilters,
		branchName:        branchName,
		repositoryName:    repositoryName,
		finopsPolicies:    finopsPolicies,
		tagPolicies:       tagPolicies,
		usageDefaults:     usageDefaults,
		repoUsage:         repoUsage,
		orgID:             runParameters.OrganizationID,
	}

	if err := os.MkdirAll(dirScanner.cacheDir, 0700); err != nil {
		return nil, fmt.Errorf("failed to create cache directory: %w", err)
	}

	for _, project := range repoConfig.Projects {
		projectResult, err := dirScanner.ScanProject(ctx, project)
		if err != nil {
			return nil, fmt.Errorf("failed to scan project %q: %w", project.Name, err)
		}
		result.Projects = append(result.Projects, projectResult)
	}
	return &result, nil
}

type DirectoryScanner struct {
	config            *Scanner
	repoConfig        *repoconfig.Config
	rootDirectory     string
	cacheDir          string
	token             *oauth2.Token
	branchName        string
	repositoryName    string
	orgID             string
	finopsPolicies    []*event.FinopsPolicySettings
	tagPolicies       []*event.TagPolicy
	productionFilters []*event.ProductionFilter
	usageDefaults     *event.UsageDefaults
	repoUsage         *usage.Usage
}

func (s *DirectoryScanner) ScanProject(ctx context.Context, project *repoconfig.Project) (*format.ProjectResult, error) {

	var projectResult format.ProjectResult
	projectResult.Config = project

	projectUsage := s.repoUsage

	usageDefaults := loadUsageDefaults(s.usageDefaults, project.Name)

	if project.UsageFile != "" && project.UsageFile != s.repoConfig.UsageFilePath {
		usagePath := filepath.Join(s.rootDirectory, project.UsageFile)
		if stat, err := os.Stat(usagePath); err == nil && !stat.IsDir() {
			f, err := os.Open(usagePath) // #nosec G304 -- we want to allow users to specify arbitrary usage file paths in their repo, so we need to allow opening files based on user input here.
			if err != nil {
				return nil, fmt.Errorf("failed to open usage file %q: %w", usagePath, err)
			}
			u, err := LoadUsageData(f, usageDefaults)
			_ = f.Close()
			if err != nil {
				return nil, fmt.Errorf("failed to load usage data from %q: %w", usagePath, err)
			}
			projectUsage = u
		}
	}

	absoluteProjectPath := filepath.Clean(filepath.Join(s.rootDirectory, project.Path))

	terraformWorkspace := tfutils.GetCurrentWorkspace(absoluteProjectPath)
	if terraformWorkspace == "" {
		terraformWorkspace = project.Terraform.Workspace
	}

	if err := s.config.plugins.EnsureParser(); err != nil {
		return nil, fmt.Errorf("failed to ensure parser plugin: %w", err)
	}

	response, err := s.config.plugins.Parser.Parse(ctx, absoluteProjectPath, s.repoConfig, project, s.config.logging.ToHCLogLevel(), &options.GenericOptions{
		ProjectName:             project.Name,
		EnvironmentName:         project.EnvName,
		RepoDirectory:           s.rootDirectory,
		TemporaryDirectory:      os.TempDir(),
		CacheDirectory:          s.cacheDir,
		WorkingDirectory:        s.rootDirectory,
		CredentialSets:          nil,   // TODO: make this configurable
		AwsCredentials:          nil,   // TODO: make this configurable
		Debug:                   nil,   // TODO: make this configurable
		SparseCheckout:          false, // TODO: make this configurable
		ProxyRouter:             nil,   // TODO: make this configurable
		RemoteModuleCacheConfig: nil,   // TODO: make this configurable
		DependencyRequest:       nil,   // TODO: make this configurable
	})
	// TODO: the parser plugin needs to stop returning errors when parsing fails - we need to return the response still, with the diagnostics which explain the problem!
	if err != nil {
		return nil, fmt.Errorf("parser plugin error: %w", err)
	}

	if response != nil && response.Diagnostics != nil {
		diags := diagnostic.FromProto(response.Diagnostics)
		projectResult.Diagnostics = diags.Unwrap()
		if diags.Critical().Len() > 0 {
			return &projectResult, nil
		}
	}

	if response.Result == nil {
		return nil, fmt.Errorf("parser plugin returned no result and no critical diagnostics")
	}

	var requiredProviders []provider.Provider
	switch result := response.Result.Value.(type) {
	case *parser.ParseResponseResult_Terraform:
		rps := make(map[provider.Provider]struct{})
		unsupported := getRequiredProviders(result.Terraform, rps)
		for unsupported := range unsupported {
			logging.Warnf("skipping unsupported provider: %s", unsupported)
		}
		requiredProviders = slices.Collect(maps.Keys(rps))
	case *parser.ParseResponseResult_Cloudformation:
		requiredProviders = []provider.Provider{provider.Provider_PROVIDER_AWS}
	default:
		return nil, fmt.Errorf("unsupported parse result type: %T", result)
	}

	isProduction := func() bool {
		for _, filter := range s.productionFilters {
			switch filter.Type {
			case event.ProductionFilter_BRANCH:
				if matchProductionFilter(filter.Value, s.branchName) {
					return filter.Include
				}
			case event.ProductionFilter_PROJECT:
				if matchProductionFilter(filter.Value, project.Name) {
					return filter.Include
				}
			case event.ProductionFilter_REPO:
				if matchProductionFilter(filter.Value, s.repositoryName) {
					return filter.Include
				}
			default:
				// do nothing
			}
		}
		return false
	}()

	input := &provider.Input{
		ParseResult:  response,
		AbsolutePath: absoluteProjectPath,
		ProjectInfo: &provider.ProjectInfo{
			Name:         project.Name,
			BranchName:   s.branchName,
			Workspace:    terraformWorkspace,
			IsProduction: isProduction,
		},
		PreviousResourceAddresses: nil, // TODO: This comes from a run on the default branch when we're diffing our current changeset to the default branch
		Usage:                     projectUsage,
		FinopsPolicyConfig: &provider.FinopsPolicyConfiguration{
			Policies: s.finopsPolicies,
		},
		Features: &provider.Features{
			EnablePriceLookups:         true, // TODO: make this configurable
			EnableRecommendations:      true, // TODO: make this configurable
			EnableFinopsPolicies:       true, // TODO: make this configurable
			EnableEnvironmentalMetrics: true, // TODO: make this configurable
		},
		Settings: &provider.Settings{
			Currency:      s.config.currency,
			UseDiskCaches: true,
		},
		Infracost: &provider.Infracost{
			ApiKey:             s.token.AccessToken,
			PricingApiEndpoint: s.config.pricingEndpoint,
			TraceId:            trace.ID,
			OrgId:              &s.orgID,
		},
	}

	for _, rp := range requiredProviders {
		if err := s.config.plugins.EnsureProvider(rp); err != nil {
			logging.WithError(err).Msgf("failed to ensure provider %s", rp)
			continue
		}

		switch rp {
		case provider.Provider_PROVIDER_AWS:
			rs, ps, err := s.config.plugins.Providers.ProcessInput(ctx, rp, input, s.config.plugins.Providers.LoadAWS, s.config.logging.ToHCLogLevel())
			if err != nil {
				return nil, fmt.Errorf("failed to execute AWS provider: %w", err)
			}
			projectResult.FinopsResults = append(projectResult.FinopsResults, ps...)
			projectResult.Resources = append(projectResult.Resources, rs...)

		case provider.Provider_PROVIDER_GOOGLE:
			rs, ps, err := s.config.plugins.Providers.ProcessInput(ctx, rp, input, s.config.plugins.Providers.LoadGoogle, s.config.logging.ToHCLogLevel())
			if err != nil {
				return nil, fmt.Errorf("failed to execute GCP provider: %w", err)
			}
			projectResult.FinopsResults = append(projectResult.FinopsResults, ps...)
			projectResult.Resources = append(projectResult.Resources, rs...)

		case provider.Provider_PROVIDER_AZURERM:
			rs, ps, err := s.config.plugins.Providers.ProcessInput(ctx, rp, input, s.config.plugins.Providers.LoadAzurerm, s.config.logging.ToHCLogLevel())
			if err != nil {
				return nil, fmt.Errorf("failed to execute Azure provider: %w", err)
			}
			projectResult.FinopsResults = append(projectResult.FinopsResults, ps...)
			projectResult.Resources = append(projectResult.Resources, rs...)

		}
	}

	// evaluate tag policies against all provider resources
	projectResult.TagPolicyResults = goprotoevent.TagPolicies(s.tagPolicies).EvaluateAgainstResources(projectResult.Resources, input.ProjectInfo)

	return &projectResult, nil
}

func getRequiredProviders(result *terraform.ModuleResult, providers map[provider.Provider]struct{}) map[string]struct{} {
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
		for _, result := range module.Results {
			us := getRequiredProviders(result, providers)
			for k := range us {
				unsupported[k] = struct{}{}
			}
		}
	}
	return unsupported
}

// matchProductionFilter evaluates the production filters.
func matchProductionFilter(matcher, value string) bool {
	escaped := strings.ReplaceAll(matcher, "*", preserver)
	escaped = regexp.QuoteMeta(escaped)

	if !strings.HasSuffix(escaped, preserver) {
		escaped += "\\b"
	}
	if !strings.HasPrefix(escaped, preserver) {
		escaped = "\\b" + escaped
	}
	escaped = strings.ReplaceAll(escaped, preserver, ".*")

	re, err := regexp.Compile(escaped)
	if err != nil {
		return false
	}
	return re.MatchString(value)
}

func loadUsageDefaults(defaults *event.UsageDefaults, projectName string) *usage.Usage {
	if defaults == nil {
		return nil
	}

	byResourceType := make(map[string]*usage.UsageItemMap, len(defaults.Resources))
	for resourceType, value := range defaults.Resources {
		resourceTypes := make(map[string]*usage.UsageValue, len(value.Usages))
		for attr, value := range value.Usages {
			list := make([]*event.UsageDefault, len(value.List))
			copy(list, value.List)
			sort.Slice(list, func(i, j int) bool {
				return list[i].Priority > list[j].Priority
			})
		List:
			for _, item := range list {
				if item.Quantity == "" {
					continue
				}

				if item.Filters != nil && item.Filters.Project != nil {
					for _, include := range item.Filters.Project.Include {
						if !matchWildcard(include, projectName) {
							continue List
						}
					}
					for _, exclude := range item.Filters.Project.Exclude {
						if matchWildcard(exclude, projectName) {
							continue List
						}
					}
				}

				if q, err := rat.NewFromString(item.Quantity); err == nil {
					resourceTypes[attr] = &usage.UsageValue{
						Value: &usage.UsageValue_NumberValue{
							NumberValue: q.Proto(),
						},
					}
					break
				}
			}
		}
		byResourceType[resourceType] = &usage.UsageItemMap{
			Items: resourceTypes,
		}
	}
	return &usage.Usage{
		ByResourceType: byResourceType,
	}
}

// countUsage iterates over the loaded usage data and splits parameters into
// estimated (non-zero value) and unestimated (zero/empty value) counts. If
// usageData is nil, both returned maps are nil (signaling no usage file).
func countUsage(usageData *usage.Usage) (estimated, unestimated map[string]int) {
	if usageData == nil {
		return nil, nil
	}
	estimated = make(map[string]int)
	unestimated = make(map[string]int)
	for resourceType, items := range usageData.GetByResourceType() {
		for attr, val := range items.GetItems() {
			key := resourceType + "." + attr
			if isEstimated(val) {
				estimated[key]++
			} else {
				unestimated[key]++
			}
		}
	}
	return estimated, unestimated
}

func isEstimated(v *usage.UsageValue) bool {
	if v == nil {
		return false
	}
	switch val := v.Value.(type) {
	case *usage.UsageValue_NumberValue:
		return val.NumberValue != nil && len(val.NumberValue.GetNumerator()) > 0
	case *usage.UsageValue_StringValue:
		return val.StringValue != ""
	default:
		return false
	}
}

// matchWildcard evaluates the usage filters.
func matchWildcard(value string, pattern string) bool {
	if pattern == "*" {
		return true
	}

	patternLen := len(pattern)
	valueLen := len(value)
	var patternIndex, valueIndex, starIndex, matchIndex int
	starIndex = -1
	matchIndex = -1

	for valueIndex < valueLen {
		switch {
		case patternIndex < patternLen && pattern[patternIndex] == '*':
			// Found a wildcard, mark position
			starIndex = patternIndex
			matchIndex = valueIndex
			patternIndex++
		case patternIndex < patternLen && (pattern[patternIndex] == value[valueIndex] || pattern[patternIndex] == '?'):
			// Characters match or single character wildcard
			patternIndex++
			valueIndex++
		case starIndex != -1:
			// No match, but we have a previous wildcard
			patternIndex = starIndex + 1
			matchIndex++
			valueIndex = matchIndex
		default:
			// No match and no wildcard
			return false
		}
	}

	// Handle any remaining pattern characters
	for patternIndex < patternLen && pattern[patternIndex] == '*' {
		patternIndex++
	}

	return patternIndex == patternLen
}
