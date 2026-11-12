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

## Normal-mode plan revision

Initial `announce_plan` remains Plan-mode only; the initial execution approval gate is unchanged. An active plan is protected against wholesale re-announcement, not immutable. `update_plan` has two mutually exclusive operations:

- `{ "step": "phase-name", "status": "running" }` preserves the status protocol (`action: "status"` is optional; statuses are `running`, `complete`, `failed`). Unknown names return `step_not_found` and exact active names.
- `{ "action": "revise", "revision": { "plan_id": "…", "base_revision": 1, "reason": "…", "steps": […] } }` supplies full replacement **definitions**. Optional `goal` and `document` (`context`, `what_not_to_do`, `verification`) belong inside `revision`; `step`/`status` are forbidden. Steps contain announcement definition fields, never runtime status, evidence, Results, lifecycle or nested proposals. `requires_approval` is deprecated and ignored, including when true. Revisions are blocked in Plan, Graph-Plan and Ask, including deferred wrappers; bot revisions are unsupported.

`BuildPlanRevisionCandidate` (`pkg/agent/plan_revision.go`) validates changed/new phases against the plan contract. Initial graph-plan announcements must cover the recorded acceptance; a Normal-mode revision may replace that acceptance along with the verification narrative, without retaining obsolete instructions. Unchanged legacy definitions can be retained. Identical definitions are `noop`; **all valid Normal-mode changes apply immediately**, including goals, documents, phase membership/order, outcomes, file declarations/kinds, verification kinds and parallel groups. There is no revision approval screen or approval/rejection/request-changes decision.

Paths are compared lexically: in-workspace absolute paths become relative; external paths stay absolute, including `../` aliases resolved against an absolute workspace. Without an absolute workspace, paths retain their relativity rather than guessing containment from CWD. Empty, NUL/newline and root file declarations are rejected. This comparison neither resolves symlinks nor grants filesystem access: existing tool/folder authorization and execution consent remain unchanged. Revising a definition does not execute its commands.

`PlanRuntime.Revise` validates and persists the new definition before publishing its snapshot. Affected running phases, delegates or verification return `plan_busy`; wrong identity/base returns `stale_revision`. Completed plans cannot be revised. Errors preserve accepted authority. An ordinary next turn continues without a special decision request.

Legacy persisted `pending_revision` data is retired on restore without applying its obsolete candidate or blocking further work. Accepted definition, status and evidence remain authoritative; restoring a session never resurrects revision controls or silently approves old proposals. Legacy decision transport is not a route for applying a candidate.

### Snapshot, persistence and evidence

`PlanSnapshot` is session authority: `plan_id`, definition `revision`, runtime `sequence`, `goal`, `document`, ordered `steps`. Legacy `pending_revision` is readable only for migration. Applying a definition increments revision; committed runtime changes advance sequence, so revision alone cannot order status events.

`PlanRuntime` and request-scoped callbacks bind operations to one session. An explicitly bound nil runtime disables lifecycle operations rather than falling back to global state. Local code mode atomically replaces the session PLAN.md sidecar. Studio persists the complete snapshot under session state key `astonish_plan_snapshot` before publishing its event. Persistence failure leaves runtime authority unchanged. Studio verification uses the authenticated session's sandbox transport and fails closed without it, never falling back to the singleton agent's host verifier.

Accepted implementation edits reset affected phases to pending and clear evidence. Description/summary-only edits preserve evidence. Details/verify edits invalidate the edited phase and later dependent waves, preserving independent siblings with the same nonempty parallel group. Structure changes invalidate from the first changed position; goal/document changes invalidate all phases. This is conservative ordered-wave closure, not a dependency graph. Unaffected status/evidence survives. Verify results publish only while captured identity, implementation and phase state remain current; stale evidence is discarded.

Restore prefers durable snapshots. Legacy accepted results and persisted statuses remain readable; absent status defaults to pending. Tool-call arguments, assistant prose, end-of-turn and sub-agent finish never infer completion. Legacy complete checkboxes without current passing evidence cannot satisfy acceptance.

`announce_plan` still requires testable `outcome`, `verify` and `verify_kind` (`unit`/`behavior`) per phase. Running surfaces require behavior verification. `gplan_finalize` requires `acceptance`, included in plan-level `verification`. GRAPH exploration rules are unchanged.

## Plan completion (evidence)

Checkboxes are not proof. `update_plan(complete)` runs phase verification; failure records a failed phase. `announce_completion` requires an approved plan, no unfinished verification/delegates, and every phase complete with a verify command and current passing evidence. It writes `## Results` from those records without new commands; definition revision is not evidence. Execution acceptance requires all phases complete and Results (`IsFullyAccepted()`), never prose or an end-of-turn sweep. Running-surface acceptance belongs in a behavior phase, not a separate completion exam. The Incus→Docker session (2026-09-06) remains the regression story: checked phases did not mean a container existed.

Execution mode may inspect a **running surface** (shell, docker, `browser_navigate`, `run_drill`) even when PLAN.md already named the files. That is verification, not rediscovery. GRAPH phase stays codegraph/`find_files` only.

## Verification

- Prompt golden + contract tests in `pkg/agent`.
- Skill index tests in `pkg/skills`.
- Detector and turn-context tests in `pkg/agent/followup_investigation_test.go` (live-surface vs failed-fix split).
- Live Evidence section tests in `pkg/agent/live_evidence_test.go` and prompt goldens.
- Skill index tests in `pkg/skills` for `inspect-live-surface`, `verify-live-with-drill`, `watch-long-running`.
- `update_plan` miss payload in `pkg/tools/plan_tool_test.go`.
