# Bot-home cleanup and permanent deletion in the daemon

The Bot worker polls deleted Bots as part of its ordinary owner-scoped store
traversal. After retention expires it uses `BotHomeDeleteBackend` only when the
backend and the optional `BotHomeCleanupStore` / `BotCleanupStore` contracts are
present. Unsupported backends retain the home; there is no host fallback.

## Reservation before external deletion

`ReserveHomeCleanup` requires an internal service principal with `bots:cleanup`
and matching routed organization/team. It validates the exact registered home,
Bot revision and fence, deleted status, elapsed restore window, absence of a run
lease/active-run teardown barrier, and absence of queued/running/waiting runs.
An unresolved teardown barrier deliberately blocks retirement: this path does
not pretend that cancellation alone proves worker shutdown.

A transaction writes an immutable claim as a JSON string in existing Bot
metadata, preserving integer precision without adding schema. The write uses
revision/fence/sequence/status/expiry CAS and records a lifecycle activity. The
claim is not cleanup completion and does not make a record purgeable. A restore
that previously read the Bot loses its sequence CAS; a later restore is rejected
because the claim exists, even if the wall clock moves backward. If restore wins
first, reservation fails before the backend is called.

## Receipt and retries

Trusted worker composition installs `BotHomeManager.AuthorizeDelete` for this
operation. The callback itself rejects owner contexts, reserves the exact home,
and derives sandbox identity from the stored owner and trusted tenant context.
It never promotes a request caller to a service principal. Each operation has a
private value-copied claim; authorization is not kept in a shared mutable field.

Only a successful backend `DeleteBotHome` return installs an exact-request
`BotCleanupVerifier`. This callback attests completed work and cannot initiate
deletion or verify another request. `ConfirmCleanup` retains its independent
preflight/postflight checks. Only then may the existing `PurgeDue` remove the
cleanup-confirmed tombstone. Backend failures and failed confirmation leave the
claim and tombstone in place, so a later poll retries idempotent deletion rather
than falsely declaring completion.

## Evidence and boundaries

`TestBotHomeCleanupReservation` and `TestBotHomeCleanupRestoreWins` exercise both
SQLite Ent scopes, including stale restore CAS, expiry, teardown barriers,
service-only authorization, wrong binding/revision, durable retry, failed
verification and database purge after successful attestation.
`TestBotRetention*` drives the production worker poll and installed launcher
manager with backend/store fixtures, including a failed delete then a successful
retry and direct owner invocation of the raw callback. These are lifecycle and
composition fixtures, not proof that Docker removed a volume. Live Docker
artifact/read/delete proof belongs to `TestBotHomeDockerPersistenceLive` and the
separate Docker offline-home acceptance described in
[docker-bot-home-offline.md](docker-bot-home-offline.md).

## Explicit permanent deletion

`POST /api/studio/bots/{id}/permanent-delete?scope=personal|team` accepts only
`expected_revision` and `confirm: true`; there is no user-supplied reason. The
actual Bot owner must authorize the request, including in Team scope. Existing
`DELETE` retains its 30-day compatibility behavior; it is not the Studio trash
control. Studio's sidebar and lifecycle controls open the same permanent-delete
confirmation in Personal and Team modes. They do not offer restoration or claim
that a pending response means removal. The Bot name and irreversible consequences
are shown; no reason is requested. The pending dialog stays scope-bound, retries
status checks, and reports errors without optimistic removal of the Bot.

Admission atomically stores irreversible `permanent_delete` intent in existing
Bot JSON, sets this Bot's deadline to now, disables schedules, revokes approvals,
and fences/cancels its work. Restore is forbidden even after clock rollback.
Cleanup still requires the service-only teardown/home verification above.

HTTP returns **202** with `{bot, status: "pending"}` and `Retry-After: 2` while
cleanup cannot yet be proven. The worker consumes the durable intent, verifies
physical cleanup and exact-purges without another browser request. A fresh worker
first retries the exact purge so verified cleanup survives SQL failure or restart.
The request returns **204** only after its own `PurgeDeleted` commits. Batch
`PurgeDue` skips permanent roots; worker retention uses the exact-target method.
A missing target returns **404**, not a fabricated success. After an authorized
accepted request, polling that target to 404 confirms removal; arbitrary absence
is not a new physical completion receipt. No independent completion record or
Bot payload remains after purge.

New drafts persist a provisioning-tracked marker. A durable allocation-attempt
marker must commit before backend allocation; the root sequence CAS serializes
it against deletion. An unbound draft is immediately purgeable only if no
allocation was ever attempted. Legacy or ambiguous unbound allocation attempts
remain blocked rather than treating a missing registry entry as proof of absent
storage. Unsupported backends and mixed-attribution child sessions likewise
remain blocked, not falsely complete.

The purge transaction removes exact Bot-attributed sessions, their memory rows
(and database-maintained retrieval indexes), events/cache diagnostics, owned
trigger jobs, approvals/proposals, runs, tracked items, goals, versions, outbox,
Bot-local skill revisions and skill roots, and finally the root (including
documents, cursors and cleanup metadata). Skill deletion uses the exact Bot ID
inside the same transaction; another Bot's skills and the shared skill library
are not swept. The session purge follows persisted parent edges through
exclusively owned descendants, but rejects
explicit foreign Bot/user attribution. Shared credentials, owner-private unfinished intake
records with no Bot ownership link, published copies, external effects, backups,
and required audit/security records are not exclusively Bot-owned and are not
swept. No backend implementation or shared PVC is deleted by this API.

`TestBotPermanentDeleteHTTP` exercises both scopes through a real HTTP server,
scoped SQLite stores and the production cleanup composition with a physical
temporary-directory backend: create, prepare home, enqueue run, create file,
reject false confirmation/reason/stale revision/non-owner, request permanent
delete, inject cleanup failure, restart worker polling without a client retry,
verify home absent and GET/POST 404. Fresh never-provisioned deletion returns 204.
Another Bot's home/file remains. This is not Docker/Kubernetes live proof.
`TestBotPermanentScopedRowsAndRollback` injects failure at root deletion, proves
transaction rollback restores children, then verifies zero target rows and
retrieval matches plus unchanged unrelated rows after success. Existing cleanup
barrier tests and `TestBotPermanentVerifiedPurge` cover active-run fencing and
service-attested cleanup separately.

`TestBotSkillRecoveryHTTPAcceptance` additionally restores mapped skill-bearing
archives into independent SQLite deployments in both Personal and Team scopes,
reopens twice, checks immutable history and scope isolation, requests exact
permanent deletion over HTTP (202), supplies a controlled service-only cleanup
attestation, and retries to committed purge (204). After reopen, the target's
skill route is 404 while another Bot's skill and the original source deployment
remain readable. A fresh logical export contains only the other Bot's skill root
and revision; the original archive still contains both Bots' history. This
attestation proves the database/HTTP deletion contract,
not physical-home cleanup; it does not replace the live backend gates above.
