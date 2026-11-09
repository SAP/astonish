# Protected durable mandate storage

## Trust and scope

`store.WithBotMandateAdmission` is an explicit, trusted composition-root hook. It must receive a resolver built from current operator ceilings and the exact available tool/resource catalog, never model text or request-supplied policy. The hook is not automatically enabled by these storage changes.

`AdmitMessage` and atomic `CreateDraftAndStart` issue task-purpose mandates only, after the actual user event has a stable ID and transcript cursor. Only a verified platform-authenticated owner or verified local-loopback owner can issue. OAuth, service/channel identities, and a claimed principal without authentication evidence cannot issue. Title start and legacy-bot messages use the same admission transaction; reads do not migrate authority or reactivate old schedules.

The mandate binds owner, organization, team, bot, immutable revision, source run, actual user event, and a digest of the admitted instructions and attachments. `BotRun.MandateRef` is excluded from public JSON and persisted in the private run envelope. Duplicate admission returns the original pin instead of issuing again. The source run's private pin, event ID, cursor and digest are checked again by the verifier.

## Persistence and mutation

The existing scoped Bot outbox stores immutable reserved-key records; root Bot JSON stores `mandate_head`, quota counters, and operation revision heads. Reads use direct keyed lookups, not an activity scan. `AppendActivity` only accepts run progress and generates its own progress keys; it cannot write mandate keys or reserved record kinds. The runtime's `CreateBotMandate` method deliberately rejects arbitrary creation.

`entstore.NewBotMandateRuntime(bots, policyResolver)` accepts only a routed personal or team Ent Bot store. Its verification path uses current scoped membership and current resolver policy on a separate runtime context, without synthesizing an owner principal. Missing membership, viewer-only membership, changed ownership, deletion, expiry, revocation, mismatched source, purpose, or removed tool/resource access fails closed.

`RevokeBotMandate` requires current verified owner authentication. Revocation is an immutable keyed record and idempotent. Mutations share the Bot root sequence compare-and-swap within the scoped transaction, serializing revocation, quota and operation writes. Failed admission rolls back the source event, mandate, private pin, head and dispatch, and cancels provisional live turn authority.

## Quota contract

- `ReserveBotMandateRun(ctx, ref, runID)` checks the persisted run pin and current authority, then reserves a concurrency slot and charges one UTC daily run. An active reservation is idempotent; a released run ID cannot be reserved again.
- `ConsumeBotMandateQuota(ctx, ref, runID, tools, tokens, spendMicros)` must be called **before** the corresponding side effect. It requires an active reservation, rejects negative or overflowing charges, and enforces finite per-run and Bot-lifetime aggregate tool/token/spend limits.
- `ReleaseBotMandateRun(ctx, ref, runID)` releases concurrency without refunding daily or cumulative charges. Release remains possible after revocation, expiry, or policy resolver denial, but still requires valid scoped ownership/membership and a matching saved run pin.
- Daily counters reset at UTC midnight. Aggregate counters do not reset on midnight, release, or issuance of a new mandate.

A consumer must handle transaction conflicts by retrying the operation, never performing a side effect after a failed reservation. Exactly-once external side effects and production quota dispatch wiring are outside this storage layer.

## Operation handoff

The returned runtime also implements `store.BotOperationStore` (type assertion). Creation binds an idempotency key to exact immutable content and starts at pending revision 1. Transitions check expected revision and the typed transition matrix, persist an immutable new record, and advance the private head atomically. Applied receipts require observed postconditions. This layer persists records only; it does not apply schedules, execute payloads, or independently attest a supplied executor receipt.

## Cleanup and verification

Existing permanent deletion removes all Bot outbox records, including mandate, revocation, operation, reservation and quota records. No schema or generated Ent changes are needed.

SQLite personal/team tests cover title admission, duplicate replay, persistence after reopening, quota boundaries and aggregate lifetime accounting, revocation, current catalog/membership denial, rollback, forged activity, private pin JSON exclusion, operation CAS/replay, and reserved-record purge. PostgreSQL mandate behavior and production daemon/launcher integration require separate verification.
