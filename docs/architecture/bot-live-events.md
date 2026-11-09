# Bot live transcript events

## Budget-stop presentation ownership

For recognized native Bot budget failures, the committed `bot-report:<run UUID>` report is the single user-facing explanation. ChatRunner retains the native completion error but emits neither the generic Chat `error` nor the synthetic `[Error: ...]` model row for this case. Ordinary Chat and nonbudget failures are unchanged. Live and restored clients render the same saved report; limits are frozen from enforcement evidence, not recomputed from current defaults. Existing historical raw errors are not rewritten. Optional report failure diagnostics are rendered once as code; no new SSE kind or client-side string deduplication is introduced.

## Chat-first projection

New `/messages` admissions use `presentation=home_interactive` and atomically persist their canonical user event plus run/outbox. The receipt carries `user_event_id` and `history_cursor`; retries and stream replay must not create duplicate echoes. Interactive output projects to ordered Home history while run traces remain inspectable. Empty presentation keeps legacy projection, and scheduled outcomes remain distinct. Inline proposal/approval references resolve authoritative records; replayed text never grants authority. See [Chat-first contracts](bot-chat-first.md).

`GET /api/studio/bots/{botID}/sessions/{sessionID}/events?scope=personal|team`
adds a read-only, long-lived SSE feed. The existing `/stream` endpoint remains a
finite snapshot ending in `done`; ordinary Chat wire events are unchanged.

## Atomic title start and durable run state

`POST /api/studio/bots?scope=personal|team` opts in only with
`"start_from_title":true`. The original authenticated chat principal and live
`BotInteractiveReady`/turn registry are required. The selected scoped store must
implement `BotTitleStartStore`; unavailable capability returns 503, never an inert
fallback. Creation atomically saves the draft, home, title user event, interactive
run and dispatch. The default charter includes the title; autonomy remains empty
and Bot lifecycle remains draft. Physical sandbox provisioning stays a worker/home
setup responsibility, not an HTTP transaction side effect.

The 201 response retains flattened Bot fields and adds `initial_turn` containing
`run`, `home_session_id`, `user_event_id`, `history_cursor`. Identical key retries
return the same admission without renewing authority; changed input or collision
with a legacy draft key returns 409. Absent/false opt-in retains inert CreateDraft;
source imports and every GET remain non-executing.

State-only ADK rows use `Actions.StateDelta.bot_run_state`, **not**
`bot_runtime_event`, with schema_version=1, bot_id, run_id, session_id,
home_session_id, presentation, status, state_sequence, optional reason and
user_event_id. They have no model content. Their deterministic IDs and sequence
come from the successful transition transaction. Home mirroring is restricted to
home_interactive; scheduled/legacy states do not become Home bubbles. Known
version payloads are validated at both transcript endpoints; unknown versions
remain opaque raw rows. No generic Chat event protocol is added.

Snapshots expose `run_states` and `history_cursor` alongside existing fields.
Legacy runs may be projected read-only without backfilling events. Consumers apply
state monotonically by state_sequence per run, advance transport cursors for every
row, and never count state metadata as messages. Reconnect remains ordinary
transcript replay; GET must not initialize, resume or register a turn. Frontend
and terminal rendering adoption is a separate phase.

## One persisted source

Both initial history and live deltas are ADK transcript envelopes, delivered as
`event: transcript`. Runtime wire data remains in
`Actions.StateDelta.bot_runtime_event` without reinterpretation. With no cursor,
the endpoint replays all persisted rows, emits `event: replay_complete` with
`{"persisted":true}`, then waits for new rows. An empty transcript immediately
emits `replay_complete` and remains open. There is no new runner, execution,
credential lookup or model call when a viewer connects.

Every SSE frame has `id: <session UUID>:<persisted row ID>`. These IDs are stable
across connections and daemon restarts; gaps are valid. Reconnect using
`Last-Event-ID` (preferred) or `?after=<id>`. The header takes precedence. The
endpoint reads strictly after the cursor. A cursor from another session,
negative cursor, or malformed cursor is rejected with 400. Do not reuse finite
`/stream` position IDs here. Clients should retain IDs as opaque strings and
apply each transcript row once. `replay_complete` repeats the last delivered
cursor (or the session-qualified zero cursor when empty), not a new event row.

## Independent Home report display boundaries

Home publication assigns each report the canonical ADK `InvocationID`
`bot-report:<run UUID>` in `entstore/bot_publication.go`. The Studio mapper
preserves it as `invocationId` on `text`; the shared reducer retains it in
`ChatMsg` for both initial replay and subsequent live rows. `turn_complete`
finalizes text, but report visibility does not depend on transient `_streaming`
state or on another owner message arriving.

`toolActivity.ts` treats an agent message carrying that canonical identity as a
hard display boundary, outside the scheduling tool fold and every neighboring
report. Ordinary intermediate agent notes still fold. Headings, markdown content,
and malformed or missing invocation IDs cannot classify a report. This is only
transcript grouping: the separate last-turn/Markdown/backend-`isReport` artifact
harness gate is unchanged, and no report grants execution authority.

The projection reads the same ordered messages without mutating or deduplicating
them. Stable SSE cursors retain transport replay deduplication; identical report
bodies from different run IDs remain separate visible bubbles. Regression tests
in `toolActivity.test.ts` and `StudioChat.test.tsx` cover scheduling call/result +
acknowledgment followed by three reports without owner input, live and full
initial replay through the real Bot mapper, and duplicate last-applied frames.

## Presentation-only internal context

Bot Studio and terminal transcript mappers omit an ADK event only when
`Actions.StateDelta._astonish_turn_context` is boolean `true` (including the
serialized lower-camel spelling). That model-history row emits no user bubble,
content, or completion boundary. It remains persisted and delivered by the
low-level feed, and the client advances its transport cursor normally. User role
alone, quoted context headings, and false/string-valued markers are not grounds
for hiding genuine user input. The same guard applies to legacy terminal mapping.
`TestBotTranscriptTurnContextMarker` and Studio mapper tests cover both wire
spellings and negative controls; the owned Chat-first browser journey verifies
Home and reload against a real persisted marked row and the final replay cursor.

## Fencing and publication

The worker creates deterministic ADK IDs from turn ID and sequence. Append first
locks/fences the Bot root row and validates owner/run/session binding. Inside the
same transaction it checks `(session_id, event_data.ID)` and does not insert or
update transcript timestamps again for a duplicate. A stale or terminal lease
is rejected even for a duplicate. No schema change or parallel event log is
introduced; ordinary transcript rows retain their existing representation.

`NewBotWorker` installs a default `BotEventHub` publication sink, preserving an
optional custom publisher after the default wakeup. The existing durable sink
publishes only after successful persistence. Hub topics include organization,
team, Bot and session IDs and contain **only wakeups**, never payloads. One-slot
coalescing means slow consumers cannot block a worker. Subscribe-before-replay
avoids the history/live handoff race. Every wake causes a bounded ordered read
from the transcript. One-second polling catches missed publication, commits on
other daemon processes and hub restarts, so this is not an in-memory event log.

## Authorization and connection states

Authentication and tenant context must agree. Personal scope is owner-only;
team scope uses live repository membership checks. Bot ID, owner attribution,
retention, purpose, and home/run/session linkage are checked before subscribing,
before each replay read and immediately before each payload write. Membership
revocation closes an active feed and denies reconnects. Authorization failures
never fall through to another scope. Already delivered bytes cannot be recalled.

Initial validation/storage errors return ordinary HTTP errors before SSE headers.
Later storage failures, revocation, cancellation or network errors close the
connection; consumers reconnect from their last delivered ID. Writes have a
10-second deadline, pages contain at most 100 events, and the hub retains no
payload queue. A slow consumer catches up from persistence on reconnect rather
than losing events to queue overflow.

## Verification and integration boundaries

`TestBotTranscriptAtomicFence` and `TestBotTranscriptConcurrentCursor` cover
idempotency, concurrent retry, malformed events, stale/expired fences and persistent
cursor paging in both stores. `TestBotEventsHTTPAcceptance` uses real HTTP,
production tenant middleware and SQLite to verify empty/live/full replay across
page boundaries, query/header cursor reconnect and precedence, both scopes and
active membership revocation with no compute installed.
`TestBotEventsReplayRevocationAndStorageFailure` verifies that a revoked read or
storage failure cannot release replay payloads. Hub tests cover tenant isolation,
bounded coalescing, unsubscribe and default publication even if a custom publisher
fails.

The optional `store.BotTranscriptReader` capability is implemented by Ent session
stores. Other store implementations must implement it before serving `/events`;
otherwise the endpoint returns 503. It is a low-level read capability: callers
must authorize the scoped Bot/session binding. UI and TUI presentation filtering
is separate from this low-level reader, as described above.
PostgreSQL deployment and proxy/load-balancer soak testing are separate from the
SQLite HTTP acceptance harness.


## Canonical employee operation cards and consent

`bot_operation` is a versioned state-delta inside the existing transcript framing,
not a new SSE event. Snapshot `operations` and transcript replay use the same safe
`BotOperationView`; merge by Bot/Home/operation ID and monotonic revision/sequence.
Terminal updates never clear unrelated pending operations. Ordinary questions stay
questions. Exception `fields` contains caption/value text selected from a closed
operation-specific allowlist, not serialized payload JSON or authority.

`POST /api/studio/bots/{botID}/operations/{operationID}/decision?scope=personal|team`
accepts `payload_digest`, `expected_revision`, `expires_at`, `approve` only. The
scoped repository resolves the reserved operation head under current owner
write authorization; the transactional decision checks digest, revision, current
policy and expiry. HTTP never supplies a mandate. Response is a safe operation
view. Approval means pending consent, not applied; denial is cancelled and replay
conflicts. Legacy routine-exception decisions resolve authority server-side too.

API admission/decision contexts and worker runtime/reconciliation compose
`WithBotOperationNotifier`. SQL post-commit callbacks emit coalesced Home wakeups;
rollback emits none. Periodic authorized durable catchup remains mandatory.

New authorized schedule report receipts include operation ID and revision. Only
a verified reserved receipt whose exact canonical applied event already exists
in Home suppresses its redundant text bubble. Detailed run trace stays stored;
legacy receipts without a revision/event retain fallback publication. Arbitrary
report summaries and other operation IDs cannot trigger suppression.
