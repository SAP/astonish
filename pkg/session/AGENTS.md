# pkg/session — AGENTS.md

Session metadata, transcripts, file-backed storage, and smart compaction. Every chat/CLI/sub-agent/fleet session is persisted through this package.

## Scope
- `index.go` — `SessionIndex`, `SessionMeta`, `IndexData`.
- `transcript.go` — `Transcript`, `TranscriptEntry` (turn-level record).
- `file_store.go` — `FileStore`, `fileSession`, `fileState` (personal-mode SQLite path uses this + `store/personal`).
- `checkpoint.go` — `CheckpointStore`: snapshot-on-write file pre-images backing code-mode `/rollback`.
- `compaction.go` — `Compactor`: smart-compaction (see `docs/architecture/smart-compaction.md`).

## Key rules
1. **Never delete a transcript entry.** Compaction produces a summarized *new* version; the original may be retained per policy. Deleting breaks the audit chain and the "resume" story.
   - **Sanctioned exception:** `FileStore.TruncateEvents` rewrites a transcript to a prefix of its events. It exists solely for code-mode `/rollback`, an explicit, user-initiated request to discard later turns. It rewrites (via `Transcript.Rewrite`) rather than silently dropping lines, and updates the index message count. Do not use it for anything but user-driven rollback.
2. **Session IDs are opaque**: they must remain unique across sub-agents and fleet sessions. Do not reuse a session ID for a resumed run — resumption creates a new turn range within the same ID.
3. **Smart compaction is triggered by token thresholds**, not turn counts. Preserve the algorithm's inputs (see the architecture doc) — flaky triggers cause unpredictable UX.
4. **Compaction runs before every model call and is memoized.** ADK rebuilds request contents from the full session each model call, so the `Compactor.BeforeModelCallback` sees the same older history repeatedly within a tool loop. `summarize` caches the summary by an old-portion fingerprint (`summaryFingerprint`) so the summarizer LLM is not re-invoked every step — do not remove this cache; it is the fix for "slow after compaction". `SetOnCompaction` reports before/after token counts for UI visibility (transcript notice + header drop) and must stay domain-agnostic (counts only, never content).
5. **Code-mode durable compaction = child session, never rewrite the parent.** At turn boundaries, over-threshold sessions become a *new* session linked via `StateKeyParentID`, seeded with the summary. Parent transcripts stay intact for `/rollback` across reloads. `LatestDescendant` / `AncestorChain` walk the chain; rollback option A re-activates the ancestor and deletes later children. Do not "fix" the active transcript to drop history for compaction — that breaks reloadable rollback.

## When editing
1. Changing `SessionMeta`? Coordinate with Studio's session list (`web/src/components/`) and the resume path in `pkg/launcher`.
2. Changing compaction thresholds or algorithm? Update `docs/architecture/smart-compaction.md` and add scenario coverage.

## References
- `docs/architecture/smart-compaction.md` — compaction algorithm.
- `docs/architecture/sqlite-backend.md` — where sessions live in personal mode.
