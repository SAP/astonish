# Runtime employee-message boundary

> **Protocol scope:** mandatory assessed-result and failure-narration requirements
> in the retained legacy sections below do not apply to `agent_led_v1` work. The
> explicit protocol preserves historical/in-flight assessment semantics.

## Agent-origin contract

The envelope additionally supports origin `agent` for the exact captured final
answer and `operation` for independently validated control receipts. An `agent`
message has a candidate digest and no model-review fields, legacy quality verdict,
or invented effect attestation. It binds `BotReport.Work` to private work, preserves
candidate text/artifacts exactly, and accepts succeeded answered/complete or partial
partial/blocked dispositions through `ValidateBotWorkCompletion`. A public projection
alone cannot authorize a candidate. Optional progress/check/skill calls are not
prerequisites and the composer never calls the narrator for this protocol.

The fallback regenerates closed, deterministic stage/reason/action text. It exposes
only counts of valid recorded observations and typed output IDs, not provider
errors, tool/source prose, artifact paths or partially streamed responses. It does
not claim that uncertain actions completed, nor ask the owner to review a hidden
draft. Waiting and stopped states remain distinct.

At the supervisor boundary, pre-model permission failures and Home/runtime failures
bind missing work metadata from the active leased store before committing the
failure. Supplied projections and invalid employee envelopes are never repaired;
successful candidate binding remains the adapter's responsibility. No model call
is added for this fallback.

At commit, `FinalizeEmployeeMessage` revalidates candidate/work identity before
refreshing the source digest for additive server-owned skill/control receipts.
Changed answer text, candidate digest, projection or other semantic facts cannot
be healed into agent-origin authority. Existing operation-origin receipts keep
their independent authority path. Restored integrity validation checks immutable
report data; commit validation additionally checks private work. No fallback or
message origin grants permission, effect success, delivery or replay authority.

## Legacy boundary (retained)

`BotReport.EmployeeMessage` is a communication envelope shared by runtime outcomes. It contains typed origin (`assessed`, `model`, `fallback`), text, source digest, optional assessed candidate digest, and a safe failure kind. It is not execution evidence, approval, or permission to retry.

## Composition

`bot.ComposeEmployeeMessage` runs after runWork has collected its final report/quality and before the supervisor commits. A validated private accepted candidate or verified blocker is preserved byte-for-byte, including its digest. `Summary` remains the original assessed candidate, independently of communication; public quality fields alone cannot promote prose to assessed origin.

For operational failures, the composer can make one tool-free call through the existing metered owner model and the same authorization/budget boundary. The response chooses between server-grounded first-person phrasings and echoes the source digest. This intentionally closed vocabulary prevents an arbitrary model claim from becoming public evidence. No raw errors, reviewer findings, tool arguments, or source text are fed to the narration call. Streaming must end with complete valid JSON; tool calls, extra fields, invented text, missing/wrong source identity, or incomplete output select fallback. Observed usage is retained, including unsuccessful narration calls.

No model/budget is created. Missing model, denied boundary, exhausted/unknown budget, cancellation, setup failure, and owner-input waiting states use deterministic fallback. Internal preparation failures use the existing typed setup reason only when validated private work has no prepared criteria, candidate, review or repairs, observed usage records zero tool calls, and the report has no effects, artifacts or pending approvals. The owner is told the requested work could not start and no result was produced, not asked to review a nonexistent incomplete result. Missing usage or evidence of production keeps the conservative partial/uncertain wording. A typed checkpoint response rejection can state only that calls in that rejected response were not executed; it never claims earlier actions had no effects. Unknown effects remain unknown.

`bot.EmployeeMessageFallback(status, report, cause)` is the exported no-model entry point for operation/recovery producers without reserved capacity. It returns the identical envelope type. Typed budget and checkpoint errors are recognized by identity, not provider-string parsing.

## Persistence contract

`report.ValidateEmployeeMessage(status, privateWork)` validates source integrity and exact assessed candidate identity without calling any model. Run committers should invoke this before mutating report fields. `report.EmployeeMessageSourceDigest(status)` excludes the envelope itself and binds the other report fields and status. A change to those fields requires a new envelope. Existing candidate acceptance checks must remain in place; communication text is not a replacement for them.

`RenderReport` renders an envelope verbatim, without reviewer or uncertainty appendices. Detailed diagnostics remain separately available in `Failure.TechnicalDetails`, quality limitations, and observed evidence. Legacy reports without an envelope retain their existing rendering behavior.

## Integration boundaries

The runtime adapter and runWork now produce envelopes, including pre-model failures. This foundational change supplies the store validator and fallback helper; operation/recovery finalizers and commit-time validation wiring are separate integration work. UI rendering/projection is also separate. Do not claim all historical or non-runtime paths are covered until those callers are wired.
