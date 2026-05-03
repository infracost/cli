package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const anthropicURL = "https://api.anthropic.com/v1/messages"

// anthropicClient wraps the Messages API. We hand-roll HTTP rather than pull
// in the SDK to keep the dep tree small for this one-off harness.
type anthropicClient struct {
	apiKey string
	model  string
	hc     *http.Client
}

func newAnthropicClient(apiKey, model string) *anthropicClient {
	return &anthropicClient{
		apiKey: apiKey,
		model:  model,
		hc:     &http.Client{Timeout: 5 * time.Minute},
	}
}

// messageRequest mirrors the Anthropic /v1/messages request shape. Only the
// fields we actually use are modeled.
type messageRequest struct {
	Model     string         `json:"model"`
	MaxTokens int            `json:"max_tokens"`
	System    string         `json:"system,omitempty"`
	Messages  []requestTurn  `json:"messages"`
	Tools     []toolDef      `json:"tools,omitempty"`
}

type requestTurn struct {
	Role    string         `json:"role"`
	Content []contentBlock `json:"content"`
}

type contentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
}

type toolDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}

type messageResponse struct {
	Content    []contentBlock `json:"content"`
	StopReason string         `json:"stop_reason"`
	Usage      usageInfo      `json:"usage"`
	Error      *apiError      `json:"error,omitempty"`
}

type usageInfo struct {
	InputTokens         int `json:"input_tokens"`
	OutputTokens        int `json:"output_tokens"`
	CacheCreationTokens int `json:"cache_creation_input_tokens"`
	CacheReadTokens     int `json:"cache_read_input_tokens"`
}

type apiError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

func (e *apiError) String() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("%s: %s", e.Type, e.Message)
}

// send fires a single request and returns the parsed response and the raw
// HTTP body for archival.
func (c *anthropicClient) send(ctx context.Context, req messageRequest) (*messageResponse, []byte, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, anthropicURL, bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", c.apiKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")

	resp, err := c.hc.Do(httpReq)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, raw, err
	}
	if resp.StatusCode/100 != 2 {
		return nil, raw, fmt.Errorf("anthropic %d: %s", resp.StatusCode, string(raw))
	}
	var parsed messageResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, raw, err
	}
	if parsed.Error != nil {
		return &parsed, raw, fmt.Errorf("anthropic error: %s", parsed.Error.String())
	}
	return &parsed, raw, nil
}

// queryDataTool defines a synthetic "query_data" tool that the model can use
// to filter or aggregate the supplied dataset. The host (this harness) loops
// through tool_use → tool_result turns to simulate a code-execution
// environment without requiring Anthropic's beta sandbox.
//
// The tool accepts a Python expression evaluated against `data` (the parsed
// fixture) and returns the repr of the result. We emulate the exec via
// canned responses keyed by the input string — see runWithTools.
//
// (We avoid Anthropic's native code-execution beta because it requires a
// separate beta header and adds infra dependency for an experiment.)
var queryDataTool = toolDef{
	Name:        "query_data",
	Description: "Run a small Python snippet against the dataset (assigned to the variable `data`) and return the repr of the result. Use this to filter, aggregate, or look up values in the data — much more reliable than reasoning by eye over large structures.",
	InputSchema: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"snippet": map[string]any{
				"type":        "string",
				"description": "Python expression or short script. The dataset is bound to `data`. Use `print(...)` to emit values; the captured stdout is returned to you.",
			},
		},
		"required": []string{"snippet"},
	},
}

// callMessages runs a request → response loop, returning the final assistant
// turn's text plus aggregated usage. If toolHandler is non-nil and the model
// requests tool use, the handler is invoked and the conversation continues
// until the model stops requesting tools or the loop limit is reached.
type toolCall struct {
	Name      string
	ToolUseID string
	Input     map[string]any
}

type toolResult struct {
	ToolUseID string
	Content   string
	IsError   bool
}

type runOutcome struct {
	FinalText        string
	InputTokens      int
	OutputTokens     int
	ToolCallCount    int
	StopReason       string
	WallTimeMs       int64
	ConversationRaw  []byte // last raw response body
}

func (c *anthropicClient) runConversation(
	ctx context.Context,
	system string,
	userText string,
	tools []toolDef,
	toolHandler func(ctx context.Context, call toolCall) toolResult,
	maxTurns int,
) (*runOutcome, error) {
	if maxTurns <= 0 {
		maxTurns = 8
	}
	start := time.Now()
	messages := []requestTurn{
		{Role: "user", Content: []contentBlock{{Type: "text", Text: userText}}},
	}
	out := &runOutcome{}

	for turn := 0; turn < maxTurns; turn++ {
		req := messageRequest{
			Model:     c.model,
			MaxTokens: 1024,
			System:    system,
			Messages:  messages,
			Tools:     tools,
		}
		resp, raw, err := c.send(ctx, req)
		if err != nil {
			return out, err
		}
		out.ConversationRaw = raw
		out.InputTokens += resp.Usage.InputTokens
		out.OutputTokens += resp.Usage.OutputTokens
		out.StopReason = resp.StopReason

		// Append the assistant's turn to the conversation verbatim.
		messages = append(messages, requestTurn{Role: "assistant", Content: resp.Content})

		// If the model didn't request tools, we're done.
		if resp.StopReason != "tool_use" || toolHandler == nil {
			out.FinalText = collectText(resp.Content)
			out.WallTimeMs = time.Since(start).Milliseconds()
			return out, nil
		}

		// Otherwise, execute every tool_use block and append a single user
		// turn containing all tool_result blocks.
		var results []contentBlock
		for _, blk := range resp.Content {
			if blk.Type != "tool_use" {
				continue
			}
			out.ToolCallCount++
			var input map[string]any
			if len(blk.Input) > 0 {
				_ = json.Unmarshal(blk.Input, &input)
			}
			tr := toolHandler(ctx, toolCall{Name: blk.Name, ToolUseID: blk.ID, Input: input})
			contentJSON, _ := json.Marshal(tr.Content)
			results = append(results, contentBlock{
				Type:      "tool_result",
				ToolUseID: tr.ToolUseID,
				Content:   contentJSON,
			})
		}
		messages = append(messages, requestTurn{Role: "user", Content: results})
	}

	return out, fmt.Errorf("exceeded max turns (%d)", maxTurns)
}

func collectText(blocks []contentBlock) string {
	var b strings.Builder
	for _, blk := range blocks {
		if blk.Type == "text" {
			b.WriteString(blk.Text)
		}
	}
	return b.String()
}
