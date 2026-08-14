# pkg/channels/a2a — AGENTS.md

A2A (Agent-to-Agent) protocol channel adapter. Implements the `channels.Channel` interface to receive tasks from external AI agents via the A2A v1.0 protocol.

## Scope
- `a2a.go` — `A2AChannel` struct implementing `Channel` interface.
- `handler.go` — Request handling: message normalization, task lifecycle, response delivery.
- `session.go` — Session key generation with agent isolation and identity propagation.

## Key Design Decisions

### Multi-Client Architecture
Unlike other channels (1 adapter = 1 bot account), the A2A channel handles ALL registered agents through a single adapter instance. Agent identity comes from HTTP authentication (resolved in `pkg/api/a2a_auth.go`), passed into handler methods.

### Identity Propagation
External agents can act on behalf of Astonish users. The `pkg/a2a/identity.go` module resolves the effective user from message metadata. The adapter uses `PlatformResolver` with the resolved identity, exactly like Telegram/Slack/Email channels.

### HTTP-Driven (Not Polling)
The A2A channel does NOT poll an external service. Instead, HTTP handlers in `pkg/api/a2a_routes.go` receive requests and call into this adapter's handler methods. `Start()` returns immediately.

### Task Scoping
Each registered agent can only access its own tasks. Session keys are prefixed with agent ID to ensure isolation.

## Key Rules
1. **Never expose one agent's tasks to another.** Task ownership is enforced at every access point.
2. **Identity propagation requires explicit opt-in.** The agent must have `AllowIdentityPropagation=true`.
3. **Response delivery uses channels.** The HTTP handler creates a response channel; `Send()` writes to it.
4. **Follow the domain-agnostic abstraction rule.** No A2A-specific logic in `pkg/channels/manager.go`.
