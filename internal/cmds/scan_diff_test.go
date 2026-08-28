package cmds

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/infracost/cli/internal/config"
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
