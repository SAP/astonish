# pkg/tui — AGENTS.md

Fullscreen terminal chat app for Astonish (Claude Code / OpenCode–style).

## Scope

- `app.go` — bubbletea root model (header, transcript viewport, status, input)
- `approval.go` — tool approval overlay (`y`/`n`/options)
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
5. Plan mode is a TUI toggle that sends `backend.TurnOptions.SystemContext` **and** `backend.TurnOptions.PlanMode`; do not fork the agent/runtime path. The `PlanMode` flag threads through `client.ChatRequest.PlanMode`/`agent.PromptOverrides.PlanMode` to a hard runtime gate (`BeforeToolCallback` in `pkg/agent/chat_agent_run.go`) that refuses `delegate_tasks` and any non-`SafeTools` tool. Enforcement lives in `pkg/agent`, not the TUI — the TUI only sets the flag and mirrors the prompt text in `planModeSystemContext` (source of truth is `agent.PlanModeSystemContext`; keep the two in sync — the mirror now also mentions that finalized plans are recorded via `announce_plan` into a session `PLAN.md`).
6. Capability-gated commands (`/provider`, `/rollback`) are exposed only when the active backend implements the matching optional interface (`backend.ProviderAdminBackend`, `backend.RollbackBackend`), which only the code-mode `localAgentBackend` does. Gate them in three places — `handleSlash`, `syncSlashCompletion` (the `extra` slash commands), and `helpText` — via `m.providerAdmin()` / `m.rollbackCap()`. Never expose these on platform chat. The `/provider` list shows **only** local config.yaml providers (all editable); code mode never fetches or displays platform providers.
7. **Esc cancels an in-flight turn** (`cancelInFlightTurn` via `turnCancel`) the same way Ctrl+C does while streaming. Esc must **not** quit the app when idle; Ctrl+C idle still quits. Overlays/approvals still own Esc when open.

## Entry points

- `astonish chat` → `launcher.RunChatTUI` → `tui.Run`
- bare `astonish` → same path when stdin/stdout are TTYs and login exists

## When editing

- Pure rendering / reducers: put tests next to the package.
- Architecture: `docs/architecture/terminal-app.md`.
