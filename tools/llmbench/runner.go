package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type runConfig struct {
	// Target acquisition
	TargetRepo    string
	TargetDir     string
	FixtureFile   string
	RefreshTarget bool
	SkillURL      string

	// Bench dimensions
	Formats  []string // any of: "bare-tf", "skill-default", "skill-llm", "skill-json"
	Question string
	Repeats  int

	// Claude Code harness
	Model     string
	ClaudeBin string
	MaxTurns  int
	// InfracostBin is the absolute path to the infracost binary that
	// cells should invoke. Threaded into the SKILL.md preamble so the
	// model never types bare `infracost` (which could resolve to a
	// system-installed legacy version that lacks the new flags).
	InfracostBin string
	// SandboxHome is a directory we pass as HOME to bare-tf cells so the
	// user's globally-installed skills (~/.claude/skills/, plugins, etc.)
	// can't leak into the "no infracost" baseline. Created once at startup,
	// reused across cells.
	SandboxHome string
	// PolicyContext, if non-empty, is the human-readable policy text the
	// user wants the bench to evaluate against. We inject it into bare-tf
	// prompts only — skill cells already see policies via infracost. Without
	// this, bare-tf has to invent the policy and the comparison degenerates
	// to "Claude can't read your mind".
	PolicyContext string

	// RerunCombos, if non-nil, filters runAll to only the cells whose
	// "<question_id>/<format>" key is in the map. Used by --rerun-failed
	// to re-execute exactly the cells that failed in a prior JSONL,
	// typically with a higher --max-turns ceiling.
	RerunCombos map[string]bool

	// Output
	OutDir string
	DryRun bool
}

// Cell is one run of (target × question × format × replicate). Every cell
// shells out to `claude -p` with Bash + Read tools available.
type Cell struct {
	Target     string `json:"target"`
	QuestionID string `json:"question_id"`
	Category   string `json:"category"`
	Format     string `json:"format"`
	Replicate  int    `json:"replicate"`

	InputTokens              int              `json:"input_tokens"`
	OutputTokens             int              `json:"output_tokens"`
	CacheCreationInputTokens int              `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int              `json:"cache_read_input_tokens"`
	CostUSD                  float64          `json:"total_cost_usd"`
	WallTimeMs               int64            `json:"wall_time_ms"`
	StopReason               string           `json:"stop_reason"`
	Verdict                  Verdict          `json:"verdict"`
	FinalText                string           `json:"final_text"`
	Error                    string           `json:"error,omitempty"`
	Skipped                  bool             `json:"skipped,omitempty"`
	ToolCalls                []ToolCallRecord `json:"tool_calls,omitempty"`

	PromptBytes int `json:"prompt_bytes"`
}

// validFormats is the set of cell formats the harness understands.
var validFormats = map[string]bool{
	"bare-tf":       true,
	"skill-default": true,
	"skill-llm":     true,
	"skill-json":    true,
}

// buildBareTFOrSkillPrompt produces the user-message prompt for a cell.
// For bare-tf with a configured policy context, we prepend the policy text
// so the model knows what to evaluate against — without it, "how many
// resources fail tagging policy?" is asking the model to invent the
// policy. Skill cells get policy data via infracost and don't need it
// repeated.
func buildBareTFOrSkillPrompt(formatName, question, policyContext string) string {
	if formatName == "bare-tf" && policyContext != "" {
		return fmt.Sprintf(
			"You are evaluating a Terraform project against the following policies. Use bash to inspect the source files in your current working directory.\n\n--- POLICIES ---\n%s\n--- END POLICIES ---\n\nQuestion: %s",
			policyContext, question,
		)
	}
	return question
}

func runAll(ctx context.Context, cfg runConfig) ([]Cell, error) {
	for _, f := range cfg.Formats {
		if !validFormats[f] {
			return nil, fmt.Errorf("unknown format %q (valid: bare-tf, skill-default, skill-llm, skill-json)", f)
		}
	}

	target, err := resolveTarget(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("resolve target: %w", err)
	}

	questions := FilterQuestions(Questions(), cfg.Question)
	if cfg.DryRun {
		printDryRun(target, questions, cfg)
		return nil, nil
	}

	client, err := newClaudeClient(cfg.ClaudeBin, cfg.Model)
	if err != nil {
		return nil, err
	}

	// Open the JSONL file up-front and flush each cell as soon as it
	// completes, so a Ctrl-C mid-run preserves whatever was already
	// finished. The full results slice is still returned for the report
	// generator.
	jsonlPath, jsonlEncoder, jsonlClose, err := openCellsJSONL(cfg.OutDir)
	if err != nil {
		return nil, err
	}
	defer jsonlClose()
	fmt.Println("Streaming cells to", jsonlPath)

	results := []Cell{}
	for _, q := range questions {
		for _, f := range cfg.Formats {
			if cfg.RerunCombos != nil && !cfg.RerunCombos[q.ID+"/"+f] {
				continue
			}
			for r := 0; r < cfg.Repeats; r++ {
				cell := runCell(ctx, client, target, q, f, r, cfg.MaxTurns, cfg.SandboxHome, cfg.PolicyContext)
				results = append(results, cell)
				printProgress(cell)
				if err := jsonlEncoder.Encode(cell); err != nil {
					fmt.Fprintln(os.Stderr, "warning: failed to write cell to jsonl:", err)
				}
			}
		}
	}
	return results, nil
}

func runCell(
	ctx context.Context,
	client *claudeClient,
	target *Target,
	q Question,
	formatName string,
	replicate, maxTurns int,
	sandboxHome, policyContext string,
) Cell {
	cell := Cell{
		Target:     target.Slug,
		QuestionID: q.ID,
		Category:   q.Category,
		Format:     formatName,
		Replicate:  replicate,
	}

	prompt := buildBareTFOrSkillPrompt(formatName, q.Prompt, policyContext)
	cell.PromptBytes = len(prompt)

	cwd, cleanup, err := setupCellCwd(target, formatName)
	if err != nil {
		cell.Error = err.Error()
		return cell
	}
	defer cleanup()

	opts := claudeOpts{
		Prompt:   prompt,
		Cwd:      cwd,
		MaxTurns: maxTurns,
		// Bash + Read covers the realistic developer surface: model can
		// run `infracost ...`, cat .tf files, jq scan output, etc. We
		// don't auto-approve Edit/Write since none of our questions ask
		// the model to modify the project.
		AllowedTools: []string{"Bash", "Read"},
	}
	if formatName == "bare-tf" {
		// bare-tf only: sandbox HOME so the user's globally-installed
		// skills (~/.claude/skills/, plugin skills) can't leak. Skill-*
		// cells keep the real HOME so infracost CLI auth works.
		opts.SandboxHome = sandboxHome
	}

	heartbeatDone := make(chan struct{})
	go heartbeat(heartbeatDone, fmt.Sprintf("%s/%s/%s rep=%d", target.Slug, q.ID, formatName, replicate))
	res, err := client.Run(ctx, opts)
	close(heartbeatDone)

	if res != nil {
		cell.InputTokens = res.Usage.InputTokens
		cell.OutputTokens = res.Usage.OutputTokens
		cell.CacheCreationInputTokens = res.Usage.CacheCreationInputTokens
		cell.CacheReadInputTokens = res.Usage.CacheReadInputTokens
		cell.CostUSD = res.TotalCostUSD
		cell.WallTimeMs = res.WallTimeMs
		cell.StopReason = res.StopReason
		cell.FinalText = res.Result
		cell.ToolCalls = res.ToolCalls
	}
	// Treat as a hard error only if claude returned no usable answer.
	// `claude -p` exits non-zero when --max-turns is reached, but the JSON
	// it emits still contains the model's last message — verifying that
	// gives us the "answered partially before being cut off" signal rather
	// than dumping the cell into the error bucket and discarding the work.
	if err != nil && cell.FinalText == "" {
		cell.Error = err.Error()
	}
	if cell.FinalText != "" {
		cell.Verdict = q.Verify(cell.FinalText, target.Fixture)
	}
	return cell
}

// setupCellCwd materializes a per-cell working directory: a fresh copy of
// the target Terraform tree, plus (for skill-* formats) a project-level
// `.claude/skills/infracost-scan/SKILL.md` whose content depends on the
// variant. Project-level skills take precedence over personal/global
// installs per Claude Code's discovery rules, so we get reproducible skill
// behavior regardless of what the user has installed at ~/.claude.
//
// For bare-tf, no skill dir is written and the cell runs with
// --disable-slash-commands to suppress any global skill auto-load.
func setupCellCwd(target *Target, formatName string) (string, func(), error) {
	tmp, err := os.MkdirTemp("", "llmbench-cell-*")
	if err != nil {
		return "", func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(tmp) }

	if err := copyDir(target.Dir, tmp); err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("copy repo: %w", err)
	}

	if strings.HasPrefix(formatName, "skill-") {
		skillBody, err := target.SkillVariant(formatName)
		if err != nil {
			cleanup()
			return "", func() {}, err
		}
		skillDir := filepath.Join(tmp, ".claude", "skills", "infracost-scan")
		if err := os.MkdirAll(skillDir, 0o750); err != nil {
			cleanup()
			return "", func() {}, err
		}
		if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skillBody), 0o600); err != nil {
			cleanup()
			return "", func() {}, err
		}
	}

	return tmp, cleanup, nil
}

// copyDir performs a recursive copy via the system `cp -R`. Faster and more
// faithful (perms, symlinks) than a hand-rolled walker for the small repos
// the bench operates on. If we ever target a giant Terraform tree, swap to
// hardlinks or a shared base dir.
func copyDir(src, dst string) error {
	if src == "" || dst == "" {
		return fmt.Errorf("copyDir: empty path")
	}
	// Trailing /. on src causes cp to copy contents into dst rather than
	// nesting src under it.
	cmd := exec.Command("cp", "-R", src+"/.", dst) //nolint:gosec // src/dst are bench-internal paths from caller
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("cp -R: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// openCellsJSONL creates the timestamped JSONL file for this run and
// returns an encoder + close-fn. Callers Encode each Cell as it completes
// so a kill mid-run still leaves a partial-but-valid file behind.
func openCellsJSONL(dir string) (string, *json.Encoder, func(), error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", nil, nil, err
	}
	path := filepath.Join(dir, fmt.Sprintf("results-%s.jsonl", time.Now().Format("20060102-150405")))
	f, err := os.Create(path) //nolint:gosec // path constructed from bench output dir + timestamp, not user input
	if err != nil {
		return "", nil, nil, err
	}
	return path, json.NewEncoder(f), func() { _ = f.Close() }, nil
}

func printDryRun(target *Target, questions []Question, cfg runConfig) {
	fmt.Println("DRY RUN — no claude calls will be made.")
	fmt.Printf("Target: %s\n  dir: %s\n  fixture: %s\n  skill source: %d bytes\n",
		target.Slug, target.Dir, target.FixturePath, len(target.SkillSource))
	for _, f := range cfg.Formats {
		if strings.HasPrefix(f, "skill-") {
			body, err := target.SkillVariant(f)
			if err != nil {
				fmt.Printf("  skill variant %s: error %v\n", f, err)
				continue
			}
			fmt.Printf("  skill variant %s: %d bytes\n", f, len(body))
		}
	}
	for _, q := range questions {
		for _, f := range cfg.Formats {
			fmt.Printf("[%s/%s] %s\n", q.ID, f, q.Prompt)
		}
	}
}

func printProgress(c Cell) {
	if c.Skipped {
		fmt.Printf("[%s/%s/%s] skipped\n", c.Target, c.QuestionID, c.Format)
		return
	}
	tag := string(c.Verdict)
	if c.Error != "" {
		tag = "error: " + truncate(c.Error, 200)
	}
	// `in` is uncached input only; cache_read is what Claude Code pulled
	// from its prompt cache. Both bill (cache reads at ~10% of standard
	// input rate). Showing both keeps token-comparison sane across cells
	// where caching kicks in differently.
	fmt.Printf("[%s/%s/%s rep=%d] in=%d cache_read=%d out=%d cost=$%.4f wall=%dms verdict=%s\n",
		c.Target, c.QuestionID, c.Format, c.Replicate,
		c.InputTokens, c.CacheReadInputTokens, c.OutputTokens, c.CostUSD, c.WallTimeMs, tag)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// heartbeat prints elapsed-time tick lines while a cell runs so the user
// can tell the bench is alive vs. wedged. Stops when done is closed.
func heartbeat(done <-chan struct{}, label string) {
	start := time.Now()
	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-done:
			return
		case t := <-tick.C:
			fmt.Printf("  ... [%s] still running (%.0fs)\n", label, t.Sub(start).Seconds())
		}
	}
}
