package cmds

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"gopkg.in/yaml.v3"
)

// azureConfigPaths are the names Azure Pipelines looks for, most canonical
// first. Only one is ever written to.
var azureConfigPaths = []string{"azure-pipelines.yml", "azure-pipelines.yaml", ".azure-pipelines.yml"}

// azureWriter places its jobs under the file's own `jobs:` or `stages:` key.
// Azure files vary more in shape than the other platforms, so the shape is read
// before anything is written and the unplaceable shapes are refused by name.
type azureWriter struct{}

func (azureWriter) ConfigPaths(repoRoot string) ([]string, error) {
	for _, p := range azureConfigPaths {
		if fileExists(filepath.Join(repoRoot, p)) {
			return []string{p}, nil
		}
	}
	return []string{azureConfigPaths[0]}, nil
}

func (w azureWriter) Write(repoRoot string, opts ciJobOpts) ([]ciWriteResult, error) {
	paths, err := w.ConfigPaths(repoRoot)
	if err != nil {
		return nil, err
	}
	rel := paths[0]
	abs := filepath.Join(repoRoot, filepath.FromSlash(rel))

	// A file we create needs a trigger, which is the user's to edit and so
	// sits outside the managed block. A file they already have keeps theirs.
	if !fileExists(abs) {
		created, unchanged, err := writeCIConfigFile(abs, azureNewFile(opts))
		if err != nil {
			return nil, err
		}
		return []ciWriteResult{{path: rel, created: created, unchanged: unchanged}}, nil
	}

	content, err := os.ReadFile(abs) //nolint:gosec // G304: abs is a registered config path under the repo root
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", rel, err)
	}

	key, reason := azurePlacementKey(string(content))
	if reason != "" {
		return []ciWriteResult{{path: rel, block: ciManagedBlock(azureJobsBlock(opts)), reason: reason}}, nil
	}

	body := azureJobsBlock(opts)
	if key == "stages" {
		body = azureStageBlock(opts)
	}

	res, err := ciPlaceBlock(repoRoot, rel, key, body)
	if err != nil {
		return nil, err
	}
	return []ciWriteResult{res}, nil
}

// Steps names the token for the host the repository actually lives on: an
// Azure pipeline building a GitHub repository comments through GitHub.
func (azureWriter) Steps(_ string, opts ciJobOpts) []string {
	if vcsHostKind(opts.repo.host) == hostGitHub {
		return []string{
			"Add two pipeline variables, both marked Keep this value secret:",
			"",
			fmt.Sprintf("  %-17s your Infracost API key", opts.apiKeySecret),
			"  GITHUB_TOKEN     a fine-grained token for this repository with Pull requests: Read and write",
			"",
			"The pipeline builds a GitHub repository, so the comment goes through the",
			"GitHub API and System.AccessToken is not involved.",
			"",
			"Leave Make secrets available to builds of forks off, under Triggers →",
			"Pull request validation: it hands a stranger's branch both tokens.",
		}
	}

	return []string{
		fmt.Sprintf("Add one pipeline variable, marked Keep this value secret: %s.", opts.apiKeySecret),
		"",
		"Then let the build identity comment:",
		"  Project Settings → Repositories → this repo → Security, search Build Service,",
		"  set Contribute to pull requests to Allow.",
		"",
		"Azure Repos ignores a pr: trigger, so wire pull request builds up as a branch",
		"policy instead: Repos → Branches → your default branch → Branch policies →",
		"Build validation.",
	}
}

// azurePlacementKey reports the top-level key to nest under, or why the file's
// shape rules out a safe edit.
func azurePlacementKey(content string) (key, reason string) {
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(content), &root); err != nil || len(root.Content) == 0 {
		// An unparseable file is ciPlaceBlock's refusal to report, not ours.
		return "jobs", ""
	}
	doc := root.Content[0]
	if doc.Kind != yaml.MappingNode {
		return "jobs", ""
	}

	keys := map[string]bool{}
	for i := 0; i+1 < len(doc.Content); i += 2 {
		keys[doc.Content[i].Value] = true
	}

	switch {
	case keys["extends"]:
		return "", "it extends a template, so the jobs are defined elsewhere"
	case keys["stages"]:
		return "stages", ""
	case keys["jobs"]:
		return "jobs", ""
	case keys["steps"]:
		return "", "its steps are declared at the top level, where a container job cannot be added"
	default:
		return "jobs", ""
	}
}

// azureNewFile quotes the branch name: unquoted, a branch called true or null
// parses as a boolean or a null rather than as its own name.
func azureNewFile(opts ciJobOpts) string {
	return `# Created by infracost ci setup. The trigger is yours to edit.
trigger:
  branches:
    include:
      - ` + strconv.Quote(opts.defaultBranch) + `

jobs:
` + indentLines(ciManagedBlock(azureJobsBlock(opts)), 2)
}

// azureStageBlock wraps the jobs in a stage, for a file built from stages:.
func azureStageBlock(opts ciJobOpts) string {
	return "- stage: infracost\n  jobs:\n" + indentLines(azureJobsBlock(opts), 4)
}

// azureJobsBlock is the diff job and the default-branch scan job. Azure job
// names allow only letters, digits and underscores, so these are not hyphenated
// the way the other platforms' are.
func azureJobsBlock(opts ciJobOpts) string {
	return `- job: infracost_diff
  # diff is a pull request run: a build of the default branch has no target
  # branch to fetch.
  condition: eq(variables['Build.Reason'], 'PullRequest')
  container: ` + opts.image + `
  steps:
    - checkout: self
      fetchDepth: 0
      # The default drops the auth header, so the fetch below cannot reach a
      # private repository.
      persistCredentials: true
    - script: |
        BRANCH="${TARGET_BRANCH#refs/heads/}"
        # --, so a branch name beginning with a dash is not read as an option.
        git fetch origin -- "$BRANCH"
        git worktree add base "origin/$BRANCH"
        git worktree add head HEAD
      env:
        # Mapped, not written into the script: Azure substitutes $(...) into
        # the script text, where bash would parse the branch name as code.
        TARGET_BRANCH: $(System.PullRequest.TargetBranch)
    - script: infracost-ci diff --base-path base --head-path head
      env:
        INFRACOST_CLI_AUTHENTICATION_TOKEN: $(` + opts.apiKeySecret + `)
` + azureCommentTokenEnv(opts.repo) + `
- job: infracost_scan
  condition: and(ne(variables['Build.Reason'], 'PullRequest'), eq(variables['Build.SourceBranch'], 'refs/heads/` + opts.defaultBranch + `'))
  container: ` + opts.image + `
  steps:
    - checkout: self
    - script: infracost-ci scan --path .
      env:
        INFRACOST_CLI_AUTHENTICATION_TOKEN: $(` + opts.apiKeySecret + `)
`
}

// azureCommentTokenEnv picks the comment token by where the repository lives:
// BUILD_REPOSITORY_PROVIDER is what infracost-ci reads to tell them apart, so
// the recipe names one rather than declaring a provider.
func azureCommentTokenEnv(repo repoInfo) string {
	if vcsHostKind(repo.host) == hostGitHub {
		return "        GITHUB_TOKEN: $(GITHUB_TOKEN)\n"
	}
	return "        SYSTEM_ACCESSTOKEN: $(System.AccessToken)\n"
}
