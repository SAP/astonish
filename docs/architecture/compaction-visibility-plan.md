# Making Compaction Visible in Code-Mode Terminal

**Status:** Proposal / plan (no code changes yet)
**Audience:** Developers working on `pkg/session/compaction.go`, `pkg/launcher/tui_code.go`, `pkg/tui`, and `pkg/api/chat_runner.go`.

## 1. The problem, stated precisely

When a long code-mode session crosses the compaction threshold, two bad things happen from the **user's** point of view:

1. **"It looks stuck."** Compaction runs *inside* the model call (`BeforeModelCallback`). It makes its own **synchronous summarization LLM call** (`Compactor.summarize` → `c.LLM(ctx, prompt)`) before the real model request goes out. That call can take several seconds on a large history. During that window the terminal just shows the same `Thinking…` / `Running <tool>…` spinner it always shows — nothing tells the user "I'm compressing context right now." It reads as a hang.

2. **"It forgot everything."** The current production compactor is the **monolithic** `summarize()` (despite `docs/architecture/smart-compaction.md` describing a 3-tier design that is *not yet implemented* — see §2). It flattens all old messages, truncates tool responses to 500 chars each, and asks the LLM for one ~500–1000 token summary. Post-compaction the model retains a tiny fraction of prior detail, so it re-asks, re-reads, re-delegates. The user experiences this as amnesia, and there is **no UI signal** explaining that a lossy compression just happened, so it feels like a bug rather than an expected memory-management step.

The root cause of both symptoms is the same: **compaction is completely invisible.** There is no event, no transcript item, no status text, no header change tied to it.

## 2. How compaction works today (verified against source)

### 2.1 Trigger and mechanism

- The compactor is wired as a `BeforeModelCallback` on the ChatAgent and sub-agent managers:
  - `pkg/agent/chat_agent_run.go:593-594` and `pkg/agent/sub_agent.go:716-717` append `c.Compactor.BeforeModelCallback()`.
- `Compactor.BeforeModelCallback()` (`pkg/session/compaction.go:486`) runs on **every** model call:
  1. `ShouldCompact(req.Contents)` — returns true when `EstimateTokens(contents) > ContextWindow * Threshold` (default `Threshold = 0.7`), or when `forceCompact` was set (§2.4).
  2. If over threshold, calls `CompactContents(ctx, req.Contents)` and replaces `req.Contents` in place.
- `EstimateTokens` (`compaction.go:66`) uses a conservative ~3 chars/token heuristic over text + function call/response args.

### 2.2 What CompactContents actually does (monolithic)

`CompactContents` (`compaction.go:186`):
1. Splits `contents` into `[old | recent]` at `len - PreserveRecent` (default `PreserveRecent = 4`, overridable via config).
2. `adjustSplitForToolPairs` walks the boundary back up to 8 positions so a preserved `FunctionResponse` is never orphaned from its `FunctionCall`.
3. Extracts the last real user text instruction from the old portion as a **task anchor** (`findLastUserTextInstruction`).
4. Calls `summarize(ctx, oldContents)` → **one LLM call** with a prompt asking for `CURRENT TASK / PROGRESS / KEY FACTS / COMPLETED WORK`. Tool responses are truncated to 500 chars each in the prompt; repeated identical tool calls are collapsed to `(×N repeated calls)`; the whole prompt is capped at 30 000 chars.
5. On LLM error → `truncationSummary` (structural, no LLM).
6. Reassembles: `summaryContent` (with role chosen for alternation) + optional task anchor + recent messages.
7. Bumps `compactionCount`, updates `lastEstimatedTokens`.

> **Important:** `docs/architecture/smart-compaction.md` describes a 3-tier (active window / current task / historical) design with per-tool-response caching in session state. **That design is not implemented in `compaction.go`.** The live code is the monolithic summarizer. Any visibility work should be honest about which one is running, and ideally is a good moment to also revisit implementing the tiered design (out of scope for this doc, but noted).

### 2.3 Observability that exists today

- `Compactor.TokenUsage()` → `(lastEstimatedTokens, ContextWindow)`.
- `Compactor.CompactionCount()` → number of compactions performed.
- `DebugMode` emits `slog.Debug` lines ("compactor threshold exceeded, compacting", "compacted messages"). These only appear in debug logs, never in the TUI.
- `pkg/api/chat_handlers.go:1470-1509` surfaces token usage / threshold / compaction count via an API/status path (used by Studio), **but code-mode TUI does not consume it live during a turn.**

### 2.4 Overflow retry (relevant to "stuck")

`pkg/agent/chat_agent_run.go:694-701`: if the provider returns a 400 context-overflow, and a compactor exists and we haven't retried yet, it calls `c.Compactor.ForceNextCompaction()` and retries. This is a **second** silent compaction on the same turn — an even longer invisible pause.

## 3. How the code-mode terminal renders progress today

Event pipeline (all mode-agnostic; see `pkg/tui/events/types.go`):

- Local backend (`pkg/launcher/tui_code.go`) emits typed events via `emit(kind, payload)`: `text`, `thinking`, `tool_call`, `tool_result`, `usage`, `approval`, `auto_approved`, `error`, `session`, `done`.
- `Transcript.Apply` (`pkg/tui/events/transcript.go:114`) reduces events into transcript state:
  - `KindThinking` / `KindStatus` set `t.Status` (the spinner line).
  - `KindSystem` appends a persistent `ItemSystem` block (rendered muted; `app.go:2220-2221`).
  - `KindUsage` updates `t.LastUsage` / `t.ContextTokens`, which drives the header (`headerUsageText`, `app.go:2992`, showing `Context X/Y (Z%)`).
- **There is no compaction event kind and no compaction rendering anywhere** (verified: no `compact*` reference in `pkg/tui`).

So the spinner just says `Thinking…` while the compaction LLM call blocks, and the header only updates *after* the turn when usage arrives.

## 4. Design goals

1. **Liveness:** the moment compaction starts, the terminal must say so ("Compacting context…") so it never reads as a hang.
2. **Honesty about memory:** after compaction, leave a **persistent, unobtrusive transcript marker** (e.g. a muted system line: `Context compacted — summarized N earlier messages (~before → ~after tokens). Older detail is condensed.`) so "it forgot" is explained as an intentional, visible step, not a bug.
3. **Header truth:** the context figure should visibly drop right after compaction, reinforcing that memory was reclaimed on purpose.
4. **Mode-agnostic:** the same event should flow through the local backend and the Studio SSE path so both the terminal and Studio Chat benefit. The event model is already shared, so we add one `Kind`.
5. **No new invariants broken:** keep the `astonish-report` gate, tenant boundaries, and the "never delete a transcript entry" rule untouched. This is additive UI signalling only.

## 5. Proposed approach

### 5.1 Emit a compaction lifecycle from the compactor

The compactor currently has no way to notify anything. Add an optional, best-effort notification hook (no behavioral change when unset), analogous to how `LLM LLMFunc` is injected:

```go
// pkg/session/compaction.go
type CompactionEvent struct {
    Phase          string // "start" | "done"
    BeforeMessages int
    AfterMessages  int
    BeforeTokens   int
    AfterTokens    int
    Forced         bool   // came from ForceNextCompaction (overflow retry)
}

type CompactionNotifyFunc func(CompactionEvent)

// New field on Compactor:
//   OnCompaction CompactionNotifyFunc // nil = silent (default)
```

Fire `Phase: "start"` at the top of `CompactContents` (before the summarize LLM call) and `Phase: "done"` just before returning, with before/after counts. `Forced` is read from the consumed `forceCompact` flag (thread it through `ShouldCompact`/`CompactContents`). The callback must be non-blocking and panic-safe (wrap in a recover) so a bad consumer never breaks a turn.

**Why a callback and not a channel:** the compactor runs inside ADK's `BeforeModelCallback` on the same goroutine as the turn; a synchronous callback that the backend turns into an `emit(...)` is the simplest fit and matches the existing `emit` pattern.

### 5.2 Add a `compaction` event kind

In `pkg/tui/events/types.go`:

```go
KindCompaction Kind = "compaction"
```

Carry phase + counts in `Meta` (the struct already has a `Meta map[string]any` escape hatch, so no field bloat), or add typed fields if preferred. Provide `events.NewCompaction(phase string, before, after int)`.

### 5.3 Reduce it in the transcript

In `Transcript.Apply`:

- On `phase == "start"`: set `t.Status = "Compacting context…"` (spinner line updates immediately — kills the "stuck" feeling). Do **not** append an item yet.
- On `phase == "done"`: append a muted `ItemSystem` (or a dedicated `ItemCompaction` if we want distinct styling) with text like:
  `↯ Context compacted — condensed N earlier messages (≈before → ≈after tokens). Older detail is summarized; ask me to re-open anything specific.`
  Then reset `t.Status` back to `Thinking…` so the turn continues cleanly.

Using a distinct `ItemCompaction` kind (rendered with a subtle glyph + muted style) is cleaner than overloading `ItemSystem`, and lets `turnStart()` treat it as a turn boundary marker if desired. Either is acceptable; `ItemSystem` is the minimal change.

### 5.4 Wire the local backend

In `pkg/launcher/tui_code.go`, when constructing/attaching the compactor for a turn, set `OnCompaction` to a closure that calls the same `emit` used elsewhere:

```go
compactor.OnCompaction = func(ev persistentsession.CompactionEvent) {
    emit("compaction", map[string]any{
        "phase":  ev.Phase,
        "before": ev.BeforeMessages,
        "after":  ev.AfterMessages,
        "beforeTokens": ev.BeforeTokens,
        "afterTokens":  ev.AfterTokens,
        "forced": ev.Forced,
    })
}
```

Note the compactor is a **shared** singleton (also used by sub-agents and Studio). The `OnCompaction` hook must be set per active turn/emitter, or be a fan-out that routes to the current turn's emitter. Simplest safe option: guard the hook with the same mechanism used for the per-turn `emit` (set at turn start, cleared at `done`). If the compactor is genuinely shared across concurrent Studio + code sessions, prefer a small registry keyed by session ID rather than a single mutable field. **This concurrency question is the main design decision to lock down before implementing.**

### 5.5 Wire the Studio SSE path (parity)

`pkg/api/chat_runner.go` should map the same `compaction` event to an SSE event so Studio Chat can render a matching inline notice. This keeps the two frontends consistent and satisfies the mode-agnostic goal. The Studio side already reads compaction stats via `chat_handlers.go` for status, so this is additive.

### 5.6 Header reinforcement (already mostly free)

After `phase: "done"`, emit the usual estimated-usage refresh (`emitEstimatedContext`) so the header's `Context X/Y (Z%)` drops immediately, visually confirming memory was reclaimed. `estimateContextTokens` already mirrors `EstimateTokens`, so post-compaction it will reflect the smaller history.

## 6. Optional but recommended: reduce the *cause*, not just the symptom

Visibility fixes the UX perception. To actually reduce how often the model "forgets," two follow-ups (separate PRs):

1. **Implement the tiered compaction** described in `smart-compaction.md` (task segmentation + per-tool-response compression + session-state cache). This preserves ~85% of findings vs. the ~2% of the monolithic summarizer, so the "it forgot" complaint largely disappears. The visibility work in this doc is orthogonal and lands first.
2. **Surface a proactive "approaching context limit" hint** in the header when `estimated > 0.6 * window` (before the 0.7 trigger), so users can `/new` or wrap up a task deliberately.

## 7. Files to touch (summary)

| File | Change |
|------|--------|
| `pkg/session/compaction.go` | Add `CompactionEvent`, `CompactionNotifyFunc`, `OnCompaction` field; fire start/done (panic-safe); thread `Forced`. |
| `pkg/session/compaction_test.go` | Assert `OnCompaction` fires start+done with correct before/after counts; assert silent when nil; assert forced flag on overflow retry path. |
| `pkg/tui/events/types.go` | Add `KindCompaction` + `NewCompaction` constructor. |
| `pkg/tui/events/transcript.go` | Reduce `KindCompaction`: start→status, done→persistent muted item + status reset. |
| `pkg/tui/events/transcript_test.go` | Assert status text on start and a persistent item on done. |
| `pkg/launcher/tui_code.go` | Set `OnCompaction` to `emit("compaction", …)` for the active turn; refresh estimated context after done. |
| `pkg/api/chat_runner.go` | Map `compaction` to an SSE event for Studio parity. |
| `pkg/tui/app.go` | (Optional) dedicated `ItemCompaction` styling; header "approaching limit" hint. |
| `docs/architecture/smart-compaction.md` | Add a "Visibility / UX signalling" section; correct the note that the tiered design is aspirational vs. the shipped monolithic summarizer. |

## 8. Concurrency / correctness checklist (decide before coding)

- Is the `Compactor` instance shared across concurrent sessions (code + Studio + sub-agents)? If yes, `OnCompaction` must be per-session (registry keyed by session ID), not a single mutable field, to avoid cross-session event leakage.
- The callback runs on the turn goroutine inside `BeforeModelCallback`; keep it non-blocking (just `emit`, which already sends on a buffered channel) and recover from panics.
- Sub-agent compactions: decide whether to surface them (probably show as a subagent-scoped notice, or suppress to avoid noise). Default: suppress sub-agent compaction notices in the main transcript for now.
- Do not alter `ShouldCompact` thresholds or `CompactContents` behavior — this is signalling only.

## 9. Acceptance criteria

1. Crossing the threshold shows `Compacting context…` on the spinner within one render frame (no perceived hang).
2. After compaction, a persistent, muted transcript line explains what happened, including a token before→after figure.
3. The header context figure visibly drops right after compaction.
4. With `OnCompaction` unset, behavior is byte-for-byte identical to today (regression guard test).
5. Studio Chat shows an equivalent inline notice (parity), without touching the `astonish-report` gate or tenant boundaries.
