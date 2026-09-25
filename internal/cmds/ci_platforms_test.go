package cmds

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDetectCIPlatform_SingleConfigFile(t *testing.T) {
	tests := []struct {
		file string
		want string
	}{
		{".github/workflows/ci.yml", "GitHub Actions"},
		{".github/workflows/ci.yaml", "GitHub Actions"},
		{".gitlab-ci.yml", "GitLab CI"},
		{"azure-pipelines.yml", "Azure Pipelines"},
		{".azure-pipelines.yml", "Azure Pipelines"},
		{"bitbucket-pipelines.yml", "Bitbucket Pipelines"},
		{"Jenkinsfile", "Jenkins"},
	}

	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			// A remote that matches no platform, so only the file can decide.
			repo := repoInfo{host: "git.acme.com", owner: "acme", repo: "infra"}
			root := repoWithFiles(t, tt.file)

			got, err := resolveCIPlatform(root, repo, "")
			require.NoError(t, err)
			assert.Equal(t, tt.want, got.name)
		})
	}
}

func TestDetectCIPlatform_EmptyWorkflowDirDoesNotMatch(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".github", "workflows"), 0o755))

	assert.Empty(t, detectCIPlatforms(root))

	got, err := resolveCIPlatform(root, repoInfo{host: "git.acme.com"}, "")
	require.NoError(t, err)
	assert.Equal(t, "generic", got.id)
}

func TestDetectCIPlatform_NoMatchFallsToHostThenGeneric(t *testing.T) {
	tests := []struct {
		host string
		want string
	}{
		{"github.com", "github"},
		{"github.acme.com", "github"},
		// The port-443 SSH endpoints are the same hosts.
		{"ssh.github.com", "github"},
		{"altssh.bitbucket.org", "bitbucket"},
		{"gitlab.acme.com", "gitlab"},
		{"bitbucket.org", "bitbucket"},
		{"ssh.dev.azure.com", "azure"},
		{"acme.visualstudio.com", "azure"},
		{"git.acme.com", "generic"},
	}

	for _, tt := range tests {
		t.Run(tt.host, func(t *testing.T) {
			got, err := resolveCIPlatform(t.TempDir(), repoInfo{host: tt.host}, "")
			require.NoError(t, err)
			assert.Equal(t, tt.want, got.id)
		})
	}
}

func TestDetectCIPlatform_SeveralMatchesNeedAPlatformFlag(t *testing.T) {
	root := repoWithFiles(t, ".github/workflows/ci.yml", "Jenkinsfile")

	matched := detectCIPlatforms(root)
	require.Len(t, matched, 2)

	// nonInteractiveStdin is not needed: the test binary's stdin is not a TTY.
	_, err := resolveCIPlatform(root, repoInfo{host: "github.com"}, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--ci-platform")
	assert.Contains(t, err.Error(), "github, jenkins")

	got, err := resolveCIPlatform(root, repoInfo{host: "github.com"}, "jenkins")
	require.NoError(t, err)
	assert.Equal(t, "Jenkins", got.name)
}

func TestResolveCIPlatform_UnknownID(t *testing.T) {
	_, err := resolveCIPlatform(t.TempDir(), repoInfo{host: "github.com"}, "travis")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unknown CI platform "travis"`)
}

func TestCIPlatformDocsURLs(t *testing.T) {
	want := map[string]string{
		"github":    "https://www.infracost.io/docs/integrations/github_actions/",
		"gitlab":    "https://www.infracost.io/docs/integrations/gitlab_ci/",
		"azure":     "https://www.infracost.io/docs/integrations/azure_pipelines/",
		"bitbucket": "https://www.infracost.io/docs/integrations/bitbucket_pipelines/",
		"jenkins":   "https://www.infracost.io/docs/integrations/jenkins/",
		"generic":   "https://www.infracost.io/docs/integrations/cicd/",
	}

	require.Len(t, supportedCIPlatforms, len(want))
	for _, p := range supportedCIPlatforms {
		assert.Equal(t, want[p.id], p.docsURL(), p.id)
	}
}

// Jenkins is detected so the platform is named correctly, and never written to.
func TestJenkinsHasNoWriter(t *testing.T) {
	p, ok := ciPlatformByID("jenkins")
	require.True(t, ok)
	assert.Nil(t, p.writer)
}

func TestHasAppIntegration(t *testing.T) {
	for _, host := range []string{"github.com", "ssh.github.com", "gitlab.com", "dev.azure.com", "acme.visualstudio.com"} {
		assert.True(t, hasAppIntegration(host), host)
	}
	for _, host := range []string{"bitbucket.org", "github.acme.com", "gitlab.acme.com", "git.acme.com"} {
		assert.False(t, hasAppIntegration(host), host)
	}
}

func repoWithFiles(t *testing.T, paths ...string) string {
	t.Helper()
	root := t.TempDir()
	for _, p := range paths {
		full := filepath.Join(root, filepath.FromSlash(p))
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(t, os.WriteFile(full, []byte("# fixture\n"), 0o644))
	}
	return root
}
