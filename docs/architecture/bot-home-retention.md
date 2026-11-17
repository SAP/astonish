# Bot-home cleanup and permanent deletion in the daemon

## Retained Task workspace

Docker's optional `BotHomeWorkspaceBackend` uses the exact registered durable
Home volume, not daemon HOME or an ephemeral session workspace. Immutable Task
bundles survive helper-container teardown; state and outputs remain separate.
Workspace publication never advances Bot definition version. Canonical executable
reads select the committed Task registry revision and verify retained bytes.
Schedule removal does not delete Task files. Existing retention cleanup remains
the only permanent Home deletion path. See [Task workspace](bot-task-workspace.md)
for physical Docker acceptance and unsupported-backend boundaries.

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

## Exact-owned execution reconciliation

Before raw storage deletion, `BotHomeManager.Delete` invokes the optional
`BotHomeSessionCleanupBackend`. Docker preflights all Home attachments against
canonical session names, immutable container IDs, the exact recovery Home binding,
owner and tenant, the scoped session registry, and the four expected mounts.
Labels alone never authorize deletion. Unknown, forged or foreign attachments
retain storage and prevent a completion receipt. Session locks serialize this
path with create/start/destroy; immutable IDs prevent name-replacement deletion.

The manager rechecks the value-copied authorization binding, and retention checks
that repeated reservations preserve the original claim/fence. The backend calls
this recheck immediately before each lifecycle teardown. It removes the session
container and non-forced overlay volume, then marks its exact-binding recovery
sidecar `cleanup_pending` and removes private payloads before the registry row.
The sidecar is removed last: registry errors and interruptions before/after the
registry commit remain discoverable on restart. A receipt without a registry row
permits only final local cleanup after proving both container and overlay absent;
it cannot authorize Docker destruction or session recreation. Failed overlay
deletion retains recovery and registry evidence for restart.
`TestBotHomeCleanupExactOwnershipAndRetry` covers both registry interruption
windows, fresh-store retry, and preservation of replacement containers/overlays.
Exact-binding recovery sidecars permit retry after the container already vanished.
Production teardown failures are logged rather than discarded. No pool cache is
required to recover an abandoned session.

Raw `DeleteBotHome` remains non-forcing. A missing Home still triggers validated
private-workspace removal, never Home recreation. Docker reference checks and a
final authorization check protect the subsequent storage deletion.

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

## Current deletion acceptance

`TestBotPermanentDeleteAttachedDockerAcceptance` now uses real scoped SQLite
stores and production HTTP routes rather than a retention-store fake. It creates
an attached running Docker session, Home, overlay and private workspace, accepts
permanent deletion (202), observes ordinary content 410 and explicit status 202,
and rejects a forged foreign attachment without touching either container. After
fixture removal of only that forged attachment, fresh backend/registry/worker
objects resume ordinary polling. The test independently checks container, overlay,
workspace, Home, registry and private recovery directory absence; exact SQL Bot,
session/history, Task/revision and scheduler-job absence; final HTTP 404; and an
unchanged unrelated active scheduler sentinel. This is controller/registry
replacement in one test process, not an OS daemon-process restart.

`TestBotPermanentDeleteHTTP/personal` and `/team` additionally inject physical
cleanup and post-verification SQL-purge failures, then resume with a fresh worker
without a backend or further client retry. Both assert owned Task/revision,
session/history/memory and scheduled-job removal and preserve another Bot's
Task/revision, history/memory, active job and file.

`TestBotPermanentDeleteBrowserAcceptance` serves the built Studio client against
production HTTP and SQLite, with a controlled adapter that deletes real disposable
Docker volumes. It checks cancel/no reason, accepted 202, content 410, explicit
pending status 202, observer closure, worker cleanup, final 404, SQL/history/FTS
absence and preservation of another Bot/file/shared credential. Its adapter does
not stand in for the attached production-Docker-backend proof above.

SQLite row deletion makes pages reusable; file size need not shrink. This path
performs no automatic global `VACUUM`, secure media erasure or backup deletion.
Existing production Bots and the normal daemon are outside these fixtures.

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
