# Channels Overview

Channels are communication adapters that connect external messaging platforms to the Astonish agent engine. They allow users to interact with their AI agents through familiar interfaces — Telegram, Email, or Slack — without changing how agents process requests.

## Supported Platforms

| Platform | Transport | Key Feature |
|----------|-----------|-------------|
| [Telegram](./telegram.md) | Bot API (polling/webhook) | Real-time chat, inline commands |
| [Email](./email.md) | IMAP/SMTP | Asynchronous, plus-addressing routing |
| [Slack](./slack.md) | Events API + OAuth | Workspace integration, threads |
| [A2A](./a2a.md) | JSON-RPC over HTTP | Agent-to-agent collaboration, multi-client |

## Architecture

Every channel adapter follows the same pattern:

1. **Receive** — Listen for incoming messages on the platform's transport
2. **Authenticate** — Verify the sender against the allowlist (config-based or database-backed)
3. **Route** — Determine which organization and team context to use
4. **Execute** — Pass the message to the agent engine (same engine used by CLI and Studio)
5. **Respond** — Format the agent's output for the platform and deliver it back

```
┌────────────┐     ┌─────────────┐     ┌──────────────┐
│  Telegram  │────▶│   Channel   │────▶│    Agent     │
│  Email     │◀────│   Adapter   │◀────│    Engine    │
│  Slack     │     └─────────────┘     └──────────────┘
│  A2A       │
└────────────┘
```

The agent engine is shared across all interfaces. A conversation started in Telegram uses the same flows, tools, and memory as one started in Studio or the CLI.

## Local vs Cloud Deployment

### Local (SQLite)

In SQLite deployments, channels use a static allowlist defined in your configuration file. Routing uses your default org and team context.

```yaml
channels:
  telegram:
    enabled: true
    bot_token: "bot-token-here"
    allow_from:
      - "123456789"  # Telegram user ID
```

### Cloud (PostgreSQL)

In PostgreSQL deployments, channels gain additional capabilities:

- **Database-backed allowlists** — Managed through the platform admin API, not static config
- **Dynamic per-message routing** — Each incoming message is routed to the correct organization and team based on sender identity or addressing
- **In-channel context switching** — Users can run `/org` and `/team` commands to change their active context without leaving the conversation
- **Multi-tenant isolation** — Messages from different organizations never cross boundaries

## Configuration

Channel configuration lives in your Astonish config file under the `channels` key. Each adapter has its own section with platform-specific settings.

See the individual channel pages for detailed setup instructions:

- [Telegram](./telegram.md)
- [Email](./email.md)
- [Slack](./slack.md)
- [A2A (Agent-to-Agent)](./a2a.md)
