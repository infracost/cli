package events

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// scrubCIEnv drops every variable getCIPlatform consults, so the result does
// not depend on the CI the suite happens to run in.
func scrubCIEnv() []string {
	out := make([]string, 0, len(os.Environ()))
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if _, ok := ciPlatformEnvs[key]; ok {
			continue
		}
		matched := false
		for _, other := range ciPlatformOtherEnvs {
			if key == other {
				matched = true
			}
		}
		for prefix := range ciPlatformEnvPrefixes {
			if strings.HasPrefix(key, prefix) {
				matched = true
			}
		}
		if !matched {
			out = append(out, entry)
		}
	}
	return out
}

func TestNormalizeCIPlatform(t *testing.T) {
	tests := []struct {
		name   string
		raw    string
		source ciPlatformSource
		want   string
	}{
		{name: "absent", want: ""},

		// Booleans and their spellings mean "in CI"/"not in CI", never a platform.
		{name: "azure without provider", raw: "azure_devops_", source: ciPlatformDetected, want: "unknown"},
		{name: "boolean fallback", raw: "true", source: ciPlatformCIVar, want: "unknown"},
		{name: "numeric boolean fallback", raw: "1", source: ciPlatformCIVar, want: "unknown"},
		{name: "falsey is not a platform", raw: "false", source: ciPlatformCIVar, want: ""},
		{name: "numeric falsey is not a platform", raw: "0", source: ciPlatformCIVar, want: ""},
		{name: "off is not a platform", raw: "off", source: ciPlatformCIVar, want: ""},
		{name: "no is not a platform", raw: "no", source: ciPlatformCIVar, want: ""},
		{name: "yes is in CI", raw: "yes", source: ciPlatformCIVar, want: "unknown"},
		{name: "on is in CI", raw: "on", source: ciPlatformCIVar, want: "unknown"},

		// Our own tooling sets the override, so a name we do not detect survives.
		// tfe only ever arrives this way: the dashboard's TFC run-task job sets
		// it, and nothing in detection produces it.
		{name: "producer names tfe", raw: "tfe", source: ciPlatformProducer, want: "tfe"},
		{name: "producer names tfc", raw: "tfc", source: ciPlatformProducer, want: "tfc"},
		{name: "producer names an undetectable CI", raw: "Woodpecker", source: ciPlatformProducer, want: "woodpecker"},
		{name: "producer names a partner integration", raw: "jenkins_plugin", source: ciPlatformProducer, want: "jenkins_plugin"},

		// Shape still bounds the producer: it is an env var anyone can set.
		{name: "producer path is not a label", raw: "/Users/owen/code/acme", source: ciPlatformProducer, want: "unknown"},
		{name: "producer url is not a label", raw: "https://ci.acme.internal/job/x", source: ciPlatformProducer, want: "unknown"},
		{name: "producer whitespace is not a label", raw: "my ci runner", source: ciPlatformProducer, want: "unknown"},
		{name: "producer overlong is not a label", raw: strings.Repeat("a", 49), source: ciPlatformProducer, want: "unknown"},
		{name: "producer at the length cap", raw: strings.Repeat("a", 48), source: ciPlatformProducer, want: strings.Repeat("a", 48)},

		// Detection produces its own labels, so they are already bounded.
		{name: "detected platform", raw: "GitHub_Actions", source: ciPlatformDetected, want: "github_actions"},
		{name: "detected azure with provider", raw: "azure_devops_TfsGit", source: ciPlatformDetected, want: "azure_devops_tfsgit"},

		// The bare CI variable is user data: it must name something we know.
		{name: "CI names a known platform", raw: "circleci", source: ciPlatformCIVar, want: "circleci"},
		{name: "CI names an internal runner", raw: "acme-prod-internal", source: ciPlatformCIVar, want: "unknown"},
		{name: "CI holds a username", raw: "owen", source: ciPlatformCIVar, want: "unknown"},
		{name: "CI holds a token-shaped value", raw: "ics_live_abc123def456", source: ciPlatformCIVar, want: "unknown"},
		{name: "CI holds a repo identifier", raw: "acme-prod-infra", source: ciPlatformCIVar, want: "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, normalizeCIPlatform(tt.raw, tt.source))
		})
	}
}

func TestNormalizedCIPlatformAtInit(t *testing.T) {
	if os.Getenv("TEST_NORMALIZED_CI_PLATFORM") == "1" {
		fmt.Print(NormalizedCIPlatform())
		os.Exit(0)
	}

	tests := []struct {
		name     string
		env      []string
		expected string
	}{
		{name: "override present", env: []string{"INFRACOST_CI_PLATFORM=Azure_DevOps_GitHub"}, expected: "azure_devops_github"},
		{name: "override names an undetectable CI", env: []string{"INFRACOST_CI_PLATFORM=woodpecker"}, expected: "woodpecker"},
		{name: "path-shaped override", env: []string{"INFRACOST_CI_PLATFORM=/Users/owen/secret"}, expected: "unknown"},
		{name: "CI names an undetectable runner", env: []string{"CI=some-internal-runner"}, expected: "unknown"},
		{name: "override normalized like detection", env: []string{"INFRACOST_CI_PLATFORM=TrUe"}, expected: "unknown"},
		{name: "override azure without provider", env: []string{"INFRACOST_CI_PLATFORM=azure_devops_"}, expected: "unknown"},
		{name: "override empty", env: []string{"INFRACOST_CI_PLATFORM=", "CI=true"}, expected: ""},
		{name: "override unset", env: []string{"CI=true"}, expected: "unknown"},
		{name: "ci disabled", env: []string{"CI=false"}, expected: ""},
		{name: "detected by prefix", env: []string{"BITBUCKET_BUILD_NUMBER=42"}, expected: "bitbucket"},
		{name: "CI names a known platform", env: []string{"CI=circleci"}, expected: "circleci"},
		{name: "CI holds an internal runner name", env: []string{"CI=acme-prod-internal"}, expected: "unknown"},
		{name: "producer override beats the CI var", env: []string{"INFRACOST_CI_PLATFORM=tfe", "CI=acme-prod-internal"}, expected: "tfe"},
		{name: "detected by exact key", env: []string{"GITHUB_ACTIONS=true"}, expected: "github_actions"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestNormalizedCIPlatformAtInit$")
			cmd.Env = append(scrubCIEnv(), append([]string{"TEST_NORMALIZED_CI_PLATFORM=1"}, tt.env...)...)
			output, err := cmd.Output()
			assert.NoError(t, err)
			assert.Equal(t, tt.expected, string(output))
		})
	}
}

func TestNormalizeEnvLabel(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "unset stays empty", raw: "", want: ""},
		{name: "a label passes through", raw: "VSCode", want: "vscode"},
		{name: "a path does not", raw: "/Users/owen/code/acme", want: "unknown"},
		{name: "a sentence does not", raw: "some long free text value", want: "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, normalizeEnvLabel(tt.raw))
		})
	}
}
