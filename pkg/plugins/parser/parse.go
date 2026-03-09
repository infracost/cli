package parser

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/hashicorp/go-hclog"
	"github.com/infracost/cli/internal/logging"
	"github.com/infracost/cli/internal/protocache"
	repoconfig "github.com/infracost/config"
	"github.com/infracost/proto/gen/go/infracost/parser/api"
	"github.com/infracost/proto/gen/go/infracost/parser/cloudformation"
	"github.com/infracost/proto/gen/go/infracost/parser/options"
	"github.com/infracost/proto/gen/go/infracost/parser/terraform"
)

func (c *Config) Parse(ctx context.Context, path string, cfg *repoconfig.Config, project *repoconfig.Project, level hclog.Level, options *options.GenericOptions) (*api.ParseResponse, error) {

	var cache protocache.Cache[*api.ParseResponse]

	// TODO: we probably want to include the parser plugin version in the cache key, but we need to decide how to get that - we could add a new method to the plugin interface that returns the version
	parserPluginVersion := ""
	cacheKey := createCacheKey(path, parserPluginVersion, cfg, project)
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
		// log the error but don't fail the whole parse if we can't save to cache
		logging.Warnf("failed to save parse result to cache: %s", err)
	}
	return response, nil
}

func (c *Config) parseWithoutCache(ctx context.Context, path string, cfg *repoconfig.Config, project *repoconfig.Project, level hclog.Level, options *options.GenericOptions) (*api.ParseResponse, error) {
	// If the path points to a directory, assume Terraform.
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		return c.parseTerraform(ctx, path, cfg, project, level, options)
	}

	// If it's a file (or Stat failed), decide by extension.
	name := filepath.Base(path)
	ext := strings.ToLower(filepath.Ext(name))

	// Terraform: .tf files or .tf.json
	if ext == ".tf" || strings.HasSuffix(strings.ToLower(name), ".tf.json") {
		return c.parseTerraform(ctx, filepath.Dir(path), cfg, project, level, options)

	}

	// CloudFormation common template extensions: .json, .yaml, .yml, .template
	switch ext {
	case ".json", ".yaml", ".yml", ".template":
		return c.parseCloudFormation(ctx, path, project, level, options)
	}

	return nil, fmt.Errorf("unsupported file type: %s, only Terraform and CloudFormation are supported", ext)
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
					LoadedModule: nil, // not needed for root module
					Options: &terraform.Options{
						Generic:                     options,
						SourceMap:                   nil, // only currently supported in ICP
						RegexSourceMap:              regexSourceMap,
						Env:                         project.Env,
						Vars:                        nil, // TODO: we need to convert these from the project config format to the parser plugin format
						DefaultTags:                 nil, // we don't currently load these from yor/hcp etc. but may want to in the future
						RemoteVars:                  nil, // we might want to load these from hcp/spacelift etc. in future
						Workspace:                   project.Terraform.Workspace,
						TfVarsFiles:                 project.Terraform.VarFiles,
						ForceLocalModulePaths:       false, // only needed for debugging - we probably prefer showing remove module paths rather than local cache ones...
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
					Flags:        0, // empty by default
					Options: &cloudformation.Options{
						Generic:         options,
						InputParameters: nil, // TODO: load these from somewhere
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
