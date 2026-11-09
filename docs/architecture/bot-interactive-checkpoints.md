# Bot interactive question checkpoints

## Chat-first combined decision

The compact review path uses `POST .../approvals/{approvalID}/approve-and-continue` to approve the saved digest/typed response and request the matching checkpoint continuation together. It cannot authorize unrelated actions or activate a paused Bot. The legacy `/decision` followed by explicit run resume remains compatible; descriptions below of decision-only behavior refer to that legacy path. See [Chat-first contracts](bot-chat-first.md).

Detached Bot runs reuse the existing Chat `ask_user` tool and `chat_question`
card payload. This is a **typed owner decision**, not permission to execute an
external action. It is separate from `exact-action-report-v1` continuations.

## Runtime and owner API

1. A serial `ask_user` call emits its normal card. `BotChatRuntimeAdapter` captures
   it into an approval bound to the Bot, run, version, exact payload digest and
   question ID. The question approval expires after 24 hours.
2. The run commits `waiting_approval` with a `user-decision-report-v1` checkpoint.
   The Chat turn returns and its resources are cleaned up; no process waits for
   a human reply. The saved approval contains the original card for reconnect.
3. The authenticated owner reads `GET /api/studio/bots/{botID}/approvals` and posts
   to `POST /api/studio/bots/{botID}/approvals/{approvalID}/decision`:

   ```json
   {
     "action_digest": "<digest from the saved approval>",
     "status": "approved",
     "response": {"question_id": "<saved question ID>", "answer": "safe"}
   }
   ```

   `yesno` answers are exactly `yes` or `no`. `select` answers must equal a saved
   option ID. A boolean approval without a response cannot answer a question.
   A `no` answer is still an accepted typed response (`status: approved`), not an
   action denial. Denial uses `status: denied` without a response and cannot resume.
   Wrong owners, unknown choices, mismatched questions and changed answers are
   rejected. An identical accepted response is idempotent.
4. Recording a decision does not execute a run. The owner explicitly posts
   `{"approval_id":"<saved approval ID>"}` to
   `POST /api/studio/bots/{botID}/runs/{runID}/resume`. Current authorization,
   active Bot/version, checkpoint, expiration and fencing checks still apply.
5. The worker reconstructs the Chat agent against the saved session and correlates
   the answer with the original `ask_user` call and invocation. It consumes the
   response durably before the resumed model runs, then appends one structured
   `bot_owner_answer` user event. The original task is not resubmitted and the
   original tool call/response remain intact. The model can perform subsequent
   serial work under current tool policy and cumulative budget, and produce a
   final report. An answer is input, never an action grant. Prior report evidence
   is retained; unverified effects keep the outcome `partial` rather than claiming
   independent verification. Consumed responses and terminal runs cannot replay.

## Durable answer identity and Studio restoration

Home-interactive runs retain `bot-message:<run>` as their admission/history
boundary. On question continuation the session adapter persists the owner input
as `bot-owner-answer:<approval>` instead; reusing the admission ID would silently
deduplicate the answer. Marked internal turn-context events retain their own IDs.
The structured answer keeps the exact question, invocation and tool-call IDs for
the model, with additive human-label display metadata. Studio renders that user
input once in the transcript rather than duplicating it in the consumed review.
Ordinary valid answers use approve-and-continue; expired turn authority requires
a distinct explicit Continue interactive click and never an automatic retry with
fresh authority.

`TestBotAmbiguousTitleQuestionBrowserAcceptance` covers custom answer visibility
live and after reload using real HTTP/SSE, SQLite, ChatRunner and a gated fixture
model. The original once-visible assertion is retained.

## Serial provider requests

Before each real parent-turn generation, `botApprovalCaptureModel` copies the
request/config/HTTP-options/extra-body path and disables parallel tool generation
through the shared typed `parallel_tool_calls` extension. OpenAI Chat Completions,
OpenAI Responses, LiteLLM and compatible provider routes serialize an explicit
`false` when tools are present. Ordinary Chat and the auxiliary model captured
before this decorator keep their defaults. Prompt wording, checkpoint formats,
exact-action grants and single-action resume semantics do not change.

This provider constraint supplements rather than replaces complete-response
validation. A provider that ignores it and returns two calls still fails before
any response reaches execution; no batch approval, truncation, unreviewed batching
or automatic unconstrained retry is introduced. Native non-OpenAI protocols retain
the existing response guard, not a newly claimed serial-generation capability.
See [provider request contract](providers.md#serial-generation-for-bot-checkpoints).
`TestBotApprovalCaptureSerialProviderWire` proves the serialized constraint and
single-call passthrough, malicious-batch rejection, pending-checkpoint halt and
ordinary-Chat isolation using local HTTP fixtures only.

## Unsupported response diagnostics

`botApprovalCaptureModel` validates the complete non-streaming response before
forwarding any tool call. Multiple calls (including calls split across response
objects) remain unsupported, as do serialized responses over 4 MiB. Errors retain
`store.ErrBotUnsupported` identity but distinguish the serial-call constraint from
the response-size constraint via native `bot.CheckpointResponseError` evidence.
The committed report has additive `failure.kind: execution_format` metadata and
explains that the model response could not be processed. None of the tool requests
**from that response** ran; earlier work and its evidence remain intact. This is
not an approval request and does not cause an automatic retry. Technical details
contain only the rejected call count or response size/limit, never arguments or
response text. ChatRunner suppresses the duplicate raw error event/transcript row
only for recognized native failures in Bot runs, leaving one committed report for
Studio and terminal clients. String lookalikes and bare unsupported errors keep
ordinary error behavior. A bare
legacy `bot capability unsupported` event cannot recover the discarded response
or establish the attempted tool identity; do not infer that identity from tool
suggestions or reserved token usage. `TestBotApprovalCaptureUnsupportedResponseDiagnostic`
and `TestBotApprovalCaptureSerialResponseUnchanged` guard this boundary.
`TestBotCheckpointFailureAdapter` covers first-response and post-work batch/size
rejections; `TestBotCheckpointFailureCommittedHTTP` covers personal/team SQLite
commit, single Home report publication, retained prior evidence and no replay.

## Wire and storage compatibility

`store.BotApproval` adds optional `interaction` and `response` fields;
`BotApprovalDecision` and the decision HTTP body accept an optional typed
`response`. `interaction` contains `kind: chat_question`, `question_id` and the
original Chat `payload`. Existing exact-action approvals omit these fields and
retain their semantics. Question continuations use the new checkpoint format
`user-decision-report-v1`; they cannot be decoded as execution grants.

These fields live in the existing scoped `bot_approvals.data` JSON. This
interactive slice adds no Ent columns, tables, generated code or SQL migration.
Older runtimes do not understand the new checkpoint format and must not be used
to resume these checkpoints.

## Acceptance evidence and limits

`TestBotQuestionSQLiteReopenChatRunner` runs the real ChatRunner, real `ask_user`,
Supervisor and both personal/team scoped SQLite stores, with a controlled serial
model and tool counters. It closes all database connections and rebuilds stores
before HTTP retrieval/decision, again before resume and after consumption. Real
HTTP requests exercise approval retrieval, foreign-owner rejection, missing typed
response rejection, idempotent owner response, idempotent requeue and terminal
replay rejection. The resumed model receives the original read result and exactly
one correlated typed answer, dispatches one post-answer write, receives its result
and produces the final report. The saved question, answer and final report survive
reopen; the prior read remains at one execution. Both resource factories are
cleaned up. The test has a ten-second context deadline rather than a human wait.

The model, home and authenticated middleware principal are fixtures. This is not
proof of production sandbox provisioning, external authentication or external
side-effect delivery. `TestBotWorkflowQuestionOwner` separately exercises the
launcher owner capability; `TestInteractionCheckpointNeverGrantsExecution` checks
that a question never yields an action grant.

Remaining unsupported behavior is explicit:

- Parallel questions and browser takeover are not implemented.
  `browser_request_human` fails closed without opening a host listener or starting
  a human wait. Serial post-answer model/tool continuation is supported; repeated
  question chains and crash injection during post-answer dispatch were not
  exercised by this acceptance test.
- `present_tutorial_blueprint` and `blueprint_to_tutorial_drill` remain blocked in
  detached Bot mode. There is no durable tutorial creator approval/conversion
  continuation, so successful tutorial approval/conversion is not claimed.
- This slice does not add a Studio or TUI Bot-specific answer/resume control.
  Existing Chat card rendering is unchanged; the proven owner workflow uses the
  Bot HTTP API. Ordinary Chat reply handling is not a Bot decision endpoint.
- Expiration prevents late decisions/resume; it does not itself sweep a saved
  waiting run into a terminal state. Denied/expired checkpoints remain durable
  history, not active human-wait processes.
- PostgreSQL interactive reopen and browser-rendered reconnect were not exercised
  by this acceptance test; team scope was exercised using SQLite.
