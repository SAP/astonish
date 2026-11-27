# Durable progress context integration status

> Resource semantics update — 2026-11-27: historical cap/deadline descriptions
> below do not imply current whole-run enforcement. Production uses accounting-only
> runtime/token/tool/model-cost settings, including saved `max_active_seconds`.
> Structural policy validation, pinned operational call deadlines/health monitoring
> and live authority remain separate; cumulative model cost is not bounded. See
> [current mandate semantics](../bot-mandates.md#existing-bots-and-pinned-grants).

## Database continuation fixtures

`TestBotProgressRecoveryHTTP` resumes the same run/session in a fresh shared
worker using a settled tool response, with no repeat dispatch. Home replay is
byte-identical across reads and does not execute work or write runtime files.
`TestBotDatabaseScheduledOccurrenceHTTP` admits a real occurrence through
`scheduler.Executor`, checks duplicate admission retains its run ID, executes the
existing worker once, and replays its committed final without redispatch in both
SQL scopes. It does not substitute for physical Docker restart acceptance.

`TestBotDatabaseDelegateStateReplayHTTP` runs the existing `SubAgentManager`
against a Bot-linked scoped session service. Child creation inherits parent Bot
linkage and retains caller initial state. Content emitted by the existing shared
compactor and subsequent state deltas survive transcript reload; requesting only
one recent event still replays the complete initial state. Reload never invokes
the delegate model again. This checks serialization of existing compaction output,
not a new durable compaction engine or automatic transcript replacement policy.

## Payload-free production timing

`BotWorkerConfig.ObserveExecution` installs the same context observer used by the
shared factory. Trusted owner rebinding carries only this callback, not request
values or authority. `BotExecutionTiming` carries typed stage/result and interval
endpoints/duration; no prompts, arguments, credentials or error text are emitted.
Callbacks can run concurrently and must synchronize their own storage.

The repository measures queue residence from the latest committed queued state
(falling back to run creation), actual ClaimRun, RequestApproval and CommitRun,
including rejected calls. The Chat adapter measures startup before producer entry,
owner admission to first durably accepted text event, and final settled progress
bookkeeping separately from CommitRun. Factory hooks continue to measure preflight,
provider, executable tool and between-call bookkeeping. Local intervals retain Go's
monotonic clock; persisted admission times use wall clock and clamp future times.
Each interval closes once, including errors. Aggregate setup and queue spans can
contain smaller intervals: use interval unions, never add/subtract nested durations
as independent provider/tool exclusions. Physical latency accounting is unchanged.

## Read-only prompt refresh and mandatory access persistence

Optional `BuildBotPrompt` refreshes use `botAgentWorkService.loadSnapshot`: live
authorization, lease-bound work reads and protocol checks still run, but pending
source-access facts are not committed under the optional two-second read deadline.
Mandatory producer preflight continues to call `load`, binding and durably saving
pending facts with the existing CAS before dispatch. This avoids abandoned
multi-file writes and repeated commits caused by treating a prompt read as a
mutation. Explicit work mutations retain their existing flush behavior; no
provider/tool/yield read-scope lifetime is extended.

Prompt-only work reads request `PromptSnapshot` and use the adapter's already
persisted current-run session when available. They retain the latest bounded
response references in chronological event/part order, excluding prior Home
history; hashes and event/call identities are unchanged. An empty snapshot is
valid and does not trigger historical hydration. Unavailable snapshots fall back
to the scoped transcript reader. Explicit `bot_work_read` pagination and exact
`bot_evidence_read` retrieval remain unchanged; no permission decision is cached.

## Synchronous producer preparation

The Bot factory installs optional ChatAgent preparation callbacks at ADK instruction
construction. Immutable prompt reads and tool declarations share a request-owned
operation. The factory borrows that operation for live checks and control context;
its progress decorator transfers cleanup to BudgetLLM through an explicit capability.
BudgetLLM closes the operation after reservation and before provider iterator
creation or any consumer yield, including errors. Unknown decorators and synthesis
retries close conservatively at the factory boundary. Progress evaluation is a
separate boundary: ChatAgent closes preparation before invoking its controller.
Consumer events and invocation cleanup also close preparation. Ordinary Chat has
no callbacks. SQL authority is never reused. This does not promise a single cache
across an intervening evaluator or compactor model call.

API work-state loading and approval capture participate in the same explicit
preflight contract when every inner decorator supports it. Their scopes close
before unknown model entry, actual provider creation, response processing and
consumer yield, including failure and panic paths. Each new model invocation
performs fresh work and authorization checks. Supervisor heartbeats separately
own a synchronous read operation spanning mandate/budget checks and lease renewal;
the operation closes before returning to the heartbeat wait. Neither mechanism
retains immutable bytes across model execution or between heartbeats.

Optional prompt refreshes are read-only. Pending captured evidence is persisted
by mandatory producer preflight or, when yielding first, before checkpoint work
revision binding. This preserves the evidence index for independent review after
process restart without making the optional two-second prompt refresh perform
multi-file writes. Progress checkpoints also retain the bounded invocation binding
for each in-flight tool call (run identity, canonical argument digest, transport
and concrete target, and trusted read/source classification). A successful response
after restore can therefore settle that exact call; missing, malformed, mismatched,
duplicate, historical-format, or write-effect bindings remain uncertain and keep
the no-replay boundary. Final commits merge repository-accounted elapsed runtime from
an intervening heartbeat, but still reject lower durable tool, token or cost usage.

## Progress evaluator JSON input contract

Progress evaluation prepends a trusted request to return JSON to the user-role
input, before the unchanged observation/previous-decision payload. Some configured
providers enforce JSON-mode eligibility against input messages without counting
system instructions. Both initial and format-correction attempts carry this
prefix. The existing 64 KiB raw-observation cap remains unchanged; the constant
format prefix adds only bounded overhead. System instructions and the prefix
explicitly treat the payload as untrusted data, never authority. The owner's
assignment, evidence and previous decision are not rewritten, and bookkeeping
scope lifetimes are unchanged. `TestBotProgressEvaluationProportionalRigor`
checks the exact prefix and byte-preserved payload on both attempts.

## Synchronous progress bookkeeping reads

`ProgressController.BeginBookkeeping` is installed by the API composition with
an immutable-content read operation. Boundary/drain, observation and the first
evaluator boundary share that operation. For decorators explicitly implementing
`SupportsModelPreflight`, the controller transfers cleanup through the neutral
`execution.WithModelPreflightEnd` contract. The Bot factory preserves only the
immutable operation across owner rebinding and chains caller cleanup to its own;
BudgetLLM closes both before actual provider entry, including iterator creation.
Unknown models still close eagerly. Correction attempts receive fresh scopes. After evaluation,
settlement/boundary and checkpoint commit share a new operation, which closes
before owner interaction or failure backoff. Direct observation/commit/drain
callers still own a short scope when none exists. SQL authority, lease and CAS
checks remain live on every call; no mutable Home or permission data is cached.
`TestProgressSynchronousBookkeepingLifetime` checks this ordering.

## Controlled execution timing

An optional request-local observer records typed preflight/provider/tool/bookkeeping
stages, success/failure and durations only. It carries no prompt, tool arguments,
source bodies or authority, and is copied explicitly through owner rebinding.
Observers must be concurrency-safe and nonblocking. These stages are diagnostic
intervals, not additive accounting: nested stage durations must not be subtracted
from a wall-clock boundary twice. The physical between-call fixture resets auxiliary
provider duration after each actual tool return, so previous calls cannot hide the
next gap. Its employee extension reports admission and execution through final commit
separately and checks completion without another approval or mandatory artifact.
The maintenance gate measures from the persisted queued-state commit timestamp to
an actual persisted StartedAt plus nonzero lease fence, not HTTP response return
or an arbitrary nonqueued status. Completion logs also expose queue-to-claim,
claimed-through-finish, producer-return-to-fixture-end and state-commit-to-return;
these overlapping diagnostics are not summed or excluded from the 15-second gap.
`TestBotProgressEvaluationProportionalRigorStrictJSONHTTP` checks both the initial
and correction requests against a strict loopback JSON-mode endpoint.

## Same-transaction state-envelope coalescing

Agent-led `SaveWork` and non-suspended `CheckpointProgress` assign the activity's
state sequence and timestamp before their existing envelope write. The saved-run
activity helper inserts the durable activity and delegates to `AppendRunState`,
which still reads authoritative persisted state. Personal and team adapters omit
only the redundant envelope encode/SQL update when that persisted sequence/time
matches exactly. Ordered durable transcript append and after-commit notification
still execute. Suspended checkpoint lifecycle writes and all other transition
producers retain their original persistence path. File fsync/readback precedes SQL
selection as before; this does not batch reservations with executable effects.

## Transaction-local unchanged checkpoint writes

A transaction remembers one fully authenticated run-content reference and a
SHA-256 digest of the fields not reconstructed by `applyRunControl`. Context
saves, dispatch accounting and quota bookkeeping reuse that exact immutable
reference only when Bot/run/version and this digest match. All nested work,
progress, title and replay changes still write a new checkpoint. No payload
bytes, authority decisions or caller-owned pointers are retained in the receipt;
it is not shared with later transactions. SQL root fencing, lease predicates,
logical digests and activity/checkpoint atomicity are unchanged. Fresh reads
continue to verify the actual referenced content before a receipt is recorded.

## File-authoritative private activity records

Progress context bytes and control-proposal payloads (including decision receipts)
are retained as immutable activity content, not embedded in SQL outbox JSON.
Personal and team activity adapters use their transaction's content codec for both
writes and hydration. SQL retains the context binding and digest, and a typed
proposal projection containing identity, version/revision fences, operation,
request/draft digests, status, decision actor/time and reconciliation state. The
codec checks these projections against the hydrated content before replay,
listing or decisions; file text alone cannot replace SQL authority. Dispatch
request/receipt/settlement fencing metadata remains in the existing typed SQL
projection. Missing content, digest mismatches and unsupported inline formats
fail closed; there is no legacy inline-content fallback.

`TestBotContentProgressControlPersistence` exercises personal/team SQLite
transactions, raw-SQL sentinel exclusion, database reopen, context and proposal
replay/listing, altered authority rejection and missing-file rejection. This is
local store evidence, not dedicated PostgreSQL or live sandbox acceptance.

New Bots select `progress_v1` through `AppConfig.EffectiveBotRuntimePolicy` when
neither an explicit runtime policy nor historical `bot_mandates` operator ceilings
are configured. Explicit legacy policy and saved finite ceilings retain their
semantics; existing versions and grants are never upgraded on read. A compatible
explicit progress policy may opt new authorization into a finite active-time cap.

### Generation health contract

New `execution.NewBotProgressRuntimePolicy()` policies explicitly select
`generation_health: {idle_timeout_seconds: 120, check_interval_seconds: 10}`
with no elapsed model-call deadline (`model_call_timeout_seconds` is zero/omitted).
This is a stall detector policy, not a maximum generation age. A healthy Bot may
continue for hours. Production runtime/token/tool/model-cost settings (including
saved `max_active_seconds`) are compatibility/accounting data, not execution caps;
there is no bounded cumulative model-cost guarantee. Cancellation, live authority
expiry/revocation, concurrency and daily-run/schedule admission remain separate
controls. Setup and individual tool operations retain operational deadlines, not
a whole-assignment budget. See [current resource semantics](../bot-mandates.md#existing-bots-and-pinned-grants).

The Bot `BudgetLLM` snapshots this policy and owns one monitor per generation,
including non-streaming evaluator/auxiliary requests. The dispatch wrapper supplies
the owner-bound live authority/lease/isolation check; the worker supervisor remains
independent. Idle expiry and parent cancellation do not wait for an authority read
to finish: they cancel its context, then monitor shutdown joins that read and the
provider iterator before a dispatch becomes settled. A successful normal stop does
not turn its own context cancellation into a failed generation or discard final
usage. Interrupted or early-consumer-exit calls retain conservative reservations.

Progress evaluation accepts a zero elapsed timeout only because its normal model
path is the same health-metered Bot model. Factory setup keeps an independent
120-second bound (or the saved finite model timeout). Configured search remains a
tool operation subject to its existing tool-call deadline; health mode does not
remove that outer tool bound or authorize retries of external effects.

Saved policies without `generation_health` retain their positive elapsed model
call timeout; decoding, cloning and reads never upgrade them. Health and a
nonzero elapsed model timeout conflict and are rejected. Health intervals must
be positive and duration-safe, with check interval no greater than idle timeout.
Unknown JSON health fields are rejected. A request cannot replace an operator's
elapsed timeout with health monitoring unless the trusted ceiling explicitly
selects health. Requested idle/check intervals cannot exceed that ceiling;
a finite historical model timeout can narrow a health ceiling up to its idle
limit. Existing active-time and resource ceiling checks are unchanged.

`bot.SharedRunBudget.GenerationHealth() *execution.BotGenerationHealthPolicy`
returns a detached optional snapshot for generation-monitor consumers.
`ModelCallTimeout()` returns zero for a validated health policy, and the budget
continues exposing the independent hard assignment deadline through `Deadline()`.
The policy/storage tests establish this contract, not live watchdog operation.

#### Reviewed Studio configuration

`web/src/api/botConfiguration.ts` shares the additive health policy with
`bots.ts` through `BotVersion.budget`; it validates the same positive bounded
intervals and rejects health plus a nonzero elapsed model deadline. Zero or an
omitted model timeout is valid only with health monitoring in progress mode.
`BotConfigurationPanel` loads saved policies verbatim: a saved 120-second model
timeout without health remains an elapsed deadline, including on unrelated edits.
Only explicit selection of progress mode for a new policy uses the new 120/10
health defaults. Existing progress policies have a separate **Use generation
health monitoring (explicit opt-in)** checkbox; switching it on replaces only the
model timeout with zero and adds health intervals, preserving total active-time,
tool and spending caps. Switching it off restores the baseline elapsed timeout
if present, otherwise requires entering a positive elapsed timeout.

The optional editor remains available only for authorized draft/paused/stopped
Bots. Loading/error/active/deleted/read-only states cannot save. Exact before/after
policy parameters, reason, pinned revision/version and separate rights consent
are required before the existing configuration PUT; edits reset consent and
conflicts retain the draft for explicit re-review. No live version, grant or
assignment is rewritten on load, and no new questionnaire or event is introduced.
The UI distinguishes inactivity from task age and explains that no fixed
generation duration does not remove spending, authorization or cancellation
boundaries. Terminal clients continue to decode the shared Go store budget types;
this is a configuration-only change, not a transcript/SSE rendering change.

Focused coverage: `botGenerationHealth.test.ts`, `botProgress.test.ts` and
`BotConfigurationPanel.test.tsx` under `web/src` cover validation, unchanged saved
deadlines, explicit reviewed opt-in, conflicting health limits and read-only
controls. These component tests do not establish live watchdog behavior.

The production Chat adapter installs dispatch accounting, context restoration and
the employee progress controller. Its activation guard remains fail-closed when
required stores or work state are missing; it is not a blanket feature refusal.
Initial title clarification uses the bounded ask-user-only approval path before a
work journal exists. Answering the title resumes the saved assignment with its
progress policy intact.

`store.BotProgressContextStore` stores bounded private working bytes in the existing
scoped Bot outbox. References contain the run, work revision and SHA-256 digest;
loading requires a current fenced lease and the reference/digest selected by the
committed progress checkpoint. Arbitrary paths are never opened. Context writes
are immutable/idempotent and use the existing root lock and activity sequence.
An orphaned context record cannot be loaded as committed context. These private
records share the existing Bot outbox deletion lifecycle.

`bot.ProgressCheckpointer` writes context before the progress checkpoint and restores
controller cadence, measured fingerprint and last decision only after verifying
committed bytes. The repository still independently rejects unsettled dispatches.
Restore returns working bytes without inserting an owner message. The runtime must
still deserialize these bytes into its ordinary producer; this helper does not
execute or resume an agent.

`TestBotProgressControllerDurableContextRestart` composes the actual controller,
checkpointer and personal/team SQLite stores, performs two employee evaluations,
closes/reopens the database, restores a fresh controller, and performs a third
evaluation. It checks altered digests, stale lease fencing and context size limits.
This is a scoped repository composition test, not worker/HTTP employee acceptance.

Production-path regressions `TestBotProgressEmployeeHTTP` and
`TestBotProgressRecoveryHTTP` exercise the worker/adapter with controlled models
and isolated SQLite, including restored ordinary producer context.
`TestBotTitleConfiguredSearchHTTP` uses the default operator policy, creates a
personal/team title over HTTP, answers its real question over HTTP, and executes
the confirmed assignment through the production worker. Configured search yields
a final read-only result; missing search produces an honest blocker. Trusted read
dispatch settlement is not a content-success receipt and never proves an unknown
write completed. Real-provider quality and wall-clock soak evidence are separate
from these deterministic regressions.

## Evidence-informed evaluation and bound tools

Progress observations now include the pinned/confirmed assignment, scoped owner
answers, persona, current fenced work notes and bounded actual tool-response
excerpts. Excerpts remain private, redacted and explicitly untrusted. Partial and
thought content is excluded. Encoded observation is capped at 48 KiB, including
at most 32 KiB of excerpts and 8 KiB of reference metadata. The reference fingerprint
remains separate from model-written notes; rewriting notes does not manufacture
measured progress. Omitted material is unknown, not absent. Internal context gaps
must not become requests for the owner to repeat established scope.

Progress and synthesis timeout wrappers preserve the private trusted
`botBoundToolContext` marker without granting it to arbitrary incoming contexts.
Workflow binding, owner identity, cancellation, deadlines and budget checks remain
intact. `TestBotWorkflowBindingThroughProgressAndTimeout` exercises the real question
adapter through both wrappers; negative tests reject unbound and foreign contexts.
`bot_runner_progress_context_test.go` covers compose/observation/evaluation with
actual scoped transcript data.

Source-access flushing reuses the captured evidence reference instead of scanning
the same transcript twice. A 12-source benchmark halved reads (24 to 12) and roughly
halved time/allocation for large responses. This does not explain the original
live 28-second gap; mandated SQLite profiling did not reproduce that delay. No
speculative quota, authorization or indexing change was made.

## Fresh real-provider browser evidence

On November 16, 2026, `TestBotRealProgressBrowserAcceptance` created a disposable
personal Bot through built Studio in actual Chromium, answered its real title
question, and used real `gpt-5.6-terra` generation with `sonar-pro` search. Durable
mandates and progress policy were enabled. A short 10-second test cadence exercised
five source-bearing progress evaluations. The run used six searches, asked only
the initial scope question, succeeded in 77.23 seconds and published one report.
The complete persisted report and ordered links matched exactly once in the
original tab and after exactly one reload. Evidence and screenshots are retained
under `.tmp/real-progress-browser-41130366/`.

Earlier runs are separate evidence: an unmandated run did not cover progress;
two mandated runs completed but encountered browser harness errors. Those errors
were reproduced and corrected, not counted as passes. The final journey uses
isolated SQLite, HTTP and Home/node transport, not the normal daemon or a real
sandbox container. It proves real-model execution and browser delivery, not
exhaustive factual verification, guaranteed latency or deployment to existing
Bots. The opt-in harness requires explicit provider configuration and bounded
execution; raw private artifacts must not be published wholesale.
