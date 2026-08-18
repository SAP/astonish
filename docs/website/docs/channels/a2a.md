# A2A (Agent-to-Agent) Protocol

The A2A channel enables **external AI agents** to interact with your Astonish agents using the open [Agent-to-Agent (A2A) protocol](https://github.com/a2aproject/a2a-spec) (Linux Foundation, v1.0). Unlike other channels where humans send messages, A2A allows machine-to-machine agent collaboration with enterprise-grade authentication.

## Key Concepts

| Concept | Description |
|---------|-------------|
| **Agent Card** | Discovery document at `/.well-known/agent-card.json` describing your agent's capabilities |
| **Task** | A unit of work submitted by an external agent. Has a lifecycle: submitted → working → completed |
| **Trusted Issuer** | An Identity Provider (IdP) whose JWTs Astonish trusts for A2A authentication |
| **Allowed Agent** | An external service authorized to call A2A on behalf of users (identified by its `act.sub` claim) |
| **JWT Bearer Auth** | All A2A requests are authenticated via a signed JWT in the `Authorization: Bearer` header |

## How It Works

```
┌──────────────┐     ┌──────────────┐     ┌──────────────┐
│   External   │────▶│   A2A Auth   │────▶│   Astonish   │
│   AI Agent   │◀────│  (JWT/JWKS)  │◀────│   Agent      │
└──────────────┘     └──────────────┘     └──────────────┘
        │                    │
        │  1. Discover       │  2. Authenticate (JWT)
        │  (Agent Card)      │  3. Send Task
        │                    │  4. Receive Result
```

1. External agent discovers your capabilities via the Agent Card
2. External agent authenticates with a signed JWT from a trusted Identity Provider
3. External agent sends a task (JSON-RPC over HTTP)
4. Astonish processes the task and returns the result

## Authentication

All A2A requests require a **JWT Bearer token** in the `Authorization` header:

```
Authorization: Bearer <signed-jwt>
```

The JWT must be issued by a **trusted issuer** configured in your Astonish instance. Astonish validates:
- **Signature** — verified against the issuer's JWKS (JSON Web Key Set) endpoint
- **Issuer (`iss`)** — must match a configured trusted issuer
- **Audience (`aud`)** — must contain the configured audience value
- **Expiry (`exp`)** — token must not be expired
- **Algorithm** — pinned to the key type (RSA → RS256/384/512, EC → ES256/384/512)

### Token Types

Astonish supports two token patterns:

#### Direct User Token
A user authenticates directly with the IdP and sends their own token:
```json
{
  "iss": "https://auth.company.com",
  "sub": "alice@company.com",
  "aud": "astonish-a2a",
  "exp": 1735689600,
  "iat": 1735686000
}
```

#### Delegated Token (Service acting on behalf of a user)
A service obtains a delegated token via [OAuth 2.0 Token Exchange (RFC 8693)](https://datatracker.ietf.org/doc/html/rfc8693) with an `act` claim identifying the calling service:
```json
{
  "iss": "https://auth.company.com",
  "sub": "alice@company.com",
  "aud": "astonish-a2a",
  "act": { "sub": "my-service-client-id" },
  "exp": 1735689600,
  "iat": 1735686000
}
```

When `act.sub` is present, Astonish verifies that the actor is in the **allowed agents** list. This prevents unauthorized services from making calls even if they can obtain a valid token.

## Setup

### 1. Enable the A2A Channel

#### Via YAML config (self-hosted / simple setups)

```yaml
channels:
  a2a:
    enabled: true
    base_url: https://your-astonish-instance.example.com
    task_ttl: 72h
    default_audience: "astonish-a2a"
    require_actor_claim: false  # true = only delegated tokens allowed
    trusted_issuers:
      - name: "Company IdP"
        issuer: "https://auth.company.com"
        jwks_url: "https://auth.company.com/.well-known/jwks.json"
        audience: "astonish-a2a"
        user_claim: "email"  # which JWT claim identifies the user (default: "sub")
    allowed_agents:
      - name: "Partner Service"
        actor_sub: "partner-service-client-id"
        issuer: "Company IdP"  # references trusted issuer by name
        rate_limit: 60         # max requests per minute (0 = unlimited)
        max_tasks: 10          # max concurrent tasks (0 = unlimited)
```

#### Via Platform Admin API

```bash
curl -X PUT https://your-instance.example.com/api/platform/channels/a2a \
  -H "Authorization: Bearer <admin-session-token>" \
  -H "Content-Type: application/json" \
  -d '{
    "enabled": true,
    "config": {
      "base_url": "https://your-instance.example.com",
      "task_ttl": "72h",
      "default_audience": "astonish-a2a",
      "require_actor_claim": false,
      "trusted_issuers": [
        {
          "name": "Company IdP",
          "issuer": "https://auth.company.com",
          "jwks_url": "https://auth.company.com/.well-known/jwks.json",
          "audience": "astonish-a2a",
          "user_claim": "email"
        }
      ],
      "allowed_agents": [
        {
          "name": "Partner Service",
          "actor_sub": "partner-service-client-id",
          "issuer": "Company IdP",
          "rate_limit": 60,
          "max_tasks": 10
        }
      ]
    }
  }'
```

### 2. Configure Your Identity Provider

Your IdP must:
1. **Serve a JWKS endpoint** — Astonish fetches public keys from this URL to verify token signatures
2. **Issue JWTs with required claims** — `iss`, `sub`, `aud`, `exp` (and `kid` in the header)
3. **Support token exchange** (optional) — if services will call on behalf of users via delegated tokens

Common IdPs that work out of the box:
- Auth0
- Okta
- Azure AD / Entra ID
- SAP IAS (Identity Authentication Service)
- Keycloak
- Google Workspace

### 3. Share Your Agent Card

Your Agent Card is automatically served at:
```
https://your-instance.example.com/.well-known/agent-card.json
```

This is a public endpoint (no authentication required) that external agents use to discover your capabilities and supported authentication methods.

## Sending Tasks (For External Agents)

External agents interact with your A2A endpoint using JSON-RPC 2.0.

### Send a Message (Synchronous)

```bash
curl -X POST https://your-instance.example.com/api/a2a \
  -H "Authorization: Bearer <jwt-token>" \
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

Response:
```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "result": {
    "id": "task-uuid-here",
    "contextId": "conversation-123",
    "status": {
      "state": "completed",
      "message": {
        "role": "agent",
        "parts": [{"type": "text", "text": "Based on the sales report..."}]
      }
    },
    "artifacts": []
  }
}
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

The task is returned immediately in `working` state. Poll with `tasks/get` or register a push notification webhook.

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

## Streaming (SSE)

For real-time streaming responses, use the streaming endpoint:

```bash
curl -X POST https://your-instance.example.com/api/a2a/stream \
  -H "Authorization: Bearer <jwt-token>" \
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

## Identity & Task Isolation

### How Identity Works

Every A2A request executes in the context of a specific user:

- **`sub` claim** → identifies the user (email, user ID, or whatever `user_claim` is configured to)
- **`act.sub` claim** → identifies the calling service (for delegation flows)
- **Task ownership** → each task is scoped to the composite identity `actor:user`, preventing cross-user task access

When a service calls on behalf of multiple users, each user's tasks are isolated — `service-A` acting for `user-1` cannot see tasks created for `user-2`, even though the same service made both calls.

### Delegation Flow (Token Exchange)

A typical enterprise integration:

```
1. User authenticates to your application via your IdP
2. Your service performs OAuth 2.0 Token Exchange:
   - subject_token = user's access token
   - client_credentials = your service's identity
   - audience = "astonish-a2a"
3. IdP issues a delegated JWT with act claim
4. Your service sends the delegated JWT to Astonish A2A
5. Astonish validates: signature ✓, issuer ✓, audience ✓, actor allowed ✓
6. Request executes as the user, with the service identified as the actor
```

## Rate Limiting

Per-agent rate limiting protects your Astonish instance from abuse:

- **Rate limit** — maximum requests per minute per agent (sliding window)
- **Max concurrent tasks** — maximum simultaneous in-flight tasks per agent

When limits are exceeded, the endpoint returns a JSON-RPC error:
```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "error": {
    "code": -32003,
    "message": "Rate limit exceeded"
  }
}
```

Configure limits per allowed agent in your channel settings:
```yaml
allowed_agents:
  - name: "Partner Service"
    actor_sub: "partner-client-id"
    issuer: "Company IdP"
    rate_limit: 60    # 60 requests/minute
    max_tasks: 10     # max 10 concurrent tasks
```

Agents without explicit limits (or with `0` values) are unlimited.

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

## Configuration Reference

### Channel Settings

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | bool | `false` | Enable/disable the A2A channel |
| `base_url` | string | listen address | External URL for the Agent Card |
| `task_ttl` | string | `"72h"` | How long completed tasks are retained |
| `default_audience` | string | — | Expected `aud` claim when not overridden per-issuer |
| `require_actor_claim` | bool | `false` | If true, reject tokens without `act` claim |
| `auto_link_by_email` | bool | `false` | Auto-link users by matching email claim |

### Trusted Issuer Fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | ✓ | Identifier for this issuer (referenced by allowed agents) |
| `issuer` | string | ✓ | Expected `iss` claim value (e.g., `https://auth.company.com`) |
| `jwks_url` | string | ✓ | URL to fetch signing keys (JWKS endpoint) |
| `audience` | string | ✓ | Expected `aud` claim value |
| `user_claim` | string | — | Which claim identifies the user. Default: `"sub"`. Common: `"email"`, `"preferred_username"` |

### Allowed Agent Fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | ✓ | Human-readable name for this agent |
| `actor_sub` | string | ✓ | Must match the `act.sub` claim in the JWT |
| `issuer` | string | ✓ | References a trusted issuer by `name` |
| `rate_limit` | int | — | Max requests per minute (0 = unlimited) |
| `max_tasks` | int | — | Max concurrent tasks (0 = unlimited) |

## Error Codes

| Code | Name | Description |
|------|------|-------------|
| `-32001` | Task Not Found | Task doesn't exist or caller doesn't own it |
| `-32002` | Auth Required | Missing or expired authentication token |
| `-32003` | Rate Limited | Per-agent rate or concurrency limit exceeded |
| `-32004` | Forbidden | Token valid but actor not allowed, or user not provisioned |
| `-32600` | Invalid Request | Malformed JSON-RPC |
| `-32601` | Method Not Found | Unknown JSON-RPC method |
| `-32602` | Invalid Params | Method params don't match expected schema |
| `-32603` | Internal Error | Server-side processing error |
| `-32700` | Parse Error | Invalid JSON |

## Security Model

- **No plaintext secrets** — authentication is via signed JWTs verified against JWKS public keys
- **Signature verification** — every token's signature is cryptographically verified; algorithm is pinned to key type
- **Issuer pinning** — only tokens from explicitly configured trusted issuers are accepted
- **Audience validation** — tokens must be intended for your Astonish instance
- **Actor authorization** — delegated tokens require the calling service to be in the allowed agents list
- **Task isolation** — each agent/user combination can only access its own tasks
- **Rate limiting** — per-agent request rate and concurrency limits prevent abuse
- **Key rotation** — JWKS keys are cached with automatic refresh on unknown `kid`

## Quick Start: End-to-End Example

Here's a complete example using a Keycloak IdP:

**1. Configure Astonish** (in `config.yaml`):
```yaml
channels:
  a2a:
    enabled: true
    base_url: "https://astonish.mycompany.com"
    default_audience: "astonish-a2a"
    trusted_issuers:
      - name: "Keycloak"
        issuer: "https://keycloak.mycompany.com/realms/main"
        jwks_url: "https://keycloak.mycompany.com/realms/main/protocol/openid-connect/certs"
        audience: "astonish-a2a"
        user_claim: "email"
    allowed_agents:
      - name: "My Service"
        actor_sub: "my-service-client"
        issuer: "Keycloak"
        rate_limit: 100
        max_tasks: 20
```

**2. Get a token** (your service performs token exchange):
```bash
# Token exchange: get a delegated token for the user
TOKEN=$(curl -s -X POST https://keycloak.mycompany.com/realms/main/protocol/openid-connect/token \
  -d "grant_type=urn:ietf:params:oauth:grant-type:token-exchange" \
  -d "client_id=my-service-client" \
  -d "client_secret=$CLIENT_SECRET" \
  -d "subject_token=$USER_ACCESS_TOKEN" \
  -d "audience=astonish-a2a" \
  -d "requested_token_type=urn:ietf:params:oauth:token-type:access_token" \
  | jq -r '.access_token')
```

**3. Call Astonish A2A**:
```bash
curl -X POST https://astonish.mycompany.com/api/a2a \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "id": 1,
    "method": "message/send",
    "params": {
      "message": {
        "role": "user",
        "parts": [{"type": "text", "text": "What meetings do I have today?"}]
      }
    }
  }'
```

**4. Verify it works**:
```bash
# Discover the agent card
curl https://astonish.mycompany.com/.well-known/agent-card.json | jq .
```

## Differences from Other Channels

| Feature | Telegram/Email/Slack | A2A |
|---------|---------------------|-----|
| Sender | Human user | AI agent / service |
| Transport | Platform SDK / webhooks / IMAP | JSON-RPC 2.0 over HTTP |
| Clients | One bot account | Multiple authenticated agents |
| Auth | Platform-managed (bot tokens) | JWT Bearer from trusted IdPs |
| Identity | Direct user mapping | JWT claims (sub + optional act) |
| Rate limiting | Platform-enforced | Per-agent configurable limits |
| Broadcast | Supported | Not supported |
