# Code-mode investigation approach

Astonish Code already finds the right files quickly (codegraph, grep, definitions). Failures on regressions were **how evidence was treated**, not missing exploration. This document is the product contract for that approach. Do not change the codegraph-first rule or GRAPH-phase allow-list to "fix" investigation quality.

## Three layers

1. **Always-on Work Policy + Live Evidence** in the code-mode system prompt (`pkg/agent/code_system_prompt_builder.go`, shared `LiveEvidenceSection` in `pkg/agent/live_evidence.go`): short bullets — keep every explicit requirement in view, user restatements are spec, drop a contradicted hypothesis, claim done only when this turn's tool output supports it, two kinds of proof (library test vs live observation/drill), CloakBrowser is not `chromium` on PATH, one ordered source of truth, `git log`/`blame`/`show` before more production edits on **code** regressions. Stop-exploring still applies to code edits; running surfaces are the exception.
2. **Builtin skills**:
   - `debug-regression` — git archaeology, failing-test-first, for code/UI logic regressions.
   - `inspect-live-surface` — live session/sandbox/browser. Product path then overlay/process then `browser_navigate`. Not a PATH probe.
   - `verify-live-with-drill` — drills as the preferred `verify_kind=behavior` harness for running surfaces.
   - `watch-long-running` — starting a rebuild is not done; `process_*` until the artifact exists.
3. **Follow-up injector** (`pkg/agent/followup_investigation.go`): live-surface needles inject `inspect-live-surface` in **Studio and Code** (the chromium PATH miss was Studio, which has no Work Policy of its own). Code-regression needles still inject `debug-regression` in Code. Live keywords win when both match. Plan-mode context is preserved and prepended.

Studio chat emits the same Live Evidence block from `SystemPromptBuilder` (no codegraph / stop-exploring language). Detailed browser/process how-tos stay in vector `memory/guidance/*.md`.

## Plan revision

`update_plan` on an unknown step returns `step_not_found` plus the active plan's exact step `name`s (`Message` + `Steps`). Graph-Optimized Plan's design self-check requires a unit test of live event order and session restore for UX/event work, and forbids guessing `update_plan` names.

`announce_plan` is rejected unless each phase has a testable `outcome`, a `verify` command, and `verify_kind` (`unit` or `behavior`). Running surfaces (cmd/api/launcher/daemon/sandbox/tui/browser/web) require `behavior`. `gplan_finalize` requires `acceptance` (the user-visible sequence); plan-level `verification` must include it. This is the capability contract: a phase is a user-visible slice, not a file batch. GRAPH-phase allow-list is unchanged.

## Plan completion (evidence)

Checkboxes are not proof. `update_plan(complete)` runs the phase `verify` command; a non-zero exit marks the phase failed. Sub-agent finish is not completion. `announce_completion` runs plan-level `verification` and writes `## Results`. Execution mode ends only when `IsFullyAccepted()` (all phases complete AND Results). The Incus→Docker session (2026-09-06) is the regression story: six mega-phases marked complete while the container did not exist. GRAPH-phase allow-list is unchanged.

Execution mode may inspect a **running surface** (shell, docker, `browser_navigate`, `run_drill`) even when PLAN.md already named the files. That is verification, not rediscovery. GRAPH phase stays codegraph/`find_files` only.

## Verification

- Prompt golden + contract tests in `pkg/agent`.
- Skill index tests in `pkg/skills`.
- Detector and turn-context tests in `pkg/agent/followup_investigation_test.go` (live-surface vs failed-fix split).
- Live Evidence section tests in `pkg/agent/live_evidence_test.go` and prompt goldens.
- Skill index tests in `pkg/skills` for `inspect-live-surface`, `verify-live-with-drill`, `watch-long-running`.
- `update_plan` miss payload in `pkg/tools/plan_tool_test.go`.
