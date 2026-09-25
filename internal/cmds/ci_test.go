package cmds_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/infracost/cli/internal/api/dashboard"
	"github.com/infracost/cli/internal/api/dashboard/mocks"
	"github.com/infracost/cli/internal/cmds"
	"github.com/infracost/cli/internal/config"
	"github.com/infracost/cli/pkg/auth"
	"github.com/infracost/cli/pkg/logging"
	"golang.org/x/oauth2"
)

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// stripANSI removes ANSI escape codes from s so test assertions can match
// plain text regardless of terminal coloring.
func stripANSI(s string) string {
	return ansiRe.ReplaceAllString(s, "")
}

// ciTestConfig returns a config that authenticates via a pre-set token source
// (not AuthenticationToken), since ci setup blocks authentication tokens.
func ciTestConfig(t *testing.T, mockClient *mocks.MockClient) *config.Config {
	t.Helper()
	nonInteractiveStdin(t)
	cfg := &config.Config{
		Dashboard: dashboard.Config{
			Client: func(_ *http.Client) dashboard.Client {
				return mockClient
			},
		},
	}
	cfg.Auth.SetTokenSource(oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "test-token"}))
	return cfg
}

// ciTestConfigWithAuthToken returns a config with AuthenticationToken set,
// for testing that ci setup rejects it.
func ciTestConfigWithAuthToken(t *testing.T) *config.Config {
	t.Helper()
	nonInteractiveStdin(t)
	return &config.Config{
		Auth: auth.Config{
			ExternalConfig: auth.ExternalConfig{
				AuthenticationToken: "test-token",
			},
		},
	}
}

// nonInteractiveStdin replaces os.Stdin with the read end of a closed pipe so
// that:
//   - os.Stdin.Stat() reports a pipe (not a char device), causing
//     ui.IsInteractive() to return false and skip huh/bubbletea prompts
//   - resolveOrg's TTY check likewise sees no char device and skips the
//     interactive org picker
//   - any direct reads from stdin get an immediate EOF
//
// We cannot use /dev/null because it is a character device on macOS, which
// would cause IsInteractive() to return true.
func nonInteractiveStdin(t *testing.T) {
	t.Helper()

	r, w, err := os.Pipe()
	require.NoError(t, err)
	_ = w.Close() // close write end so reads get EOF

	old := os.Stdin
	os.Stdin = r
	t.Cleanup(func() {
		os.Stdin = old
		_ = r.Close()
	})
}

func initGitRepo(t *testing.T, remoteURL string) string {
	t.Helper()
	dir := t.TempDir()

	gitCmd := exec.Command("git", "init")
	gitCmd.Dir = dir
	require.NoError(t, gitCmd.Run())

	gitCmd = exec.Command("git", "remote", "add", "origin", remoteURL)
	gitCmd.Dir = dir
	require.NoError(t, gitCmd.Run())

	return dir
}

func chdir(t *testing.T, dir string) {
	t.Helper()
	old, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(old) })
}

func captureOutput(t *testing.T, fn func()) string {
	t.Helper()

	r, w, err := os.Pipe()
	require.NoError(t, err)

	old := os.Stdout
	os.Stdout = w
	// UI status helpers (Success, Step, Warn, ...) write through the
	// logging output router so they coordinate with TUI spinners. Point
	// it at the same pipe so the captured output matches what users see.
	restore := logging.SetOutput(w)

	fn()

	restore()
	os.Stdout = old
	_ = w.Close()

	var buf bytes.Buffer
	_, err = io.Copy(&buf, r)
	require.NoError(t, err)

	return stripANSI(buf.String())
}

// restrictPATH sets PATH to only include git, ensuring tools like gh are not found.
func restrictPATH(t *testing.T) {
	t.Helper()

	gitPath, err := exec.LookPath("git")
	require.NoError(t, err)

	binDir := t.TempDir()
	require.NoError(t, os.Symlink(gitPath, filepath.Join(binDir, "git")))
	t.Setenv("PATH", binDir)
}

// pathWithFakeGH puts a stub gh ahead of the real PATH, recording its argv and
// stdin, so the secret path runs end to end without touching GitHub.
func pathWithFakeGH(t *testing.T, exitCode int) (argsFile, stdinFile string) {
	t.Helper()

	dir := t.TempDir()
	argsFile = filepath.Join(dir, "args")
	stdinFile = filepath.Join(dir, "stdin")

	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$@\" > \"" + argsFile + "\"\n" +
		"cat > \"" + stdinFile + "\"\n" +
		"exit " + strconv.Itoa(exitCode) + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o700))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	return argsFile, stdinFile
}

// freshSingleOrgClient is a second mock for tests that run the command twice;
// each Execute resolves the org again.
func freshSingleOrgClient(t *testing.T) *mocks.MockClient {
	t.Helper()
	c := mocks.NewMockClient(t)
	c.EXPECT().CurrentUser(mock.Anything).Return(singleOrgUser(), nil)
	return c
}

func singleOrgUser() dashboard.CurrentUser {
	return dashboard.CurrentUser{
		ID:    "user-1",
		Name:  "Alice",
		Email: "alice@acme.com",
		Organizations: []dashboard.Organization{
			{
				ID:   "org-1",
				Name: "Acme Corp",
				Slug: "acme-corp",
			},
		},
	}
}

func TestCISetup_RejectsAuthToken(t *testing.T) {
	dir := initGitRepo(t, "git@github.com:acme-corp/platform-infra.git")
	chdir(t, dir)

	cfg := ciTestConfigWithAuthToken(t)
	cmd := cmds.CI(cfg)
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetArgs([]string{"setup", "--pipeline", "--yes"})
	cmd.SetContext(context.Background())

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "INFRACOST_CLI_AUTHENTICATION_TOKEN")
}

// The key is only demanded when gh is there to consume it, so the fake gh is
// what makes this the failing case.
func TestCISetup_PipelineNoAPIKey(t *testing.T) {
	dir := initGitRepo(t, "git@github.com:acme-corp/platform-infra.git")
	chdir(t, dir)
	t.Setenv("INFRACOST_API_KEY", "")
	pathWithFakeGH(t, 0)

	mockClient := mocks.NewMockClient(t)
	mockClient.EXPECT().
		CurrentUser(mock.Anything).
		Return(singleOrgUser(), nil)

	cfg := ciTestConfig(t, mockClient)
	cmd := cmds.CI(cfg)
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetArgs([]string{"setup", "--pipeline", "--yes"})
	cmd.SetContext(context.Background())

	var execErr error
	output := captureOutput(t, func() {
		execErr = cmd.Execute()
	})

	require.Error(t, execErr)
	assert.Contains(t, execErr.Error(), "INFRACOST_API_KEY")
	assert.Contains(t, output, "✔  Git repository      acme-corp/platform-infra")
	assert.Contains(t, output, "✔  CI platform         GitHub Actions")
	assert.Contains(t, output, "✔  Infracost org       acme-corp")
	assert.Contains(t, output, "✗  Infracost API key   not found")
	assert.Contains(t, output, "To get an API key, visit your organization's CLI tokens page:")
	assert.Contains(t, output, "https://dashboard.infracost.io/org/acme-corp/settings/cli-tokens")
	assert.Contains(t, output, "export INFRACOST_API_KEY=<your-key>")
}

// A GitLab repository is named and routed to its recipe rather than refused:
// returning an error here is the bug this replaced.
func TestCISetup_PipelineNoWriterYet(t *testing.T) {
	dir := initGitRepo(t, "git@gitlab.com:acme-corp/platform-infra.git")
	chdir(t, dir)
	t.Setenv("INFRACOST_API_KEY", "test-api-key")

	mockClient := mocks.NewMockClient(t)
	mockClient.EXPECT().
		CurrentUser(mock.Anything).
		Return(singleOrgUser(), nil)

	cfg := ciTestConfig(t, mockClient)
	cmd := cmds.CI(cfg)
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetArgs([]string{"setup", "--pipeline", "--yes"})
	cmd.SetContext(context.Background())

	var execErr error
	output := captureOutput(t, func() {
		execErr = cmd.Execute()
	})

	require.NoError(t, execErr)

	want := `
Scanning repository
  ✔  Git repository      acme-corp/platform-infra
  ✔  CI platform         GitLab CI
  ✔  Infracost org       acme-corp

Set up GitLab CI by hand:

  https://www.infracost.io/docs/integrations/gitlab_ci/

The recipe needs two secrets:
  →  INFRACOST_CLI_AUTHENTICATION_TOKEN — get a key at
     https://dashboard.infracost.io/org/acme-corp/settings/cli-tokens
  →  A VCS token the job comments with — the recipe names the one your platform uses
`
	assert.Equal(t, want, output)

	// Nothing was configured, so the "Setup complete" card must not appear.
	assert.NotContains(t, output, "Setup complete.")
}

// A Jenkinsfile is detected, named, routed to the docs, and left alone.
func TestCISetup_PipelineJenkinsIsNeverWritten(t *testing.T) {
	dir := initGitRepo(t, "git@github.com:acme-corp/platform-infra.git")
	chdir(t, dir)

	jenkinsfile := filepath.Join(dir, "Jenkinsfile")
	require.NoError(t, os.WriteFile(jenkinsfile, []byte("pipeline { agent any }\n"), 0o644))

	mockClient := mocks.NewMockClient(t)
	mockClient.EXPECT().
		CurrentUser(mock.Anything).
		Return(singleOrgUser(), nil)

	cfg := ciTestConfig(t, mockClient)
	cmd := cmds.CI(cfg)
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetArgs([]string{"setup", "--pipeline", "--yes"})
	cmd.SetContext(context.Background())

	var execErr error
	output := captureOutput(t, func() {
		execErr = cmd.Execute()
	})

	require.NoError(t, execErr)
	assert.Contains(t, output, "✔  CI platform         Jenkins")
	assert.Contains(t, output, "https://www.infracost.io/docs/integrations/jenkins/")

	content, err := os.ReadFile(jenkinsfile)
	require.NoError(t, err)
	assert.Equal(t, "pipeline { agent any }\n", string(content))

	assert.NoDirExists(t, filepath.Join(dir, ".github"))
}

// The deprecated flag name keeps working for scripts that already use it.
func TestCISetup_DeprecatedCIPipelineFlag(t *testing.T) {
	dir := initGitRepo(t, "git@gitlab.com:acme-corp/platform-infra.git")
	chdir(t, dir)

	mockClient := mocks.NewMockClient(t)
	mockClient.EXPECT().
		CurrentUser(mock.Anything).
		Return(singleOrgUser(), nil)

	cfg := ciTestConfig(t, mockClient)
	cmd := cmds.CI(cfg)
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetArgs([]string{"setup", "--ci-pipeline", "--yes"})
	cmd.SetContext(context.Background())

	var execErr error
	output := captureOutput(t, func() {
		execErr = cmd.Execute()
	})

	require.NoError(t, execErr)
	assert.Contains(t, output, "✔  CI platform         GitLab CI")
}

// Bitbucket has no app integration, so plain `ci setup` goes to pipeline mode
// instead of opening a dashboard page that cannot connect the repository.
func TestCISetup_BitbucketSkipsTheAppPitch(t *testing.T) {
	dir := initGitRepo(t, "git@bitbucket.org:acme-corp/platform-infra.git")
	chdir(t, dir)

	mockClient := mocks.NewMockClient(t)
	mockClient.EXPECT().
		CurrentUser(mock.Anything).
		Return(singleOrgUser(), nil)

	cfg := ciTestConfig(t, mockClient)
	cmd := cmds.CI(cfg)
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetArgs([]string{"setup", "--yes"})
	cmd.SetContext(context.Background())

	var execErr error
	output := captureOutput(t, func() {
		execErr = cmd.Execute()
	})

	require.NoError(t, execErr)
	assert.Contains(t, output, "!  bitbucket.org has no Infracost app integration — setting up a CI pipeline instead.")
	assert.Contains(t, output, "✔  CI platform         Bitbucket Pipelines")
	assert.NotContains(t, output, "The recommended way to set up Infracost is the app integration.")
}

// --ci-platform alone implies pipeline mode, even on a host whose app
// integration would otherwise win.
func TestCISetup_PlatformFlagImpliesPipeline(t *testing.T) {
	dir := initGitRepo(t, "git@github.com:acme-corp/platform-infra.git")
	chdir(t, dir)

	// HasRepo is not mocked: reaching the app path fails the test.
	mockClient := mocks.NewMockClient(t)
	mockClient.EXPECT().
		CurrentUser(mock.Anything).
		Return(singleOrgUser(), nil)

	cfg := ciTestConfig(t, mockClient)
	cmd := cmds.CI(cfg)
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetArgs([]string{"setup", "--ci-platform", "gitlab", "--yes"})
	cmd.SetContext(context.Background())

	var execErr error
	output := captureOutput(t, func() {
		execErr = cmd.Execute()
	})

	require.NoError(t, execErr)
	assert.Contains(t, output, "✔  CI platform         GitLab CI")
	assert.NotContains(t, output, "The recommended way to set up Infracost is the app integration.")
	assert.NoDirExists(t, filepath.Join(dir, ".github"))
}

// Without --yes the run stops at the confirmation, which a non-interactive
// terminal cannot answer — and nothing is written.
func TestCISetup_PipelineWithoutYesNeedsConfirmation(t *testing.T) {
	dir := initGitRepo(t, "git@github.com:acme-corp/platform-infra.git")
	chdir(t, dir)
	t.Setenv("INFRACOST_API_KEY", "test-api-key")
	restrictPATH(t)

	mockClient := mocks.NewMockClient(t)
	mockClient.EXPECT().
		CurrentUser(mock.Anything).
		Return(singleOrgUser(), nil)

	cfg := ciTestConfig(t, mockClient)
	cmd := cmds.CI(cfg)
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetArgs([]string{"setup", "--pipeline"})
	cmd.SetContext(context.Background())

	var execErr error
	output := captureOutput(t, func() {
		execErr = cmd.Execute()
	})

	require.Error(t, execErr)
	assert.Contains(t, execErr.Error(), "cannot confirm in a non-interactive terminal")
	assert.Contains(t, output, "→  Create  .github/workflows/infracost-diff.yml")
	assert.NotContains(t, output, "✔  Created")
	assert.NoDirExists(t, filepath.Join(dir, ".github", "workflows"))
}

// With gh absent the key is never read, so it is neither demanded nor reported.
func TestCISetup_PipelineSuccess(t *testing.T) {
	dir := initGitRepo(t, "git@github.com:acme-corp/platform-infra.git")
	chdir(t, dir)
	t.Setenv("INFRACOST_API_KEY", "test-api-key")
	restrictPATH(t)

	mockClient := mocks.NewMockClient(t)
	mockClient.EXPECT().
		CurrentUser(mock.Anything).
		Return(singleOrgUser(), nil)

	cfg := ciTestConfig(t, mockClient)
	cmd := cmds.CI(cfg)
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetArgs([]string{"setup", "--pipeline", "--yes"})
	cmd.SetContext(context.Background())

	var execErr error
	output := captureOutput(t, func() {
		execErr = cmd.Execute()
	})

	require.NoError(t, execErr)

	want := `
Scanning repository
  ✔  Git repository      acme-corp/platform-infra
  ✔  CI platform         GitHub Actions
  ✔  Infracost org       acme-corp

This will:
  →  Create  .github/workflows/infracost-diff.yml
  →  Create  .github/workflows/infracost-scan.yml
  ✔  Created .github/workflows/infracost-diff.yml
  ✔  Created .github/workflows/infracost-scan.yml

Remaining manual steps:
Set the API key as a GitHub secret:

  printf '%s' "$INFRACOST_API_KEY" | gh secret set INFRACOST_API_KEY \
      --repo github.com/acme-corp/platform-infra

Or add it in GitHub:
  https://github.com/acme-corp/platform-infra/settings/secrets/actions/new

Done. Push this commit to see Infracost on your next PR:

  git add .github/workflows/infracost-diff.yml .github/workflows/infracost-scan.yml
  git commit -m "chore: add Infracost CI integration"
  git push

     ╭──────────────────────────────────────────────────────────────────────╮
     │                                                                      │
     │  Setup complete.                                                     │
     │                                                                      │
     │  What's next?                                                        │
     │    → Open a pull request that changes your infrastructure —          │
     │    Infracost will comment with the cost diff                         │
     │                                                                      │
     ╰──────────────────────────────────────────────────────────────────────╯
`
	assert.Equal(t, want, output)

	// Verify workflow files were created with correct content.
	diffContent, err := os.ReadFile(filepath.Join(dir, ".github", "workflows", "infracost-diff.yml"))
	require.NoError(t, err)
	diffWorkflow := string(diffContent)
	assert.Contains(t, diffWorkflow, "container: ghcr.io/infracost/ci:0.1")
	assert.Contains(t, diffWorkflow, "infracost-ci diff --base-path base --head-path head")
	assert.Contains(t, diffWorkflow, "INFRACOST_CLI_AUTHENTICATION_TOKEN: ${{ secrets.INFRACOST_API_KEY }}")
	assert.Contains(t, diffWorkflow, "infracost-ci status --status \"$PR_STATUS\"")
	assert.Contains(t, diffWorkflow, "INFRACOST_VCS_PULL_REQUEST_ID:")
	assert.NotContains(t, diffWorkflow, "infracost/actions/")

	// The gh lookup stays on the runner: the container has no gh CLI.
	assert.Contains(t, diffWorkflow, "INPUT_PR_NUMBER: ${{ inputs.pr-number }}")
	assert.Contains(t, diffWorkflow, "if [ -n \"$INPUT_PR_NUMBER\" ]; then")
	assert.NotContains(t, diffWorkflow, "if [ -n \"${{ inputs.pr-number }}\" ]; then")
	assert.NotContains(t, diffWorkflow, "BASE_REF=\"${{ github.event.pull_request.base.ref }}\"")

	scanContent, err := os.ReadFile(filepath.Join(dir, ".github", "workflows", "infracost-scan.yml"))
	require.NoError(t, err)
	assert.Contains(t, string(scanContent), "container: ghcr.io/infracost/ci:0.1")
	assert.Contains(t, string(scanContent), "infracost-ci scan --path .")
	assert.Contains(t, string(scanContent), "INFRACOST_CLI_AUTHENTICATION_TOKEN: ${{ secrets.INFRACOST_API_KEY }}")
	assert.NotContains(t, string(scanContent), "infracost/actions/")

	// Both files carry the block version FIX-745 upgrades from.
	assert.Contains(t, diffWorkflow, "# Managed by infracost ci setup v1")
	assert.Contains(t, string(scanContent), "# Managed by infracost ci setup v1")

	// Re-running with the same version rewrites nothing.
	before, err := os.Stat(filepath.Join(dir, ".github", "workflows", "infracost-diff.yml"))
	require.NoError(t, err)

	cmd = cmds.CI(ciTestConfig(t, freshSingleOrgClient(t)))
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetArgs([]string{"setup", "--pipeline", "--yes"})
	cmd.SetContext(context.Background())

	rerun := captureOutput(t, func() {
		execErr = cmd.Execute()
	})
	require.NoError(t, execErr)
	assert.Contains(t, rerun, "✔  Unchanged .github/workflows/infracost-diff.yml")
	assert.Contains(t, rerun, "Done. Your CI config is already up to date.")

	after, err := os.Stat(filepath.Join(dir, ".github", "workflows", "infracost-diff.yml"))
	require.NoError(t, err)
	assert.Equal(t, before.ModTime(), after.ModTime())
}

// The whole secret path through the command: gh is on PATH, so the key is
// required, the secret is set from stdin, and no manual steps are printed.
func TestCISetup_PipelineSetsTheSecret(t *testing.T) {
	dir := initGitRepo(t, "git@github.com:acme-corp/platform-infra.git")
	chdir(t, dir)
	t.Setenv("INFRACOST_API_KEY", "ik_supersecret")
	argsFile, stdinFile := pathWithFakeGH(t, 0)

	cfg := ciTestConfig(t, freshSingleOrgClient(t))
	cmd := cmds.CI(cfg)
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetArgs([]string{"setup", "--pipeline", "--yes"})
	cmd.SetContext(context.Background())

	var execErr error
	output := captureOutput(t, func() {
		execErr = cmd.Execute()
	})

	require.NoError(t, execErr)
	assert.Contains(t, output, "✔  Infracost API key   ready (from INFRACOST_API_KEY)")
	assert.Contains(t, output, "→  Set     INFRACOST_API_KEY secret on acme-corp/platform-infra")
	assert.Contains(t, output, "✔  Set INFRACOST_API_KEY secret")
	assert.NotContains(t, output, "Remaining manual steps:")
	assert.Contains(t, output, "Setup complete.")

	args, err := os.ReadFile(argsFile)
	require.NoError(t, err)
	assert.Equal(t, "secret\nset\nINFRACOST_API_KEY\n--repo\ngithub.com/acme-corp/platform-infra\n", string(args))

	stdin, err := os.ReadFile(stdinFile)
	require.NoError(t, err)
	assert.Equal(t, "ik_supersecret", string(stdin))
}

// A secret we tried and failed to set is not a completed setup, so the card
// that says so must not print.
func TestCISetup_PipelineFailedSecretSkipsTheCard(t *testing.T) {
	dir := initGitRepo(t, "git@github.com:acme-corp/platform-infra.git")
	chdir(t, dir)
	t.Setenv("INFRACOST_API_KEY", "ik_supersecret")
	pathWithFakeGH(t, 1)

	cfg := ciTestConfig(t, freshSingleOrgClient(t))
	cmd := cmds.CI(cfg)
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetArgs([]string{"setup", "--pipeline", "--yes"})
	cmd.SetContext(context.Background())

	var execErr error
	output := captureOutput(t, func() {
		execErr = cmd.Execute()
	})

	require.NoError(t, execErr)
	assert.Contains(t, output, "Failed to set the INFRACOST_API_KEY secret")
	assert.Contains(t, output, "Remaining manual steps:")
	assert.NotContains(t, output, "Setup complete.")
}

// A self-hosted GitHub host has no app integration, so plain `ci setup` writes
// the workflows instead of opening a dashboard page that cannot connect it.
func TestCISetup_SelfHostedGitHubSkipsTheAppPitch(t *testing.T) {
	dir := initGitRepo(t, "git@github.acme.com:acme-corp/platform-infra.git")
	chdir(t, dir)
	t.Setenv("INFRACOST_API_KEY", "")
	restrictPATH(t)

	cfg := ciTestConfig(t, freshSingleOrgClient(t))
	cmd := cmds.CI(cfg)
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetArgs([]string{"setup", "--yes"})
	cmd.SetContext(context.Background())

	var execErr error
	output := captureOutput(t, func() {
		execErr = cmd.Execute()
	})

	// No gh and no API key still configures the repository.
	require.NoError(t, execErr)
	assert.Contains(t, output, "!  github.acme.com has no Infracost app integration — setting up a CI pipeline instead.")
	assert.Contains(t, output, "✔  CI platform         GitHub Actions")
	assert.Contains(t, output, "✔  Created .github/workflows/infracost-diff.yml")
	assert.NotContains(t, output, "The recommended way to set up Infracost is the app integration.")
	// The manual secret step keeps the self-hosted host.
	assert.Contains(t, output, "--repo github.acme.com/acme-corp/platform-infra")

	assert.FileExists(t, filepath.Join(dir, ".github", "workflows", "infracost-scan.yml"))
}

// Upgrading a repo that already has composite-action workflows keeps both
// filenames and job names, and deletes nothing.
func TestCISetup_PipelineMigratesCompositeActions(t *testing.T) {
	dir := initGitRepo(t, "git@github.com:acme-corp/platform-infra.git")
	chdir(t, dir)
	t.Setenv("INFRACOST_API_KEY", "test-api-key")
	restrictPATH(t)

	workflowDir := filepath.Join(dir, ".github", "workflows")
	require.NoError(t, os.MkdirAll(workflowDir, 0o755))
	for _, name := range []string{"infracost-diff.yml", "infracost-scan.yml"} {
		require.NoError(t, os.WriteFile(filepath.Join(workflowDir, name),
			[]byte("jobs:\n  infracost:\n    steps:\n      - uses: infracost/actions/diff@abc123\n"), 0o644))
	}

	cfg := ciTestConfig(t, freshSingleOrgClient(t))
	cmd := cmds.CI(cfg)
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetArgs([]string{"setup", "--pipeline", "--yes"})
	cmd.SetContext(context.Background())

	var execErr error
	output := captureOutput(t, func() {
		execErr = cmd.Execute()
	})
	require.NoError(t, execErr)

	assert.Contains(t, output, "✔  Updated .github/workflows/infracost-diff.yml")
	assert.Contains(t, output, "✔  Updated .github/workflows/infracost-scan.yml")

	entries, err := os.ReadDir(workflowDir)
	require.NoError(t, err)
	require.Len(t, entries, 2)

	for _, name := range []string{"infracost-diff.yml", "infracost-scan.yml"} {
		content, err := os.ReadFile(filepath.Join(workflowDir, name))
		require.NoError(t, err)
		assert.NotContains(t, string(content), "infracost/actions/")
		assert.Contains(t, string(content), "container: ghcr.io/infracost/ci:0.1")

		// Committed files keep their mode.
		info, err := os.Stat(filepath.Join(workflowDir, name))
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o644), info.Mode().Perm())
	}

	// The job names branch protection may be pinned to are unchanged.
	diff, err := os.ReadFile(filepath.Join(workflowDir, "infracost-diff.yml"))
	require.NoError(t, err)
	assert.Contains(t, string(diff), "\n  infracost-diff:\n")
	scan, err := os.ReadFile(filepath.Join(workflowDir, "infracost-scan.yml"))
	require.NoError(t, err)
	assert.Contains(t, string(scan), "\n  infracost-scan:\n")
}

func TestCISetup_PipelineExistingWorkflows(t *testing.T) {
	dir := initGitRepo(t, "git@github.com:acme-corp/platform-infra.git")
	chdir(t, dir)
	t.Setenv("INFRACOST_API_KEY", "test-api-key")
	restrictPATH(t)

	// Create an existing workflow file with stale content.
	workflowDir := filepath.Join(dir, ".github", "workflows")
	require.NoError(t, os.MkdirAll(workflowDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workflowDir, "infracost-diff.yml"), []byte("old"), 0o644))

	mockClient := mocks.NewMockClient(t)
	mockClient.EXPECT().
		CurrentUser(mock.Anything).
		Return(singleOrgUser(), nil)

	cfg := ciTestConfig(t, mockClient)
	cmd := cmds.CI(cfg)
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetArgs([]string{"setup", "--pipeline", "--yes"})
	cmd.SetContext(context.Background())

	var execErr error
	output := captureOutput(t, func() {
		execErr = cmd.Execute()
	})

	require.NoError(t, execErr)

	// --yes should silently overwrite existing workflows.
	assert.Contains(t, output, "✔  Updated .github/workflows/infracost-diff.yml")
	assert.Contains(t, output, "✔  Created .github/workflows/infracost-scan.yml")

	// Verify the old content was replaced.
	content, err := os.ReadFile(filepath.Join(workflowDir, "infracost-diff.yml"))
	require.NoError(t, err)
	assert.NotEqual(t, "old", string(content))
	assert.Contains(t, string(content), "container: ghcr.io/infracost/ci:0.1")
}

func TestCISetup_PipelineMultipleOrgs(t *testing.T) {
	dir := initGitRepo(t, "git@github.com:acme-corp/platform-infra.git")
	chdir(t, dir)
	t.Setenv("INFRACOST_API_KEY", "test-api-key")
	restrictPATH(t)

	mockClient := mocks.NewMockClient(t)
	mockClient.EXPECT().
		CurrentUser(mock.Anything).
		Return(dashboard.CurrentUser{
			ID:    "user-1",
			Name:  "Alice",
			Email: "alice@acme.com",
			Organizations: []dashboard.Organization{
				{ID: "org-1", Name: "Acme Corp", Slug: "acme-corp"},
				{ID: "org-2", Name: "Beta Inc", Slug: "beta-inc"},
			},
		}, nil)

	cfg := ciTestConfig(t, mockClient)
	cmd := cmds.CI(cfg)
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetArgs([]string{"setup", "--pipeline", "--yes"})
	cmd.SetContext(context.Background())

	var execErr error
	output := captureOutput(t, func() {
		execErr = cmd.Execute()
	})

	require.Error(t, execErr)
	assert.Contains(t, execErr.Error(), "no organization selected")
	assert.Contains(t, execErr.Error(), "acme-corp")
	assert.Contains(t, execErr.Error(), "beta-inc")
	assert.Contains(t, execErr.Error(), "--org")

	assert.Contains(t, output, "✔  Git repository      acme-corp/platform-infra")
	assert.NotContains(t, output, "Infracost API key")
}

func TestCISetup_AppAlreadyConnected(t *testing.T) {
	dir := initGitRepo(t, "git@github.com:acme-corp/platform-infra.git")
	chdir(t, dir)

	mockClient := mocks.NewMockClient(t)
	mockClient.EXPECT().
		CurrentUser(mock.Anything).
		Return(singleOrgUser(), nil)
	mockClient.EXPECT().
		HasRepo(mock.Anything, "org-1", "acme-corp/platform-infra").
		Return(true, nil)

	cfg := ciTestConfig(t, mockClient)
	cmd := cmds.CI(cfg)
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetArgs([]string{"setup"})
	cmd.SetContext(context.Background())

	var execErr error
	output := captureOutput(t, func() {
		execErr = cmd.Execute()
	})

	require.NoError(t, execErr)

	want := `
Scanning repository
  ✔  GitHub repository  acme-corp/platform-infra
  ✔  Infracost org      acme-corp
  ✔  App integration already connected

This repository is already sending PR cost estimates.
To manage settings, visit:
  https://dashboard.infracost.io/org/acme-corp/repos

     ╭──────────────────────────────────────────────────────────────────────╮
     │                                                                      │
     │  Setup complete.                                                     │
     │                                                                      │
     │  What's next?                                                        │
     │    → Open a pull request that changes your infrastructure —          │
     │    Infracost will comment with the cost diff                         │
     │                                                                      │
     ╰──────────────────────────────────────────────────────────────────────╯
`
	assert.Equal(t, want, output)
}

func TestCISetup_PipelineHTTPS(t *testing.T) {
	dir := initGitRepo(t, "https://github.com/acme-corp/platform-infra.git")
	chdir(t, dir)
	t.Setenv("INFRACOST_API_KEY", "test-api-key")
	restrictPATH(t)

	mockClient := mocks.NewMockClient(t)
	mockClient.EXPECT().
		CurrentUser(mock.Anything).
		Return(singleOrgUser(), nil)

	cfg := ciTestConfig(t, mockClient)
	cmd := cmds.CI(cfg)
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetArgs([]string{"setup", "--pipeline", "--yes"})
	cmd.SetContext(context.Background())

	var execErr error
	output := captureOutput(t, func() {
		execErr = cmd.Execute()
	})

	require.NoError(t, execErr)
	assert.Contains(t, output, "✔  Git repository      acme-corp/platform-infra")
	assert.Contains(t, output, "✔  Created .github/workflows/infracost-diff.yml")
}
