# API & Studio

## Overview

Astonish provides a comprehensive REST API (51 files in `pkg/api/`) that powers the Studio web UI -- a React-based visual interface for chat, flow editing, fleet management, drill execution, settings, and more. The API uses Server-Sent Events (SSE) for real-time chat streaming and standard JSON REST endpoints for CRUD operations.

## Artifact access boundary

Platform artifact content, downloads and PDF source reads require an existing authorized session, including for superadmins. They never read daemon-host paths or daemon-local transcripts. The source for a growing Markdown document is the authorized destination file in the sandbox backend; there is no secondary job store or reconstruction from persisted chunks. Trusted local-code mode retains host-file access. Downloads set `nosniff` and a sandboxed `default-src 'none'` CSP; HTML and JavaScript remain attachments. Reconnect/restore never grants cross-user lookup, and no existing artifact endpoint or authorization rule is weakened. A partial destination is visible and usable through this same boundary; the existing `astonish-report` fence publishes it as a report only after the continuation marker has been removed.

## Delegation identity

Structured delegation progress adds optional `batch_id`, `task_id`, `child_session_id`, `attempt`, `call_id`, and `sequence`. Batch identity uses the parent tool-call ID when available. Task identity survives retries; each attempt has its own child session, persisted in child state and returned in `attempt_history`. Live tool projection uses a tracker per child/attempt and correlates deferred tools by call ID. History reconstructs identity from persisted delegation results rather than treating task names as globally unique.

`GET /api/studio/sessions/{id}/subtask-events` accepts `child_session_id` or the complete `batch_id`, `task_id`, `attempt` tuple for exact selection. The child must belong to the authorized parent's child list, match its persisted parent state, and pass ownership and request-scoped store checks. Invalid selectors return 400 and inaccessible exact selections return 404 without reading the transcript. Legacy `task_name` selection remains available for unique matches; ambiguity returns 409 instead of selecting the last retry. Detail events include call identity and transcript order.

Each `attempt_history` entry includes its own duration (not the cumulative retry duration). Queued cancellation is `cancelled`; an expired deadline is `timeout` in results and `failed` in progress. Validation failures produce a batch-scoped `delegation_complete` with error details even when no child starts. Structured child activity suppresses duplicate generic text/tools while preserving image delivery.

## Key Design Decisions

### Why SSE for Chat Streaming

Server-Sent Events were chosen over WebSockets for real-time chat because:

- **Simpler protocol**: SSE is HTTP-native, works through proxies and load balancers without special configuration, and auto-reconnects on connection loss.
- **Unidirectional fits the model**: Chat is request-response -- the user sends a message (POST), then receives a stream of events (SSE). There's no need for bidirectional WebSocket communication.
- **Built-in browser support**: The `EventSource` API is simple and well-supported.

The SSE stream delivers text chunks (partial and complete), tool calls, tool results, approval requests, errors and public runtime metadata. Content-less knowledge/tool tracking records are internal transcript metadata, not public SSE messages. Authorized memory inspection uses the existing REST debug endpoints.

### Why a Single-Binary Server

The API server is embedded in the Astonish binary and served by the daemon. The React UI is built and embedded via Go's `embed` package. This means:

- **Single deployment**: No separate frontend server, no CORS issues, no reverse proxy needed.
- **Dev mode**: `make daemon-dev` runs the Vite dev server on port 5173 with hot reload, proxied to the Go API.
- **Production mode**: `make daemon` serves the pre-built UI from embedded assets.

### Why Device Authorization

Studio uses a device authorization flow for authentication:

- On first access, the user gets a code to approve in the terminal/daemon.
- Once approved, the browser receives a session token.
- This avoids password management while ensuring only the machine owner can access the UI.

### Why a Dedicated AI Chat Panel

Studio includes a separate AI-powered assistant (`AIChatPanel`) that helps users with Studio-specific questions -- how to create flows, configure settings, use features. This assistant uses its own chat session and is independent of the main agent conversation.

## Architecture

### API Structure

The API is organized by domain:

| Domain | Key Endpoints | Handler File |
|---|---|---|
| **Chat** | `POST /chat`, `GET /chat/stream` (SSE) | `chat_handlers.go` |
| **Sessions** | `GET /sessions`, `DELETE /sessions/:id`, `GET /sessions/:id/events` | `session_handlers.go` |
| **Flows** | `GET /flows`, `POST /flows`, `PUT /flows/:name`, `POST /flows/validate` | `flow_handlers.go` |
| **Fleet** | `POST /fleet/sessions`, `GET /fleet/sessions/:id/stream`, `POST /fleet/sessions/:id/message` | `fleet_handlers.go` |
| **Drills** | `GET /drills/suites`, `POST /drills/run`, `GET /drills/results` | `drill_handlers.go` |
| **MCP** | `GET /mcp/servers`, `POST /mcp/servers`, `GET /mcp/inspector` | `mcp_handlers.go` |
| **Sandbox** | `POST /sandbox/init`, `GET /sandbox/templates`, `POST /sandbox/proxy` | `sandbox_handlers.go` |
| **Scheduler** | `GET /scheduler/jobs`, `POST /scheduler/jobs`, `POST /scheduler/jobs/:id/run` | `scheduler_handlers.go` |
| **Credentials** | `GET /credentials`, `POST /credentials`, `DELETE /credentials/:name` | `credential_handlers.go` |
| **Settings** | `GET /settings`, `PUT /settings`, `GET/PUT /settings/platform/providers`, `GET /standard-servers`, `POST /standard-servers/{id}/install|activate`, `GET/PUT/DELETE /web-search/perplexity/*` | `settings_handlers.go`, `provider_settings_handlers.go`, `standard_servers_handler.go`, `perplexity_web_search_handler.go` |
| **Tools** | `GET /tools`, `GET /tools/cache`, `GET /tools/web-capable` | `tools_handlers.go`, `tools_api.go` |
| **AI Chat** | `POST /ai-chat`, `GET /ai-chat/stream` | `ai_chat_handlers.go` |
| **Memory health** | `GET /memories/health`, `GET /memories/map`, `POST /memories/consolidate/preview`, `POST /memories/consolidate/apply` | `memory_health.go`, `memory_map.go`, `memory_consolidation.go` |

The memory health and consolidation endpoints are platform-only and tenant-scoped. `GET /memories/health` evaluates recommendations lazily when the UI is opened and reuses fresh results for five days; it does not run on a scheduler. Consolidation drafts and upserts structured scenario cards through the existing memory stores; it does not connect directly to tenant databases. After successful card processing, incorporated raw source memories are deleted or discarded so cards remain the durable memory format.

### SSE Chat Streaming

```
Client: POST /chat {sessionID, message}
  |
  v
Server: StudioChatHandler
  1. Validate session, resolve or create
  2. Start SSE response (Content-Type: text/event-stream)
  3. Run ADK runner with ChatAgent
  4. For each event from runner:
     - Text events (partial=true): stream as "text" SSE events
     - Text events (partial=false, seenPartialText=true): SKIP (aggregate duplicate)
     - Tool call events: stream as "tool_call" SSE events
     - Tool result events: stream as "tool_result" SSE events
     - Approval events: stream as "approval" SSE events
  5. Apply credential redaction to all text events
  6. Close SSE stream
```

The `seenPartialText` flag is critical for preventing message duplication. ADK's streaming aggregator yields every text segment twice -- once as partial streaming chunks, and once as a non-partial aggregate. The SSE handler filters out the aggregate to prevent doubled messages in the UI.

### Per-turn memory debug API

Both existing endpoints use the same persisted-record resolver:

- `GET /api/studio/sessions/{id}/knowledge-debug?invocationId=...`
- `GET /api/platform/admin/sessions/{id}/knowledge-debug?invocationId=...` (with explicit administrative scope identifiers)

Access remains platform-superadmin-only with existing personal/team tenant resolution; no authorization or SSE contract changes. The response retains `sessionId`, `invocationId`, `knowledge` and `tools`. Versioned knowledge records add `version`, `invocation_id`, `capture_status`, `dispatched` and an optional `snapshot` with sanitized formatted text, ordered snippets/provenance, byte counts and truncation information. Full memory text is saved only when the authorized Debug flag was enabled for that invocation. It is REST inspection data, not ordinary chat history or a new stream event.

Records must match the requested invocation, including agreement between event and payload IDs. The latest valid matching record wins, allowing provider dispatch to supersede preparation. Knowledge and tools resolve independently. Legacy records with exact IDs can supply metadata but not invented text; empty-ID legacy records are attributable only within an unambiguous contiguous preparation interval. Missing/unknown invocations return null data rather than another turn's latest retrieval. Snapshots are read from saved transcript metadata, never reconstructed from current memory sources or the latest session state.

The existing Memory tab distinguishes captured empty retrieval, capture disabled, legacy text unavailable, prepared without recorded dispatch, no record, loading and HTTP errors. Captured text is rendered as escaped whitespace-preserving plain text and remains available after reload with capture now off. The suffix itself is request-local to the current native non-Code invocation and does not become replayable conversation Content. See [agent-engine.md](agent-engine.md) for lifetime and legacy limitations, and [chat-rendering-pipeline.md](chat-rendering-pipeline.md#turn-debug-memory-and-cache) for UI states.

### React Studio Frontend

The Studio UI is built with React 19, Vite 7, and Tailwind CSS 4. Key components:

| Component | Purpose |
|---|---|
| `StudioChat` | Main chat interface with SSE streaming, message rendering |
| `FlowCanvas` | Visual flow editor with drag-and-drop nodes and edges |
| `FleetView` | Fleet session management: start, monitor, message |
| `DrillView` | Drill suite runner and result viewer |
| `SettingsPage` | Configuration management for providers, channels, sandbox |
| `SetupWizard` | First-run setup flow |
| `MCPInspector` | MCP server debugging and testing |
| `FlowStorePanel` | Flow marketplace browser |
| `MCPStoreModal` | MCP server catalog browser |
| `AIChatPanel` | AI-powered UI assistant |
| `ProviderModelSelector` | Provider and model picker with validation |

### Rate Limiting and Security

- **Rate limiting**: Applied to API endpoints to prevent abuse.
- **CSP headers**: Content Security Policy headers prevent XSS while allowing the production font stylesheet from Google Fonts and the client-side GitHub release check.
- **Device authorization**: Protects Studio access.
- **Credential redaction**: All API responses pass through the Redactor.

### Tools Cache

MCP tools are cached with background refresh to avoid slow MCP server queries on every request. The cache is warmed at startup and refreshed periodically.

### API container image (`docker/astonish`)

The published `ghcr.io/sap/astonish` image is the Studio/daemon runtime (not a sandbox). CI and Makefile host-cross-compile `astonish-linux-{amd64,arm64}` and only `COPY` the binary into the image.

Runtime tools needed for **host-side** stdio MCP (`npx` / `uvx` when no sandbox is wired) are layered as prebuilt multi-arch artifacts rather than `apt install nodejs npm` + `pip install uv`:

- Node 22 + npm/npx from the official `node:22-bookworm-slim` image
- `uv` / `uvx` from `ghcr.io/astral-sh/uv` (pinned version in the Dockerfile)
- Minimal apt: `ca-certificates`, `git`

This avoids installing Node under QEMU on GitHub Actions multi-arch `linux/arm64` builds (the previous dominant cost in beta image jobs). Deps layers stay stable across code-only rebuilds; only the binary `COPY` invalidates. CI also uses a registry build cache at `ghcr.io/sap/astonish:buildcache`.

## Key Files

| File | Purpose |
|---|---|
| `pkg/api/chat_handlers.go` | Chat SSE streaming, message handling, duplicate filtering |
| `pkg/api/server.go` | HTTP server setup, routing, middleware |
| `pkg/api/session_handlers.go` | Session CRUD endpoints |
| `pkg/api/flow_handlers.go` | Flow CRUD, validation, schema generation |
| `pkg/api/fleet_handlers.go` | Fleet session management, SSE streaming, headless sessions |
| `pkg/api/drill_handlers.go` | Drill suite management and execution |
| `pkg/api/mcp_handlers.go` | MCP server management and inspector |
| `pkg/api/sandbox_handlers.go` | Sandbox initialization, templates, proxy |
| `pkg/api/ai_chat_handlers.go` | Dedicated AI assistant for Studio UI |
| `pkg/api/memory_health.go` | Lazy Memory Health recommendations with a five-day freshness window |
| `pkg/api/memory_map.go` | Advanced Memory Map diagnostics for duplicate/scattered/risky memories |
| `pkg/api/memory_consolidation.go` | Scenario-card preview/apply endpoints for controlled consolidation |
| `web/src/components/` | React components (37+ files) |
| `web/src/api/` | API client functions for the frontend |

## Interactions

- **Agent Engine**: The chat handler runs the ChatAgent via ADK runner and streams events.
- **Sessions**: Session endpoints read/write to the FileStore.
- **Credentials**: All API text responses are redacted. Credential endpoints manage the encrypted store.
- **Sandbox**: Sandbox endpoints trigger initialization, template management, and container proxy.
- **Fleet**: Fleet endpoints manage sessions, stream agent activity, and handle human messages.
- **MCP**: MCP endpoints manage server lifecycle and provide an inspector for debugging.
- **Daemon**: The API server is started as part of daemon initialization.

### Chrome extension client

The unpacked Chrome extension (`extension/`) is another client of existing Studio APIs. It does not add routes.

- OAuth Authorization Code + PKCE flow via `/oauth/authorize` and `/oauth/token` — the extension uses the built-in `astonish-chrome-extension` public client with a `chrome-extension://` redirect URI. Cookies (`astonish_access`) cannot be sent from `chrome-extension://`.
- `POST /api/studio/chat` SSE with `Authorization: Bearer` and optional `systemContext` (current page URL/title/body). See `docs/architecture/chrome-extension.md`.
