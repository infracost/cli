package toon_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/infracost/cli/internal/format"
	"github.com/infracost/cli/internal/format/fixturegen"
	"github.com/infracost/cli/internal/format/toon"
	"github.com/pkoukk/tiktoken-go"
)

// TestTokenBench encodes representative format.Output fixtures as JSON and
// TOON, then counts tokens with the OpenAI tiktoken vocabularies (cl100k_base
// for GPT-3.5/4-class models, o200k_base for GPT-4o-class). Anthropic does
// not publish a public tokenizer, so cl100k/o200k stand in as proxies.
//
// The test is informational — it always passes — but it prints a comparison
// table when run with -v. Run with:
//
//	go test -v -run TestTokenBench ./internal/format/toon/...
func TestTokenBench(t *testing.T) {
	cl100k, err := tiktoken.GetEncoding("cl100k_base")
	if err != nil {
		t.Fatalf("load cl100k_base: %v", err)
	}
	o200k, err := tiktoken.GetEncoding("o200k_base")
	if err != nil {
		t.Fatalf("load o200k_base: %v", err)
	}

	sizes := []fixturegen.Size{fixturegen.Small, fixturegen.Medium, fixturegen.Large}

	rows := make([]bench, 0, len(sizes))

	for _, sz := range sizes {
		out := fixturegen.Build(fixturegen.SpecFor(sz))

		jsonBuf, err := encodeJSON(out)
		if err != nil {
			t.Fatalf("%s json: %v", sz, err)
		}
		toonBuf, err := encodeTOON(out)
		if err != nil {
			t.Fatalf("%s toon: %v", sz, err)
		}

		r := bench{
			size:      sz,
			jsonBytes: len(jsonBuf),
			toonBytes: len(toonBuf),
			jsonCL:    len(cl100k.Encode(string(jsonBuf), nil, nil)),
			toonCL:    len(cl100k.Encode(string(toonBuf), nil, nil)),
			jsonO200:  len(o200k.Encode(string(jsonBuf), nil, nil)),
			toonO200:  len(o200k.Encode(string(toonBuf), nil, nil)),
		}
		rows = append(rows, r)
	}

	t.Log("\n" + renderBenchTable(rows))
}

// TestTOONJSONStructuralParity smoke-checks that --llm and --json carry the
// same field set on a representative fixture. The shapes differ (TOON's
// tabular form collapses uniform arrays of objects), but every leaf scalar in
// the JSON should appear textually somewhere in the TOON output. Catches
// regressions where a struct field accidentally drops out of TOON.
func TestTOONJSONStructuralParity(t *testing.T) {
	out := fixturegen.Build(fixturegen.SpecFor(fixturegen.Small))

	jsonBuf, err := encodeJSON(out)
	if err != nil {
		t.Fatalf("json: %v", err)
	}
	toonBuf, err := encodeTOON(out)
	if err != nil {
		t.Fatalf("toon: %v", err)
	}

	// Walk the JSON, collect every leaf string/number, and assert each one
	// appears somewhere in the TOON output.
	var doc any
	if err := json.Unmarshal(jsonBuf, &doc); err != nil {
		t.Fatalf("unmarshal json: %v", err)
	}
	missing := 0
	walkLeaves(doc, func(s string) {
		if s == "" {
			return
		}
		if !strings.Contains(string(toonBuf), s) {
			t.Errorf("leaf %q present in JSON but missing from TOON", s)
			missing++
			if missing > 5 {
				t.Fatalf("aborting: too many missing leaves")
			}
		}
	})
}

func encodeJSON(out *format.Output) ([]byte, error) {
	var buf bytes.Buffer
	if err := out.ToJSON(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func encodeTOON(out *format.Output) ([]byte, error) {
	var buf bytes.Buffer
	if err := out.ToTOON(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func walkLeaves(v any, visit func(string)) {
	switch x := v.(type) {
	case map[string]any:
		for _, vv := range x {
			walkLeaves(vv, visit)
		}
	case []any:
		for _, vv := range x {
			walkLeaves(vv, visit)
		}
	case string:
		visit(x)
	case float64:
		// JSON unmarshal gives float64. Not all numbers round-trip cleanly,
		// so we don't compare these — strings are the more reliable signal.
	case bool, nil:
	}
}

// Force toon import to be used in this test package (it's via ToTOON, but
// some build configs strip unreferenced imports).
var _ = toon.Marshal

func renderBenchTable(rows []bench) string {
	var b strings.Builder
	fmt.Fprintln(&b, "Token efficiency: --llm vs --json")
	fmt.Fprintln(&b, "")
	fmt.Fprintf(&b, "%-7s | %-22s | %-22s | %-22s\n",
		"size", "bytes", "cl100k_base tokens", "o200k_base tokens")
	fmt.Fprintln(&b, strings.Repeat("-", 84))
	for _, r := range rows {
		fmt.Fprintf(&b, "%-7s | %-22s | %-22s | %-22s\n",
			r.size,
			compare(r.jsonBytes, r.toonBytes),
			compare(r.jsonCL, r.toonCL),
			compare(r.jsonO200, r.toonO200),
		)
	}
	return b.String()
}

// bench is a row alias so the table renderer can be defined separately from
// the test that builds the data.
type bench = struct {
	size                               fixturegen.Size
	jsonBytes, toonBytes               int
	jsonCL, toonCL, jsonO200, toonO200 int
}

func compare(jsonV, toonV int) string {
	if jsonV == 0 {
		return fmt.Sprintf("%d / %d", jsonV, toonV)
	}
	delta := float64(jsonV-toonV) / float64(jsonV) * 100
	return fmt.Sprintf("%6d → %-6d (%+5.1f%%)", jsonV, toonV, -delta)
}
