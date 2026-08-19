# pkg/store/entstore — AGENTS.md

Multi-tenant DB router on top of Ent. This is the **only** legitimate entry point for reads/writes of tenant data.

Read the parent [`pkg/store/AGENTS.md`](../AGENTS.md) first; this file adds implementation-specific rules.

## Scope
- Choose the correct Ent client for a given tenant scope (`platform`, `org`, `team`, `personal`).
- Own connection pools per DB / schema.
- Apply per-tenant encryption context (envelope keys are held by the platform scope).
- Emit audit rows for `INSERT` / `UPDATE` / `DELETE` on auditable entities.

## Non-negotiable rules
1. **Never open an ent client outside `entstore`.** Handlers, tools, daemon wiring, tests — all must go through the router. This is the invariant that keeps database-per-org / schema-per-team isolation structural rather than policy-only.
2. **Never cross scopes.** A handler resolving to org `A` may not touch org `B`'s data even if it holds a raw `orgID`. Cross-tenant reads (platform-admin surfaces) go through the platform scope explicitly.
3. **Never bypass audit tables with raw SQL**. UPDATE and DELETE on audit tables are revoked at the DB level — respect that constraint.
4. **Encryption context propagates via the router.** Credentials use per-org DEKs; the router must set the DEK for the caller's org before any credentials read/write.

## Adding a new entity
1. Decide the scope (`platform`, `org`, `team`, `personal`) — see `ent/AGENTS.md`.
2. Add the schema under `ent/<scope>/schema/`.
3. Regenerate with the `generate.go` entrypoint for that scope (do not edit generated code).
4. Add an Atlas migration under `schema/*.sql` or `pkg/store/*/migrations/*.sql`. The pre-commit hook validates integrity for `<scope>_pg` and `<scope>_lite` envs.
5. Expose the entity through `entstore` router methods — do **not** export the raw ent client.

## Migrations
- Postgres and SQLite both supported. Every Atlas env must stay in sync — the pre-commit hook enforces `atlas migrate hash --env <env>` for `platform_{pg,lite}`, `org_{pg,lite}`, `team_{pg,lite}`, `personal_{pg,lite}` whenever migration files change.
- Personal mode uses SQLite with the same multi-tenant semantics (see `docs/architecture/sqlite-backend.md`).

## References
- `docs/architecture/multi-tenant-platform.md` — six enforcement points, envelope encryption, cascading defaults.
- `docs/architecture/sqlite-backend.md` — SQLite topology.
- `ent/AGENTS.md` — schema editing rules.
 tenant enforcement and cascading defaults.
- [`docs/architecture/sqlite-backend.md`](../../../docs/architecture/sqlite-backend.md) — SQLite topology.
- [`docs/architecture/migrations.md`](../../../docs/architecture/migrations.md) — migration workflow.
- [`ent/AGENTS.md`](../../../ent/AGENTS.md) — schema editing rules.
