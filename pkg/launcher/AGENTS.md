# pkg/launcher — AGENTS.md

Launcher wires runtime components together and exposes entry surfaces: the
platform-backed terminal chat app and the Studio HTTP server.

## Scope
- `tui_chat.go:RunChatTUI` — **interactive CLI chat** (requires platform login). Studio SSE → `pkg/tui`.
- `chat_factory.go:NewWiredChatAgent` — fully wired `ChatAgent` for Studio/daemon (not used by the CLI TUI).
- `studio.go:NewStudioServer` — HTTP server + SPA serving. Registers `/api/*` (via `pkg/api.RegisterRoutes`), platform auth, tenant middleware, CSP, rate-limit; serves the embedded SPA from `web/embed.go` (falls back to `web/dist` on disk when present).
- `web_simple.go:RunSimpleWeb` — minimal dev-only chat web server.
- `console.go` — flow/agent console runner (non-chat).

## Key rules
1. **CLI chat is platform-only.** `astonish chat` always uses authenticated Studio REST/SSE (`client` package). There is no in-process personal chat path.
2. **`NewWiredChatAgent` is the wiring choke point for Studio/daemon agents.** If you need a new dependency in the agent runtime, add it here.
3. **Studio SPA assets**: `getWebAssets()` prefers `web/dist` on disk (dev) and falls back to the embedded FS built via `web/embed.go`. Do not add a third code path.
4. **Middleware order in `NewStudioServer`** matters: platform auth → tenant → rate-limit → CSP → SPA/API split. Preserve this order when adding middleware.
5. **`/api/*` vs. SPA**: everything under `/api/` is routed to the API mux; every other path serves the SPA `index.html`.
6. **TUI presentation lives in `pkg/tui`.** Launcher implements `backend.Backend` (`platformBackend` in `tui_chat.go`) and calls `tui.Run`. See `docs/architecture/terminal-app.md`.

## Entry-point relationship
- CLI chat: `astonish chat` → requires `astonish login` → `RunChatTUI` → `platformBackend` → `tui.Run`.
- Studio: `astonish daemon run` → `pkg/daemon.Run` → `NewStudioServer` → `NewWiredChatAgent` per session.

## When editing
- Changing the terminal UI chrome/render? Prefer `pkg/tui`.
- Changing SPA-serving behavior? Update both dev (`web/dist`) and embedded paths, and re-test `make studio-dev` and `make studio`.
