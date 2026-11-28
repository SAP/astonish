# File-first restore recovery

File-first restore currently requires a clean SQLite target and a Docker backend
with journaled fresh-Home allocation and exact deletion. PostgreSQL, Kubernetes,
and OpenShell restore are not implied by this recovery path. Ordinary non-Bot
logical restore is unchanged.

The format-3 recovery acceptance uses disposable personal/team SQLite scopes in
`TestBotDatabaseBackupRestore`: real platform export, offline verification,
canceled/interrupted restore cleanup and retry, paused authority-neutral restore,
mixed legacy run records, exact binary artifact readback and inline initial-state
reload. `TestBotDatabaseArtifactClosure` exports, validates and restores saved
report/candidate artifacts and checks exact bytes; reference-shaped tool arguments
are ignored, while corrupt inline records and corrupt reachable bytes fail closed.
These tests are not proof of live PostgreSQL recovery or Docker CLI acceptance.

## Database runtime records

File-first archives retain format-3 Bot runtime records in SQL; no runtime file is
invented or copied. Offline verification validates their typed union, exact inline
size/digest, scoped identity and logical codec. Named run report/candidate and
report/publication activity artifact references join the same physical-byte closure
as format-2 retained files. Opaque checkpoints, tool arguments and source paths
are never searched for authority. SQL-only Bot backup remains unsupported because
workspace/configuration/artifact bytes still require the file-first protocol.

Restore maps only typed runtime scope and artifact edges, recomputes inline and
logical digests, scrubs run admission/worker/lease fields even without mappings,
and retains the paused/reconciliation hold. Older binaries cannot read format 3;
rollback needs a compatible archive or dual-reader binary. No legacy evidence or
checkpoint files are reclaimed as part of this representation change.

## Cold Docker export

Export may retain the exact stopped Home session rather than require its removal.
This is limited to a backend's trusted Home/session binding and matching persisted
launch recipe, exited physical container identity, and exact Home/private mounts.
The archive holds the same storage admission lock used by session startup through
capture and helper cleanup; it does not acquire lifecycle locks inside SQL, start
compute, or detach anything. Running, paused, foreign, and unbound attachments
remain rejected. Restore still requires a fresh detached destination.

`TestCompiledHomeFilesNoPythonDocker` exercises stopped export of exact binary
bytes while a real `StartSession` attempt is blocked by admission. It then checks
that the original physical container remains stopped. Detached restore/reexport
and original-session recovery are separate assertions in the same fixture.
`TestBotFileFirstStoppedBackupCLI` additionally builds and invokes the public CLI
against isolated source/target roots: backup preserves stopped Chat compute,
restore allocates fresh storage, failed allocation cleanup preserves the source,
and reopened target HTTP/snapshot reads return exact bytes. Target reads use a
target-local backend, not the source registry/recipe. The original detached-source
CLI test remains separate. Both run personal and team SQLite/Docker scopes, not
PostgreSQL or cross-backend platform backup acceptance.

## Cleanup and retry

If restore fails or its process is interrupted:

1. Keep the original archive and the configured target data directory, platform
   DSN, and Docker endpoint unchanged. Stop the target daemon and all other target
   writers; do not start the target while a restore is incomplete.
2. Preserve `<data-dir>/.filefirst-restore/journal.json`. It contains the archive
   SHA-256, source scopes, explicit mappings, exact source/destination Home
   bindings, allocation results, phase, and any reported failure. It is private
   operator metadata, not an agent document. Do not edit or delete it manually.
3. Run `astonish platform restore cleanup <same-archive> --confirm` using the same
   configuration. Cleanup uses the journal's identities, not new mapping flags.
   It verifies the archive's exact bytes; encrypted archives do not need to be
   decrypted for cleanup. It never deletes the archive or resets platform SQL.
4. If cleanup reports an attached Home or ownership conflict, leave the journal
   intact. Resolve only known test/restore-owned attachments through normal
   lifecycle commands, then repeat cleanup. Unknown attachments are never forcibly
   detached. A populated platform or committed restore is refused, not wiped.
5. After successful cleanup, repeat the original restore command with its original
   mappings and, for an encrypted archive, its passphrase. This allocates new
   destination generations; this workflow does not resume half-imported SQL.

Cleanup is explicit and destructive only to the journal-owned failed attempt.
No automatic recovery, source-Home adoption, legacy reservation adoption, or
`--reset-target` is used. A missing/corrupt journal or foreign marker is a refusal,
not evidence that data is disposable. The command's confirmation is mandatory.

## Durability and visibility

Restore serializes with cleanup using a persistent target-root lock inode. The
0700 journal directory and 0600 atomic, synced journal record are bound to the
canonical target root. The backend must call the durable reservation callback
**before** any allocation, including a Docker request whose response may be lost.
The returned binding must exactly match that reservation. Both ordinary Home and
private workspace cleanup use the backend's existing scope/ownership/attachment
checks.

Phases are `prepared`, `files-ready`, `sql-importing`, `committed`, and `failed`;
`cleaned` records completed removals before the reservation is released. Each
scope imports transactionally into a private SQL tree first. All tenant imports
must succeed before tenant files are published, and a final empty-target platform
transaction establishes visibility. A failure in the second tenant does not
publish the first tenant as a restored platform. If the process dies between the
platform commit and receipt write, the nonempty-platform guard blocks destructive
cleanup even with an older journal phase. Do not use cleanup to undo a committed
restore.

An interrupted cleanup retains repeatable exact Home deletions. Owned tenant
files are moved back under the private journal before recursive deletion so a
partially removed ownership marker cannot strand a public tree. If no content
reservation was created (or it was already removed), cleanup requires the content
root to be empty and cannot erase unrelated later files. Root-relative operations
reject symlink escapes. These guards do not replace the requirement to stop other
writers: a journal is not a distributed lock against arbitrary filesystem tools.

## Evidence

`TestBackupFileFirstRecoveryInterruptions` covers second-Home allocation failure,
backend readback mismatch, attachment refusal/retry, cleanup after reservation
removal, source archive preservation, exact destination deletion, and successful
retry. `TestBackupFileFirstRecoveryPartialSQL` injects a later personal database
failure after an earlier team database import and checks no platform visibility,
cleanup, and retry. Guard tests cover wrong archive/DSN, populated platform, foreign
reservation, concurrent operation, and unreserved foreign content. The Docker
reservation test proves a failed journal callback prevents allocation. These are
isolated tests with backend doubles; they are not physical Docker recovery or
power-loss evidence.
