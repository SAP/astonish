# Code-mode investigation approach

Astonish Code already finds the right files quickly (codegraph, grep, definitions). Failures on regressions were **how evidence was treated**, not missing exploration. This document is the product contract for that approach. Do not change the codegraph-first rule or GRAPH-phase allow-list to "fix" investigation quality.

## Three layers

1. **Always-on Work Policy** in the code-mode system prompt (`pkg/agent/code_system_prompt_builder.go`): short bullets — user restatements are spec, drop a contradicted hypothesis, claim fixed only with a user-visible-sequence test, one ordered source of truth, `git log`/`blame`/`show` before more production edits on regressions.
2. **Builtin skill `debug-regression`**: full protocol, loaded via `skill_lookup` when the task matches (regression, still broken, zero difference, doesn't show, live vs restore / badge vs summary).
3. **Failed-fix follow-up injector** (`pkg/agent/followup_investigation.go`): if cleaned user text matches those phrases, per-turn SessionContext gets a short reminder **even when the model skips skill_lookup**. Plan-mode context is preserved and prepended.

## Plan revision

`update_plan` on an unknown step returns `step_not_found` plus the active plan's exact step `name`s (`Message` + `Steps`). Graph-Optimized Plan's design self-check requires a unit test of live event order and session restore for UX/event work, and forbids guessing `update_plan` names.

## Verification

- Prompt golden + contract tests in `pkg/agent`.
- Skill index tests in `pkg/skills`.
- Detector and turn-context tests in `pkg/agent/followup_investigation_test.go`.
- `update_plan` miss payload in `pkg/tools/plan_tool_test.go`.
