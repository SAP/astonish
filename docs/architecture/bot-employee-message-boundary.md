# Runtime employee-message boundary

`BotReport.EmployeeMessage` is a communication envelope shared by runtime outcomes. It contains typed origin (`assessed`, `model`, `fallback`), text, source digest, optional assessed candidate digest, and a safe failure kind. It is not execution evidence, approval, or permission to retry.

## Composition

`bot.ComposeEmployeeMessage` runs after runWork has collected its final report/quality and before the supervisor commits. A validated private accepted candidate or verified blocker is preserved byte-for-byte, including its digest. `Summary` remains the original assessed candidate, independently of communication; public quality fields alone cannot promote prose to assessed origin.

For operational failures, the composer can make one tool-free call through the existing metered owner model and the same authorization/budget boundary. The response chooses between server-grounded first-person phrasings and echoes the source digest. This intentionally closed vocabulary prevents an arbitrary model claim from becoming public evidence. No raw errors, reviewer findings, tool arguments, or source text are fed to the narration call. Streaming must end with complete valid JSON; tool calls, extra fields, invented text, missing/wrong source identity, or incomplete output select fallback. Observed usage is retained, including unsuccessful narration calls.

No model/budget is created. Missing model, denied boundary, exhausted/unknown budget, cancellation, setup failure, and owner-input waiting states use deterministic fallback. A typed checkpoint response rejection can state only that calls in that rejected response were not executed; it never claims earlier actions had no effects. Unknown effects remain unknown.

`bot.EmployeeMessageFallback(status, report, cause)` is the exported no-model entry point for operation/recovery producers without reserved capacity. It returns the identical envelope type. Typed budget and checkpoint errors are recognized by identity, not provider-string parsing.

## Persistence contract

`report.ValidateEmployeeMessage(status, privateWork)` validates source integrity and exact assessed candidate identity without calling any model. Run committers should invoke this before mutating report fields. `report.EmployeeMessageSourceDigest(status)` excludes the envelope itself and binds the other report fields and status. A change to those fields requires a new envelope. Existing candidate acceptance checks must remain in place; communication text is not a replacement for them.

`RenderReport` renders an envelope verbatim, without reviewer or uncertainty appendices. Detailed diagnostics remain separately available in `Failure.TechnicalDetails`, quality limitations, and observed evidence. Legacy reports without an envelope retain their existing rendering behavior.

## Integration boundaries

The runtime adapter and runWork now produce envelopes, including pre-model failures. This foundational change supplies the store validator and fallback helper; operation/recovery finalizers and commit-time validation wiring are separate integration work. UI rendering/projection is also separate. Do not claim all historical or non-runtime paths are covered until those callers are wired.
