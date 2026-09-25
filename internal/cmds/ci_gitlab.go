package cmds

import "fmt"

const gitlabConfigPath = ".gitlab-ci.yml"

// gitlabWriter splices two top-level jobs into the user's .gitlab-ci.yml. Jobs
// are top-level keys on GitLab, so this is the append primitive's case.
type gitlabWriter struct{}

func (gitlabWriter) ConfigPaths(string) ([]string, error) {
	return []string{gitlabConfigPath}, nil
}

func (gitlabWriter) Write(repoRoot string, opts ciJobOpts) ([]ciWriteResult, error) {
	res, err := ciPlaceBlock(repoRoot, gitlabConfigPath, "", gitlabJobBlock(opts))
	if err != nil {
		return nil, err
	}
	return []ciWriteResult{res}, nil
}

// Steps names GITLAB_TOKEN: GitLab has no built-in token that can post a note,
// so unlike GitHub this cannot be finished for the user.
func (gitlabWriter) Steps(_ string, opts ciJobOpts) []string {
	return []string{
		fmt.Sprintf("Add two masked CI/CD variables under Settings → CI/CD → Variables on %s:", opts.repo.slug()),
		"",
		fmt.Sprintf("  %-17s your Infracost API key", opts.apiKeySecret),
		"  GITLAB_TOKEN      a project or group access token with the api scope",
		"",
		"CI_JOB_TOKEN cannot post notes, so it is not a fallback for GITLAB_TOKEN.",
		"",
		fmt.Sprintf("  %s", gitlabVariablesURL(opts.repo)),
	}
}

func gitlabVariablesURL(repo repoInfo) string {
	return fmt.Sprintf("https://%s/%s/-/settings/ci_cd", repo.host, repo.slug())
}

// gitlabJobBlock is the diff job and the default-branch scan job, in one
// managed span: GitLab has a single config file to put anything in.
func gitlabJobBlock(opts ciJobOpts) string {
	return `infracost-diff:
  image: ` + opts.image + `
  rules:
    - if: $CI_PIPELINE_SOURCE == "merge_request_event"
  variables:
    # Full history: the shallow default may not contain the target branch.
    GIT_DEPTH: 0
    INFRACOST_CLI_AUTHENTICATION_TOKEN: $` + opts.apiKeySecret + `
  script:
    # --, so a branch name beginning with a dash is not read as an option.
    - git fetch origin -- "$CI_MERGE_REQUEST_TARGET_BRANCH_NAME"
    - git worktree add base "origin/$CI_MERGE_REQUEST_TARGET_BRANCH_NAME"
    - git worktree add head HEAD
    - infracost-ci diff --base-path base --head-path head

infracost-scan:
  image: ` + opts.image + `
  rules:
    - if: $CI_COMMIT_BRANCH == "` + opts.defaultBranch + `"
  variables:
    INFRACOST_CLI_AUTHENTICATION_TOKEN: $` + opts.apiKeySecret + `
  script:
    - infracost-ci scan --path .
`
}
