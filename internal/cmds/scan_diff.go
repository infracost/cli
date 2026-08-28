package cmds

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/infracost/cli/internal/api/dashboard"
	"github.com/infracost/cli/internal/cache"
	"github.com/infracost/cli/internal/config"
	"github.com/infracost/cli/internal/format"
	"github.com/infracost/cli/internal/scanner"
	"github.com/infracost/cli/internal/vcs"
	pkgscanner "github.com/infracost/cli/pkg/scanner"
	"golang.org/x/oauth2"
)

// ScanDiff runs `scan --diff`: the target must be a Terraform plan JSON file
// (`terraform show -json plan.tfplan`), which carries both the planned state
// and the prior state. The plan file itself is scanned through the normal
// pipeline (policies, telemetry and caching apply as for any scan), then a
// synthetic plan whose planned_values is the plan's prior_state is scanned to
// price the previous state, and the two outputs are diffed.
//
// The current-state ScanResult is returned alongside the diff so the caller
// can apply the usual critical-diagnostics exit-code handling.
func ScanDiff(ctx context.Context, cfg *config.Config, source oauth2.TokenSource, store cache.Store, in ScanInput, outputFormat string, pluginOpts pkgscanner.PluginOpts) (*format.ScanDiffOutput, ScanResult, error) {
	target := in.Path
	if target == "" {
		target = "."
	}
	absolutePath, err := filepath.Abs(filepath.Clean(target))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get absolute path to target: %w", err)
	}

	info, err := os.Stat(absolutePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, fmt.Errorf("target does not exist")
		}
		return nil, nil, fmt.Errorf("failed to get info for target: %w", err)
	}
	if info.IsDir() {
		return nil, nil, fmt.Errorf("--diff currently only supports Terraform plan JSON: pass the path to a plan JSON file (terraform show -json plan.tfplan > plan.json)")
	}

	plan, err := readPlanJSON(absolutePath)
	if err != nil {
		return nil, nil, err
	}

	current, err := Scan(ctx, cfg, source, store, in, outputFormat, pluginOpts)
	if err != nil {
		return nil, nil, err
	}
	// A plan that failed to parse has no priced resources; diffing it would
	// report a bogus $0 state, so surface the failure instead.
	if err := criticalDiagnosticsErr(current); err != nil {
		return nil, current, err
	}

	previous, err := scanPriorState(ctx, cfg, source, in, plan, vcs.GetCurrentBranch(filepath.Dir(absolutePath)), pluginOpts)
	if err != nil {
		return nil, current, fmt.Errorf("failed to cost the plan's prior state: %w", err)
	}

	return format.BuildScanDiff(previous, current), current, nil
}

// scanPriorState prices the prior state embedded in a plan: it writes a
// synthetic plan JSON whose planned_values is the original's prior_state into
// a temp dir and scans it. The scan runs with zero RunParameters so no
// policies, guardrails or budgets are evaluated — only costs are needed — and
// bypasses Scan() so the synthetic file never pollutes run telemetry or the
// results cache. Zero RunParameters only match the current scan's settings in
// self-hosted pricing mode, which validateDiffFlags guarantees; supporting
// Infracost Cloud runs means sharing the current scan's RunParameters here
// and skipping only policies, guardrails and budgets.
func scanPriorState(ctx context.Context, cfg *config.Config, source oauth2.TokenSource, in ScanInput, plan map[string]json.RawMessage, branchName string, pluginOpts pkgscanner.PluginOpts) (*format.Output, error) {
	tempDir, err := os.MkdirTemp("", "infracost-diff-")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tempDir) }()

	priorPath := filepath.Join(tempDir, "prior-plan.json")
	priorPlan, err := priorPlanJSON(plan)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(priorPath, priorPlan, 0o600); err != nil {
		return nil, fmt.Errorf("failed to write prior state plan file: %w", err)
	}

	s := &scanner.Scanner{
		Plugins:         &cfg.Plugins,
		Logging:         cfg.Logging,
		Dashboard:       cfg.Dashboard,
		Currency:        in.Currency,
		PricingEndpoint: cfg.PricingEndpoint,
		PricingAPIKey:   cfg.PricingAPIKey,
		FetchAuth:       scanner.SSHFetchAuthFromValue(cfg.SSHKeyFile),
	}
	result, err := s.Scan(ctx, dashboard.RunParameters{}, priorPath, branchName, source, pluginOpts)
	if err != nil {
		return nil, err
	}
	output := format.ToOutput(result)
	if err := criticalDiagnosticsErr(&output); err != nil {
		return nil, err
	}
	return &output, nil
}

var (
	wrapperHeaderLine  = regexp.MustCompile(`(?m)^\[command\].*\r?\n`)
	wrapperCommandLine = regexp.MustCompile(`(?m)^::.*(\r?\n|$)`)
)

// readPlanJSON reads and validates a Terraform plan JSON file, returning its
// top-level keys. Mirrors the terraform-plan parser plugin's identification:
// noise from the setup-terraform GitHub Action's wrapper is stripped, and the
// content must carry format_version plus at least one plan-specific marker
// key — this rejects HCL, state JSON, and arbitrary JSON with a message
// pointing at what --diff supports.
func readPlanJSON(path string) (map[string]json.RawMessage, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- user-specified scan target
	if err != nil {
		return nil, fmt.Errorf("failed to read plan file: %w", err)
	}
	if trimmed := bytes.TrimLeft(raw, " \t\r\n"); len(trimmed) == 0 || trimmed[0] != '{' {
		raw = wrapperCommandLine.ReplaceAll(wrapperHeaderLine.ReplaceAll(raw, nil), nil)
	}

	var plan map[string]json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&plan); err != nil {
		return nil, fmt.Errorf("--diff currently only supports Terraform plan JSON, and %q is not valid JSON", path)
	}

	_, hasFormatVersion := plan["format_version"]
	_, hasPlannedValues := plan["planned_values"]
	_, hasResourceChanges := plan["resource_changes"]
	_, hasConfiguration := plan["configuration"]
	hasPlanMarker := hasPlannedValues || hasResourceChanges || hasConfiguration
	if !hasFormatVersion || !hasPlanMarker {
		return nil, fmt.Errorf("--diff currently only supports Terraform plan JSON, and %q does not look like one (generate it with: terraform show -json plan.tfplan > plan.json)", path)
	}
	return plan, nil
}

// priorPlanJSON builds a plan JSON document representing the prior state: the
// original plan with planned_values replaced by prior_state.values.
// configuration is carried over for provider region resolution. A plan with
// no prior state (first apply) yields an empty planned_values, which the
// parser prices as nothing.
func priorPlanJSON(plan map[string]json.RawMessage) ([]byte, error) {
	priorValues := json.RawMessage(`{}`)
	if rawPrior, ok := plan["prior_state"]; ok {
		var prior struct {
			Values json.RawMessage `json:"values"`
		}
		if err := json.Unmarshal(rawPrior, &prior); err != nil {
			return nil, fmt.Errorf("failed to parse the plan's prior_state: %w", err)
		}
		if len(prior.Values) > 0 {
			priorValues = prior.Values
		}
	}

	priorPlan := map[string]json.RawMessage{
		"format_version": plan["format_version"],
		"planned_values": priorValues,
	}
	if v, ok := plan["terraform_version"]; ok {
		priorPlan["terraform_version"] = v
	}
	if v, ok := plan["configuration"]; ok {
		priorPlan["configuration"] = v
	}
	return json.Marshal(priorPlan)
}
