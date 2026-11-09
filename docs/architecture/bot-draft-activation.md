# Scheduled draft activation

Interactive Chat is independent of this lifecycle transaction: title-first creation and authenticated owner messages may do useful scoped work while Draft or Paused without activation. A title, question answer, or successful interactive turn is not consent to recurring work. The activation rules below apply only to separately reviewed autonomous recurrence; legacy control-only conversation remains compatible. See [Chat-first contracts](bot-chat-first.md).

A reviewed `schedule_create` or `schedule_edit` proposal may save an enabled cron trigger while its Bot is still Draft. That immutable flag records the reviewed recurrence intent, not permission to run before activation. The existing scoped scheduler row must remain disabled with no next occurrence until separate activation consent.

## Delegated scheduling

The rules below describe legacy owner-reviewed configuration. The authorized scheduling worker has a separate exact-source transaction: an owner-authorized enabled schedule captured under a bounded task mandate may activate a Draft in the same commit that installs its target job and observed receipt. This is not title/turn-completion consent, nor does it upgrade the source task pin. Readiness and current policy still fence application; Paused/Stopped Bots and unrelated disabled jobs never implicitly resume. The tool's pending receipt is not activation evidence. See [authorized scheduling](bot-authorized-scheduling.md) and [Home delivery](bot-schedule-home-delivery.md).

## First activation transaction

Both exact `BotControlActivate` proposal confirmation and legacy `BotStore.Activate`:

1. Check owner authorization, current revision/version, lifecycle and configured trigger bindings. Proposal confirmation additionally checks the saved digest, expiry and completed source run.
2. Accept a Draft's enabled cron definition only with its matching, disabled/no-next-run scheduler projection. An already enabled Draft job is a conflict, not evidence of consent. Scope, owner, Bot/version/trigger IDs, cron/timezone and executable-payload restrictions remain checked.
3. Run trusted backend readiness outside the write transaction, then recheck bindings and concurrency guards inside the transaction.
4. Commit Draft → Active and enable only the existing reviewed enabled cron rows, computing their next occurrence from the activation clock. No scheduler rows or immutable versions are created by activation, and no immediate run is queued. A receipt/activity failure rolls back both lifecycle and schedule changes.

An explicitly disabled trigger remains disabled. Replaying an applied proposal does not re-enable or reschedule it. Paused/Stopped → Active is **not** first activation: existing resume/activation behavior leaves disabled recurrence untouched and requires a separate reviewed enable operation. See [disabled recurrence and resume](bot-resume-disabled-recurrence.md).

The state handling lives in `pkg/store/entstore/bot_activation.go`, with transactional callers in `bot_control_proposal_execution.go` and `bot_transactions.go`. Draft schedule creation/edit projection in `bot_triggers.go` is unchanged.

## Regression evidence

`TestBotDraftScheduledActivationConsent` uses real personal/team SQLite stores, authenticated `AdmitMessage` control conversations, worker-bound proposals, exact schedule create/edit confirmations, and separate activation consent. It covers create-enabled and disabled-then-edit-enabled sequences through both activation interfaces, disabled recurrence preservation, immutable version preservation, exact receipt replay, and subsequent due-occurrence admission. Personal repositories have no fixed team slug, matching production owner scoping.

`TestBotDraftScheduledActivationRejectsCorruption`, `TestBotDraftScheduledActivationRollback` and `TestBotDraftScheduledActivationLegacyReadinessCAS` cover mismatched bindings, wrong scope/digest, prematurely enabled Draft jobs, missing rows, readiness refusal, preflight-to-transaction mutations and final receipt rollback. Existing activation and disabled-resume tests remain required. The old corruption case treating an inert Draft projection as invalid now tests an Active projection instead; Draft's valid state and invalid enabled state have explicit regression coverage.

These are store tests with controlled readiness, not deployed daemon, scheduler tick, sandbox, provider or PostgreSQL proof. No daemon/build/live harness changes are needed for this store fix.
