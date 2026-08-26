# Context Compaction Architecture

## Overview

Astonish uses context compaction to manage finite model context windows during long sessions. When estimated token usage exceeds a configurable threshold (default 70%), older conversation history is summarized and replaced with a compact representation.

The compaction system is **mode-aware**: code mode produces structured summaries tracking files, tasks, and decisions; platform/chat mode uses a generic conversational summary.

## Core Components

### Compactor (`pkg/session/compaction.go`)

The `Compactor` struct manages the compaction lifecycle:
- Token estimation (conservative ~3 chars/token heuristic)
- Threshold detection and force-compaction flags
- Summary caching to avoid re-summarization in tool loops
- Plan file inlining (PLAN.md survives compaction)
- Before/after token notification hooks

### CompactionStrategy Interface (`pkg/session/compaction_strategy.go`)

```go
type CompactionStrategy interface {
    BuildSummarizationPrompt(contents []*genai.Content) string
    Name() string
}
```

Two implementations:

| Strategy | Name | Used By | Summary Format |
|----------|------|---------|----------------|
| `GenericStrategy` | "platform" | Platform/Studio chat | CURRENT TASK / PROGRESS / COMPLETED |
| `CodeStrategy` | "code" | Code mode TUI | 7-section structured (OBJECTIVE, FILES MODIFIED, TASKS COMPLETED, TASKS PENDING, KEY DECISIONS, ERRORS & FIXES, CURRENT STATE) |

### SessionNotes (`pkg/session/session_notes.go`)

An incremental session state tracker that maintains structured data throughout a coding session:
- **Files Modified**: path → action + description (deduplicates, upgrades action priority)
- **Tasks Completed/Pending**: work item tracking
- **Decisions**: architectural choices
- **Errors**: with resolution status
- **Current State**: what's actively being worked on

When compaction triggers, SessionNotes are passed to `CodeStrategy.BuildSummarizationPrompt()` as pre-built state. The LLM validates/supplements rather than reconstructing from scratch — dramatically reducing summary quality loss.

### KindCompaction Event (`pkg/tui/events/types.go`)

A first-class TUI event carrying structured compaction data:
```go
type CompactionInfo struct {
    BeforeTokens   int
    AfterTokens    int
    Strategy       string
    MessageCount   int
    SummaryPreview string
}
```

Rendered as a visually distinct card in the TUI (not a fleeting system message).

## Compaction Flow

### Code Mode (TUI)

1. Turn completes → `maybeCompactToChild()` checks if threshold exceeded
2. SessionNotes are cloned and attached to `CodeStrategy`
3. `compactToChild()` calls `CompactContents()` → `summarize()` → strategy
4. History is archived, active session rewritten with compact history
5. `"compaction"` event emitted → mapped through `mapSSEToEvents` → `KindCompaction`
6. TUI renders compaction card with before/after tokens

### Platform Mode (Studio)

1. `BeforeModelCallback` fires during `ChatAgent.Run()`
2. `onCompaction` hook (registered in `ChatRunner`) emits SSE `"compaction"` event
3. Studio frontend receives the event and displays a notice
4. Hook is cleared after the run completes

## Configuration

- **Threshold**: `sessions.compaction.threshold` (default 0.8, code mode uses 0.7)
- **PreserveRecent**: `sessions.compaction.preserve_recent` (default 4 messages)
- **Strategy**: Automatically selected based on `CodeMode` flag in `ChatFactoryConfig`

## Design Decisions

1. **Strategy pattern over mode flag**: Allows future strategies (e.g., flow-mode, fleet-mode) without modifying the Compactor core.
2. **SessionNotes as pre-built summary**: Inspired by Claude Code's Session Memory — amortizes summarization cost across the session rather than doing expensive LLM work at compaction time.
3. **Tool-pair safety**: The split/preserve logic ensures FunctionCall/FunctionResponse pairs are never orphaned across the compaction boundary.
4. **Plan file inlining**: PLAN.md is always appended to summaries because prose summarization cannot faithfully preserve phase-level completion status.
5. **Compaction visibility**: First-class events in both TUI and Studio so users understand when/why context was compressed.
