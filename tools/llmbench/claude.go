package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// claudeClient shells out to the local `claude` CLI in -p (print) mode and
// parses its JSON output. This matches the actual harness developers use,
// auths via the user's existing Claude Code session, and is billed against
// the user's subscription pool rather than the raw API tier.
type claudeClient struct {
	binPath string
	model   string
}

func newClaudeClient(binPath, model string) (*claudeClient, error) {
	if binPath == "" {
		binPath = "claude"
	}
	resolved, err := exec.LookPath(binPath)
	if err != nil {
		return nil, fmt.Errorf("claude binary not found on PATH: %w", err)
	}
	return &claudeClient{binPath: resolved, model: model}, nil
}

// claudeOpts groups the per-cell parameters. Each cell results in exactly
// one `claude -p` invocation; the agentic loop happens inside Claude Code,
// not here.
type claudeOpts struct {
	Prompt             string
	Cwd                string
	AppendSystemPrompt string
	// SandboxHome, if set, overrides $HOME for the subprocess. Used by
	// bare-tf cells so the user's globally-installed skills (~/.claude/
	// skills/, plugin skills) can't leak into the "no infracost" baseline.
	SandboxHome  string
	MaxTurns     int
	AllowedTools []string
}

// ToolCallRecord captures one model→tool→model round-trip so we can
// inspect what commands the model actually ran. Input is preserved as raw
// JSON because the structure varies per tool (Bash takes `command`, Read
// takes `file_path`, etc.). Result is the textual output the tool returned.
type ToolCallRecord struct {
	Name    string          `json:"name"`
	Input   json.RawMessage `json:"input,omitempty"`
	Result  string          `json:"result"`
	IsError bool            `json:"is_error,omitempty"`
}

// claudeResult is the parsed view of one `claude -p --output-format
// stream-json` invocation. ToolCalls preserves the per-cell sequence of
// bash invocations so we can analyze what queries the model wrote (and
// where the inspect CLI fell short of what it wanted).
type claudeResult struct {
	SessionID    string  `json:"session_id"`
	Result       string  `json:"result"`
	TotalCostUSD float64 `json:"total_cost_usd"`
	Model        string  `json:"model"`
	StopReason   string  `json:"stop_reason"`
	Usage        struct {
		InputTokens              int `json:"input_tokens"`
		OutputTokens             int `json:"output_tokens"`
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
		CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	} `json:"usage"`

	ToolCalls  []ToolCallRecord `json:"tool_calls,omitempty"`
	WallTimeMs int64            `json:"-"`
}

// streamEvent is a single line of `--output-format stream-json` output.
// Most events are either `assistant` (model output, possibly with tool_use
// blocks), `user` (tool_result blocks fed back to the model), or `result`
// (final aggregated cell data). System events are silently skipped.
type streamEvent struct {
	Type         string         `json:"type"`
	Subtype      string         `json:"subtype,omitempty"`
	SessionID    string         `json:"session_id,omitempty"`
	Result       string         `json:"result,omitempty"`
	TotalCostUSD float64        `json:"total_cost_usd,omitempty"`
	Model        string         `json:"model,omitempty"`
	StopReason   string         `json:"stop_reason,omitempty"`
	Message      *streamMessage `json:"message,omitempty"`
	Usage        *struct {
		InputTokens              int `json:"input_tokens"`
		OutputTokens             int `json:"output_tokens"`
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
		CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	} `json:"usage,omitempty"`
}

type streamMessage struct {
	Content []streamContentBlock `json:"content"`
}

type streamContentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
}

// Run executes one `claude -p` invocation and returns the parsed result,
// including the full sequence of tool_use / tool_result pairs. A non-nil
// result may still accompany an error (e.g. the CLI exits non-zero on
// max-turns but emits valid stream events).
func (c *claudeClient) Run(ctx context.Context, opts claudeOpts) (*claudeResult, error) {
	args := []string{
		"-p", opts.Prompt,
		"--output-format", "stream-json",
		"--verbose", // required when output-format is stream-json
		"--model", c.model,
	}
	if opts.MaxTurns > 0 {
		args = append(args, "--max-turns", strconv.Itoa(opts.MaxTurns))
	}
	if opts.AppendSystemPrompt != "" {
		args = append(args, "--append-system-prompt", opts.AppendSystemPrompt)
	}
	if len(opts.AllowedTools) > 0 {
		args = append(args, "--allowedTools", strings.Join(opts.AllowedTools, ","))
	}

	start := time.Now()
	cmd := exec.CommandContext(ctx, c.binPath, args...)
	cmd.Dir = opts.Cwd
	cmd.Env = buildEnv(opts.SandboxHome)

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = io.MultiWriter(&stderr, os.Stderr)

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start claude: %w", err)
	}

	parsed, parseErr := parseStream(stdoutPipe)
	parsed.WallTimeMs = time.Since(start).Milliseconds()

	runErr := cmd.Wait()

	// If parseStream couldn't make sense of the stream and claude also
	// failed, surface both — usually the run error is the proximate cause
	// and the parse error is a downstream symptom.
	switch {
	case runErr != nil && parsed.Result == "" && len(parsed.ToolCalls) == 0:
		return parsed, fmt.Errorf("claude exited non-zero: %v; stderr: %s; parse: %v",
			runErr, trimOutput(stderr.String()), parseErr)
	case runErr != nil:
		return parsed, fmt.Errorf("claude exited non-zero: %v; stderr: %s",
			runErr, trimOutput(stderr.String()))
	case parseErr != nil:
		return parsed, fmt.Errorf("parse claude stream: %w", parseErr)
	}
	return parsed, nil
}

// parseStream consumes a stream-json stdout, returning the aggregated
// result. Tool_use blocks are matched to their tool_result counterparts by
// ID. Pending tool_uses without a matching tool_result (e.g. the cell
// terminated mid-loop) are still recorded with an empty Result so we
// preserve the "last thing the model tried to do" signal.
func parseStream(r io.Reader) (*claudeResult, error) {
	out := &claudeResult{}
	pending := map[string]ToolCallRecord{}

	scanner := bufio.NewScanner(r)
	// scan lines can be huge — cached system prompts + large tool results
	// (e.g., a `cat scan.json` of 8MB) come through verbatim.
	scanner.Buffer(make([]byte, 1<<20), 64<<20)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var ev streamEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			continue // skip lines we can't parse rather than fail the whole cell
		}

		switch ev.Type {
		case "assistant":
			if ev.Message == nil {
				continue
			}
			for _, b := range ev.Message.Content {
				if b.Type == "tool_use" {
					pending[b.ID] = ToolCallRecord{
						Name:  b.Name,
						Input: append([]byte(nil), b.Input...),
					}
				}
			}
		case "user":
			if ev.Message == nil {
				continue
			}
			for _, b := range ev.Message.Content {
				if b.Type != "tool_result" {
					continue
				}
				rec, ok := pending[b.ToolUseID]
				if !ok {
					rec = ToolCallRecord{}
				}
				rec.Result = extractToolResultText(b.Content)
				rec.IsError = b.IsError
				out.ToolCalls = append(out.ToolCalls, rec)
				delete(pending, b.ToolUseID)
			}
		case "result":
			out.SessionID = ev.SessionID
			out.Result = ev.Result
			out.TotalCostUSD = ev.TotalCostUSD
			out.Model = ev.Model
			out.StopReason = ev.StopReason
			if ev.Usage != nil {
				out.Usage.InputTokens = ev.Usage.InputTokens
				out.Usage.OutputTokens = ev.Usage.OutputTokens
				out.Usage.CacheCreationInputTokens = ev.Usage.CacheCreationInputTokens
				out.Usage.CacheReadInputTokens = ev.Usage.CacheReadInputTokens
			}
		}
	}
	// Flush pending tool_uses that never got a tool_result.
	for _, rec := range pending {
		out.ToolCalls = append(out.ToolCalls, rec)
	}
	if err := scanner.Err(); err != nil {
		return out, fmt.Errorf("scan: %w", err)
	}
	return out, nil
}

// extractToolResultText handles the two shapes a tool_result content can
// take: a bare JSON string, or an array of content blocks. We concatenate
// all `text` blocks and ignore other types (images, etc., which our tools
// don't produce anyway).
func extractToolResultText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err == nil {
		var sb strings.Builder
		for _, b := range blocks {
			if b.Type == "text" {
				sb.WriteString(b.Text)
			}
		}
		return sb.String()
	}
	return string(raw)
}

// buildEnv prepares the subprocess environment. By default we pass the
// caller's env through unchanged so claude inherits OAuth, PATH, etc. When
// sandboxHome is set we replace HOME with the sandbox path.
func buildEnv(sandboxHome string) []string {
	if sandboxHome == "" {
		return os.Environ()
	}
	out := make([]string, 0, len(os.Environ()))
	homeReplaced := false
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "HOME=") {
			out = append(out, "HOME="+sandboxHome)
			homeReplaced = true
			continue
		}
		out = append(out, kv)
	}
	if !homeReplaced {
		out = append(out, "HOME="+sandboxHome)
	}
	return out
}

func trimOutput(s string) string {
	const limit = 1024
	s = strings.TrimSpace(s)
	if len(s) > limit {
		return s[:limit] + "...[truncated]"
	}
	return s
}
