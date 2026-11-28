# Mixed deliverables with pending controls

A durable pending receipt proves enqueue, not application. The source turn's
accepted candidate must remain the authoritative server acknowledgment, regardless
of optimistic producer prose or a permissive assessor judgment. A schedule-only
acknowledgment is retained as evidence but is not published as conversation.

`runBotWork` constructs pending `Candidate.Text` exclusively from the private,
identity-validated server ledger controls. The producer's complete final text is
retained separately in `Candidate.Bodies["producer-final-draft"]` as **untrusted,
private assessment evidence**. It is hashed together with the acknowledgment,
artifact bodies, observations and complete trusted pending payload. A review pass
never promotes the private draft into the published summary.

The pending reviewer evaluates the complete original requirements and queued
payload. `receipt_only` describes the publication surface, not a waiver of mixed
requirements. A report present only in the private draft has **not been delivered**:
the reviewer must mark that requirement failed/unverified and explain that mixed
prose delivery is unsupported. Other private drafts, including optimistic control
claims, cannot override the truthful server acknowledgment for control-only work.
Deferred application remains separate from current-turn enqueue correctness.

This is a conservative safety boundary, **not full mixed-deliverable support**.
Publishing mixed prose safely requires a distinct reviewed deliverable field with
contract/digest validation, independent of control claims. That is not implemented
here. Semantic recognition of mixed requirements still depends on the assessor;
these tests do not prove real-provider reliability. Existing artifact assessment
is unchanged.

`TestBotWorkMixedPendingPublication` exercises honest mixed drafts remaining
private/unverified, false application claims, missing reports, stale digests,
receipt-only output and optimistic control-only drafts. The regression in
`TestBotWorkPendingNoApplicationRepair/optimistic-producer` uses a deliberately
permissive pending assessor: even a pass cannot publish the producer's application
claim. Tests also check that private drafts remain available for assessment and
are digest-bound. No producer replay or polling is introduced after enqueue.

## Confirmed-only schedule communication

`CommitRun` alone assigns `BotReport.acknowledgment` with typed kind
`schedule_pending` and `operation_ids`. Caller-supplied markers are rejected.
`entstore.scheduleAcknowledgment` requires successful accepted private work, exact
candidate/summary equality, contiguous `pending-control:N` bodies, exact pending
tool-response envelopes and optional scoped assessment context (plus the private
producer draft), exact schedule payloads and observations, and independently
read pending executable operations in the source run's mandate and owner scope.
It verifies the issuer's grant and source run/user-event provenance. This is not
prose classification: arbitrary queued-looking text, mixed bodies/observations,
changed evidence, artifacts, failures and pending approvals are not suppressed.

The marker is part of the immutable committed outcome, not recomputed against
mutable operation status at publication time. `PublishReport` records its ordinary
idempotent receipt with optional `suppressed: true`, empty text and no notifications,
and appends no Home event. Publication-before-apply and apply-before-publication
therefore behave identically. The applied schedule receipt still reuses its
canonical operation event, preserving IDs and replay deduplication.

`BotOperationView` adds optional `presentation` (schema version remains 1):
- `internal`: schedule create/edit/pause/remove pending or applying;
- `conversation`: verified applied schedule summary, failed/blocked change, or cancellation;
- omitted: authorization-required actionable views, other operation kinds and legacy records.

The applied summary comes from the verified schedule receipt and saved payload,
not model prose. It confirms create/update/pause/remove, describes an every-minute
cadence where exact, and includes the verified next run in UTC where present.
Raw payloads, mandate data, grants and private draft text remain excluded.
Canonical state-only events are unchanged; clients render the summary according
to presentation rather than adding a second assistant event. Unknown optional
fields are compatible with existing clients; older omitted presentation retains
legacy rendering. Tests cover both scopes and publication orders, canonical event
reuse, forged/mixed rejection, error visibility, and API presentation serialization.
