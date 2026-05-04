package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type aggKey struct {
	target string
	format string
}

type aggCell struct {
	count                                              int
	totalIn, totalOut, totalCacheCreate, totalCacheRead int64
	totalWall                                          int64
	totalCost                                          float64
	correct, incorrect, ambiguous, errors, skipped     int
}

// writeReport renders a Markdown summary. Three sections:
//   - aggregate per (target, format)
//   - skill / json / llm vs the bare-tf baseline (does giving the model
//     infracost-via-skill or infracost-via-prompt actually help?)
//   - per-question accuracy across formats
func writeReport(dir string, cells []Cell, cfg runConfig) (string, error) {
	if len(cells) == 0 {
		return "", nil
	}
	path := filepath.Join(dir, fmt.Sprintf("report-%s.md", time.Now().Format("20060102-150405")))
	f, err := os.Create(path) //nolint:gosec // path constructed from bench output dir + timestamp, not user input
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()

	w := func(s string, args ...any) { _, _ = fmt.Fprintf(f, s, args...) }
	w("# llmbench report\n\n")
	w("- **model**: `%s`\n", cfg.Model)
	target := strings.TrimSpace(cfg.TargetDir)
	if target == "" {
		target = cfg.TargetRepo
	}
	w("- **target**: %s\n", target)
	w("- **formats**: %s\n", strings.Join(cfg.Formats, ", "))
	w("- **repeats**: %d\n", cfg.Repeats)
	w("- **max-turns**: %d\n", cfg.MaxTurns)
	w("- **harness**: `claude -p` (Claude Code subscription billing pool)\n")
	w("- **total cells**: %d\n\n", len(cells))

	groups := map[aggKey]*aggCell{}
	keys := []aggKey{}
	for _, c := range cells {
		k := aggKey{c.Target, c.Format}
		a, ok := groups[k]
		if !ok {
			a = &aggCell{}
			groups[k] = a
			keys = append(keys, k)
		}
		a.count++
		if c.Skipped {
			a.skipped++
			continue
		}
		if c.Error != "" {
			a.errors++
			continue
		}
		a.totalIn += int64(c.InputTokens)
		a.totalOut += int64(c.OutputTokens)
		a.totalCacheCreate += int64(c.CacheCreationInputTokens)
		a.totalCacheRead += int64(c.CacheReadInputTokens)
		a.totalWall += c.WallTimeMs
		a.totalCost += c.CostUSD
		switch c.Verdict {
		case Correct:
			a.correct++
		case Incorrect:
			a.incorrect++
		case Ambiguous:
			a.ambiguous++
		}
	}

	// Canonical format order: bare-tf baseline, then the skill variants.
	formatOrder := map[string]int{
		"bare-tf":       0,
		"skill-default": 1,
		"skill-llm":     2,
		"skill-json":    3,
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].target != keys[j].target {
			return keys[i].target < keys[j].target
		}
		return formatOrder[keys[i].format] < formatOrder[keys[j].format]
	})

	w("## Aggregate per (target, format)\n\n")
	w("Accuracy excludes ambiguous-verdict cells (e.g. fix-task questions, where token cost is the metric of interest) and error cells. Token / cost / wall averages are over cells that successfully returned data.\n\n")
	w("| target | format | n | errors | acc | input tok (avg) | output tok (avg) | cost (avg, USD) | wall ms (avg) |\n")
	w("|--------|--------|---|--------|-----|------------------|-------------------|------------------|----------------|\n")
	for _, k := range keys {
		a := groups[k]
		if a.skipped == a.count {
			w("| %s | %s | %d (skipped) | — | — | — | — | — | — |\n",
				k.target, k.format, a.count)
			continue
		}
		successful := a.count - a.skipped - a.errors
		errPart := fmt.Sprintf("%d/%d", a.errors, a.count-a.skipped)
		gradedDen := a.correct + a.incorrect
		acc := percentage(a.correct, gradedDen)
		if successful == 0 {
			w("| %s | %s | %d | %s | — | — | — | — | — |\n",
				k.target, k.format, a.count, errPart)
			continue
		}
		w("| %s | %s | %d | %s | %s | %.0f | %.0f | $%.4f | %.0f |\n",
			k.target, k.format, a.count, errPart, acc,
			float64(a.totalIn)/float64(successful),
			float64(a.totalOut)/float64(successful),
			a.totalCost/float64(successful),
			float64(a.totalWall)/float64(successful),
		)
	}

	// Each non-bare-tf format compared against bare-tf for the same target.
	w("\n## Formats vs bare-tf baseline\n\n")
	w("Compares each non-bare-tf cell against the bare-tf baseline (model gets only the Terraform source + bash + read; no infracost). Negative percentages = the comparison format used fewer tokens / less cost / less time than bare-tf for the same target.\n\n")
	w("| target | format | acc Δ (vs bare-tf) | input tok Δ | output tok Δ | cost Δ | wall ms Δ |\n")
	w("|--------|--------|--------------------|-------------|---------------|--------|------------|\n")
	bareKeys := []aggKey{}
	for k, a := range groups {
		if k.format == "bare-tf" && a.count > a.skipped+a.errors {
			bareKeys = append(bareKeys, k)
		}
	}
	sort.Slice(bareKeys, func(i, j int) bool { return bareKeys[i].target < bareKeys[j].target })
	for _, ref := range bareKeys {
		base := groups[ref]
		baseSucc := float64(base.count - base.skipped - base.errors)
		for _, k := range keys {
			if k.target != ref.target || k.format == "bare-tf" {
				continue
			}
			a := groups[k]
			succ := float64(a.count - a.skipped - a.errors)
			if succ == 0 {
				continue
			}
			w("| %s | %s | %s | %s | %s | %s | %s |\n",
				k.target, k.format,
				pctDelta(a.correct, a.correct+a.incorrect, base.correct, base.correct+base.incorrect),
				ratioDelta(float64(a.totalIn)/succ, float64(base.totalIn)/baseSucc),
				ratioDelta(float64(a.totalOut)/succ, float64(base.totalOut)/baseSucc),
				ratioDelta(a.totalCost/succ, base.totalCost/baseSucc),
				ratioDelta(float64(a.totalWall)/succ, float64(base.totalWall)/baseSucc),
			)
		}
	}

	// Per-question breakdown.
	w("\n## Per-question accuracy\n\n")
	allCols := []string{"bare-tf", "skill-default", "skill-llm", "skill-json"}
	seenCols := map[string]bool{}
	for _, c := range cells {
		seenCols[c.Format] = true
	}
	cols := make([]string, 0, len(allCols))
	for _, c := range allCols {
		if seenCols[c] {
			cols = append(cols, c)
		}
	}
	w("| target | question |")
	for _, c := range cols {
		w(" %s |", c)
	}
	w("\n|--------|----------|")
	for range cols {
		w("--------|")
	}
	w("\n")
	type qkey struct {
		target string
		qid    string
	}
	type qrow map[string]*aggCell
	qrows := map[qkey]qrow{}
	qids := []qkey{}
	for _, c := range cells {
		k := qkey{c.Target, c.QuestionID}
		if _, ok := qrows[k]; !ok {
			qrows[k] = qrow{}
			qids = append(qids, k)
		}
		a, ok := qrows[k][c.Format]
		if !ok {
			a = &aggCell{}
			qrows[k][c.Format] = a
		}
		a.count++
		if c.Skipped {
			a.skipped++
			continue
		}
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
	sort.Slice(qids, func(i, j int) bool {
		if qids[i].target != qids[j].target {
			return qids[i].target < qids[j].target
		}
		return qids[i].qid < qids[j].qid
	})
	for _, k := range qids {
		w("| %s | %s |", k.target, k.qid)
		for _, ck := range cols {
			a := qrows[k][ck]
			switch {
			case a == nil:
				w(" — |")
			case a.skipped == a.count:
				w(" skip |")
			case a.errors == a.count-a.skipped:
				w(" err |")
			case a.ambiguous == a.count-a.skipped-a.errors:
				w(" ~ |")
			default:
				w(" %d/%d |", a.correct, a.correct+a.incorrect)
			}
		}
		w("\n")
	}

	w("\nLegend: `~` = ambiguous-only (typically a fix-task question; check token / cost columns instead). `err` = every replicate hit an error. `skip` = format not run for this cell.\n")
	w("\nRaw cell-by-cell results were written alongside this report as `results-*.jsonl`.\n")
	return path, nil
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
