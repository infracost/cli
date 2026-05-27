package parser

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/hashicorp/go-hclog"
	repoconfig "github.com/infracost/config"
	"github.com/infracost/proto/gen/go/infracost/parser/options"
)

// End-to-end tests that exercise the full dual-mode flow:
// tryPerIaCPlugins → legacyRoute, using real compiled plugin binaries.
//
// These require both the mono-parser and per-IaC plugins to be built:
//   cd parser && make build-plugins

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

	// Set PluginDir so per-IaC plugins are discovered.
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

	// Point PluginDir at an empty directory so no per-IaC plugins are found.
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

	// Empty string means per-IaC is completely disabled.
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
