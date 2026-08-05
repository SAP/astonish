# pkg/channels — AGENTS.md

Communication-channel adapters (Slack, Telegram, Email) plus routing and channel commands. All external messaging (inbound and outbound) goes through this layer.

## Scope
- `channel.go` — the `Channel` interface: `InboundMessage`, `OutboundMessage`, `Target`, `ChannelStatus`.
- `manager.go` — `ChannelManager` (channel registry, routing preferences, per-message routing decisions).
- Adapters:
  - `email/email.go` — `EmailChannel` (plus-addressing for per-org routing).
  - `slack/slack.go` — `SlackChannel`.
  - `telegram/telegram.go` — `TelegramChannel`.
- `commands.go` — `Command`, `CommandRegistry`, `CommandContext` (in-channel `/org`, `/team`, etc.).
- `fleet_commands.go` — command hooks that talk to `pkg/fleet`.

## Key rules
1. **Adapters implement `Channel`, nothing more.** Cross-cutting concerns (routing, rate limits, allowlists) live in `manager.go` and `commands.go`.
2. **Allowlists are DB-backed** — configured in platform mode via the tenant DB. Do not add file-based allowlists.
3. **Email plus-addressing**: `bot+orgname@domain.com` routes to `orgname`. The routing logic is centralized — do not re-implement it per-adapter.
4. **Do not couple to `pkg/fleet` directly** from an adapter. Adapters expose messages; `ChannelManager` and `fleet_commands.go` bridge to fleet.
5. **Follow the root domain-agnostic abstraction rule.** Channel adapters are general-purpose infrastructure. They may map generic message structure to platform-native primitives, but they must not special-case one domain, provider, customer dataset, resource type, or example response.

## When editing
1. Adding a new channel? Implement `Channel`, register it in `manager.go`, add adapter-specific config to platform/team ent schemas, and write a scenario test that exercises the routing path.
2. Adding a new in-channel command? Extend `CommandRegistry` — do not add ad-hoc parsing in adapters.
