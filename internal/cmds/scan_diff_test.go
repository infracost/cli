package cmds

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/infracost/cli/internal/config"
	"github.com/infracost/cli/internal/format"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeTempFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

const minimalPlanJSON = `{
  "format_version": "1.2",
  "terraform_version": "1.7.0",
  "planned_values": {"root_module": {"resources": [{"address": "aws_instance.web", "type": "aws_instance", "name": "web", "mode": "managed", "values": {"instance_type": "t2.medium"}}]}},
  "prior_state": {"format_version": "1.0", "values": {"root_module": {"resources": [{"address": "aws_instance.web", "type": "aws_instance", "name": "web", "mode": "managed", "values": {"instance_type": "t2.small"}}]}}},
  "resource_changes": [],
  "configuration": {"provider_config": {"aws": {"name": "aws", "expressions": {"region": {"constant_value": "us-east-1"}}}}}
}`

func TestReadPlanJSON_ValidPlan(t *testing.T) {
	path := writeTempFile(t, "plan.json", minimalPlanJSON)
	plan, err := readPlanJSON(path)
	require.NoError(t, err)
	assert.Contains(t, plan, "planned_values")
	assert.Contains(t, plan, "prior_state")
}

func TestReadPlanJSON_WrappedPlan(t *testing.T) {
	// hashicorp/setup-terraform's wrapper prepends a [command] line and
	// appends ::set-output lines when show output is piped to a file.
	wrapped := "[command]/usr/bin/terraform show -json plan.tfplan\n" + minimalPlanJSON + "\n::set-output name=stdout::...\n"
	path := writeTempFile(t, "plan.json", wrapped)
	plan, err := readPlanJSON(path)
	require.NoError(t, err)
	assert.Contains(t, plan, "planned_values")
}

func TestReadPlanJSON_RejectsNonJSON(t *testing.T) {
	path := writeTempFile(t, "main.tf", `resource "aws_instance" "web" {}`)
	_, err := readPlanJSON(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "only supports Terraform plan JSON")
}

func TestReadPlanJSON_RejectsStateJSON(t *testing.T) {
	// `terraform show -json` on a state file has format_version + values but
	// none of the plan marker keys.
	path := writeTempFile(t, "state.json", `{"format_version": "1.0", "values": {"root_module": {}}}`)
	_, err := readPlanJSON(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not look like one")
}

func TestReadPlanJSON_RejectsArbitraryJSON(t *testing.T) {
	path := writeTempFile(t, "package.json", `{"name": "not-a-plan", "version": "1.0.0"}`)
	_, err := readPlanJSON(path)
	require.Error(t, err)
}

func TestPriorPlanJSON_SwapsPriorState(t *testing.T) {
	path := writeTempFile(t, "plan.json", minimalPlanJSON)
	plan, err := readPlanJSON(path)
	require.NoError(t, err)

	priorRaw, err := priorPlanJSON(plan)
	require.NoError(t, err)

	var prior map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(priorRaw, &prior))

	assert.Contains(t, prior, "format_version")
	assert.Contains(t, prior, "terraform_version")
	assert.Contains(t, prior, "configuration")
	assert.NotContains(t, prior, "prior_state")
	assert.NotContains(t, prior, "resource_changes")

	// planned_values must now hold the prior state's values: the t2.small
	// instance, not the planned t2.medium.
	var values struct {
		RootModule struct {
			Resources []struct {
				Address string         `json:"address"`
				Values  map[string]any `json:"values"`
			} `json:"resources"`
		} `json:"root_module"`
	}
	require.NoError(t, json.Unmarshal(prior["planned_values"], &values))
	require.Len(t, values.RootModule.Resources, 1)
	assert.Equal(t, "aws_instance.web", values.RootModule.Resources[0].Address)
	assert.Equal(t, "t2.small", values.RootModule.Resources[0].Values["instance_type"])
}

func TestPriorPlanJSON_NoPriorState(t *testing.T) {
	plan := map[string]json.RawMessage{
		"format_version": json.RawMessage(`"1.2"`),
		"planned_values": json.RawMessage(`{"root_module": {}}`),
	}
	priorRaw, err := priorPlanJSON(plan)
	require.NoError(t, err)

	var prior map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(priorRaw, &prior))
	assert.JSONEq(t, `{}`, string(prior["planned_values"]))
}

func outputWithResources(names ...string) *format.Output {
	resources := make([]format.ResourceOutput, 0, len(names))
	for _, name := range names {
		resources = append(resources, format.ResourceOutput{Name: name})
	}
	return &format.Output{Projects: []format.ProjectOutput{{ProjectName: "main", Resources: resources}}}
}

func resourceNames(o *format.Output) []string {
	var names []string
	for _, p := range o.Projects {
		for _, r := range p.Resources {
			names = append(names, r.Name)
		}
	}
	return names
}

func TestStripNonTargetResources_TargetedPlan(t *testing.T) {
	// A -target plan: prior_state carries the whole infrastructure, but only
	// aws_instance.a was targeted (planned_values + resource_changes). The
	// untargeted aws_instance.b must not surface in the diff as deleted.
	// aws_s3_bucket.c is a targeted no-op: present in the current scan, so
	// kept even without a resource_changes entry.
	previous := outputWithResources("aws_instance.a", "aws_instance.b", "aws_s3_bucket.c")
	current := outputWithResources("aws_instance.a", "aws_s3_bucket.c")
	plan := map[string]json.RawMessage{
		"resource_changes": json.RawMessage(`[{"address": "aws_instance.a", "change": {"actions": ["update"]}}]`),
	}

	require.NoError(t, stripNonTargetResources(previous, current, plan))

	assert.Equal(t, []string{"aws_instance.a", "aws_s3_bucket.c"}, resourceNames(previous))
	assert.Equal(t, []string{"aws_instance.a", "aws_s3_bucket.c"}, resourceNames(current))
}

func TestStripNonTargetResources_KeepsDeletions(t *testing.T) {
	// A deleted resource is absent from planned_values but present in
	// resource_changes, so it must survive the filter to show as removed.
	previous := outputWithResources("aws_instance.doomed")
	current := outputWithResources()
	plan := map[string]json.RawMessage{
		"resource_changes": json.RawMessage(`[{"address": "aws_instance.doomed", "change": {"actions": ["delete"]}}]`),
	}

	require.NoError(t, stripNonTargetResources(previous, current, plan))

	assert.Equal(t, []string{"aws_instance.doomed"}, resourceNames(previous))
}

func TestStripNonTargetResources_NoResourceChangesKey(t *testing.T) {
	previous := outputWithResources("aws_instance.a", "aws_instance.b")
	current := outputWithResources("aws_instance.a")
	plan := map[string]json.RawMessage{}

	require.NoError(t, stripNonTargetResources(previous, current, plan))

	// Without resource_changes there is no target information; leave the
	// prior side untouched.
	assert.Equal(t, []string{"aws_instance.a", "aws_instance.b"}, resourceNames(previous))
}

func TestStripNonTargetResources_InvalidResourceChanges(t *testing.T) {
	previous := outputWithResources("aws_instance.a")
	current := outputWithResources("aws_instance.a")
	plan := map[string]json.RawMessage{
		"resource_changes": json.RawMessage(`{"not": "a list"}`),
	}

	require.Error(t, stripNonTargetResources(previous, current, plan))
}

func TestStagePriorScanConfig_NoConfig(t *testing.T) {
	targetDir := t.TempDir()
	tempDir := t.TempDir()

	require.NoError(t, stagePriorScanConfig(targetDir, tempDir))

	entries, err := os.ReadDir(tempDir)
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestStagePriorScanConfig_CopiesConfigAndUsage(t *testing.T) {
	targetDir := t.TempDir()
	tempDir := t.TempDir()

	configYML := "version: \"1.0\"\ncurrency: EUR\nusage_file: usage/infracost-usage.yml\nprojects:\n  - path: plan.json\n"
	require.NoError(t, os.WriteFile(filepath.Join(targetDir, "infracost.yml"), []byte(configYML), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(targetDir, "usage"), 0o700))
	usageYML := "version: 0.1\nresource_usage:\n  aws_s3_bucket.assets:\n    standard:\n      storage_gb: 500\n"
	require.NoError(t, os.WriteFile(filepath.Join(targetDir, "usage", "infracost-usage.yml"), []byte(usageYML), 0o600))

	require.NoError(t, stagePriorScanConfig(targetDir, tempDir))

	stagedConfig, err := os.ReadFile(filepath.Join(tempDir, "infracost.yml"))
	require.NoError(t, err)
	assert.Equal(t, configYML, string(stagedConfig))

	stagedUsage, err := os.ReadFile(filepath.Join(tempDir, "usage", "infracost-usage.yml"))
	require.NoError(t, err)
	assert.Equal(t, usageYML, string(stagedUsage))
}

func TestStagePriorScanConfig_CopiesTemplate(t *testing.T) {
	targetDir := t.TempDir()
	tempDir := t.TempDir()

	tmpl := "version: \"1.0\"\nprojects:\n  - path: plan.json\n"
	require.NoError(t, os.WriteFile(filepath.Join(targetDir, "infracost.yml.tmpl"), []byte(tmpl), 0o600))

	require.NoError(t, stagePriorScanConfig(targetDir, tempDir))

	staged, err := os.ReadFile(filepath.Join(tempDir, "infracost.yml.tmpl"))
	require.NoError(t, err)
	assert.Equal(t, tmpl, string(staged))
}

func TestStagePriorScanConfig_MissingUsageFileOK(t *testing.T) {
	targetDir := t.TempDir()
	tempDir := t.TempDir()

	// The scanner treats a missing usage file as "no usage data", so staging
	// must too — both scans then agree.
	configYML := "version: \"1.0\"\nusage_file: infracost-usage.yml\n"
	require.NoError(t, os.WriteFile(filepath.Join(targetDir, "infracost.yml"), []byte(configYML), 0o600))

	require.NoError(t, stagePriorScanConfig(targetDir, tempDir))

	_, err := os.Stat(filepath.Join(tempDir, "infracost-usage.yml"))
	assert.True(t, os.IsNotExist(err))
}

func TestStagePriorScanConfig_RejectsEscapingUsagePath(t *testing.T) {
	targetDir := t.TempDir()
	tempDir := t.TempDir()

	configYML := "version: \"1.0\"\nusage_file: ../infracost-usage.yml\n"
	require.NoError(t, os.WriteFile(filepath.Join(targetDir, "infracost.yml"), []byte(configYML), 0o600))

	err := stagePriorScanConfig(targetDir, tempDir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "usage_file")
}

func TestValidateDiffFlags(t *testing.T) {
	cfg := &config.Config{}

	require.NoError(t, validateDiffFlags(false, cfg))

	err := validateDiffFlags(true, cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires --json")

	// --json alone is not enough: Infracost Cloud mode would scan the prior
	// state without the org's usage defaults, so --diff is gated to
	// self-hosted pricing mode for now.
	cfg.JSON.Value = true
	err = validateDiffFlags(true, cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "self-hosted pricing mode")

	cfg.PricingAPIKey = "static-key"
	require.NoError(t, validateDiffFlags(true, cfg))

	cfg.LLM.Value = true
	err = validateDiffFlags(true, cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--llm")
}
