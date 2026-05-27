package parser

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/hashicorp/go-hclog"
)

func TestIsPerIaCPluginBinary(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"infracost-parser-plugin-terraform", true},
		{"infracost-parser-plugin-cloudformation", true},
		{"infracost-parser-plugin-terraform.exe", true},
		{"infracost-parser-plugin-cloudformation.exe", true},
		{"infracost-parser-plugin", false},
		{"infracost-parser-plugin.exe", false},
		{"infracost-parser-plugin-terraform-debug", false},
		{"infracost-parser-plugin-terraform-debug.exe", false},
		{"random-binary", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isPerIaCPluginBinary(tt.name)
			if got != tt.want {
				t.Errorf("isPerIaCPluginBinary(%q) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}

func TestNewPluginManager_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	mgr := NewPluginManager(dir, hclog.Off)
	defer mgr.Close()

	if mgr.HasPlugins() {
		t.Fatal("expected no plugins in empty directory")
	}
	if len(mgr.Plugins()) != 0 {
		t.Fatalf("expected 0 plugins, got %d", len(mgr.Plugins()))
	}
}

func TestNewPluginManager_EmptyString(t *testing.T) {
	mgr := NewPluginManager("", hclog.Off)
	defer mgr.Close()

	if mgr.HasPlugins() {
		t.Fatal("expected no plugins with empty plugin dir")
	}
}

func TestNewPluginManager_NonexistentDir(t *testing.T) {
	mgr := NewPluginManager("/nonexistent/plugin/dir", hclog.Off)
	defer mgr.Close()

	if mgr.HasPlugins() {
		t.Fatal("expected no plugins with nonexistent dir")
	}
}

func TestNewPluginManager_NoMatchingBinaries(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "random-binary")
	if err := os.WriteFile(f, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}

	mgr := NewPluginManager(dir, hclog.Off)
	defer mgr.Close()

	if mgr.HasPlugins() {
		t.Fatal("expected no plugins when no matching binaries exist")
	}
}

func TestDetect_NoPlugins(t *testing.T) {
	dir := t.TempDir()
	mgr := NewPluginManager(dir, hclog.Off)
	defer mgr.Close()

	plugin, projectType, err := mgr.Detect(context.Background(), "/some/path")
	if err != nil {
		t.Fatal(err)
	}
	if plugin != nil {
		t.Fatal("expected nil plugin when no plugins are discovered")
	}
	if projectType != "" {
		t.Fatalf("expected empty project type, got %q", projectType)
	}
}

func TestPluginManager_Close_Idempotent(t *testing.T) {
	dir := t.TempDir()
	mgr := NewPluginManager(dir, hclog.Off)
	mgr.Close()
	mgr.Close()
	if mgr.HasPlugins() {
		t.Fatal("expected no plugins after close")
	}
}

// Integration tests that use real compiled per-IaC plugin binaries.
// These require the parser binaries to be built first:
//   cd /path/to/parser && make build-plugins

func parserBinDir() string {
	binDir := os.Getenv("INFRACOST_TEST_PARSER_BIN_DIR")
	if binDir != "" {
		return binDir
	}
	// Default to the relative path from the CLI repo root.
	return filepath.Join("..", "..", "..", "..", "parser", "bin")
}

func hasPerIaCPlugins(t *testing.T) string {
	t.Helper()
	dir := parserBinDir()
	tfPlugin := filepath.Join(dir, "infracost-parser-plugin-terraform")
	cfnPlugin := filepath.Join(dir, "infracost-parser-plugin-cloudformation")

	if _, err := os.Stat(tfPlugin); err != nil {
		t.Skipf("terraform per-IaC plugin not found at %s (run: cd parser && make build-plugins)", tfPlugin)
	}
	if _, err := os.Stat(cfnPlugin); err != nil {
		t.Skipf("cloudformation per-IaC plugin not found at %s (run: cd parser && make build-plugins)", cfnPlugin)
	}
	abs, _ := filepath.Abs(dir)
	return abs
}

func TestIntegration_PluginManager_DiscoverPlugins(t *testing.T) {
	dir := hasPerIaCPlugins(t)

	mgr := NewPluginManager(dir, hclog.Off)
	defer mgr.Close()

	if !mgr.HasPlugins() {
		t.Fatal("expected to discover per-IaC plugins")
	}

	plugins := mgr.Plugins()
	if len(plugins) < 2 {
		t.Fatalf("expected at least 2 plugins, got %d", len(plugins))
	}

	names := make(map[string]bool)
	for _, p := range plugins {
		names[p.Name] = true
	}
	if !names["terraform"] {
		t.Error("expected terraform plugin to be discovered")
	}
	if !names["cloudformation"] {
		t.Error("expected cloudformation plugin to be discovered")
	}
}

func TestIntegration_PluginManager_PriorityOrder(t *testing.T) {
	dir := hasPerIaCPlugins(t)

	mgr := NewPluginManager(dir, hclog.Off)
	defer mgr.Close()

	plugins := mgr.Plugins()
	for i := 1; i < len(plugins); i++ {
		if plugins[i].Metadata.Priority < plugins[i-1].Metadata.Priority {
			t.Errorf("plugins not sorted by priority: %s (priority %d) before %s (priority %d)",
				plugins[i-1].Name, plugins[i-1].Metadata.Priority,
				plugins[i].Name, plugins[i].Metadata.Priority)
		}
	}

	if plugins[0].Name != "terraform" {
		t.Errorf("expected terraform plugin first (lowest priority number), got %s", plugins[0].Name)
	}
}

func TestIntegration_Detect_TerraformDirectory(t *testing.T) {
	dir := hasPerIaCPlugins(t)

	tfDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tfDir, "main.tf"), []byte(`resource "aws_instance" "a" {}`), 0644); err != nil {
		t.Fatal(err)
	}

	mgr := NewPluginManager(dir, hclog.Off)
	defer mgr.Close()

	plugin, projectType, err := mgr.Detect(context.Background(), tfDir)
	if err != nil {
		t.Fatal(err)
	}
	if plugin == nil {
		t.Fatal("expected terraform plugin to claim directory")
	}
	if plugin.Name != "terraform" {
		t.Errorf("expected terraform plugin, got %s", plugin.Name)
	}
	if projectType != "terraform" {
		t.Errorf("expected project type 'terraform', got %q", projectType)
	}
}

func TestIntegration_Detect_TerragruntDirectory(t *testing.T) {
	dir := hasPerIaCPlugins(t)

	tgDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tgDir, "terragrunt.hcl"), []byte(`include "root" {}`), 0644); err != nil {
		t.Fatal(err)
	}

	mgr := NewPluginManager(dir, hclog.Off)
	defer mgr.Close()

	plugin, projectType, err := mgr.Detect(context.Background(), tgDir)
	if err != nil {
		t.Fatal(err)
	}
	if plugin == nil {
		t.Fatal("expected terraform plugin to claim terragrunt directory")
	}
	if projectType != "terragrunt" {
		t.Errorf("expected project type 'terragrunt', got %q", projectType)
	}
}

func TestIntegration_Detect_CloudFormationJSON(t *testing.T) {
	dir := hasPerIaCPlugins(t)

	cfnDir := t.TempDir()
	cfnJSON := `{
		"AWSTemplateFormatVersion": "2010-09-09",
		"Resources": {
			"MyBucket": { "Type": "AWS::S3::Bucket" }
		}
	}`
	f := filepath.Join(cfnDir, "template.json")
	if err := os.WriteFile(f, []byte(cfnJSON), 0644); err != nil {
		t.Fatal(err)
	}

	mgr := NewPluginManager(dir, hclog.Off)
	defer mgr.Close()

	plugin, projectType, err := mgr.Detect(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	if plugin == nil {
		t.Fatal("expected cloudformation plugin to claim JSON template")
	}
	if plugin.Name != "cloudformation" {
		t.Errorf("expected cloudformation plugin, got %s", plugin.Name)
	}
	if projectType != "cloudformation" {
		t.Errorf("expected project type 'cloudformation', got %q", projectType)
	}
}

func TestIntegration_Detect_CloudFormationYAML(t *testing.T) {
	dir := hasPerIaCPlugins(t)

	cfnDir := t.TempDir()
	cfnYAML := `AWSTemplateFormatVersion: "2010-09-09"
Resources:
  MyBucket:
    Type: AWS::S3::Bucket
`
	f := filepath.Join(cfnDir, "stack.yaml")
	if err := os.WriteFile(f, []byte(cfnYAML), 0644); err != nil {
		t.Fatal(err)
	}

	mgr := NewPluginManager(dir, hclog.Off)
	defer mgr.Close()

	plugin, projectType, err := mgr.Detect(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	if plugin == nil {
		t.Fatal("expected cloudformation plugin to claim YAML template")
	}
	if projectType != "cloudformation" {
		t.Errorf("expected project type 'cloudformation', got %q", projectType)
	}
}

func TestIntegration_Detect_UnknownFile(t *testing.T) {
	dir := hasPerIaCPlugins(t)

	tmpDir := t.TempDir()
	f := filepath.Join(tmpDir, "data.csv")
	if err := os.WriteFile(f, []byte("a,b,c\n1,2,3\n"), 0644); err != nil {
		t.Fatal(err)
	}

	mgr := NewPluginManager(dir, hclog.Off)
	defer mgr.Close()

	plugin, _, err := mgr.Detect(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	if plugin != nil {
		t.Errorf("expected no plugin to claim .csv file, but %s did", plugin.Name)
	}
}

func TestIntegration_Detect_TFJSONPriorityOverCFN(t *testing.T) {
	dir := hasPerIaCPlugins(t)

	tmpDir := t.TempDir()
	tfJSON := `{"resource": {"aws_instance": {"a": {}}}}`
	f := filepath.Join(tmpDir, "main.tf.json")
	if err := os.WriteFile(f, []byte(tfJSON), 0644); err != nil {
		t.Fatal(err)
	}

	mgr := NewPluginManager(dir, hclog.Off)
	defer mgr.Close()

	plugin, projectType, err := mgr.Detect(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	if plugin == nil {
		t.Fatal("expected a plugin to claim .tf.json file")
	}
	if plugin.Name != "terraform" {
		t.Errorf("expected terraform plugin (higher priority) to claim .tf.json, got %s", plugin.Name)
	}
	if projectType != "terraform" {
		t.Errorf("expected project type 'terraform', got %q", projectType)
	}
}
