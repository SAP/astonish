# Lifecycle resume with disabled recurrence

Pause and stop atomically disable owned schedules and clear their next occurrence without changing the immutable Bot version. Explicit lifecycle resume (`POST /api/studio/bots/{id}/activate` for paused/stopped Bots) accepts that disabled/no-next-run projection. Exact scope, owner, Bot, version, trigger, cron, timezone and payload binding checks remain, both before readiness and inside the revision CAS. Missing/corrupted bindings fail closed. Draft activation does not receive this exception.

Resume changes lifecycle only: it does not enable recurrence, replay missed occurrences, or automatically requeue approval-waiting runs. An approved run can then be explicitly resumed against its unchanged version and exact approval digest. Recurrence needs a separate explicit trigger-enable operation; approval consumption remains single-use and fenced.

## Explicit task-scoped resume under a mandate

The lifecycle-only API above is not the conversational recurrence path. A mandated
owner turn uses `propose_bot_control` with `operation: resume_tasks` and exact
`resume_targets.schedule_ids` from current owner-scoped control state. The model
schema omits legacy `resume`, and capture rejects it without saving a review
proposal; it never guesses targets or translates a broad resume into all tasks.
The current-control prompt distinguishes routine authorized operations from legacy
review proposals and does not ask for redundant confirmation within the mandate.

The worker applies targeted resume after the source turn ends. It verifies the
current operation mandate and each selected task's original unexpired grant, then
enables only those jobs and activates the Bot. Paused responsibility changes do
not resume work or renew task grants. Other disabled schedules remain disabled.
Out-of-ceiling controls still require bounded authorization; legacy lifecycle API
and non-mandated proposals are unchanged.

`TestMandatedResumeCannotFallBackToLegacyProposal` reproduces the erroneous broad
proposal and checks exact-target routing and exception preservation.
`TestRoutineResumeAfterPausedResponsibilityUpdate` covers authorized schedule
creation, a separately disabled schedule, pause, a paused responsibility update,
and explicit targeted resume through durable admission/capture/application in
personal and team SQLite stores, with no human proposal. These are deterministic
checks, not live-provider acceptance.

`TestBotActivateResumeHTTP` covers pause/stop and disabled schedule preservation through production routes and real SQLite in personal/team scopes. `TestBotResumeDisabledScheduleApproval` covers an exact approved run, unchanged version and no catch-up. `TestBotResumeDisabledScheduleRejectsCorruption` covers invalid schedule bindings. These deterministic tests use controlled readiness/worker identity and do not claim real model execution.
