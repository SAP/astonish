# A2A Protocol — Astonish as Server

## Overview

The Agent-to-Agent (A2A) protocol is an open standard (Linux Foundation, v1.0.0) that enables AI agents built on different frameworks to communicate and collaborate. Astonish implements the **A2A Server** role as a channel adapter, allowing external agents to discover Astonish's capabilities, send tasks, receive streaming responses, and engage in multi-turn conversations — just as human users do through Telegram, Slack, or Email.

This document covers **Astonish as an A2A Server only**. The A2A Client role (Astonish calling out to other agents) is a separate future concern.

## Why A2A as a Channel

The A2A Server role maps naturally to Astonish's channel architecture:

| Channel Pattern | A2A Server Equivalent |
|---|---|
| External platform sends a message | External agent sends `SendMessage` (JSON-RPC) |
| Adapter normalizes to `InboundMessage` | A2A adapter extracts text/file/data Parts → `InboundMessage` |
| Router determines session key | `contextId` → session key (persistent conversation) |
| ChatAgent processes, produces response | ChatAgent processes, produces response |
| `OutboundMessage` sent back via adapter | Response wrapped as A2A Task result / SSE stream |
| Typing indicator | Task state `WORKING` + streaming status events |

The A2A adapter implements the `Channel` interface like any other adapter. The key difference is that the "platform" is another AI agent rather than a human messaging app, and the transport is JSON-RPC over HTTP rather than a platform-specific API.

### What makes A2A different from other channels

| Concern | Telegram/Slack/Email | A2A |
|---|---|---|
| Initiator | Human user | AI agent |
| Transport | Platform SDK / webhooks / IMAP | JSON-RPC 2.0 over HTTP |
| Session model | `channel:chatType:chatID` | `contextId` (server-generated) |
| Response delivery | Immediate reply via platform API | Synchronous response OR async callback (push notifications) |
| Multi-turn | Continuous thread | `INPUT_REQUIRED` state → client sends follow-up |
| Streaming | Typing indicator only | Full SSE with task state events + artifact chunks |
| Discovery | Manual configuration | Agent Card at `/.well-known/agent-card.json` |
| Task lifecycle | Fire-and-forget | Rich state machine (9 states) |
| Authentication | Platform-specific (bot tokens) | OpenAPI-style security schemes (Bearer, API key, OAuth, mTLS) |

## Architecture

### Component Diagram

```
External Agent (A2A Client)
    |
    | HTTPS (JSON-RPC 2.0 / SSE)
    v
┌─────────────────────────────────────────────────────────────┐
│  pkg/api                                                     │
│                                                              │
│  GET  /.well-known/agent-card.json  → agentCardHandler       │
│  POST /api/a2a                      → a2aJSONRPCHandler      │
│  POST /api/a2a/stream               → a2aStreamHandler (SSE) │
└─────────────┬───────────────────────────────────────────────┘
              |
              v
┌─────────────────────────────────────────────────────────────┐
│  pkg/a2a                                                     │
│                                                              │
│  ┌──────────────────┐  ┌────────────────────────────────┐   │
│  │ types.go         │  │ task_store.go                   │   │
│  │                  │  │                                 │   │
│  │ - AgentCard      │  │ - TaskStore interface           │   │
│  │ - Task           │  │ - InMemoryTaskStore             │   │
│  │ - Message        │  │ - Create / Get / List / Cancel  │   │
│  │ - Part           │  │ - State transitions             │   │
│  │ - Artifact       │  │                                 │   │
│  │ - TaskState      │  └────────────────────────────────┘   │
│  │ - Skill          │                                        │
│  │ - PushConfig     │  ┌────────────────────────────────┐   │
│  └──────────────────┘  │ push.go                        │   │
│                         │                                 │   │
│  ┌──────────────────┐  │ - PushNotifier                 │   │
│  │ agent_card.go    │  │ - HTTP POST to client webhook  │   │
│  │                  │  │ - Retry with backoff            │   │
│  │ - Build card     │  └────────────────────────────────┘   │
│  │   from config    │                                        │
│  └──────────────────┘                                        │
└─────────────┬───────────────────────────────────────────────┘
              |
              v
┌─────────────────────────────────────────────────────────────┐
│  pkg/channels/a2a/a2a.go                                     │
│                                                              │
│  A2AChannel implements Channel interface                     │
│                                                              │
│  - ID() → "a2a"                                              │
│  - Start() → registers with ChannelManager                   │
│  - HandleTask() → normalizes to InboundMessage               │
│  - Send() → wraps response as Task result / pushes callback  │
│  - SendTyping() → emits WORKING status event                 │
│  - Maps A2A contextId ↔ Astonish session keys                │
└─────────────┬───────────────────────────────────────────────┘
              |
              v
┌─────────────────────────────────────────────────────────────┐
│  ChannelManager → Router → ChatAgent (existing infra)        │
└─────────────────────────────────────────────────────────────┘
```

### Request Flow

#### Synchronous (blocking) request

```
1. External agent POSTs JSON-RPC `message/send` to /api/a2a
2. a2aJSONRPCHandler validates auth, parses request
3. Creates Task (state: SUBMITTED → WORKING)
4. Normalizes message Parts → InboundMessage
5. Calls A2AChannel.handler(ctx, inboundMsg)
6. ChannelManager routes to ChatAgent
7. ChatAgent processes (tool calls, LLM turns)
8. Response flows back through A2AChannel.Send()
9. A2AChannel wraps response as Task (state: COMPLETED) with Artifacts
10. JSON-RPC response returned to caller
```

#### Streaming request (SSE)

```
1. External agent POSTs JSON-RPC `message/stream` to /api/a2a/stream
2. a2aStreamHandler validates auth, opens SSE connection
3. Creates Task (state: SUBMITTED → WORKING)
4. Normalizes message → InboundMessage
5. Calls A2AChannel.handler(ctx, inboundMsg)
6. As ChatAgent streams tokens:
   - TaskStatusUpdateEvent (state: WORKING) with partial message
   - TaskArtifactUpdateEvent for generated files
7. On completion:
   - TaskStatusUpdateEvent (state: COMPLETED)
   - SSE stream closes
```

#### Asynchronous with push notification (callback)

```
1. External agent configures push notification URL via `pushNotification/set`
2. Agent POSTs `message/send` with `returnImmediately: true`
3. a2aJSONRPCHandler returns Task immediately (state: SUBMITTED)
4. Processing continues in background:
   - ChatAgent runs tool loop
   - On completion, PushNotifier POSTs result to client's webhook URL
5. Client can also poll via `tasks/get` at any time
```

### Multi-Turn Conversations

A2A supports multi-turn through the `INPUT_REQUIRED` state:

```
Turn 1:
  Client → SendMessage("Analyze the quarterly report")
  Server → Task(state: INPUT_REQUIRED, message: "Which quarter? Q1, Q2, Q3, or Q4?")

Turn 2:
  Client → SendMessage(taskId: "xxx", "Q3")
  Server → Task(state: WORKING → COMPLETED, artifacts: [...])
```

This maps to Astonish's existing session model:
- `contextId` → persistent session key (like a Telegram conversation)
- `taskId` → individual request within that session
- `INPUT_REQUIRED` → agent explicitly asks for clarification (the ChatAgent already does this naturally via its response text; the A2A adapter detects clarification-seeking responses and maps them to the appropriate state)

## Agent Card

The Agent Card is served at `GET /.well-known/agent-card.json` and describes Astonish's A2A capabilities:

```json
{
  "name": "Astonish",
  "description": "Multi-tenant AI agent platform with tool execution, code intelligence, and multi-modal capabilities",
  "url": "https://astonish.example.com/api/a2a",
  "version": "1.0.0",
  "provider": {
    "organization": "Astonish",
    "url": "https://astonish.example.com"
  },
  "capabilities": {
    "streaming": true,
    "pushNotifications": true,
    "stateTransitionHistory": true
  },
  "securitySchemes": {
    "bearerAuth": {
      "type": "http",
      "scheme": "bearer"
    },
    "apiKey": {
      "type": "apiKey",
      "in": "header",
      "name": "X-API-Key"
    }
  },
  "security": [
    { "bearerAuth": [] },
    { "apiKey": [] }
  ],
  "defaultInputModes": ["text/plain", "application/json"],
  "defaultOutputModes": ["text/plain", "text/markdown", "application/json"],
  "skills": []
}
```

### Dynamic Skills

The `skills` array is **dynamically generated** per-team based on configured capabilities:

- **MCP tools** registered for the team → mapped to A2A skills
- **Flows** → exposed as skills with structured input schemas
- **Agent specializations** → described as skills based on system prompt / tool groups

This means different teams can expose different A2A skill sets, maintaining multi-tenant isolation.

### Extended Agent Card

Authenticated clients can request `GET /.well-known/agent-card.json` with credentials to receive an **extended** Agent Card containing additional skills and capabilities not exposed publicly.

## Authentication & Multi-Tenant Mapping

### Identity Model

External A2A agents authenticate using one of:
- **API Key**: Team-scoped API keys (existing `pkg/credentials` infrastructure)
- **Bearer Token**: JWT or opaque token mapped to a service account
- **OAuth 2.0**: Client credentials flow for agent-to-agent auth

### Tenant Resolution

The A2A adapter uses the same `PlatformResolver` as other channels:

1. Authentication credentials → resolve to a **service account** (new entity type, or mapped to an existing user with `channel_type = "a2a"`)
2. Service account → linked to an org + team
3. Team context → injects team-scoped stores (credentials, flows, skills, MCP, memories)

This ensures:
- Each external agent gets its own session namespace
- Tool execution happens within the correct tenant boundary
- Credential isolation is maintained

### Agent Identity Registration

Admins register external agent identities through:
- Platform admin UI (future)
- CLI: `astonish admin a2a register-agent --name "SalesforceAgent" --team sales --key <api-key>`
- API: `POST /api/admin/a2a/agents`

## Task Lifecycle & State Machine

### States

| State | Description | Terminal? |
|---|---|---|
| `SUBMITTED` | Task received, queued for processing | No |
| `WORKING` | Agent is actively processing | No |
| `INPUT_REQUIRED` | Agent needs more information from client | No |
| `AUTH_REQUIRED` | Additional auth needed (e.g., OAuth consent) | No |
| `COMPLETED` | Successfully finished | Yes |
| `FAILED` | Processing error | Yes |
| `CANCELED` | Client or system canceled | Yes |
| `REJECTED` | Agent refused the task | Yes |

### State Transitions

```
SUBMITTED → WORKING → COMPLETED
                    → FAILED
                    → CANCELED (via tasks/cancel)
                    → INPUT_REQUIRED → (client follow-up) → WORKING
                    → AUTH_REQUIRED  → (client authenticates) → WORKING
           → REJECTED (skill mismatch, rate limit, etc.)
```

### Task Persistence

Tasks are stored with configurable backends:
- **In-memory** (default for personal mode): Simple map with TTL-based cleanup
- **Database** (platform mode): Persisted in the team-scoped database for durability and audit

Task records include:
- Task ID (server-generated UUID)
- Context ID (session grouping)
- Current state + state history
- Messages (conversation turns)
- Artifacts (generated outputs)
- Push notification config (if registered)
- Timestamps (created, updated)

## Push Notifications (Async Callbacks)

When an external agent registers a push notification URL, Astonish delivers results asynchronously:

```json
{
  "method": "pushNotification/set",
  "params": {
    "taskId": "task-123",
    "pushNotificationConfig": {
      "url": "https://agent.example.com/a2a/callback",
      "token": "callback-auth-token"
    }
  }
}
```

The `PushNotifier` component:
1. Stores the webhook URL per task
2. On task state change → POSTs a `TaskStatusUpdateEvent` to the URL
3. On artifact generation → POSTs a `TaskArtifactUpdateEvent`
4. Includes the `token` in the `Authorization` header for verification
5. Retries with exponential backoff on failure (max 3 attempts)

This is the mechanism that enables true async processing: the external agent fires a request, disconnects, and receives the result via callback when ready.

## Streaming (SSE)

A2A streaming uses Server-Sent Events, which aligns perfectly with Astonish's existing SSE infrastructure (Studio Chat already streams via SSE).

### SSE Event Format

Each SSE `data:` field contains a complete JSON-RPC 2.0 response:

```
data: {"jsonrpc":"2.0","id":"req-1","result":{"task":{"id":"task-123","contextId":"ctx-456","status":{"state":"working"},"history":[...]}}}

data: {"jsonrpc":"2.0","id":"req-1","result":{"taskId":"task-123","status":{"state":"working","message":{"role":"agent","parts":[{"text":"Analyzing..."}]}}}}

data: {"jsonrpc":"2.0","id":"req-1","result":{"taskId":"task-123","artifact":{"name":"report.md","parts":[{"text":"# Report\n..."}],"lastChunk":true}}}

data: {"jsonrpc":"2.0","id":"req-1","result":{"taskId":"task-123","status":{"state":"completed","message":{"role":"agent","parts":[{"text":"Done. Report generated."}]}}}}
```

### Reconnection

If the SSE connection drops, clients can resubscribe via `tasks/resubscribe` with the task ID to resume receiving events from the current state.

## Configuration

### Platform Configuration (YAML)

```yaml
channels:
  a2a:
    enabled: true
    # Base URL for Agent Card and endpoints (auto-detected from daemon config if omitted)
    base_url: "https://astonish.example.com"
    # Authentication methods to advertise
    auth_methods:
      - bearer
      - api_key
    # Rate limiting per agent identity
    rate_limit:
      requests_per_minute: 60
      max_concurrent_tasks: 10
    # Task retention
    task_ttl: "72h"
    # Push notification settings
    push:
      max_retries: 3
      retry_backoff: "5s"
      timeout: "30s"
```

### Per-Team Overrides

Teams can customize their A2A exposure:
- Which skills to advertise
- Custom Agent Card description
- Stricter rate limits
- Allowed agent identities (allowlist)

## Security Considerations

### Rate Limiting

Unlike human users who type slowly, AI agents can flood requests. The A2A adapter enforces:
- Per-agent-identity request rate limiting
- Maximum concurrent tasks per agent
- Maximum task duration (timeout + cancellation)

### Input Validation

- Message size limits (prevent oversized payloads)
- Part count limits (prevent resource exhaustion)
- Skill matching validation (reject tasks for non-advertised skills)

### Context Isolation

- A2A sessions are structurally isolated from human channel sessions
- Agent identities are separate from user identities
- Audit trails distinguish agent-initiated vs. human-initiated actions
- A2A tasks cannot access sessions from other channels

### Credential Redaction

All outbound responses pass through the existing `credentials.Redactor` — same as Telegram/Slack/Email. Secrets are never leaked to external agents.

## Relationship to MCP

A2A and MCP are **complementary**, not competing:

| Protocol | Purpose | Astonish Role |
|---|---|---|
| **MCP** | Agent ↔ Tools/Data | Astonish consumes MCP servers (tool provider) |
| **A2A** | Agent ↔ Agent | Astonish serves as an A2A agent (task executor) |

An external agent uses A2A to **delegate a task** to Astonish. Astonish then uses its internal tools (including MCP-connected tools) to fulfill that task. The external agent never sees Astonish's internal tools — it only sees the task result.

## Key Files (Planned)

| File | Purpose |
|---|---|
| `pkg/a2a/types.go` | A2A protocol types (AgentCard, Task, Message, Part, Artifact, TaskState) |
| `pkg/a2a/agent_card.go` | Agent Card builder (static + dynamic skills from team config) |
| `pkg/a2a/task_store.go` | TaskStore interface + in-memory implementation |
| `pkg/a2a/push.go` | PushNotifier for async webhook callbacks |
| `pkg/channels/a2a/a2a.go` | A2AChannel implementing the Channel interface |
| `pkg/api/a2a_routes.go` | HTTP routes: agent card endpoint, JSON-RPC handler, SSE stream handler |
| `pkg/api/a2a_auth.go` | A2A-specific authentication middleware |

## Dependencies

- **`github.com/a2aproject/a2a-go/v2`** — Official Go SDK (A2A v1.0 compliant). Provides protocol types, JSON-RPC handling, and SSE utilities. Requires Go 1.25+.
- Existing Astonish infrastructure: `pkg/channels` (Channel interface), `pkg/api` (HTTP routing), `pkg/credentials` (auth + redaction), `pkg/session` (session management).

## A2A Client (Astonish Calling External Agents)

The A2A Client role — Astonish connecting outward to external A2A agents — is now implemented and documented separately. See **[`a2a-client.md`](./a2a-client.md)** for the full architecture covering:
- Configuration cascade (file → platform → org → team)
- Credential integration via `CredentialResolver`
- Tool generation from Agent Card skills
- Streaming support (`message/stream` via SSE)
- Multi-tenant isolation guarantees

### Multi-Client Architecture

Unlike other channel adapters (Telegram, Slack, Email) which maintain a 1:1 relationship between an adapter instance and a bot account, the A2A channel supports **multiple simultaneous external agent connections** through a single adapter instance.

**Agent Registry** (`pkg/a2a/agent_registry.go`):
- Each external agent is registered with a unique ID, name, and API key
- API keys are stored as SHA-256 hashes (never plaintext)
- Lookup uses constant-time comparison to prevent timing attacks
- Admin endpoints (`/api/admin/a2a/agents`) manage registration and key rotation

**Per-Agent Isolation**:
- Tasks are scoped to the agent that created them (`task.AgentID`)
- An agent cannot access, cancel, or modify another agent's tasks
- Session keys include the agent ID to prevent session cross-contamination
- Push notification configs are per-task (and thus per-agent)

**Authentication Flow**:
1. External agent sends request with `Authorization: Bearer <key>` or `X-API-Key: <key>`
2. `A2AAuthMiddleware` looks up the agent in the registry
3. On success, injects `*RegisteredAgent` into request context
4. Handler methods receive the agent and enforce ownership on all operations

### Identity Propagation

A2A supports **identity propagation** — an external agent can act on behalf of an Astonish platform user. This ensures the request executes with the correct org/team context, credentials, and memories.

**Resolution Priority** (`pkg/a2a/identity.go`):
1. Propagated identity from message metadata (if `AllowIdentityPropagation=true`)
2. Agent's linked user ID (default identity)

**Metadata Format**:
```json
{
  "extensions": {
    "identity": {
      "user_email": "alice@example.com"
    }
  }
}
```

Or flat format:
```json
{
  "user_email": "alice@example.com"
}
```

**Integration with PlatformResolver**:
- The resolved `externalID` is passed to `PlatformResolver.ResolveChannelUserWithHint`
- Channel type is always `"a2a"`
- Org/team hints come from the agent's `LinkedOrgSlug`/`LinkedTeamSlug`

### Admin API

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/api/admin/a2a/agents` | GET | List registered agents |
| `/api/admin/a2a/agents` | POST | Register new agent (returns API key once) |
| `/api/admin/a2a/agents/<id>` | DELETE | Remove agent |
| `/api/admin/a2a/agents/<id>/rotate-key` | POST | Rotate API key |

### Configuration

A2A is configured via platform settings (DB source of truth):

```json
{
  "channels": {
    "a2a": {
      "enabled": true,
      "base_url": "https://astonish.example.com",
      "task_ttl": "72h",
      "allow_identity_propagation": true
    }
  }
}
```

Or via `config.yaml`:
```yaml
channels:
  a2a:
    enabled: true
    base_url: https://astonish.example.com
    task_ttl: 72h
```

## Interactions

- **Channel System**: A2A Server is a channel adapter — same lifecycle as Telegram/Slack/Email.
- **Agent Engine**: Tasks are processed by the ChatAgent with dedicated sessions.
- **Sessions**: Per-contextId persistent sessions (same as per-conversation for other channels).
- **Credentials**: Auth validation + outbound redaction.
- **Multi-Tenant**: PlatformResolver maps agent identity → team context.
- **Scheduler**: Scheduled jobs could target A2A push endpoints (future).
- **Daemon**: A2AChannel initialized during daemon startup alongside other channels.
