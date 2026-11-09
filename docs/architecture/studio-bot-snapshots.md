# Studio Bot snapshots and viewer read state

The current [Chat-first workspace](bot-chat-first.md) renders compact identity/status with the existing Chat transcript and composer. Snapshot-backed advanced controls, runs, schedules, goals, memory and reviews live under Details, not permanent top-level tabs. Closing Details preserves mounted unfinished edits; snapshot errors disable authoritative review decisions and expose retry. This UI change does not alter server-side scope, read-state or lifecycle authority.

## Authoritative projection

`GET /api/studio/bots/{botID}/snapshot?scope=personal|team` returns:

- `bot`: the existing root, including revision/version and lifecycle state;
- `read_cursor`: `{bot_id, viewer_id, sequence}` for the authenticated viewer only;
- `latest_sequence` and `unread_count`: durable control-plane high-water mark minus the viewer cursor;
- `pending_approvals`: all persisted pending or approved/unconsumed decisions, ordered by expiry then ID;
- optional `latest_outcome`: the finished run with the latest finish time (ID breaks ties).

`GET .../{botID}/read` exposes only that viewer's saved cursor, defaulting to zero.
The existing `POST .../{botID}/read` accepts only `{sequence}`; it remains monotonic,
rejects future positions, and does not append activity. A viewer cannot name or
modify another viewer's cursor. Ordinary root/activity/run/approval list contracts
are unchanged.

The optional `store.BotSnapshotReader` capability is implemented by both concrete
Ent Bot stores. It reads existing root metadata and pages through existing approval
and run repositories; it creates no schema or transcript store. All reads use the
same live membership and personal-owner checks as other Bot operations. The
projection is read in a transaction and rechecks the root revision/sequence before
returning, rejecting concurrent torn reads rather than publishing mixed state.

Unread means **control-plane activity updates**, not unseen transcript tokens.
Expired decisions retain their persisted status until existing reconciliation
changes it; this read-only endpoint does not mutate approval authority.

## Shared Studio state

`api/botSnapshot.ts` pins every request to the captured team, uses `teamFetch`,
encodes IDs and requests activity with `after` and `limit=50`.
`useBotSnapshot` shares one disposable projection and one 15-second poll among
sidebar/workspace consumers keyed by viewer, team, scope and Bot ID. No transcript
SSE is needed for background outcomes to appear. The sidebar also refreshes the
ordinary Bot root list independently to discover newly created Bots.

Applied operation heads from Chat's existing ordered snapshot/history/live merge
also invalidate this same projection immediately. `syncOperations` compares the
operation ID, operation revision and state sequence against the snapshot's heads;
these coordinates are evidence of a commit, **not** a Bot revision/version. If a
read was already in flight before the commit, it drains that read and performs one
fresh read when necessary. The root and operation evidence remain paired; older
root revision, version or activity high-water marks cannot replace newer state.

The composer awaits this reconciliation at submission and takes its message
version from the returned snapshot Bot, never from a second local revision
counter. Missing commit evidence fails closed and preserves input. A concurrent
owner mutation after this read is still rejected by the server's version CAS:
there is no automatic replay, conflict retry, or clearing of the rejected draft.
Duplicate/older operation events already covered by the snapshot do not trigger
additional reads. Foreign Bot/Home heads and non-applied heads cannot supply
admission authority. The same shared projection updates the workspace and sidebar;
manual Details → Refresh Bot is not required between applied work and a new turn.

Workspace activity catches up to the snapshot's fixed high-water mark through all
pages, deduplicates by sequence, and merges monotonically. New arrivals after that
mark are picked up on the next poll. Snapshot state remains visible if activity
paging fails; failures expose retry instead of silently treating partial data as
complete. Mark displayed activity read posts the actual displayed high-water mark,
then fetches persisted read state and notifies the shared consumers.

Identity changes expose an empty/loading state immediately; last-subscriber cleanup
aborts outstanding reads, removes timers and discards cached data. Requests and
responses remain pinned to the captured team and viewer. Authorization/not-found
failures clear previously displayed snapshot data. Transient failures preserve the
same-Bot loaded workspace and its unsaved panel edits while surfacing the error.

No Chat transcript, report gate, terminal TUI, or sandbox behavior is changed.

## Verification

- `TestBotSnapshotPagesAndViewerReadState`: both SQLite scopes, 121+ activity rows,
  strict cursor paging, latest outcome, monotonic reads, independent team viewers,
  personal isolation and revoked membership.
- `TestBotStorageHTTPAcceptance`: production HTTP routes/tenant middleware, anonymous
  and cross-scope denial, snapshot/cursor reads and independent owner/member cursors.
- `useBotSnapshot.test.tsx`: shared projection, 121-event catch-up, dedup, persisted
  cursor reload, stale Bot/viewer switch cancellation and independent timed refresh.
- `BotSnapshotSurfaces.test.tsx`: real sidebar/workspace sharing the hook, identical
  counts/latest outcome and mark-read clearing both after 121 displayed rows.
- `api/__tests__/botSnapshot.test.ts`: encoded URLs, explicit team, pagination,
  cursor-only write body and authorization errors.
- `StudioChat.test.tsx`: applied cadence → latest snapshot version submission,
  followed by a concurrent conflict retaining the draft without replay.
- `TestBotComposerRevisionBrowserAcceptance`: built SPA in native Chromium,
  scoped production HTTP/SQLite and worker-applied schedule; the next composer
  POST uses version 2 and is accepted without reload or opening Details. A real
  concurrent owner mutation returns HTTP 409 with unchanged input and no extra
  admitted run. Run with `ASTONISH_MEMORY_BROWSER_ACCEPTANCE=1 go test ./pkg/api
  -run '^TestBotComposerRevisionBrowserAcceptance$' -count=1` after `cd web && npm run build`.

Browser/daemon deployment, PostgreSQL concurrency/load, and very large Bot-history
performance are not proven by these dependency-free SQLite/React tests. Snapshot
approval/outcome selection scans all existing pages; a future indexed query can
optimize this without changing the API.
