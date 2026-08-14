# A2A (Agent-to-Agent) Protocol

The A2A channel enables **external AI agents** to interact with your Astonish agents using the open [Agent-to-Agent (A2A) protocol](https://github.com/a2aproject/a2a-spec) (Linux Foundation, v1.0). Unlike other channels where humans send messages, A2A allows machine-to-machine agent collaboration.

## Key Concepts

| Concept | Description |
|---------|-------------|
| **Agent Card** | Discovery document at `/.well-known/agent-card.json` describing your agent's capabilities |
| **Task** | A unit of work submitted by an external agent. Has a lifecycle: submitted → working → completed |
| **Registered Agent** | An external agent authorized to use your A2A endpoint, identified by API key |
| **Identity Propagation** | An external agent acting on behalf of a specific user in your platform |

## How It Works

```
┌──────────────┐     ┌──────────────┐     ┌──────────────┐
│   External   │────▶│   A2A Auth   │────▶│   Astonish   │
│   AI Agent   │◀────│   + Channel  │◀────│   Agent      │
└──────────────┘     └──────────────┘     └──────────────┘
        │                    │
        │  1. Discover       │  2. Authenticate
        │  (Agent Card)      │  3. Send Task
        │                    │  4. Receive Result
```

1. External agent discovers your capabilities via the Agent Card
2. External agent authenticates with its registered API key
3. External agent sends a task (JSON-RPC over HTTP)
4. Astonish processes the task and returns the result

## Setup

### 1. Enable the A2A Channel

In your platform admin settings, enable the A2A channel:

```yaml
channels:
  a2a:
    enabled: true
    base_url: https://your-astonish-instance.example.com
    task_ttl: 72h  # How long completed tasks are retained
```

Or via the Platform Admin UI under **Settings → Channels → A2A**.

### 2. Register External Agents

Each external agent that will connect needs to be registered. Registration generates an API key that the agent uses for authentication.

**Via API:**

```bash
curl -X POST https://your-instance.example.com/api/admin/a2a/agents \
  -H "Authorization: Bearer <admin-token>" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "Partner Agent",
    "description": "Our partner's AI assistant",
    "linked_user_id": "service-account@example.com",
    "linked_org_slug": "myorg",
    "linked_team_slug": "engineering",
    "allow_identity_propagation": false
  }'
```

Response:
```json
{
  "agent": {
    "id": "abc123",
    "name": "Partner Agent",
    "linked_user_id": "service-account@example.com"
  },
  "api_key": "a2a_xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx"
}
```

::: warning
The API key is shown **only once** at registration time. Store it securely — it cannot be retrieved later (only rotated).
:::

### 3. Share Your Agent Card

Your Agent Card is automatically served at:
```
https://your-instance.example.com/.well-known/agent-card.json
```

This is a public endpoint (no authentication required) that external agents use to discover your capabilities.

## Authentication

External agents authenticate using one of two methods:

| Method | Header | Example |
|--------|--------|---------|
| Bearer Token | `Authorization` | `Authorization: Bearer a2a_xxx...` |
| API Key | `X-API-Key` | `X-API-Key: a2a_xxx...` |

## Sending Tasks (For External Agents)

External agents interact with your A2A endpoint using JSON-RPC 2.0:

### Send a Message (Synchronous)

```bash
curl -X POST https://your-instance.example.com/api/a2a \
  -H "X-API-Key: a2a_xxx..." \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "id": 1,
    "method": "message/send",
    "params": {
      "message": {
        "role": "user",
        "parts": [{"type": "text", "text": "Analyze the latest sales report"}]
      },
      "configuration": {
        "contextId": "conversation-123"
      }
    }
  }'
```

### Send a Message (Async)

For long-running tasks, use `returnImmediately: true`:

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "message/send",
  "params": {
    "message": {
      "role": "user",
      "parts": [{"type": "text", "text": "Generate a full quarterly report"}]
    },
    "configuration": {
      "returnImmediately": true
    }
  }
}
```

The task is returned immediately in `submitted`/`working` state. Poll with `tasks/get` or register a push notification webhook.

### Check Task Status

```json
{
  "jsonrpc": "2.0",
  "id": 2,
  "method": "tasks/get",
  "params": {"taskId": "task-uuid-here"}
}
```

### Cancel a Task

```json
{
  "jsonrpc": "2.0",
  "id": 3,
  "method": "tasks/cancel",
  "params": {"taskId": "task-uuid-here"}
}
```

### Push Notifications

Register a webhook to receive task updates:

```json
{
  "jsonrpc": "2.0",
  "id": 4,
  "method": "pushNotification/set",
  "params": {
    "taskId": "task-uuid-here",
    "pushNotificationConfig": {
      "url": "https://your-agent.example.com/webhook",
      "token": "your-webhook-secret"
    }
  }
}
```

## Identity Propagation

When an external agent acts on behalf of a specific user, it can propagate that user's identity. This ensures the request executes with the correct org/team context, credentials, and memories.

### Enabling

1. Set `allow_identity_propagation: true` when registering the agent
2. Include the user's identity in the message metadata:

```json
{
  "message": {
    "role": "user",
    "parts": [{"type": "text", "text": "Check my calendar"}],
    "metadata": {
      "extensions": {
        "identity": {
          "user_email": "alice@example.com"
        }
      }
    }
  }
}
```

### Resolution Priority

1. **Propagated identity** (from message metadata) — if the agent allows it
2. **Agent's linked user** — the default user configured at registration

## Task Lifecycle

```
submitted → working → completed
                   → failed
                   → input-required → working → ...
                   → canceled
         → rejected
         → canceled
```

Tasks follow a strict state machine. Terminal states (`completed`, `failed`, `canceled`, `rejected`) cannot transition further.

## Admin API Reference

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/api/admin/a2a/agents` | `GET` | List all registered agents |
| `/api/admin/a2a/agents` | `POST` | Register a new agent |
| `/api/admin/a2a/agents/{id}` | `DELETE` | Remove an agent |
| `/api/admin/a2a/agents/{id}/rotate-key` | `POST` | Rotate an agent's API key |

## Streaming (SSE)

For real-time streaming responses, use the streaming endpoint:

```bash
curl -X POST https://your-instance.example.com/api/a2a/stream \
  -H "X-API-Key: a2a_xxx..." \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "id": 1,
    "method": "message/stream",
    "params": {
      "message": {
        "role": "user",
        "parts": [{"type": "text", "text": "Explain quantum computing"}]
      }
    }
  }'
```

The response is delivered as Server-Sent Events (SSE):
```
data: {"jsonrpc":"2.0","id":1,"result":{"id":"task-123","status":{"state":"completed",...}}}
```

## Security Considerations

- API keys are stored as **SHA-256 hashes** — never in plaintext
- Key lookup uses **constant-time comparison** to prevent timing attacks
- Each agent can only access **its own tasks** — no cross-agent visibility
- Identity propagation requires **explicit admin opt-in** per agent
- The Agent Card endpoint is public (protocol requirement), but all task operations require authentication

## Differences from Other Channels

| Feature | Telegram/Email/Slack | A2A |
|---------|---------------------|-----|
| Sender | Human user | AI agent |
| Transport | Polling/Webhook | HTTP (request/response) |
| Clients | One bot account | Multiple registered agents |
| Auth | Platform-managed allowlist | Per-agent API keys |
| Identity | Direct user mapping | Propagation from agent metadata |
| Broadcast | Supported | Not supported |
