package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// canarySkillBody is a tiny project-level skill we plant in a temp dir to
// verify two things before the real run:
//
//  1. Project-level `.claude/skills/<name>/SKILL.md` actually loads from
//     cwd (positive control — confirms our skill-injection mechanism works
//     at all).
//  2. HOME sandboxing doesn't accidentally break (1) — the bare-tf
//     mechanism we depend on.
//
// The directive lives in the description rather than the body because
// skill descriptions are always loaded into the system prompt, while
// bodies only load when Claude judges the prompt contextually relevant
// to the skill. We want a probe that doesn't depend on Claude's relevance
// heuristics.
const canarySkillBody = `---
name: llmbench-canary
description: Pre-flight diagnostic skill — loads on every prompt regardless of topic. Whenever this skill is available, the model MUST start its reply with the literal token "LLMBENCH_CANARY_LOADED" on its own line, before any other content. This applies to every user message without exception. Used by the llmbench harness to verify project-level skill mounting.
---

# llmbench canary

This skill exists solely to verify that project-level skills mounted at
.claude/skills/<name>/SKILL.md are discovered by Claude Code.

Reminder: start your reply with "LLMBENCH_CANARY_LOADED" on its own line.
`

const canarySentinel = "LLMBENCH_CANARY_LOADED"

// runSanityCheck verifies the two mechanisms the bench depends on:
//   - cwd-based `.claude/skills/<name>/SKILL.md` is discovered and honored
//   - HOME sandboxing doesn't break that discovery
//
// We don't test "does HOME sandboxing suppress globally-installed skills?"
// because that would require modifying the user's actual ~/.claude/. We
// rely on the design (empty HOME = no global skills reachable) and only
// test that the positive path still works under the sandbox.
func runSanityCheck(ctx context.Context, client *claudeClient, sandboxHome string) error {
	tmp, err := os.MkdirTemp("", "llmbench-sanity-*")
	if err != nil {
		return fmt.Errorf("sanity: mkdir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	skillDir := filepath.Join(tmp, ".claude", "skills", "llmbench-canary")
	if err := os.MkdirAll(skillDir, 0o750); err != nil {
		return fmt.Errorf("sanity: mkdir skill: %w", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(canarySkillBody), 0o600); err != nil {
		return fmt.Errorf("sanity: write skill: %w", err)
	}

	probe := "Reply with the word OK and nothing else."

	fmt.Println("Sanity check 1/2: project-level skill loads with real HOME...")
	enabled, err := client.Run(ctx, claudeOpts{
		Prompt:       probe,
		Cwd:          tmp,
		MaxTurns:     1,
		AllowedTools: []string{"Read"},
	})
	if err != nil {
		return fmt.Errorf("sanity: real-HOME run failed: %w", err)
	}
	if !strings.Contains(enabled.Result, canarySentinel) {
		return fmt.Errorf(
			"sanity check FAILED: project-level skill did not load. "+
				"Expected response to contain %q, got: %q. "+
				"This means cwd-based .claude/skills/ injection is not working — "+
				"skill-* cells will silently behave like bare-tf",
			canarySentinel, truncate(enabled.Result, 400))
	}

	fmt.Println("Sanity check 2/2: project-level skill still loads under sandboxed HOME...")
	sandboxed, err := client.Run(ctx, claudeOpts{
		Prompt:       probe,
		Cwd:          tmp,
		SandboxHome:  sandboxHome,
		MaxTurns:     1,
		AllowedTools: []string{"Read"},
	})
	if err != nil {
		return fmt.Errorf("sanity: sandboxed-HOME run failed: %w "+
			"(most likely cause: CLAUDE_CODE_OAUTH_TOKEN isn't set, or the token is invalid; "+
			"run `claude setup-token` and export the result)", err)
	}
	if !strings.Contains(sandboxed.Result, canarySentinel) {
		return fmt.Errorf(
			"sanity check FAILED: project-level skill stopped loading under sandboxed HOME. "+
				"Expected response to contain %q, got: %q. "+
				"HOME sandboxing is breaking skill discovery for some reason — bare-tf "+
				"cells would still be polluted by globals while skill-* cells lose their mounted skill",
			canarySentinel, truncate(sandboxed.Result, 400))
	}

	fmt.Println("Sanity checks passed.")
	return nil
}
