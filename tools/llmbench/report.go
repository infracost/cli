package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/infracost/cli/internal/format/fixturegen"
)

type aggKey struct {
	size              fixturegen.Size
	format, condition string
}

type aggCell struct {
	count                                    int
	totalIn, totalOut, totalTools, totalWall int64
	correct, incorrect, ambiguous, errors    int
}

// writeReport renders a Markdown summary of the cells. The report focuses on
// the top-level questions: token cost per condition, accuracy per condition,
// and how often each format triggered tool use.
func writeReport(dir string, cells []Cell, cfg runConfig) (string, error) {
	if len(cells) == 0 {
		return "", nil
	}
	path := filepath.Join(dir, fmt.Sprintf("report-%s.md", time.Now().Format("20060102-150405")))
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	w := func(s string, args ...any) { fmt.Fprintf(f, s, args...) }
	w("# llmbench report\n\n")
	w("- **model**: `%s`\n", cfg.Model)
	w("- **sizes**: %s\n", joinSizes(cfg.Sizes))
	w("- **formats**: %s\n", strings.Join(cfg.Formats, ", "))
	w("- **conditions**: %s\n", strings.Join(cfg.Conditions, ", "))
	w("- **repeats**: %d\n", cfg.Repeats)
	w("- **total cells**: %d\n\n", len(cells))

	groups := map[aggKey]*aggCell{}
	keys := []aggKey{}
	for _, c := range cells {
		k := aggKey{c.Size, c.Format, c.Condition}
		a, ok := groups[k]
		if !ok {
			a = &aggCell{}
			groups[k] = a
			keys = append(keys, k)
		}
		a.count++
		a.totalIn += int64(c.InputTokens)
		a.totalOut += int64(c.OutputTokens)
		a.totalTools += int64(c.ToolCallCount)
		a.totalWall += c.WallTimeMs
		if c.Error != "" {
			a.errors++
			continue
		}
		switch c.Verdict {
		case Correct:
			a.correct++
		case Incorrect:
			a.incorrect++
		case Ambiguous:
			a.ambiguous++
		}
	}

	sort.Slice(keys, func(i, j int) bool {
		if keys[i].size != keys[j].size {
			return keys[i].size < keys[j].size
		}
		if keys[i].format != keys[j].format {
			return keys[i].format < keys[j].format
		}
		return keys[i].condition < keys[j].condition
	})

	w("## Aggregate per (size, format, condition)\n\n")
	w("| size | format | condition | n | acc | input tok (avg) | output tok (avg) | tool calls (avg) | wall ms (avg) |\n")
	w("|------|--------|-----------|---|-----|------------------|-------------------|-------------------|----------------|\n")
	for _, k := range keys {
		a := groups[k]
		acc := percentage(a.correct, a.count-a.errors)
		w("| %s | %s | %s | %d | %s | %.0f | %.0f | %.2f | %.0f |\n",
			k.size, k.format, k.condition, a.count, acc,
			float64(a.totalIn)/float64(a.count),
			float64(a.totalOut)/float64(a.count),
			float64(a.totalTools)/float64(a.count),
			float64(a.totalWall)/float64(a.count),
		)
	}

	// Side-by-side delta: --llm vs --json within each (size, condition).
	w("\n## --llm vs --json delta\n\n")
	w("Negative percentages mean --llm used fewer tokens / less time than --json in the same condition.\n\n")
	w("| size | condition | acc Δ (llm-json) | input tok Δ | output tok Δ | tool calls Δ | wall ms Δ |\n")
	w("|------|-----------|-------------------|-------------|---------------|----------------|------------|\n")
	pairKeys := pairUp(groups)
	for _, p := range pairKeys {
		j, t := groups[p.json], groups[p.toon]
		if j == nil || t == nil {
			continue
		}
		w("| %s | %s | %s | %s | %s | %s | %s |\n",
			p.size, p.condition,
			pctDelta(t.correct, t.count-t.errors, j.correct, j.count-j.errors),
			ratioDelta(float64(t.totalIn)/float64(t.count), float64(j.totalIn)/float64(j.count)),
			ratioDelta(float64(t.totalOut)/float64(t.count), float64(j.totalOut)/float64(j.count)),
			ratioDelta(float64(t.totalTools)/float64(t.count), float64(j.totalTools)/float64(j.count)),
			ratioDelta(float64(t.totalWall)/float64(t.count), float64(j.totalWall)/float64(j.count)),
		)
	}

	// Per-question breakdown so we can see where the format actually mattered.
	w("\n## Per-question accuracy\n\n")
	w("| size | question | json/no-tools | json/with-tools | llm/no-tools | llm/with-tools |\n")
	w("|------|----------|----------------|------------------|----------------|------------------|\n")
	type qkey struct {
		size fixturegen.Size
		qid  string
	}
	type qrow map[string]*aggCell // condition+format → aggCell
	qrows := map[qkey]qrow{}
	qids := []qkey{}
	for _, c := range cells {
		k := qkey{c.Size, c.QuestionID}
		if _, ok := qrows[k]; !ok {
			qrows[k] = qrow{}
			qids = append(qids, k)
		}
		ck := c.Format + "/" + c.Condition
		a, ok := qrows[k][ck]
		if !ok {
			a = &aggCell{}
			qrows[k][ck] = a
		}
		a.count++
		if c.Verdict == Correct {
			a.correct++
		}
	}
	sort.Slice(qids, func(i, j int) bool {
		if qids[i].size != qids[j].size {
			return qids[i].size < qids[j].size
		}
		return qids[i].qid < qids[j].qid
	})
	cols := []string{"json/no-tools", "json/with-tools", "llm/no-tools", "llm/with-tools"}
	for _, k := range qids {
		w("| %s | %s |", k.size, k.qid)
		for _, ck := range cols {
			a := qrows[k][ck]
			if a == nil {
				w(" — |")
			} else {
				w(" %d/%d |", a.correct, a.count)
			}
		}
		w("\n")
	}

	w("\nRaw cell-by-cell results were written alongside this report as `results-*.jsonl`.\n")
	return path, nil
}

type sizeCondPair struct {
	size       fixturegen.Size
	condition  string
	json, toon aggKey
}

// pairUp produces (json, toon) pairs for each (size, condition) seen.
func pairUp(groups map[aggKey]*aggCell) []sizeCondPair {
	seen := map[[2]string]bool{}
	out := []sizeCondPair{}
	for k := range groups {
		id := [2]string{string(k.size), k.condition}
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, sizeCondPair{
			size:      k.size,
			condition: k.condition,
			json:      aggKey{size: k.size, format: "json", condition: k.condition},
			toon:      aggKey{size: k.size, format: "llm", condition: k.condition},
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].size != out[j].size {
			return out[i].size < out[j].size
		}
		return out[i].condition < out[j].condition
	})
	return out
}

func percentage(num, denom int) string {
	if denom == 0 {
		return "—"
	}
	return fmt.Sprintf("%.0f%% (%d/%d)", 100*float64(num)/float64(denom), num, denom)
}

func pctDelta(tNum, tDen, jNum, jDen int) string {
	if tDen == 0 || jDen == 0 {
		return "—"
	}
	t := 100 * float64(tNum) / float64(tDen)
	j := 100 * float64(jNum) / float64(jDen)
	return fmt.Sprintf("%+.1fpp", t-j)
}

func ratioDelta(tVal, jVal float64) string {
	if jVal == 0 {
		return "—"
	}
	return fmt.Sprintf("%+.1f%%", 100*(tVal-jVal)/jVal)
}

func joinSizes(s []fixturegen.Size) string {
	out := make([]string, 0, len(s))
	for _, x := range s {
		out = append(out, string(x))
	}
	return strings.Join(out, ", ")
}
