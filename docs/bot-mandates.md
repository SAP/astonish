# Bot mandate and operation contracts

This document defines mandate/operation contracts and current operator defaults.
The contracts alone do not authorize execution: production issuance and durable
verification supply the grants described below. Existing interactive turn
authorization remains process-local and expires normally.
See also [interactive checkpoints](architecture/bot-interactive-checkpoints.md).

## Authority boundary

`execution.BotMandateRef` pins mandate ID, bot ID, organization, team, owner and
revision. A stored reference is a restriction/lookup key, not a grant. A trusted
composition-root-injected `BotMandateVerifier` must re-read durable revision,
revocation, active membership, protected source provenance and **current**
operator ceilings. Only `VerifyBotMandateAuthorization` constructs the sealed
`VerifiedBotMandateAuthorization`. Never bind verifier/provenance fields from
HTTP bodies, model output, retrieved documents, chat history or tool arguments.
Production composes the durable verifier; the reference alone never grants authority.

Mandates have typed purposes (`schedule`, `task`, `operation`), exact capability
scopes and exact action/resource pairs (no wildcard resources). Resource checks
must use the sealed authorization's `Authorize(action, resource)`; the baseline
capability authorizer checks scopes, not resource ownership. JSON snapshots and
caller ceilings cannot reconstruct the sealed token. Forged mandate principals,
modified identities, expired mandates and scope escalation are rejected. A
mandate principal is **not** covered by the non-OAuth legacy-user scope bypass.
No bearer or secrets are persisted. Snapshots deep-copy all nested slices.

## Finite operator ceilings

`pkg/config/app_config.go:DefaultBotMandateConfig` is the canonical source.
`AppConfig.BotMandates == nil` selects defaults. An explicitly supplied
`bot_mandates` object replaces the **entire** default object, not individual
fields. All nine capacity fields must be positive; zero/negative capacities,
partial objects and per-run limits exceeding aggregate limits fail closed.
`ValidateWithin` compares against the current operator config (including
aggregate and permission limits). Explicit lower or higher operator ceilings
remain exact, including values equal to historical defaults.

| Limit | Default |
| --- | ---: |
| Minimum schedule interval | 60 seconds |
| Schedules | 8 |
| Daily runs | 1440 |
| Concurrent runs | 1 |
| Runtime per run | 300 seconds |
| Tool calls per run | 100 |
| Tokens per run | 1,000,000 |
| Aggregate tool calls | 144000 |
| Aggregate tokens | 1,440,000,000 |
| Spend (integer micros) | 0 |
| External delivery | false |
| Destructive actions | false |

A complete application `config.yaml` override matching the built-in defaults:

```yaml
bot_mandates:
  min_schedule_interval_seconds: 60
  max_schedules: 8
  max_daily_runs: 1440
  max_concurrent_runs: 1
  max_runtime_seconds: 300
  max_tool_calls: 100
  max_tokens: 1000000
  max_total_tool_calls: 144000
  max_total_tokens: 1440000000
```

Omitted spend and authority fields retain zero/false. These operator ceilings
are not permission grants and do not change provider context/output limits.
Integers avoid NaN/infinity and floating spend ambiguity. Aggregate limits are
independent finite ceilings, not multiplication at admission.

### Existing Bots and pinned grants

Production issuance (`pkg/store/entstore/bot_mandate.go`) creates a fresh mandate
with the current effective operator limits for each authenticated owner message,
including messages to an existing Bot. A Bot version's explicit lower token
budget remains binding; explicit zero forbids model use rather than selecting
the default. An absent version token budget is capped by the mandate. No stored
Bot version or historical failed run is rewritten or retried by a default change.

Already pinned runs, approval resumes and schedule grants retain their stored
limits, subject to current-policy verification. Raising the default does not
upgrade those grants; lowering operator ceilings can invalidate an older grant.
Existing schedule ticks retain their schedule's pinned grant, not a later owner
message's new grant. Resumes restore cumulative usage rather than reset it.

Aggregate tokens/tool calls are **lifetime cumulative usage on the Bot ledger,
not daily allowances** (`pkg/store/entstore/bot_mandate_quota.go`). Only daily-run
counts reset at the day boundary. Neither a fresh owner-message mandate nor a
higher default clears aggregate usage. There is no blanket stored-record
migration: a stored 100,000-token cap may be explicit and must not be inferred
to have come from a default.

## Operations and receipts

Policy results are `Execute`, `Clarify`, `RequireAuthorization`, or `Deny`.
Model suggestions are payloads only and must never supply the policy result.
States and permitted transitions:

- Pending → Applying, Blocked, Cancelled.
- Applying → Applied, Blocked, Failed, Cancelled.
- Blocked → Pending (fresh policy evaluation required), Cancelled.
- Applied, Failed and Cancelled are terminal; retry uses a new operation.

Storage implementations must scope idempotency keys, compare-and-swap revisions,
and enforce the transition table. An Applied receipt requires a nonzero
observation timestamp and nonempty verified resource/revision postconditions.
A model saying “done,” a successful tool invocation, or a planned action is not
a postcondition. Only the trusted executor can publish receipts after reading
actual committed state. Receipt validation is structural, not proof of source.

## Verification

Fail-first contract tests precede implementation. Run:

```sh
go test ./pkg/execution ./pkg/store ./pkg/config -run 'TestBotMandate|TestBotDelegation|TestBotTurnAuthorization|TestBotCallerCeiling' -count=1
```

No live runner, database implementation, revocation race, or aggregate debit
has been exercised in this foundation phase; those belong to consuming phases.
