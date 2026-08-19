# pkg/scheduler — AGENTS.md

Scheduled-job models, stores, cron lifecycle, execution modes, and result delivery.

## Invariants
- Cron expressions use the established five-field parser. Resolve configured timezones and store computed next runs in UTC; invalid cron/timezone input must remain observable.
- Prevent overlapping dispatch of the same job. Keep the in-flight map synchronized and release entries on every completion path.
- Preserve failure backoff, consecutive-failure accounting, last status/error, and next-run persistence. A delivery failure does not rewrite execution success/failure unless the contract explicitly changes.
- `RunNow` and tick execution share the same execution/state-update path. Do not create a manual path that skips timeout, scope, sandbox, or persistence rules.
- Execution receives the correct scoped flow, credentials, stores, gateway/network policy, and delivery context. Personal jobs and team jobs must not share the wrong credential/store bundle.
- Adaptive/ephemeral sandboxes are readied before execution and destroyed/invalidation-safe afterward, including timeout/error paths. Lifecycle cleanup remains idempotent.
- Delivery resolves the live channel manager at delivery time. Fleet-poll jobs own their communication and bypass ordinary scheduler delivery.

## When editing
- New job modes require model validation, executor routing, API/tools, persistence, and tests.
- Delivery mode changes require resolver tests for owner/member/team scope and deduplication.
- Multi-tenant behavior must be exercised with explicit org/team/user context.

## Verification

```bash
go test ./pkg/scheduler/...
go test ./pkg/scheduler -run 'Test.*(Backoff|Timeout|Sandbox|Delivery|RunNow)'
```

Include daemon/API tests when changing registration or multi-tenant wiring.

## References
- [`pkg/store/AGENTS.md`](../store/AGENTS.md)
- [`pkg/sandbox/AGENTS.md`](../sandbox/AGENTS.md)
- [`pkg/fleet/AGENTS.md`](../fleet/AGENTS.md)
