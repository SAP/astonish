# Chat Commands

The `astonish chat` command starts an interactive agent session from the terminal.

## Usage

```bash
astonish chat [flags]
```

Running `astonish chat` with no arguments starts a new interactive session using your default provider and model.

## Flags

| Flag | Short | Description |
|------|-------|-------------|
| `--provider` | `-p` | AI provider to use (pins onto the new session by default) |
| `--model` | `-m` | Model name (pins onto the new session by default) |
| `--resume` | `-r` | Resume a session by ID (or latest if no ID given) |
| `--no-pin` | | Do not persist `-p`/`-m` as a session pin (ephemeral override only) |
| `--clear-model` | | Clear the model pin on the resumed session (requires `--resume`) |
| `--workspace` | `-w` | Working directory (default: current directory) |
| `--auto-approve` | | Auto-approve all tool executions |
| `--debug` | | Enable debug mode |

## Examples

```bash
# Start a new chat with defaults
astonish chat

# Chat with a specific provider and model (pins onto the session)
astonish chat -p openai -m gpt-4o

# Same as above but ephemeral — model is NOT pinned
astonish chat -p openai -m gpt-4o --no-pin

# Resume the most recent session
astonish chat -r

# Resume a specific session
astonish chat -r abc123

# Resume with a one-time model override (does NOT rewrite the existing pin)
astonish chat --resume abc123 -m claude-sonnet-4

# Clear the model pin on a resumed session (falls back to cascade default)
astonish chat --resume abc123 --clear-model

# Auto-approve all tool calls
astonish chat --auto-approve
```

## Subcommands

### `astonish chat model <provider>:<model>`

Change or clear the model pin on the most recent session (or a specific session with `--session`).

```bash
# Pin the last session to openai/gpt-4o
astonish chat model openai:gpt-4o

# Clear the pin (restore cascade default)
astonish chat model ""

# Pin a specific session
astonish chat model anthropic:claude-sonnet-4 --session abc123
```

The argument uses a first-colon split, so model names containing colons (e.g., `openai:gpt-4o:2024-08-06`) are handled correctly.

## Model Pin Behavior

When `-p` or `-m` is provided on a **new session** (no `--resume`), the choice is persisted as the session's model pin. On subsequent `--resume` calls without flags, the pinned model is used automatically.

When `-p` or `-m` is provided on a **resumed session** (`--resume`), the override is ephemeral — it applies to this invocation only and does NOT rewrite the stored pin. Use `--clear-model` to explicitly remove a pin.

If the pinned provider's credential is revoked or unavailable, the session still opens using the cascade default. A warning is printed to stderr; the pin is never auto-cleared.

The terminal footer shows the active provider and the resolved concrete model when the platform reports them. It does not show `default` as the model name; while the cascade is still resolving, it shows the provider with `model resolving…` instead.

## In-Session Commands

While in an active chat session, type `/` to access these commands:

| Command | Description |
|---------|-------------|
| `/help` | Show available commands |
| `/status` | Show this session's provider, model (including pin), context, tools, and session info |
| `/new` | Start a fresh conversation |
| `/compact` | Show context window usage and compaction status |
| `/distill` | Distill the current session into a reusable flow |
| `/fleet` | Show available fleets and fleet commands |
| `/fleet-plan` | Create a reusable fleet plan (redirects to Studio) |
| `/drill` | Create a drill suite with guided wizard |
| `/drill-add` | Add new drills to an existing suite |
| `/authorize <code>` | Authorize a device to access Studio |

## Network authorization prompts

When a sandboxed command tries to reach a network endpoint that is not yet allowed by policy, the terminal shows a focused network authorization prompt. Press `Enter` or `y` to allow the specific endpoint, `b` to allow the suggested broader pattern when one is shown, or `n`/`Esc` to deny. The prompt closes immediately after your key press and the terminal shows progress while Astonish records the decision and, for approvals, asks the agent to retry the blocked command.

Type `exit` or `quit` to end the session.
