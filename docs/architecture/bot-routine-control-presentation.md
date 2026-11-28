# Confirmed routine-control conversations

Routine metadata changes use the same confirmed-only conversation contract as
schedules. The finite presentation allowlist is responsibility, persona,
goal_create, goal_edit, memory_remember, memory_correct, and memory_forget.
This is presentation policy, not an authorization ceiling.

## Source acknowledgment

`CommitRun` binds an accepted acknowledgment-only candidate to independently read
pending operations. It preserves source run/user event/owner IDs, exact stored
payload equality, ordered observations, candidate text, assessment scope, and
immutable issuer-grant checks. Schedule captures use `verifyAuthorizedGrant`;
routine captures use `verifyRoutineGrant` and `RoutineCapture` acknowledgment and
observation semantics. Mixed content, tool errors, extra evidence, mismatched
payloads, and caller-provided markers remain visible or are rejected.

The private tool-response envelope must match the concrete tool exactly:
schedules have operation_id/status/message; the current routine tool has
operation_id/bot_id/digest/status/decision/message (six fields). The real launcher
factory/tool/runner test checks this path rather than assuming schedule's
three-field envelope or adding an invented seventh field. Unknown fields fail
closed. These private snapshots are not presentation authority.

New routine (or combined routine/schedule) acknowledgments receive the server-only
`control_pending` marker. Existing `schedule_pending` markers remain supported.
Publication persists an idempotent suppression receipt with no Home prose or
notifications, regardless of whether application or source publication happens
first. The source run's immutable report remains available for diagnostics.

## Final publication

Pending/applying operations use `presentation=internal`. Applied metadata changes
use `presentation=conversation` only with a source-bound executor receipt whose
single verified postcondition names the exact `bot_control:<operation>` resource.
Summaries describe the confirmed mutation without operation UUIDs or payload
contents. Failures, blocked-without-authorization, and cancellations have honest
conversational summaries; authorization exceptions retain the existing card.
Absent presentation remains the legacy fallback.

Routine application writes its operation event and reserved
`authorized-routine-receipt:<id>` publication intent in the same transaction.
Publication independently verifies the routine grant and receipt, then reuses
the canonical operation event's exact ID/revision instead of appending duplicate
prose. Retry does not reapply or publish another confirmation. External delivery
is not authorized by this receipt. Studio and terminal already consume the
shared optional presentation field; no client rendering or trigger changes are
required.

## Deliberate exclusions

- `task_once`: application queues a separate run; it does not complete that work.
  Its existing capture/result presentation is unchanged and must never be
  generalized into a completed-task confirmation.
- `resume_tasks`: selected schedule resumption has lifecycle and per-target
  verification semantics outside this metadata-only change.
- `priority_pause`: may apply immediately during capture, unlike the deferred
  pending acknowledgment contract, and can fence/cancel other work.
- Activation, broad resume, configuration, and owner-review proposals retain
  their existing authorization and presentation behavior.

## Verification

Temporary personal and team SQLite tests cover all seven metadata kinds, both
apply/publication orders, commit/apply/publication retries, one canonical final
Home event, forged markers, mixed prose, error responses, payload/source and
routine-envelope tampering. Shared projection tests cover all statuses and
unrelated receipts. API tests preserve presentation and source identity through
the transcript wire mapper. The real launcher/tool/runner test covers clean
captures and retained mixed failures. Existing schedule and terminal parity
suites remain applicable. These are controlled tests, not live-user or live-model
acceptance; no running user data is modified.
