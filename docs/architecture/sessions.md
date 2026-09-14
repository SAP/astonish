# Session Management

## Overview

Sessions persist the conversation state between the user and the agent across turns, daemon restarts, and UI transitions. Astonish implements ADK's `session.Service` interface with a file-based store that uses JSONL transcript files and a JSON metadata index.

Sessions are the backbone of continuity -- they store the full conversation history (user messages, LLM responses, tool calls, tool results) that gets replayed to the LLM on each turn to maintain context.

## Key Design Decisions

### Why File-Based Instead of a Database

A file-based approach was chosen over SQLite or other databases because:

- **Inspectability**: JSONL transcripts can be read, searched, and debugged with standard Unix tools.
- **Simplicity**: No database driver dependency, no schema migrations, no connection pooling.
- **Portability**: Sessions are plain files in the user's config directory. Easy to back up, move, or delete.
- **Streaming writes**: JSONL is append-only -- each event is one line appended to the file. No transaction overhead.

The tradeoff is that operations like "find all sessions for a user" require scanning the metadata index, and concurrent writes need mutex protection.

### Why Three-Tier State

Session state is organized into three scopes mirroring ADK's internal model:

- **App state**: Shared across all sessions for an application.
- **User state**: Shared across all sessions for a user within an app.
- **Session state**: Private to a single session.

This allows flow nodes to communicate via session state (`x["variable"]` in conditions), while persistent preferences can live at the user level.

### Why Orphaned Tool Call Repair

When the daemon crashes mid-turn, a session transcript may contain `FunctionCall` events without corresponding `FunctionResponse` events. When this transcript is replayed to the LLM on the next turn, some providers (notably OpenAI) reject the request with HTTP 400 because the message sequence is invalid.

`repairOrphanedToolCalls` scans loaded events and injects synthetic error responses for any unmatched function calls:

```json
{"error": "Tool execution was interrupted. The result is unknown."}
```

This self-heals the session so it can resume without manual intervention.

### Why Credential Redaction on Persist

Every event is passed through the Redactor before being written to the JSONL file. This ensures that even if credential values somehow appear in tool outputs or LLM text (bypassing the placeholder system), they are caught at the persistence boundary. The `RedactSession()` function provides retroactive full-transcript redaction after a new credential is saved.

### Why Context Compaction

LLMs have finite context windows. As conversations grow, the full history eventually exceeds the limit. The `Compactor` addresses this with a `BeforeModelCallback` that:

1. Estimates token count for the current conversation (heuristic: ~4 chars per token).
2. If above 80% of the context window, splits the history into "old" and "recent" (last N messages preserved).
3. Sends old messages to the LLM with a summarization prompt.
4. Replaces old messages with the compact summary.
5. Ensures role alternation in the compacted history (some providers reject consecutive same-role messages).

Fallback: if summarization fails, truncation removes the oldest messages until under threshold.

## Architecture

### Storage Layout

```
~/.config/astonish/sessions/
  <app-name>/
    <user-id>/
      index.json              # Metadata: session ID, title, creation time, last activity
      <session-id>.jsonl      # Event transcript (one JSON object per line)
      <session-id>.jsonl      # ...
```

### Event Lifecycle

```
User sends message
    |
    v
ADK runner calls AppendEvent for each event:
  - User message event
  - LLM response events (text, function calls)
  - Function response events (tool results)
    |
    v
FileStore.AppendEvent():
  1. Redact credential values from event content
  2. Sanitize large blobs (strip base64 images)
  3. Append JSON line to .jsonl file
  4. Update in-memory event list
  5. Update metadata index (last activity timestamp)
```

### Session Loading

```
FileStore.Get()
  |
  v
1. Check in-memory cache
  |
  v
2. If miss: load metadata from index.json
  |
  v
3. Load events from .jsonl file
  |
  v
4. repairOrphanedToolCalls() -- inject synthetic responses for incomplete tool calls
  |
  v
5. sanitizeEventsOnLoad() -- strip base64 image data from historical events
  |
  v
6. Cache in memory, return session
```

### Parent-Child Sessions

Sub-agents (from `delegate_tasks` and fleet) create child sessions linked via `StateKeyParentID`:

- `Delete()` performs cascade deletion: when a parent session is deleted, all child sessions are also removed.
- `List()` returns only top-level sessions (excludes children) for a clean UI.
- Children share the parent's sandbox container via `NodeClientPool.Alias()`.

### Context Compaction Flow

```
BeforeModelCallback fires before each LLM API call
    |
    v
Compactor.ShouldCompact(contents, contextWindow)
    |  No: proceed normally
    v  Yes:
Split contents into old (to summarize) and recent (to preserve)
    |
    v
LLMFunc(summarization prompt + old messages)
    |  Success: replace old with summary
    v  Failure: truncate old messages instead
Ensure role alternation (no consecutive same-role messages)
    |
    v
Return compacted contents to LLM
```

## Oversized transcript lines

A single transcript record can become enormous when a tool response captures a
file that is one huge line (e.g. a generated `.codeintel/index.json`). Without a
guard this makes an entire session unreadable.

- **Reader limit.** `session.MaxTranscriptLineBytes` (10 MB) bounds a single
  JSONL record. `Transcript.ReadEvents` uses a `bufio.Reader` loop, not
  `bufio.Scanner` (which cannot recover after `ErrTooLong`).
- **Skip-and-warn.** An oversized line is skipped, not fatal: every other event
  is still returned, and a synthetic trailing `system` event
  (`transcript-skipped-lines`) reports how many lines and bytes were dropped, so
  the gap is visible rather than silent. `ReadEvents` returns a nil error in
  this case; it errors only on genuine I/O failure.
- **Redaction never drops content.** `RedactTranscript` passes oversized lines
  through unmodified. Only explicit repair removes lines.
- **Chain safety.** `FileStore.LatestDescendant` treats a present-but-unreadable
  transcript like a missing one and falls back to the last good ancestor.
- **Error surfacing.** `localAgentBackend.loadHistory` propagates load failures
  so the `/sessions` overlay renders "Failed to load session: …" instead of an
  empty body.
- **Prevention.** `read_file` caps output by bytes (`readFileSoftCapBytes`) in
  addition to lines, so no new transcript line can approach the reader limit.
- **Recovery.** `astonish sessions repair <id> [--dry-run]` walks the whole
  compaction chain (the poisoned file is usually the tip, not the ID typed),
  reports oversized lines per session, writes a `.bak`, and atomically rewrites
  the transcript with the oversized lines removed. Retained lines are copied
  byte-for-byte, preserving the header and JSONL format.

## Key Files

| File | Purpose |
|---|---|
| `pkg/session/file_store.go` | FileStore: JSONL persistence, CRUD, redaction, orphan repair, cascade delete |
| `pkg/session/compaction.go` | Compactor: token estimation, LLM summarization, truncation fallback |
| `pkg/session/transcript.go` | JSONL transcript reader/writer, oversized-line skip-and-warn, `ScanOversized`, `RepairOversized` |
| `pkg/session/trace.go` | Execution trace persistence (separate from session events) |

## Interactions

- **Agent Engine**: ChatAgent and AstonishAgent use `SessionService` for conversation persistence. The Compactor's `BeforeModelCallback` is wired into the agent's callback chain.
- **Credentials**: `RedactSession()` is called after `save_credential` for retroactive scrubbing. All events are redacted on persist.
- **Sandbox**: Session IDs map to containers via `SessionRegistry`. Session deletion triggers container cleanup.
- **Fleet**: Child sessions are linked to fleet parent sessions. Cascade deletion cleans up the full tree.
- **API/Studio**: Session list, create, delete, and message history endpoints operate on the FileStore.
- **Channels**: Each external channel conversation maps to a persistent session.
