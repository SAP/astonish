# Chat

The Astonish agent operates through an LLM-driven tool-use loop. You describe a goal, and the agent plans, executes tools, observes results, and iterates until the task is complete.

## How It Works

1. You send a message (Studio or CLI)
2. The LLM analyzes the request and selects appropriate tools
3. Tools execute, and results feed back to the LLM
4. The LLM decides: respond to you or invoke more tools
5. Loop continues until the task is resolved

There is no fixed pipeline. The agent dynamically adapts based on tool outputs, errors, and intermediate findings.

## Studio

The Studio chat interface at `http://localhost:9393` provides the full visual experience:

- Real-time streaming responses
- Inline tool execution visualization with expandable cards
- File diffs for write operations
- Artifact previews (reports, generated files, and [slide decks](../slides/))
- Model selector in the toolbar (per-session pin; see [Studio Chat](../studio/chat.md))
- Session history sidebar with search
- Token usage tracking

<!-- IMAGE: Studio chat interface showing tool execution cards and streaming response -->

## CLI

The terminal chat interface provides the same agent capabilities:

```bash
astonish chat                                      # New session
astonish chat -p anthropic -m claude-sonnet-4-20250514  # Pin provider/model on the new session
astonish chat --resume <session-id>                # Resume a session
astonish chat -r <session-id>                      # Short form
astonish chat --auto-approve                       # Skip tool confirmations
astonish chat --debug                              # Enable debug output
astonish chat -w /path/to/project                  # Set working directory
```

`-p` / `-m` on a new session persist as a session pin (same concept as the Studio Model control). See [Chat Commands](../cli/chat.md) for `--no-pin`, `--clear-model`, and `astonish chat model`.

::: warning Authentication Required
The CLI requires authentication before use. Run `astonish login <server-url>` first. See [Remote CLI](../platform/remote-cli.md).
:::

## Confirmation System

Destructive or sensitive tool calls require confirmation before execution:

- **auto-approve** — Tools like `read_file`, `grep_search`, `memory_search` run without asking
- **always-confirm** — Tools like `shell_command`, `write_file` prompt you first

In Studio, confirmations appear as inline approval cards. In CLI, you see a `[y/N]` prompt.

Use `--auto-approve` on the CLI to skip all confirmations (use with caution).

## Slash Commands

Slash commands are available in both Studio and CLI:

| Command | Description |
|---------|-------------|
| `/help` | Show available commands |
| `/status` | Show this session's provider, model (including pin), tools, and memory status |
| `/new` | Start a fresh conversation (new session) |
| `/compact` | Show context window usage and compaction status |
| `/distill` | Distill the current session into a reusable flow |
| `/fleet` | Start a fleet session |
| `/drill` | Create a drill suite with guided wizard |

## Multi-Turn Context

The agent maintains full context within a session. Reference previous messages, build on prior tool results, and incrementally refine solutions. For long-running sessions, the agent automatically compacts context when approaching token limits.

Chat can also create and refine complete presentations from prompts, attachments, research, and connected systems. See [Slides](../slides/) for templates, editing, saving, presenting, and export.

See [Sessions](./sessions.md) for persistence details and [Tools Overview](./tools/index.md) for the complete tool catalog.
