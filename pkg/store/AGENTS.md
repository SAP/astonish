# pkg/store — AGENTS.md

Storage contracts and dependency bundles for Astonish's `platform → org → team → personal` hierarchy. Implementations live below this package; [`entstore/AGENTS.md`](entstore/AGENTS.md) adds Ent-specific rules.

## Scope model
- Platform stores own cross-org identity, organizations, login/OIDC, platform defaults, and platform-wide resources.
- `TenantRouter.ForOrg` enters one organization. Org data then resolves team and personal stores; never accept an ID from request data as proof of scope.
- Team stores hold shared resources such as fleet state, published apps/flows, schedules, and team credentials.
- Personal stores hold private chat sessions, credentials, apps/flows, memory, settings, and personal schedules. Publication upward is explicit.

## `Services` rules
- `Services` is the startup/request dependency container; inject it rather than adding package globals.
- Similar fields are intentionally different: `Sessions` versus `PersonalSessions`, `Credentials` versus `PersonalCredentials`, shared versus personal apps/flows/schedulers, and platform/org/team variants.
- Nil fields can be valid in modes without that capability. Consumers must require only the interfaces they need and fail clearly rather than silently falling back across scope.
- Defaults cascade downward, while same-name lower-scope definitions override for that scope. Writes go to the explicit owner scope; reads must not mutate broader defaults.

## Interface and implementation changes
1. Define/extend the narrow interface here, then update every implementation, composition root, mocks, and scope-routing test.
2. Schema-backed entities require `ent/<scope>/schema`, generated Ent output, migrations, and `entstore` implementation in one change.
3. Keep PostgreSQL and SQLite behavior equivalent where both are supported.
4. Preserve context cancellation, deterministic errors (`errors.Is` where exposed), and concurrency safety of cached routed stores.
5. Raw SQL is limited to documented implementation needs; do not bypass tenant routing, audit, encryption, or migration contracts.

## Verification

```bash
go test ./pkg/store/...
go test ./pkg/store/entstore/...
```

Run integration tests for migration, dialect, or database-isolation changes.

## References
- [`ent/AGENTS.md`](../../ent/AGENTS.md)
- [`docs/architecture/multi-tenant-platform.md`](../../docs/architecture/multi-tenant-platform.md)
- [`docs/architecture/migrations.md`](../../docs/architecture/migrations.md)
