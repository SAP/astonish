# Migration Management

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

## Bot skill persistence (DDL marker 2)

`botMigrationVersion=2` adds `bot_skills` and immutable `bot_skill_revisions`
in personal/team Ent scopes. This marker is deliberately independent of
`store.BotSchemaVersion=1`: historical immutable BotVersion JSON remains valid.
No open, read, or schema reconciliation converts scheduler instructions or memory
into skills. `ConvertLegacySkill` currently returns `ErrBotUnsupported`; explicit
legacy conversion belongs to the wakeup integration phase.

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

`botMigrationVersion=3` adds scoped personal/team `bot_tasks` and
`bot_task_revisions` through the current additive Ent reconciliation workflow.
The registry stores identity/lifecycle and head revision/digest, not another editable
copy of live TASK.md instructions. Immutable revision JSON holds archival/recovery
bundle bytes using the existing precision-protecting envelope; publication state,
original authority ref and backend receipt ref are separate envelope columns.
Task heads are independent from BotVersion and Skill heads. Historical document
schema versions, JSON bytes/hashes and legacy schedule payloads remain unchanged.
No reconciliation backfills Tasks or converts existing instructions into Tasks.

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
No startup or GET backfills historical state or initializes legacy drafts.
`start_from_title:true` is explicit create-only admission, not a migration of old
create keys. Imports and default CreateDraft stay inert. Snapshot fields
`run_states` and `history_cursor` are additive read-only projections. PostgreSQL
requires a separate disposable-database run; SQLite proof is not PostgreSQL proof.

## Bot per-turn extraction persistence (DDL marker 4)

`botMigrationVersion=4` adds personal/team `bot_knowledge_extractions` and
`bot_knowledge_file_heads` using additive Ent reconciliation. Generate with
`go run generate.go` in each scope, without historical document version or Atlas
checksum changes. Full source tuples are unique; exact input remains in the
precision-protecting `bot_document` envelope. New input requires a trusted
complete-source verifier plus a canonical terminal run and result activity.

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
