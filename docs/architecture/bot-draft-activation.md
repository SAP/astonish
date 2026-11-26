# Active Bot lifecycle and independent recurrence

This supersedes the experimental scheduled-Draft-activation design. Bots are
unreleased: the current lifecycle is **Active, Paused, Stopped, Deleted**, with no
Draft state, first-activation transaction or compatibility activation endpoint.

Ordinary, template, source and promotion creation returns Active immediately.
Creation does not queue work, provision physical Home or enable recurrence.
Explicit title-first creation (`CreateAndStart`, `start_from_title:true`) admits
exactly one requested conversation; identical retries return the same Bot/turn.
Useful scoped work follows current caller authority and resource readiness, not
an onboarding activation ceremony.

## Recurrence consent

An enabled schedule requires a separately authorized exact schedule operation.
Neither creation, question answering, successful conversation, Home preparation
nor unrelated schedule edits grants recurring execution. Authorized application
writes the target version/job and receipt atomically. Unrelated scheduler rows
preserve their exact effective Enabled and NextRun values.

Pause/stop disables recurrence. `POST /api/studio/bots/{id}/resume` restores
lifecycle only and does not enable disabled schedules or replay missed ticks.
Explicit targeted `resume_tasks` may enable only the separately authorized targets;
other disabled schedules remain disabled. Scope/owner/version checks, readiness,
lease fencing, revision CAS and receipt rollback remain mandatory.

## Verification boundaries

`--stage active-bot-status` requires exact fresh named creation, HTTP, schedule,
CLI subprocess/PTY and built Studio browser passes, including personal/team
creation. Any skip, malformed evidence or missing executable browser blocks.
`--checks-only` checks the driver, not Bot behavior. Real provider/deployed sandbox
and disposable PostgreSQL fresh-schema behavior are separate unverified tiers.

No Draft upgrade/data conversion, historical execution-intent adapter or marker-6
migration is required. Existing DDL marker 5 and unrelated migration infrastructure
remain unchanged. Experimental development Bots are disposable; cleanup must use
scoped Bot deletion, never remove shared databases, ordinary Chats or unrelated
resources. Current-format backups keep their safe paused restore rules. Unfinished
forms and skill/task drafts are not Bot lifecycle states and remain supported.
