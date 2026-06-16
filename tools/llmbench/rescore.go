package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/infracost/cli/internal/format"
)

// runRescore reapplies each question's Verify function to the cells in an
// existing JSONL, writing a new JSONL with refreshed verdicts. Useful when
// the verifier itself changes — e.g. when extractInt is taught to handle
// new whitespace / quoting — and we want to retroactively correct prior
// runs without re-spending money on the model.
//
// Cells with empty FinalText (errored / max-turns) are preserved as-is so
// the "this cell didn't finish" signal isn't lost behind a verifier's
// default-Ambiguous return.
func runRescore(inPath, outPath, fixtureFile, targetRepo, targetDir, outDir string) error {
	cells, err := loadCellsJSONL(inPath)
	if err != nil {
		return fmt.Errorf("read --rescore: %w", err)
	}
	if len(cells) == 0 {
		return fmt.Errorf("--rescore: no cells found in %s", inPath)
	}

	fixture, fixturePath, err := loadFixtureForRescore(fixtureFile, targetRepo, targetDir, outDir)
	if err != nil {
		return err
	}
	fmt.Printf("Loaded fixture: %s\n", fixturePath)

	qByID := map[string]Question{}
	for _, q := range Questions() {
		qByID[q.ID] = q
	}

	var changes []rescoreChange
	missingQuestion := map[string]int{}

	for i := range cells {
		c := &cells[i]
		if c.FinalText == "" {
			// Preserve "errored / cut off" cells as-is — re-running Verify on
			// empty input would clobber the empty-verdict signal with Ambiguous.
			continue
		}
		q, ok := qByID[c.QuestionID]
		if !ok {
			missingQuestion[c.QuestionID]++
			continue
		}
		oldV := c.Verdict
		newV := q.Verify(c.FinalText, fixture)
		if oldV != newV {
			changes = append(changes, rescoreChange{
				question: c.QuestionID,
				format:   c.Format,
				old:      oldV,
				new:      newV,
				final:    truncateForLog(c.FinalText, 80),
			})
			c.Verdict = newV
		}
	}

	if outPath == "" {
		outPath = defaultRescoreOutPath(inPath)
	}
	if err := writeRescoredJSONL(outPath, cells); err != nil {
		return err
	}
	fmt.Printf("Wrote %s (%d cells)\n", outPath, len(cells))

	printRescoreSummary(cells, changes, missingQuestion)
	return nil
}

// loadFixtureForRescore returns the parsed fixture used to score answers.
// Prefers --fixture-file when set; otherwise derives the cached fixture
// path from --target-repo / --target-dir using the same slug rules as the
// main run path.
func loadFixtureForRescore(fixtureFile, targetRepo, targetDir, outDir string) (*format.Output, string, error) {
	path := fixtureFile
	if path == "" {
		var slug string
		switch {
		case targetDir != "":
			slug = slugify(filepath.Base(targetDir))
		case targetRepo != "":
			slug = slugifyURL(targetRepo)
		default:
			return nil, "", fmt.Errorf("--rescore needs a fixture: pass --fixture-file or set --target-repo/--target-dir so we can find the cached fixture")
		}
		cacheRoot := filepath.Join(outDir, "..", ".cache")
		path = filepath.Join(cacheRoot, "fixtures", slug+".json")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, "", err
	}
	raw, err := os.ReadFile(abs) //nolint:gosec // bench-internal cache path or operator-supplied flag
	if err != nil {
		return nil, abs, fmt.Errorf("read fixture %s: %w", abs, err)
	}
	var out format.Output
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, abs, fmt.Errorf("parse fixture %s: %w", abs, err)
	}
	return &out, abs, nil
}

func defaultRescoreOutPath(inPath string) string {
	dir := filepath.Dir(inPath)
	base := filepath.Base(inPath)
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	return filepath.Join(dir, stem+".rescored.jsonl")
}

func writeRescoredJSONL(path string, cells []Cell) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	f, err := os.Create(path) //nolint:gosec // path constructed from operator-supplied JSONL path
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	enc := json.NewEncoder(f)
	for _, c := range cells {
		if err := enc.Encode(c); err != nil {
			return err
		}
	}
	return nil
}

// rescoreChange records a single before/after verdict transition produced by
// re-running the verifier; declared at package level so we can pass slices
// around (Go disallows that for inline struct types).
type rescoreChange struct {
	question, format string
	old, new         Verdict
	final            string
}

// printRescoreSummary reports per-cell verdict diffs and the new aggregate
// breakdown so the operator can sanity-check the rescore at a glance.
func printRescoreSummary(cells []Cell, changes []rescoreChange, missingQuestion map[string]int) {
	if len(changes) == 0 {
		fmt.Println("No verdict changes.")
	} else {
		fmt.Printf("\nVerdict changes (%d):\n", len(changes))
		// Sort for stable output.
		sort.Slice(changes, func(i, j int) bool {
			if changes[i].format != changes[j].format {
				return changes[i].format < changes[j].format
			}
			return changes[i].question < changes[j].question
		})
		for _, ch := range changes {
			oldStr := string(ch.old)
			if oldStr == "" {
				oldStr = "<empty>"
			}
			newStr := string(ch.new)
			if newStr == "" {
				newStr = "<empty>"
			}
			fmt.Printf("  %-15s %-32s %s → %s   (final: %q)\n",
				ch.format, ch.question, oldStr, newStr, ch.final)
		}
	}

	if len(missingQuestion) > 0 {
		fmt.Printf("\nWARNING: %d cells reference unknown question IDs (skipped):\n", len(missingQuestion))
		for q, n := range missingQuestion {
			fmt.Printf("  %s × %d\n", q, n)
		}
	}

	// Aggregate breakdown after rescore — keyed by format.
	type bucket struct {
		correct, incorrect, ambiguous, empty int
	}
	by := map[string]*bucket{}
	for _, c := range cells {
		b := by[c.Format]
		if b == nil {
			b = &bucket{}
			by[c.Format] = b
		}
		switch c.Verdict {
		case Correct:
			b.correct++
		case Incorrect:
			b.incorrect++
		case Ambiguous:
			b.ambiguous++
		default: // empty
			b.empty++
		}
	}
	formats := make([]string, 0, len(by))
	for f := range by {
		formats = append(formats, f)
	}
	sort.Strings(formats)
	fmt.Println("\nNew verdict tallies:")
	fmt.Printf("  %-15s %8s %8s %8s %8s\n", "format", "correct", "wrong", "ambig", "empty")
	for _, f := range formats {
		b := by[f]
		fmt.Printf("  %-15s %8d %8d %8d %8d\n", f, b.correct, b.incorrect, b.ambiguous, b.empty)
	}
}

func truncateForLog(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
