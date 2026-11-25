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

## Employee delivery and retained context

Ordinary information results are delivered directly in the committed conversation,
not forced into Home files. Explicit owner-requested artifacts and already-pinned
file-delivery contracts retain immutable saved-byte/publication requirements; prose
length and the word report do not create a file contract. This changes no Home
provisioning, retention or deletion rule. Initial title refinement is accepted once
before execution; scheduled occurrences use the same shared employee loop without
interactive refinement or recurrence upsells. Later owner turns supply their latest
objective while retained Home context remains supporting context, not replacement
authority. Reading Home or replaying the conversation never triggers execution.

## File-first continuation bootstrap

Both autonomous and interactive Bot factories load the actual critical Home files
and compact `MEMORY.md`. They do not scan `ListRuns`, hydrate historical report
work, or open old transcripts to manufacture a provenance prompt. Older documents
are retrieved with explicit file reads using the memory index; a summary or URL
alone cannot establish a research method or successful external action. Optional
historical content therefore cannot expire a short lookup deadline and abort an
otherwise healthy continuation.

This is not a relaxation of current-run recovery: the exact lease-bound
`BotWorkStore.LoadWork`, current transcript, pinned Task, authorization and
checkpoint validation remain mandatory. Their corruption, cancellation and
security errors still propagate. Recurring-run status lookup and approval/schedule
continuation checks remain independently bounded and fail closed.

## Additive retained-file access

The optional `BotHomeFilesBackend` extends access to an **existing registered
Home**, not its lifecycle. Ordinary file listing/stat/streaming and atomic writes
operate on mutable working Home data. Immutable content revisions and published
artifact snapshots use protected private storage, separate from agent mounts and
from working paths. A snapshot receipt identifies copied, hashed bytes; it is not
an atomic snapshot of the whole Home while shell writers are active. SQL reference
metadata is not file content, proof of an external effect or permission to execute.

`BotHomeManager` resolves scope-bound registered storage for these operations;
reads and listings do not call provisioning or require a running model/session.
The old bounded `ReadBotHomeArtifact` capability remains independent, and its
16 MiB transport ceiling does not define the streaming contract. The private
Task workspace listing must never be substituted for an ordinary Home file tree.

Session destruction still leaves the independently retained Home intact. The
next session uses the same existing binding and remount path. This foundation
changes no OverlayFS composition, layer ownership, allocation, restore window,
teardown barrier, cleanup reservation or permanent-deletion authorization below.
New file operations neither retire storage nor silently remove orphan objects.
Working files and immutable Bot-owned objects require the same explicit,
ownership-bound retention cleanup; team-published template copies are not
source-Bot-owned cleanup targets.

**Pre-Home content remains a separate, unfinished acceptance boundary.** Creation
before provisioning needs explicitly configured durable scope-bound staging and
verified binding/copy into retained storage before execution. Team templates need
team-owned durable storage. The additive backend interface does not implement or
prove that lifecycle by itself. No host temporary directory, database body,
legacy-content migration or dual-write fallback is introduced by this foundation.
Abandoned staged objects require explicit ownership-bound cleanup, not a GET side
effect or broad storage sweep.

Docker and direct Kubernetes provide the optional retained-file contract, subject
to their separate physical acceptance gates. OpenShell deliberately lacks it and
must fail file-first capability admission rather than degrade to session files.
Ordinary OpenShell sessions and bounded artifact reads remain unchanged;
`DeleteBotHome` still returns `ErrBotHomeUnsupported` because the gateway cannot
prove complete attachment exclusion and existing-only storage access. Unsupported
cleanup remains pending and retains data; it is never a successful purge receipt.
See [sandbox backend capabilities](sandbox-backends.md#additive-retained-file-capability-file-first-foundation)
for the contract and OpenShell capability evidence tests.

## Internal immutable-content archive foundation

`backup_bot_files.go` and `backup_bot_files_archive.go` now provide an **internal
file-only** archive path, `exportBotFileContent`, selected with
`botFileBackupExportOptions`. It is not a platform backup and does not bypass the
public `validateBotArchiveRow` / restore rejection gate. No CLI success is enabled.

`collectBotBackupRoots` uses the existing scoped SQL adapters and the explicit
`homeRecordEnvelope` allowlist. It also collects the root memory catalogue and
session `bot_initial_state` envelopes. Independently owned team templates and
unfinished intake use `rootFromIndependent` with their own exact scope. Trusted
callers must supply the complete scope allowlist and collect all selected roots
inside the same writer/attachment fence; this helper does not establish that
production fence or select all tenant templates/intakes on their behalf.

Inventory follows only typed `BotArtifact.ContentRef` edges in run reports, work
candidates, report outcome activities and publication receipts. Approval bodies,
session events and memory catalogues have explicit decoders; arbitrary action
arguments, checkpoint JSON, transcript text, source paths, mandate and workspace
authority references are never interpreted as object addresses. Future persisted
content-bearing DTOs require explicit inventory updates, not recursive JSON search.

Each immutable source is streamed into a private temporary spool, checked for
exact size/EOF/SHA256 and scope/kind/revision/key, then written through
`backup.Writer.AddFileSized`. Raw artifacts have no 16 MiB transport ceiling.
Typed containers have a separate 64 MiB decoding limit; the inventory is limited
to 100,000 objects and its serialized index to the archive metadata limit. The
index holds references/codecs only, never content bodies. Cancellation closes
and joins the owned source; any error discards this operation's temporary archive
and leaves the prior destination intact. No source or SQL writes occur.

`validateBotFileContentArchive` independently checks checksums, exact supplied
SQL roots, allowed scopes, reachable typed closure, duplicate/conflicting refs,
missing/extra objects and size/digest agreement. It does not extract, provision,
import SQL or authorize execution. The caller must provide independently validated
SQL roots rather than trusting the index's root declarations. Immutable byte
integrity is not a substitute for logical SQL identity/authority validation.

`TestBotFileInventory*` and `TestBotFileArchive*` cover scoped SQLite roots,
separate template/intake ownership, real local staging reads, retained-stream
fixtures, an artifact larger than 18 MiB, exact bytes, bounded transport reads,
missing/corrupt/short/oversized/foreign objects, altered root sets, archive tampering,
error cleanup and unchanged SQL content envelopes. This is not live Docker,
Kubernetes, PostgreSQL or complete platform restore evidence.

Remaining integration: full tenant SQL-row/independent-root collection and logical
identity validation; production paused/quiescent writer and attachment barriers;
mutable ordinary Home and private Task workspace snapshots; retained-Home and
cross-scope typed reference rewriting/installation before SQL commit; paused/review
reauthorization; public CLI export/restore acceptance. Public Bot backup remains
unsupported until that complete recovery protocol is implemented and proven.

### Verified same-scope staging restore

`restoreBotFileContent` (`backup_bot_files_restore.go`) is an internal usable
restore component for archives whose complete closure uses `staged-v1` keys.
`botFileBackupRestoreOptions` takes independently validated SQL roots, exact
allowed scopes, an explicitly supplied private spool directory, and authorized
`*botDiskContent` targets in a fresh control-plane root. The target scope must
exactly match each archived reference, and its hashed namespace must not already
exist (even empty). No destination path is accepted from the archive.

The helper snapshots the input archive by bounded streaming into an operation-owned
private directory, then validates the entire snapshot before reserving any target
namespace. All subsequent `Reader.ForEachFile` passes use that same private copy;
replacing the original archive between validation and installation cannot alter the
installed bytes. Each object is streamed into a private file, fsynced, rehashed,
atomically hard-linked without overwrite, and reopened/read through the production
scoped repository. Only after all objects pass readback does the helper return the
complete `map[BotContentRef]BotContentRef`, including nested artifacts. The map is
an **identity map**: original scopes, keys, parent bytes, digests and typed nested
references remain exact. It does not pretend that unchanged parent bytes support
physical ID or owner/team/Bot remapping. Retained `content-v1` keys and mismatched
target scopes are rejected before destination writes; converting them requires a
separate child-first typed reconstruction and new parent hashes.

There are no SQL changes, source removals, implicit provisioning or execution
reauthorization. Destination namespaces are reserved exclusively. An IO,
cancellation or authorization error during installation returns no mapping; it can
leave verified unselected objects in the operation's reserved destination. The
caller owns cleanup/recovery of that explicitly supplied staging root and must
never commit SQL selecting a failed restore. This is not a multi-scope atomic SQL
transaction or a complete Home recovery protocol. Private archive spools are
removed on all returns; pre-validation failures leave destination namespaces
untouched. Existing destinations and original source bytes are never overwritten.

`TestBotFileRestoreDiskRoundtrip` writes real source staging objects, exports,
closes and removes only the test-owned source root, restores to a fresh root,
closes/reopens the target and reads every root and nested artifact, including a
binary larger than 16 MiB. `TestBotFileRestoreRejectsBeforeDestinationWrites` and
`TestBotFileArchiveStrictClosure` exercise corruption, duplicate/incomplete archives,
foreign scopes, wrong targets, old archives, typed schema/closure errors, cancellation,
retained-key rejection and unchanged destinations. This is local disk evidence,
not public CLI, Home, Docker, Kubernetes or PostgreSQL restore evidence.

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

## Exact-owned control-plane staging purge

Both scoped SQL `PurgeBot` adapters now remove the selected Bot's control-plane
staging namespace before deleting its SQL root. This happens only after root
revision/sequence/fence CAS, deleted status, elapsed retention, cleanup completion,
and no-run/no-lease checks, plus exact session and scheduler ownership checks.
An explicitly confirmed, never-provisioned permanent draft uses the existing
unprovisioned attestation; attempted or ambiguous provisioning still blocks purge.

The staging scope is derived from the transaction's Bot row, not a viewer's active
team or a content reference. Personal staging retains its stable org/user namespace.
Purge bypasses retained-Home selection because physical Home deletion has already
completed; it neither opens/provisions Home nor reenters the outer SQLite client.
Only the exact flat namespace is enumerated, in bounded batches, including orphan
immutable revisions and interrupted `.stage-*` files. Links are unlinked without
following their targets, unexpected subdirectories fail closed, and independent
team templates, intake namespaces and other Bots are not enumerated or removed.

Directory removal is fsynced before SQL commit. A disk error rolls back SQL; a SQL
failure after disk removal leaves the irreversible tombstone and cleanup evidence
available for metadata-only retry. A missing namespace is idempotent, but a missing
configured storage root is an error, never proof of successful cleanup. This is
explicit final-retirement cleanup, not reference-aware orphan GC for live Bots.
Live/recoverable **deletion** remains blocked. The explicit internal
`CollectBotStagingOrphans` maintenance entry point now performs an owner-authorized,
root-UPDATE-fenced preflight with a minimum 24-hour grace. It validates the typed
Home-selection SQL envelopes and their referenced bytes, then inventories only
that Bot's flat staging namespace in bounded batches. It reports referenced,
young, in-flight `.stage-*`, and **old unclassified** counts, never paths or a
claim that old files are orphans. Unknown layouts, links, malformed references,
and corrupt objects fail closed. The root update is rolled back and all files
are retained; a completed preflight returns `ErrBotUnsupported`, not a successful
GC receipt.

The direct SQL inventory is not complete transitive reference closure: session
metadata/memory and references nested inside immutable run/activity objects need
explicit typed enumeration. Additionally, `BotContentStaging.Put` can publish
outside the root transaction; grace age alone cannot exclude a delayed writer.
No deletion or automatic setup hook is enabled until both guarantees are proven.
Reads and GETs never collect objects. `TestBotStagingGC*` covers both SQLite scopes,
old referenced bytes, rolled-back immutable publications, young and in-flight
files, namespace isolation, unknown-layout refusal, and unchanged root counters.

`TestBotPermanentDraftPurgesStaging`, `TestBotStagingPurgeFailureRetry`, and
`TestBotRetentionPurgesStagingAfterCleanup` exercise both SQLite scopes, namespace
isolation, interrupted files, disk failure, post-purge SQL rollback and retry.
These tests prove local staging/SQL behavior, not live backend physical deletion.

## Recovered interrupted-run teardown

`RecoverRun` revokes an expired lease and clears the logical active-run pointer;
this is not physical teardown evidence. The ordinary worker's next poll observes
`RunInterrupted` and verifies destruction of that run's exact persisted session
before `ConfirmCleanup` can append a `cleanup-confirmed` receipt. No new public
interface or owner-controlled cleanup endpoint is introduced.

The store accepts the pointer-free recovery path only with the private recovered
commit marker, matching recovery report activity, exact run/fence, nonempty
session identity, empty active-run and lease fields, and the immediate successor
Bot fence. The existing exact Home, revision, service scope and tenant checks run
both before and after the external verifier. A newer worker claim, pause, delete
or restore invalidates an in-flight receipt. Root CAS includes empty-pointer and
lease predicates; cleanup cannot erase newer execution. Audit failure rolls back
the receipt, and a durable exact replay does not call the verifier again.

This receipt never resumes the interrupted run, settles its dispatch ledger,
changes its report/billing, or authorizes Home retirement. Later scheduled
occurrences still need independently safe dispatch evidence; pending unknown tool
writes remain held even after confirmed teardown. `TestBotRecoveredCleanup*`
covers both SQLite scopes and races; `TestBotRecoveredScheduleCleanupHTTP` drives
production worker recovery and repeat polling, scheduler admission and HTTP run
reads for pending models, settled tools, unknown writes and absent/foreign
receipts. Physical destruction is fixture-attested in these tests, not live
Docker or an OS-process restart evidence.

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
file-first archives into independent SQLite deployments in both Personal and Team
scopes. Both source Bots are settled and paused before export; test-owned source
Home and staging files are removed before restore to exclude lazy fallback. The
test reopens twice, checks immutable heads, history, run-pinned checkpoints and
scope isolation, and requires paused Bots and cleared run leases even with the
transient/scheduler restore options enabled. It requests exact permanent deletion
over HTTP (202), supplies a controlled service-only cleanup attestation, and retries
to committed purge (204). After reopen the target's skill route is 404 while the
other Bot remains readable. A fresh file-first export contains only the other
Bot's skill root and revision; the original verified archive retains both Bots'
history. This proves local disk/SQLite/HTTP recovery, not physical Docker or
Kubernetes cleanup, PostgreSQL, or public CLI acceptance.

Mapping is child-first and typed: only content scope identities and explicit
artifact edges change; parent objects are rehashed and retained parents are
republished before SQL references select them. Skill provenance, review/action
content and checkpoint bytes are not recursively rewritten. Independent template
and intake objects must match the exact source tenant/owner namespace; an unknown
Bot scope cannot become an independently owned object. Restored run records clear
mandate/caller, admission, continuation/quota and live lease authority, then bind
the readable history with a recomputed logical digest. The root coordination
allowlist accepts `restore_reconciliation_required` only as boolean `true`, allowing
post-purge re-export without accepting a false reauthorization assertion. Restore
continues to pause Bots, fence leases, suppress undelivered outbox replay and pause
Bot schedules; the marker does not itself authorize execution.
