package scanner

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/infracost/cli/pkg/logging"
	"github.com/infracost/cli/pkg/plugins"
	repoconfig "github.com/infracost/config"
	"github.com/infracost/go-proto/pkg/address"
	"github.com/infracost/go-proto/pkg/diagnostic"
	goprotoevent "github.com/infracost/go-proto/pkg/event"
	providerconv "github.com/infracost/go-proto/pkg/providers"
	"github.com/infracost/go-proto/pkg/rat"
	treeresource "github.com/infracost/go-proto/pkg/tree/resource"
	parserapi "github.com/infracost/proto/gen/go/infracost/parser/api"
	"github.com/infracost/proto/gen/go/infracost/parser/event"
	"github.com/infracost/proto/gen/go/infracost/parser/options"
	"github.com/infracost/proto/gen/go/infracost/parser/terraform"
	pluginpb "github.com/infracost/proto/gen/go/infracost/plugin"
	"github.com/infracost/proto/gen/go/infracost/provider"
	treepb "github.com/infracost/proto/gen/go/infracost/tree"
	"github.com/infracost/proto/gen/go/infracost/usage"
	"golang.org/x/oauth2"
)

// PluginOpts holds arbitrary plugin-specific parse options, keyed by plugin
// name then option key (nested when the option key is dotted). The schema of
// the inner map is owned by the plugin that matches the outer key.
type PluginOpts map[string]map[string]any

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

	// TokenSource is consulted immediately before each provider plugin call so
	// long multi-project scans don't reuse an expired access token.
	TokenSource oauth2.TokenSource
	// AccessToken is a fallback for callers that don't have a refresh-capable token source.
	AccessToken string // nolint:gosec // G117: passed to providers, and not exposed
	// PricingAPIKey is a static key for a self-hosted Cloud Pricing API. When
	// set it is sent to providers as the pricing API key instead of the OAuth
	// access token (which a self-hosted pricing API cannot validate), and the
	// TokenSource is never consulted.
	PricingAPIKey   string // nolint:gosec // G117: passed to providers, and not exposed
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

	Plugins       *plugins.Config
	Logging       logging.Config
	PluginOptions PluginOpts
	// FetchAuth carries transport-level auth for remote fetches (the developer's
	// on-disk SSH keys), passed to the plugin's in-process getter.
	FetchAuth *options.FetchAuth
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

	projectType := finalProjectType(opts.Project.Type, absoluteProjectPath)
	parserPlugin, err := opts.Plugins.ParserPluginForProject(ctx, string(projectType))
	if err != nil {
		return nil, fmt.Errorf("failed to load parser plugin for project type %q: %w", projectType, err)
	}

	overrides := make(map[string]any)
	if opts.PluginOptions != nil {
		for _, key := range []string{string(projectType), parserPlugin.Info.GetName(), strings.TrimPrefix(parserPlugin.Info.GetName(), "infracost/")} {
			if values, ok := opts.PluginOptions[key]; ok {
				maps.Copy(overrides, values)
			}
		}
	}

	genericOptions := buildGenericOptions(opts)
	// The plugin blob is always forwarded verbatim, keyed by the project type, so every plugin
	// (Terraform, CloudFormation, Kubernetes, ...) receives its persisted parse options - including
	// per-environment options like a Helm chart's selected values files. Overrides are also just
	// always applied.
	rawOptions, err := pluginBlobJSON(opts.Project, string(projectType), overrides)
	if err != nil {
		return nil, fmt.Errorf("failed to build parser plugin options: %w", err)
	}

	// Try the parser-results cache first. The fingerprint folds in RawOptions so e.g. a different
	// workspace or tfvars file invalidates correctly. Skipping the gRPC + HCL parse + module load
	// is the entire point of this cache; the typical hot path on a 10k-project repo where one
	// project changed is N-1 hits + 1 miss.
	pluginName := parserPlugin.Info.GetName()
	pluginVersion := parserPlugin.Info.GetVersion()

	fingerprint, fpErr := fingerprintProject(absoluteProjectPath, rawOptions)
	if fpErr != nil {
		logging.Debugf("parser fingerprint failed for %q: %s", absoluteProjectPath, fpErr)
	}

	var response *pluginpb.ParseResponse
	if fpErr == nil {
		response = loadParsedResponse(pluginName, pluginVersion, absoluteProjectPath, fingerprint)
	}

	if response == nil {
		response, err = parserPlugin.Parse(ctx, &pluginpb.ParseRequest{
			Path:           absoluteProjectPath,
			GenericOptions: genericOptions,
			RawOptions:     rawOptions,
			// raw_options_format was removed from the proto - raw_options is always JSON now.
		})
		if err != nil {
			return nil, fmt.Errorf("parser plugin error: %w (run with --debug or set INFRACOST_CLI_LOG_LEVEL=debug for more details)", err)
		}
		if fpErr == nil && response != nil {
			saveParsedResponse(pluginName, pluginVersion, absoluteProjectPath, fingerprint, response)
		}
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

	if response == nil || response.Tree == nil {
		return nil, fmt.Errorf("parser plugin returned no tree and no critical diagnostics")
	}

	projectResult.RemoteModuleCalls = response.Tree.GetRemoteModuleCalls()
	requiredProviders := GetRequiredProvidersFromTree(response.Tree)
	if len(requiredProviders) == 0 && projectType == repoconfig.ProjectTypeCloudFormation {
		requiredProviders = []provider.Provider{provider.Provider_PROVIDER_AWS}
	}

	// nil FinopsPolicies => leave the config nil so the provider evaluates all
	// policies; an empty (non-nil) slice keeps the config so it evaluates none.
	var finopsPolicyConfig *provider.FinopsPolicyConfiguration
	if opts.FinopsPolicies != nil {
		finopsPolicyConfig = &provider.FinopsPolicyConfiguration{
			Policies: opts.FinopsPolicies,
		}
	}

	input := &provider.TreeInput{
		Tree:         response.Tree,
		AbsolutePath: absoluteProjectPath,
		ProjectInfo: &provider.ProjectInfo{
			Name:         opts.Project.Name,
			BranchName:   opts.BranchName,
			Workspace:    opts.Project.Terraform.Workspace,
			IsProduction: isProduction,
		},
		PreviousResourceAddresses: opts.PreviousResourceAddresses,
		Usage:                     projectUsage,
		FinopsPolicyConfig:        finopsPolicyConfig,
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

	taggableResources := make([]*provider.Resource, 0, len(response.Tree.GetUnsupportedResources()))
	for _, res := range response.Tree.GetUnsupportedResources() {
		pr := treeresource.ProtoToProviderResource(res)
		if pr == nil {
			continue
		}
		if pr.IsFree {
			pr.IsSupported = true
		}
		if def := res.GetDefinition(); def != nil && !slices.Contains(input.PreviousResourceAddresses, address.FromProto(def.GetAddress()).String()) {
			pr.Action = provider.ResourceAction_CREATE
		}
		projectResult.Resources = append(projectResult.Resources, pr)
		taggableResources = append(taggableResources, pr)
	}

	_ = requiredProviders // No longer used to gate plugin invocation — every loaded provider plugin runs.

	providerPlugins, err := opts.Plugins.ProviderPlugins(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to load provider plugins: %w", err)
	}

	// A static self-hosted pricing API key always wins: it is the only
	// credential a self-hosted Cloud Pricing API accepts, so it must never be
	// overwritten by an OAuth access token. Otherwise refresh the access token
	// once, immediately before the first provider call — skipped entirely when
	// no provider plugins are loaded so we don't burn a refresh for a no-op
	// scan.
	if opts.PricingAPIKey != "" {
		input.Infracost.ApiKey = opts.PricingAPIKey
	} else if len(providerPlugins) > 0 && opts.TokenSource != nil {
		token, err := opts.TokenSource.Token()
		if err != nil {
			return nil, fmt.Errorf("failed to retrieve access token: %w", err)
		}
		input.Infracost.ApiKey = token.AccessToken
	}

	for _, p := range providerPlugins {
		resp, err := p.Process(ctx, &pluginpb.ProcessRequest{Input: input})
		if err != nil {
			return nil, fmt.Errorf("failed to execute provider %s: %w", p.Info.GetName(), err)
		}
		output := resp.GetOutput()
		if output == nil {
			continue
		}
		projectResult.Resources = append(projectResult.Resources, output.Resources...)
		taggableResources = append(taggableResources, output.Resources...)
		projectResult.FinopsResults = append(projectResult.FinopsResults, output.FinopsResults...)
	}

	// Evaluate tag policies against provider output plus parser-flagged unsupported resources.
	projectResult.TagPolicyResults = goprotoevent.TagPolicies(opts.TagPolicies).EvaluateAgainstResources(taggableResources, input.ProjectInfo)

	// Compute total monthly cost from resources.
	projectResult.TotalMonthlyCost = TotalMonthlyCostFromResources(projectResult.Resources)

	return projectResult, nil
}

func finalProjectType(projectType repoconfig.ProjectType, absoluteProjectPath string) repoconfig.ProjectType {
	if projectType == repoconfig.ProjectTypeUnknown {
		if stat, err := os.Stat(filepath.Join(absoluteProjectPath, "terragrunt.hcl")); err == nil && !stat.IsDir() {
			return repoconfig.ProjectTypeTerragrunt
		}
		if stat, err := os.Stat(filepath.Join(absoluteProjectPath, "terragrunt.hcl.json")); err == nil && !stat.IsDir() {
			return repoconfig.ProjectTypeTerragrunt
		}
		return repoconfig.ProjectTypeTerraform
	}
	if strings.HasPrefix(string(projectType), "cdk_") {
		return repoconfig.ProjectTypeCloudFormation
	}
	return projectType
}

func buildGenericOptions(opts *ScanProjectOptions) *options.GenericOptions {
	genericOptions := &options.GenericOptions{
		ProjectName:        opts.Project.Name,
		EnvironmentName:    opts.Project.EnvName,
		RepoDirectory:      opts.RootDir,
		TemporaryDirectory: os.TempDir(),
		CacheDirectory:     opts.CacheDir,
		WorkingDirectory:   opts.RootDir,
		// Caller-sourced runtime options always travel in GenericOptions, regardless of project
		// type - that is the point of GenericOptions: any plugin that cares reads them, the rest
		// ignore them, and they are never duplicated into the opaque plugin blob. (Workspace and the
		// terraform cloud config are also read outside the plugin.)
		Env:                opts.Project.Env,
		RequiredAttributes: protoAttributeRequirements(namingPolicyAttributeRequirements(opts.FinopsPolicies)),
		Workspace:          opts.Project.Terraform.Workspace,
		FetchAuth:          opts.FetchAuth,
	}

	if c := opts.Project.Terraform.Cloud; c.Org != "" || c.Workspace != "" || c.Host != "" {
		genericOptions.TerraformCloudConfiguration = &options.TerraformCloudConfiguration{
			Organization: c.Org,
			Workspace:    c.Workspace,
			Hostname:     c.Host,
		}
	}

	return genericOptions
}

// pluginBlobJSON marshals the project's persisted plugins.<key> blob (keyed by the consuming plugin)
// as JSON, returning nil when there is no blob for that key.
func pluginBlobJSON(project *repoconfig.Project, key string, overrides map[string]any) ([]byte, error) {
	blob := project.Plugins[key]
	if blob == nil {
		if len(overrides) == 0 {
			return nil, nil
		}
		return json.Marshal(overrides)
	}
	maps.Copy(blob, overrides)
	return json.Marshal(blob)
}

// protoAttributeRequirements converts the naming-policy attribute requirements into the typed
// GenericOptions form.
func protoAttributeRequirements(reqs []*parserapi.AttributeRequirement) []*options.AttributeRequirement {
	if len(reqs) == 0 {
		return nil
	}
	out := make([]*options.AttributeRequirement, 0, len(reqs))
	for _, r := range reqs {
		if r == nil {
			continue
		}
		out = append(out, &options.AttributeRequirement{ResourceType: r.ResourceType, Attributes: r.Attributes})
	}
	return out
}

type namingValidationSettings struct {
	AttributeValidationRules []string `json:"attributeValidationRules"`
}

func namingPolicyAttributeRequirements(policies []*event.FinopsPolicySettings) []*parserapi.AttributeRequirement {
	grouped := make(map[string]map[string]struct{})
	for _, policy := range policies {
		if len(policy.GetSettings()) <= 2 {
			continue
		}

		var settings namingValidationSettings
		if err := json.Unmarshal([]byte(policy.GetSettings()), &settings); err != nil {
			continue
		}

		for _, rule := range settings.AttributeValidationRules {
			resourceType, attribute, err := parseAttributeValidationRule(rule)
			if err != nil {
				continue
			}
			attrs, ok := grouped[resourceType]
			if !ok {
				attrs = make(map[string]struct{})
				grouped[resourceType] = attrs
			}
			attrs[attribute] = struct{}{}
		}
	}

	reqs := make([]*parserapi.AttributeRequirement, 0, len(grouped))
	for rt, attrs := range grouped {
		names := make([]string, 0, len(attrs))
		for name := range attrs {
			names = append(names, name)
		}
		sort.Strings(names)
		reqs = append(reqs, &parserapi.AttributeRequirement{ResourceType: rt, Attributes: names})
	}
	sort.Slice(reqs, func(i, j int) bool { return reqs[i].ResourceType < reqs[j].ResourceType })
	return reqs
}

func parseAttributeValidationRule(rule string) (resourceType, attribute string, err error) {
	keyRaw, valueRaw, found := strings.Cut(rule, ":")
	if !found {
		return "", "", fmt.Errorf("invalid rule format, expected 'resource_type.attribute: /regex/': %s", rule)
	}

	key := strings.TrimSpace(keyRaw)
	value := strings.TrimSpace(valueRaw)

	resourceType, attribute, found = strings.Cut(key, ".")
	if !found {
		return "", "", fmt.Errorf("invalid attribute key, expected 'resource_type.attribute': %s", key)
	}
	resourceType = strings.TrimSpace(resourceType)
	attribute = strings.TrimSpace(attribute)
	if resourceType == "" || attribute == "" {
		return "", "", fmt.Errorf("invalid attribute key, expected non-empty resource type and attribute: %s", key)
	}

	if len(value) < 2 || value[0] != '/' || value[len(value)-1] != '/' {
		return "", "", fmt.Errorf("invalid regex literal, expected /pattern/: %s", value)
	}
	if _, err := regexp.Compile(value[1 : len(value)-1]); err != nil {
		return "", "", err
	}

	return resourceType, attribute, nil
}

func GetRequiredProvidersFromTree(tree *treepb.Tree) []provider.Provider {
	if tree == nil {
		return nil
	}

	seen := make(map[provider.Provider]struct{})
	for raw := range tree.GetProviders() {
		if raw == "azure" {
			raw = "azurerm"
		}
		p := providerconv.ToProto(raw)
		if p == provider.Provider_PROVIDER_UNSPECIFIED {
			logging.Warnf("skipping unsupported provider: %s", raw)
			continue
		}
		seen[p] = struct{}{}
	}

	out := make([]provider.Provider, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	providerOrder := map[provider.Provider]int{
		provider.Provider_PROVIDER_AWS:     0,
		provider.Provider_PROVIDER_GOOGLE:  1,
		provider.Provider_PROVIDER_AZURERM: 2,
	}
	sort.Slice(out, func(i, j int) bool { return providerOrder[out[i]] < providerOrder[out[j]] })
	return out
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
