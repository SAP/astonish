# Plan Mode Hard Enforcement

## Problem

Plan mode (`/plan` or `shift+tab` in code/terminal mode) was **advisory only**. It injected a
soft `systemContext` prompt asking the agent not to make changes, but nothing stopped the LLM
from calling `write_file`, `edit_file`, `shell_command`, `delegate_tasks`, etc. A model that
ignored the prose could still mutate the workspace.

## Goal

1. **Stronger guidance** — a firmer, structured plan-mode prompt that teaches a complete-plan
   methodology (see [Plan-mode methodology](#plan-mode-methodology)), not just a "don't make changes" rule.
2. **Hard runtime enforcement** — mutating tools are refused at execution time.
3. **Self-reminder** — when a tool is blocked, the model is told it is in Plan mode so it
   self-corrects and keeps building the plan (instead of aborting the turn).

## How it works

Plan mode now carries **two** signals per turn instead of one:

- `SystemContext` — the (strengthened) plan-mode prompt.
- `PlanMode bool` — a flag that enables a **hard runtime gate**.

### The runtime gate

Enforced server-side in `pkg/agent/chat_agent_run.go` as a `BeforeToolCallback`:

```
if planMode:
    if tool == "delegate_tasks" || !IsToolSafe(tool):
        return { status: "blocked_plan_mode", error: PlanModeBlockedMessage(tool) }
```

- **Blocked:** `delegate_tasks` (so plan mode can't be bypassed via a sub-agent) and any tool
  **not** in `agent.SafeTools` (the read-only allow-list: `write_file`, `edit_file`,
  `shell_command`, `memory_save`, …).
- **Allowed:** read-only tools (`read_file`, `grep_search`, `find_files`, `file_tree`,
  the tree-sitter navigation tools `repo_map` / `code_definition` / `code_references`,
  `memory_search`, …) so the agent can still investigate to produce an accurate plan.
  Note: structural navigation is read-only (it only parses and returns locations) and
  must never be blocked in Plan mode. `announce_plan` and `update_plan` are also allowed in Plan
  mode (both in `agent.SafeTools`) because they perform no arbitrary mutation — they only
  record/transition plan state (in-memory `PlanState` + the session `PLAN.md`). `announce_plan` is
  **Plan-mode only**: it is stripped from the tool list and refused in Normal/Ask.
- The block returns a **result** (not an error), so the model receives a reminder and keeps
  producing the plan rather than crashing the turn.

The design mirrors the existing `CheckTutorialDrillToolGate` hard-block pattern — no new
runtime path was forked.

> **Related: Normal-mode authorization reuses this allow-list.** `astonish code` also adds a
> Normal-mode (non-plan) authorization gate that reuses the **same `agent.SafeTools` list** as an
> *auto-allow baseline* rather than a hard block: safe/read-only tools run freely, but a
> non-whitelisted tool pauses for the user's authorization (Allow / Always Allow /
> deny) instead of being refused outright. A companion folder-access gate scopes tools to the
> project working directory. Both gates are code-mode only and bypassed by `--auto-approve` /
> `--yolo`. See **[Tool & folder authorization (code mode)](architecture/terminal-app.md#tool--folder-authorization-code-mode)**
> and `pkg/agent/tool_authorization.go`.

## Flag plumbing (end to end)

```
TUI toggle (m.planMode)
  → backend.TurnOptions{SystemContext, PlanMode}
    → code mode:     tui_code.go  → agent.PromptOverrides{PlanMode}
    → platform mode: tui_chat.go  → client.ChatRequest{PlanMode}
                                   → api StudioChatRequest{PlanMode}
                                   → ChatRunner.Run(..., planMode)
                                   → agent.PromptOverrides{PlanMode}
  → chat_agent_run.go reads po.PlanMode → registers the gate BeforeToolCallback
Studio SPA: connectChat({ planMode }) → body.planMode (client capability wired; no SPA toggle yet)
```

## Files changed

| File | Change |
|------|--------|
| `pkg/agent/tool_categories.go` | Added `PlanModeSystemContext` (centralized prompt) and `PlanModeBlockedMessage()` |
| `pkg/agent/system_prompt_builder.go` | Added `PlanMode bool` to `PromptOverrides` |
| `pkg/agent/chat_agent_run.go` | Captured `planMode` from overrides; registered the hard-gate `BeforeToolCallback` |
| `pkg/tui/backend/backend.go` | Added `PlanMode bool` to `TurnOptions` |
| `pkg/tui/app.go` | Strengthened `planModeSystemContext`; `turnOptions()` sets `PlanMode: true` |
| `pkg/launcher/tui_code.go` | Threads `opts.PlanMode` into `PromptOverrides` (code mode) |
| `pkg/launcher/tui_chat.go` | Threads `opts.PlanMode` into `client.ChatRequest` (platform mode) |
| `pkg/client/api.go` | Added `PlanMode` to `ChatRequest` |
| `pkg/api/chat_handlers.go` | Added `PlanMode` to `StudioChatRequest`; passes `req.PlanMode` to runner |
| `pkg/api/chat_runner.go` | `Run(...)` accepts `planMode bool`; injects into `PromptOverrides` |
| `web/src/api/studioChat.ts` | Added `planMode` param + `body.planMode` |

## Tests

- `pkg/agent/plan_mode_gate_test.go` (new): blocks mutating/delegation tools, allows read-only
  tools, `PlanModeBlockedMessage` names the tool + mode, prompt contains hard-constraint language.
- `pkg/tui/plan_mode_test.go`: asserts `turnOptions().PlanMode` is set/unset with the toggle.
- `web/src/api/__tests__/studioChat.test.ts`: `planMode` included when enabled / omitted when off.
- All existing `ChatRunner.Run` call sites updated for the new arg (5 integration test sites).

Result: `go build ./...` clean, Go tests for `pkg/{api,agent,tui,launcher,client}` pass,
`tsc --noEmit` clean, web tests pass, `npm run lint` clean for touched files.

## Docs

- `docs/architecture/terminal-app.md` — rewrote the "Plan mode" section to describe the hard gate.
- `pkg/tui/AGENTS.md` — updated rule 5 to note the flag + enforcement location.

## Plan-mode methodology

Beyond the "no changes" rule, `agent.PlanModeSystemContext` (source of truth in
`pkg/agent/tool_categories.go`, mirrored byte-for-byte by `planModeSystemContext` in
`pkg/tui/app.go`) teaches four disciplines so plans are **complete and approvable**, not partial
sketches — inspired by mature planning agents but adapted to Astonish's structural tools:

1. **Investigate thoroughly.** Orient with `repo_map`, read the actual declaration of each symbol
   with `code_definition`, enumerate **all** call sites with `code_references`, and read the real
   regions. Batch independent read-only lookups in parallel. Keep going until no affected file,
   caller, interface, test, generated file, migration, or doc remains unexamined.
2. **Cover every dependency — no partial implementations.** A complete plan touches every layer the
   change reaches (the symbol + its callers + tests + generated code + migrations + docs), ordered
   dependency-first (shared types/interfaces before consumers), with no orphaned/unwired code.
3. **Surface decisions for the user.** Breaking changes, meaningful alternatives with trade-offs,
   and genuine ambiguities are called out explicitly so the user can decide before execution; a
   single concise clarifying question is allowed when the code can't resolve it.
4. **Be efficient.** Effort is proportional to blast radius — stop once every file to change can be
   named; prefer structural tools over broad grep; never re-read files already in context.

The prompt directs the finalized plan into `announce_plan` with, per phase, an affected-files list
(each marked new/modify/delete), a concrete `details` approach, and a `verify` command — the exact
fields persisted to `PLAN.md` (see below). The same discipline is reinforced during execution by the
delegation "Planning strategy" block (`SystemPromptBuilder`) and the `guidanceCodeIntelligence`
"Planning a change" subsection (vector-store guidance).

## Plan persistence (PLAN.md) — code mode

An in-memory plan alone is lost detail during context compaction: the summarizer folds the
step-by-step structure into prose. To make the plan durable, code mode persists the announced
plan to a **per-session `PLAN.md`**.

- **Where:** a sidecar next to the session transcript,
  `<sessions-dir>/<app>/<userID>/<sessionID>.PLAN.md` (same folder as `<sessionID>.jsonl`).
  Resolved in `localAgentBackend.planFilePath` (`pkg/launcher/tui_code.go`) and set on the
  `ChatAgent` each turn via `SetPlanFilePath`.
- **When:** written the moment `announce_plan` fires (`ChatAgent.SetActivePlan`), and rewritten
  on **every phase transition** via a `PlanState.onChange` hook — no extra LLM round-trips.
  `announce_plan` is in `agent.SafeTools`, so it is allowed in Plan mode (it performs no arbitrary
  mutation) — the model records its finalized plan while planning, and persistence is a side effect
  of a permitted tool rather than a new hole in the gate.
- **Progress tracking (two mechanisms):**
  - **Delegated work** — phases progress automatically from `delegate_tasks` sub-task lifecycle
    events (`PlanState.StartStep` / `CompleteTask`), matched via each task's `plan_step`.
  - **Main-thread work** — the model calls the **`update_plan`** tool (`{step, status}`) to mark a
    phase `running` / `complete` / `failed` as it works. This drives `PlanState.SetStepStatus`,
    which rewrites `PLAN.md` and marks the plan **manually tracked**. `update_plan` is also in
    `agent.SafeTools` (allowed in Plan mode). This closes the previous gap where inline, non-
    delegated sequential work never advanced the checklist.
- **No fabricated completion:** the end-of-turn sweep (`CompleteAll` in `chat_agent_run.go`) now
  runs **only when both** (a) execution actually began this turn (`PlanState.HasStartedSteps()` —
  at least one phase left `pending`) **and** (b) the plan was not manually tracked
  (`IsManuallyTracked() == false`). When the model drove the plan via `update_plan`, its reported
  statuses are authoritative — the runtime no longer bulk-marks every remaining phase complete
  (which previously made `PLAN.md` show all phases done regardless of real progress). The
  `HasStartedSteps()` guard fixes a distinct regression where a **freshly announced** plan (e.g. the
  finalization turn in Plan mode, or any announce-only turn where no tool ran afterward) was swept to
  fully complete on creation — every phase showing `[x]` before any work was done. When no phase has
  started, the plan is also left **active** so it carries into the next turn where execution begins,
  instead of being cleared. Defended by `TestPlanState_AnnounceOnlyTurnDoesNotComplete` and
  `TestPlanState_HasStartedSteps`.
- **Detail preserved:** each phase requires a `details` implementation spec plus a `files` list
  (each affected file marked new/modify/delete) and a `verify` command (build/test/lint), all
  captured by `announce_plan` and rendered as an indented sub-block. Incomplete announcements are
  rejected. This makes the *detailed*, dependency-aware plan — not just one-line labels — survive
  compaction, and encodes the "no partial implementations / every phase ends verified" discipline
  the plan-mode prompt now teaches.
- **Format:** human-readable Markdown with GitHub-style checkboxes per phase
  (`[ ]` pending · `[~]` running · `[x]` complete · `[!]` failed), plus indented sub-lines for
  affected files (`- File (<kind>): <path>`), the verification command (`Verify: <cmd>`), and a
  free-form details block. Rendered/parsed losslessly by `RenderPlanMarkdown` / `ParsePlanMarkdown`
  in `pkg/agent/plan_document.go` (round-trip covered by `TestParsePlanMarkdown_FilesAndVerifyRoundTrip`).
- **Compaction survival:** the `Compactor` (`pkg/session/compaction.go`) holds an optional
  `PlanFilePath`. When a plan file exists at compaction time, its contents are inlined into the
  context summary (capped at 32KiB). Approved execution turns also inline the same document in
  the per-turn system context and restore per-phase status from disk (`RestoreApprovedPlan`
  preserves `[~]`/`[x]`). The compactor stays domain-agnostic — it reads the file as opaque text.
- **Scope:** code mode only. Platform/Studio has no local session filesystem for this, so the
  prompt guidance (`SystemPromptBuilder.PlanFilePersistence`) is enabled only when
  `!PlatformMode`.
- **Cleanup on delete:** the sidecar is removed when its session is deleted, so plan files never
  outlive their session on disk. `localAgentBackend.DeleteSession` (`pkg/launcher/tui_code.go`)
  removes the parent and any child-session sidecars via `removePlanFile`, and the CLI
  (`astonish sessions delete` / `sessions clear` in `cmd/astonish/sessions.go`) removes them via
  the shared `removeSessionFiles` helper (`sessionSidecarSuffixes = {".jsonl", ".PLAN.md"}`).
  All removals are best-effort — a missing file is not an error.

## Scope note

Plan mode remains a **terminal/code-mode affordance** — there is no Plan toggle in the Studio
SPA yet. The API + client now fully support `planMode`, so adding a SPA toggle later is a
UI-only change.

## Graph-Optimized Plan mode (code mode only)

Graph-Optimized Plan is now simply called **Plan mode** in the UI. `shift+tab` cycles
**Normal → Plan → Ask → Normal**. Like the old Plan mode it is a **no-changes** mode
(`write_file` / `edit_file` / `shell_command` are blocked in every phase), but instead of a
free-form investigation it enforces a fixed **"plan-for-the-plan"** flow driven by
[**codegraph**](https://github.com/colbymchenry/codegraph) — an external knowledge-graph tool
exposed over MCP as the read-only `codegraph_explore` query tool. The goal is faster, cheaper,
more complete plans: codegraph pre-computes symbols, call edges, dependencies and blast-radius,
answering most structural questions in 1–4 calls instead of many broad `grep_search` / `find_files`
passes.

### Phase state machine + phased gate

A **per-session phase state machine** (`pkg/agent/graph_plan_state.go`, `GraphPlanState`) tracks
the current phase; the phase determines the runtime tool allow-list. The model advances phases via
three small **transition tools** (`gplan_reads`, `gplan_gaps`, `gplan_finalize`) — the only
always-allowed "write-like" tools; they only mutate phase state. Enforcement is a
`BeforeToolCallback` in `pkg/agent/chat_agent_run.go` (the same mechanism as the Plan-mode gate),
so the model *physically cannot* call `grep_search` in the Graph phase.

Phase → additive allow-list (`GraphPlanPhaseTools`, single source of truth):

| Phase   | Allowed (besides transition tools + `update_plan`) |
|---------|----------------------------------------------------------------------|
| `graph` | `codegraph_explore` |
| `read`  | + `read_file`, `read_pdf`, `filter_json` |
| `gap`   | + `grep_search`, `find_files`, `file_tree`, `repo_map`, `code_definition`, `code_references`, `web_fetch`, `memory_search`, `memory_get`, `skill_lookup`, `delegate_tasks` (read-only `tools` filters) |
| `plan`  | + `announce_plan` |

Discovery budgets (`GraphPlanState.ChargeExploration`) bound **codegraph queries**, complementary **gap-filling** calls, and **delegation**. Source reads (`read_file` / `read_pdf` / `filter_json`) and plan recorders (`announce_plan` / `update_plan`) are **not charged** — a read quota truncates the READ phase, and charging `announce_plan` as a gap call deadlocks PLAN after the gap cap. Duplicate work is prevented by prompt reuse rules (never re-read a path already in context; never re-query the graph for a known fact), not by refusing the files the plan still needs.

Approved execution separately caps rediscovery (1 codegraph/code-intelligence call and 2 search/list calls per turn) but does **not** cap source reads. Read each target file once immediately before editing.

Transition semantics:
- `gplan_reads(read_list)` — records the synthesized, non-repetitive read list and advances
  `graph → read` (unlocks `read_file`).
- `gplan_gaps(gaps)` — advances `read → gap` (or `graph → gap` when codegraph has no coverage).
  An **empty** gaps list is a legitimate skip: it advances straight to `plan`.
- `gplan_finalize(notes?)` — advances to `plan` (unlocks `announce_plan`).

Blocked tools return a `blocked_graph_plan` **result** (not an error), with phase-aware guidance
(`GraphPlanBlockedMessage`) so the model self-corrects and advances phases legitimately. `graphPlan`
and `planMode` are **mutually exclusive**; when `graphPlan` is set the phased gate replaces the
plan-mode gate and the code-mode folder/tool authorization gates are skipped (read-only mode).

`codegraph_explore` and the three `gplan_*` transition tools are added to `agent.SafeTools`
(read-only / phase-only, so they auto-approve). This mode remains a **no-changes** invariant: no
mutating tool is ever allowed in any phase.

### Codegraph as a native standard MCP server

Rather than requiring users to hand-edit `mcp_config.json`, codegraph is registered as a
**native "standard MCP server"** (`pkg/config/standard_servers.go`) — the same registry that ships
Tavily / Brave / Firecrawl. It is a **keyless** entry (`EnvVars: []`) with a new non-web category
`"codeintel"`, command `codegraph serve --mcp`, and `FixedEnv{CODEGRAPH_MCP_TOOLS: "explore"}` so
only the read-only `codegraph_explore` tool is exposed (the graph-mutating subcommands are CLI-only,
never MCP tools). Because it is keyless + non-web, `mergeStandardServersWithConfig` always injects it
and `filterInactiveStandardWebServers` never strips it — so it appears automatically in code mode's
main-thread MCP injection with **zero user config**, and `SaveMCPConfig`'s standard-ID stripping keeps
it out of `mcp_config.json`.

### Bootstrap

`launcher.EnsureCodegraph` (`pkg/launcher/codegraph_bootstrap.go`) runs before each Graph-Plan turn:
1. `exec.LookPath("codegraph")` — if missing, install **only on explicit consent**
   (`ASTONISH_CODEGRAPH_AUTO_INSTALL=1`, since a single non-interactive turn can't round-trip a
   prompt) via `npm i -g @colbymchenry/codegraph` (or the documented curl installer).
2. If `<workingDir>/.codegraph/` is absent, run `codegraph init` to build the index.
3. Returns `ready=true` once both exist.

**Fallback:** if the user declines or bootstrap fails, the turn **downgrades to free-form Plan mode**
with a one-shot system notice, so the user still gets a plan. Language coverage: if codegraph returns
no coverage, the prompt + gate permit an immediate `graph → gap` skip so grep/tree-sitter unlock.

### Flag plumbing

```
TUI toggle (m.graphPlanMode)
  → backend.TurnOptions{SystemContext: graphPlanModeSystemContext, GraphPlanMode: true}
    → tui_code.go: EnsureCodegraph → (ready) reset GraphPlanState + SetActiveGraphPlan
                                     (else) downgrade to PlanMode
    → agent.PromptOverrides{GraphPlanMode}
  → chat_agent_run.go reads po.GraphPlanMode → registers the phased gate BeforeToolCallback
gplan_* tools → tools.graphPlanAdvanceCallback → chatAgent.GetActiveGraphPlan().Advance(phase)
```

### Files (Graph-Optimized Plan)

| File | Change |
|------|--------|
| `pkg/agent/graph_plan_state.go` (new) | `GraphPlanPhase`, `GraphPlanState`, `GraphPlanPhaseTools` allow-list |
| `pkg/agent/chat_agent.go` | Per-session `graphPlanStates` + `GetOrCreateGraphPlanState`; active-graph-plan pointer |
| `pkg/agent/chat_agent_run.go` | Threads `graphPlan`; phased gate; mutual exclusion with `planMode` |
| `pkg/agent/tool_categories.go` | `GraphPlanModeSystemContext`, `GraphPlanBlockedMessage`; `SafeTools` += codegraph + `gplan_*` |
| `pkg/agent/system_prompt_builder.go` | `GraphPlanMode bool` on `PromptOverrides` |
| `pkg/tools/graph_plan_tool.go` (new) | `gplan_reads` / `gplan_gaps` / `gplan_finalize` + advance callback |
| `pkg/launcher/chat_factory.go` | Registers the three transition tools; wires `SetGraphPlanAdvanceCallback` |
| `pkg/launcher/codegraph_bootstrap.go` (new) | `EnsureCodegraph` CLI/index bootstrap + fallback |
| `pkg/launcher/tui_code.go` | Bootstrap invocation, phase reset, active-graph-plan wiring, downgrade |
| `pkg/config/standard_servers.go` | Keyless non-web `codegraph` entry + `FixedEnv` support |
| `pkg/tui/backend/backend.go` | `GraphPlanMode bool` on `TurnOptions` |
| `pkg/tui/app.go` | 3-way `togglePlanMode` cycle; `graphPlanModeSystemContext` mirror; "Plan" label (amber) |

### Tests (Graph-Optimized Plan)

- `pkg/agent/graph_plan_gate_test.go` — per-phase allow/block table, additive invariant,
  phase-aware blocked message, prompt discipline, `SafeTools` membership.
- `pkg/agent/graph_plan_state_test.go` — transitions incl. `graph→gap` skip, reset, per-session isolation.
- `pkg/tools/graph_plan_tool_test.go` — transition tools advance phases; empty-gaps skip to plan.
- `pkg/tui/plan_mode_test.go` — 3-way cycle (Normal → Plan → Ask → Normal), `turnOptions` for Plan/Ask, composer labels.
- `pkg/config/mcp_config_test.go` — codegraph always injected, survives web filter, not persisted to `mcp_config.json`.

