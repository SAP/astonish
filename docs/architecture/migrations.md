# Migration Management

## Unreleased Active Bot lifecycle (no migration)

Bots create as Active with independent disabled-by-default recurrence. This is the
current unreleased design, not a released Draft-to-Active upgrade. No lifecycle
data conversion, historical intent adapters or marker-6 bump is required. DDL
marker 5 and unrelated reconciliation infrastructure remain unchanged. Current
format backups retain safe paused restore; experimental development Bots may be
removed only through scoped Bot deletion, never shared DB or ordinary Chat removal.

## Employee-operation persistence acceptance — 2026-11-08

`TestIntegrationPostgresBotEmployeeOperations` uses the production router with actual PostgreSQL personal/team schemas, never SQLite under a PostgreSQL label. It requires the existing fail-closed disposable guard: loopback-only nondefault port, `postgres` database, `astonish_activation_final` bootstrap role, no TLS/fallback hosts, and explicit disposable consent. Run only against a newly owned temporary pgvector server; never use a production DSN or reset a user database. The suite exercises operation/occurrence transaction rollback, concurrent retries, router reopen, mandate revocation and deletion cleanup. Current concurrent schedule insertion failures are acceptance blockers, not a reason to regenerate schemas or remove constraints. No schema migration changes are made by this test addition.

## Current Ent workflow

The active store is `pkg/store/entstore`, using Ent schemas under
`ent/{platform,org,team,personal}/schema`. Generate clients and migration
metadata with `make ent-generate`; do not hand-edit generated clients.
The scoped router reconciles schemas when provisioning or opening a database.
PostgreSQL open paths preserve existing columns and extra indexes by skipping
ModifyColumn, DropIndex and DropColumn. SQLite reconciliation retains existing
rows and applies additive schema changes.

Bot schema version 1 adds personal/team Bot aggregates, immutable version
records, goals/tracked items, runs, approvals and outbox records; team scope
also adds templates. Existing sessions receive nullable Bot linkage plus
`purpose=chat` and `retention=standard` defaults. Existing scheduler modes remain
valid alongside `bot_trigger`; this does not enable Bot execution or convert jobs.

`pkg/store/entstore/bot_migrate.go` wraps team/personal reconciliation with a
`bot_schema_version` marker. A newer marker is rejected before Ent changes;
a failed reconciliation is never stamped. Both open and provisioning paths use
the wrapper. The marker is migration metadata, not an application data store.
Do not downgrade a deployment to an older binary that predates this guard.

Verification:

```sh
make ent-generate
go test ./pkg/store/entstore -run '^TestBot(Migration|Schema)' -count=1 -v
go test ./pkg/store/... ./pkg/memory/... -count=1
```

Current tests prove a simulated pre-Bot SQLite database can reopen, preserve
Chat/job rows, reject newer versions and avoid stamping a failed migration.
PostgreSQL upgrade/claim tests and old-backup compatibility remain required
before the Bot persistence phase is complete. Do not weaken backup hash checks.

## Bounded Bot continuation hydration

Bot-linked ADK session `Get` calls hydrate only the latest eight committed events,
in cursor order. Durable Bot work, checkpoints, mandates and current authority are
loaded through their existing stores; this window is conversational context only.
Ordinary Chat session loading is unchanged. Full Bot history remains durable and
pageable through `BotTranscriptReader`, so Studio restore and audit do not lose old
rows. This prevents a long-lived file-backed Home from making every new model turn
O(total transcript history).

## Bot transcript event identity (DDL marker 5)

`botMigrationVersion=5` adds nullable `session_events.event_id` in personal and
team scopes plus a unique `(session_id,event_id)` index. Bot-owned transcript
writers populate the deterministic event ID; ordinary Chat rows remain NULL and
keep their existing inline representation. Reconciliation backfills only the
small `bot_record_v2` SQL metadata envelope and never opens retained Home payloads.
Existing non-null `(session_id,event_id)` identities are reserved before backfill.
For duplicate legacy envelopes, the lowest row receives indexed identity and later
mirrored duplicates remain nullable/readable; migration never deletes transcript
rows or violates the unique index. Transcript append idempotency now uses one indexed lookup instead of decoding every
historical event while holding the Bot write fence. Existing event bodies, cursors,
file references, tenant routing and replay semantics remain unchanged.

Verification:

```sh
go test ./pkg/store/entstore -run '^TestBot(Transcript|Migration|ContentSessionPersistence)' -count=1
```

## Bot disk-content format (no DDL migration)

`botMigrationVersion` is **5**, tracking DDL only. The scoped disk codecs use
explicit `bot_record_v2` persisted DTOs in existing columns: SQL contains typed
references and coordination metadata; hydrated document, Task/Skill bundle, run,
approval, knowledge and transcript bodies are read from immutable disk content.
No Ent regeneration or table drop is required for this cutover. The logical
`store.BotSchemaVersion=1` is independent of both the DDL and storage formats.

`SelectBotHomeConfiguration` now selects all supported Bot-owned typed categories,
including the Task publication's named nested candidate, into the verified retained
Home in one fenced transaction. It validates copied bytes and preserves logical
digests. Templates and pre-Bot intakes remain independently scoped. Selection does
not delete staging; see [retained content selection](bot-retained-content-selection.md).

Inline development records are explicitly unsupported. There is no migration,
backfill, dual read, SQL body fallback or automatic deletion; schema reconciliation
still permits ordinary Chat to open the shared database. An old Bot requires
explicit development cleanup and new creation rather than implicit conversion.

The public backup gate rejects Bot-bearing legacy inline archives **and**
reference-only SQL archives until the file-first archive protocol is implemented.
Empty Bot tables and the DDL marker alone do not imply incompatible Bot content.
Historical schema-hash compatibility checks below do not override this content
format gate or prove new-format recovery. Generic Bot memory SQL/FTS/source-record
removal and compact file-backed MEMORY.md remain pending; these codecs alone do
not establish completed file-first acceptance. Existing Docker foundation evidence
does not replace dedicated PostgreSQL or physical Kubernetes cutover acceptance.

## Bot skill persistence (DDL marker 2)

Historical DDL marker 2 adds `bot_skills` and immutable `bot_skill_revisions`
in personal/team Ent scopes. DDL is deliberately independent of the logical
`store.BotSchemaVersion=1`; unchanged logical schemas do not make inline SQL
content compatible with the disk codec. No open, read or schema reconciliation
converts scheduler instructions or memory into skills. Explicit procedure import
from a readable pinned version is separate from database migration; see
[Skill inspection](bot-skill-inspection.md).

Skill worker mutations hold the Bot root lease fence, revalidate live membership,
and use work/head CAS plus content-bound idempotency. Orientation adds exactly one
skill snapshot before production and rehashes that contract without changing its
original assignment, charter, goals, attachments, or other work fields. Learning
is staged privately; staging does not advance the skill head. Lifecycle archive
and restore are explicit, while activation requires a validated accepted run.
Deletion purges skill revisions before their aggregate and before the Bot root.

Canonical provenance uses SHA-256 of full event text (or pinned attachment text),
exact event/session/run linkage and timestamp, and a quoted substring. Owner event
sources must match the canonical admitted user event and saved instructions, not
merely an event authored `user`. Goal sources use the canonical complete goal JSON
hash and exact current goal revision. Feedback-only learning binds both evidence
and candidate digests to that verified owner source; candidate learning hashes the
canonical `AvailableEvidenceRefs()` list for its evidence digest. This phase does
not make semantic assertions about whether a proposed method will work. Provenance
validation is not prose/credential redaction: the authenticated service must sanitize
source text before proposing it; storage preserves the content-bound immutable digest.

Verification: `go test ./pkg/store/entstore -run '^TestBot(Skill|Cleanup)' -count=1` uses
SQLite only. PostgreSQL migration/concurrency and runtime learning commit remain
separate acceptance requirements.

## Bot Task persistence (DDL marker 3)

Historical DDL marker 3 adds scoped personal/team `bot_tasks` and
`bot_task_revisions` through the current additive Ent reconciliation workflow.
The registry stores identity/lifecycle and head revision/digest, not another editable
copy of live TASK.md instructions. Current revision SQL contains a typed publication
record and a candidate disk reference, not archival bundle bytes. Publication state,
original authority ref and backend receipt ref remain coordination fields. Task
heads are independent from BotVersion and Skill heads. DDL reconciliation does not
rewrite historical content, backfill Tasks or convert existing instructions;
unchanged rows are not a legacy-content compatibility guarantee.

The schemas and generated migration metadata are produced by running
`go run generate.go` in `ent/personal` and `ent/team`. Their generate.go files are
ignore-tagged programs, not go:generate directives; a silent `go generate` exit is
not evidence that output changed. Repeat actual generation and compare output for
drift. Keep historical Atlas checksums unchanged; they are not this runtime's DDL.

`TestBotTaskMigrateV3` exercises marker 2 → 3 and idempotent empty Task reconciliation
in SQLite personal/team scopes, including exact historical v2 backup hashes with
Skills present and Tasks absent. The compatibility reconstruction removes only
known Task additions (and, separately, known Skill additions for v1), rejects
archives containing excluded tables, and rejects unrelated schema differences.
Existing legacy Chat/job preservation and future-marker/failed-reconciliation
tests remain required. PostgreSQL and Task
backup/restore/file recovery are not certified by these SQLite tests. See
[Bot Task workspace contracts](bot-task-workspace.md) for states, quotas and the
strict compatibility-only acceptance driver.

## Title-start/run-state compatibility

This change adds no DDL, generated Ent changes, or Bot schema-version bump.
Existing transcript JSON carries `bot_run_state` schema_version=1 state-only
envelopes. Old clients ignore unknown metadata; new clients ignore unknown
versions for rendering but advance replay cursors. Ordinary Chat remains unchanged.
No startup or GET backfills historical state or upgrades experimental Bot lifecycle data.
`start_from_title:true` is explicit create-only admission, not a migration of old
create keys. Imports and default Create return Active without starting work. Snapshot fields
`run_states` and `history_cursor` are additive read-only projections. PostgreSQL
requires a separate disposable-database run; SQLite proof is not PostgreSQL proof.

## Bot per-turn extraction persistence (DDL marker 4)

`botMigrationVersion=4` adds personal/team `bot_knowledge_extractions` and
`bot_knowledge_file_heads` using additive Ent reconciliation. Generate with
`go run generate.go` in each scope, without historical document version or Atlas
checksum changes. Full source tuples are unique. Current typed knowledge records
retain disk references for exact input and prepared decision content, not the old
inline `bot_document` body. New input requires a trusted complete-source verifier
plus a canonical terminal run and result activity.

Extraction claims are independent from terminal run leases. Each mutation locks
the Bot root, rechecks live editable membership/original actor and current root
fence. Begin persists one attempt ID before model use. Expired Extracting becomes
Held, never Pending. Prepared decisions may take fresh extraction fences for
recovery; uncertain Held without a decision may only abort. FilesReady/Committed
require trusted exact fenced byte verification. Scoped path heads CAS all original
expected digests atomically with the extraction receipt. Retained Markdown is inert.
Restore invalidates claims and holds unfinished records; root restore barriers block
claims until explicit reconciliation. Purge removes extraction journals and heads.
Marker-3 backup compatibility excludes only these two tables; historical v1/v2
reconstruction also excludes them without relaxing unrelated schema hashes.

SQLite checks: `go test ./pkg/store/entstore -run 'TestBotKnowledgeExtraction|TestBotMigration|TestBotTaskMigrate' -count=1`.
PostgreSQL concurrency, production source-verifier/worker wiring, actual physical
publication and paid-model execution are separate acceptance requirements; these
store tests do not prove completed learning.

## Personal mailbox configuration (additive settings column)

Personal mailbox persistence adds only nullable JSON `personal_settings.bot_email`
through the existing scoped Ent reconciliation. Its typed value is
`store.BotEmailSettings`: provider/address/endpoints/folder plus a credential name,
not a password or token. Credentials remain in the existing encrypted credential
store in the caller's own scope. Missing legacy values and explicit clears remain
nil (SQL NULL), with no platform/team mailbox inheritance. Provider/model/theme
preferences, row identities and the local-user UUID sentinel are unchanged.

Generate only personal scope with `cd ent/personal && go run generate.go`;
`go generate` is not the generator for this ignore-tagged program. Generated
migration metadata is the active additive migration; do not edit historical Atlas
SQL/checksums or backfill old rows. This settings addition does not change the Bot
aggregate DDL marker or Bot content format. No user database was migrated during
implementation.

`TestPersonalSettingsBotEmailLegacySQLiteUpgrade` creates a disposable old-schema
SQLite database with no mailbox column, reopens through scoped reconciliation,
checks preserved preferences and nil legacy mailboxes, then updates/reopens/clears
configuration and checks owner isolation and SQL NULL. The generated column
contract is checked separately without presenting it as a live PostgreSQL test.
Verification:

```sh
go test ./pkg/store/entstore -run 'Test(PersonalSettings|TeamSettingsBotEmail)' -count=1
go test -race ./pkg/store/entstore -run 'Test(PersonalSettings|TeamSettingsBotEmail)' -count=1
go test ./pkg/daemon -run '^TestBotConfiguredServiceAdmissionHTTP$' -count=1
```

The HTTP admission suite exercises actual configured loopback SMTP delivery via
shared adapters in personal Studio, interactive and scheduled exact-approval resume
modes, plus team Bot modes. It also covers A2A and drill admission. Controlled
models/SQLite/test-owned transports are not paid-provider, Docker/Kubernetes or
live PostgreSQL evidence. Live PostgreSQL validation is explicitly deferred by the
user; PostgreSQL Ent compilation and nullable JSON metadata remain checked.

## Historical Atlas workflow (not the active Ent implementation)

The remainder documents the former sqlitestore/pgstore workflow. Its paths and
commands are historical reference, not instructions for current Bot changes.

Astonish uses [Atlas](https://atlasgo.io/) as a dev-time schema diff tool to generate SQL migration files. At runtime, the existing `migrate()` functions in sqlitestore/pgstore apply migrations unchanged — Atlas is never imported into the binary.

## Architecture

```
schema/                         ← Desired state (source of truth)
├── platform/
│   ├── schema.pg.sql
│   └── schema.sqlite.sql
├── org/
│   ├── schema.pg.sql
│   └── schema.sqlite.sql
├── team/
│   ├── schema.pg.sql
│   └── schema.sqlite.sql
└── personal/
    ├── schema.pg.sql
    └── schema.sqlite.sql

atlas.hcl                       ← Atlas project config (8 environments)

pkg/store/pgstore/migrations/   ← Generated migration files (committed)
pkg/store/sqlitestore/migrations/
```

## Workflow: Adding a Schema Change

1. **Edit the schema file** (single source of truth):
   ```bash
   # e.g., add a new table to team scope (both dialects)
   vim schema/team/schema.pg.sql
   vim schema/team/schema.sqlite.sql
   ```

2. **Generate the migration** (Atlas diffs desired vs. current state):
   ```bash
   # PG migrations (primary use case — Atlas handles these cleanly)
   make migrate-diff ENV=team_pg NAME=add_notifications

   # SQLite migrations: write manually (see Limitations below)
   ```

3. **Review** the generated SQL in `pkg/store/pgstore/migrations/team/`

4. **Commit** both the schema change and the migration together.

## Environments

| Environment | Schema Source | Migration Dir | Dev URL |
|---|---|---|---|
| `platform_pg` | `schema/platform/schema.pg.sql` | `pkg/store/pgstore/migrations/platform/` | `docker://postgres/16/dev` |
| `platform_lite` | `schema/platform/schema.sqlite.sql` | `pkg/store/sqlitestore/migrations/platform/` | `sqlite://dev?mode=memory` |
| `org_pg` | `schema/org/schema.pg.sql` | `pkg/store/pgstore/migrations/org/` | `docker://postgres/16/dev` |
| `org_lite` | `schema/org/schema.sqlite.sql` | `pkg/store/sqlitestore/migrations/org/` | `sqlite://dev?mode=memory` |
| `team_pg` | `schema/team/schema.pg.sql` | `pkg/store/pgstore/migrations/team/` | `docker://postgres/16/dev` |
| `team_lite` | `schema/team/schema.sqlite.sql` | `pkg/store/sqlitestore/migrations/team/` | `sqlite://dev?mode=memory` |
| `personal_pg` | `schema/personal/schema.pg.sql` | `pkg/store/pgstore/migrations/personal/` | `docker://postgres/16/dev` |
| `personal_lite` | `schema/personal/schema.sqlite.sql` | `pkg/store/sqlitestore/migrations/personal/` | `sqlite://dev?mode=memory` |

## `{{schema}}` Placeholder Handling

Team and personal PG schemas use `{{schema}}` for multi-tenant isolation (replaced at runtime with e.g., `team_general` or `personal_abc123`). Atlas can't parse this directly, so:

- `make migrate-diff` preprocesses: `{{schema}}` → `public` (for Atlas to parse)
- After generation: `public.` → `{{schema}}.` in the output migration
- The resolved files (`schema.pg.resolved.sql`) are gitignored

## Makefile Targets

```bash
make migrate-diff ENV=<env> NAME=<name>  # Generate migration for one env
make migrate-diff-all NAME=<name>         # Generate for all 8 envs
make migrate-verify                       # Validate atlas.sum integrity
make migrate-status                       # Show migration status per env
```

## Pre-commit Hook

The hook enforces two rules:

1. If `schema/*.sql` files are staged without corresponding migration files → **blocks commit** (reminds you to run `make migrate-diff`)
2. If migration files are staged → validates `atlas.sum` integrity (no tampered or missing hash entries)

## CI (lint.yml)

The `migration-drift-check` job runs `atlas migrate validate` on all 8 directories. Fails if any `atlas.sum` is inconsistent with its migration files.

## Baselining (One-Time Setup)

After initial Atlas setup, run `scripts/atlas-baseline.sh` to generate the initial `atlas.sum` files for existing migrations. This has already been done — the sum files are committed.

## Limitations

### SQLite (Community Edition)

Atlas community edition has two limitations affecting SQLite:

1. **Triggers/FTS5 require Atlas Pro** — The `org`, `team`, and `personal` SQLite schemas use FTS5 virtual tables with content-sync triggers. Atlas community edition cannot parse these. Migrations for these scopes must be written manually.

2. **Inline UNIQUE normalization** — SQLite `UNIQUE` column constraints create implicit indexes with auto-generated names. Atlas sees these differently from explicit `CREATE UNIQUE INDEX` statements, producing cosmetic drop/recreate diffs. The `platform_lite` environment (no triggers) works but may emit cosmetic no-op index changes on initial diff.

**Recommendation**: Use Atlas primarily for **PostgreSQL migrations** (`*_pg` environments). For SQLite, write migrations manually and update the schema file + `atlas.sum` together. The schema files still serve as the canonical reference for what the database should look like.

### Updating atlas.sum for manual migrations

After writing a SQLite migration manually:
```bash
# Add your migration file, then re-hash
atlas migrate hash --env team_lite
```

## Prerequisites

- **Atlas CLI**: `curl -sSf https://atlasgo.sh | sh`
- **Docker** (for PG environments): Atlas uses `docker://postgres/16/dev` to spin up a temporary PG instance for schema diffing

## Key Design Decisions

- **Dev-time only**: Atlas never appears in the Go binary or runtime. Zero runtime risk.
- **Existing `migrate()` unchanged**: sqlitestore and pgstore continue to embed and apply SQL files via `schema_migrations` at startup.
- **Two SQL files per scope**: `.pg.sql` + `.sqlite.sql` — no HCL, familiar format.
- **`atlas.sum` committed**: Provides integrity verification without needing Atlas at runtime.
- **PG primary, SQLite manual**: Atlas diff generation works cleanly for PG; SQLite migrations are written manually due to trigger/FTS5 limitations.
