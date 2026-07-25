# pkg/tui — AGENTS.md

Fullscreen terminal chat app for Astonish (Claude Code / OpenCode–style).

## Scope

- `app.go` — bubbletea root model (header, transcript viewport, status, input)
- `approval.go` — tool approval overlay (`y`/`n`/options)
- `sessions.go` — sessions picker + resume/new session
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

## Entry points

- `astonish chat` → `launcher.RunChatTUI` → `tui.Run`

## When editing

- Pure rendering / reducers: put tests next to the package.
- Architecture: `docs/architecture/terminal-app.md`.
