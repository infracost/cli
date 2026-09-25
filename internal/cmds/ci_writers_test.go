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

// gitlabV0Recipe is the .gitlab-ci.yml from the v0.1 docs page, beside a job
// of the user's own.
const gitlabV0Recipe = `stages:
  - test
  - infracost:merge-request-checks
  - infracost:default-branch-update

unit-tests:
  stage: test
  script:
    - make test

# Run Infracost on merge requests and comment with the diff.
infracost:merge-request-checks:
  stage: infracost:merge-request-checks
  image:
    name: infracost/infracost:ci-0.10
    entrypoint: [""]
  rules:
    - if: $CI_PIPELINE_SOURCE == "merge_request_event"
  variables:
    GIT_DEPTH: 0
  script:
    - git clone $CI_REPOSITORY_URL --branch=$CI_MERGE_REQUEST_TARGET_BRANCH_NAME --single-branch /tmp/base
    - infracost breakdown --path=/tmp/base/${TF_ROOT} --format=json --out-file=/tmp/infracost-base.json
    - infracost diff --path=${TF_ROOT} --compare-to=/tmp/infracost-base.json --format=json --out-file=/tmp/infracost.json
    - infracost comment gitlab --path=/tmp/infracost.json --repo=$CI_PROJECT_PATH --merge-request=$CI_MERGE_REQUEST_IID --gitlab-token=$GITLAB_TOKEN --behavior=update

infracost:default-branch-update:
  stage: infracost:default-branch-update
  image:
    name: infracost/infracost:ci-0.10
    entrypoint: [""]
  rules:
    - if: $CI_COMMIT_BRANCH == $CI_DEFAULT_BRANCH
  script:
    - infracost breakdown --path=${TF_ROOT} --format=json --out-file=/tmp/infracost.json
    - infracost upload --path=/tmp/infracost.json
`

func TestGitlabWriter_UpgradesTheV0Recipe(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, gitlabConfigPath, gitlabV0Recipe)

	plan := gitlabWriter{}.Upgrades(root)
	assert.Equal(t, []string{"infracost:merge-request-checks", "infracost:default-branch-update"}, ciJobNames(plan))

	results, err := gitlabWriter{}.Write(root, testJobOpts("gitlab.com"))
	require.NoError(t, err)
	assert.Equal(t, ciJobNames(plan), results[0].replaced)

	got := readFile(t, root, gitlabConfigPath)
	requireParses(t, got)
	assert.NotContains(t, got, "infracost/infracost:ci-0.10")
	assert.NotContains(t, got, "Run Infracost on merge requests")

	var parsed map[string]any
	require.NoError(t, yaml.Unmarshal([]byte(got), &parsed))
	assert.Contains(t, parsed, "unit-tests")
	assert.Contains(t, parsed, "infracost-diff")
	assert.Contains(t, parsed, "infracost-scan")
	assert.Equal(t, []any{"test"}, parsed["stages"])

	// The scope the old jobs ran under does not carry over, and /tmp is where
	// the recipe cloned the base branch rather than a scope the user chose.
	notes := strings.Join(results[0].notes, "\n")
	assert.Contains(t, notes, "The replaced job scanned $TF_ROOT")
	assert.NotContains(t, notes, "/tmp")
	assert.NotContains(t, notes, "infracostApiKey")
}

func TestGitlabWriter_UpgradesTheContainerRecipe(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, gitlabConfigPath, "unit-tests:\n  script:\n    - make test\n\ninfracost:\n  image: ghcr.io/infracost/ci:0.1\n  script:\n    - infracost-ci diff --base-path base --head-path head\n")

	results, err := gitlabWriter{}.Write(root, testJobOpts("gitlab.com"))
	require.NoError(t, err)
	assert.Equal(t, []string{"infracost"}, results[0].replaced)

	var parsed map[string]any
	got := readFile(t, root, gitlabConfigPath)
	require.NoError(t, yaml.Unmarshal([]byte(got), &parsed))
	assert.NotContains(t, parsed, "infracost")
	assert.Contains(t, parsed, "infracost-diff")
	assert.Contains(t, parsed, "unit-tests")
	assert.Empty(t, results[0].notes)
}

// A job named after us that runs something else is not a recipe we published,
// so it keeps the refusal rather than being deleted on a guess.
func TestGitlabWriter_RefusesAJobItDidNotPublish(t *testing.T) {
	root := t.TempDir()
	existing := "infracost-audit:\n  image: alpine\n  script:\n    - ./scripts/audit.sh\n"
	writeFile(t, root, gitlabConfigPath, existing)

	assert.Empty(t, gitlabWriter{}.Upgrades(root))

	_, err := gitlabWriter{}.Write(root, testJobOpts("gitlab.com"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), `already defines "infracost-audit"`)
	assert.Equal(t, existing, readFile(t, root, gitlabConfigPath))
}

// A job that runs Infracost under another name is left alone — deleting it
// would be guessing — but two jobs mean two comments, so it is called out.
func TestGitlabWriter_WarnsAboutAJobUnderAnotherName(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, gitlabConfigPath, "cost-check:\n  image: alpine\n  script:\n    - infracost-ci diff --base-path base --head-path head\n")

	results, err := gitlabWriter{}.Write(root, testJobOpts("gitlab.com"))
	require.NoError(t, err)
	assert.Empty(t, results[0].replaced)
	require.Len(t, results[0].warnings, 1)
	assert.Contains(t, results[0].warnings[0], "cost-check in .gitlab-ci.yml also runs Infracost")

	got := readFile(t, root, gitlabConfigPath)
	requireParses(t, got)
	assert.Contains(t, got, "cost-check:")
	assert.Contains(t, got, "infracost-diff:")
}

// azureV0Recipe is the azure-pipelines.yml from the v0.1 docs page. commentStep
// is where the Repos and GitHub-backed variants differ.
func azureV0Recipe(commentStep string) string {
	return `trigger:
  branches:
    include:
      - main

pr:
  branches:
    include:
      - main

jobs:
  - job: compile
    steps:
      - script: make

  # Infracost, from the docs.
  - job: infracost_pull_request_checks
    condition: eq(variables['Build.Reason'], 'PullRequest')
    pool:
      vmImage: ubuntu-latest
    steps:
      - task: InfracostSetup@2
        inputs:
          apiKey: $(infracostApiKey)
      - bash: |
          branch=$(System.PullRequest.TargetBranch)
          git clone $(Build.Repository.Uri) --branch=${branch#refs/heads/} --single-branch /tmp/base
      - bash: infracost breakdown --path=/tmp/base/$(TF_ROOT) --format=json --out-file=/tmp/infracost-base.json
      - bash: infracost diff --path=$(TF_ROOT) --compare-to=/tmp/infracost-base.json --format=json --out-file=/tmp/infracost.json
` + commentStep + `
  - job: infracost_cloud_update
    condition: ne(variables['Build.Reason'], 'PullRequest')
    steps:
      - task: InfracostSetup@2
        inputs:
          apiKey: $(infracostApiKey)
      - bash: infracost breakdown --path=$(TF_ROOT) --format=json --out-file=/tmp/infracost.json
      - bash: infracost upload --path=/tmp/infracost.json
`
}

func TestAzureWriter_UpgradesTheV0Recipe(t *testing.T) {
	tests := []struct {
		host        string
		commentStep string
		wantNote    string
	}{
		{
			host:        "dev.azure.com",
			commentStep: "      - bash: infracost comment azure-repos --path=/tmp/infracost.json --azure-access-token=$(System.AccessToken) --pull-request=$(System.PullRequest.PullRequestId) --repo-url=$(Build.Repository.Uri)\n",
			wantNote:    "The replaced job read $(infracostApiKey); the new one reads $(INFRACOST_API_KEY).",
		},
		{
			host:        "github.com",
			commentStep: "      - bash: infracost comment github --path=/tmp/infracost.json --github-token=$(githubToken) --pull-request=$(System.PullRequest.PullRequestNumber) --repo=$(Build.Repository.Name)\n",
			wantNote:    "The replaced job read $(githubToken); the new one reads $(GITHUB_TOKEN).",
		},
	}

	for _, tt := range tests {
		t.Run(tt.host, func(t *testing.T) {
			root := t.TempDir()
			writeFile(t, root, "azure-pipelines.yml", azureV0Recipe(tt.commentStep))

			plan := azureWriter{}.Upgrades(root)
			assert.Equal(t, []string{"infracost_pull_request_checks", "infracost_cloud_update"}, ciJobNames(plan))

			results, err := azureWriter{}.Write(root, testJobOpts(tt.host))
			require.NoError(t, err)
			assert.Equal(t, ciJobNames(plan), results[0].replaced)

			got := readFile(t, root, "azure-pipelines.yml")
			requireParses(t, got)
			assert.NotContains(t, got, "InfracostSetup@2")
			assert.NotContains(t, got, "Infracost, from the docs")

			var parsed struct {
				Trigger any `yaml:"trigger"`
				PR      any `yaml:"pr"`
				Jobs    []struct {
					Job string `yaml:"job"`
				} `yaml:"jobs"`
			}
			require.NoError(t, yaml.Unmarshal([]byte(got), &parsed))
			assert.NotNil(t, parsed.Trigger)
			assert.NotNil(t, parsed.PR)
			require.Len(t, parsed.Jobs, 3)
			assert.Equal(t, "compile", parsed.Jobs[0].Job)
			assert.Equal(t, "infracost_diff", parsed.Jobs[1].Job)
			assert.Equal(t, "infracost_scan", parsed.Jobs[2].Job)

			notes := strings.Join(results[0].notes, "\n")
			assert.Contains(t, notes, tt.wantNote)
			assert.Contains(t, notes, "The replaced job scanned $(TF_ROOT)")
			assert.NotContains(t, notes, "/tmp")
		})
	}
}

func TestAzureWriter_UpgradesTheContainerRecipe(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "azure-pipelines.yml", "trigger:\n  - main\n\njobs:\n  - job: infracost\n    container: ghcr.io/infracost/ci:0.1\n    steps:\n      - checkout: self\n      - script: infracost-ci scan --path .\n")

	results, err := azureWriter{}.Write(root, testJobOpts("dev.azure.com"))
	require.NoError(t, err)
	assert.Equal(t, []string{"infracost"}, results[0].replaced)
	assert.Empty(t, results[0].notes)

	var parsed struct {
		Jobs []struct {
			Job string `yaml:"job"`
		} `yaml:"jobs"`
	}
	got := readFile(t, root, "azure-pipelines.yml")
	require.NoError(t, yaml.Unmarshal([]byte(got), &parsed))
	require.Len(t, parsed.Jobs, 2)
	assert.Equal(t, "infracost_diff", parsed.Jobs[0].Job)
}

// The plan is body-independent: ciCanReplaceSpan, asked with no body, reads
// every key in an existing managed block as foreign, and a plan built on it
// would list nothing while the write went on to delete a job the user was
// never shown.
func TestAzureWriter_PlanListsEveryJobTheWriteRemoves(t *testing.T) {
	root := t.TempDir()
	managed := "trigger:\n  - main\n\nstages:\n" + indentLines(ciManagedBlock(azureStageBlock(testJobOpts("dev.azure.com"))), 2) +
		"\njobs:\n  - job: infracost_cloud_update\n    steps:\n      - task: InfracostSetup@2\n        inputs:\n          apiKey: $(infracostApiKey)\n"
	writeFile(t, root, "azure-pipelines.yml", managed)

	// The trap: the span check alone would refuse this file outright.
	require.Error(t, ciCanReplaceSpan(managed, ""))

	plan := azureWriter{}.Upgrades(root)
	require.Equal(t, []string{"infracost_cloud_update"}, ciJobNames(plan))

	results, err := azureWriter{}.Write(root, testJobOpts("dev.azure.com"))
	require.NoError(t, err)
	assert.Equal(t, ciJobNames(plan), results[0].replaced)
	assert.NotContains(t, readFile(t, root, "azure-pipelines.yml"), "InfracostSetup@2")
}
