package cmds

import (
	"fmt"

	"github.com/infracost/cli/internal/format"
	"github.com/infracost/cli/internal/inspect"
	"github.com/infracost/cli/internal/ui"
)

// printInspectHints prints a "What's next?" section pointing users at common
// inspect commands. Hints are filtered to only those that map to data
// actually present in the output (e.g. --failing is only suggested when
// there are failing policies).
func printInspectHints(data *format.Output) {
	resourceCount := 0
	hasFailingPolicy := false
	hasTriggeredGuardrail := false
	hasOverBudget := false
	providers := map[string]struct{}{}

	for _, p := range data.Projects {
		resourceCount += len(p.Resources)
		for _, r := range p.Resources {
			providers[inspect.InferProvider(r.Type)] = struct{}{}
		}
		for _, f := range p.FinopsResults {
			if len(f.FailingResources) > 0 {
				hasFailingPolicy = true
			}
		}
		for _, t := range p.TaggingResults {
			if len(t.FailingResources) > 0 {
				hasFailingPolicy = true
			}
		}
	}
	for _, gr := range data.GuardrailResults {
		if gr.Triggered {
			hasTriggeredGuardrail = true
		}
	}
	for _, br := range data.BudgetResults {
		if br.OverBudget {
			hasOverBudget = true
		}
	}

	type hint struct {
		cmd  string
		desc string
	}
	var hints []hint

	if resourceCount > 0 {
		hints = append(hints, hint{"infracost inspect --group-by resource", "Show every resource sorted by cost"})
	}
	if resourceCount > 10 {
		hints = append(hints, hint{"infracost inspect --top 10", "Show the 10 most expensive resources"})
	}
	if hasFailingPolicy {
		hints = append(hints,
			hint{"infracost inspect --failing", "Show only failing policies"},
			hint{"infracost inspect --group-by policy", "Group results by policy"},
			hint{"infracost inspect --policy <name>", "Drill into a specific policy"},
		)
	}
	if hasTriggeredGuardrail {
		hints = append(hints,
			hint{"infracost inspect --group-by guardrail", "Group results by guardrail"},
			hint{"infracost inspect --guardrail <name>", "Drill into a specific guardrail"},
		)
	}
	if hasOverBudget {
		hints = append(hints,
			hint{"infracost inspect --group-by budget", "Group results by budget"},
			hint{"infracost inspect --budget <name>", "Drill into a specific budget"},
		)
	}
	if len(providers) > 1 {
		hints = append(hints, hint{"infracost inspect --group-by provider", "Group results by provider"})
	}
	if resourceCount > 0 {
		hints = append(hints, hint{"infracost inspect --json", "Re-run with full JSON output"})
	}

	if len(hints) == 0 {
		return
	}

	fmt.Println()
	ui.Heading("What's next?")
	for _, h := range hints {
		ui.Stepf("%s  %s", ui.Accent(h.cmd), h.desc)
	}
}
