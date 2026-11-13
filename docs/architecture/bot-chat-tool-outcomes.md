# Interactive Chat tool outcome accounting

## Bounded deliverable assessment

For work-enabled Bot runs, `runBotWork` loads the lease-scoped pinned contract
before production. A tool-free preparation call extracts source-linked criteria;
every candidate is captured and persisted before a separate tool-free inspection.
Inspection receives original requirements, actual tool/source/delegate responses,
immutable artifact bodies and mechanical observations. A successful write is not
a quality verdict, and delegated output is still candidate work.

At most two correction attempts are consumed durably before execution. Each uses
a fresh ChatRunner and invocation identity, but one active-run registration,
resource cleanup, ordered event sequence, authority and shared budget. Preparation,
inspection, children and compaction all use the same owner-bound metered model.
Malformed/forged assessments, missing evidence, oversized inputs, unavailable
work storage and exhausted limits cannot produce an accepted result. Model
judgments are explicitly `model_assessment`, never mechanical attestations.
Prior unverified effects remain partial even after content passes inspection;
uncertain effects prevent corrective replay. Exact-action continuations cannot
perform additional repairs or reads. The repository separately verifies the exact
accepted candidate and quality identity at commit, then publishes the final report.

`TestBotWorkRuntimeHTTPAcceptance` uses real HTTP admission/readback, personal/team
SQLite, worker, factory and ChatRunner with a controlled provider. It forces
fail→repair→pass, finite exhaustion, malformed/forged verdicts, missing evidence
and authority revocation. `TestBotWorkControllerSafety` additionally covers captured
source content, unavailable storage, exact-action limits, unknown-effect no-replay,
interruption and a real shared BudgetLLM denying review. These controlled tests
are not real-model semantic certification or deployment evidence.

## Factory dependency timeouts

Factory setup errors matching `context.DeadlineExceeded` are failed attempts,
not interrupted runs, while the worker-owned run context remains healthy. The
report and error message identify a setup dependency timeout without exposing
endpoint URLs; the wrapped cause remains available for diagnostics. Actual run
cancellation/deadline classification, terminal-attempt behavior, retries and
authority are unchanged. `TestBotChatRuntimeAdapterFactoryDependencyTimeout`
exercises a short HTTP client timeout, zero model/tool usage, cleanup and a fresh
successful run; `TestBotChatRuntimeAdapterFactoryRunInterruption` preserves true
cancellation and the adapter-owned deadline.

## Known unavailable web sources

The pinned built-in `web_fetch` transport also records a completed non-2xx
HTTP response (with valid status, final URL and body) as an unchanged observation:
**requested source unavailable**, not successful retrieval. This prevents a known
401/429 read failure from being misclassified as an uncertain write/send and
blocking an otherwise assessed alternate-source result. The raw response and
limitation remain available to the reviewer; required missing information still
fails assessment. Transport errors, malformed responses, unknown tools and
non-read HTTP methods retain their existing uncertainty handling. This does not
certify article freshness or relax the final assessment/publication gate.

## Typed source-access facts

`bot.ToolOutcome.SourceAccess` carries server-only `[]store.BotSourceAccessFact`
from `recordBuiltinToolObservation`, independently of legacy effect receipts.
Only pinned built-in `web_fetch` (GET) and `http_request` (GET/default or HEAD)
responses qualify, with a bound Bot run and active function-call ID. Unknown tools
and write methods cannot mint access facts. This adds no authorization or retry.

Each fact contains `EvidenceRef = "capture:" + CallID`, the context-derived
`CallID`, argument-derived `OriginalURL`, observed `FinalURL`, `HTTPStatus`,
`Truncated`, UTC `ObservedAt`, and `BodyDigest`. Despite its historical field
name, `BodyDigest` is lowercase SHA-256 of `json.Marshal(responseMap)`, **not**
of extracted article text: it must match the captured response evidence digest.
Absent final URLs are not guessed. A valid 2xx response without an error records
`BotSourceAccessReturned`; non-2xx responses and explicit JSON error responses
record `BotSourceAccessFailed` (HTTP status zero when no valid status is known).
A 200 footer, empty body or truncated page establishes no usefulness, freshness,
truth or task completion. Those remain agent judgments.

Facts are snapshotted in the per-call ledger and consumed once. They neither
replace nor upgrade existing `Verified`, `Changed`, `Unchanged` or artifact
receipts. API consumers must retain facts even when `Verified` is false, resolve
the matching same-run capture, and compare its digest before persisting it.
Callers that return Go errors before invoking this helper provide no response to
capture. Callers that unwrap response envelopes must preserve or separately bind
the original response digest; an unwrapped digest is not evidence for an envelope.

`TestBotBuiltinSourceAccessFacts` covers 200 footer, 429, explicit error responses,
GET/HEAD, URL provenance, truncation, UTC and exact JSON digests while preserving
legacy receipts. `TestBotBuiltinSourceAccessRejectsUnboundAndNonRead` rejects
unbound, unknown and write paths. `TestBotSourceAccessLedgerSnapshot` checks copy
and consume semantics, and `TestBotCatalogueSourceAccessResponseDigest` checks the
production catalogue wrapper with controlled node responses and one call per
response. These tests do not claim live-provider or full API persistence coverage.

## Routine control capture

Mandated routine controls use server-only `bot.RoutineCapture` metadata, separate from schedule captures and applied-operation receipts. `launcher.verifyRoutineCapture` compares an independent authorized-operation read with the enqueue result and request, including pending/execute state, payload digest, source run, owner, mandate, idempotency key and provenance. Missing, mismatched or already-applied routine readback cannot produce a pending attestation. The per-call ledger snapshots and consumes metadata once; returned JSON cannot create it.

`ChatRunner.observeBotToolOutcome` binds the capture to the current run and response, records an **unchanged observation**, and supplies a trusted **pending, not applied** summary instead of premature model completion text. Capture metadata cannot also claim changed resources, artifacts, scheduling or a schedule capture. Application remains separate worker work after the source run commits; priority pause retains its immediate-apply contract. No REST/SSE or Studio/TUI rendering schema changes are involved.

Goal input remains nested under `payload.goal.goal`; schema descriptions explain the nesting, and schema-validation failures record the original diagnostic in the trusted ledger. A corrected later capture never erases an earlier failure or turns a mixed-error source run into success. `TestRoutineGoalSourceRunnerThenWorkerApply` exercises valid and malformed-then-corrected goal calls through the real factory/runner and temporary SQLite storage: pending source reporting, source-commit fencing, later worker application, retained pause state and unchanged source history. `TestRoutineCaptureReportsPendingNotChanged`, `TestRoutineCaptureLedgerSnapshot`, and `TestBotRoutineCaptureOutcome` cover readback failures, identity/tampering, single consumption and partial-error preservation. These are controlled regressions, not live-provider replay evidence.

## Interactive outcomes

Interactive Bot Chat uses ordinary Chat authorization, model binding and tools; it does not use the autonomous action classifier. The per-run wrapper reserves each dispatched tool invocation against the shared budget. Its request processor must replace the underlying tool's registration with the wrapper, just as discovery and lazy toolsets expose wrapped executables.

Trusted outcome accounting is separate from authorization and invocation counting. The shared built-in node observation helper is used by interactive and autonomous wrappers. A completed built-in HTTP GET with a 2xx status and response body, or a built-in file read returning content, supplies an unchanged read observation. This attests to transport observation, not truth of remote content or correctness of the model's answer. Write and publication outcomes still require the existing mechanically checked file receipts. Unknown tools, failed results, missing/tampered receipts and arbitrary MCP metadata do not become verified merely because Run returned nil error.

No runner tool-name exceptions, replay, global success gate bypass or policy widening are involved. Owner session and redactor values remain captured in the bound context, including workflow calls; workflow identity checks use run identity rather than requiring an autonomous policy for interactive Chat.

Scoped metadata discovery uses the same per-call `bot.ToolOutcome` ledger. Only the concrete `agent.botDiscoveryTool` wrapping launcher-owned local `search_tools` and `describe_tools` records an unchanged observation after authorization and successful lookup. Its source binds the run, helper and active function-call ID; its summary attests only to catalog metadata lookup, never an external action or the truth of returned content. Empty results and per-name lookup misses are completed metadata lookups, not proof that excluded tools exist or executed. Errors, absent results and revoked authority produce no success observation. Returned JSON and same-name unsealed tools cannot mint ledger entries. Native dispatch invokes the sealed wrapper once; `execute_tool` keeps the resolved target's original call ID and outcome, with no dispatcher attestation. Metadata discovery still spends no external-action quota. The runner consumes these records through its existing unchanged-observation path; no new report/UI schema or success exemption is needed.

`TestBotDiscoveryOutcomeThroughFactoryRunner` exercises controlled search → describe → permitted action → final through the actual factory/runner, preserving three response observations and one metered action. `TestBotMandateDiscoveryNativeContract` separately rejects an excluded target. `TestBotDiscoveryOutcomeExactInvocation`, `TestBotDiscoveryLiveBinding`, and `TestBotDiscoveryResultJSONCannotMintOutcome` cover direct/native identity, single consumption, empty catalogs, failures/revocation, and spoof rejection.

Regression coverage: `TestInteractiveChatOutcomeAccountingThroughRunner` runs real built-in web_fetch against a local HTTP test server using a test-only dial transport (SSRF validation remains enabled), real file reads/writes, missing write receipts and unknown shell effects. All perform exactly one node invocation and one budgeted tool call. `TestBotInteractiveChatHTTPAcceptance`, `TestBotInteractivePersonalHTTPAcceptance` and authority/question acceptance cases cover API scopes and Chat authorization. This is source-level regression evidence, not a live daemon/browser acceptance receipt.
