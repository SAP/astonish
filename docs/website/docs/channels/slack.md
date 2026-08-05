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
7. Under **Slash Commands**, either create a `/link` command manually or configure App Manifest command sync in Astonish. For Socket Mode, Slack delivers slash commands over the WebSocket. For Events API mode, set the command request URL to `https://<your-astonish-host>/api/slack/commands`.
8. Optional, for automatic command registration: create a Slack **App Configuration Token** from the Slack app configuration/token tooling and note the Slack **App ID**. This is **not** the Socket Mode App-Level Token (`xapp-...`) from **Basic Information → App-Level Tokens**, and it is not the bot token (`xoxb-...`). Astonish uses the configuration token to call App Manifest APIs and register `/link` plus the Slack-safe commands that make sense outside a specific thread.
9. Install or **reinstall** the app to your workspace after changing scopes, slash commands, or event subscriptions, then copy the **Bot User OAuth Token** (`xoxb-...`).

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
    app_id: "A1234567890"       # Optional, for App Manifest command sync
    config_token: "xoxe.xoxp-..." # Optional, stored in credential store
    command_url: "https://example.com/api/slack/commands" # Omitted in Socket Mode manifests; required for Events API slash commands
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

## Programmatic Command Registration

Astonish can register Slack slash commands through Slack's App Manifest APIs. Configure the Slack App ID and an App Configuration Token (`channels.slack.config_token`). In Socket Mode, Astonish sets `settings.socket_mode_enabled=true` in the synced manifest and omits slash-command URLs, so no public command endpoint is needed. In Events API mode, also configure a public HTTPS command URL (`channels.slack.command_url`). Astonish will export the current app manifest, merge the commands it owns, validate the manifest, and update Slack best-effort on startup and when command registrations change. On startup, the daemon logs either `Slack slash command manifest sync enabled` or a `Slash command manifest sync skipped` reason so missing App Manifest configuration is visible. If Slack returns `invalid_auth`, the token is usually the wrong kind; App Manifest APIs require an App Configuration Token, not the `xapp-...` Socket Mode token created under App-Level Tokens. If Slack returns `invalid_manifest` in Events API mode, verify the command URL is a public `https://` URL ending in `/api/slack/commands`.

The sync includes `/link` plus Slack-safe, app-prefixed command aliases such as `/astonish-help`, `/astonish-status`, and `/astonish-context` for commands that do not require a specific conversation thread. Slack reserves many generic command names, so Astonish does not try to register bare names such as `/status` or `/help`. Existing Slack app manifest fields are preserved, and non-Astonish slash commands are left in place. Because Slack manifest updates are exhaustive, Astonish always starts from Slack's exported manifest instead of constructing a minimal replacement manifest.

Set `command_url` to `https://<your-astonish-host>/api/slack/commands` only when using Events API mode. For Socket Mode, Astonish intentionally leaves the `url` field empty on each slash command and relies on the Socket Mode WebSocket connection (`xapp-...` token with `connections:write`) for delivery.

## Available Commands

Use Slack slash commands from the main message composer. Slack account linking uses `/link`; other commands are handled by Astonish after your Slack account is linked.

Slack does not support custom app slash commands inside thread reply composers. To run an Astonish command in a thread, mention the bot and type the command name instead:

```text
@Astonish status
@Astonish help
@Astonish jobs
@Astonish context
```

You can also include the slash or Slack alias after the mention, for example `@Astonish /status` or `@Astonish /astonish-status`. Astonish normalizes these forms to the same shared command registry and replies in that Slack thread.

Slack conversation history is scoped by thread: a new top-level DM or channel mention starts a new Astonish session, and replies in that Slack thread continue the same session. For that reason, session-scoped commands such as `/new`, `/distill`, `/fleet`, and `/fleet_stop` are not registered as Slack slash commands; use a new Slack thread to start fresh.

| Command | Description |
|---------|-------------|
| `/astonish-help` | Show available Slack commands |
| `/astonish-status` | Show provider, model (including pin), and routing/session info |
| `/astonish-jobs` | Show scheduled jobs |
| `/astonish-authorize <code>` | Authorize a device to access Astonish Studio |
| `/astonish-org <slug>` | Switch active organization |
| `/astonish-team <slug>` | Switch active team |
| `/astonish-context` | Show current routing context |

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

If Slack shows no response when you run `/link`, verify that the Slack app has a `/link` slash command. If using programmatic command registration, check that the App ID and App Configuration Token are configured and that the daemon logs show command sync success. Also verify that Socket Mode or the `/api/slack/commands` request URL is configured. Slack slash commands are not delivered as normal DM text.

If `/link` works but normal chat messages do not, verify that **Event Subscriptions** is enabled, that the bot is subscribed to `message.im` and `app_mention`, and that the app was reinstalled after those changes. OAuth scopes grant permission, but bot event subscriptions control which message events Slack delivers to Astonish.

## Managing the Channel

```bash
astonish channels status           # Check channel status
astonish channels disable slack    # Disable the channel
```
