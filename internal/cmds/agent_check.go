package cmds

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Masterminds/semver/v3"
	"github.com/infracost/cli/internal/cache"
	"github.com/infracost/cli/internal/config"
	"github.com/infracost/cli/internal/ui"
	"github.com/infracost/cli/pkg/logging"
)

// StaleAgent describes a configured AI agent whose installed Infracost
// skill version is behind the latest published version.
type StaleAgent struct {
	Name      string `json:"name"`
	Installed string `json:"installed"`
	Latest    string `json:"latest"`
}

// agentCheckTTL bounds how often we probe the user's agents + hit the
// network for the latest skill version. Mirrors the 24h self-update check.
const agentCheckTTL = 24 * time.Hour

// latestSkillVersionURL is the raw plugin manifest on agent-skills' default
// branch. We read the same `version` field Claude Code uses as its update
// gate, so "latest" here means exactly what every skill channel installs.
const latestSkillVersionURL = "https://raw.githubusercontent.com/infracost/agent-skills/main/plugins/infracost/.claude-plugin/plugin.json"

// CachedStaleAgents returns the stale-agent list from the last completed
// check, reading only the on-disk cache. It never probes agents or hits
// the network, so it's safe to call synchronously on the hot path — the
// end-of-run nag uses it so a fast command never waits on agent scanning.
// Returns nil when there's no cache yet or the check is disabled.
func CachedStaleAgents() []StaleAgent {
	if skipAgentCheck() {
		return nil
	}
	c, err := loadAgentCheckCache()
	if err != nil || c == nil {
		return nil
	}
	return c.Stale
}

// RefreshStaleAgentsIfStale returns the cached stale-agent list unchanged
// when the cache is still fresh; otherwise it runs a live check (probing
// each detectable agent for its installed skill version and comparing
// against the latest published version), writes the result to the cache,
// and returns it. Intended to run in a background goroutine — the caller
// should read its result with a non-blocking select so the foreground
// command is never blocked on it.
//
// It deliberately takes no *config.Config: it runs concurrently with flag
// parsing, so reading config fields here would race. Agent binaries are
// resolved from PATH only (a custom ClaudePath override is honored by the
// explicit `agent status` command, not this background nag).
func RefreshStaleAgentsIfStale(ctx context.Context) []StaleAgent {
	if skipAgentCheck() {
		return nil
	}

	if c, err := loadAgentCheckCache(); err == nil && c != nil && time.Since(c.CheckedAt) < agentCheckTTL {
		return c.Stale
	}

	stale, latest, err := checkStaleAgents(ctx, nil)
	if err != nil {
		logging.Debugf("agent-skill check failed: %v", err)
		return nil
	}

	if err := saveAgentCheckCache(&agentCheckCache{
		CheckedAt: time.Now(),
		Latest:    latest,
		Stale:     stale,
	}); err != nil {
		logging.Debugf("failed to save agent-skill check cache: %v", err)
	}
	return stale
}

// ClearStaleAgentsCache removes the cached check result so the next run
// re-probes from scratch. Called after `agent setup`/`remove` so a fix (or
// removal) isn't shadowed by a stale "you're behind" warning for up to a day.
func ClearStaleAgentsCache() {
	if err := os.Remove(agentCheckCachePath()); err != nil && !os.IsNotExist(err) {
		logging.Debugf("failed to clear agent-skill check cache: %v", err)
	}
}

// AgentStatus is the per-agent view of an installed, version-detectable
// agent — surfaced by `infracost agent status`.
type AgentStatus struct {
	Name      string
	Installed string
	Latest    string
	Stale     bool
}

// probeInstalledAgents fetches the latest skill version, then probes every
// enabled, version-detectable agent for its installed version. Agents that
// aren't installed, can't be version-detected, or report an unparseable
// version are omitted. Returns the per-agent statuses plus the latest
// version string.
func probeInstalledAgents(ctx context.Context, cfg *config.Config) ([]AgentStatus, string, error) {
	latestStr, err := fetchLatestSkillVersion(ctx)
	if err != nil {
		return nil, "", err
	}
	latest, err := semver.NewVersion(latestStr)
	if err != nil {
		return nil, "", fmt.Errorf("parsing latest skill version %q: %w", latestStr, err)
	}

	var statuses []AgentStatus
	for _, a := range supportedAgents {
		if !a.enabled || a.version == nil {
			continue
		}

		// For agents driven by a CLI, only probe when that CLI is on PATH;
		// filesystem-driven agents (no binaries) resolve their version
		// directly and don't need one.
		var bin string
		if len(a.binaries) > 0 {
			resolved, resolveErr := resolveAgentBinaryForCheck(cfg, a)
			if resolveErr != nil {
				continue // agent CLI not installed — nothing to check
			}
			bin = resolved
		}

		installedStr, verErr := a.version(bin)
		if verErr != nil || installedStr == "" {
			continue // not installed, or version undeterminable
		}
		installed, parseErr := semver.NewVersion(installedStr)
		if parseErr != nil {
			continue
		}
		statuses = append(statuses, AgentStatus{
			Name:      a.name,
			Installed: installedStr,
			Latest:    latestStr,
			Stale:     installed.LessThan(latest),
		})
	}
	return statuses, latestStr, nil
}

// checkStaleAgents performs the live check and returns only the agents
// that are behind the latest published skill version.
func checkStaleAgents(ctx context.Context, cfg *config.Config) ([]StaleAgent, string, error) {
	statuses, latest, err := probeInstalledAgents(ctx, cfg)
	if err != nil {
		return nil, "", err
	}
	var stale []StaleAgent
	for _, s := range statuses {
		if s.Stale {
			stale = append(stale, StaleAgent{Name: s.Name, Installed: s.Installed, Latest: s.Latest})
		}
	}
	return stale, latest, nil
}

// AgentStatuses runs a live probe of installed agents for `agent status`.
func AgentStatuses(ctx context.Context, cfg *config.Config) ([]AgentStatus, string, error) {
	return probeInstalledAgents(ctx, cfg)
}

// isSkillStale reports whether the installed version is a valid semver
// strictly older than latest. Unparseable versions are treated as
// not-stale so a bad signal never produces a false warning.
func isSkillStale(installed, latest string) bool {
	iv, err := semver.NewVersion(installed)
	if err != nil {
		return false
	}
	lv, err := semver.NewVersion(latest)
	if err != nil {
		return false
	}
	return iv.LessThan(lv)
}

// fetchLatestSkillVersion reads the `version` field from the plugin
// manifest on agent-skills' default branch.
func fetchLatestSkillVersion(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, latestSkillVersionURL, nil)
	if err != nil {
		return "", err
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetching latest skill version: unexpected status %d", resp.StatusCode)
	}

	var manifest struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&manifest); err != nil {
		return "", fmt.Errorf("decoding plugin manifest: %w", err)
	}
	if manifest.Version == "" {
		return "", fmt.Errorf("plugin manifest has no version")
	}
	return strings.TrimSpace(manifest.Version), nil
}

// FormatStaleAgentsNotice renders the end-of-run warning for stale agents.
// Returns "" when nothing is stale so the caller can print unconditionally.
func FormatStaleAgentsNotice(stale []StaleAgent) string {
	if len(stale) == 0 {
		return ""
	}
	var b strings.Builder
	if len(stale) == 1 {
		s := stale[0]
		fmt.Fprintf(&b, "\n%s %s is running an outdated Infracost skill (%s → %s)\n",
			ui.Caution("Agent skills:"), ui.Bold(s.Name), s.Installed, ui.Bold(s.Latest))
	} else {
		fmt.Fprintf(&b, "\n%s some agents are running outdated Infracost skills:\n", ui.Caution("Agent skills:"))
		for _, s := range stale {
			fmt.Fprintf(&b, "  • %s (%s → %s)\n", ui.Bold(s.Name), s.Installed, ui.Bold(s.Latest))
		}
	}
	fmt.Fprintf(&b, "  Run %s to upgrade.\n", ui.Code("infracost agent setup"))
	return b.String()
}

// skipAgentCheck mirrors the self-update check's opt-outs: an explicit env
// var and test-binary runs. (Unlike the update check there's no "dev
// build" skip — the skill versions live in a separate repo, so a dev CLI
// build can still meaningfully compare skill versions.)
func skipAgentCheck() bool {
	if os.Getenv("INFRACOST_SKIP_AGENT_CHECK") != "" {
		return true
	}
	return isAgentCheckTestBinary()
}

// isAgentCheckTestBinary is a var so tests can exercise the check paths.
var isAgentCheckTestBinary = func() bool {
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	return strings.HasSuffix(exe, ".test")
}

type agentCheckCache struct {
	CheckedAt time.Time    `json:"checkedAt"`
	Latest    string       `json:"latest"`
	Stale     []StaleAgent `json:"stale"`
}

// agentCheckCachePath is a var so tests can redirect it to a temp dir.
var agentCheckCachePath = func() string {
	return filepath.Join(cache.Root(), cache.AgentSkillsCheckFilename)
}

func loadAgentCheckCache() (*agentCheckCache, error) {
	b, err := os.ReadFile(agentCheckCachePath()) //nolint:gosec // path is under the user's own cache dir
	if err != nil {
		return nil, err
	}
	var c agentCheckCache
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

func saveAgentCheckCache(c *agentCheckCache) error {
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	path := agentCheckCachePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}
