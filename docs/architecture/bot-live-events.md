# Bot live transcript events

## Database conversation acceptance

`TestBotDatabaseEmployeeBrowser` uses built Studio in physical Chrome with the
production HTTP/SSE handlers, shared Chat worker and scoped SQL session service
in both personal and team scopes. It selects **U.S. markets and major indexes**
once, observes model-generated acknowledgement while configured research is held,
physically reconnects before completion, and verifies accepted input,
acknowledgement and final exactly once in order live and after reload. Repeated
history responses must preserve their ordered durable SSE IDs and bodies.
Read-only SQL inspection proves format-3 runtime envelopes for sessions, runs,
approvals and outbox payloads; a snapshot excludes runtime file writes while
permitting standing documents and unchanged memory-extraction inputs.
`TestBotDatabaseEmployeePTY` invokes the strict API-owned
`TestBotDatabaseEmployeePTYFixture` with an explicit native CLI test binary.
It checks the same accepted market selection, genuine acknowledgement before
research release, final response, reconnect and Home reload on decoded PTY
screens in personal and team scopes. Owner rows are compared exactly because
the acknowledgement legitimately repeats the selected market words. SQL/file
representation checks also run after the native journey.

This is controlled-provider acceptance, not a SAP latency measurement or proof
of Docker transport operation counts. No public event or renderer is changed.

The shared catalog's sealed native `search_tools` and `describe_tools` calls are
classified as read-only metadata bookkeeping for effect settlement, but remain
ineligible as factual source evidence. This prevents ordinary discovery before
source reads from forcing independent review while preserving the distinction
between finding a tool and establishing a fact. Every shared Bot runtime—interactive
and autonomous—installs the scoped Perplexity classifier before wiring Chat tools.
The scoped Perplexity classifier recognizes both the native and shared-catalog qualified identity
`configured_search/perplexity_web_search`; both bind the same freshly resolved
provider fingerprint and neither broadens unrelated tool authority. The
shared-qualified identity binds its own exact descriptor before consulting the
fallback classifier, while the native identity retains its existing fallback
validation and repairable argument feedback. This lets qualified search record a
read even when the fallback correctly rejects unknown shared names. Read-only
final promotion uses the settled journal/report effect state rather than requiring billed executable
tool calls in the resumed synthesis attempt to equal all observed responses; native
metadata and local bookkeeping responses are intentionally not external tool
charges, while missing or mismatched responses still make the journal uncertain.

## Bounded reconnect warm-up

Reconnect projection warm-up uses the optional `BotTranscriptTailReader` to load
at most eight committed rows ending at the client cursor. Ent-backed personal/team
stores use the indexed `(session_id,id)` tail query; custom stores without this
capability retain the full compatibility warm-up. A reconnect with a validated
session cursor flushes its SSE response headers before file-backed delta hydration;
initial no-cursor replay retains fail-before-header HTTP errors. Authorization is
checked around every bounded durable read batch (before the read and before any
frames from that batch), not once per projected frame, avoiding multiplicative
retained-store reads while preserving revocation boundaries. This reconstructs recent
presentation correlation and the latest eight approval records without hydrating an entire file-backed Home before SSE
headers. Full zero-cursor replay remains ordered and pageable.

## Employee objective physical acceptance

`TestBotEmployeeObjectiveBrowser` and `TestBotEmployeeObjectivePTY` exercise
personal and team Bot scopes against disposable production HTTP handlers, ordered
session storage, and the existing shared Chat worker. The deterministic configured
search adapter first returns an irrelevant fruit result, then a product result
with a coverage gap, then a market result. The agent uses those real tool responses
to refine its next query and commits a complete sourced inline employee message;
no reviewer or file publication is needed for this informational objective.

The browser journey uses built Studio and physical Chrome. The native PTY journey
starts the actual CLI `Execute` test subprocess, answers the saved refinement via
its existing `/bots approvals` menu, reconnects while the first source is held,
and restores Home after completion. Both verify the ordered accepted owner input,
actual acknowledgement and final body once, and check the stored run rather than
manufacturing SSE results. The retained answered refinement card is not a second
owner bubble. The terminal retains the saved `[approved]` decision in its inbox;
that is not another pending approval. The PTY fixture disconnects active SSE request
contexts before reconnecting (closing idle HTTP connections alone does not reliably
interrupt an active Go HTTP stream). Replay is still served by the production
history/events handlers. These tests use no paid model/search, production Bot DB or daemon.

## Accepted owner answers: commit before worker startup

Question acceptance writes `bot-owner-answer:<approval UUID>` through the scoped
ordered transcript writer in the same root-sequence transaction as the decision.
`botApprovalActivity` covers decision-only, approve-and-continue and fresh interactive
continuation paths. Both the run and its visible Home projection are available
before HTTP success, including when paused; no worker lease, sandbox or model setup
is required. Authorization, checkpoint validation and queue/pause semantics remain
unchanged. Invalid source correlation rolls back acceptance rather than inventing input.

`store.BotInteractionAnswerMessage` is the shared correlation/label constructor for
acceptance and resume. The worker's producer-local session view omits only its
already-durable answer while ADK submits the same canonical input, preventing two
model inputs and two transcript bubbles. Persistent history is never filtered or
rewritten. Deterministic IDs keep retries/reconnect/resume idempotent; live/history
use the existing `user_message` projection, not optimistic UI text or a new event.
`TestBotApproveContinueQuestionHTTP` and `TestBotQuestionSQLiteReopenChatRunner`
exercise actual loopback HTTP acceptance without a resumed worker, repeated history
reads, reopen, and exactly one answer after resumed model/tool execution in both
scopes and work protocols. Title Home HTTP tests assert the answer is already in
SSE replay before resume and is not emitted again as a worker delta.

## Shared server projection (consumer migration boundary)

`SessionEventProjector` in `pkg/api/chat_utils.go` now owns the ordered projection
used by both `BotEventsHandler` and `BotHistoryStreamHandler`. Its `Messages`
method delegates ordinary Chat history conversion unchanged, including report-marker
promotion. `ProjectBot` performs question/reference enrichment, existing Home
narrative classification, committed-publication validation and public-field
serialization once. Stored rows are not mutated. Embedded review questions are
removed before the scoped authoritative approval is consulted; unknown state,
controller payload types and ADK private response metadata are not forwarded.
Publication receipt equality and authorization before read/delivery remain required.

Each projection separates durable transport `Cursor` from `EventID`, session/run/
turn provenance and per-turn `Sequence`. Normalized event IDs use session + durable
row ID + ordered part index, not text or argument equality. A hidden valid row
advances the live cursor through a content-free `cursor` control frame;
duplicate publication rows retain the existing suppression behavior. Finite history
and live streams now use the same durable row cursors.

Both endpoints send canonical Chat event names and payloads with `_projection`
metadata. Studio forwards them to the shared `applyChatEvent` boundary, while the
terminal uses the shared Chat SSE mapper and transcript. Neither consumer parses
ADK Bot envelopes or offers a legacy wire fallback. Existing Home telemetry
classification is retained; completing the broader rendering parity matrix remains
an acceptance obligation.

`TestSessionEventProjectorOrderedParity` covers distinct repeated text/calls, hidden
rows and transport/display identity separation. The review/provenance tests cover
untrusted embedded questions, authoritative enrichment without storage mutation,
and Home speech correlation. Home narration keeps its source run-session ID even
when delivered on the Home stream. When those IDs differ, the projector resolves
the run through the request-scoped repository and requires exact Bot, run and source
session identity before rendering; it never treats the copied envelope as authority.
`TestSessionEventProjectorHomeRunSpeechBinding` rejects missing and foreign bindings,
and the title/answer HTTP baseline requires the live acknowledgement before the
blocked source returns. `TestBotEventsHTTPAcceptance` compares actual personal/
team live, reconnect and finite-history HTTP payloads while exercising paging,
revocation and no-execution GET behavior. These are controlled local tests, not
built-browser/native-PTY or deployed-provider evidence.

## Canonical frame contract

`SessionEventProjection.BotFrames(previous)` defines the shared transport contract.
It encodes the
**entire row before writing any frame**, returning `BotPresentationFrame{Type,
Data, Cursor}`. `Type` is the ordinary Chat event name and `Data` is its unchanged
payload plus reserved `_projection` metadata:

```json
{"text":"Hello","_projection":{"event_id":"<session>/<stored-event-id>/0","session_id":"<session>","run_id":"<run>","turn_id":"<turn>","sequence":7,"cursor":"<session>:41"}}
```

- `event_id` is stable across live, restore and reconnect; equal text or equal tool
  arguments are **not** duplicates. It is distinct from a tool-call payload `id`.
- `session_id` identifies the authorized viewing session. `run_id`, `turn_id` and
  `sequence` preserve runtime provenance when present; sequence is not a cursor.
  Historical events without runtime provenance have a zero UUID `run_id` and may
  omit `turn_id`/`sequence`; consumers must not fabricate a run identity.
- SSE `id` and `_projection.cursor` both use `<session>:<durable-row-cursor>`.
  On rows with multiple presentation events, all frames except the last carry
  the **previous fully delivered row cursor**. Only the last advances it. The
  server updates its local cursor after all frames are flushed successfully.
- An interruption between parts replays the entire row; consumers deduplicate
  `event_id` while still processing transport cursor updates for duplicates.
  Never derive `after` from runtime sequence, display identity, or array length.
- A valid hidden row produces one `cursor` control frame with no `event_id`, no
  ADK/state payload and no display content. It advances the cursor even if no
  later public event exists. Unknown private envelopes remain invisible.
- `replay_complete` ends initial live catch-up, not execution. `done` from Chat
  describes a turn, not stream closure; finite history terminates with a separate
  `done` payload containing `persisted:true`. Neither is a publication receipt.
- Rendering passes the ordinary fields directly to the shared Chat reducer after
  stripping `_projection`. Typed Bot run/operation/review/publication events use
  their existing validated public shapes; they cannot authorize execution.

Finite history reads actual durable cursors through `BotTranscriptReader`, rather
than inventing snapshot indices. Per-frame fresh membership/linkage checks, scoped
source-run checks, authoritative review lookup and committed-publication validation
remain mandatory. Completed Bot runtime text carries `sourceEventId` identifying
its persisted ADK response. The source row owns run-transcript presentation; the
redundant runtime copy is cursor-only, including when reconnect begins after the
source. Home narration keeps its runtime copy because that view does not contain
the ADK source. This correlation never compares text values; independent identical
messages remain visible. `TestBotRuntimeProductionCapture` exercises the real
ordered ADK/runtime journal, and `TestBotCanonicalSourceTextOwnership` covers
independent identical text and the Home/run distinction.

`TestBotCanonicalFramesAtomicCursor` proves unchanged Chat payloads, stable
per-event identities, safe interrupted-row replay and nonmutation.
`TestBotCanonicalFramesHiddenAndInvalidRows` proves hidden progress and atomic
encoding failure. `TestSessionEventProjectorRedactsCanonicalPayload` proves
redaction of model/tool strings, preserved ordinary Chat user-text semantics,
thought/private-state exclusion and saved-row nonmutation. Existing approval,
publication and cross-session provenance tests remain in force.

The public runtime allowlist preserves the shared renderer's artifact fields
(`fileName`, `toolName`, `reportTitle`), `report_marker` path/title, and delegation
`event_type`. These are existing Chat presentation fields, not authority or private
checkpoint payloads. `TestBotCanonicalSharedPresentationFields` rejects field loss
and unknown-field leakage; `TestBotCanonicalPresentationHTTPFields` exercises the
same payloads through the authorized finite HTTP stream. Report promotion still
requires the existing last-turn/Markdown/isReport gate. Stored plan snapshots are
projected as `subtask_progress` with `event_type: plan_snapshot` via the same
`planSnapshotData` helper as ordinary Chat, never as the storage key
`astonish_plan_snapshot`. `TestBotCanonicalPlanUsesSharedProgressContract` guards
that shared renderer contract. Validated plan snapshots also pass the Home
narration filter; private/malformed subtask payloads retain their existing
classification until the complete public telemetry cutover is verified.

## Progress-driven controls and wire contract

`BotRunState.progress` is optional `BotProgressProjection` (schema version 1).
It contains `bot_id`, `run_id`, `session_id`, checkpoint `revision`, `phase`,
optional `reason`, optional employee `summary`, optional RFC3339 `evaluated_at`,
`active_duration_nanos`, optional RFC3339 `active_since` and `next_eligible_at`,
and optional nonnegative `coalesced_occurrences` (omitted at zero).
The enclosing state adds RFC3339 `committed_at`; its existing `state_sequence`
orders updates **within the same run only**. Live SSE carries this exact state at
`actions.stateDelta.bot_run_state`; snapshot `run_states` uses the same constructor.
No context reference, dispatch ledger, caller ceiling or private grant is exposed.
Coalesced counts are stored with the logical assignment and advance once per new
accepted occurrence, never on duplicate delivery. Live state, restored snapshot,
and admission receipts use the same committed count and sequence. Before the
first valid checkpoint, the count stays private with the run; it appears with the
first valid progress projection.

Phases are `working`, `evaluating`, `replanning`, `synthesizing`, `checkpointing`,
`waiting_user`, `waiting_budget`, `waiting_authorization`, `suspended`, and
`reconciling`. Reasons are `cadence`, `recovery`, `tool_failure`,
`context_pressure`, `ready_to_synthesize`, `owner_input`, `budget`,
`authorization`, `paused`, and `uncertain_effect`. None denotes completion:
`BotRunState.status` and committed final publication remain authoritative.

For an owner-paused, safely settled interactive or mandated progress assignment, send:

```http
POST /api/studio/bots/{botID}/runs/{runID}/continue-interactive?scope=personal|team
Content-Type: application/json

{"kind":"progress_v1","checkpoint_revision":7,"expected_revision":4,"idempotency_key":"unique-owner-action"}
```

`checkpoint_revision` is the displayed progress `revision`; `expected_revision`
is the current Bot revision. Do not include `response` or fabricate an owner
answer. Success is HTTP 202 with the existing public BotRun response; identical
idempotent retries do not queue duplicate work. Stale checkpoint/revision, unsafe
or cancelled/deleted work conflict (409); invalid authority is forbidden (403).
The operation retains the same run/session, context, ledger and cumulative usage
and does not activate the paused Bot or its recurrence. Scheduled mandate runs
require current owner authentication and verification of the original child and
parent grants, applied scheduling receipt, scope and current operator policy.
Continuation restores the same per-run quota occupancy without changing daily or
lifetime totals, limits, grant IDs or expiration. Expired/revoked authority remains
`waiting_authorization`; exhausted quota remains `waiting_budget`. This endpoint
does not renew scheduled grants. Only the exact continued run may use its valid
grant while the Bot is paused; a later lifecycle revision invalidates that permit.

A still-live caller grant is reused byte-for-byte, without extending expiry.
Missing/expired registry authority requires current authenticated admission and
intersection with the saved caller ceiling; persisted principal/tenant context
alone is not authentication evidence and cannot renew it. Controlled personal/team
HTTP tests cover both branches, forged-context rejection, preserved usage/context,
non-replayed completed reads, pause/cancel/delete and ordered snapshot/SSE parity.
These are isolated SQLite/worker tests, not production deployment or browser proof.

## Skill inspection and scheduling surfaces

Studio Details no longer mounts `BotSkillsPanel` or the Skills & history accordion, in personal/team and all lifecycle/read-only/resource states. There is no replacement Tasks panel, file browser or badge. Goals, Memory & charter, Schedule & triggers, Runs & activity, policy/approval controls and the mounted Home conversation remain unchanged; Close/Escape and refresh do not start or cancel work. The removed inspector issues no skill-list/history fetches. Schedule & triggers independently retains its existing active-skill selection GET (`limit=100`); removing the inspector does not disable scheduling.

Remote `/bots skills` independently renders committed skill revisions and `BotReport.skill` receipts from scoped GET routes, not transcript claims. Backend skill APIs, tools, data and the standalone component remain intact. Unsupported schemas remain unavailable. Studio/terminal parity compares shared ordered transcript, run/result/progress and final invocation fields, not a Studio Skills dataset. Terminal skill-reference/schema/read-only checks remain independent. No new SSE kind or Home transcript rewriting is introduced.

Schedule & triggers references an active skill ID plus invocation intent; it does not copy the method into instructions. Legacy instruction schedules remain explicitly labeled and editable without automatic import. Missing/loading/failed skill reads block new skill selection but do not remove legacy controls. General Settings projects `bot_managed` and offers only Bot navigation for bound jobs; regular scheduler jobs retain their existing controls. Server authorization remains authoritative.

## Snapshot polling and auxiliary activity

The shared Studio snapshot cache treats the root snapshot (including ordered run
states and committed operation heads) as authoritative, independently of activity
pagination. The root high-water sequence is not a promise that an activity row
exists at that sequence. An exhausted activity page ends this bounded catch-up;
it does not report an uncertain work outcome. Transient activity read failures
retain fresh snapshot state and already fetched activity, then resume from the
last activity sequence on a later poll. They never flash the workspace outcome
warning, regress running/succeeded state, or replay a send.

Snapshot read/identity errors still surface and operation synchronization still
requires committed heads in an authoritative root. A 401/403/404 from either read
clears protected cached data and surfaces the access failure. Aborted reads cannot
publish into a switched viewer/Bot workspace. Message send failures remain owned
by the message path, not suppressed by this auxiliary-read policy.

## Agent-led work progress

For `agent_led_v1`, `SaveWork` updates the private document and `BotRun.Work`
projection in one lease/CAS-fenced transaction, advances the Bot root sequence,
and persists the existing `bot_run_state` event. Notifications run only after
commit. Snapshots and transcript replay carry that same projection; failed CAS
or event persistence rolls everything back. Legacy work saves retain their old
non-progress event behavior. Work updates do not change execution status, so a
running state may recur with a higher sequence and work revision before the final
run commit. No progress event is a final answer or an independent verification.

Studio keeps structured work summaries and item/check/remaining-work data in the
Details inspector (`BotRunWorkDetails` / `BotWorkDetails`), not as standalone
per-run rows at the top of Home or run-trace chat. The main transcript remains
ordinary messages and tool activity, with the existing active-work indicator.
Live updates, reconnect and restored snapshots still merge run states for busy
state and receipts; presentation does not delete or alter persisted work. Routine
transport connection/cursor diagnostics are not displayed in chat. Actionable
reconnect/access errors and read-only composer restrictions remain visible.

The current controlled tests cover transactional notification/rollback, snapshot
and history parity, optional notes, scoped evidence reads, and unavailable-check
feedback followed by a useful final answer. Vitest covers uncluttered live/reconnect/
restored chat and Details inspection across close/reopen in light and dark themes;
the complete phase-3 acceptance matrix is a separate verification obligation.

## Invocation-bound read evidence

Agent-led reporting retains the exact transport name (`execute_tool`) and call ID
separately from the concrete target normalized from captured invocation arguments.
Trusted nonexecution receipts match that concrete target and call ID, retaining
transport, conflict, and mixed-effect checks. Malformed/nested bridge calls and
response JSON cannot supply or replace a validated target.

Exact-action approval capture normalizes the captured `execute_tool` invocation
before comparing its inner action digest, while retaining the outer call ID and
transport name for the blocked response. Approved resume uses that same shared
bridge when the concrete tool is deferred; it does not expose a duplicate direct
tool. A mismatched target, changed arguments, mismatched call ID, duplicate
response or failed outcome cannot acquire the nonexecution exemption.
`TestBotApprovalIdentityEvidence` and the scheduled occurrence HTTP acceptance
cover the bridged approval/resume path and exactly-once effects.

Source eligibility is consumed atomically with the call. `bot_work_*` and
`bot_skill_*` bookkeeping remain read-only without becoming source references:
work inspection must not advance its revision through evidence flush, and skill
use requests must not qualify themselves as use evidence. Actual source reads
through the same bridge remain eligible; duplicates cannot add fresh evidence.
CAS, authorization, transcript retention, and learning requirements are unchanged.

## Assessed work publication

Runs with a private pinned work contract persist producer/reviewer/correction content and draft artifact envelopes only in their run transcript. The shared repository routing decision applies to both personal/team stores and both ADK content and runtime envelopes. Canonical owner messages, answers, approval cards and state transitions retain their separate trusted writers; rejected drafts are not hidden only in a client reducer.

Successful work commits require the persisted accepted assessment, matching work revision, requirement/candidate digests, final text and artifact identities. Model assessments remain labelled judgments, not effect attestations. Work-enabled publication does not reuse an earlier conversational reply: it uses the existing immutable report-sequence intent and canonical `bot-report:<run UUID>` boundary. Legacy runs without work state keep their existing reply reuse. Controlled HTTP tests exercise draft exclusion before review and on restore, three independently identified identical final reports, and replay-safe reconciliation; this is not deployed real-model evidence.

## Budget-stop presentation ownership

For recognized native Bot budget failures, the committed `bot-report:<run UUID>` report is the single user-facing explanation. ChatRunner retains the native completion error but emits neither the generic Chat `error` nor the synthetic `[Error: ...]` model row for this case. Ordinary Chat and nonbudget failures are unchanged. Live and restored clients render the same saved report; limits are frozen from enforcement evidence, not recomputed from current defaults. Existing historical raw errors are not rewritten. Optional report failure diagnostics remain stored and are available through the explicit diagnostic formatter, not appended to new employee messages; no new SSE kind or client-side string deduplication is introduced.

## Employee messages versus diagnostics

`bot.RenderReport` publishes the committed summary for Home and terminal, with plain-language uncertainty and pending-authorization notices when needed. Empty summaries use status-aware completion, approval, or noncompletion text; missing results never become an inferred success. New messages do not append the run-report audit wrapper, quality assessments, evidence references, artifact hashes, technical failure details, approval IDs, or scheduling metadata. Meaningful failure explanations and next steps in the summary remain visible; a message never grants approval authority.

`bot.RenderDiagnosticReport` retains the detailed representation, including quality judgments, evidence and code-formatted failure diagnostics. Structured `BotReport` data remains persisted. Historical Studio/terminal fixtures (`bot_budget_stop.json` and `bot_checkpoint_failure.json`) intentionally match this diagnostic format: saved transcript text is replayed unchanged, not re-rendered or migrated to concise prose. New live and restored publication uses the same saved `RenderReport` message.

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

## Canonical file delivery events

A file-bearing Home report is one persisted ADK assistant event, not a second
transient artifact stream. Its `InvocationID` is `bot-report:<run UUID>`; its
stable `ID` is `BotPublicationEventID(run, report_sequence)`. Its single model
text part is the concise summary and
`Actions.StateDelta.bot_file_publication` contains the schema-version-1 public
[delivery envelope](bot-home-assessment-publication.md#canonical-file-publication).
The summary and ordered file metadata belong to the same committed identity.
Transport SSE cursor IDs remain distinct from that publication/event identity.

Finite history and live catch-up both call `marshalCommittedBotTranscriptEvent`.
They validate Bot/Home session/run/report/event binding, assistant/model role,
nonpartial status, and exact summary text, then compare the typed envelope to
`ReadFilePublication` for the exact committed sequence. StateDelta alone is not
proof of commit. A missing receipt, unknown schema, mismatched identity or altered
artifact order/content fails closed; it cannot be replaced with the latest report.
Serialization builds a fresh allowlisted event, stripping unrelated actions,
private storage references, thought signatures and response metadata.

Replay suppresses duplicate file-publication event IDs, not duplicate prose or
filenames. Distinct report sequences/runs remain distinct deliveries even when
text matches. Cards must use committed artifact IDs and exact report routes for
preview/download, never reinterpret their display paths as mutable historical
content. An absent file envelope is not permission to synthesize cards from tool
text; suppressed publications do not emit file cards. Existing report grouping
and ordinary Chat's last-turn/Markdown/backend-`isReport` promotion gate are not
replaced by this Bot-specific metadata.

`TestBotFileFirstPublicationSSE` and `TestBotFilePublicationBindingAndAllowlist`
cover controlled transport identity and allowlisting. They do not certify Studio
browser rendering/reload, terminal PTY behavior, physical backend persistence or
backup/restore. Those require separate acceptance evidence.

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
before each bounded replay read and again before any payload from that batch.
Membership revocation closes an active feed and denies reconnects. Authorization failures
never fall through to another scope. Already delivered bytes cannot be recalled.

Initial validation/storage errors return ordinary HTTP errors before SSE headers.
Later storage failures, revocation, cancellation or network errors close the
connection; consumers reconnect from their last delivered ID. Writes have a
10-second deadline, pages contain at most 100 events, and the hub retains no
payload queue. A slow consumer catches up from persistence on reconnect rather
than losing events to queue overflow.

## Progressive tool read attribution

The attempt-local report journal keeps `execute_tool` as the transport identity
and classifies its normalized concrete target and original arguments with the
trusted runtime classifier. The classifier may add effect constraints, never
substitute identity or arguments. The bridge itself remains unclassified: it is
not a read-only capability. Only a matching, successful tool-role response can
complete this read classification; resolver, malformed-envelope and authorization
failures cannot attest that a read executed. Duplicate call/response identities
remain uncertain, and model result fields cannot mint receipts.

This read exemption creates no changed-state, artifact or publication evidence.
Writes and sends still require independent server-ledger receipts; missing or
conflicting effect receipts retain uncertainty and no-replay behavior. No dispatch
arguments or protected metadata are added to public events, and the ordinary
last-turn/Markdown/`isReport` report marker gate is unchanged. The controlled
`TestBotAgentBridgeActualDispatch` exercises the genuine progressive resolver and
authorization gate; `TestBotEnhancedChatBaseline/title_configured_search` exercises
the isolated daemon HTTP title/answer/search/detail workflow in personal/team scopes.

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
