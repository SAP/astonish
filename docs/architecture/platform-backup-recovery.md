# Platform Backup and Recovery

## Overview

Astonish platform backups capture control-plane data so an operator can recover a deployment or port data between locations. The backup system is intentionally backend-aware but not backend-locked: logical archives are designed to move data between SQLite and PostgreSQL deployments, while later physical backups can optimize same-backend disaster recovery.

The design follows the existing tenant topology:

- PostgreSQL: a platform database, one database per organization, and team/personal schemas inside each org database.
- SQLite: `platform.db`, `orgs/{org}/org.db`, `orgs/{org}/teams/{team}.db`, and `orgs/{org}/personal/{user}.db`.

All logical export/import code must go through `pkg/store/entstore`. Backup code may define archive primitives in `pkg/backup`, but it must not open raw Ent clients or bypass the tenant router.

## Archive Format

The archive is a single user-facing `.astonish-backup` file. Internally it is a tar-compatible multi-file logical archive, gzip-compressed by default. Readers detect gzip by magic bytes and continue to accept uncompressed tar archives for compatibility. The archive contains these reserved metadata entries:

```text
manifest.json
checksums.json
platform/*.jsonl
orgs/{org_slug}/org/*.jsonl
orgs/{org_slug}/teams/{team_slug}/*.jsonl
orgs/{org_slug}/personal/{user_id}/*.jsonl
```

`manifest.json` identifies the archive:

- `format`: always `astonish.backup`.
- `formatVersion`: archive format version, starting at `1`; this versions the tar/gzip layout and manifest semantics independently from database schema versions.
- `createdAt`: UTC timestamp.
- `backend`: source backend, for example `sqlite` or `postgres`.
- `mode`: `logical` for portable entity JSONL.
- `compression`: `gzip` by default, or `none` for explicitly uncompressed archives.
- `scopes`: exported platform/org/team/personal scopes.
- `schemaVersions`: applied migration state per scope when available.
- `entries`: files that belong to the archive payload.

`checksums.json` contains SHA-256 digests and sizes for every non-metadata logical payload file. Checksums are calculated over the uncompressed payload bytes so integrity semantics do not depend on the archive compression wrapper. `backup.Verify` rejects missing files, checksum mismatches, duplicate tar entries, unchecked payload files, unsafe archive paths, and malformed compressed streams.

When `--passphrase` is used, Astonish first creates the complete tar/gzip archive and then encrypts that entire byte stream. This means `manifest.json`, `checksums.json`, and all payload files are inside the AES-256-GCM ciphertext. The encryption metadata stores a versioned cipher/KDF description, including Argon2id memory, time, thread, and key-length parameters, and is passed as AES-GCM additional authenticated data.

## Scope Model

Backups are scoped to one of four boundaries:

| Scope | Required identifiers | Intended use |
| --- | --- | --- |
| `platform` | none | Full control-plane metadata and all selected tenant scopes. |
| `org` | `orgSlug` | Move or recover one organization. |
| `team` | `orgSlug`, `teamSlug` | Move or recover one team workspace. |
| `personal` | `orgSlug`, `userId` | Move or recover one user's private workspace. |

The scope identifiers in the manifest are security-relevant. Exporters must resolve them through the tenant router and must not widen a narrower request. A team export cannot include another team, and a personal export cannot include team data unless an explicit higher-level scope requested it.

## Credential Handling

Credential payloads are encrypted at rest and must remain encrypted in archives. Logical export must never serialize plaintext secrets, OAuth access tokens, refresh tokens, or provider keys. The default restore behavior preserves encrypted blobs only when the target has compatible key material.

Credentials use envelope encryption: each org has a `credential_key` row in `org_encryption_keys`, encrypted by the installation master key (`ASTONISH_MASTER_KEY` or `~/.config/astonish/.store_key`), and personal/team credential rows are encrypted with that org key. Restore planning inspects encrypted credential material before writing. If a backup contains credential rows and the current master key is missing or cannot decrypt the restored org credential key, restore reports a blocker instead of importing credentials that would appear in lists but fail at use time.

Portable non-secret exports use the explicit `--redact-secrets` option. Archive-level encryption is explicit via a passphrase and uses AES-256-GCM with an Argon2id-derived key. Archive passphrases protect the backup file in transit/storage; they do not replace the installation master key needed to decrypt restored credentials at runtime. Any future key export or re-key flow must be explicit, audited, and documented separately from normal backup creation.

## Consistency Guarantees

Logical backups are the portability path. Each source scope should be read in a consistent transaction where the backend supports it:

- SQLite logical export reads each database file through its normal connection with a read transaction.
- PostgreSQL logical export walks the platform database, each org database, and team/personal schemas through entstore-owned SQL connections.

A full PostgreSQL platform backup spans multiple databases, so there is no single cluster-wide snapshot in the first design. The archive should record per-scope snapshot times when logical export is implemented. Operators that require strict full-platform consistency should pause writes or run in a maintenance window until a platform-wide maintenance gate exists.

Physical backup mode is future work. It must use safe backend mechanisms such as SQLite's online backup API or PostgreSQL `pg_dump`; it must not copy live WAL-backed SQLite files naïvely.

## Bot recovery coverage (Phase 2 verification pending)

Logical restore orders Bot aggregate roots before versions/goals/runs, and those before tracked items/approvals/outbox records. This ordering is shared by the SQLite and PostgreSQL import code; schema-hash checks and per-scope foreign-key validation are unchanged.

Phase 2 also persists structured `MemoryAttribution` in existing memory metadata, reviewed memory replacement/source snapshots and `memory_review` receipts in the scoped activity/outbox records, and opaque `BotHomeBinding` records. No new physical-home archive format is implied: mapped documents are reconstructed from records, not separately backed-up files. `RegisterBotHomeBinding` is an internal verified registry/CAS operation, while local sandbox JSON and scoped Ent adapters serialize the binding. Reopen/CAS tests prove those service records survive reconstruction; the archive tests below establish their stated recovery cases, not a blanket acceptance of every newer reviewed-ingest receipt under scope mapping. Sandbox runtime registry rows retain the existing transient restore policy.

`TestRestorePlatformBackupSQLiteFreshTarget` creates personal/team Bots through the scoped router, exports a real archive, and restores into a fresh SQLite deployment. It verifies immutable versions, goals, tracked commitments, correction activity, home-session linkage, team templates and cross-scope denials. Soft-deleted Bots retain their original restore deadline and can restore to paused after archive import. Existing Chat/flow/app recovery and default-paused scheduler assertions remain in the same test.

The shared logical importer now invalidates Bot execution authority inside each scope transaction, even with `--include-transient` and `--enable-scheduled-jobs`:

- Active Bots restore **paused**, with an advanced revision. Draft, paused, stopped and deleted states stay unchanged.
- Bot/run/outbox fencing counters advance; worker leases are cleared. Existing active-run pointers remain teardown barriers, and `cleanup_complete` resets to false: restoring rows does not prove source containers stopped or home volumes recovered.
- Queued, running and approval-waiting runs become **interrupted**, with an explicit unverified-recovery note. Terminal outcomes, report evidence and checkpoints are retained. SQL status and embedded run/approval documents agree.
- Pending and approved exact-action decisions become **revoked**. Consumed, denied, expired and already-revoked history is preserved.
- Undelivered outbox rows are held at `9999-12-31T23:59:59Z`, with a reconciliation reason; this is a disabled-work sentinel, not a promised delivery date. Payloads, sequence numbers, deduplication keys, attempts and existing delivery receipts remain intact. No false delivered receipt is generated. Recovery tooling must explicitly reconcile these rows before making them eligible again; it is not implemented yet.
- Bot-trigger jobs always restore paused with no next occurrence. The general enable-jobs option still applies to ordinary schedules.

`TestBotRestoreQuiescenceDocuments` covers both text and object JSON representations, exact counter/cursor precision, preserved teardown barriers and receipts, and malformed documents/counters. `TestBotRestoreMalformedDocumentRollsBackScope` verifies that invalid Bot documents roll back the scope import. The archive round trip exercises all persisted run and approval states in both scopes, with default and explicit runtime restore options.

This coverage does **not** establish a complete operational recovery workflow. The immediate pre-Bot SQLite/PostgreSQL upgrades and Bot logical-recovery acceptance have been exercised; physical home recovery, source-worker shutdown and explicit reconciliation/resume tooling remain pending. Do not enable Bot workers against a restored archive until those recovery gates exist. Logical control-plane backups are not a backup of independently retained home volumes. The internal cleanup callback/receipt seam records trusted attestations only and does not perform or independently verify physical teardown.

The run archive fixture uses production `botJSONMap` envelopes and reads restored state through `botDecode`. Recovery updates the inner `bot_document` and retains its envelope, including exact integer values. `TestBotRestoreApprovalEnvelope` covers wrapped/unwrapped documents in text/object columns and rejects malformed envelopes. Earlier unwrapped-only fixtures did not prove production run-document recovery; the envelope fixture exposed and now covers that gap.

### Bot scope mapping

Bot rows do not use recursive string substitution. Only current root ownership, linked session ownership and viewer cursor keys are remapped using the source organization and destination scope. Bot IDs, names, charter/persona definitions, historical authorship, evidence, action arguments/digests, content hashes and opaque home storage keys are preserved. This keeps immutable reviewed documents intact rather than treating every matching string as a scope reference. A cursor collision resets the mapped viewer to unread. Ordinary non-Bot mapping remains unchanged.

`TestRestorePlatformBackupSQLiteMappedOrgAndTeam` also maps a personal user identity and verifies team/personal Bot reads, home linkage, unchanged reviewed definitions/goals, preserved read positions and rejection of the old principal. `TestBotRestoreMappingPreservesContent` covers legacy object documents and rejects owner mappings from unrelated organizations. Mapping a storage key does not relocate physical files; home recovery is still an operator/runtime reconciliation requirement.

### Bot-local skill recovery

`bot_skills` and `bot_skill_revisions` are durable personal/team Bot-owned rows, not global skill-library entries. Logical export includes both tables. Restore inserts skill roots before immutable revisions, validates row/document identity and revision digests, and rejects revisions whose root belongs to another Bot. Validation runs inside the existing scope transaction; a corrupt revision rolls back that scope. This does not add a platform-wide transaction.

Skill documents, source provenance, historical authorship, revision IDs/digests and run-pinned snapshots are immutable under org/team/user mapping. Only current ownership and routing references follow the existing Bot mapping rules. Execution quiescence remains unconditional and does not rewrite skill history: a recovered skill is not permission to resume a Bot or a worker lease.

The schema compatibility exception also recognizes the exact immediate **pre-skill Bot v1** schema in team/personal scopes: project out only `bot_skills` and `bot_skill_revisions` from the current target hash, retaining every other attribute checked by the backend's existing hash. A legacy archive containing either skill table is rejected; unrelated schema drift remains rejected. The pre-Bot projection excludes these newer tables as well. DDL migration version `2` is independent of the unchanged Bot document schema version `1`.

`TestBotSkillRestoreSQLitePreSkillArchive`, `TestBotSkillRestoreValidation` and `TestBotSkillRestoreMalformedRevisionRollsBackScope` cover exact compatibility, invalid digest/foreign-root rejection and rollback. `TestBotSkillRecoveryHTTPAcceptance` uses real loopback HTTP with production routes/tenant middleware and independent SQLite deployments in both scopes. It creates two skill revisions, maps an archive into a fresh deployment, reopens twice, reads exact head/history and pinned runs, checks paused/interrupted recovery, rejects old-owner/cross-Bot/cross-scope reads, then permanently deletes one Bot after a controlled cleanup attestation. Reopen and a fresh logical export confirm the target's skill rows are removed while another Bot's skill, the source archive and the source deployment remain intact. Authentication, readiness and physical cleanup are controlled fixtures; this is not a live identity-provider, model, daemon or volume-deletion proof.

PostgreSQL equivalents are `TestBotSkillPostgresPreSkillArchive` and `TestBotSkillPostgresRecovery`, including schema version, foreign provenance rejection, transaction rollback, concurrent work/root CAS, replay, archive restore and repeated reopen. They require an **isolated disposable**, admin-capable `ASTONISH_TEST_DSN`; the harness creates and drops generated databases. Explicit acceptance must run:

```sh
ASTONISH_REQUIRE_SKILL_POSTGRES=1 go test -race -tags integration ./pkg/store/entstore -run '^TestBotSkillPostgres' -count=1 -timeout=5m
```

The marker makes a missing DSN a failure. Ordinary integration selection without it may skip unavailable infrastructure; a skip is not PostgreSQL evidence. The recovery implementation's local verification had no disposable DSN, so these new PostgreSQL cases remain unverified against a running database.

### Immediate pre-Bot archive upgrades

SQLite and PostgreSQL team/personal archives from the immediate pre-Bot schema can restore into the current schema. On a normal schema-hash mismatch, each backend computes a pre-Bot projection of the target: it removes only the enumerated Bot tables (including the later skill tables), four session linkage/classification columns and the team sandbox home-binding column. All other attributes in that backend's schema hash remain checked. The archive must match that projected hash exactly and contain no Bot-table entries in that scope. Platform/org scopes do not use this exception; the general `checkSchemaCompatible` policy is unchanged. This is not arbitrary historical-version compatibility or unrestricted cross-backend schema conversion.

`TestBotMigrationReopen` and `TestRestorePlatformBackupSQLitePreBotArchive` cover SQLite migration/reopen, actual pre-Bot archive restore, retained ordinary Chat classification and default-paused jobs, with no invented Bots. `TestIntegrationBotPostgresPreBotMigration` reconstructs the actual immediate pre-Bot PostgreSQL schemas, reopens twice and checks narrow hash compatibility while rejecting unrelated drift, invalid versions and Bot-bearing legacy archives. `TestIntegrationBotPostgresPreBotBackupRestore` exports that legacy PostgreSQL archive, restores into a fresh deployment and reopens twice to check preserved ordinary rows, paused jobs and the new empty Bot schema.

### PostgreSQL Bot recovery evidence

`TestIntegrationBotPostgresBackupRecovery` uses the existing PostgreSQL backup harness with generated disposable platform/org database names. Through the scoped repositories it creates personal/team Bots, activates them with test-only readiness injection, claims a running invocation, queues another invocation, and persists pending/approved exact-action decisions. It exports and restores a real logical archive with both runtime-related restore options enabled. Assertions cover paused Bots, interrupted runs, revoked approvals with preserved action digests, rejected source-worker lease renewal, retained activity, scope isolation and no immediately eligible outbox entries.

On 2026-11-04, the latest parent live PostgreSQL run selected `^TestIntegration.*(Bot|MemoryAttribution)` with the race detector and passed in **58.512 seconds**. The selector covers these eleven top-level tests:

- `TestIntegrationBotPostgresBackupRecovery`
- `TestIntegrationBotPostgresPreBotMigration`
- `TestIntegrationBotPostgresPreBotBackupRestore`
- `TestIntegrationPostgresBotMemoryPrefilterBeforeTopK`
- `TestIntegrationPostgresBotServicesSpendRace`
- `TestIntegrationPostgresBotServicesSpendSettlementAndCaps`
- `TestIntegrationPostgresBotServicesCorrection`
- `TestIntegrationPostgresBotServicesCleanup`
- `TestIntegrationPostgresMemoryAttribution`
- `TestIntegrationPostgresBotHomeBindingRegistration`
- `TestIntegrationPostgresBotPausedStoppedConfiguration`

Reproduce with an isolated admin-capable `ASTONISH_TEST_DSN`: `go test -race -tags integration ./pkg/store/entstore -run '^TestIntegration.*(Bot|MemoryAttribution)' -count=1 -timeout=5m`. These tests require the explicit DSN rather than silently claiming skipped acceptance. In addition to recovery, spend, corrections and injected cleanup verification, this exercises structured attribution across scoped vector/FTS/hybrid retrieval, internal home-binding registration, and paused/stopped prose edits preserving lifecycle barriers and revoking obsolete approvals. The parent also reported passing full store/memory/bot/execution/api/sandbox suites, including SQLite reviewed-batch CAS/receipt/evidence tests and HTTP mapped ingest, health apply and finite persisted Bot streaming.

**Phase 2 remains verification pending** until the parent records phase verification. These checks do not perform purchases, provision or retire physical homes, stop live Bot workers, subscribe to live runner SSE or establish runtime usage accounting. Physical mount/delete callbacks, runtime/scheduler composition, source-worker shutdown and explicit operational reconciliation remain later-phase gates.

## Restore Safety

Restore is a fresh-target recovery workflow first. The target must be empty, or the restore command fails before writing. SQLite targets may opt into a destructive reset path with `--reset-target`; this closes the target store, removes the SQLite platform database, tenant database tree, and legacy file-backed runtime directories under the configured data directory, bootstraps a new empty platform schema, and then imports the archive. Merge-style restore into an existing active platform is future work because it requires explicit conflict policy for users, org slugs, team slugs, app IDs, sessions, scheduler jobs, and credentials.

Restore must validate before writing:

1. Archive format and version are supported.
2. Manifest and checksums verify.
3. Target schema versions are compatible with the archive.
4. Target is empty, unless SQLite destructive reset is explicitly requested.
5. Encrypted credential payloads are usable with the current installation master key, or the backup is intentionally redacted/non-recovery.

Credential validation is a preflight safety gate. It checks restored `org_encryption_keys.credential_key` rows and representative personal/team `credentials.encrypted` rows. Missing or wrong key material is a blocker with an operator-facing remediation: copy the source `~/.config/astonish/.store_key` or set the same `ASTONISH_MASTER_KEY` before restore. Legacy plaintext credential rows are allowed with a warning.

Each logical scope import is wrapped in a database transaction and the SQLite path runs foreign-key checks before commit. PostgreSQL full-platform restore is not one global transaction because it provisions and writes multiple databases and schemas; failures can leave already-committed earlier scopes in place. This is acceptable only for the first fresh-target recovery workflow and is why PostgreSQL destructive reset remains outside the tool.

The implemented restore command supports:

- `--dry-run`: plan only, no writes.
- `--confirm`: execute restore; required for writes.
- `--reset-target`: delete and recreate a non-empty SQLite target before restore.
- `--enable-scheduled-jobs`: keep scheduled jobs active; default restores them paused.
- `--include-transient`: include login/runtime transient state; default skips it.
- `--passphrase`: decrypt an encrypted archive for inspect, verify, or restore.
- `--map-org old:new`: restore an organization under a new slug.
- `--map-team oldorg/oldteam:neworg/newteam`: restore a team under a new org/team slug.
- `--map-user oldorg/olduser:neworg/newuser`: restore a personal scope under a new org/user ID.

Mapping rewrites manifest scopes at restore time, provisions the mapped target scopes, and rewrites common scope-carrying values in logical rows before insert. Organization mappings clear `organizations.db_name` so PostgreSQL targets use the destination deployment's derived database name. Team mappings rewrite `teams.slug` and `teams.schema_name` to the destination schema convention. User mappings rewrite common user reference columns and string values, but they are still a portability tool rather than a merge/conflict resolver.

Future flags can relax fresh-target behavior further:

- `--skip-existing`: leave target records untouched.
- `--overwrite`: replace updateable records while preserving append-only audit semantics.

Scheduled jobs restore paused by default to avoid duplicate automations after staging restores or migrations. Login sessions, device sessions, link codes, OAuth caches, and sandbox runtime sessions are transient/security state and are skipped by default.

## Current Implementation Slice

The current implementation provides the safe archive foundation, SQLite/PostgreSQL logical export, and fresh-target logical restore:

- `pkg/backup.Manifest` and validation.
- `pkg/backup.Writer` for gzip-compressed tar-compatible archive creation.
- `pkg/backup.RecordWriter` for logical JSONL payloads.
- `pkg/backup.Inspect` for manifest/checksum metadata.
- `pkg/backup.OpenReader` and `Reader.ForEachFile` for streaming tar payload traversal. Encrypted archives still decrypt to memory because AES-GCM requires authenticated plaintext before tar parsing.
- `pkg/backup.Verify` for streaming checksum validation.
- `astonish platform backup create --output <archive> [--compression gzip|none] [--passphrase <secret>]` for logical row export across platform, org, team, and personal databases.
- Scoped export flags: `--org`, `--team`, and `--user`.
- `astonish platform backup inspect` and `astonish platform backup verify`, with `--passphrase` for encrypted archives.
- `astonish platform restore <archive> --dry-run` for restore planning, including encrypted credential key preflight.
- `astonish platform restore <archive> --confirm` for SQLite or PostgreSQL fresh-target logical restore.
- `astonish platform restore <archive> --confirm --reset-target` for destructive SQLite target reset followed by restore.
- Restore-time scope mapping with `--map-org`, `--map-team`, and `--map-user`.
- PostgreSQL integration scaffolding in `pkg/store/entstore/backup_postgres_restore_integration_test.go`, gated by `-tags=integration` and `ASTONISH_TEST_DSN`.

For SQLite and PostgreSQL, `backup create` discovers platform/org/team/personal scopes through the tenant metadata and exports every user table as JSONL records. This includes durable rows for flows, apps, app state, fleets, drills, sessions, memories, settings, MCP servers, credentials, and scheduled jobs when those rows exist. It does not include physical `.db` files. Recovery backups preserve stored values by default, including password hashes and encrypted credential blobs, because those values are required for full system recovery. Use `--redact-secrets` only for portable/support exports that intentionally cannot restore protected values. PostgreSQL destructive reset is intentionally blocked; operators should restore into a fresh PostgreSQL platform database or reset databases externally with site-specific safeguards. App-created SQL schemas and merge-style restore remain planned follow-up work.

## References

- `docs/architecture/multi-tenant-platform.md` — PostgreSQL tenant topology and isolation guarantees.
- `docs/architecture/sqlite-backend.md` — SQLite file topology and backup implications.
- `pkg/store/entstore/AGENTS.md` — tenant router invariants.
- `docs/architecture/credentials.md` — encrypted credential storage and redaction rules.
