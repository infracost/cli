package scanner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/infracost/cli/internal/api/dashboard"
	"github.com/infracost/cli/internal/cache"
	"github.com/infracost/cli/internal/format"
	"github.com/infracost/cli/internal/trace"
	"github.com/infracost/cli/pkg/logging"
	"github.com/infracost/cli/pkg/plugins"
	pkgscanner "github.com/infracost/cli/pkg/scanner"
	repoconfig "github.com/infracost/config"
	goprotoevent "github.com/infracost/go-proto/pkg/event"
	"github.com/infracost/proto/gen/go/infracost/parser/event"
	pluginpb "github.com/infracost/proto/gen/go/infracost/plugin"
	"github.com/infracost/proto/gen/go/infracost/provider"
	"google.golang.org/protobuf/encoding/protojson"

	"golang.org/x/oauth2"
)

var pj = protojson.UnmarshalOptions{
	DiscardUnknown: true,
}

// Scanner is the per-invocation scanning context. Callers populate the
// fields directly so the scanner doesn't depend on the full config.Config
// — useful for callers that want to vary one piece (e.g. swapping the
// currency for an MCP tool call) without rebuilding a Config.
type Scanner struct {
	Plugins         *plugins.Config
	Logging         logging.Config
	Dashboard       dashboard.Config
	Currency        string
	PricingEndpoint string
}

type FinOpsPolicy struct {
	*provider.FinopsPolicy
	Settings *event.FinopsPolicySettings
	Provider string
}

type TaggingPolicy struct {
	*event.TagPolicy
}

// applyFeatureFlags reads the per-org feature flags returned in the run
// parameters and applies the ones the CLI acts on to the plugin config.
// Currently this gates the Kubernetes plugins on enableK8sPlugins. It must run
// before any plugin is loaded so the manager knows whether to download and load
// them (EnsurePlugins builds the Manager once and memoizes it).
func (s *Scanner) applyFeatureFlags(runParameters *dashboard.RunParameters) error {
	if runParameters == nil || len(runParameters.FeatureFlags) == 0 {
		return nil
	}
	flags := new(event.FeatureFlags)
	if err := pj.Unmarshal(runParameters.FeatureFlags, flags); err != nil {
		return fmt.Errorf("failed to unmarshal feature flags: %w", err)
	}
	s.Plugins.EnableK8sPlugins = flags.GetEnableK8SPlugins()
	return nil
}

func (s *Scanner) ListPolicies(ctx context.Context, runParameters *dashboard.RunParameters, providerFilter []string) ([]FinOpsPolicy, []TaggingPolicy, error) {
	if err := s.applyFeatureFlags(runParameters); err != nil {
		return nil, nil, err
	}

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

	// Bypass the dashboard policy filter so every locally-available policy is
	// listed — used to develop policies before they're registered upstream.
	if os.Getenv("INFRACOST_CLI_USE_ALL_LOCAL_POLICIES") != "" {
		hasRunParameters = false
	}

	providerPlugins, err := s.Plugins.ProviderPlugins(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to load provider plugins: %w", err)
	}

	wantProvider := make(map[string]struct{}, len(providerFilter))
	for _, name := range providerFilter {
		wantProvider[name] = struct{}{}
	}

	var finOpsPolicies []FinOpsPolicy
	for _, p := range providerPlugins {
		providerName := p.Info.GetName()
		if len(wantProvider) > 0 {
			if _, ok := wantProvider[providerName]; !ok {
				continue
			}
		}

		resp, err := p.ListFinopsPolicies(ctx, &pluginpb.ListFinopsPoliciesRequest{})
		if err != nil {
			logging.WithError(err).Msgf("failed to list FinOps policies for provider %s", providerName)
			continue
		}
		for _, policy := range resp.GetPolicies() {
			var settings *event.FinopsPolicySettings
			if hasRunParameters {
				var enabled bool
				for _, s := range finopsPolicySettings {
					if s.Slug == policy.GetSlug() {
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
				FinopsPolicy: &provider.FinopsPolicy{
					Slug:             policy.GetSlug(),
					Name:             policy.GetName(),
					Group:            policy.GetGroup(),
					Description:      policy.GetDescription(),
					OnlyNewResources: policy.GetOnlyNewResources(),
				},
				Settings: settings,
				Provider: providerName,
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

func (s *Scanner) Scan(ctx context.Context, runParameters dashboard.RunParameters, absolutePath, branchName string, tokenSource oauth2.TokenSource, pluginOpts map[string]map[string]any) (*format.Result, error) {
	var result format.Result

	// Apply run-parameter feature flags (e.g. the Kubernetes plugin gate) before
	// EnsurePlugins loads any plugins below.
	if err := s.applyFeatureFlags(&runParameters); err != nil {
		return nil, err
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
	if runParameters.ConfigTemplate != "" {
		repoConfigOpts = append(repoConfigOpts, repoconfig.WithTemplate(runParameters.ConfigTemplate))
	}

	repoConfigOpts = append(repoConfigOpts, repoconfig.WithPluginDir(s.Plugins.PluginDir()))

	// Ensure required plugins are installed before generating the repo
	// config — autodetection delegates to plugin identifiers, so a missing
	// binary means its file types (e.g. terraform plan JSON) won't be
	// recognized.
	if _, err := s.Plugins.EnsurePlugins(); err != nil {
		return nil, fmt.Errorf("failed to install plugins: %w", err)
	}

	stat, err := os.Stat(absolutePath)
	if err != nil {
		return nil, err
	}
	isFileMode := !stat.IsDir()
	absoluteDirectory := absolutePath
	if isFileMode {
		absoluteDirectory = filepath.Dir(absolutePath)
		// no point searching recursively if we know the file w're looking at
		repoConfigOpts = append(repoConfigOpts, repoconfig.WithMaxSearchDepth(1))
		repoConfigOpts = append(repoConfigOpts, repoconfig.WithSingleFileMode(true))

	}

	repoConfig, err := pkgscanner.LoadOrGenerateRepositoryConfig(ctx, absoluteDirectory, repoConfigOpts...)
	if err != nil {
		return nil, fmt.Errorf("repository configuration error: %w", err)
	}

	if isFileMode {
		// if we're scanning a single file, filter the repo config to only include the project that matches that file
		var filtered []*repoconfig.Project
		for _, candidate := range repoConfig.Projects {
			candidatePath := filepath.Join(absoluteDirectory, candidate.Path)
			if absolutePath == candidatePath {
				filtered = append(filtered, candidate)
			}
		}
		if len(filtered) == 0 {
			return nil, fmt.Errorf("file at %q is not a recognized scannable type", absolutePath)
		}
		repoConfig.Projects = filtered
	}

	result.Config = repoConfig
	if s.Currency != "" {
		result.Config.Currency = s.Currency
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

	var finopsPolicies []*event.FinopsPolicySettings
	if os.Getenv("INFRACOST_CLI_USE_ALL_LOCAL_POLICIES") == "" {
		finopsPolicies = make([]*event.FinopsPolicySettings, 0, len(runParameters.FinopsPolicies))
		for _, p := range runParameters.FinopsPolicies {
			policy := new(event.FinopsPolicySettings)
			if err := pj.Unmarshal(p, policy); err != nil {
				return nil, fmt.Errorf("failed to unmarshal FinOps policy: %w", err)
			}
			finopsPolicies = append(finopsPolicies, policy)
		}
	}

	cacheDir := cache.ParserDir()
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		return nil, fmt.Errorf("failed to create parser cache directory: %w", err)
	}

	logging.Debugf("autodetect discovered %d project(s) under %q", len(repoConfig.Projects), absoluteDirectory)
	for _, project := range repoConfig.Projects {
		logging.Debugf("scanning project name=%q path=%q type=%q env=%q deps=%v", project.Name, project.Path, project.Type, project.Env, project.DependencyPaths)
		projectResult, err := pkgscanner.ScanProject(ctx, &pkgscanner.ScanProjectOptions{
			RootDir:           absoluteDirectory,
			CacheDir:          cacheDir,
			RepoConfig:        repoConfig,
			Project:           project,
			TokenSource:       tokenSource,
			BranchName:        branchName,
			RepositoryName:    repositoryName,
			OrgID:             runParameters.OrganizationID,
			PricingEndpoint:   s.PricingEndpoint,
			Currency:          result.Config.Currency,
			TraceID:           trace.ID,
			ProductionFilters: productionFilters,
			FinopsPolicies:    finopsPolicies,
			TagPolicies:       tagPolicies,
			UsageDefaults:     usageDefaults,
			RepoUsage:         repoUsage,
			Plugins:           s.Plugins,
			Logging:           s.Logging,
			PluginOptions:     pluginOpts,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to scan project %q: %w", project.Name, err)
		}
		// A nil result means the project was intentionally skipped (e.g. a
		// Kubernetes project while the k8s plugins are feature-gated off).
		if projectResult == nil {
			continue
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
