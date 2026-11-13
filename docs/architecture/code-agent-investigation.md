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

## Code delegate investigation defaults

Code delegates receive a focused coding baseline, not the parent's full system prompt or conversation. The launcher explicitly sets `SubAgentConfig.CodeMode` (`pkg/agent/sub_agent.go`); project context, a working directory or coding words in a prompt do not enable it. `codeDelegateDefaults` (`pkg/agent/code_delegate_tools.go`) applies only to public preferred-selection tasks (`ChildToolSelectionPreferred`), with no `CustomPrompt`, `OverrideTools` or `OverrideToolsets`.

For eligible tasks, `ChildToolScope.addCodeDelegateTools` promotes the permitted core structural tools (`code_definition`, `code_references`, `repo_map`) and permitted `mcp:codegraph` tools into the **initial declarations**, even when `preferred_tools` contains only `core` or other groups. The baseline comes from the frozen invocation scope, preserving its wrapped executable instances; it does not consult ambient tools or eagerly load unrelated MCP groups. Initial declarations are sorted and deduplicated before the immutable handoff records their names. Preferences do not grant permission: denied/excluded tools, disabled tools and inaccessible groups remain unavailable. Non-Code delegates, strict tool-selection tasks, custom prompts and explicit tool/toolset overrides retain their existing behavior and boundaries.

`codeDelegateGuidance` adds a small child-specific **Code Investigation** block based on those resolved declarations:

- Use codegraph first for implementation, definitions, callers and tests; do not begin with repository-wide grep or directory traversal.
- Use permitted structural tools next for gaps, then narrow file reads. Reserve `grep_search` for literal/string/configuration gaps, not symbol discovery.
- Treat source returned by codegraph as already read; do not read the same regions again. Stop exploring when implementation scope, affected callers and verification are clear.

Missing, denied or unresolved codegraph is not fatal: resolution failure produces a warning and the guidance explicitly says graph tools are unavailable, directing the child to permitted structural tools and narrow reads without claiming graph coverage. If structural tools are also absent, use only permitted discovery and narrowly scoped file tools. This targeted guidance is separate from handoff context: prior findings and task-specific constraints still require explicit instructions; it is not full parent-prompt inheritance.

## Bounded literal search

`grep_search` (`pkg/tools/grep_search.go`) uses ripgrep's JSON stream, not a full-output buffer or a pipe prefix followed by waiting on a still-writing process. `parseRipgrepOutput` retains complete JSON records within `head_limit` (default **5 MiB**), including records larger than the usual scanner 64 KiB limit. An unfinished record at the byte cap is discarded, while earlier complete records survive. At most one extra byte is read to distinguish exact EOF from truncation. Malformed JSON and unexpected read failures return errors instead of disappearing silently.

`max_results` (default **50**) counts match records, not context records. Output ends at the limit-th match, without its trailing after-context; `total` counts all returned match and context entries. A byte or result cap sets `capped` and an explicit `truncated_reason`. On a cap or parser/read failure, `tryRipgrep` kills the producer and closes the pipe rather than draining an arbitrarily large search. Every successfully started process is reaped with `Wait`, including cancellation and error paths. No-match exit 1 is an empty success; exit 2 remains a ripgrep error, not an empty search result.

The fixed safety timeout remains **25 seconds**. The search context now derives from the caller (nil direct-call contexts still work), so caller cancellation or an earlier deadline stops the running search and remains distinguishable from the tool's own actionable timeout error. Timeout guidance asks for a narrower path, glob or type filter; it is not permission to scan broad trees repeatedly.

**Cold-start caveat:** ripgrep resolution still prefers `rg` on PATH, then the managed copy, then pinned auto-provisioning (`pkg/tools/ripgrep/ripgrep.go`); there is no pure-Go backend for `grep_search`. `ResolvePath` does not accept the caller context, and a fresh download uses its own **90-second** background-context timeout. The 25-second search deadline therefore is not a strict wall-clock bound for a cold provisioning call, and caller cancellation cannot interrupt that provisioning step. Once resolution returns successfully, the expired/cancelled search context prevents the search from proceeding normally.

## Normal-mode plan revision

`announce_plan` remains Plan-mode only. A valid new announcement replaces any older plan—pending, approved, running, failed or completed—without requiring cancellation. Replacement creates a new plan identity with pending steps, no inherited evidence/Results, and its own execution approval. The previous plan remains historical; its graph acceptance and delegate registry do not constrain the new plan. Failed validation or persistence leaves the old plan unchanged, and late verification against the old identity is discarded. Normal/Ask announcements remain blocked; `update_plan` revisions remain distinct from new announcements. `update_plan` has two mutually exclusive operations:

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

## Immutable delegation handoff

`delegate_tasks` captures the invocation-scoped plan once per batch (`CaptureDelegationPlan` in `pkg/agent/delegation_handoff.go`, called by `pkg/tools/delegate_tool.go`). `ForTask` matches only an explicit, exact `plan_step`; it never guesses from the task name. A matching capture contains plan ID, revision, sequence, goal, document definitions and **only the assigned phase definition**, including files, outcome and verification. It excludes sibling phases, mutable status/evidence, pending revisions, the parent conversation and the child system prompt. No scoped plan records `no_scoped_plan`; an omitted phase with an available plan records `no_plan_step`. An unknown explicit phase in an available plan fails with the available names.

Each child attempt freezes a version-1 `DelegationHandoff` after its initial tools have been resolved. The record includes parent/batch/task/child/attempt identity, original and effective task/instructions, assigned phase, captured plan context, working directory, preferred tools and initially available tool names. Original fields and phase context survive retries; effective fields include retry/checkpoint guidance. Retries create their own attempt record without recapturing a later plan revision. Prior findings reach the child through explicit task/instructions/checkpoint context, not automatic parent-history inheritance.

String values are UTF-8 normalized and credential-redacted before JSON serialization; plan revision/sequence integers retain precision. The final JSON is capped at **256 KiB**: oversize serialization or persistence failure returns an error before child model invocation, rather than truncating the assignment or silently continuing. `appendDelegationHandoff` persists that exact JSON in a content-less child-session StateDelta under `astonish_delegation_handoff`; the same serialized record is embedded in the child prompt. The persisted record is supplied context, not a dump of the full prompt, and is not emitted as ordinary child conversation text. Caller-provided session state cannot overwrite reserved handoff or attempt-identity keys on this path.

Custom-prompt tasks opt out (`CustomPrompt`); tasks without a handoff seed retain their existing path. Capture does not change model routing, tool selection or authorization. `preferred_tools` is descriptive preference, not a new permission boundary. `source_mirror`, when supplied through `WithDelegationPlanSource`, is provenance only: capture does not read it, discover a repository-root PLAN.md, or grant access to it. Neither capture nor inspection changes plan approval, revision, status, verification or completion lifecycle.

Authorized detail readers expose the saved attempt record separately from execution activity and reapply current credential redaction. Missing, malformed, unsupported or mismatched records are unavailable, including older sessions; readers never reconstruct them from today's plan or parent history. See [terminal detail](terminal-app.md#delegation-detail-adapters) and [Studio detail](chat-rendering-pipeline.md#captured-handoff-and-rich-delegate-detail).

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
- Code delegate baseline, guidance and permission/selection boundaries in `pkg/agent/code_delegate_tools_test.go`.
- Streaming grep caps, large JSON records, context accounting, cancellation and exit distinctions in `pkg/tools/grep_search_stream_test.go`; actionable timeout guidance in `pkg/tools/search_tools_test.go`.
