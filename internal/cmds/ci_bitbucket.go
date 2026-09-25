package cmds

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

const bitbucketConfigPath = "bitbucket-pipelines.yml"

// bitbucketWriter nests its steps under the file's `pipelines:` key. Bitbucket
// has no top-level job list, so this is the insert-under-key primitive's case.
//
// The children of `pipelines:` are a fixed, small set — `default`, `branches`,
// `pull-requests`, `tags`, `custom` — so a repository that already uses one
// cannot be given a second without producing a duplicate key that stops the
// file parsing. Each half is written only if its key is free, and whatever is
// left over becomes an instruction. The image is named per step rather than at
// the top level, which would override the user's other pipelines.
type bitbucketWriter struct{}

func (bitbucketWriter) ConfigPaths(string) ([]string, error) {
	return []string{bitbucketConfigPath}, nil
}

func (bitbucketWriter) Write(repoRoot string, opts ciJobOpts) ([]ciWriteResult, error) {
	var parts []string
	if !bitbucketHasOwnKey(repoRoot, "pull-requests") {
		parts = append(parts, bitbucketPullRequestsBlock(opts))
	}
	if !bitbucketHasOwnKey(repoRoot, "branches") {
		parts = append(parts, bitbucketBranchesBlock(opts))
	}

	if len(parts) == 0 {
		block := ciManagedBlock(bitbucketPullRequestsBlock(opts) + "\n" + bitbucketBranchesBlock(opts))
		return []ciWriteResult{{
			path:   bitbucketConfigPath,
			block:  block,
			reason: "its pipelines: already defines both pull-requests: and branches:",
		}}, nil
	}

	// ciJobsNone: Bitbucket's recipes put their step under the user's own
	// pull-requests: key, so replacing one means replacing a pipeline they may
	// share with other steps.
	res, err := ciPlaceBlock(repoRoot, bitbucketConfigPath, "pipelines", strings.Join(parts, "\n"), ciJobsNone)
	if err != nil {
		return nil, err
	}
	return []ciWriteResult{res}, nil
}

func (bitbucketWriter) Steps(repoRoot string, opts ciJobOpts) []string {
	steps := []string{
		fmt.Sprintf("Add two secured repository variables on %s:", opts.repo.slug()),
		"",
		fmt.Sprintf("  %-17s your Infracost API key", opts.apiKeySecret),
		"  BITBUCKET_TOKEN   a repository or workspace access token with pull requests: write",
		"",
		"Bitbucket has no variable for the pull request title or author, so the diff",
		"reads them from the API with the same token. It fails without one.",
		"",
		fmt.Sprintf("  https://%s/%s/admin/pipelines/repository-variables", opts.repo.host, opts.repo.slug()),
	}

	if bitbucketHasOwnKey(repoRoot, "pull-requests") {
		steps = append(steps,
			"",
			"This repository already has a pull-requests: pipeline, and a second one",
			"would stop the file parsing. Add this step to the one you have:",
			"",
			"  infracost-ci diff --base-path base --head-path head",
		)
	}
	if bitbucketHasOwnKey(repoRoot, "branches") {
		steps = append(steps,
			"",
			"This repository already has a branches: pipeline, so the default-branch",
			fmt.Sprintf("scan was not added. Add a step to your %s branch that runs:", opts.defaultBranch),
			"",
			"  infracost-ci scan --path .",
		)
	}

	return append(steps,
		"",
		"Bitbucket clones shallow by default, which may not reach the destination",
		"branch. Add a top-level `clone:` with `depth: full` if you have not already.",
	)
}

// bitbucketHasOwnKey reports whether the user defines pipelines.<key> outside
// the managed block. Ours does not count, or a re-run would drop the half it
// wrote the first time.
func bitbucketHasOwnKey(repoRoot, key string) bool {
	b, err := os.ReadFile(filepath.Join(repoRoot, bitbucketConfigPath)) //nolint:gosec // G304: a registered config path under the repo root
	if err != nil {
		return false
	}

	start, end, hasBlock := findCIManagedSpan(strings.Split(string(b), "\n"))

	var root yaml.Node
	if err := yaml.Unmarshal(b, &root); err != nil || len(root.Content) == 0 {
		return false
	}
	pipelines := ciMappingUnderKey(root.Content[0], "pipelines")
	if pipelines == nil {
		return false
	}

	for i := 0; i+1 < len(pipelines.Content); i += 2 {
		k := pipelines.Content[i]
		if k.Value != key {
			continue
		}
		if hasBlock && k.Line-1 >= start && k.Line-1 < end {
			continue
		}
		return true
	}
	return false
}

// bitbucketRepoURL keys the dashboard on the host the repository actually lives
// on: bitbucket.acme.com is a Bitbucket platform too.
func bitbucketRepoURL(repo repoInfo) string {
	return "https://" + repo.host + "/$BITBUCKET_REPO_FULL_NAME"
}

// bitbucketPullRequestsBlock is the diff. BITBUCKET_PR_ID only exists in a
// pull-requests pipeline, which is why diff lives here.
func bitbucketPullRequestsBlock(opts ciJobOpts) string {
	return `pull-requests:
  '**':
    - step:
        name: Infracost diff
        image: ` + opts.image + `
        script:
          # BITBUCKET_GIT_HTTP_ORIGIN is http, which keys a second repository
          # on the dashboard. Set the https web URL instead.
          - export INFRACOST_VCS_REPOSITORY_URL="` + bitbucketRepoURL(opts.repo) + `"
          - export INFRACOST_CLI_AUTHENTICATION_TOKEN="$` + opts.apiKeySecret + `"
          # FETCH_HEAD, not origin/<branch>: the clone has no remote-tracking
          # ref for the destination branch. --, so a branch name beginning with
          # a dash is not read as an option.
          - git fetch origin -- "$BITBUCKET_PR_DESTINATION_BRANCH"
          - git worktree add base FETCH_HEAD
          - git worktree add head HEAD
          - infracost-ci diff --base-path base --head-path head
`
}

// bitbucketBranchesBlock quotes the branch name: unquoted, a branch called true
// or null becomes a boolean or a null key rather than its own name.
func bitbucketBranchesBlock(opts ciJobOpts) string {
	return `branches:
  ` + strconv.Quote(opts.defaultBranch) + `:
    - step:
        name: Infracost scan
        image: ` + opts.image + `
        script:
          - export INFRACOST_VCS_REPOSITORY_URL="` + bitbucketRepoURL(opts.repo) + `"
          - export INFRACOST_CLI_AUTHENTICATION_TOKEN="$` + opts.apiKeySecret + `"
          - infracost-ci scan --path .
`
}
