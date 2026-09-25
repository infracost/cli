package cmds

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func testJobOpts(host string) ciJobOpts {
	return ciJobOpts{
		image:         ciImage,
		repo:          repoInfo{host: host, owner: "acme", repo: "infra"},
		defaultBranch: "main",
		apiKeySecret:  ciAPIKeySecret,
	}
}

func TestGitlabWriter_SplicesBesideExistingJobs(t *testing.T) {
	root := t.TempDir()
	existing := "stages:\n  - test\n\nunit-tests:\n  stage: test\n  script:\n    - make test\n"
	writeFile(t, root, gitlabConfigPath, existing)

	results, err := gitlabWriter{}.Write(root, testJobOpts("gitlab.com"))
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.False(t, results[0].created)

	got := readFile(t, root, gitlabConfigPath)
	assert.Contains(t, got, existing)

	var parsed map[string]any
	require.NoError(t, yaml.Unmarshal([]byte(got), &parsed))
	assert.Contains(t, parsed, "unit-tests")
	assert.Contains(t, parsed, "infracost-diff")
	assert.Contains(t, parsed, "infracost-scan")

	// A second run replaces the span rather than appending a second job.
	results, err = gitlabWriter{}.Write(root, testJobOpts("gitlab.com"))
	require.NoError(t, err)
	assert.True(t, results[0].unchanged)
	assert.Equal(t, got, readFile(t, root, gitlabConfigPath))
}

func TestBitbucketWriter_BranchesPipeline(t *testing.T) {
	t.Run("adds both pipelines to a file that uses neither key", func(t *testing.T) {
		root := t.TempDir()
		writeFile(t, root, bitbucketConfigPath, "image: atlassian/default-image:4\n\npipelines:\n  tags:\n    'v*':\n      - step:\n          script:\n            - make release\n")

		_, err := bitbucketWriter{}.Write(root, testJobOpts("bitbucket.org"))
		require.NoError(t, err)

		got := readFile(t, root, bitbucketConfigPath)
		requireParses(t, got)
		assert.Contains(t, got, "pull-requests:")
		assert.Contains(t, got, "branches:")
		assert.Contains(t, got, "infracost-ci scan --path .")
		assert.Contains(t, got, "tags:")

		steps := strings.Join(bitbucketWriter{}.Steps(root, testJobOpts("bitbucket.org")), "\n")
		assert.NotContains(t, steps, "already has a")
	})

	// Both keys taken leaves nothing to splice, so the block is printed instead.
	t.Run("refuses when both keys are taken", func(t *testing.T) {
		root := t.TempDir()
		content := "pipelines:\n  pull-requests:\n    '**':\n      - step:\n          script:\n            - make test\n  branches:\n    develop:\n      - step:\n          script:\n            - make build\n"
		writeFile(t, root, bitbucketConfigPath, content)

		results, err := bitbucketWriter{}.Write(root, testJobOpts("bitbucket.org"))
		require.NoError(t, err)
		assert.Contains(t, results[0].reason, "already defines both")
		assert.Contains(t, results[0].block, "infracost-ci diff")
		assert.Equal(t, content, readFile(t, root, bitbucketConfigPath))

		steps := strings.Join(bitbucketWriter{}.Steps(root, testJobOpts("bitbucket.org")), "\n")
		assert.Contains(t, steps, "already has a pull-requests: pipeline")
		assert.Contains(t, steps, "already has a branches: pipeline")
	})

	// A second branches: under pipelines: is a duplicate key that stops the
	// whole file parsing, so the scan pipeline becomes an instruction.
	t.Run("skips the scan when the user already has branches", func(t *testing.T) {
		root := t.TempDir()
		writeFile(t, root, bitbucketConfigPath, "pipelines:\n  branches:\n    develop:\n      - step:\n          script:\n            - make build\n")

		_, err := bitbucketWriter{}.Write(root, testJobOpts("bitbucket.org"))
		require.NoError(t, err)

		got := readFile(t, root, bitbucketConfigPath)
		requireParses(t, got)
		assert.Contains(t, got, "pull-requests:")
		assert.Contains(t, got, "infracost-ci diff")
		assert.NotContains(t, got, "infracost-ci scan")
		assert.Equal(t, 1, strings.Count(got, "branches:"))
		assert.NotContains(t, got, "develop:\n      - step:\n          name: Infracost")

		steps := strings.Join(bitbucketWriter{}.Steps(root, testJobOpts("bitbucket.org")), "\n")
		assert.Contains(t, steps, "already has a branches: pipeline")
		assert.Contains(t, steps, "infracost-ci scan --path .")
	})

	// Our own branches: must not make a re-run think the user has one.
	t.Run("re-run keeps the scan it wrote", func(t *testing.T) {
		root := t.TempDir()
		writeFile(t, root, bitbucketConfigPath, "image: atlassian/default-image:4\n")

		_, err := bitbucketWriter{}.Write(root, testJobOpts("bitbucket.org"))
		require.NoError(t, err)
		first := readFile(t, root, bitbucketConfigPath)

		results, err := bitbucketWriter{}.Write(root, testJobOpts("bitbucket.org"))
		require.NoError(t, err)
		assert.True(t, results[0].unchanged)
		assert.Equal(t, first, readFile(t, root, bitbucketConfigPath))
		assert.Contains(t, first, "infracost-ci scan --path .")
	})
}

func TestAzureWriter_Shapes(t *testing.T) {
	t.Run("creates a file with a trigger outside the block", func(t *testing.T) {
		root := t.TempDir()

		results, err := azureWriter{}.Write(root, testJobOpts("dev.azure.com"))
		require.NoError(t, err)
		assert.True(t, results[0].created)
		assert.Equal(t, "azure-pipelines.yml", results[0].path)

		got := readFile(t, root, "azure-pipelines.yml")
		requireParses(t, got)
		assert.Less(t, strings.Index(got, "trigger:"), strings.Index(got, ">>> infracost ci setup"))

		// A re-run finds jobs: and replaces in place, keeping the trigger.
		results, err = azureWriter{}.Write(root, testJobOpts("dev.azure.com"))
		require.NoError(t, err)
		assert.True(t, results[0].unchanged)
		assert.Equal(t, got, readFile(t, root, "azure-pipelines.yml"))
	})

	t.Run("nests a stage under stages", func(t *testing.T) {
		root := t.TempDir()
		writeFile(t, root, "azure-pipelines.yml", "trigger: none\n\nstages:\n  - stage: build\n    jobs:\n      - job: compile\n        steps:\n          - script: make\n")

		_, err := azureWriter{}.Write(root, testJobOpts("dev.azure.com"))
		require.NoError(t, err)

		got := readFile(t, root, "azure-pipelines.yml")
		requireParses(t, got)
		assert.Contains(t, got, "- stage: infracost")

		var parsed struct {
			Stages []struct {
				Stage string `yaml:"stage"`
			} `yaml:"stages"`
		}
		require.NoError(t, yaml.Unmarshal([]byte(got), &parsed))
		require.Len(t, parsed.Stages, 2)
		assert.Equal(t, "build", parsed.Stages[0].Stage)
		assert.Equal(t, "infracost", parsed.Stages[1].Stage)
	})

	t.Run("appends jobs to an existing jobs list", func(t *testing.T) {
		root := t.TempDir()
		writeFile(t, root, "azure-pipelines.yaml", "jobs:\n  - job: compile\n    steps:\n      - script: make\n")

		results, err := azureWriter{}.Write(root, testJobOpts("dev.azure.com"))
		require.NoError(t, err)
		// The existing file's name is used, not the canonical one.
		assert.Equal(t, "azure-pipelines.yaml", results[0].path)

		var parsed struct {
			Jobs []struct {
				Job string `yaml:"job"`
			} `yaml:"jobs"`
		}
		got := readFile(t, root, "azure-pipelines.yaml")
		require.NoError(t, yaml.Unmarshal([]byte(got), &parsed))
		require.Len(t, parsed.Jobs, 3)
		assert.Equal(t, "compile", parsed.Jobs[0].Job)
		assert.Equal(t, "infracost_diff", parsed.Jobs[1].Job)
		assert.Equal(t, "infracost_scan", parsed.Jobs[2].Job)
	})

	t.Run("refuses the shapes it cannot place into", func(t *testing.T) {
		tests := []struct {
			name    string
			content string
			reason  string
		}{
			{"extends a template", "extends:\n  template: shared.yml@templates\n", "extends a template"},
			{"steps at the top level", "trigger: none\nsteps:\n  - script: make\n", "top level"},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				root := t.TempDir()
				writeFile(t, root, "azure-pipelines.yml", tt.content)

				results, err := azureWriter{}.Write(root, testJobOpts("dev.azure.com"))
				require.NoError(t, err)
				assert.Contains(t, results[0].reason, tt.reason)
				assert.Contains(t, results[0].block, ciImage)
				assert.False(t, results[0].created)
				assert.Equal(t, tt.content, readFile(t, root, "azure-pipelines.yml"))
			})
		}
	})

	// BUILD_REPOSITORY_PROVIDER decides which API comments, so the recipe names
	// the token for the host the repository actually lives on.
	t.Run("picks the comment token by host", func(t *testing.T) {
		azureRoot, githubRoot := t.TempDir(), t.TempDir()

		_, err := azureWriter{}.Write(azureRoot, testJobOpts("dev.azure.com"))
		require.NoError(t, err)
		azure := readFile(t, azureRoot, "azure-pipelines.yml")
		assert.Contains(t, azure, "SYSTEM_ACCESSTOKEN: $(System.AccessToken)")
		assert.NotContains(t, azure, "GITHUB_TOKEN")

		_, err = azureWriter{}.Write(githubRoot, testJobOpts("github.com"))
		require.NoError(t, err)
		github := readFile(t, githubRoot, "azure-pipelines.yml")
		assert.Contains(t, github, "GITHUB_TOKEN: $(GITHUB_TOKEN)")
		assert.NotContains(t, github, "SYSTEM_ACCESSTOKEN")

		steps := strings.Join(azureWriter{}.Steps(githubRoot, testJobOpts("github.com")), "\n")
		assert.Contains(t, steps, "Pull requests: Read and write")
		assert.Contains(t, steps, "Make secrets available to builds of forks off")
	})
}

// Every writer's output must parse, and must not leave the file it was given
// worse than it found it.
func TestCIWriters_OutputParses(t *testing.T) {
	for _, platform := range supportedCIPlatforms {
		if platform.writer == nil {
			continue
		}

		t.Run(platform.id, func(t *testing.T) {
			root := t.TempDir()
			results, err := platform.writer.Write(root, testJobOpts("github.com"))
			require.NoError(t, err)
			require.NotEmpty(t, results)

			for _, r := range results {
				require.Empty(t, r.reason, "%s refused an empty repository", r.path)
				content := readFile(t, root, r.path)
				requireParses(t, content)
				assert.Contains(t, content, ciImage)
				assert.Equal(t, ciBlockVersion, ciConfigVersion(content))
			}
		})
	}
}
