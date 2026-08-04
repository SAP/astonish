# Slack

The Slack channel integrates Astonish into your Slack workspace, enabling AI agent interactions in channels, DMs, and threads.

## Setup

### 1. Create a Slack App

1. Go to [api.slack.com/apps](https://api.slack.com/apps) and click **Create New App**
2. Choose **From scratch**, name it, and select your workspace
3. Under **OAuth & Permissions**, add these bot token scopes:
   - `chat:write`
   - `commands`
   - `app_mentions:read`
   - `im:history`
   - `im:read`
   - `im:write`
   - `users:read`
   - Optional, if you want channel/private-channel history or App Agent features: `channels:history`, `groups:history`, `assistant:write`
4. Under **Socket Mode**, enable Socket Mode.
5. For Socket Mode: under **App-Level Tokens**, create a token with `connections:write` scope (`xapp-...`).
6. Under **Event Subscriptions**:
   - Turn **Enable Events** on.
   - Under **Subscribe to bot events**, add:
     - `message.im` — required for direct messages to the app.
     - `app_mention` — required for `@Astonish` mentions in channels.
     - Optional, for Slack App Agent features: `assistant_thread_started`, `assistant_thread_context_changed`.
7. Under **Slash Commands**, create a `/link` command. For Socket Mode, Slack delivers this over the WebSocket. For Events API mode, set the request URL to `https://<your-astonish-host>/api/slack/commands`.
8. Install or **reinstall** the app to your workspace after changing scopes, slash commands, or event subscriptions, then copy the **Bot User OAuth Token** (`xoxb-...`).

### 2. Configure via CLI

Run the interactive setup wizard:

```bash
astonish channels setup slack
```

The wizard validates your bot token, collects the app-level token (for Socket Mode) or signing secret (for Events API), and stores credentials securely.

Alternatively, configure manually:

```yaml
channels:
  slack:
    enabled: true
    mode: "socket"              # "socket" (WebSocket) or "events" (HTTP webhook)
    bot_token: "xoxb-..."       # Stored in credential store
    app_token: "xapp-..."       # For Socket Mode (stored in credential store)
    allow_from:
      - "U0KRQLJ9H"            # Allowed Slack user IDs
```

### 3. Start the Daemon

```bash
astonish daemon start
```

## Connection Modes

| Mode | Transport | Use Case |
|------|-----------|----------|
| `socket` | WebSocket (Socket Mode) | Recommended for most setups. No public URL needed. |
| `events` | HTTP webhook (Events API) | For environments requiring HTTP endpoints. Needs `signing_secret`, `POST /api/slack/events` as the Events request URL, and `POST /api/slack/commands` as the `/link` slash-command request URL. |

## Interaction Patterns

- **Direct Message** — Send a DM to the bot for a private conversation
- **Mention** — `@Astonish <message>` in any channel the bot is invited to
- **Thread** — Replies within a thread maintain session context

## Message Formatting

Astonish sends Slack-native replies. Regular responses are converted to Slack `mrkdwn`, and structured summaries may use Block Kit sections, fields, tables, and a compact context footer for readability. Markdown tables and grouped inventory-style lists can be rendered as Slack table blocks without relying on a specific domain or topic. Long answers, code blocks, and content that would exceed Slack block limits automatically fall back to chunked text replies.

## Available Commands

Send these as messages to the bot. Slack account linking uses Slack's configured `/link` slash command; the other commands are handled by Astonish after your Slack account is linked.

| Command | Description |
|---------|-------------|
| `/help` | Show available commands |
| `/status` | Show this session's provider, model (including pin), and session info |
| `/new` | Start a new session |
| `/distill` | Distill the last task into a reusable flow |
| `/jobs` | Show scheduled jobs |
| `/org <slug>` | Switch active organization |
| `/team <slug>` | Switch active team |
| `/context` | Show current routing context |
| `/fleet` | Start a fleet session |

## Multi-Tenant Routing (PostgreSQL)

In PostgreSQL deployments, Slack gains multi-tenant capabilities:

- **User linking** — Slack users link their account to their platform identity via `/link <code>`
- **Context switching** — `/org` and `/team` commands change the active context
- **Platform-managed access** — Access is governed by platform org membership rather than a static allowlist

### Linking Slack to Platform Account

```
User: /link ABC123
Bot:  ✓ Account linked. You're now connected as alice@acme.corp
```

If Slack shows no response when you run `/link`, verify that the Slack app has a `/link` slash command and that Socket Mode or the `/api/slack/commands` request URL is configured. Slack slash commands are not delivered as normal DM text.

If `/link` works but normal chat messages do not, verify that **Event Subscriptions** is enabled, that the bot is subscribed to `message.im` and `app_mention`, and that the app was reinstalled after those changes. OAuth scopes grant permission, but bot event subscriptions control which message events Slack delivers to Astonish.

## Managing the Channel

```bash
astonish channels status           # Check channel status
astonish channels disable slack    # Disable the channel
```
