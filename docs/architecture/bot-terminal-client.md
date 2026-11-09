# Remote Bot CLI and terminal Chat

`astonish bots` is remote-only. `pkg/client.Bots` uses the existing authenticated client and `/api/studio/bots`, never a local execution runtime. `--scope personal|team` selects storage scope; authentication and configured team headers remain unchanged. Exact UUIDs are fetched directly; exact names resolve across all list pages. Ambiguous names return candidate UUIDs, scopes and states. `--json` emits machine-readable output without update banners.

## Commands and explicit consent

- `list`, `create draft NAME --charter TEXT`, `show BOT`, `run BOT`
- `pause|resume|stop|delete|restore BOT --revision N --reason TEXT`
- `logs BOT [--session UUID] [--follow] [--after SESSION_UUID:CURSOR]`
- `approvals BOT`
- `stop-run BOT --run RUN_UUID --confirm SCOPE:BOT_UUID:RUN_UUID`
- `approval approve|deny BOT APPROVAL_UUID --digest EXACT_DIGEST [--response JSON]`
- `test-once BOT --version N --confirm SCOPE:BOT_UUID --instructions TEXT`
- `conversation BOT --version N --confirm SCOPE:BOT_UUID --instructions TEXT`

The last two commands use the separate `/test-run` and `/conversation` endpoints. Test once requires Draft; conversation requires Paused and is tool-free. Neither activates the Bot. The server remains authoritative for version, lifecycle, membership, readiness and execution policy. Use `--key` to retain an idempotency key across retries of creation or execution admission. Creating a draft never authorizes recurrence. Stop/delete confirmations are scope-bound; deletion retains the Bot for 30 days.

Lifecycle `resume` uses the existing `activate` endpoint. This explicit action activates Draft Bots and resumes Paused or Stopped Bots after a scoped lookup; repository readiness checks remain authoritative, and owner authorization, expected revision, and lifecycle state are rechecked during the transaction. Active Bots and stale/replayed revisions return conflict without another transition. Neither Test once nor conversation calls this lifecycle action. `resume BOT --run RUN_UUID --approval APPROVAL_UUID` requests checkpoint continuation separately. Approval decisions do not enqueue continuation, activate, or resume a paused Bot. Errors are surfaced with nonzero exit status, not silently retried with new authority. Existing scheduler commands remain the route for owned schedules; no second scheduling client/runtime is introduced.

## Terminal workspace

`astonish chat --bot UUID_OR_UNIQUE_NAME [--bot-scope personal|team]` opens the same platform TUI. Session/model/auto-approve startup flags cannot be combined with `--bot`. The optional `backend.BotBackend` exists only on platform backends; bare local Code mode retains host execution and `/bots` explains the remote requirement without starting a worker.

`/bots` opens a separate picker with loading, empty, error and populated states. Selection immediately watches the Home transcript. Draft, Paused and Active Home input now uses `/messages`; the server chooses the permitted intent and returns a canonical user event/run receipt. Draft/Paused conversation is tool-free except for bounded proposals and never implies activation or test-once consent. Active interactive prompts and replies appear in ordered Home history; run traces remain inspectable through `/bots runs`.

Input is text-only; errors preserve composer text and identical retries retain the admission key. Selected run traces are read-only. Legacy explicit `/runs`, `/conversation` and `/test-run` calls preserve their earlier contracts. `/bots proposals` reviews saved payloads and exact digests; current approval review uses the combined approve-and-continue endpoint, unlike the legacy decision-only CLI. See [Chat-first contracts](bot-chat-first.md) for boundaries.

Native commands:

- `/bots runs`: paginated persisted run inventory; Enter watches a selected run read-only.
- `/bots home`: refresh metadata and return to the home transcript.
- `/bots goals`: durable goals from the authoritative snapshot.
- `/bots schedule` and `/bots status`: reviewed trigger/configuration and lifecycle information.
- `/bots approvals`: exact action, digest, version, run, expiry and status.
- `/bots config`: scoped Studio link under `/#/chat/bots/{scope}/{id}`.
- `/bots stop-run`: pick a run and display its exact scope/Bot/run-bound CLI cancellation command. Enter displays the command only; executing that separately confirmed command requests cancellation. Escape never cancels work.
- `/bots detach`: close the viewer without stopping work.

The header refreshes read-only snapshots serially every two seconds with a five-second request bound. It shows authoritative lifecycle and pending decision count. Failed reads show unavailable rather than stale success. Next-run time is explicitly unavailable when the server provides no scheduler projection; clients do not infer it. Snapshot requests are context-cancellable, and selection generations prevent stale replies from changing another Bot's header.

Composer authority does not wait for that poll after an automatic change. `botPlatformBackend` feeds Home operation envelopes and accepted snapshots through the same `events.Transcript` operation reducer used by the visible terminal (Bot/Home binding and per-operation revision/sequence ordering). Before the next Home submission, any Applied head absent from the accepted snapshot triggers a bounded snapshot reconciliation, with at most one additional read for a pre-commit projection. The complete snapshot root supplies both Bot version and revision; operation counters are never converted into Bot authority. Duplicate/caught-up heads do not fetch again, and foreign or non-Applied heads do not trigger a refresh. Regressing root versions, revisions, history cursors or latest sequences are rejected with their evidence, so a late header response cannot restore stale composer authority. Missing committed evidence and read errors fail closed; refreshed lifecycle, cancellation and selection are checked before admission. Draft/Paused/Active retain their existing server-enforced conversation rules, run traces stay read-only, and HTTP 409 remains an error with retained input—never an automatic POST retry or replay.

For exact decisions, `y`/`n` then Enter confirms submission; Escape closes without deciding. For typed interactions, `y` opens the persisted yes/no or select question, arrows select an answer, Enter reviews it, and a second Enter submits the exact approval/digest/question/answer. Answering No is distinct from denying an approval. A completed request cannot reopen a dismissed or replaced overlay. No restored transcript event supplies an executable approval.

## One durable chronology

Logs/watch use `/api/studio/bots/{bot}/sessions/{session}/events`. Snapshot reads end at `replay_complete`; follow reconnects from the last accepted `transcript` cursor and drops repeated rows. Invalid or cross-session cursors fail closed; replay markers do not advance past unseen rows. Permission errors are surfaced. SSE frames are bounded to 4 MiB.

A single `botTranscriptMapper` lives across reconnects within each viewer subscription. Conversation envelopes (`session`, `text`, `tool_call`, `tool_result`) select the runtime-wire projection **per invocation**, never for the whole Home history. ChatRunner and ADK use different invocation IDs: the initial session-to-done window binds the ADK user-row invocation before its model/tool rows, and payload `invocationId` supplies explicit aliases. Missing-ID legacy model rows are suppressed only inside that active window. Completion closes the window; known invocation aliases still suppress old duplicate projections. Control/reference metadata alone does not select a conversation projection. `Actions.StateDelta.bot_runtime_event` uses the existing `mapSSEToEvents`, retaining artifacts/report metadata, children, status and errors. ADK user input remains visible. Unrelated raw/legacy invocations retain their fallback. Runtime deduplication is by turn/sequence, never identical text. Metadata-only rows do not create text-completion boundaries. Approval envelopes are not replayed into interactive grants.

Canonical raw assistant publications (`InvocationID = bot-report:<lowercase UUID>`) map to the internal typed `KindBotReport`, preserving both invocation and persisted row `EventID` on the event and transcript item. They remain independent completed agent blocks in sticky and linear modes, including three consecutive identical bodies with no owner input. Each publication is a hard boundary for subsequent text, tool folds and routing stamps; later tools cannot overwrite it. The reducer drops repeated publication row IDs without moving the first occurrence (invocation fallback only for missing row IDs), not repeated body text. Report arrival does not synthesize `Done`, finalize another running tool, or adopt its routing badge. Ordinary Chat `Done`/intermediate-text semantics and artifact report markers are unchanged. Bot restoration is a fresh Watch mapper replay through `Transcript.Apply`, not ordinary Chat `LoadHistory`.

Regression coverage: `TestBotScheduledReportsMapperLiveAndReplay`, `TestBotRuntimeInvocationBindingWindow`, `TestBotRuntimeSuppressionIsInvocationScoped`, `TestBotRuntimeProductionCapture`, the `events.TestBotReport*` controls, and `TestBotScheduledReports*` reducer/render tests verify identity, chronological boundaries, duplicate suppression, cached rendering and live/replay parity.

Switching sessions starts a new session-bound subscription with its own cursor and mapper. Old-generation callbacks cannot append to the selected conversation. Detach, quit and session changes close only the viewer and metadata requests; explicit server controls remain necessary to cancel a run or pause a Bot.

The browser extension preserves additive `transcript` and `replay_complete` envelopes at its transport boundary. Its unchanged side panel does not subscribe to Bot feeds or manage Bots; transport compatibility is not a Bot viewer claim.

## Title start, durable working state, and interactive answers

`astonish bots create draft NAME --start-from-title` explicitly admits a title-seeded first conversation. Omitting the flag preserves inert draft creation. It never activates recurrence.

Bot Home and run trace viewers reduce typed `bot_run_state` metadata from snapshots and history. Matching Bot/session bindings and monotonically increasing per-run state sequences govern Queued, Thinking, waiting, interrupted and terminal indicators. Sequences are never compared between runs: live work takes precedence over old terminal runs, with stable run-ID tie-breaking for equally prioritized states. Ordinary text, user messages, done frames and transport errors cannot replace this durable state. State-only envelopes do not select the runtime-wire projection or hide subsequent ADK model text. Switching views clears old state and rejects old-generation callbacks.

Questions support text, select, yes/no and optional custom answers. While editing, `j` and `k` are text; arrows navigate choices. Saved labels are shown while saved option IDs are submitted. Enter reviews and a second Enter confirms; Escape never submits. No is answer data, not denial. Interactive question answers and ordinary tool approvals use approve-and-continue under original authority. An expired or missing grant is surfaced without retrying through `continue-interactive`; only the separate explicit Continue action can renew authority.

`/bots continue` lists runs for explicit interactive continuation. Enter first reviews the selected run; a second Enter requests continuation against the selected Bot revision. Escape cancels. The server decides eligibility and no lifecycle activation or scheduling mutation is implied.

`TestBotTitleChatPTYAcceptance` drives a real CLI/PTY against an authenticated fixture: useful title draft and Thinking survive Home reload; a text question accepts `jk`, reviews before posting, and sends the saved question ID and action digest through original-authority approve-and-continue. This does not exercise a real LLM or deployed persistence.

## Verification and limits

`TestBotCLIAndPTYAcceptance` drives the real Execute dispatcher and Bubble Tea terminal through subprocesses/PTYs against an isolated authenticated HTTP server. Additional CLI intent tests exercise 52 subprocess invocations for both intents/scopes, exact request bodies, confirmations, auth/error handling and candidate selection. `bots_parity_pty_test.go` verifies native panels, Active run admission and run-trace navigation, Paused home replies, one acknowledgement/echo despite replay, runtime report artifacts, automatic header updates, retained input on rejection, and no lifecycle mutations from refresh/detach. `TestBotBareCodePTYAcceptance` proves bare local startup and remote-only Bot gating without login or model configuration.

`TestBotComposerRevisionPTYAcceptance` holds a pre-commit header read open, delivers a live Applied operation, and immediately sends native terminal input without `/bots home` or a refresh. It verifies snapshot version 9 (not prior Bot version 2 or operation revision 73), successful admission, rejection of the late old header snapshot, then a controlled concurrent-edit HTTP 409 with the draft retained and exactly two POSTs. Run it with `go test ./cmd/astonish -run '^TestBotComposerRevisionPTYAcceptance$' -count=1 -v`; launcher fail-first/race coverage is `go test -race ./pkg/launcher -run '^TestBotComposer' -count=1`.

Client/launcher/TUI tests additionally cover cancellation, stale selection and decisions, exact reconnect order, artifact/usage deduplication and unchanged ordinary events. These are real terminal processes with controlled authenticated HTTP/SSE fixtures, not deployed daemon/provider/sandbox acceptance. Phase 7 supplies the combined deployed-client and infrastructure release gate.
