# Chat Interface

The Chat tab is Studio's primary interaction surface. It provides a rich conversation interface with streaming responses, tool call visualization, and artifact rendering.

## Streaming Responses

Chat uses Server-Sent Events (SSE) to stream agent responses in real time. Tokens appear as they're generated — no waiting for the full response before seeing output.

## Model Selection

The chat toolbar includes a **Model** control on the left:

- **Before the first message** — Choose a provider and model for the new session (or leave **default — cascade** to use Settings defaults). The choice is pinned when the session starts.
- **During a session** — Open the same control to change or reset the pin. The pin applies to that session only; other users and sessions keep their own selection.

The resolution order for each message is:

```
Session pin → User default → Team → Org → Platform
```

Empty pin fields fall through to the next layer. If a pinned provider has no credential, the session still runs on the cascade default and Studio shows a soft warning — the pin is not cleared automatically.

`/status` reports the **effective model for this session** (including any pin), not a process-wide singleton.

Team-wide defaults still live in **Settings → Providers**. Use the chat Model control for per-conversation overrides.

## Tool Call Visualization

When the agent invokes tools, the chat displays:

- **Tool name** and parameters (collapsible)
- **Execution status** (running, complete, error)
- **Output** formatted for readability (code blocks, tables)

Tool calls appear inline within the response stream, showing exactly when and how the agent uses its capabilities.

## Slash Commands

Type `/` in the input to access available commands:

| Command | Description |
|---------|-------------|
| `/help` | Show available commands |
| `/status` | Show this session's provider, model (including pin), and tools info |
| `/new` | Start a fresh conversation |
| `/compact` | Show context window usage |
| `/distill` | Distill the current session into a reusable flow |
| `/fleet` | Start a fleet-based task with specialized agents |
| `/fleet-plan` | Create a reusable fleet plan |
| `/drill` | Create a drill suite with guided wizard |
| `/drill-add` | Add new drills to an existing suite |

## Artifact Rendering

When the agent produces artifacts (files, reports, applications), they render inline:

- **Markdown reports** — Rendered as formatted documents directly in the chat
- **Code files** — Syntax-highlighted with copy button
- **Applications** — Embedded preview with live interaction (see [Generative UI](../generative-ui/))
- **Slide decks** — Interactive generation progress, preview, navigation, editing, saving, presentation, and export controls (see [Slides](../slides/))

Non-report file writes appear as compact download tiles linking to the Files panel.

## Session Management

The sidebar lists previous sessions with timestamps and preview text. Click any session to resume it. Sessions can be:

- Resumed at any time
- Deleted when no longer needed

## Input Features

- **Multi-line input** — Shift+Enter for new lines
- **File attachment** — Drag and drop files into the input area
- **Slash command autocomplete** — Type `/` to see available commands with filtering
