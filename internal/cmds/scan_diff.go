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
	"gopkg.in/yaml.v3"
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

	previous, err := scanPriorState(ctx, cfg, source, in, plan, absolutePath, pluginOpts)
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
//
// The target directory's repo config and usage file are mirrored into the
// temp dir, and the synthetic plan keeps the target's basename, so the prior
// scan resolves the same currency, usage data and project entry as the
// current scan.
func scanPriorState(ctx context.Context, cfg *config.Config, source oauth2.TokenSource, in ScanInput, plan map[string]json.RawMessage, targetPath string, pluginOpts pkgscanner.PluginOpts) (*format.Output, error) {
	tempDir, err := os.MkdirTemp("", "infracost-diff-")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tempDir) }()

	targetDir := filepath.Dir(targetPath)
	if err := stagePriorScanConfig(targetDir, tempDir); err != nil {
		return nil, err
	}

	priorPath := filepath.Join(tempDir, filepath.Base(targetPath))
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
	result, err := s.Scan(ctx, dashboard.RunParameters{}, priorPath, vcs.GetCurrentBranch(targetDir), source, pluginOpts)
	if err != nil {
		return nil, err
	}
	output := format.ToOutput(result)
	if err := criticalDiagnosticsErr(&output); err != nil {
		return nil, err
	}
	return &output, nil
}

// stagePriorScanConfig mirrors the target directory's scan configuration into
// the prior scan's temp dir: infracost.yml (and its template), plus the usage
// file the config references. Currency and the usage file path only come from
// a loaded repo config — a generated one never sets them — so without this
// the prior scan would fall back to USD with no usage while the current scan
// uses the repo's settings, and unchanged usage-based resources would show
// phony cost changes. A template is copied verbatim but not rendered, so a
// usage file only referenced from the template is not mirrored.
func stagePriorScanConfig(targetDir, tempDir string) error {
	for _, name := range []string{pkgscanner.RepoConfigFilename, pkgscanner.RepoConfigTemplateFilename} {
		if err := copyFileIfExists(filepath.Join(targetDir, name), filepath.Join(tempDir, name)); err != nil {
			return err
		}
	}

	usageFile, err := repoConfigUsageFilePath(filepath.Join(targetDir, pkgscanner.RepoConfigFilename))
	if err != nil {
		return err
	}
	if usageFile == "" {
		return nil
	}
	// A usage path that escapes the repo directory can't be mirrored into the
	// temp dir at the same relative location; the prior scan would silently
	// run without usage data, so refuse rather than return skewed numbers.
	if !filepath.IsLocal(usageFile) {
		return fmt.Errorf("--diff requires the %s usage_file to be a relative path inside the repository, got %q", pkgscanner.RepoConfigFilename, usageFile)
	}
	dst := filepath.Join(tempDir, usageFile)
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return fmt.Errorf("failed to stage usage file for the prior state scan: %w", err)
	}
	return copyFileIfExists(filepath.Join(targetDir, usageFile), dst)
}

// repoConfigUsageFilePath returns the top-level usage_file from an
// infracost.yml, or "" when the config or the key is absent. All repo config
// formats (current, file-based and legacy) spell the key usage_file, so a
// minimal decode avoids pulling the full config loader (and its plugin
// machinery) into the diff path.
func repoConfigUsageFilePath(configPath string) (string, error) {
	data, err := os.ReadFile(configPath) // #nosec G304 -- repo config next to the user-specified scan target
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("failed to read %s: %w", configPath, err)
	}
	var repoConfig struct {
		UsageFilePath string `yaml:"usage_file"`
	}
	if err := yaml.Unmarshal(data, &repoConfig); err != nil {
		return "", fmt.Errorf("failed to parse %s: %w", configPath, err)
	}
	return repoConfig.UsageFilePath, nil
}

// copyFileIfExists copies src to dst, silently doing nothing when src does
// not exist (or is a directory) — mirroring how the scanner treats a missing
// usage file as "no usage data" rather than an error.
func copyFileIfExists(src, dst string) error {
	stat, err := os.Stat(src)
	if err != nil || stat.IsDir() {
		return nil
	}
	data, err := os.ReadFile(src) // #nosec G304 -- config file next to the user-specified scan target
	if err != nil {
		return fmt.Errorf("failed to read %s: %w", src, err)
	}
	if err := os.WriteFile(dst, data, 0o600); err != nil {
		return fmt.Errorf("failed to stage %s for the prior state scan: %w", filepath.Base(src), err)
	}
	return nil
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
