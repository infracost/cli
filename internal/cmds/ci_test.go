package cmds_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/infracost/cli/internal/api/dashboard"
	"github.com/infracost/cli/internal/api/dashboard/mocks"
	"github.com/infracost/cli/internal/cmds"
	"github.com/infracost/cli/internal/config"
	"github.com/infracost/cli/pkg/auth"
	"golang.org/x/oauth2"
)

// ciTestConfig returns a config that authenticates via a pre-set token source
// (not AuthenticationToken), since ci setup blocks authentication tokens.
func ciTestConfig(t *testing.T, mockClient *mocks.MockClient) *config.Config {
	t.Helper()
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
	return &config.Config{
		Auth: auth.Config{
			ExternalConfig: auth.ExternalConfig{
				AuthenticationToken: "test-token",
			},
		},
	}
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

	fn()

	os.Stdout = old
	_ = w.Close()

	var buf bytes.Buffer
	_, err = io.Copy(&buf, r)
	require.NoError(t, err)

	return buf.String()
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
	assert.Contains(t, execErr.Error(), "INFRACOST_API_KEY")

	want := `
Scanning repository
✔  GitHub repository   acme-corp/platform-infra
✗  Infracost API key   not found

CI pipeline setup is currently in early access.
To get access, please contact a sales representative.

Already have a key? Set it as an environment variable:
  export INFRACOST_API_KEY=<your-key>
`
	assert.Equal(t, want, output)
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
		"✔  Git repository      acme-corp/platform-infra\n" +
		"✗  CI provider         GitLab detected — GitHub Actions only for now\n" +
		"\n" +
		"Run `infracost ci setup` to use the app integration instead.\n"
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
✔  Infracost API key   ready (from INFRACOST_API_KEY)
✔  Infracost org       Acme Corp
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

	want := `
Scanning repository
✔  GitHub repository   acme-corp/platform-infra
✔  Infracost API key   ready (from INFRACOST_API_KEY)
`
	assert.Equal(t, want, output)
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
✔  Infracost org      Acme Corp
✔  App integration already connected

This repository is already sending PR cost estimates.
To manage settings: https://dashboard.infracost.io/org/acme-corp/repos
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