# Supervised Bot execution intents

The earlier active-only integration blocker is resolved by explicit persisted execution intents. This document describes the implementation and its verification boundaries; it does not replace Phase 7 deployment acceptance.

## Separate consent without lifecycle changes

`store.BotExecutionIntent` is persisted in the existing BotRun JSON. The empty value is the backward-compatible regular run; `draft_test` and `conversation` are explicit alternatives. The server selects the intent from separate authenticated routes, not from arbitrary request fields:

| Route | Required lifecycle | Meaning |
| --- | --- | --- |
| `POST /api/studio/bots/{botID}/runs` | Active | Existing manual execution |
| `POST /api/studio/bots/{botID}/test-run` | Draft | One reviewed test; no activation or recurrence |
| `POST /api/studio/bots/{botID}/conversation` | Paused | Tool-free owner conversation; no resume |

Requests bind `version`, `idempotency_key`, and `instructions`. Identical retries return the same run; changing the intent or payload conflicts. The existing personal/team scope selector, authenticated authorization, immutable version, readiness, daily limits, run budgets and lease fencing remain authoritative. Unknown intents fail closed. Readiness for the two new intents does not require enabling recurring triggers. Neither endpoint enables schedules or changes Bot status.

Admission, durable input loading, worker polling, supervisor checks, model bindings, live guards, both scoped database CAS implementations, transcript writes and completion validate the persisted intent's required lifecycle. The zero-valued regular intent still requires Active; polling drafts does not make legacy queued runs executable. Explicit run cancellation remains available. Changing lifecycle or version invalidates execution rather than upgrading its intent.

A draft test uses ordinary Bot tool permissions and exact-action approvals. Approval alone does not enqueue continuation. Explicit resumption of the same approved draft-test checkpoint is allowed while still Draft, with a fresh fence and single consumption of the saved action. This exception does not allow ordinary approvals to resume while Paused. Test consent is not an action grant or recurring consent.

## Paused conversation

The existing wired Chat agent provides the reply. Its conversation configuration exposes no executable tools or discovery bridge, and the bound tool/live policy boundaries deny execution independently of model behavior. Conversation runs cannot request tool approvals. Studio currently accepts text only for this mode and explains why attachments are disabled.

The existing ADK session adapter supplies prior Bot home history to the model without copying old turns into the new run. Actual current user/model events are persisted through the existing fenced Bot transcript store. Follow-up replies therefore retain conversational context and survive reload; no fabricated response, second transcript engine or temporary Active state is involved.

Successful conversation publication reuses the persisted current-run model reply's event ID instead of appending a second rendered run report. The check requires that actual event in both the run and home transcripts. Run diagnostics and idempotent publication receipts remain available; missing replies and failed/partial outcomes still publish diagnostic reports. Conversation publication records notification skips rather than sending externally. Regular and draft-test reporting are unchanged.

## Studio behavior

Draft **Test once** requires a separate version-bound confirmation. Cancel admits nothing. After consent, the existing read-only run trace and output harness display results while the Bot remains Draft. Activation remains a separate action.

The paused owner composer sends through the conversation route and uses the same StudioChat reducers, durable watch and history path. It keeps the Bot Paused, displays the tool-free restriction and preserves text on errors. A submission lock is independent from watch refreshes, so an incoming snapshot cannot re-enable Send during admission.

## Evidence and boundaries

- `TestBotExecutionIntentsDurableLease` covers personal/team intent-bound admission, claims, renewals, approval continuation, stale leases and forbidden conversation approval creation.
- `TestBotIntentHTTPWorkerConversation` exercises real HTTP, SQLite and the production worker/Chat runtime with controlled model and sandbox adapters. Follow-ups see prior home turns once; attempted read, flow, delegation, unknown and question tool calls have zero executable callbacks.
- `TestBotIntentHTTPWorkerDraftApprovalExactOnce` exercises HTTP admission, the production worker's approval checkpoint, repeated decisions without execution, explicit resume and one saved harmless action. Both scopes remain Draft with recurrence disabled; ordinary paused approval resumption is rejected. An unverified tool receipt remains Partial rather than fabricated success.
- Publication regressions reproduce the duplicate successful conversation reply in both scopes before the fix and check unchanged ordinary reports, diagnostic failures and receipt replay afterward.
- Studio API/component/scenario tests cover explicit consent/cancel, stable retries, paused sending, submission locking and unchanged active sending.
- `TestBotScheduledWorkspaceBrowserAcceptance` contains the actual built-SPA intent journeys in addition to its scheduled work/approval journey, including reload and exactly-one conversation reply assertions.

These controlled tests do not establish real-provider behavior, physical sandbox execution for these intents, or live PostgreSQL concurrency. Those remain separate release checks. Existing fail-closed tests for generic manual requests on inactive Bots remain valid; they do not forbid the new explicit routes.
