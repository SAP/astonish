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

## When editing
1. Changing `SessionMeta`? Coordinate with Studio's session list (`web/src/components/`) and the resume path in `pkg/launcher`.
2. Changing compaction thresholds or algorithm? Update `docs/architecture/smart-compaction.md` and add scenario coverage.

## References
- `docs/architecture/smart-compaction.md` — compaction algorithm.
- `docs/architecture/sqlite-backend.md` — where sessions live in personal mode.
