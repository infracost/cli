package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/infracost/cli/internal/format"
)

// Target describes a Terraform codebase the bench operates against. Each
// cell runs in a per-cell temp dir copied from Dir, optionally with a
// `.claude/skills/infracost-scan/SKILL.md` written into it (variant chosen
// per cell). The captured infracost output is the source of ground truth
// for verifiers; it is NOT shown to the model.
type Target struct {
	Slug        string         // sanitized name used for cache filenames + report grouping
	Dir         string         // absolute path to a checked-out Terraform tree
	FixturePath string         // absolute path to the cached infracost --json output
	Fixture     *format.Output // parsed fixture (ground truth)
	SkillSource string         // raw upstream SKILL.md including YAML frontmatter
	// BinPath is the absolute path to the infracost binary cells should
	// invoke. Threaded into the skill variants so the model never types
	// bare `infracost` (which can hit a system-installed legacy version
	// shadowed by a .zshrc that re-exports PATH).
	BinPath string
}

const cacheDirName = ".cache"

// resolveTarget produces a Target by, in order: cloning --target-repo (or
// using --target-dir), running `infracost scan --json` (or loading
// --fixture-file), and caching both. Subsequent runs reuse the cache unless
// --refresh-target is set.
func resolveTarget(ctx context.Context, cfg runConfig) (*Target, error) {
	cacheRoot := filepath.Join(cfg.OutDir, "..", cacheDirName)
	cacheRoot, err := filepath.Abs(cacheRoot)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(cacheRoot, 0o750); err != nil {
		return nil, err
	}

	dir, slug, err := resolveTargetDir(ctx, cfg, cacheRoot)
	if err != nil {
		return nil, err
	}

	fixturePath, err := resolveFixture(ctx, cfg, cacheRoot, slug, dir)
	if err != nil {
		return nil, err
	}

	raw, err := os.ReadFile(fixturePath) //nolint:gosec // fixturePath is bench-internal cache path or --fixture-file flag value
	if err != nil {
		return nil, fmt.Errorf("read fixture %s: %w", fixturePath, err)
	}
	var out format.Output
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("parse fixture %s: %w", fixturePath, err)
	}

	skillSource, err := resolveSkillSource(ctx, cfg, cacheRoot)
	if err != nil {
		return nil, err
	}

	return &Target{
		Slug:        slug,
		Dir:         dir,
		FixturePath: fixturePath,
		Fixture:     &out,
		SkillSource: skillSource,
		BinPath:     cfg.InfracostBin,
	}, nil
}

// defaultSkillURL points at the upstream scan SKILL.md, fetched live so the
// bench tracks whatever instructions real users see. Override via
// runConfig.SkillURL for a private fork.
const defaultSkillURL = "https://raw.githubusercontent.com/infracost/agent-skills/main/plugins/infracost/skills/scan/SKILL.md"

// resolveSkillSource fetches the upstream SKILL.md verbatim (frontmatter
// included) and caches it. We keep the raw markdown rather than a stripped
// "prompt body" because each skill cell writes a copy to its temp dir's
// `.claude/skills/infracost-scan/SKILL.md`, where Claude Code's skill
// loader expects the original frontmatter to be present.
func resolveSkillSource(ctx context.Context, cfg runConfig, cacheRoot string) (string, error) {
	url := cfg.SkillURL
	if url == "" {
		url = defaultSkillURL
	}
	skillsDir := filepath.Join(cacheRoot, "skills")
	if err := os.MkdirAll(skillsDir, 0o750); err != nil {
		return "", err
	}
	// Drop any trailing `.md` from the URL slug so we don't end up with
	// foo-SKILL.md.md when the URL itself ends in .md.
	cacheBase := strings.TrimSuffix(slugifyURL(url), ".md")
	cachePath := filepath.Join(skillsDir, cacheBase+".md")

	if !cfg.RefreshTarget {
		if b, err := os.ReadFile(cachePath); err == nil { //nolint:gosec // cachePath is bench cache dir + slug, not user input
			fmt.Printf("Using cached skill: %s\n", cachePath)
			return string(b), nil
		}
	}

	fmt.Printf("Fetching skill: %s → %s\n", url, cachePath)
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req) //nolint:gosec // URL is from bench config / built-in default, not user input
	if err != nil {
		return "", fmt.Errorf("fetch skill %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("fetch skill %s: HTTP %d", url, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(cachePath, body, 0o600); err != nil {
		return "", fmt.Errorf("cache skill: %w", err)
	}
	return string(body), nil
}

// SkillVariant returns the SKILL.md text for the given format variant.
// For `skill-default` this is the upstream source verbatim, but with a
// fool-proof binary-path preamble injected after the frontmatter so the
// model uses the bench-built binary explicitly rather than gambling on
// PATH resolution. (The user's system likely has a legacy `infracost` —
// e.g. from Homebrew — earlier on PATH, which has no `scan` / `inspect` /
// new flags. Empirically, PATH-prepending in the bench process doesn't
// reliably propagate through the cell's bash subprocess because shell
// init reorders PATH.)
//
// `skill-llm` / `skill-json` add the format-flag override appendix on
// top, again referring to the bench-built binary by absolute path.
func (t *Target) SkillVariant(format string) (string, error) {
	preamble, err := t.skillBinaryPathPreamble()
	if err != nil {
		return "", err
	}
	body := injectBinaryPath(t.SkillSource, preamble)
	switch format {
	case "skill-default":
		return body, nil
	case "skill-llm":
		return body + skillLLMOverride(t.BinPath), nil
	case "skill-json":
		return body + skillJSONOverride(t.BinPath), nil
	default:
		return "", fmt.Errorf("not a skill variant: %q", format)
	}
}

// skillBinaryPathPreamble produces a markdown block that goes immediately
// after the YAML frontmatter, telling the model — in the strongest terms
// — to use the bench-built infracost binary at its full absolute path
// and never to invoke a bare `infracost`.
func (t *Target) skillBinaryPathPreamble() (string, error) {
	if t.BinPath == "" {
		return "", fmt.Errorf("target.BinPath not set; resolve infracost binary before building skill variants")
	}
	return fmt.Sprintf(`# IMPORTANT — Use exactly this `+"`infracost`"+` binary

For this session, every reference to `+"`infracost`"+` in the rest of this skill MUST be invoked at the absolute path:

`+"```"+`
%s
`+"```"+`

Do **not** type bare `+"`infracost`"+`. The system's PATH may resolve `+"`infracost`"+` to a different (older) version that lacks the `+"`scan`"+`, `+"`inspect`"+`, `+"`--llm`"+`, `+"`--fields`"+`, and other flags this skill expects. Always type the full path above.

For example, where the skill below says:

`+"```"+`
infracost scan /path/to/repo
`+"```"+`

You MUST run:

`+"```"+`
%s scan /path/to/repo
`+"```"+`

This applies to every command on every invocation, with no exceptions.

---

`, t.BinPath, t.BinPath), nil
}

// injectBinaryPath places the binary-path preamble immediately after the
// YAML frontmatter (so Claude Code's skill loader still parses it
// correctly) but before the user-visible body. If the source has no
// frontmatter we just prepend.
func injectBinaryPath(source, preamble string) string {
	if !strings.HasPrefix(source, "---\n") {
		return preamble + source
	}
	rest := source[4:]
	end := strings.Index(rest, "\n---\n")
	if end < 0 {
		return preamble + source
	}
	frontmatter := source[:4+end+5]
	body := source[4+end+5:]
	return frontmatter + preamble + body
}

// skillLLMOverride is appended to the upstream SKILL.md to test whether
// steering the model to `--llm` plus the new query flags improves
// outcomes. Phrased as a strong override because the upstream skill
// actively discourages parsing machine-readable output ("you DO NOT NEED
// to write any scripts to handle JSON yourself"); a hint phrased as a
// suggestion would lose to that. Takes the absolute binary path so all
// example commands point at the bench-built binary explicitly.
func skillLLMOverride(binPath string) string {
	return `

## Output format (REQUIRED — overrides any other guidance in this skill)

When invoking the bench-built ` + "`infracost`" + ` (` + "`" + binPath + "`" + `) for any subcommand, **always** include the global ` + "`--llm`" + ` flag. This applies to ` + "`scan`" + `, ` + "`inspect`" + `, and ` + "`price`" + `. The ` + "`--llm`" + ` output is a compact, indentation-based text format you read directly — no jq required.

` + skillCommonFlagAppendix(binPath) + `
`
}

// skillJSONOverride is the equivalent for the JSON variant. Same strength
// of override so the comparison with skillLLMOverride is fair.
func skillJSONOverride(binPath string) string {
	return `

## Output format (REQUIRED — overrides any other guidance in this skill)

When invoking the bench-built ` + "`infracost`" + ` (` + "`" + binPath + "`" + `) for any subcommand, **always** include the global ` + "`--json`" + ` flag. This applies to ` + "`scan`" + `, ` + "`inspect`" + `, and ` + "`price`" + `.

` + skillCommonFlagAppendix(binPath) + `
`
}

// skillCommonFlagAppendix documents the inspect flags that obviate jq /
// python fallbacks. Shared between the --llm and --json variant overrides
// because the flags themselves are format-agnostic. Takes the absolute
// binary path so every example invocation is unambiguous.
func skillCommonFlagAppendix(binPath string) string {
	bin := "`" + binPath + "`"
	return `## Prefer native ` + bin + ` ` + "`inspect`" + ` flags over jq / python

The ` + "`inspect`" + ` command has dedicated flags for the patterns that would otherwise need jq or python aggregation. Always use these in preference to writing your own parsing scripts:

### Aggregations
- ` + "`infracost inspect --total-savings`" + ` — scalar sum of monthly savings across every FinOps issue.
- ` + "`infracost inspect --top-savings N`" + ` — top N FinOps issues sorted by ` + "`monthly_savings`" + ` desc.

### Resource selection
- ` + "`infracost inspect --missing-tag <key>`" + ` — resources missing that tag entirely.
- ` + "`infracost inspect --invalid-tag <key>`" + ` — resources where that tag's value is outside the policy's allowed list.
- ` + "`infracost inspect --min-cost N`" + ` / ` + "`--max-cost N`" + ` — resources within a monthly-cost range.
- ` + "`infracost inspect --filter \"<expr>\"`" + ` — comma-separated AND'd predicates. Supported keys: ` + "`policy`" + `, ` + "`project`" + `, ` + "`provider`" + `, ` + "`tag.<key>=missing`" + `. Example: ` + "`--filter \"tag.team=missing,provider=aws\"`" + `.

### Output projection (replaces ` + "`cut`" + ` / ` + "`awk '{print $N}'`" + `)
- ` + "`infracost inspect --fields <a,b,c>`" + ` — choose which columns to emit, in that order. With one field you get one value per line; multiple fields give a TSV with header.
  - For ` + "`--top-savings`" + `: ` + "`address`" + `, ` + "`policy`" + `, ` + "`policy_slug`" + `, ` + "`project`" + `, ` + "`monthly_savings`" + `, ` + "`description`" + `.
  - For ` + "`--missing-tag`" + ` / ` + "`--invalid-tag`" + ` / ` + "`--min-cost`" + ` / ` + "`--max-cost`" + `: ` + "`address`" + `, ` + "`type`" + `, ` + "`project`" + `, ` + "`monthly_cost`" + `, ` + "`is_free`" + `.
- ` + "`infracost inspect --addresses-only`" + ` — alias for ` + "`--fields=address`" + ` for the common "just give me the names" case.

### Pre-computed totals on ` + "`scan`" + ` output
The ` + "`scan`" + ` output includes a top-level ` + "`summary`" + ` block with pre-computed totals (total monthly cost, total potential savings, distinct failing resources counts, failing policy counts). Read that first before drilling in — most "how many X are failing" questions can be answered with one ` + "`infracost inspect --summary --fields <name>`" + ` call.

Available summary fields (use with ` + "`--summary --fields <name>`" + `): ` + "`projects`" + `, ` + "`resources`" + `, ` + "`costed_resources`" + `, ` + "`free_resources`" + `, ` + "`monthly_cost`" + `, ` + "`finops_policies`" + `, ` + "`failing_policies`" + ` (failing FinOps), ` + "`tagging_policies`" + `, ` + "`failing_tagging_policies`" + `, ` + "`guardrails`" + `, ` + "`triggered_guardrails`" + `, ` + "`budgets`" + `, ` + "`over_budget`" + `. Single field → bare value, no label.

### Examples — what to use for common queries

(Replace ` + "`<INFRACOST>`" + ` below with ` + bin + ` exactly — never type bare ` + "`infracost`" + `.)

` + "```" + `bash
# Setup: populate the cache.
` + binPath + ` scan /path/to/repo

# Counts and totals (single --summary call answers most "how many" questions):
` + binPath + ` inspect --summary                                # full summary block
` + binPath + ` inspect --summary --fields failing_policies      # just the count, bare value
` + binPath + ` inspect --summary --fields failing_policies,failing_tagging_policies,resources
` + binPath + ` inspect --total-savings                          # one number

# "List the top N highest-savings opportunities" (no jq, no awk):
` + binPath + ` inspect --top-savings 5 --fields address,monthly_savings

# "Which resources fail the tagging policy?":
` + binPath + ` inspect --missing-tag team                        # default: one address per line
` + binPath + ` inspect --missing-tag team --fields address,type  # with type column

# "All resources failing a specific policy" (preserves full list, no truncation):
` + binPath + ` inspect --policy "Required Tags" --addresses-only

# Composable filter (multiple predicates, AND'd):
` + binPath + ` inspect --filter "tag.team=missing,provider=aws" --fields address,monthly_cost
` + "```" + `

### Anti-patterns

Do not:
- Write ` + "`jq`" + ` pipelines or ` + "`python3 -c`" + ` heredocs over the raw scan output for any of the patterns above. The dedicated flags exist for them.
- Pipe through ` + "`cut -f`" + ` or ` + "`awk '{print $N}'`" + ` — use ` + "`--fields`" + ` to project columns directly.
- Run ` + bin + ` ` + "`scan --json`" + ` and parse the result yourself for aggregates that ` + "`--summary`" + ` already computes.
- Type bare ` + "`infracost`" + `; always invoke it as ` + bin + `.
`
}

func resolveTargetDir(ctx context.Context, cfg runConfig, cacheRoot string) (string, string, error) {
	if cfg.TargetDir != "" {
		abs, err := filepath.Abs(cfg.TargetDir)
		if err != nil {
			return "", "", err
		}
		info, err := os.Stat(abs)
		if err != nil {
			return "", "", fmt.Errorf("stat --target-dir %s: %w", abs, err)
		}
		if !info.IsDir() {
			return "", "", fmt.Errorf("--target-dir %s is not a directory", abs)
		}
		return abs, slugify(filepath.Base(abs)), nil
	}

	if cfg.TargetRepo == "" {
		return "", "", fmt.Errorf("either --target-repo or --target-dir must be set")
	}

	slug := slugifyURL(cfg.TargetRepo)
	dir := filepath.Join(cacheRoot, "repos", slug)

	if cfg.RefreshTarget {
		if err := os.RemoveAll(dir); err != nil {
			return "", "", fmt.Errorf("clear cached repo: %w", err)
		}
	}

	if _, err := os.Stat(dir); err == nil {
		fmt.Printf("Using cached target repo: %s\n", dir)
		return dir, slug, nil
	}

	if err := os.MkdirAll(filepath.Dir(dir), 0o750); err != nil {
		return "", "", err
	}
	fmt.Printf("Cloning %s → %s\n", cfg.TargetRepo, dir)
	cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(cctx, "git", "clone", "--depth=1", cfg.TargetRepo, dir) //nolint:gosec // TargetRepo is bench-operator-supplied via flag
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", "", fmt.Errorf("git clone %s: %w", cfg.TargetRepo, err)
	}
	return dir, slug, nil
}

func resolveFixture(ctx context.Context, cfg runConfig, cacheRoot, slug, repoDir string) (string, error) {
	if cfg.FixtureFile != "" {
		abs, err := filepath.Abs(cfg.FixtureFile)
		if err != nil {
			return "", err
		}
		if _, err := os.Stat(abs); err != nil {
			return "", fmt.Errorf("stat --fixture-file %s: %w", abs, err)
		}
		return abs, nil
	}

	fixturesDir := filepath.Join(cacheRoot, "fixtures")
	if err := os.MkdirAll(fixturesDir, 0o750); err != nil {
		return "", err
	}
	fixturePath := filepath.Join(fixturesDir, slug+".json")

	if !cfg.RefreshTarget {
		if _, err := os.Stat(fixturePath); err == nil {
			fmt.Printf("Using cached fixture: %s\n", fixturePath)
			return fixturePath, nil
		}
	}

	infracost := infracostBinary()
	fmt.Printf("Running %s scan --json %s → %s\n", infracost, repoDir, fixturePath)
	cctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(cctx, infracost, "scan", "--json", repoDir) //nolint:gosec // infracost binary path resolved via PATH lookup, repoDir from bench config
	out, err := cmd.Output()
	if err != nil {
		// Surface stderr so the user sees auth / plugin errors, not just exit code.
		var stderr string
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = string(ee.Stderr)
		}
		return "", fmt.Errorf("infracost scan failed: %w (stderr: %s)", err, stderr)
	}
	if err := os.WriteFile(fixturePath, out, 0o600); err != nil {
		return "", fmt.Errorf("write fixture: %w", err)
	}
	return fixturePath, nil
}

// infracostBinary picks `infracost` from PATH if present, otherwise falls
// back to `infracost-preview` (matches the SessionStart hook in this repo).
func infracostBinary() string {
	if path, err := exec.LookPath("infracost"); err == nil {
		return path
	}
	if path, err := exec.LookPath("infracost-preview"); err == nil {
		return path
	}
	return "infracost"
}

func slugifyURL(u string) string {
	s := strings.TrimSuffix(u, ".git")
	s = strings.TrimPrefix(s, "https://")
	s = strings.TrimPrefix(s, "http://")
	s = strings.TrimPrefix(s, "git@")
	s = strings.ReplaceAll(s, ":", "-")
	s = strings.ReplaceAll(s, "/", "-")
	clean := slugify(s)
	if clean == "" {
		// Fallback to a deterministic hash if the URL has nothing slug-friendly.
		h := sha256.Sum256([]byte(u))
		return "repo-" + hex.EncodeToString(h[:6])
	}
	return clean
}

func slugify(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_' || r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), "-")
}
