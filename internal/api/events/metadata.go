package events

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"

	"github.com/Masterminds/semver/v3"

	"github.com/infracost/cli/internal/trace"
	"github.com/infracost/cli/version"
)

// metadataMu guards the map itself. Snapshot is a shallow copy, so slice and
// map values stay shared with it — nothing mutates those after registration.
var (
	metadataMu sync.RWMutex
	metadata   map[string]interface{}
)

func init() {
	metadata = map[string]interface{}{
		"caller":      getCaller(),
		"ciPlatform":  NormalizedCIPlatform(),
		"cliPlatform": normalizeEnvLabel(os.Getenv("INFRACOST_CLI_PLATFORM")),
		"version":     stripVersion(version.Version),
		"fullVersion": version.Version,
		"isDev":       version.Version == "dev",
		"isTest":      strings.HasSuffix(os.Args[0], ".test"),
		"installId":   trace.InstallID,
		"os":          runtime.GOOS,
		"arch":        runtime.GOARCH,
	}
}

// RegisterMetadata adds or updates entries in the global event metadata.
// Other packages should call this during initialization to attach metadata
// that will be included with every event.
func RegisterMetadata(key string, value interface{}) {
	metadataMu.Lock()
	defer metadataMu.Unlock()
	metadata[key] = value
}

// Snapshot returns a shallow copy of the current metadata. Used by
// long-running processes (the MCP stdio server) to capture a baseline
// they can roll back to between per-request mutations.
func Snapshot() map[string]interface{} {
	metadataMu.RLock()
	defer metadataMu.RUnlock()
	out := make(map[string]interface{}, len(metadata))
	for k, v := range metadata {
		out[k] = v
	}
	return out
}

// Restore replaces the metadata map with a copy of the given snapshot.
// Pair with Snapshot to scope per-request metadata in a long-running
// process so values registered while serving one request (orgId,
// repoId, etc.) don't leak into the next.
func Restore(snapshot map[string]interface{}) {
	next := make(map[string]interface{}, len(snapshot))
	for k, v := range snapshot {
		next[k] = v
	}
	metadataMu.Lock()
	defer metadataMu.Unlock()
	metadata = next
}

// GetMetadata retrieves the value for the specified metadata, and false if it doesn't
// exist or the type is wrong.
func GetMetadata[V any](key string) (V, bool) {
	metadataMu.RLock()
	defer metadataMu.RUnlock()
	value, ok := metadata[key]
	if !ok {
		var v V
		return v, false
	}
	v, ok := value.(V)
	return v, ok
}

func stripVersion(v string) string {
	parsed, err := semver.NewVersion(v)
	if err != nil {
		return v
	}
	return fmt.Sprintf("%d.%d.%d", parsed.Major(), parsed.Minor(), parsed.Patch())
}

func getCaller() string {
	if caller, ok := os.LookupEnv("INFRACOST_CLI_CALLER"); ok {
		// if this has been explicitly set, then use that.
		return caller
	}

	// Check for known AI agent environment variables.
	for env, caller := range map[string]string{
		"CLAUDE_CODE":            "claude-code",
		"CLAUDE_CODE_ENTRYPOINT": "claude-code",
		"CLAUDECODE":             "claude-code",
		"CODEX_CLI_INVOKED_BY":   "codex",
		"GEMINI_CLI":             "gemini-cli",
		"AIDER":                  "aider",
		"CONTINUE_GLOBAL_DIR":    "continue",
		"CLINE_TASK_ID":          "cline",
		"CURSOR_TRACE_ID":        "cursor",
	} {
		if _, ok := os.LookupEnv(env); ok {
			return caller
		}
	}

	// Check TERM_PROGRAM for AI-powered IDEs. This isn't a perfect signal since
	// the user could be typing manually in the IDE's terminal, but it's a
	// reasonable heuristic.
	switch os.Getenv("TERM_PROGRAM") {
	case "cursor":
		return "cursor"
	case "windsurf":
		return "windsurf"
	}

	return ""
}

// normalizeEnvLabel bounds a label set by the environment, the same way
// ciPlatform is: an unbounded value here rides on every event.
func normalizeEnvLabel(raw string) string {
	label := strings.ToLower(raw)
	if label == "" {
		return ""
	}
	if !isPlatformLabel(label) {
		return "unknown"
	}
	return label
}

// CI detection reads these; tests scrub the ambient environment from the
// same lists, so keep them here rather than inline.
var (
	ciPlatformEnvs = map[string]string{
		"GITHUB_ACTIONS":      "github_actions",
		"GITLAB_CI":           "gitlab_ci",
		"CIRCLECI":            "circleci",
		"JENKINS_HOME":        "jenkins",
		"BUILDKITE":           "buildkite",
		"TFC_RUN_ID":          "tfc",
		"ENV0_ENVIRONMENT_ID": "env0",
		"SCALR_RUN_ID":        "scalr",
		"CF_BUILD_ID":         "codefresh",
		"TRAVIS":              "travis",
		"CODEBUILD_CI":        "codebuild",
		"TEAMCITY_VERSION":    "teamcity",
		"BUDDYBUILD_BRANCH":   "buddybuild",
		"BITRISE_IO":          "bitrise",
		"SEMAPHORE":           "semaphoreci",
		"APPVEYOR":            "appveyor",
		"WERCKER_GIT_BRANCH":  "wercker",
		"MAGNUM":              "magnumci",
		"SHIPPABLE":           "shippable",
		"TDDIUM":              "tddium",
		"GREENHOUSE":          "greenhouse",
		"CIRRUS_CI":           "cirrusci",
		"TS_ENV":              "terraspace",
	}

	ciPlatformEnvPrefixes = map[string]string{
		"ATLANTIS_":       "atlantis",
		"BITBUCKET_":      "bitbucket",
		"CONCOURSE_":      "concourse",
		"SPACELIFT_":      "spacelift",
		"HARNESS_":        "harness",
		"TERRATEAM_":      "terrateam",
		"KEPTN_":          "keptn",
		"CLOUDCONCIERGE_": "cloudconcierge",
	}

	// ciPlatformOtherEnvs are read by getCIPlatform outside the two maps.
	ciPlatformOtherEnvs = []string{
		"INFRACOST_CI_PLATFORM",
		"SYSTEM_COLLECTIONURI",
		"BUILD_REPOSITORY_PROVIDER",
		"CI",
	}
)

// ciPlatformSource records who set the value, because that decides how far it
// is trusted. Our own tooling names a platform; a bare CI var is user data.
type ciPlatformSource int

const (
	ciPlatformNone ciPlatformSource = iota
	// ciPlatformProducer: INFRACOST_CI_PLATFORM, set by our dashboard run-task,
	// runner and action images to name a CI that detection cannot see.
	ciPlatformProducer
	// ciPlatformDetected: derived from our own lists, so already bounded.
	ciPlatformDetected
	// ciPlatformCIVar: the bare CI variable, whose value is whatever the user's
	// environment happens to hold.
	ciPlatformCIVar
)

func getCIPlatform() (string, ciPlatformSource) {
	if ciPlatform, ok := os.LookupEnv("INFRACOST_CI_PLATFORM"); ok {
		return ciPlatform, ciPlatformProducer
	}

	for env, platform := range ciPlatformEnvs {
		if _, ok := os.LookupEnv(env); ok {
			return platform, ciPlatformDetected
		}
	}

	// Azure DevOps uses a dynamic platform name based on the repository provider.
	if _, ok := os.LookupEnv("SYSTEM_COLLECTIONURI"); ok {
		return fmt.Sprintf("azure_devops_%s", os.Getenv("BUILD_REPOSITORY_PROVIDER")), ciPlatformDetected
	}

	for prefix, platform := range ciPlatformEnvPrefixes {
		for _, k := range os.Environ() {
			if strings.HasPrefix(k, prefix) {
				return platform, ciPlatformDetected
			}
		}
	}

	if ciPlatform, ok := os.LookupEnv("CI"); ok {
		return ciPlatform, ciPlatformCIVar
	}

	return "", ciPlatformNone
}
