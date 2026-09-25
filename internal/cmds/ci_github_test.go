package cmds

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestGithubWorkflowsAreValidYAML(t *testing.T) {
	opts := ciJobOpts{
		image:         ciImage,
		repo:          repoInfo{host: "github.com", owner: "acme", repo: "infra"},
		defaultBranch: "main",
		apiKeySecret:  ciAPIKeySecret,
	}

	for name, content := range map[string]string{
		githubDiffWorkflowPath: githubDiffWorkflowContent(opts),
		githubScanWorkflowPath: githubScanWorkflowContent(opts),
	} {
		t.Run(name, func(t *testing.T) {
			var wf struct {
				Name string                    `yaml:"name"`
				Jobs map[string]map[string]any `yaml:"jobs"`
			}
			require.NoError(t, yaml.Unmarshal([]byte(content), &wf))
			assert.NotEmpty(t, wf.Name)

			for job, body := range wf.Jobs {
				if job == "infracost-pr" {
					continue // resolves refs on the runner, where gh exists
				}
				assert.Equal(t, ciImage, body["container"], job)
			}
		})
	}
}

func TestGithubDiffWorkflowJobs(t *testing.T) {
	var wf struct {
		Jobs map[string]struct {
			Needs string `yaml:"needs"`
			Env   map[string]string
			Steps []map[string]any
		} `yaml:"jobs"`
	}
	require.NoError(t, yaml.Unmarshal([]byte(githubDiffWorkflowContent(ciJobOpts{
		image: ciImage, defaultBranch: "main", apiKeySecret: ciAPIKeySecret,
	})), &wf))

	require.Contains(t, wf.Jobs, "infracost-diff")
	diff := wf.Jobs["infracost-diff"]
	assert.Equal(t, "infracost-pr", diff.Needs)

	// Step-scoped, never job-scoped: a job-level token is handed to
	// actions/checkout and to every step a user adds later.
	for name, job := range wf.Jobs {
		assert.Empty(t, job.Env, "job %s must not hold secrets at job scope", name)
	}
	for _, step := range diff.Steps {
		env, _ := step["env"].(map[string]any)
		if step["uses"] != nil {
			assert.Nil(t, env["INFRACOST_CLI_AUTHENTICATION_TOKEN"], "checkout steps must not receive the Infracost token")
			assert.Nil(t, env["GITHUB_TOKEN"], "checkout steps must not receive the PR-write token")
			continue
		}
		if run, ok := step["run"].(string); ok && strings.Contains(run, "infracost-ci") {
			assert.Equal(t, "${{ secrets.INFRACOST_API_KEY }}", env["INFRACOST_CLI_AUTHENTICATION_TOKEN"])
			assert.Equal(t, "${{ secrets.GITHUB_TOKEN }}", env["GITHUB_TOKEN"])
		}
	}

	// The closed-PR path still reports the final state to the dashboard.
	var closedStep map[string]any
	for _, step := range diff.Steps {
		if step["if"] == "github.event.action == 'closed'" {
			closedStep = step
		}
	}
	require.NotNil(t, closedStep)
	assert.Contains(t, closedStep["run"], "infracost-ci status")
}

// A branch name may contain ], which unquoted ends the flow sequence early.
func TestGithubScanWorkflowQuotesTheBranch(t *testing.T) {
	content := githubScanWorkflowContent(ciJobOpts{
		image: ciImage, defaultBranch: "release]v1", apiKeySecret: ciAPIKeySecret,
	})

	var parsed any
	require.NoError(t, yaml.Unmarshal([]byte(content), &parsed))
	assert.Contains(t, content, `branches: ["release]v1"]`)
}

func TestGithubScanWorkflowScopesTheToken(t *testing.T) {
	var wf struct {
		Jobs map[string]struct {
			Env   map[string]string
			Steps []map[string]any
		} `yaml:"jobs"`
	}
	require.NoError(t, yaml.Unmarshal([]byte(githubScanWorkflowContent(ciJobOpts{
		image: ciImage, defaultBranch: "main", apiKeySecret: ciAPIKeySecret,
	})), &wf))

	for name, job := range wf.Jobs {
		assert.Empty(t, job.Env, "job %s must not hold secrets at job scope", name)
	}
	for _, step := range wf.Jobs["infracost-scan"].Steps {
		env, _ := step["env"].(map[string]any)
		if step["uses"] != nil {
			assert.Nil(t, env["INFRACOST_CLI_AUTHENTICATION_TOKEN"])
			continue
		}
		if run, ok := step["run"].(string); ok && strings.Contains(run, "infracost-ci") {
			assert.Equal(t, "${{ secrets.INFRACOST_API_KEY }}", env["INFRACOST_CLI_AUTHENTICATION_TOKEN"])
		}
	}
}

// A self-hosted remote must keep its host in --repo, or gh writes the key to
// the same-named repository on github.com.
func TestGithubSetSecret(t *testing.T) {
	opts := ciJobOpts{
		repo:         repoInfo{host: "github.acme.com", owner: "acme-corp", repo: "platform-infra"},
		apiKeySecret: ciAPIKeySecret,
	}

	t.Run("passes the host and the value on stdin", func(t *testing.T) {
		argsFile, stdinFile := fakeGH(t, 0, "")
		t.Setenv(ciAPIKeySecret, "ik_supersecret")

		require.NoError(t, githubWriter{}.SetSecret(context.Background(), opts))

		args := readLines(t, argsFile)
		assert.Equal(t, []string{"secret", "set", ciAPIKeySecret, "--repo", "github.acme.com/acme-corp/platform-infra"}, args)
		assert.NotContains(t, args, "--body")
		assert.NotContains(t, args, "ik_supersecret")

		body, err := os.ReadFile(stdinFile)
		require.NoError(t, err)
		assert.Equal(t, "ik_supersecret", string(body))
	})

	t.Run("surfaces gh's stderr", func(t *testing.T) {
		fakeGH(t, 1, "HTTP 404: Not Found")
		t.Setenv(ciAPIKeySecret, "ik_supersecret")

		err := githubWriter{}.SetSecret(context.Background(), opts)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "HTTP 404: Not Found")
	})
}

// fakeGH puts a gh on PATH that records its argv and stdin, then exits with
// exitCode after writing stderrMsg.
func fakeGH(t *testing.T, exitCode int, stderrMsg string) (argsFile, stdinFile string) {
	t.Helper()

	dir := t.TempDir()
	argsFile = filepath.Join(dir, "args")
	stdinFile = filepath.Join(dir, "stdin")

	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$@\" > \"" + argsFile + "\"\n" +
		"cat > \"" + stdinFile + "\"\n"
	if stderrMsg != "" {
		script += "echo '" + stderrMsg + "' >&2\n"
	}
	script += "exit " + strconv.Itoa(exitCode) + "\n"

	ghPath := filepath.Join(dir, "gh")
	require.NoError(t, os.WriteFile(ghPath, []byte(script), 0o700))
	// Prepended, not replacing: the script needs cat, and dir still wins for gh.
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	return argsFile, stdinFile
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	return strings.Split(strings.TrimRight(string(b), "\n"), "\n")
}
