package parser

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/hashicorp/go-hclog"
	parsermocks "github.com/infracost/cli/pkg/plugins/parser/mocks"
	repoconfig "github.com/infracost/config"
	"github.com/infracost/proto/gen/go/infracost/parser/api"
	"github.com/infracost/proto/gen/go/infracost/parser/options"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

// --- Mock-based unit tests ---

// runParse calls parseWithoutCache against a mocked client and returns
// the captured *api.ParseRequest. parseWithoutCache bypasses the disk
// cache that Parse layers on top, keeping the test hermetic across
// machines.
func runParse(t *testing.T, project *repoconfig.Project, path string) *api.ParseRequest {
	t.Helper()

	client := parsermocks.NewMockParserServiceClient(t)
	client.EXPECT().Initialize(mock.Anything, mock.Anything).Return(&api.InitializeResponse{}, nil)

	var captured *api.ParseRequest
	client.EXPECT().Parse(mock.Anything, mock.Anything).
		Run(func(_ context.Context, in *api.ParseRequest, _ ...grpc.CallOption) {
			captured = in
		}).
		Return(&api.ParseResponse{}, nil)

	cfg := &Config{
		Load: func(hclog.Level) (api.ParserServiceClient, func(), error) {
			return client, func() {}, nil
		},
	}

	// Disable per-IaC plugins so we exercise the legacyRoute path.
	savedPluginDir := PluginDir
	PluginDir = ""
	defer func() { PluginDir = savedPluginDir }()

	_, err := cfg.parseWithoutCache(
		context.Background(),
		path,
		&repoconfig.Config{},
		project,
		hclog.Off,
		&options.GenericOptions{},
	)
	require.NoError(t, err)
	require.NotNil(t, captured, "expected Parse() to have been invoked")
	return captured
}

func TestParseWithoutCache_RoutesByProjectType(t *testing.T) {
	t.Run("terraform type routes to terraform target", func(t *testing.T) {
		req := runParse(t,
			&repoconfig.Project{Type: repoconfig.ProjectTypeTerraform, Path: "."},
			t.TempDir(),
		)
		_, ok := req.Target.Value.(*api.ParseRequestTarget_Terraform)
		require.True(t, ok, "expected Terraform target, got %T", req.Target.Value)
	})

	t.Run("terragrunt type routes to terraform target", func(t *testing.T) {
		req := runParse(t,
			&repoconfig.Project{Type: repoconfig.ProjectTypeTerragrunt, Path: "."},
			t.TempDir(),
		)
		_, ok := req.Target.Value.(*api.ParseRequestTarget_Terraform)
		require.True(t, ok, "expected Terraform target, got %T", req.Target.Value)
	})

	t.Run("cloudformation type routes to cloudformation target", func(t *testing.T) {
		req := runParse(t,
			&repoconfig.Project{Type: repoconfig.ProjectTypeCloudFormation, Path: "template.json"},
			"template.json",
		)
		got, ok := req.Target.Value.(*api.ParseRequestTarget_Cloudformation)
		require.True(t, ok, "expected CloudFormation target, got %T", req.Target.Value)
		require.Equal(t, "template.json", got.Cloudformation.TemplatePath)
	})

	t.Run("arm type routes to arm target", func(t *testing.T) {
		req := runParse(t,
			&repoconfig.Project{Type: repoconfig.ProjectTypeARM, Path: "template.json"},
			"template.json",
		)
		got, ok := req.Target.Value.(*api.ParseRequestTarget_Arm)
		require.True(t, ok, "expected ARM target, got %T", req.Target.Value)
		require.Equal(t, "template.json", got.Arm.TemplatePath)
	})
}

func TestParseARM_AzureContextPopulation(t *testing.T) {
	t.Run("azure context flows through when fields are set", func(t *testing.T) {
		req := runParse(t,
			&repoconfig.Project{
				Type: repoconfig.ProjectTypeARM,
				Path: "template.json",
				Azure: repoconfig.ProjectAzureConfig{
					SubscriptionID:    "sub-123",
					TenantID:          "tenant-456",
					ResourceGroupName: "rg-test",
					Location:          "eastus",
					ManagementGroupID: "mg-789",
				},
			},
			"template.json",
		)
		got, ok := req.Target.Value.(*api.ParseRequestTarget_Arm)
		require.True(t, ok)
		require.NotNil(t, got.Arm.Options.AzureContext)
		require.Equal(t, "sub-123", got.Arm.Options.AzureContext.SubscriptionId)
		require.Equal(t, "tenant-456", got.Arm.Options.AzureContext.TenantId)
		require.Equal(t, "rg-test", got.Arm.Options.AzureContext.ResourceGroupName)
		require.Equal(t, "eastus", got.Arm.Options.AzureContext.Location)
		require.Equal(t, "mg-789", got.Arm.Options.AzureContext.ManagementGroupId)
	})

	t.Run("azure context is nil when no fields are set", func(t *testing.T) {
		req := runParse(t,
			&repoconfig.Project{Type: repoconfig.ProjectTypeARM, Path: "template.json"},
			"template.json",
		)
		got, ok := req.Target.Value.(*api.ParseRequestTarget_Arm)
		require.True(t, ok)
		require.Nil(t, got.Arm.Options.AzureContext)
	})
}

func TestParseWithoutCache_ExtensionFallback(t *testing.T) {
	t.Run("untyped .tf file routes to terraform", func(t *testing.T) {
		req := runParse(t,
			&repoconfig.Project{Path: "main.tf"},
			"main.tf",
		)
		_, ok := req.Target.Value.(*api.ParseRequestTarget_Terraform)
		require.True(t, ok, "expected Terraform target, got %T", req.Target.Value)
	})

	t.Run("untyped .json file routes to cloudformation", func(t *testing.T) {
		req := runParse(t,
			&repoconfig.Project{Path: "template.json"},
			"template.json",
		)
		_, ok := req.Target.Value.(*api.ParseRequestTarget_Cloudformation)
		require.True(t, ok, "expected CloudFormation target, got %T", req.Target.Value)
	})

	t.Run("untyped directory routes to terraform", func(t *testing.T) {
		req := runParse(t,
			&repoconfig.Project{Path: "."},
			t.TempDir(),
		)
		_, ok := req.Target.Value.(*api.ParseRequestTarget_Terraform)
		require.True(t, ok, "expected Terraform target, got %T", req.Target.Value)
	})
}

// --- End-to-end tests using real compiled plugin binaries ---

func setupE2EConfig(t *testing.T) (*Config, string) {
	t.Helper()

	binDir := parserBinDir()
	monoPlugin := filepath.Join(binDir, "infracost-parser-plugin")
	if _, err := os.Stat(monoPlugin); err != nil {
		t.Skipf("mono-parser plugin not found at %s (run: cd parser && make build-plugins)", monoPlugin)
	}

	absDir, _ := filepath.Abs(binDir)
	monoPlugin = filepath.Join(absDir, "infracost-parser-plugin")

	cfg := &Config{
		Plugin: monoPlugin,
	}
	cfg.Process()

	PluginDir = absDir

	return cfg, absDir
}

func defaultRepoConfig() *repoconfig.Config {
	return &repoconfig.Config{}
}

func defaultProject() *repoconfig.Project {
	return &repoconfig.Project{}
}

func defaultOptions() *options.GenericOptions {
	return &options.GenericOptions{
		RepoDirectory:    os.TempDir(),
		WorkingDirectory: os.TempDir(),
	}
}

func TestE2E_ParseTerraformDir_ViaPerIaCPlugin(t *testing.T) {
	cfg, _ := setupE2EConfig(t)

	tfDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tfDir, "main.tf"), []byte(`
resource "aws_instance" "example" {
  ami           = "ami-12345678"
  instance_type = "t2.micro"
}
`), 0644); err != nil {
		t.Fatal(err)
	}

	opts := defaultOptions()
	opts.RepoDirectory = tfDir
	opts.WorkingDirectory = tfDir

	resp, err := cfg.parseWithoutCache(context.Background(), tfDir, defaultRepoConfig(), defaultProject(), hclog.Off, opts)
	if err != nil {
		t.Fatal(err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if resp.Result == nil {
		t.Fatal("expected non-nil result")
	}
}

func TestE2E_ParseCloudFormationJSON_ViaPerIaCPlugin(t *testing.T) {
	cfg, _ := setupE2EConfig(t)

	cfnDir := t.TempDir()
	cfnJSON := `{
		"AWSTemplateFormatVersion": "2010-09-09",
		"Resources": {
			"MyBucket": {
				"Type": "AWS::S3::Bucket",
				"Properties": {
					"BucketName": "my-test-bucket"
				}
			}
		}
	}`
	f := filepath.Join(cfnDir, "template.json")
	if err := os.WriteFile(f, []byte(cfnJSON), 0644); err != nil {
		t.Fatal(err)
	}

	opts := defaultOptions()
	opts.RepoDirectory = cfnDir
	opts.WorkingDirectory = cfnDir

	resp, err := cfg.parseWithoutCache(context.Background(), f, defaultRepoConfig(), defaultProject(), hclog.Off, opts)
	if err != nil {
		t.Fatal(err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if resp.Result == nil {
		t.Fatal("expected non-nil result")
	}
}

func TestE2E_ParseCloudFormationYAML_ViaPerIaCPlugin(t *testing.T) {
	cfg, _ := setupE2EConfig(t)

	cfnDir := t.TempDir()
	cfnYAML := `AWSTemplateFormatVersion: "2010-09-09"
Resources:
  MyBucket:
    Type: AWS::S3::Bucket
    Properties:
      BucketName: my-test-bucket
`
	f := filepath.Join(cfnDir, "stack.yaml")
	if err := os.WriteFile(f, []byte(cfnYAML), 0644); err != nil {
		t.Fatal(err)
	}

	opts := defaultOptions()
	opts.RepoDirectory = cfnDir
	opts.WorkingDirectory = cfnDir

	resp, err := cfg.parseWithoutCache(context.Background(), f, defaultRepoConfig(), defaultProject(), hclog.Off, opts)
	if err != nil {
		t.Fatal(err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if resp.Result == nil {
		t.Fatal("expected non-nil result")
	}
}

func TestE2E_FallbackToMonoParser_WhenNoPerIaCPlugins(t *testing.T) {
	binDir := parserBinDir()
	monoPlugin := filepath.Join(binDir, "infracost-parser-plugin")
	if _, err := os.Stat(monoPlugin); err != nil {
		t.Skipf("mono-parser plugin not found at %s", monoPlugin)
	}

	absPlugin, _ := filepath.Abs(monoPlugin)
	cfg := &Config{
		Plugin: absPlugin,
	}
	cfg.Process()

	PluginDir = t.TempDir()

	tfDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tfDir, "main.tf"), []byte(`
resource "aws_s3_bucket" "b" {
  bucket = "my-bucket"
}
`), 0644); err != nil {
		t.Fatal(err)
	}

	opts := defaultOptions()
	opts.RepoDirectory = tfDir
	opts.WorkingDirectory = tfDir

	resp, err := cfg.parseWithoutCache(context.Background(), tfDir, defaultRepoConfig(), defaultProject(), hclog.Off, opts)
	if err != nil {
		t.Fatal(err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response from mono-parser fallback")
	}
	if resp.Result == nil {
		t.Fatal("expected non-nil result from mono-parser fallback")
	}
}

func TestE2E_FallbackToMonoParser_EmptyPluginDir(t *testing.T) {
	binDir := parserBinDir()
	monoPlugin := filepath.Join(binDir, "infracost-parser-plugin")
	if _, err := os.Stat(monoPlugin); err != nil {
		t.Skipf("mono-parser plugin not found at %s", monoPlugin)
	}

	absPlugin, _ := filepath.Abs(monoPlugin)
	cfg := &Config{
		Plugin: absPlugin,
	}
	cfg.Process()

	PluginDir = ""

	tfDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tfDir, "main.tf"), []byte(`
resource "aws_instance" "x" {
  ami           = "ami-abc"
  instance_type = "t3.small"
}
`), 0644); err != nil {
		t.Fatal(err)
	}

	opts := defaultOptions()
	opts.RepoDirectory = tfDir
	opts.WorkingDirectory = tfDir

	resp, err := cfg.parseWithoutCache(context.Background(), tfDir, defaultRepoConfig(), defaultProject(), hclog.Off, opts)
	if err != nil {
		t.Fatal(err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response from mono-parser fallback")
	}
}

// --- Per-IaC-only tests: prove per-IaC plugins handle the path, not the fallback ---

func TestPerIaCOnly_ParseTerraformDir(t *testing.T) {
	cfg, _ := setupE2EConfig(t)

	tfDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tfDir, "main.tf"), []byte(`
resource "aws_instance" "a" {
  ami           = "ami-12345678"
  instance_type = "t2.micro"
}
`), 0644); err != nil {
		t.Fatal(err)
	}

	opts := defaultOptions()
	opts.RepoDirectory = tfDir
	opts.WorkingDirectory = tfDir

	resp, handled, err := cfg.tryPerIaCPlugins(context.Background(), tfDir, defaultRepoConfig(), defaultProject(), hclog.Off, opts)
	if err != nil {
		t.Fatal(err)
	}
	if !handled {
		t.Fatal("expected per-IaC plugin to handle terraform directory, but it fell through")
	}
	if resp == nil {
		t.Fatal("expected non-nil response from per-IaC plugin")
	}
}

func TestPerIaCOnly_ParseCloudFormationJSON(t *testing.T) {
	cfg, _ := setupE2EConfig(t)

	cfnDir := t.TempDir()
	cfnJSON := `{
		"AWSTemplateFormatVersion": "2010-09-09",
		"Resources": {
			"MyBucket": {
				"Type": "AWS::S3::Bucket",
				"Properties": {
					"BucketName": "my-test-bucket"
				}
			}
		}
	}`
	f := filepath.Join(cfnDir, "template.json")
	if err := os.WriteFile(f, []byte(cfnJSON), 0644); err != nil {
		t.Fatal(err)
	}

	opts := defaultOptions()
	opts.RepoDirectory = cfnDir
	opts.WorkingDirectory = cfnDir

	resp, handled, err := cfg.tryPerIaCPlugins(context.Background(), f, defaultRepoConfig(), defaultProject(), hclog.Off, opts)
	if err != nil {
		t.Fatal(err)
	}
	if !handled {
		t.Fatal("expected per-IaC plugin to handle CloudFormation JSON, but it fell through")
	}
	if resp == nil {
		t.Fatal("expected non-nil response from per-IaC plugin")
	}
}

func TestPerIaCOnly_ParseCloudFormationYAML(t *testing.T) {
	cfg, _ := setupE2EConfig(t)

	cfnDir := t.TempDir()
	cfnYAML := `AWSTemplateFormatVersion: "2010-09-09"
Resources:
  MyBucket:
    Type: AWS::S3::Bucket
    Properties:
      BucketName: my-test-bucket
`
	f := filepath.Join(cfnDir, "stack.yaml")
	if err := os.WriteFile(f, []byte(cfnYAML), 0644); err != nil {
		t.Fatal(err)
	}

	opts := defaultOptions()
	opts.RepoDirectory = cfnDir
	opts.WorkingDirectory = cfnDir

	resp, handled, err := cfg.tryPerIaCPlugins(context.Background(), f, defaultRepoConfig(), defaultProject(), hclog.Off, opts)
	if err != nil {
		t.Fatal(err)
	}
	if !handled {
		t.Fatal("expected per-IaC plugin to handle CloudFormation YAML, but it fell through")
	}
	if resp == nil {
		t.Fatal("expected non-nil response from per-IaC plugin")
	}
}

func TestPerIaCOnly_ParseARMTemplate(t *testing.T) {
	cfg, _ := setupE2EConfig(t)

	armDir := t.TempDir()
	armJSON := `{
		"$schema": "https://schema.management.azure.com/schemas/2019-04-01/deploymentTemplate.json#",
		"contentVersion": "1.0.0.0",
		"resources": [
			{
				"type": "Microsoft.Storage/storageAccounts",
				"apiVersion": "2021-02-01",
				"name": "mystorageaccount",
				"location": "eastus",
				"kind": "StorageV2",
				"sku": {
					"name": "Standard_LRS"
				}
			}
		]
	}`
	f := filepath.Join(armDir, "template.json")
	if err := os.WriteFile(f, []byte(armJSON), 0644); err != nil {
		t.Fatal(err)
	}

	opts := defaultOptions()
	opts.RepoDirectory = armDir
	opts.WorkingDirectory = armDir

	resp, handled, err := cfg.tryPerIaCPlugins(context.Background(), f, defaultRepoConfig(), defaultProject(), hclog.Off, opts)
	if err != nil {
		t.Fatal(err)
	}
	if !handled {
		t.Fatal("expected per-IaC plugin to handle ARM template, but it fell through")
	}
	if resp == nil {
		t.Fatal("expected non-nil response from per-IaC plugin")
	}
}
