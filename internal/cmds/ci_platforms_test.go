package cmds

import (
	"os"
	"path/filepath"
	"strings"
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

// Azure clone URLs carry a project between the organization and the repository,
// and the dashboard keys the repository on all three.
func TestParseRemoteURL_Azure(t *testing.T) {
	tests := []struct {
		name   string
		remote string
		host   string
		slug   string
	}{
		{"https", "https://dev.azure.com/acme/platform/_git/infra", "dev.azure.com", "acme/platform/infra"},
		{"https with userinfo", "https://acme@dev.azure.com/acme/platform/_git/infra", "dev.azure.com", "acme/platform/infra"},
		{"https with .git", "https://dev.azure.com/acme/platform/_git/infra.git", "dev.azure.com", "acme/platform/infra"},
		// The project is left out of the URL when it is named after the repo.
		{"https without project", "https://dev.azure.com/acme/_git/infra", "dev.azure.com", "acme/infra/infra"},
		// An org may legitimately be called DefaultCollection.
		{"https org named DefaultCollection", "https://dev.azure.com/DefaultCollection/platform/_git/infra", "dev.azure.com", "DefaultCollection/platform/infra"},
		// The dashboard stores the decoded project name, not the encoded one.
		{"https with encoded project", "https://dev.azure.com/acme/My%20Project/_git/infra", "dev.azure.com", "acme/My Project/infra"},
		{"ssh", "git@ssh.dev.azure.com:v3/acme/platform/infra", "ssh.dev.azure.com", "acme/platform/infra"},
		{"legacy https", "https://acme.visualstudio.com/platform/_git/infra", "acme.visualstudio.com", "acme/platform/infra"},
		{"legacy https collection", "https://acme.visualstudio.com/DefaultCollection/platform/_git/infra", "acme.visualstudio.com", "acme/platform/infra"},
		{"legacy https without project", "https://acme.visualstudio.com/_git/infra", "acme.visualstudio.com", "acme/infra/infra"},
		{"legacy ssh", "acme@vs-ssh.visualstudio.com:v3/acme/platform/infra", "vs-ssh.visualstudio.com", "acme/platform/infra"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseRemoteURL(tt.remote)
			require.NoError(t, err)
			assert.Equal(t, tt.host, got.host)
			assert.Equal(t, tt.slug, got.slug())
			assert.Equal(t, hostAzure, vcsHostKind(got.host))
		})
	}
}

// The host guard, not the URL shape, is what keeps a self-hosted remote with a
// _git or v3 path segment from being read as Azure.
func TestParseAzureRemoteURL_RejectsNonAzureHosts(t *testing.T) {
	for _, remote := range []string{
		"https://gitlab.acme.com/group/_git/repo",
		"git@gitlab.acme.com:v3/group/sub/project.git",
	} {
		t.Run(remote, func(t *testing.T) {
			_, ok := parseAzureRemoteURL(remote)
			assert.False(t, ok)
		})
	}
}

// Adding the Azure forms must not change how an ordinary remote parses.
func TestParseRemoteURL_NonAzureUnaffected(t *testing.T) {
	tests := []struct {
		remote string
		host   string
		slug   string
	}{
		{"git@github.com:acme/infra.git", "github.com", "acme/infra"},
		{"https://gitlab.acme.com/acme/infra.git", "gitlab.acme.com", "acme/infra"},
		{"git@bitbucket.org:acme/infra.git", "bitbucket.org", "acme/infra"},
	}

	for _, tt := range tests {
		t.Run(tt.remote, func(t *testing.T) {
			got, err := parseRemoteURL(tt.remote)
			require.NoError(t, err)
			assert.Equal(t, tt.host, got.host)
			assert.Equal(t, tt.slug, got.slug())
		})
	}
}

// A remote may carry a token. repoInfo.host is printed, passed to gh and used
// to pick the platform, so the credential must never reach it (FIX-746).
func TestParseRemoteURL_DropsRemoteCredentials(t *testing.T) {
	tests := []struct {
		name   string
		remote string
		host   string
		slug   string
	}{
		{"github token", "https://x-access-token:ghp_SECRET@github.com/acme/infra.git", "github.com", "acme/infra"},
		{"gitlab token", "https://oauth2:glpat-SECRET@gitlab.acme.com/acme/infra.git", "gitlab.acme.com", "acme/infra"},
		{"basic auth", "https://user:pw@github.com/acme/infra", "github.com", "acme/infra"},
		{"user only", "https://acme@github.com/acme/infra.git", "github.com", "acme/infra"},
		{"azure token", "https://acme:pat-SECRET@dev.azure.com/acme/platform/_git/infra", "dev.azure.com", "acme/platform/infra"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseRemoteURL(tt.remote)
			require.NoError(t, err)
			assert.Equal(t, tt.host, got.host)
			assert.Equal(t, tt.slug, got.slug())
			assert.NotContains(t, got.hostSlug(), "SECRET")
			assert.NotContains(t, got.hostSlug(), "pw")
		})
	}
}

// FIX-746 binds every writer, not just the one that exists today: what a writer
// puts in a file the user commits must be a reference, never a value. Seeds the
// environment with sentinels and asserts none of them reach disk.
func TestCIWriters_NeverEmitSecretValues(t *testing.T) {
	const (
		apiKeyValue = "ik_SENTINEL_APIKEY"
		caFileValue = "/etc/pki/SENTINEL-private-ca.pem"
	)

	for _, platform := range supportedCIPlatforms {
		if platform.writer == nil {
			continue
		}

		t.Run(platform.id, func(t *testing.T) {
			t.Setenv(ciAPIKeySecret, apiKeyValue)
			t.Setenv("INFRACOST_CI_VCS_TLS_CA_CERT_FILE", caFileValue)
			t.Setenv("INFRACOST_CI_VCS_TLS_INSECURE_SKIP_VERIFY", "true")

			root := t.TempDir()
			opts := ciJobOpts{
				image:         ciImage,
				repo:          repoInfo{host: "github.com", owner: "acme", repo: "infra"},
				defaultBranch: "main",
				apiKeySecret:  ciAPIKeySecret,
			}

			results, err := platform.writer.Write(root, opts)
			require.NoError(t, err)
			require.NotEmpty(t, results)

			for _, r := range results {
				content := readFile(t, root, r.path)
				assert.NotContains(t, content, apiKeyValue, "%s holds the API key literally", r.path)
				assert.NotContains(t, content, caFileValue, "%s holds a TLS setting literally", r.path)
				assert.Contains(t, content, ciAPIKeySecret, "%s must reference the secret by name", r.path)
			}

			steps := strings.Join(platform.writer.Steps(root, opts), "\n")
			assert.NotContains(t, steps, apiKeyValue)
			assert.NotContains(t, steps, caFileValue)
		})
	}
}
