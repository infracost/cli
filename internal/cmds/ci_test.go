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
	cmd.SetArgs([]string{"setup", "--ci-pipeline", "--yes"})
	cmd.SetContext(context.Background())

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "INFRACOST_CLI_AUTHENTICATION_TOKEN")
}

func TestCISetup_PipelineNoAPIKey(t *testing.T) {
	dir := initGitRepo(t, "git@github.com:acme-corp/platform-infra.git")
	chdir(t, dir)
	t.Setenv("INFRACOST_API_KEY", "")

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

	require.Error(t, execErr)
	assert.Contains(t, execErr.Error(), "INFRACOST_API_KEY")
	assert.Contains(t, output, "✔  GitHub repository   acme-corp/platform-infra")
	assert.Contains(t, output, "✔  Infracost org       acme-corp")
	assert.Contains(t, output, "✗  Infracost API key   not found")
	assert.Contains(t, output, "To get an API key, visit your organization's CLI tokens page:")
	assert.Contains(t, output, "https://dashboard.infracost.io/org/acme-corp/settings/cli-tokens")
	assert.Contains(t, output, "export INFRACOST_API_KEY=<your-key>")
}

func TestCISetup_PipelineNonGitHub(t *testing.T) {
	dir := initGitRepo(t, "git@gitlab.com:acme-corp/platform-infra.git")
	chdir(t, dir)
	t.Setenv("INFRACOST_API_KEY", "test-api-key")

	cfg := ciTestConfig(t,mocks.NewMockClient(t))
	cmd := cmds.CI(cfg)
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetArgs([]string{"setup", "--ci-pipeline", "--yes"})
	cmd.SetContext(context.Background())

	var execErr error
	output := captureOutput(t, func() {
		execErr = cmd.Execute()
	})

	require.Error(t, execErr)
	assert.Contains(t, execErr.Error(), "GitLab")

	want := "\nScanning repository\n" +
		"  ✔  Git repository      acme-corp/platform-infra\n" +
		"  ✗  CI provider         GitLab detected — GitHub Actions only for now\n" +
		"\n" +
		"To use the app integration instead, run:\n" +
		"  infracost ci setup\n"
	assert.Equal(t, want, output)
}

func TestCISetup_PipelineSuccess(t *testing.T) {
	dir := initGitRepo(t, "git@github.com:acme-corp/platform-infra.git")
	chdir(t, dir)
	t.Setenv("INFRACOST_API_KEY", "test-api-key")
	restrictPATH(t)

	mockClient := mocks.NewMockClient(t)
	mockClient.EXPECT().
		CurrentUser(mock.Anything).
		Return(singleOrgUser(), nil)

	cfg := ciTestConfig(t,mockClient)
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

	want := `
Scanning repository
  ✔  GitHub repository   acme-corp/platform-infra
  ✔  Infracost org       acme-corp
  ✔  Infracost API key   ready (from INFRACOST_API_KEY)
  !  gh CLI              not found — secret will need to be set manually

This will:
  →  Create  .github/workflows/infracost-diff.yml
  →  Create  .github/workflows/infracost-scan.yml
  ✔  Created .github/workflows/infracost-diff.yml
  ✔  Created .github/workflows/infracost-scan.yml

One manual step remaining:
Set the API key as a GitHub secret:

  gh secret set INFRACOST_API_KEY --body "$INFRACOST_API_KEY" \
      --repo acme-corp/platform-infra

Or add it in GitHub:
  https://github.com/acme-corp/platform-infra/settings/secrets/actions/new

Done. Push this commit to see Infracost on your next PR:

  git add .github/workflows/infracost-diff.yml .github/workflows/infracost-scan.yml
  git commit -m "chore: add Infracost CI integration"
  git push
`
	assert.Equal(t, want, output)

	// Verify workflow files were created with correct content.
	diffContent, err := os.ReadFile(filepath.Join(dir, ".github", "workflows", "infracost-diff.yml"))
	require.NoError(t, err)
	assert.Contains(t, string(diffContent), "infracost/actions/diff@")
	assert.Contains(t, string(diffContent), "secrets.INFRACOST_API_KEY")

	scanContent, err := os.ReadFile(filepath.Join(dir, ".github", "workflows", "infracost-scan.yml"))
	require.NoError(t, err)
	assert.Contains(t, string(scanContent), "infracost/actions/scan@")
	assert.Contains(t, string(scanContent), "secrets.INFRACOST_API_KEY")
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

	cfg := ciTestConfig(t,mockClient)
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

	// --yes should silently overwrite existing workflows; output is the same as fresh setup.
	assert.Contains(t, output, "✔  Created .github/workflows/infracost-diff.yml")
	assert.Contains(t, output, "✔  Created .github/workflows/infracost-scan.yml")

	// Verify the old content was replaced.
	content, err := os.ReadFile(filepath.Join(workflowDir, "infracost-diff.yml"))
	require.NoError(t, err)
	assert.NotEqual(t, "old", string(content))
	assert.Contains(t, string(content), "infracost/actions/diff@")
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

	cfg := ciTestConfig(t,mockClient)
	cmd := cmds.CI(cfg)
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetArgs([]string{"setup", "--ci-pipeline", "--yes"})
	cmd.SetContext(context.Background())

	var execErr error
	output := captureOutput(t, func() {
		execErr = cmd.Execute()
	})

	require.Error(t, execErr)
	assert.Contains(t, execErr.Error(), "multiple organizations")

	assert.Contains(t, output, "✔  GitHub repository   acme-corp/platform-infra")
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

	cfg := ciTestConfig(t,mockClient)
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

	cfg := ciTestConfig(t,mockClient)
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
	assert.Contains(t, output, "✔  GitHub repository   acme-corp/platform-infra")
	assert.Contains(t, output, "✔  Created .github/workflows/infracost-diff.yml")
}
