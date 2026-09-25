package cmds

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	githubDiffWorkflowPath = ".github/workflows/infracost-diff.yml"
	githubScanWorkflowPath = ".github/workflows/infracost-scan.yml"
)

// githubWriter owns two whole workflow files rather than splicing into one the
// user wrote. The names are kept because branch protection checks are named
// after the job, and a check that disappears blocks every merge.
type githubWriter struct{}

// ciFileMarker heads a file the CLI owns outright, carrying the same version an
// spliced file carries on its sentinels.
var ciFileMarker = fmt.Sprintf("# Managed by infracost ci setup v%d — re-run `infracost ci setup --pipeline` to update.", ciBlockVersion)

func (githubWriter) ConfigPaths(string) ([]string, error) {
	return []string{githubDiffWorkflowPath, githubScanWorkflowPath}, nil
}

func (githubWriter) Write(repoRoot string, opts ciJobOpts) ([]ciWriteResult, error) {
	dir := filepath.Join(repoRoot, ".github", "workflows")
	if err := os.MkdirAll(dir, 0o750); err != nil { //nolint:gosec // G301: workflows dir needs group read+exec for CI runners
		return nil, fmt.Errorf("creating workflow directory: %w", err)
	}

	files := []struct {
		path    string
		content string
	}{
		{githubDiffWorkflowPath, githubDiffWorkflowContent(opts)},
		{githubScanWorkflowPath, githubScanWorkflowContent(opts)},
	}

	results := make([]ciWriteResult, 0, len(files))
	for _, f := range files {
		created, unchanged, err := writeCIConfigFile(filepath.Join(repoRoot, filepath.FromSlash(f.path)), f.content)
		if err != nil {
			// Files written before the failure are still on disk, so hand them
			// back with the error rather than leaving them unreported.
			return results, err
		}
		results = append(results, ciWriteResult{path: f.path, created: created, unchanged: unchanged})
	}
	return results, nil
}

func (githubWriter) Steps(opts ciJobOpts) []string {
	return []string{
		"Set the API key as a GitHub secret:",
		"",
		// Piped, not --body: argv is readable by every other user on the box.
		fmt.Sprintf("  printf '%%s' \"$%s\" | gh secret set %s \\", opts.apiKeySecret, opts.apiKeySecret),
		fmt.Sprintf("      --repo %s", opts.repo.hostSlug()),
		"",
		"Or add it in GitHub:",
		fmt.Sprintf("  https://%s/%s/settings/secrets/actions/new", opts.repo.host, opts.repo.slug()),
	}
}

func (githubWriter) CanSetSecret() bool {
	path, _ := exec.LookPath("gh")
	return path != ""
}

// SetSecret reads the key from the environment variable that shares the
// secret's name, so the value never travels through ciJobOpts. The value goes
// in on stdin, because argv is readable by every other user on the machine.
func (githubWriter) SetSecret(ctx context.Context, opts ciJobOpts) error {
	ghPath, err := exec.LookPath("gh")
	if err != nil {
		return fmt.Errorf("looking up gh: %w", err)
	}

	value := os.Getenv(opts.apiKeySecret)
	if value == "" {
		return fmt.Errorf("%s is not set in the environment", opts.apiKeySecret)
	}

	cmd := exec.CommandContext(ctx, ghPath, "secret", "set", opts.apiKeySecret, //nolint:gosec // G204: ghPath is from exec.LookPath, not user input
		"--repo", opts.repo.hostSlug())
	cmd.Stdin = strings.NewReader(value)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return fmt.Errorf("%w: %s", err, msg)
		}
		return err
	}
	return nil
}

func githubDiffWorkflowContent(opts ciJobOpts) string {
	return `name: Infracost Diff
` + ciFileMarker + `

on:
  pull_request:
    types: [opened, synchronize, reopened, closed]
  workflow_dispatch:
    inputs:
      pr-number:
        description: "Pull request number to scan"
        required: true
        type: number

permissions:
  contents: read
  pull-requests: write

jobs:
  # On the runner, not in the container: the Infracost image ships git and
  # infracost-ci, not the gh CLI a workflow_dispatch run needs to resolve refs.
  infracost-pr:
    runs-on: ubuntu-latest
    outputs:
      base-ref: ${{ steps.pr.outputs.base-ref }}
      head-ref: ${{ steps.pr.outputs.head-ref }}
      pr-number: ${{ steps.pr.outputs.pr-number }}
    steps:
      - name: Get PR details
        id: pr
        env:
          GH_TOKEN: ${{ github.token }}
          INPUT_PR_NUMBER: ${{ inputs.pr-number }}
          PR_NUMBER: ${{ inputs.pr-number || github.event.pull_request.number }}
          BASE_REF: ${{ github.event.pull_request.base.ref }}
          HEAD_REF: ${{ github.event.pull_request.head.ref }}
        run: |
          if [ -n "$INPUT_PR_NUMBER" ]; then
            BASE_REF=$(gh pr view "$PR_NUMBER" --repo "$GITHUB_REPOSITORY" --json baseRefName -q .baseRefName)
            HEAD_REF=$(gh pr view "$PR_NUMBER" --repo "$GITHUB_REPOSITORY" --json headRefName -q .headRefName)
          fi
          echo "base-ref=${BASE_REF}" >> $GITHUB_OUTPUT
          echo "head-ref=${HEAD_REF}" >> $GITHUB_OUTPUT
          echo "pr-number=${PR_NUMBER}" >> $GITHUB_OUTPUT

  infracost-diff:
    needs: infracost-pr
    runs-on: ubuntu-latest
    container: ` + opts.image + `
    env:
      INFRACOST_CLI_AUTHENTICATION_TOKEN: ${{ secrets.` + opts.apiKeySecret + ` }}
      GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
      INFRACOST_VCS_PULL_REQUEST_ID: ${{ needs.infracost-pr.outputs.pr-number }}
    steps:
      - name: Checkout base branch
        if: github.event.action != 'closed'
        uses: actions/checkout@v4
        with:
          ref: ${{ needs.infracost-pr.outputs.base-ref }}
          path: base

      - name: Checkout head branch
        if: github.event.action != 'closed'
        uses: actions/checkout@v4
        with:
          ref: ${{ needs.infracost-pr.outputs.head-ref }}
          path: head

      - name: Run Infracost Diff
        if: github.event.action != 'closed'
        run: infracost-ci diff --base-path base --head-path head

      # Without this the dashboard leaves every merged pull request at OPEN.
      - name: Update pull request status
        if: github.event.action == 'closed'
        env:
          PR_STATUS: ${{ github.event.pull_request.merged && 'MERGED' || 'CLOSED' }}
        run: infracost-ci status --status "$PR_STATUS"
`
}

// githubScanWorkflowContent quotes the branch name: a branch may contain ] or
// , and would otherwise break the workflow or silently widen the trigger.
func githubScanWorkflowContent(opts ciJobOpts) string {
	return `name: Infracost Scan
` + ciFileMarker + `

on:
  push:
    branches: [` + strconv.Quote(opts.defaultBranch) + `]

permissions:
  contents: read

jobs:
  infracost-scan:
    runs-on: ubuntu-latest
    container: ` + opts.image + `
    env:
      INFRACOST_CLI_AUTHENTICATION_TOKEN: ${{ secrets.` + opts.apiKeySecret + ` }}
    steps:
      - name: Checkout
        uses: actions/checkout@v4

      - name: Run Infracost Scan
        run: infracost-ci scan --path .
`
}
