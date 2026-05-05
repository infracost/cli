// Command llmbench measures end-to-end Claude Code cost, latency, and
// accuracy on a real Terraform codebase. It runs each question across
// four formats, each backed by one `claude -p` invocation per cell:
//
//	bare-tf        no skill — `claude -p` with --disable-slash-commands.
//	               The model has Bash + Read tools but no infracost
//	               instructions; this is the "what would Claude do
//	               unassisted" baseline.
//	skill-default  the upstream infracost-scan SKILL.md is written to
//	               the cell's working dir under .claude/skills/, so it
//	               loads via Claude Code's project-skill precedence
//	               regardless of what the user has installed globally.
//	skill-llm      same as skill-default plus a short appended section
//	               instructing the model to pass `--llm` on infracost
//	               commands when reading machine-readable output.
//	skill-json     same as skill-default plus a section recommending
//	               `--json` instead.
//
// Auth flows through the user's existing Claude Code session — no API
// key needed. Run:
//
//	go run ./tools/llmbench --fixture-file /tmp/scan.json
//
// The bare-tf vs skill-default comparison answers "does the skill help?";
// skill-default vs skill-llm vs skill-json answers "which output flag
// should the skill recommend?".
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const defaultTargetRepo = "https://github.com/glenngillen/demo-missing-tags"

func main() {
	var (
		targetRepo    = flag.String("target-repo", defaultTargetRepo, "Git URL of the Terraform repo to scan (ignored when --target-dir is set)")
		targetDir     = flag.String("target-dir", "", "Local path to a Terraform tree (overrides --target-repo)")
		fixtureFile   = flag.String("fixture-file", "", "Path to a pre-captured `infracost scan --json` output (skips running infracost)")
		refreshTarget = flag.Bool("refresh-target", false, "Re-clone the repo, re-run infracost, and re-fetch the skill even if cached")
		formats       = flag.String("formats", "bare-tf,skill-default,skill-llm,skill-json", "comma-separated formats (bare-tf, skill-default, skill-llm, skill-json)")
		skillURL      = flag.String("skill-url", "", "Override URL of the SKILL.md fetched for skill cells (defaults to infracost/agent-skills main)")
		claudeBin     = flag.String("claude-bin", "/opt/homebrew/bin/claude", "Path to the claude CLI binary. Defaults to the Homebrew install path explicitly so we bypass any shell aliasing or Nix wrapper that resolves `claude` on $PATH to a sandboxed-user wrapper (which resets PATH for cell subprocesses and breaks our PATH-prepended infracost binary).")
		model         = flag.String("model", "opus", "Model alias or full ID to pass to claude --model")
		maxTurns      = flag.Int("max-turns", 25, "Pass to claude --max-turns (cap on agentic turns per cell)")
		out           = flag.String("out", "tools/llmbench/results", "output directory for results and report")
		dryRun        = flag.Bool("dry-run", false, "print prompt plan without calling claude")
		question      = flag.String("question", "", "filter to questions whose ID contains this substring")
		repeats       = flag.Int("repeats", 5, "repeat each cell N times to average out variance")
		skipSanity    = flag.Bool("skip-sanity", false, "skip the startup probe that verifies project-skill loading and --disable-slash-commands suppression")
		infracostBin  = flag.String("infracost-bin", "", "Path to an `infracost` binary to use in cell subprocesses. If empty, llmbench builds one from the current repo so --llm is guaranteed present.")
		policyCtxFile = flag.String("policy-context-file", "", "Path to a text file describing the tagging / FinOps policies in plain English. Injected into bare-tf prompts only — skill cells already get policy data via infracost. Without this, bare-tf has to guess the policy and will systematically fail on detection / fix questions, distorting the comparison.")
		reportOnly    = flag.String("report-only", "", "Skip running cells; build a fresh report from existing JSONL files. Comma-separated list of paths or globs (e.g. 'tools/llmbench/results/results-*.jsonl'). Useful for combining a skill-only run with a separate bare-tf run.")
		rerunFailed   = flag.String("rerun-failed", "", "Path to a JSONL from a prior run. Re-runs only the (question, format) cells whose Error is non-empty or Verdict is empty. Combine with a higher --max-turns to get accurate cost numbers on cells that previously hit the turn cap.")
		rescore       = flag.String("rescore", "", "Path to a JSONL from a prior run. Reapplies each question's verifier against the cells' final_text without re-running the model — useful when the verifier itself changed (e.g. extractInt now strips backticks). Writes a `<input>.rescored.jsonl` next to the input unless --rescore-out is set.")
		rescoreOut    = flag.String("rescore-out", "", "Output path for --rescore. Defaults to '<input>.rescored.jsonl' alongside the input.")
	)
	flag.Parse()

	formatList := splitCSV(*formats)

	if *rescore != "" {
		if err := runRescore(*rescore, *rescoreOut, *fixtureFile, *targetRepo, *targetDir, *out); err != nil {
			fail(err)
		}
		return
	}

	if *reportOnly != "" {
		cells, err := loadCellsJSONL(*reportOnly)
		if err != nil {
			fail(err)
		}
		if len(cells) == 0 {
			fail(fmt.Errorf("--report-only matched no cells (check the path/glob)"))
		}
		cfg := runConfig{
			Model:      *model,
			Formats:    uniqueFormats(cells),
			Repeats:    *repeats,
			MaxTurns:   *maxTurns,
			OutDir:     *out,
			TargetRepo: *targetRepo,
			TargetDir:  *targetDir,
		}
		path, err := writeReport(*out, cells, cfg)
		if err != nil {
			fail(err)
		}
		fmt.Printf("Wrote %s (%d cells loaded from %s)\n", path, len(cells), *reportOnly)
		return
	}

	policyContext := ""
	if *policyCtxFile != "" {
		b, err := os.ReadFile(*policyCtxFile)
		if err != nil {
			fail(fmt.Errorf("read --policy-context-file: %w", err))
		}
		policyContext = strings.TrimSpace(string(b))
	}

	// bare-tf cells sandbox HOME to suppress globally-installed skills.
	// The OAuth credentials cached under the real HOME aren't reachable in
	// that sandbox, so we require the env var as fallback. Only enforce
	// when bare-tf is actually being run.
	if containsFormat(formatList, "bare-tf") && !*dryRun && os.Getenv("CLAUDE_CODE_OAUTH_TOKEN") == "" {
		fail(errors.New(
			"bare-tf cells sandbox HOME and need CLAUDE_CODE_OAUTH_TOKEN set explicitly. " +
				"One-time setup: run `claude setup-token` interactively, copy the printed token, then " +
				"`export CLAUDE_CODE_OAUTH_TOKEN=<token>`. Or drop bare-tf from --formats if you only want skill-* comparisons"))
	}

	if err := os.MkdirAll(*out, 0o750); err != nil {
		fail(err)
	}

	// Steer all os.MkdirTemp calls to a project-local directory. macOS's
	// /private/var/folders/.../T/ has user-only ACLs that subprocesses with
	// different effective uid/gid (e.g. Claude Code's sandboxed shell) can't
	// traverse, leading to "shell-init: getcwd: Permission denied" errors
	// when we set cmd.Dir to a temp path. The project cache dir is owned by
	// the user running the bench so subprocesses inherit access.
	tmpBase := filepath.Join(*out, "..", ".cache", "tmp")
	if err := os.MkdirAll(tmpBase, 0o750); err != nil {
		fail(err)
	}
	if err := os.Setenv("TMPDIR", tmpBase); err != nil {
		fail(err)
	}

	// Sandbox HOME for bare-tf cells: a single empty directory reused
	// across cells. claude will create whatever it needs (~/.claude/
	// session caches, etc.) on first use; we only care that the user's
	// globally-installed skills aren't reachable through it.
	sandboxHome := filepath.Join(*out, "..", ".cache", "sandbox-home")
	if err := os.MkdirAll(sandboxHome, 0o750); err != nil {
		fail(err)
	}

	// Build (or accept) the infracost binary the cell subprocesses will
	// invoke. Prepending its dir to PATH ensures the model's `infracost ...`
	// commands hit a binary that matches the current repo state — important
	// because `--llm` is brand-new and any system-installed `infracost`
	// likely doesn't have it yet.
	cacheRoot := filepath.Join(*out, "..", ".cache")
	binPath, err := resolveInfracostBin(*infracostBin, cacheRoot)
	if err != nil {
		fail(err)
	}
	binDir := filepath.Dir(binPath)
	pathSep := string(os.PathListSeparator)
	if err := os.Setenv("PATH", binDir+pathSep+os.Getenv("PATH")); err != nil {
		fail(err)
	}
	fmt.Printf("Using infracost at %s (PATH-prepended for cell subprocesses)\n", binPath)

	cfg := runConfig{
		TargetRepo:    *targetRepo,
		TargetDir:     *targetDir,
		FixtureFile:   *fixtureFile,
		RefreshTarget: *refreshTarget,
		SkillURL:      *skillURL,
		Formats:       formatList,
		Model:         *model,
		ClaudeBin:     *claudeBin,
		MaxTurns:      *maxTurns,
		SandboxHome:   sandboxHome,
		PolicyContext: policyContext,
		InfracostBin:  binPath,
		OutDir:        *out,
		DryRun:        *dryRun,
		Question:      *question,
		Repeats:       *repeats,
	}

	if *rerunFailed != "" {
		prior, err := loadCellsJSONL(*rerunFailed)
		if err != nil {
			fail(fmt.Errorf("read --rerun-failed: %w", err))
		}
		// Only re-run cells where the run didn't produce useful data:
		// claude errored hard (Error set) OR no verdict computed (cell
		// terminated before reaching the verifier). Don't re-run
		// "ambiguous" verdicts — fix-task questions are intentionally
		// scored ambiguous (verifyTokenOnly), and they have valid cost /
		// token data.
		rerun := map[string]bool{}
		for _, c := range prior {
			if c.Error != "" || c.Verdict == "" {
				rerun[c.QuestionID+"/"+c.Format] = true
			}
		}
		if len(rerun) == 0 {
			fmt.Println("--rerun-failed: no failed cells found, nothing to do.")
			return
		}
		cfg.RerunCombos = rerun
		fmt.Printf("--rerun-failed: re-running %d (question, format) cells from %s\n", len(rerun), *rerunFailed)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Hour)
	defer cancel()

	if !*skipSanity && !*dryRun {
		client, err := newClaudeClient(cfg.ClaudeBin, cfg.Model)
		if err != nil {
			fail(err)
		}
		if err := runSanityCheck(ctx, client, sandboxHome); err != nil {
			fail(err)
		}
	}

	results, err := runAll(ctx, cfg)
	if err != nil {
		fail(err)
	}

	reportPath, err := writeReport(*out, results, cfg)
	if err != nil {
		fail(err)
	}
	if reportPath != "" {
		fmt.Printf("\nWrote %s (%d cells)\n", reportPath, len(results))
	}
}

func splitCSV(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "llmbench:", err)
	os.Exit(1)
}

func containsFormat(formats []string, target string) bool {
	for _, f := range formats {
		if f == target {
			return true
		}
	}
	return false
}

// loadCellsJSONL reads cells from one or more JSONL files. spec is a
// comma-separated list of paths or globs; matches are deduped, and we
// silently skip blank lines so partially-flushed files are tolerated.
func loadCellsJSONL(spec string) ([]Cell, error) {
	seen := map[string]bool{}
	var paths []string
	for _, p := range strings.Split(spec, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		matches, err := filepath.Glob(p)
		if err != nil {
			return nil, fmt.Errorf("glob %s: %w", p, err)
		}
		if len(matches) == 0 {
			// Treat as a literal path so we surface a useful "file not
			// found" if the user typoed a path.
			matches = []string{p}
		}
		for _, m := range matches {
			abs, err := filepath.Abs(m)
			if err != nil {
				return nil, err
			}
			if seen[abs] {
				continue
			}
			seen[abs] = true
			paths = append(paths, abs)
		}
	}

	var cells []Cell
	for _, p := range paths {
		f, err := os.Open(p) //nolint:gosec // p is a results jsonl path supplied via --rerun-failed flag by bench operator
		if err != nil {
			return nil, fmt.Errorf("open %s: %w", p, err)
		}
		dec := json.NewDecoder(f)
		for dec.More() {
			var c Cell
			if err := dec.Decode(&c); err != nil {
				_ = f.Close()
				return nil, fmt.Errorf("decode %s: %w", p, err)
			}
			cells = append(cells, c)
		}
		_ = f.Close()
	}
	return cells, nil
}

// uniqueFormats preserves the canonical ordering for report column layout.
func uniqueFormats(cells []Cell) []string {
	order := []string{"bare-tf", "skill-default", "skill-llm", "skill-json", "json", "llm"}
	seen := map[string]bool{}
	for _, c := range cells {
		seen[c.Format] = true
	}
	out := make([]string, 0, len(seen))
	for _, f := range order {
		if seen[f] {
			out = append(out, f)
			delete(seen, f)
		}
	}
	for f := range seen {
		out = append(out, f)
	}
	return out
}
