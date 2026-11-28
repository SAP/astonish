# Employee narration from safe outcome facts

> **Protocol scope:** the bounded writer/reviewer flow below is retained for
> legacy assessment runs. It is not required or invoked for `agent_led_v1` work.

## Synchronous scheduling presentation

For coordinator-owned captured schedules, daemon execution first returns from the
supervisor's terminal source commit and heartbeat cleanup. The direct schedule
coordinator independently reads the actual operation receipt and scheduler job;
those immutable facts, not private pending prose, are passed to a fresh tools-disabled
Employee presentation. Preparation and claim retain the source pin, deadline, expiry
and cumulative quota. Completion precedes Home publication. Separate runtime schedule
receipt publication is suppressed only for trusted coordinator-marked sources.
Store coordination tests are not proof of the complete Employee HTTP workflow;
crash-before-presentation gating and actual private-worker HTTP acceptance must also
be verified before rollout.

## Initial title questions

A newly title-created employee first asks one focused, title-specific `ask_user`
question with three or four materially different interpretations and custom text.
This trusted pending stage replaces work-first guidance only until confirmation;
it is not an onboarding questionnaire or a mandatory completion review. No research,
Task publication, work journal or narrator/reviewer runs before the answer. Invalid
plain-text or tool responses receive at most one metered correction and otherwise
fail clarification without speculative work. The persisted question is restored,
not regenerated while waiting. Once answered, normal employee ownership and
useful-work-first behavior resume under unchanged permission boundaries.

### Confirmed-title acknowledgement in Home

For Home-interactive agent-led work, the owner answer and actual producer interim
narration survive both live delivery and replay. `botConversationSessions` assigns
answers the deterministic `bot-owner-answer:<approval>` ID; the fenced transcript
writer checks the consumed approval, version, run and exact typed response before
mirroring that event. A user role or serialized answer marker alone is insufficient.

`runBotChatAttempt` already holds producer text until a subsequent tool-call boundary
proves it is interim narration. `bot_work_narration.go` permits only those runtime
`text` envelopes for work with that buffering policy. Tool calls are diagnostics,
not communication: they are never mirrored merely to make an acknowledgement
visible. The ordinary raw ADK producer content, thoughts, tool/source response
bodies, artifacts and final
`done` remain private. The existing committed report publisher still owns the final
answer. This does not generate an acknowledgement or require additional model calls.
The persisted ordered rows remain the single source for Studio and terminal Home;
no new SSE kind or client-only rendering path is added.

The missing acknowledgement was a persistence projection gap: `084cd66d` introduced
`mirrorBotRunEvent`'s blanket private-work gate, and mandate-backed resumed titles
initialize that work state. Registry-only/run-transcript tests did not exercise the
Home gate. `TestBotTitleMandateAcknowledgementHomeHTTP` reproduces the production
mandate path with the registry closed, real title/question/answer HTTP handlers,
worker/factory, scoped SQLite and a blocking controlled HTTP source, in both personal
and team scope. It checks live answer → producer acknowledgement order while the
first source remains blocked, and acknowledgement → tool-call chronology in the
private run diagnostics. Repeat HTTP replay, one final publication, idempotent
answer resubmission, and no raw source or tool telemetry leakage remain required.

`projectBotHomeNarrative` is shared by live and history HTTP transports, hence by
Studio and the terminal. Already-written Home tool telemetry is projected to an
identity-only row, retaining its cursor and stored history. Raw run diagnostics
are unchanged. Released runtime speech is projected as complete ADK text, so the
existing clients finalize it immediately without a visible tool boundary; it is
not a fabricated acknowledgement or a new event kind. Genuine `ask_user` controls,
approval/review events, user input and committed final publications remain visible.

The six `bot_title_chat_browser_test.go` journeys exercise built Studio, compact Q/A,
actual acknowledgement before blocked work, no tool cards, final/reload chronology,
personal/team modes and owned disposable Docker Home. Terminal mapper/reducer
coverage is `TestBotHomeNarrativeTerminalBeforeFinal`; the title PTY harness checks
option/custom-answer transport separately. Controlled evidence is not a claim of
real-provider quality/latency or deployed-daemon verification.

## Agent-owned final communication

`ComposeEmployeeMessage` selects the protocol before legacy preparation/narration.
A captured final answer whose text, candidate digest, artifacts, status and public
work projection match validated private agent work is preserved byte-for-byte with
origin `agent`. Answered/complete and partial/blocked outcomes are distinct; neither
origin nor succeeded status guarantees factual correctness or completed external
effects. No extra writer/reviewer model capacity is consumed, including on failure.

Absent/mismatched final answers use deterministic server-selected failure text.
The message names the execution stage, recognized typed reason, available recorded
observation count and typed artifact references, and an actionable next condition.
It never includes raw provider errors, observation prose, artifact paths, private
URLs or partially streamed text. Unknown causes stay unknown. Waiting states ask
for the required answer/authorization without claiming the action completed; stopped
runs do not promise a background retry or instruct the owner to inspect a hidden
draft. Protocol-specific wording leaves historical fallback text unchanged.

`TestBotAgentEmployeeMessageExactFinal`, `TestBotAgentEmployeeFailureNoNarrator`,
`TestBotAgentEmployeeRejectsUnboundCandidate` and
`TestBotAgentEmployeeFinalizationIntegrity` exercise zero narrator calls, exact
result binding, additive receipts and tamper rejection alongside the legacy tests.
These are library tests, not live provider/HTTP acceptance.

## Legacy narration protocol (retained)

`pkg/bot/employee_message.go` owns composition, not execution or assessment authority.

## Exact assessed communication

A validated accepted candidate, or a validated blocked response on a partial run,
is preserved byte-for-byte with its candidate digest. No writer or reviewer is
called for that path. Unassessed success is never narrated by a model.

## Safe facts and fallback

For failed/partial work the writer receives JSON containing:

- `source_digest`: binding to the report and status, excluding the envelope;
- `outcome`: always `incomplete`;
- `run_status`: `failed` or `partial`;
- `failure_kind`: native failure classification;
- `reason`: server-selected typed failure reason;
- `reason_explanation` and `required_action`: server-owned descriptions of that reason;
- `completion_claims_allowed`: always false.

The reason whitelist distinguishes unknown operational failure, unverified result,
setup failure, budget restriction, unknown model cost, input exceeding budget,
usage overrun, rejected model response, multiple tool calls, response size limit,
interruption, authorization denial, unsupported capability, expiration, missing
resource, invalid request, and revision/lease conflict. Specificity comes only from
native typed errors or identities, never from parsing provider error prose.
Generic execution-format failures do not establish that tool requests were never
executed; the specific native checkpoint errors do establish that fact for the
rejected response only.

Raw errors, report summaries, technical details, criterion prose, source bodies,
endpoints and tool arguments are excluded. `Unverified` does not imply unknown
external effects. Unknown safe reasons remain explicitly unknown rather than
inventing the cause. Fallback text explains the recognized reason and user action;
it allocates **no new budget**, calls no model and retries no work.

## Bounded generation and review

The writer contract is `BOT_EMPLOYEE_MESSAGE_V2`:

```json
{"source_digest":"supplied digest","outcome":"incomplete","text":"employee explanation"}
```

The reviewer contract is `BOT_EMPLOYEE_MESSAGE_REVIEW_V1`. It receives the same
facts, `proposed_text` as untrusted data, and its SHA-256 `text_digest`, and returns:

```json
{"source_digest":"supplied digest","text_digest":"supplied text digest","outcome":"incomplete","approved":true}
```

Only the exact reviewed text can be published. Each invocation is tool-free,
limited to 512 output tokens and 4096 accumulated JSON bytes. The explanation is
limited to 1600 bytes; URI schemes, common URL prefixes and unsafe control bytes
are rejected. These checks are not a general semantic or secret detector. The
review explicitly checks incomplete-work communication, safe reason/action fidelity,
and absence of unsupported success/effect/permission claims or private diagnostics.

There are at most **two model calls**: one writer and one reviewer, with no repair
loop. Both use the existing owner LLM and assessment-purpose context. The existing
reservation/settlement wrapper must still reserve each call; live boundary checks
run before and after each call. A denied review discards the draft. Usage from both
calls, including failed calls with usage metadata, is counted. Format errors,
unknown JSON fields, missing/mismatched digests, wrong outcomes, rejected review,
interruption, tool output or capacity failure return the deterministic fallback.
Budget, setup and interrupted failures use fallback directly.

## Downstream schema and validation boundary

No exported Go signature or persisted `BotEmployeeMessage` schema changes here.
`Origin=model` now means generated text that passed this bounded review, not a
choice among fixed messages. Source digests remain integrity bindings, not
attestations. Current store validation does not reproduce the semantic review or
persist its outcome; it must not treat arbitrary caller-supplied model envelopes
as reviewed proof. Model review is fallible, not a guarantee against every false
claim. This implementation does not add success authority.

A downstream persistence hardening change should restrict model envelopes to
failed/partial status and persist server-owned typed `outcome=incomplete` plus
review provenance bound to source/text digests (and validate it at the trusted
commit boundary). Merely adding a model-supplied `approved` field is not an
attestation. That store/API migration is outside this file's ownership and is not
implemented here.

## Verification

`go test ./pkg/bot -count=1` covers actual safe fact requests (not choices), novel
reason-specific prose, false completion rejected by review, a wrong typed outcome,
invalid/partial/tool responses, stale review binding, review refusal, budget denial
before review without retries, two-call usage accounting, typed fallbacks, and
unassessed-success rejection. These are controlled-model library tests, not live
provider or end-to-end runtime proof.
