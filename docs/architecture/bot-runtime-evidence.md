# Bot runtime report evidence

## Agent-led working evidence

New `agent_led_v1` runs have optional read/update/evidence/check capabilities, not
legacy accepted-review prerequisites. A useful final answer is captured even when
optional items are unfinished: in-progress items become pending (never completed),
and the result is partial. No finish tool is required and no retry is scheduled.
Legacy assessment evidence below remains specific to the legacy protocol.

Source bodies stay in the scoped, append-only run transcript rather than being
copied into every candidate. Before the next producer model call, the adapter
flushes source references and typed access facts against that persisted transcript;
call ID, event, body digest and run identity must match. HTTP 200 establishes only
transport return; 429/failures are retained negative observations, not permanent
content vetoes. Source-retrieval errors must be distinguished from denied authority
and uncertain effects by completion handling. Metadata exhaustion returns an
explicit error instead of dropping evidence; evidence reads provide byte offsets,
length and truncation. A failed final provider call does not erase earlier facts.

Optional checks reserve a durable call receipt before spending tokens, release the
service mutex during the model call, then reload the current revision before saving
feedback. Interrupted reservations cannot replay. Cancellation/fencing prevents
late feedback mutation. Checks share the factory's already-metered model; cumulative
usage snapshots are recorded once for reporting, never charged again. Empty,
oversized, interrupted or tool-calling responses return check-unavailable feedback,
not a universal review failure. Existing negative feedback is append-only.

Factory-bound `BotChatResources.PreviousWork(context.Context)` supplies the previous
committed projection for this Bot. `ValidateWorkContinuation(context.Context,
store.BotWorkContinuationKind, uuid.UUID)` validates live same-Bot schedule/approval
references; absent wiring rejects these continuations. Neither callback grants
execution authority, accepts model-selected scope, nor creates a schedule.

Unit evidence includes `TestBotAgentAccessFlushedBeforeModelAndRetainedOnFailure`,
`TestBotAgentCheckConcurrentUpdateUsageAndCancellation`,
`TestBotAgentCheckMalformedAndOversizedFeedbackRecover`,
`TestBotAgentPreviousAndContinuationAreScoped`, and
`TestBotAgentUnfinishedItemsDoNotGateAnswer`. These use controlled models/stores;
they are not live-provider or restart/database acceptance.

## Budget-stop evidence

`bot.BudgetLimitError` snapshots token limit, accounted usage (including holds), remaining allowance and optional request estimates at enforcement under the meter lock. `BudgetFailureMessage` recognizes native typed errors, existing mandate token quota dimensions, and legacy budget sentinels—not error text. First-call rejection is not consumption. Unknown/overflow/runtime/tool cases receive a nonnumeric fallback; no current configuration defaults are consulted.

`BotChatRuntimeAdapter` freezes that explanation in `BotReport.Summary` and adds optional `Failure` (`kind=budget`, safe technical details). Native causes remain on `RunError`, with failed status and prior observed effects retained. Diagnostics contain selected enforcement facts, never arbitrary wrapped provider messages or request bodies; `RenderReport` indents them as code. No scheduler state or retry policy changes. Deadline cancellation and pre-admission failures retain their existing lifecycle; this is not a universal pre-claim/runtime reporting redesign.

`BotChatRuntimeAdapter` installs a per-attempt `bot.ToolOutcomes` ledger in the execution context. A trusted server tool calls `bot.RecordToolOutcome(ctx, ctx.FunctionCallID(), outcome)` **after** independently observing its effects. ChatRunner consumes that entry only when the corresponding current tool response arrives. ADK JSON-round-trips function-tool results, so attestations travel out-of-band rather than being reconstructed from result JSON. The launcher explicitly transfers only this ledger across its owner-context isolation boundary using `bot.WithToolOutcomesFromContext`; ambient viewer values remain discarded.

Model prose, tool arguments, remote JSON and serialized transcript replays cannot create ledger entries. They never populate `Changed` or `Unchanged`. Unknown outcomes remain `Unverified`, making a completed tool-bearing run partial rather than asserting success. Recognized verified outcomes can succeed; later execution failures retain already-observed evidence without changing the failure status.

Observations require an observation kind, nonempty source and summary, observation timestamp and actor. Exact repeated observations are deduplicated. Tools may attest both changed and unchanged observations, explicit unknowns, report artifacts and a server-observed next-run timestamp. A trusted implementation must not copy these fields from model output. A positive dispatch result alone is not evidence of an external effect.

Report artifacts include a nonzero artifact ID, captured bytes, SHA-256, a canonical relative path and durable Bot home ID. Runtime checks the digest, path containment and current Bot home before exposing the artifact or using its body. `scheduler.ResolveAdaptiveReportBody` reuses adaptive `astonish-report` marker extraction with these captured bodies, without any host-file read callback. A report body remains presentation content: parsing its prose never grants evidence or notification authority.

The resulting `store.BotReport` flows through the existing Supervisor commit and replay-safe Publisher. `NotifyOnChange` thresholds count the validated changed observations, not words in the final answer. No transport or delivery-authorization gates are weakened.

## Built-in file receipts

`write_file` reads back and compares actual file bytes in the same node invocation
before returning a SHA-256 receipt. It reports `content_changed` only when the
pre-write content (or absence) was observed: identical writes are `Unchanged`,
new/different content is `Changed`, and an unknown baseline remains unverified.
`load_file` hashes its existing metadata scan
and checks size/mtime consistency; it still returns no file body. Only the pinned
built-in sandbox adapter validates these narrow receipts and records server-bound
actor/session/call/time observations in the ledger. It never decodes ToolOutcome
JSON, accepts MCP receipts, or upgrades old nodes without receipts. No additional
tool dispatch, budget exemption or exact-action continuation expansion is added.
Load inspection does not attest browser download or create a durable report
artifact. Proposal capture reads back the saved proposal and attests only saved
for review, not applied.

`TestBotBuiltinFileOutcomesThroughRunner` reproduces the formerly partial result
using actual built-in dispatch, JSON node transport, catalogue/owner binding, ADK
and ChatRunner, with one node call and one tool usage per operation. Its transport
and model are controlled; it is not live container/provider acceptance.

## Skill assessment format recovery

`botWorkJSON` permits at most one JSON-only format correction for skill orientation
and skill learning, as for work assessment. Both calls use the same selected skill
schema, original input, tool-free configuration and shared run budget. Prior model
text remains explicitly untrusted. Invalid JSON and strict schema decoding errors
(including unknown fields and wrong types) may use the same second call; no fields
are silently dropped. Correction instructions use static safe text, never raw
parser errors, and each attempt decodes into fresh storage. Syntax, schema and
citation correction share one two-call ceiling, not independent retry budgets.
Wrong digests, semantic authority/provenance rejection, tool calls,
partial/interrupted streams and provider errors do not enable format retries.
Cancellation and authority/budget boundaries are checked before correction; usage
from both calls is retained. There is no fenced-JSON extraction or task replay.

`TestBotSkillJSONFormatRecovery` reproduces the previous one-attempt regression
for both prompts and covers the bounded failures. `TestBotSkillJSONFormatRecoverySharedBudget`
uses the real BudgetLLM decorator to verify both-call charges and correction
admission against the original allowance.

`TestBotSkillRuntimeHTTPAcceptance/{personal,team}/format-recovery` starts with
malformed orientation, corrects with the identical schema, saves the skill before
production, fetches an actual temporary HTTP source exactly once, then also
recovers malformed learning. It checks the active skill's observed source revision,
a succeeded run, and exactly one delivered answer in Home SSE on repeated readback
and after another worker poll. Repeated malformed orientation remains bounded to
two calls with zero tools and no skill. The strict controlled gate is
`python3 tests/e2eboot/employee_http_acceptance.py --skills-stage runtime`.
This is controlled worker/HTTP/SQLite evidence, not live provider or daemon proof;
no existing user Bot is mutated.

## Content citation recovery evidence

`TestBotContentQualityHTTPAcceptance/{personal,team}/citation-repair` exercises
owner HTTP admission, isolated SQLite, worker and ChatRunner with one actual HTTP
feed fetch: bad source quote → same-input/schema reviewer correction → unsupported
claims/off-topic findings → existing producer repair → valid review → exactly one
Home publication. `citation-only` needs no producer retry; `citation-exhausted`
stops after two reviewer calls with no rejected content/artifacts and no tool replay.
`TestBotContentCitationRecovery` covers unknown refs, missing coverage, retained
negative findings, wrong-digest non-recovery, cancellation/budget before correction,
shared actual usage accounting and the shared format/citation correction ceiling.
Errors contain only typed reasons, indices and hashed identifiers. No live provider
judgment, deployed daemon or existing Bot was exercised or changed.

## Acceptance coverage and limits

`TestBotRuntimeReport` runs the real ChatRunner with controlled tools and tests verified, unverified, JSON-spoofed, invalid and foreign-home results. `TestBotRuntimeHTTPAcceptance` uses real HTTP handlers, SQLite stores, scheduler admission, daemon worker, launcher and ChatRunner. It exercises distinct scheduled occurrences, duplicate admission, approval reconstruction, distinct controlled filesystem execution directories, captured artifact publication and change-threshold notification replay protection.

The home backend, isolation provider, model and notification endpoint in this journey are controlled fixtures. It is not proof of production container isolation or an external provider's delivery. Native tools that do not explicitly record independent outcome metadata remain unverified; no generic tool success is upgraded to verified evidence.
