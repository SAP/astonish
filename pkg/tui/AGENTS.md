# pkg/tui — AGENTS.md

Fullscreen terminal chat app for Astonish (Claude Code / OpenCode–style).

## Scope

- `app.go` — bubbletea root model (header, transcript viewport, status, input)
- `approval.go` — tool approval overlay (`y`/`n`/options), including code-mode tool & folder authorization prompts (`ApprovalKind` `tool`/`folder`)
- `sessions.go` — sessions picker + resume/new session
- `rollback.go` — `/rollback` picker overlay (code-mode only; reverts chat + file changes to an earlier message)
- `commands.go` — slash command palette definitions and filtering (`/plan`, `/files`, `/sessions`, …)
- `file_completion.go` — local `@file` completion and bounded inline context expansion
- `theme.go` — lipgloss theme tokens (numbers, +/−, brand, NO_COLOR)
- `wrap.go` — content margins, line truncation, padding
- `events/` — event types + transcript reducer (+ `LoadHistory`)
- `backend/` — `Backend` interface; platform impl is `pkg/launcher.platformBackend`
- `render/` — pure markdown/code/diff/activity renderers (unit-test heavy)

## Key rules

1. **Chat is always platform-backed.** Requires `astonish login`. No in-process agent path in the TUI.
2. **TUI never imports `pkg/daemon` or agent wiring.** It only consumes `backend.Backend` and reduces `events.Event`.
3. **cmd stays thin** — no bubbletea models under `cmd/astonish`.
4. Soft-degrade Studio-only SSE events (`app_preview`, browser handoff, …) to system notices.
5. Plan mode is a TUI toggle that sends `backend.TurnOptions.SystemContext` **and** `backend.TurnOptions.PlanMode`; do not fork the agent/runtime path. The `PlanMode` flag threads through `client.ChatRequest.PlanMode`/`agent.PromptOverrides.PlanMode` to a hard runtime gate (`BeforeToolCallback` in `pkg/agent/chat_agent_run.go`) that refuses `delegate_tasks` and any non-`SafeTools` tool. Enforcement lives in `pkg/agent`, not the TUI — the TUI only sets the flag and mirrors the prompt text in `planModeSystemContext` (source of truth is `agent.PlanModeSystemContext`; keep the two in sync — the mirror now also mentions that finalized plans are recorded via `announce_plan` into a session `PLAN.md`). `shift+tab` cycles a **third** mode, **Graph Plan** (`backend.TurnOptions.GraphPlanMode` → `agent.PromptOverrides.GraphPlanMode`, cyan composer border, code-mode only): Normal → Plan → Graph Plan → Normal. It is also a no-changes mode but enforces a **phased** gate driven by the codegraph MCP tool (graph → read → gap → plan); enforcement + the phase state machine live in `pkg/agent` (`graph_plan_state.go`, `chat_agent_run.go`). The TUI likewise only sets the flag and mirrors the prompt text in `graphPlanModeSystemContext` (source of truth `agent.GraphPlanModeSystemContext`; keep in sync). `planMode` and `graphPlanMode` are mutually exclusive.
6. Capability-gated commands (`/provider`, `/rollback`) are exposed only when the active backend implements the matching optional interface (`backend.ProviderAdminBackend`, `backend.RollbackBackend`), which only the code-mode `localAgentBackend` does. Gate them in three places — `handleSlash`, `syncSlashCompletion` (the `extra` slash commands), and `helpText` — via `m.providerAdmin()` / `m.rollbackCap()`. Never expose these on platform chat. The `/provider` list shows **only** local config.yaml providers (all editable); code mode never fetches or displays platform providers.
7. **Esc cancels an in-flight turn** (`cancelInFlightTurn` via `turnCancel`) the same way Ctrl+C does while streaming. Esc must **not** quit the app when idle; Ctrl+C idle still quits. Overlays/approvals still own Esc when open.
8. **Authorization approvals are code-mode only.** The approval overlay carries an `ApprovalKind` (`tool` or `folder`) mapped from the state delta's `approval_kind`/`approval_options`/path fields in `tui_code.go`'s `processStateDelta` (platform `tui_chat.go` stays unchanged). `renderApprovalOverlay`/`renderFolderApprovalOverlay` present the options via the shared `renderApprovalOptions` helper as a **cursor-navigable vertical list** (↑/↓ or `j`/`k` move `Transcript.ApprovalCursor`, Enter submits the highlighted option, default cursor 0 = the safe first option; `1`/`2`/`3` and `y`/`esc` are accelerators). Both kinds present the same three options — **Allow / Always Allow / Deny**; folder prompts also show the requested path + allowed root. The submitted option strings must match `agent.ApplyAuthorizationDecision`'s expected labels (`Allow` = once, `Always Allow` = broader grant, `Deny`). Enforcement + grant bookkeeping live in `pkg/agent` (`tool_authorization.go`), not the TUI.

## Entry points

- `astonish chat` → `launcher.RunChatTUI` → `tui.Run`
- bare `astonish` → same path when stdin/stdout are TTYs and login exists

## When editing

- Pure rendering / reducers: put tests next to the package.
- Architecture: `docs/architecture/terminal-app.md`.
