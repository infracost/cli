package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/infracost/cli/internal/format"
	"github.com/infracost/cli/internal/format/fixturegen"
	"github.com/infracost/cli/internal/format/toon"
)

type runConfig struct {
	Sizes      []fixturegen.Size
	Formats    []string // "json" | "llm"
	Conditions []string // "no-tools" | "with-tools"
	Model      string
	OutDir     string
	DryRun     bool
	Question   string
	Repeats    int
	APIKey     string
}

// Cell is one combination: size × question × format × condition × replicate.
type Cell struct {
	Size       fixturegen.Size `json:"size"`
	QuestionID string          `json:"question_id"`
	Category   string          `json:"category"`
	Format     string          `json:"format"`
	Condition  string          `json:"condition"`
	Replicate  int             `json:"replicate"`

	InputTokens   int     `json:"input_tokens"`
	OutputTokens  int     `json:"output_tokens"`
	ToolCallCount int     `json:"tool_call_count"`
	WallTimeMs    int64   `json:"wall_time_ms"`
	StopReason    string  `json:"stop_reason"`
	Verdict       Verdict `json:"verdict"`
	FinalText     string  `json:"final_text"`
	Error         string  `json:"error,omitempty"`

	// Bookkeeping for cost estimation; we don't apply pricing here because
	// it changes faster than the report.
	PromptBytes int `json:"prompt_bytes"`
}

func runAll(ctx context.Context, cfg runConfig) ([]Cell, error) {
	fixtures := BuildFixtures(cfg.Sizes)
	questions := FilterQuestions(Questions(), cfg.Question)

	if cfg.DryRun {
		printDryRun(fixtures, questions, cfg)
		return nil, nil
	}

	client := newAnthropicClient(cfg.APIKey, cfg.Model)
	results := []Cell{}

	for _, sf := range fixtures {
		jsonPrompt, toonPrompt, err := encodeFixture(sf.Fixture)
		if err != nil {
			return results, fmt.Errorf("encode %s: %w", sf.Size, err)
		}
		// The tool always operates on JSON-parsed data; the format only
		// affects what the model sees in the prompt.
		dataForTool := jsonPrompt

		for _, q := range questions {
			for _, f := range cfg.Formats {
				prompt := jsonPrompt
				if f == "llm" {
					prompt = toonPrompt
				}
				for _, cond := range cfg.Conditions {
					for r := 0; r < cfg.Repeats; r++ {
						cell := Cell{
							Size:        sf.Size,
							QuestionID:  q.ID,
							Category:    q.Category,
							Format:      f,
							Condition:   cond,
							Replicate:   r,
							PromptBytes: len(prompt),
						}
						out, err := runOneCell(ctx, client, prompt, dataForTool, q, f, cond)
						if err != nil {
							cell.Error = err.Error()
						}
						if out != nil {
							cell.InputTokens = out.InputTokens
							cell.OutputTokens = out.OutputTokens
							cell.ToolCallCount = out.ToolCallCount
							cell.WallTimeMs = out.WallTimeMs
							cell.StopReason = out.StopReason
							cell.FinalText = out.FinalText
							cell.Verdict = q.Verify(out.FinalText, sf.Fixture)
						}
						results = append(results, cell)
						printProgress(cell)
					}
				}
			}
		}
	}

	if err := saveCellsJSONL(cfg.OutDir, results); err != nil {
		return results, err
	}
	return results, nil
}

func runOneCell(
	ctx context.Context,
	client *anthropicClient,
	prompt, dataJSON string,
	q Question,
	formatName, condition string,
) (*runOutcome, error) {
	system := buildSystem(formatName)
	userMsg := buildUserMessage(prompt, formatName, q.Prompt)

	if condition == "no-tools" {
		return client.runConversation(ctx, system, userMsg, nil, nil, 1)
	}

	handler := func(ctx context.Context, call toolCall) toolResult {
		if call.Name != queryDataTool.Name {
			return toolResult{ToolUseID: call.ToolUseID, Content: "unknown tool", IsError: true}
		}
		snippet, _ := call.Input["snippet"].(string)
		out, err := runPython(ctx, snippet, dataJSON)
		if err != nil {
			return toolResult{ToolUseID: call.ToolUseID, Content: fmt.Sprintf("error: %v\nstdout: %s", err, out), IsError: true}
		}
		// Truncate enormous outputs so a runaway tool call doesn't blow our
		// token budget. 4 KB is plenty for an aggregate or filter.
		if len(out) > 4096 {
			out = out[:4096] + "\n...[truncated]"
		}
		return toolResult{ToolUseID: call.ToolUseID, Content: out}
	}

	return client.runConversation(ctx, system, userMsg, []toolDef{queryDataTool}, handler, 8)
}

func buildSystem(formatName string) string {
	if formatName == "llm" {
		return "You are answering analyst questions about an infracost cloud-cost scan. The data is provided in a compact, indentation-based text format. Arrays are declared as `key[N]:` and tabular arrays as `key[N]{f1,f2}:` followed by comma-separated rows at the next indent level. Be concise."
	}
	return "You are answering analyst questions about an infracost cloud-cost scan. The data is provided as JSON. Be concise."
}

func buildUserMessage(prompt, formatName, question string) string {
	header := "Here is the scan output (" + strings.ToUpper(formatName) + "):"
	return fmt.Sprintf("%s\n\n```\n%s\n```\n\nQuestion: %s", header, prompt, question)
}

// runPython executes the model-supplied snippet with `data` bound to the
// parsed JSON. We use a fresh subprocess per call. Everything is captured
// stdout so the model sees only what it asked for via print().
func runPython(ctx context.Context, snippet, dataJSON string) (string, error) {
	if !strings.Contains(snippet, "data") {
		// The model sometimes returns a one-liner without referencing data;
		// allow it but warn so we can flag bad tool calls in the report.
	}
	wrapper := fmt.Sprintf(`
import json, sys
data = json.loads(sys.stdin.read())
%s
`, snippet)

	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, "python3", "-c", wrapper)
	cmd.Stdin = strings.NewReader(dataJSON)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	out := stdout.String()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return out + stderr.String(), fmt.Errorf("python exit %d", ee.ExitCode())
		}
		return out + stderr.String(), err
	}
	return out, nil
}

// encodeFixture renders a fixture in both formats; we share the work since
// each (size × question) pair uses both.
func encodeFixture(out *format.Output) (string, string, error) {
	var jbuf, tbuf bytes.Buffer
	if err := out.ToJSON(&jbuf); err != nil {
		return "", "", err
	}
	if err := toon.MarshalTo(&tbuf, out); err != nil {
		return "", "", err
	}
	return jbuf.String(), tbuf.String(), nil
}

func saveCellsJSONL(dir string, cells []Cell) error {
	path := filepath.Join(dir, fmt.Sprintf("results-%s.jsonl", time.Now().Format("20060102-150405")))
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, c := range cells {
		if err := enc.Encode(c); err != nil {
			return err
		}
	}
	fmt.Println("Wrote", path)
	return nil
}

func printDryRun(fixtures []SizeFixture, questions []Question, cfg runConfig) {
	fmt.Println("DRY RUN — no API calls will be made.")
	for _, sf := range fixtures {
		jb, tb, err := encodeFixture(sf.Fixture)
		if err != nil {
			fmt.Println("encode error:", err)
			continue
		}
		fmt.Printf("\n=== fixture %s: json=%d bytes, llm=%d bytes ===\n", sf.Size, len(jb), len(tb))
		for _, q := range questions {
			for _, f := range cfg.Formats {
				prompt := jb
				if f == "llm" {
					prompt = tb
				}
				fmt.Printf("\n[%s/%s/%s] %s\n", sf.Size, q.ID, f, q.Prompt)
				fmt.Println("  prompt size:", len(prompt), "bytes")
			}
		}
	}
}

func printProgress(c Cell) {
	tag := string(c.Verdict)
	if c.Error != "" {
		tag = "error: " + c.Error
	}
	fmt.Printf("[%s/%s/%s/%s rep=%d] in=%d out=%d tools=%d wall=%dms verdict=%s\n",
		c.Size, c.QuestionID, c.Format, c.Condition, c.Replicate,
		c.InputTokens, c.OutputTokens, c.ToolCallCount, c.WallTimeMs, tag)
}
