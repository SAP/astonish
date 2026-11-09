# Bot deterministic drill execution

`launcher.BotDrillTools` preserves the `run_drill` and
`inject_drill_credentials` Chat schemas, but does not call their ordinary
host-capable execution handlers. Production supplies the already provisioned
physical Bot sandbox ID and backend. The Chat factory configures each adapter
with its bound tools and metered model after the catalogue is wrapped.

## Authority and dispatch

The existing `drill.SuiteRunner` handles test ordering, assertions, parameters,
auto-waits and tutorial steps. Every explicit or implicit tool call goes through
the same terminal `botBoundTool` as a normal Bot call: live lease/isolation,
caller tool ceiling, exact-action policy and shared tool budget. Step contexts
retain the invoking child policy and runner deadline, not a parent approval
handoff. Nested `run_flow`, `run_drill` and credential-injection tool nodes are
not admitted. Unsupported/missing adapters never select local tools.

A narrow optional `drill.TerminalToolExecutor` lets this adapter latch a failed
dispatch and stop all subsequent tests, parameters, retries and cleanup calls.
Ordinary drill executors do not implement it and retain existing behavior.

## Credentials

Only suite-declared mappings from the trusted team's flow store are considered;
there is no template/Fleet-plan or filesystem credential fallback. Injection
requires a separate exact action containing the normalized mappings, injection
manifest and physical sandbox ID; approving just the suite name cannot approve
changed credential destinations. The current caller policy must also allow
`inject_drill_credentials`.

Stored static values come from the factory's scoped credential store (team Bots
cannot access personal credentials). The existing Fleet file materializer writes
only through the pinned backend, rechecking the lease/isolation before each
write. It receives a resolved snapshot and no resolving store, so it cannot
refresh OAuth on the daemon. Values are registered with the run redactor before
writes. Results and stored reports are scrubbed; no environment map is returned.

## Explicit limitations

- Environment injection is blocked until a pinned node environment adapter is
  available. Static file fields supported: token/value/password/username/content.
- No daemon report files or artifact directory are created. Successful reports
  can be persisted to the scoped report store.
- Recording/scene artifact output depends on a scoped browser artifact adapter;
  the existing unavailable recording tools remain unavailable, not host-backed.
- Browser takeover is blocked by the existing browser adapter when no durable
  attached-owner checkpoint is configured; drills never report a waiting state
  themselves or open a host listener.
- No automatic template change, setup, readiness, teardown, or triage agent is
  introduced. Semantic assertions use the factory's metered model.
