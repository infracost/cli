package main

import (
	"context"
	"crypto/sha1"
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
	if err := os.MkdirAll(cacheRoot, 0o755); err != nil {
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

	raw, err := os.ReadFile(fixturePath)
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
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		return "", err
	}
	// Drop any trailing `.md` from the URL slug so we don't end up with
	// foo-SKILL.md.md when the URL itself ends in .md.
	cacheBase := strings.TrimSuffix(slugifyURL(url), ".md")
	cachePath := filepath.Join(skillsDir, cacheBase+".md")

	if !cfg.RefreshTarget {
		if b, err := os.ReadFile(cachePath); err == nil {
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
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch skill %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("fetch skill %s: HTTP %d", url, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(cachePath, body, 0o644); err != nil {
		return "", fmt.Errorf("cache skill: %w", err)
	}
	return string(body), nil
}

// SkillVariant returns the SKILL.md text for the given format variant. For
// `skill-default` this is the upstream source verbatim; for `skill-llm` and
// `skill-json` we append a short "Output format" instruction telling the
// model to prefer that flag on infracost commands. The frontmatter is
// preserved so Claude Code's skill loader picks the file up correctly.
func (t *Target) SkillVariant(format string) (string, error) {
	switch format {
	case "skill-default":
		return t.SkillSource, nil
	case "skill-llm":
		return t.SkillSource + skillLLMOverride, nil
	case "skill-json":
		return t.SkillSource + skillJSONOverride, nil
	default:
		return "", fmt.Errorf("not a skill variant: %q", format)
	}
}

// skillLLMOverride is appended to the upstream SKILL.md to test whether
// steering the model to `--llm` improves outcomes. Phrased as a strong
// override because the upstream skill actively discourages parsing
// machine-readable output ("you DO NOT NEED to write any scripts to handle
// JSON yourself"); a hint phrased as a suggestion would lose to that.
const skillLLMOverride = `

## Output format (REQUIRED — overrides any other guidance in this skill)

When invoking ` + "`infracost`" + ` for any subcommand, **always** include the global ` + "`--llm`" + ` flag. This applies to ` + "`scan`" + `, ` + "`inspect`" + ` (with any sub-flags like ` + "`--policy`" + `, ` + "`--group-by`" + `, ` + "`--summary`" + `), and ` + "`price`" + `. The ` + "`--llm`" + ` output is a compact, indentation-based text format you read directly — no jq required.

Examples:

` + "```" + `bash
infracost scan --llm /path/to/repo
infracost inspect --summary --llm
infracost inspect --policy "Required Tags" --llm
infracost inspect --group-by policy --llm
` + "```" + `

This overrides the default human-readable output described elsewhere in this skill. Do not omit ` + "`--llm`" + `.
`

// skillJSONOverride is the equivalent for the JSON variant. Same strength
// of override so the comparison with skillLLMOverride is fair.
const skillJSONOverride = `

## Output format (REQUIRED — overrides any other guidance in this skill)

When invoking ` + "`infracost`" + ` for any subcommand, **always** include the global ` + "`--json`" + ` flag. This applies to ` + "`scan`" + `, ` + "`inspect`" + ` (with any sub-flags like ` + "`--policy`" + `, ` + "`--group-by`" + `, ` + "`--summary`" + `), and ` + "`price`" + `. Parse the JSON output with ` + "`jq`" + ` to extract whatever you need.

Examples:

` + "```" + `bash
infracost scan --json /path/to/repo
infracost inspect --summary --json
infracost inspect --policy "Required Tags" --json
infracost inspect --group-by policy --json
` + "```" + `

This overrides the default human-readable output described elsewhere in this skill. Do not omit ` + "`--json`" + `.
`

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

	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return "", "", err
	}
	fmt.Printf("Cloning %s → %s\n", cfg.TargetRepo, dir)
	cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(cctx, "git", "clone", "--depth=1", cfg.TargetRepo, dir)
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
	if err := os.MkdirAll(fixturesDir, 0o755); err != nil {
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
	cmd := exec.CommandContext(cctx, infracost, "scan", "--json", repoDir)
	out, err := cmd.Output()
	if err != nil {
		// Surface stderr so the user sees auth / plugin errors, not just exit code.
		var stderr string
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = string(ee.Stderr)
		}
		return "", fmt.Errorf("infracost scan failed: %w\nstderr: %s", err, stderr)
	}
	if err := os.WriteFile(fixturePath, out, 0o644); err != nil {
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
		h := sha1.Sum([]byte(u))
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
