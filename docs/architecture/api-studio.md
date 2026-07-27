# API & Studio

## Overview

Astonish provides a comprehensive REST API (51 files in `pkg/api/`) that powers the Studio web UI -- a React-based visual interface for chat, flow editing, fleet management, drill execution, settings, and more. The API uses Server-Sent Events (SSE) for real-time chat streaming and standard JSON REST endpoints for CRUD operations.

## Key Design Decisions

### Why SSE for Chat Streaming

Server-Sent Events were chosen over WebSockets for real-time chat because:

- **Simpler protocol**: SSE is HTTP-native, works through proxies and load balancers without special configuration, and auto-reconnects on connection loss.
- **Unidirectional fits the model**: Chat is request-response -- the user sends a message (POST), then receives a stream of events (SSE). There's no need for bidirectional WebSocket communication.
- **Built-in browser support**: The `EventSource` API is simple and well-supported.

The SSE stream delivers multiple event types: text chunks (partial and complete), tool calls, tool results, approval requests, error events, and metadata events (knowledge tracking, tool tracking).

### Why a Single-Binary Server

The API server is embedded in the Astonish binary and served by the daemon. The React UI is built and embedded via Go's `embed` package. This means:

- **Single deployment**: No separate frontend server, no CORS issues, no reverse proxy needed.
- **Dev mode**: `make studio-dev` runs the Vite dev server on port 5173 with hot reload, proxied to the Go API.
- **Production mode**: `make studio` serves the pre-built UI from embedded assets.

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
| **Settings** | `GET /settings`, `PUT /settings` | `settings_handlers.go` |
| **Tools** | `GET /tools`, `GET /tools/cache` | `tools_handlers.go` |
| **AI Chat** | `POST /ai-chat`, `GET /ai-chat/stream` | `ai_chat_handlers.go` |
| **Memory diagnostics** | `GET /memories/map`, `POST /memories/consolidate/preview`, `POST /memories/consolidate/apply` | `memory_map.go`, `memory_consolidation.go` |

The memory consolidation endpoints are platform-only and tenant-scoped. They draft and upsert structured scenario cards through the existing memory stores; they do not connect directly to tenant databases and they do not delete source memories.

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
- **CSP headers**: Content Security Policy headers prevent XSS.
- **Device authorization**: Protects Studio access.
- **Credential redaction**: All API responses pass through the Redactor.

### Tools Cache

MCP tools are cached with background refresh to avoid slow MCP server queries on every request. The cache is warmed at startup and refreshed periodically.

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
| `pkg/api/memory_map.go` | Memory Map diagnostics for duplicate/scattered/risky memories |
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
