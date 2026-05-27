package parser

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/hashicorp/go-hclog"
	"github.com/infracost/cli/internal/protocache"
	"github.com/infracost/cli/pkg/logging"
	repoconfig "github.com/infracost/config"
	"github.com/infracost/proto/gen/go/infracost/parser/api"
	armpb "github.com/infracost/proto/gen/go/infracost/parser/arm"
	"github.com/infracost/proto/gen/go/infracost/parser/cloudformation"
	"github.com/infracost/proto/gen/go/infracost/parser/options"
	"github.com/infracost/proto/gen/go/infracost/parser/terraform"
)

// PluginDir is the directory to scan for per-IaC parser plugins.
// Set by the CLI before parsing begins. If empty, only the mono-parser is used.
var PluginDir string

func (c *Config) Parse(ctx context.Context, path string, cfg *repoconfig.Config, project *repoconfig.Project, level hclog.Level, options *options.GenericOptions) (*api.ParseResponse, error) {

	var cache protocache.Cache[*api.ParseResponse]

	// Bind the cache key to the plugin binary's mtime — a deterministic
	// stand-in for a real plugin version. Without this, iterating on the
	// parser plugin (rebuild, re-scan) silently returns the old cached
	// response. A proper Version RPC on the plugin would be cleaner, but
	// mtime gives us correctness today and falls back to "" cleanly when
	// the path isn't statable.
	cacheKey := createCacheKey(path, pluginCacheVersion(c.Plugin), cfg, project)
	if response, err := cache.Load(cacheKey); err == nil {
		return response, nil
	} else if !errors.Is(err, protocache.ErrCacheMiss) {
		logging.Warnf("failed to load parse result from cache: %s", err)
	}
	response, err := c.parseWithoutCache(ctx, path, cfg, project, level, options)
	if err != nil {
		return nil, err
	}
	if err := cache.Save(cacheKey, response); err != nil {
		logging.Warnf("failed to save parse result to cache: %s", err)
	}
	return response, nil
}

func (c *Config) parseWithoutCache(ctx context.Context, path string, cfg *repoconfig.Config, project *repoconfig.Project, level hclog.Level, options *options.GenericOptions) (*api.ParseResponse, error) {
	// Step 1: Try per-IaC plugins if any are available.
	if resp, handled, err := c.tryPerIaCPlugins(ctx, path, cfg, project, level, options); handled {
		return resp, err
	}

	// Step 2: Fallback to existing mono-parser routing.
	return c.legacyRoute(ctx, path, cfg, project, level, options)
}

// tryPerIaCPlugins attempts to detect and parse using per-IaC plugins.
// Returns (response, true, err) if a plugin handled the path, or (nil, false, nil) to fall back.
func (c *Config) tryPerIaCPlugins(ctx context.Context, path string, cfg *repoconfig.Config, project *repoconfig.Project, level hclog.Level, opts *options.GenericOptions) (*api.ParseResponse, bool, error) {
	if PluginDir == "" {
		return nil, false, nil
	}

	mgr := NewPluginManager(PluginDir, level)
	defer mgr.Close()

	if !mgr.HasPlugins() {
		return nil, false, nil
	}

	plugin, projectType, err := mgr.Detect(ctx, path)
	if err != nil {
		logging.Debugf("per-IaC plugin detection error: %v, falling back to mono-parser", err)
		return nil, false, nil
	}
	if plugin == nil {
		return nil, false, nil
	}

	logging.Debugf("per-IaC plugin %q claimed path %s as %s", plugin.Name, path, projectType)

	client := plugin.Client()
	if _, err := client.Initialize(ctx, new(api.InitializeRequest)); err != nil {
		return nil, false, fmt.Errorf("failed to initialize per-IaC plugin %s: %w", plugin.Name, err)
	}

	switch projectType {
	case "terraform":
		resp, err := c.parseTerraformWith(ctx, client, path, cfg, project, opts)
		return resp, true, err
	case "terragrunt":
		resp, err := c.parseTerraformWith(ctx, client, path, cfg, project, opts)
		return resp, true, err
	case "cloudformation":
		resp, err := c.parseCloudFormationWith(ctx, client, path, project, opts)
		return resp, true, err
	case "arm":
		resp, err := c.parseARMWith(ctx, client, path, project, opts)
		return resp, true, err
	default:
		logging.Debugf("per-IaC plugin %q returned unknown project type %q, falling back", plugin.Name, projectType)
		return nil, false, nil
	}
}

// legacyRoute is the existing hardcoded routing logic (mono-parser fallback).
func (c *Config) legacyRoute(ctx context.Context, path string, cfg *repoconfig.Config, project *repoconfig.Project, level hclog.Level, options *options.GenericOptions) (*api.ParseResponse, error) {
	// When the project config declares a type, honour it. Autodetect
	// populates Type for every project it discovers, so this is the
	// fast path. Extension-based dispatch below is a fallback for
	// callers that pass a bare path without going through autodetect
	// (eg. the LSP single-file flow).
	switch project.Type {
	case repoconfig.ProjectTypeTerraform, repoconfig.ProjectTypeTerragrunt:
		return c.parseTerraform(ctx, terraformDir(path), cfg, project, level, options)
	case repoconfig.ProjectTypeCloudFormation:
		return c.parseCloudFormation(ctx, path, project, level, options)
	case repoconfig.ProjectTypeARM:
		return c.parseARM(ctx, path, project, level, options)
	}

	// If the path points to a directory, assume Terraform.
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		return c.parseTerraform(ctx, path, cfg, project, level, options)
	}

	name := filepath.Base(path)
	ext := strings.ToLower(filepath.Ext(name))

	if ext == ".tf" || strings.HasSuffix(strings.ToLower(name), ".tf.json") {
		return c.parseTerraform(ctx, filepath.Dir(path), cfg, project, level, options)
	}

	switch ext {
	case ".json", ".yaml", ".yml", ".template":
		return c.parseCloudFormation(ctx, path, project, level, options)
	}

	return nil, fmt.Errorf("unsupported file type: %s, only Terraform, CloudFormation, and ARM are supported", ext)
}

// terraformDir resolves the directory to hand to the terraform parser.
// Terraform projects are directory-shaped (the parser loads every .tf
// file alongside), so if path is a file we walk up one level.
func terraformDir(path string) string {
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		return path
	}
	return filepath.Dir(path)
}

// parseTerraformWith uses a specific client (per-IaC plugin) instead of loading the mono-parser.
func (c *Config) parseTerraformWith(ctx context.Context, client api.ParserServiceClient, path string, cfg *repoconfig.Config, project *repoconfig.Project, opts *options.GenericOptions) (*api.ParseResponse, error) {
	dir := path
	if info, err := os.Stat(path); err == nil && !info.IsDir() {
		dir = filepath.Dir(path)
	}

	var regexSourceMap map[string]string
	if len(cfg.Terraform.SourceMap) > 0 {
		regexSourceMap = make(map[string]string, len(cfg.Terraform.SourceMap))
		for _, source := range cfg.Terraform.SourceMap {
			regexSourceMap[source.Match] = source.Replace
		}
	}

	var cloudConfig *terraform.TerraformCloudConfiguration
	if project.Terraform.Cloud.Org != "" {
		cloudConfig = &terraform.TerraformCloudConfiguration{
			Organization: project.Terraform.Cloud.Org,
			Hostname:     project.Terraform.Cloud.Host,
			Workspace:    project.Terraform.Cloud.Workspace,
		}
	}

	return client.Parse(ctx, &api.ParseRequest{
		RepoDirectory:    opts.RepoDirectory,
		WorkingDirectory: opts.WorkingDirectory,
		Target: &api.ParseRequestTarget{
			Value: &api.ParseRequestTarget_Terraform{
				Terraform: &terraform.Target{
					Directory: dir,
					Options: &terraform.Options{
						Generic:                     opts,
						RegexSourceMap:              regexSourceMap,
						Env:                         project.Env,
						Workspace:                   project.Terraform.Workspace,
						TfVarsFiles:                 project.Terraform.VarFiles,
						TerraformCloudConfiguration: cloudConfig,
					},
				},
			},
		},
	})
}

// parseCloudFormationWith uses a specific client (per-IaC plugin) instead of loading the mono-parser.
func (c *Config) parseCloudFormationWith(ctx context.Context, client api.ParserServiceClient, path string, project *repoconfig.Project, opts *options.GenericOptions) (*api.ParseResponse, error) {
	var awsContext *cloudformation.AwsContext
	if project.AWS.AccountID != "" || project.AWS.Region != "" || project.AWS.StackID != "" || project.AWS.StackName != "" {
		awsContext = &cloudformation.AwsContext{
			AccountId: project.AWS.AccountID,
			Region:    project.AWS.Region,
			StackId:   project.AWS.StackID,
			StackName: project.AWS.StackName,
		}
	}

	return client.Parse(ctx, &api.ParseRequest{
		RepoDirectory:    opts.RepoDirectory,
		WorkingDirectory: opts.WorkingDirectory,
		Target: &api.ParseRequestTarget{
			Value: &api.ParseRequestTarget_Cloudformation{
				Cloudformation: &cloudformation.Target{
					TemplatePath: path,
					Options: &cloudformation.Options{
						Generic:    opts,
						AwsContext: awsContext,
					},
				},
			},
		},
	})
}

// parseARMWith uses a specific client (per-IaC plugin) instead of loading the mono-parser.
func (c *Config) parseARMWith(ctx context.Context, client api.ParserServiceClient, path string, project *repoconfig.Project, opts *options.GenericOptions) (*api.ParseResponse, error) {
	var azureContext *armpb.AzureContext
	if project.Azure.SubscriptionID != "" ||
		project.Azure.TenantID != "" ||
		project.Azure.ResourceGroupName != "" ||
		project.Azure.Location != "" ||
		project.Azure.ManagementGroupID != "" {
		azureContext = &armpb.AzureContext{
			SubscriptionId:    project.Azure.SubscriptionID,
			TenantId:          project.Azure.TenantID,
			ResourceGroupName: project.Azure.ResourceGroupName,
			Location:          project.Azure.Location,
			ManagementGroupId: project.Azure.ManagementGroupID,
		}
	}

	return client.Parse(ctx, &api.ParseRequest{
		RepoDirectory:    opts.RepoDirectory,
		WorkingDirectory: opts.WorkingDirectory,
		Target: &api.ParseRequestTarget{
			Value: &api.ParseRequestTarget_Arm{
				Arm: &armpb.Target{
					TemplatePath: path,
					Options: &armpb.Options{
						Generic:      opts,
						AzureContext: azureContext,
					},
				},
			},
		},
	})
}

func (c *Config) parseTerraform(ctx context.Context, path string, cfg *repoconfig.Config, project *repoconfig.Project, level hclog.Level, options *options.GenericOptions) (*api.ParseResponse, error) {
	client, stop, err := c.Load(level)
	if stop != nil {
		defer stop()
	}
	if err != nil {
		return nil, fmt.Errorf("failed to load parser plugin: %w", err)
	}

	if _, err := client.Initialize(ctx, new(api.InitializeRequest)); err != nil {
		return nil, fmt.Errorf("failed to initialize parser: %w", err)
	}

	var regexSourceMap map[string]string
	if len(cfg.Terraform.SourceMap) > 0 {
		regexSourceMap = make(map[string]string, len(cfg.Terraform.SourceMap))
		for _, source := range cfg.Terraform.SourceMap {
			regexSourceMap[source.Match] = source.Replace
		}
	}

	var cloudConfig *terraform.TerraformCloudConfiguration
	if project.Terraform.Cloud.Org != "" {
		cloudConfig = &terraform.TerraformCloudConfiguration{
			Organization: project.Terraform.Cloud.Org,
			Hostname:     project.Terraform.Cloud.Host,
			Workspace:    project.Terraform.Cloud.Workspace,
		}
	}

	response, err := client.Parse(ctx, &api.ParseRequest{
		RepoDirectory:    options.RepoDirectory,
		WorkingDirectory: options.WorkingDirectory,
		Target: &api.ParseRequestTarget{
			Value: &api.ParseRequestTarget_Terraform{
				Terraform: &terraform.Target{
					Directory:    path,
					LoadedModule: nil,
					Options: &terraform.Options{
						Generic:                     options,
						SourceMap:                   nil,
						RegexSourceMap:              regexSourceMap,
						Env:                         project.Env,
						Vars:                        nil,
						DefaultTags:                 nil,
						RemoteVars:                  nil,
						Workspace:                   project.Terraform.Workspace,
						TfVarsFiles:                 project.Terraform.VarFiles,
						ForceLocalModulePaths:       false,
						TerraformCloudConfiguration: cloudConfig,
					},
				},
			},
		},
	})
	if err != nil {
		return response, fmt.Errorf("failed to parse terraform: %w", err)
	}
	return response, nil
}

func (c *Config) parseARM(ctx context.Context, path string, project *repoconfig.Project, level hclog.Level, options *options.GenericOptions) (*api.ParseResponse, error) {
	client, stop, err := c.Load(level)
	if stop != nil {
		defer stop()
	}
	if err != nil {
		return nil, fmt.Errorf("failed to load parser plugin: %w", err)
	}

	initReq := &api.InitializeRequest{
		ArmSupportedResources: c.SupportedARMResources,
	}
	if _, err := client.Initialize(ctx, initReq); err != nil {
		return nil, fmt.Errorf("failed to initialize parser: %w", err)
	}

	var azureContext *armpb.AzureContext
	if project.Azure.SubscriptionID != "" ||
		project.Azure.TenantID != "" ||
		project.Azure.ResourceGroupName != "" ||
		project.Azure.Location != "" ||
		project.Azure.ManagementGroupID != "" {
		azureContext = &armpb.AzureContext{
			SubscriptionId:    project.Azure.SubscriptionID,
			TenantId:          project.Azure.TenantID,
			ResourceGroupName: project.Azure.ResourceGroupName,
			Location:          project.Azure.Location,
			ManagementGroupId: project.Azure.ManagementGroupID,
		}
	}

	response, err := client.Parse(ctx, &api.ParseRequest{
		RepoDirectory:    options.RepoDirectory,
		WorkingDirectory: options.WorkingDirectory,
		Target: &api.ParseRequestTarget{
			Value: &api.ParseRequestTarget_Arm{
				Arm: &armpb.Target{
					TemplatePath: path,
					Flags:        0,
					Options: &armpb.Options{
						Generic:         options,
						InputParameters: nil,
						AzureContext:    azureContext,
					},
				},
			},
		},
	})
	if err != nil {
		return response, fmt.Errorf("failed to parse arm: %w", err)
	}
	return response, nil
}

func (c *Config) parseCloudFormation(ctx context.Context, path string, project *repoconfig.Project, level hclog.Level, options *options.GenericOptions) (*api.ParseResponse, error) {
	client, stop, err := c.Load(level)
	if stop != nil {
		defer stop()
	}
	if err != nil {
		return nil, fmt.Errorf("failed to load parser plugin: %w", err)
	}

	if _, err := client.Initialize(ctx, new(api.InitializeRequest)); err != nil {
		return nil, fmt.Errorf("failed to initialize parser: %w", err)
	}

	var awsContext *cloudformation.AwsContext
	if project.AWS.AccountID != "" || project.AWS.Region != "" || project.AWS.StackID != "" || project.AWS.StackName != "" {
		awsContext = &cloudformation.AwsContext{
			AccountId: project.AWS.AccountID,
			Region:    project.AWS.Region,
			StackId:   project.AWS.StackID,
			StackName: project.AWS.StackName,
		}
	}

	response, err := client.Parse(ctx, &api.ParseRequest{
		RepoDirectory:    options.RepoDirectory,
		WorkingDirectory: options.WorkingDirectory,
		Target: &api.ParseRequestTarget{
			Value: &api.ParseRequestTarget_Cloudformation{
				Cloudformation: &cloudformation.Target{
					TemplatePath: path,
					Flags:        0,
					Options: &cloudformation.Options{
						Generic:         options,
						InputParameters: nil,
						AwsContext:      awsContext,
					},
				},
			},
		},
	})
	if err != nil {
		return response, fmt.Errorf("failed to parse cloudformation: %w", err)
	}
	return response, nil
}
