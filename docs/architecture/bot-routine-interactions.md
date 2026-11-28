# Routine control intake and interactions

The concrete `propose_bot_control` name is retained for compatibility. For a run
with a mandate, its supported routine responsibility, persona, goal and memory
payloads use the same durable authorized-operation runtime as scheduling. The
capture adapter binds the live run lease and own Bot target; it never fabricates
a human proposal confirmation. Goal targets omitted by the model are bound to
the current Bot, while explicit cross-Bot targets fail closed.

Goal creation and editing require non-empty `title` and `completion_criteria`.
The concrete tool schema declares those fields, and authorized routine capture
rejects missing/blank goal text before persisting its grant or operation. This
matches the existing apply-time requirement: `description` alone is not a
completion condition. The source turn can correct its input (or clarify an
unknown outcome) instead of receiving a pending receipt that later cancels.
Criteria must reflect the owner's requested outcome; the adapter does not invent
criteria or substitute responsibility/memory operations. This input check does
not change goal-edit revision checks, apply-time authority/snapshot validation,
or paused independent work. Personal/team deterministic tests cover a paused
goal correction and application with its schedule still disabled and no next run.

The issuer must authorize the capture tool resource `propose_bot_control` and
exact execution resources returned by `entstore.RoutineControlResource`.
Canonical direct-owner source verification permits capture, not execution.
Storage independently selects Execute/Pending or RequireAuthorization/Blocked.
Only the worker's committed receipt establishes an applied mutation. Tool
outcomes verify durable capture only, and repeated worker polls do not apply a
successful operation twice.

Missing facts remain a real `ask_user` clarification. Answers are input, never
a capability grant. Unsupported external access, spending, destructive effects
and unknown adapters cannot be authorized by natural-language instructions.

## Current integration boundary

The HTTP acceptance fixture exercises personal and team owner message admission,
the actual worker and capture tool, then one persisted goal with no approvals.
The model and compute isolation are controlled in the fixture.

Bounded exceptions use the existing `/bots/{botID}/approvals/{operationID}/decision`
HTTP route with a `routine_exception` descriptor containing mandate reference,
operation ID, payload digest, expected revision, approve flag and expiry. The
store rechecks current authenticated owner evidence and exact
`BotMandatePolicy.ExceptionConstraints`; this is separate from worker grants.
Denial is durable and suppresses identical repeated escalation. Approval produces
Pending, not Applied; the worker applies one immutable effect and checks expiry
again. No viewer credential is minted as a worker grant. UI approval-card wiring
is not claimed.

`task_once` binds one deterministic queued run to exact instructions and the
original finite mandate/aggregate quota. It is dispatched after its source ends;
it never renews lifetime. `priority_pause` commits during authorized capture,
disables schedules and fences all active/queued runs, including its source. It
does not preserve an indefinitely running agent for narration. `resume_tasks`
requires 1–32 unique explicit schedule IDs; only current enabled cron triggers
with still-valid original task grants can resume. Legacy/expired grants and
unknown IDs fail closed; unrelated schedules remain disabled.

### Resume target identities and capture validation

`resume_targets.schedule_ids` accepts **canonical scheduler job UUIDs**, copied
from the current control snapshot's `triggers[].schedule_id` or the scoped
`list_scheduled_jobs` result's `jobs[].schedule_id` (also exposed as `job_id`).
For compatibility the list's existing `id` field remains the **Bot trigger UUID**;
it is not a resume target. Unbound list entries omit both canonical job fields.
The schema, list description/result message and current-control guidance make
this distinction explicit; trigger IDs are never silently treated as aliases.

Capture checks every selected target against the current pinned version inside
its existing authenticated, owner-scoped lease/source transaction, before
writing either an operation grant or pending/blocked operation. Each target must
be an enabled cron definition with a matching scheduler binding (owner, Bot,
trigger, version and team). Unknown, foreign and trigger-ID targets return a
typed invalid-target error; the tool exposes `status:error`,
`error_code:invalid_target`, the field and rejected ID, with no `operation_id`.
There is no durable operation to cancel later. The model may correct the ID in
the same source turn; it must not guess a replacement or infer foreign resources.

This read-only capture check is not permission to resume. Application still
revalidates source completion, current ownership/scope, version/revision,
original finite task-grant validity and scheduler bindings. No task authority is
renewed and unrelated disabled jobs remain inert. Deterministic personal/team
SQLite tests cover trigger-ID rejection before persistence and the actual
factory list → canonical target → durable capture → committed resume sequence.
They are not live-provider or daemon acceptance evidence.

The exact HTTP fixture now also exercises bounded task, priority pause, narrow
goal exception deny/approve and expired decision, for personal/team scopes.
Resume target and original-grant expiry checks have scoped store coverage.
Paused-owner-turn operation-purpose admission and a composed HTTP
pause/paused-goal/resume sequence remain unverified/unfinished. Existing legacy
proposal behavior is retained for non-mandated controls. No UI change or live
user-daemon/sandbox proof is included here.
