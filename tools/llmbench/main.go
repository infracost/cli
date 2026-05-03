// Command llmbench measures end-to-end LLM cost and accuracy on infracost
// outputs encoded as JSON vs the compact --llm format, with and without
// code-execution tools.
//
// It builds the same fixtures the static token benchmark uses
// (internal/format/fixturegen), encodes them in both formats, and asks a set
// of programmatically-scoreable questions in four conditions:
//
//	JSON, no tools
//	JSON, with code-execution tool
//	--llm, no tools
//	--llm, with code-execution tool
//
// For each cell it records input/output tokens, tool-call count, wall time,
// and whether the model's answer was correct, and writes a Markdown report.
//
// This harness is NOT run as part of `go test` because it requires an
// Anthropic API key and incurs real cost. Run it manually:
//
//	ANTHROPIC_API_KEY=sk-... go run ./tools/llmbench --sizes small,medium
//
// Use --dry-run to print prompts without calling the API.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/infracost/cli/internal/format/fixturegen"
)

func main() {
	var (
		sizesFlag = flag.String("sizes", "small,medium", "comma-separated fixture sizes (small, medium, large)")
		formats   = flag.String("formats", "json,llm", "comma-separated formats (json, llm)")
		conds     = flag.String("conditions", "no-tools,with-tools", "comma-separated tool conditions")
		model     = flag.String("model", "claude-haiku-4-5-20251001", "Anthropic model ID")
		out       = flag.String("out", "tools/llmbench/results", "output directory for results and report")
		dryRun    = flag.Bool("dry-run", false, "print prompts without calling the API")
		question  = flag.String("question", "", "filter to questions whose ID contains this substring")
		repeats   = flag.Int("repeats", 1, "repeat each cell N times to average out variance")
	)
	flag.Parse()

	sizes, err := parseSizes(*sizesFlag)
	if err != nil {
		fail(err)
	}
	formatList := splitCSV(*formats)
	condList := splitCSV(*conds)

	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" && !*dryRun {
		fail(fmt.Errorf("ANTHROPIC_API_KEY environment variable must be set (or use --dry-run)"))
	}

	if err := os.MkdirAll(*out, 0o755); err != nil {
		fail(err)
	}

	cfg := runConfig{
		Sizes:      sizes,
		Formats:    formatList,
		Conditions: condList,
		Model:      *model,
		OutDir:     *out,
		DryRun:     *dryRun,
		Question:   *question,
		Repeats:    *repeats,
		APIKey:     apiKey,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	results, err := runAll(ctx, cfg)
	if err != nil {
		fail(err)
	}

	reportPath, err := writeReport(*out, results, cfg)
	if err != nil {
		fail(err)
	}
	fmt.Printf("\nWrote %s (%d cells)\n", reportPath, len(results))
}

func parseSizes(s string) ([]fixturegen.Size, error) {
	parts := splitCSV(s)
	out := make([]fixturegen.Size, 0, len(parts))
	for _, p := range parts {
		switch fixturegen.Size(p) {
		case fixturegen.Small, fixturegen.Medium, fixturegen.Large:
			out = append(out, fixturegen.Size(p))
		default:
			return nil, fmt.Errorf("unknown size %q (expected small, medium, large)", p)
		}
	}
	return out, nil
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
