# Plan Mode Hard Enforcement

## Problem

Plan mode (`/plan` or `shift+tab` in code/terminal mode) was **advisory only**. It injected a
soft `systemContext` prompt asking the agent not to make changes, but nothing stopped the LLM
from calling `write_file`, `edit_file`, `shell_command`, `delegate_tasks`, etc. A model that
ignored the prose could still mutate the workspace.

## Goal

1. **Stronger guidance** — a firmer, structured plan-mode prompt.
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
  must never be blocked in Plan mode.
- The block returns a **result** (not an error), so the model receives a reminder and keeps
  producing the plan rather than crashing the turn.

The design mirrors the existing `CheckTutorialDrillToolGate` hard-block pattern — no new
runtime path was forked.

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

## Scope note

Plan mode remains a **terminal/code-mode affordance** — there is no Plan toggle in the Studio
SPA yet. The API + client now fully support `planMode`, so adding a SPA toggle later is a
UI-only change.
