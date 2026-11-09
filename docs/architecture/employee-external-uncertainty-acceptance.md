# External effect uncertainty: controlled acceptance and scope boundary

## Deterministic gate

```sh
GOPROXY=off GOSUMDB=off go test ./pkg/api -run '^TestBotExternalUncertaintyLoopbackAcceptance$' -count=1 -v
```

This test runs the real `BotChatRuntimeAdapter`, ChatRunner, exact-action
checkpoint/consumption guard, trusted outcome ledger, and shared
`tools.HttpRequest` implementation. It does not merely increment a tool counter.
An owned loopback HTTP sink writes a key-bound receipt to a temporary file and
closes the connection **without returning a response**. The caller cannot infer
success from that connection failure. The sink's record is independently queried
by provider key afterward.

The controlled composition limits execution to `http_request`, the exact Bot/run/
owner/org/team identity, POST of the approved arguments to one endpoint, and GET
of the one key-bound receipt. The runtime budget is finite (30 seconds, 8 tool
calls, 4096 tokens). The mock model makes no external model requests. All HTTP
dials are pinned to the single owned listener; proxies and connection reuse are
disabled. The private-network test setting and transport are restored afterward.
Do not run this test concurrently with tests changing process-global HTTP policy.

Assertions:

- Pending exact approval: zero POSTs/effects.
- Controlled approval: one approval consumption, one POST, one effect, lost
  response, partial result with `UncertainEffect`, no claimed changed evidence,
  and an unverified report carrying the provider key.
- Two repeat resumes reject without a second POST.
- Two separately invoked read-only runtime attempts query the receipt and verify
  its exact key, payload SHA-256, and effect count. POST count remains one.
- Denied approval, wrong tenant scope, and changed classified endpoint: zero
  approval consumption, zero POSTs, zero effects, no provider receipt file.

## What this does not establish

This is **controlled runtime acceptance**, not employee-launcher production
acceptance. The approval store is the existing in-memory fixture; approval is an
explicit fixture state change, not a human button click or HTTP decision. The
HTTP adapter's exact resource classifier and reconciliation interpretation are
test-only. Production sandbox routing, real user authentication, PTY/Studio
rendering, durable approval-store restart, and crash/power-loss durability are
not exercised. Clearing a checkpoint in the controlled read request is not a
production resume/reconciliation API. The original uncertain report remains
uncertain; the read produces separate evidence, not a rewritten run receipt.

Production routine exceptions intentionally remain a closed set of local
own-controls (`pkg/store/entstore/bot_authorized_routine.go`). The
`memory_forget` approval restriction (`pkg/config/bot_routine_policy.go`) removes
routine authority; it is not an external grant configuration. General sandbox
HTTP/MCP/email availability does not supply an employee-mandate exact external
exception plus provider-key reconciliation protocol. The existing exact-action
resume does correctly refuse replay after an attempted action
(`pkg/api/bot_runner_resume.go`).

**Production scope blocker:** no supported configured employee external-effect
exception/reconciliation adapter was identified. Do not add arbitrary external
capabilities to the operator baseline or pretend the local memory transaction is
an external uncertain effect. Closing that separate production gap requires an
explicitly reviewed adapter, bounded provider/resource policy, durable effect
key/receipt, and existing human approval UI-to-worker integration. This gate is
useful required regression evidence but cannot mark that full acceptance passed.

## Machine-readable evidence

With `-v`, each of the four subtests emits one line containing
`EXTERNAL_UNCERTAINTY_RECEIPT ` followed by JSON. Require the Go command to exit 0
and exactly these scenarios: `approve`, `deny`, `wrong_scope`, `wrong_target`.
Every record has `schema: employee-external-uncertainty/v1`, `passed: true`, and
`production_scope_blocked: true`.

For `approve`, require `post_attempts == effect_count == approval_consumptions ==
1`, `reconciliation_gets == replay_rejections == 2`, a nonempty UUID
`provider_key`, a 64-hex `payload_sha256`, and `uncertain_report.unverified`
containing that key with no changed evidence. Also require
`human_ui_verified == false`, `approval_store == controlled_in_memory`, and
`transport == owned_loopback_shared_http` to preserve the evidence boundary.
For the negative cases require all three counters to equal zero. Do not treat a
missing selector, skipped test, missing receipt, or the production scope blocker
as success of the overall employee external acceptance requirement.
