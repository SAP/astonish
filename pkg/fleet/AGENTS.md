# pkg/fleet — AGENTS.md

Multi-agent orchestration: fleets are reusable templates describing a graph of agents, roles, delegations, and tools; fleet sessions are live executions of those templates.

## Scope
- Configuration and validation (`config.go`), session lifecycle (`session_manager.go`, `dispatcher.go`), and recovery (`recovery.go`).
- Durable team-scoped run state, mailbox, and task board through `pkg/store` interfaces.
- Plan activation/registry (`plan_activator.go`, `plan_registry.go`) and scheduled activation.
- Channel bridges and SSE/transcript messages.
- Bundled templates and setup profiles/steps.

## Invariants
- **Steps are the setup source of truth.** Put prompt/content/tools/pinned groups on steps; do not add profile-level wizard prose or new embedded `plan_wizard` blocks.
- **Serial regression floor:** `MaxParallelAgents <= 1` stays serial with durable mailbox and task board. Parallel dispatch requires both a higher limit and parallelizable agents.
- **Bundled templates are immutable.** Embedded keys win on read/list; save/delete for a bundled key fails. Customize by cloning to a new team-owned key. `EnsureBundled` is legacy personal-file behavior only.
- `Message` is the SSE/transcript envelope; durable per-recipient mailbox is the agent-facing handoff source of truth. `monitor_state.go` is only the GitHub poll cursor, not fleet run state.
- `CapabilityRegistry` is advisory and domain-neutral; only `supervisor` has routing semantics.
- Keep fleet logic channel-agnostic. Add generic adapters under `pkg/channels` rather than coupling orchestration to a provider.

## When editing
1. Schema/config changes must update validation, loaders, bundled fixtures, and daemon/API consumers together.
2. New durable data is team-scoped via `ent/team/schema`, `pkg/store` interfaces, and `entstore`; never bypass tenant routing.
3. Do not break `Channel` or `FleetRecoverFunc` for mailbox/recovery additions; layer alongside or wrap.
4. `PlanActivator` changes must preserve scheduler execution/delivery semantics.
5. Fleet SSE changes require matching Studio consumers and scenario coverage in the same change.

## Verification

Run `go test ./pkg/fleet/...`. For durable state or scheduled activation also run `go test ./pkg/store/... ./pkg/scheduler/...`; for stream changes verify API and Studio fleet consumers.

## References
- [`pkg/store/AGENTS.md`](../store/AGENTS.md)
- [`pkg/scheduler/AGENTS.md`](../scheduler/AGENTS.md)
- [`docs/architecture/chat-rendering-pipeline.md`](../../docs/architecture/chat-rendering-pipeline.md)
