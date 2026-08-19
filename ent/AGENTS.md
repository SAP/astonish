# ent/ — AGENTS.md

Ent (`entgo.io/ent`) schemas split into four **tenant scopes**. Almost every file here is generated. The hand-editable surface is small — respect it.

## The four scopes
- `ent/platform/` — global identity: users, orgs, tenants, platform-level config, envelope master keys.
- `ent/org/`      — per-org data (database-per-org): sandbox templates, org-scoped credentials, memory shards, audit logs.
- `ent/team/`     — per-team data (schema-per-team within an org DB): team resources, flows, apps, memory, MCP configs, skills.
- `ent/personal/` — per-user private workspace: personal sessions, credentials, memory, apps.

Cascading defaults flow **downward** (`platform → org → team → personal`); ownership publications flow **upward** only via explicit user actions.

## Editable vs. generated
- **Editable**: `ent/<scope>/schema/*.go` — schema definitions (Fields, Edges, Indexes, Annotations, Mixins).
- **Editable**: `ent/<scope>/generate.go` — the `go:generate` directive.
- **NOT editable** (regenerate instead): everything else under `ent/<scope>/` — `client.go`, `mutation.go`, `*_query.go`, `*_create.go`, `*_update.go`, `*_delete.go`, `runtime.go`, hooks, etc.

If you find yourself opening `mutation.go` to make a code change, stop — you're editing generated code. Update the schema or a hook.

## Regeneration flow
```bash
# From repo root
go generate ./ent/platform/...
go generate ./ent/org/...
go generate ./ent/team/...
go generate ./ent/personal/...
```
Or the umbrella target if present:
```bash
make ent-generate
```

After regenerating:
1. Run focused tests for the affected store, then `go test ./...` as practical.
2. If you added or renamed fields, create the required migration using the repository's current migration workflow; see `docs/architecture/migrations.md`.
3. Commit schema, generated output, and migration together; the pre-commit hook checks schema/migration consistency.

## Choosing the right scope
- If it needs cross-org visibility → `platform`.
- If it is per-org configuration or per-org bulk data → `org`.
- If it is a team-shared resource (flow, app, memory, skill, MCP) → `team`.
- If it is private to a single user → `personal`.

When in doubt, ask: "Would exposing this to another team/org be a data leak?" If yes, do not put it in `platform` or `org`.

## References
- [`pkg/store/AGENTS.md`](../pkg/store/AGENTS.md) — storage interfaces, scope rules, and implementation map.
- [`pkg/store/entstore/AGENTS.md`](../pkg/store/entstore/AGENTS.md) — the router that picks the right Ent client at runtime.
- [`docs/architecture/multi-tenant-platform.md`](../docs/architecture/multi-tenant-platform.md) — invariants and enforcement points.
- [`docs/architecture/migrations.md`](../docs/architecture/migrations.md) — migration workflow and safety rules.
