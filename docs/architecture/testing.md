# Testing Architecture

## Corrected Bot Chat model gates — 2026-11-07

`TestIntegrationPostgresBotChatModel` and `TestIntegrationPostgresBotTitleStartState` exercise real PostgreSQL personal/team transactions, not SQLite substitution. The new gate checks title replay across reopen, execution mode/caller-ceiling persistence and public redaction, queued state, custom question continuation after registry revocation, explicit reauthorization and one saved run/fresh fence. It requires `ASTONISH_PG_ACTIVATION_DISPOSABLE=1` plus a dedicated loopback nondefault-port DSN using role `astonish_activation_final`, database `postgres`, TLS disabled and no fallback hosts; absence fails rather than skips. Use disposable PostgreSQL with pgvector and remove its tmpfs container afterward.

Strict driver selectors require title browser, ambiguous-question, thinking restore and title PTY tests alongside legacy compatible journeys. Controlled models and source tests are not real-provider acceptance. Fresh corrected title-first live acceptance remains pending; historical nine-stage control-conversation results cannot be recycled. BOTS-001–010 still requires current non-skipped deployed evidence. No full release certification is implied.

This document defines the three-tier testing structure for Astonish.

`docs/TESTING.md` is the comprehensive reference for the unit / SSE-scenario / backend-integration / prompt-contract layers (vocabulary: "Layer 0–3"). This document is authoritative for the **build-tag taxonomy** (`unit` / `integration` / `e2e`) and for the e2e harness, K8s sandbox infra, and inspector mode. The two are complementary: the layers describe **what kind of bug a test catches**; the build tags describe **what infrastructure a test needs to run**.

## Security acceptance tracks

`make test-security SECURITY_TRACK=urgent SECURITY_STORAGE=both SECURITY_RUNTIME=k8s`
runs `TestE2E_SecurityUrgent*` in `chat_auth`, first with PostgreSQL selected,
then SQLite. The PostgreSQL leg additionally selects
`TestE2E_SecurityPostgresFirstRunSetup` (CHAT-115); the SQLite leg does not select
that PostgreSQL-only test. Most cases use the real StudioServer harness; the local first-run setup,
legacy-MCP, full-config mask and scoped-memory local proofs described below
always use temporary SQLite.
These are the only registered track,
storage-matrix and runtime selections today. Unknown, empty, single-storage,
and unimplemented selections (including `all`) fail before bootstrap; new tracks
must add real tests and an exact required package/test/subcase manifest in
`tests/scenarios/parse-run.mjs`, plus COVERS markers and scenario catalog entries.
Ordinary E2E reporting remains permissive and normal make targets are unchanged.

Strict parsing requires all six original urgent categories, the three existing
session-ownership actor subcases, configured-init endpoint/actor subcases, and
the same-team org-owner denial/owner-resume test. Channel proof lifecycle coverage
adds direct-verification rejection, expiry, eight concurrent requests with one
winner plus replay denial, and an email already linked to another user.
Memory participant coverage adds source, duplicate, discard, implicitly selected
and explicitly selected destination cases, each with denied/no-mutation and
allowed/persisted-result subcases. Organization provider coverage adds 13 cases
for redacted reads, member denial, exact/stale/foreign/missing masks, mixed-invalid
updates, explicit replacement, omission, deletion and clearing. Same-slug
cross-org session coverage requires both org-owner directions for read/list,
delete and injection denials, owner resume and owner deletion. Team provider
coverage requires 13 cases in each of patch and replace modes,
including inherited-mask rejection, member write denial, exact preservation,
explicit replacement/clear, omission and empty-map semantics. Every team case checks
whole persisted team settings and unchanged org, foreign-team and platform settings.
The opt-in `e2eboot.SeedSameSlugTeams` fixture provisions distinct team IDs
with the same slug without changing the standard seed layout; HTTP session snapshots
must remain unchanged after denials and the other org's session must survive deletion.
This is session isolation coverage, not sandbox filesystem collision acceptance.
Each organization provider case checks persisted org state and unchanged foreign-org
and platform settings. Platform provider coverage adds 16 cases: platform-admin
redacted reads and exact/replacement/omission/deletion/clear writes, invalid mask
rejections, and org-owner/team-admin/member denials with lower-scope positives.
Each checks whole platform settings, both org/team canaries and deployment config.
MCP install isolation adds 21 cases covering POST install and DELETE uninstall:
member/default-team/team/org/platform and owner/platform denials, invalid scopes,
forged foreign-team scope, and authorized default-team/team/org persistence.
`SeedProviderAuthority` adds independent org-member principals: the team admin
can write default-team/team scope but cannot write org/platform or select a
sibling team; the platform admin can install/uninstall at platform scope.
Narrow writes and denials preserve global secret ciphertext and legacy credentials.
Four absent-global cases cover default-team/team/org install and uninstall plus
member denial; neither the global `secrets` key nor `credentials` key may be
created when absent. Unrelated platform settings and scoped canaries remain intact.
The platform positive instead verifies Tavily's global secret replacement/removal,
allows removal of its legacy fallback, and preserves unrelated secrets and credentials.
Every case preserves unrelated scoped MCP configuration and checks the authenticated
creator. Synthetic keys do not prove Tavily discovery or tool execution.
First-run setup adds seven cases in each of `none` and `builtin` auth modes:
missing/wrong tokens on both endpoints, authorized SQLite initialization with
persisted admin/org/team membership, and no-effects configured retries on both
endpoints. Builtin mode additionally requires restart from persisted config,
anonymous settings denial, fresh password login, correct org/team identity and
authorized platform/team settings reads. These setup positives use temporary
SQLite even in the PostgreSQL leg; they provide no positive PostgreSQL setup proof.
The separate CHAT-115 fixture below implements that obligation but remains unverified
until it runs on the explicitly approved disposable instance.
Independent role authority adds four leaves: `org-admin-settings`,
`org-admin-providers`, `team-admin-providers`, and `platform-admin-full-config`.
The org admin remains only a team member; team/platform admins remain only org
members. Nonempty authorized writes are paired with forbidden writes, redacted
responses and exact persisted snapshots of target/unrelated scopes and deployment
config, so overlapping privileges cannot stand in for independent role authority.
`TestE2E_SecurityUrgentLegacyMCPLocalAuthorization` (`CHAT-112`) adds a
self-contained **local SQLite HTTP/JWT proof** of scoped MCP mutations and refresh,
including the legacy toggle and refresh routes. Each legacy route has 17 denial cases: missing/invalid JWT, member scope writes,
independent-admin scope violations, sibling/foreign teams, and effective/unknown
scopes. Four authorized refresh cases and four missing-row toggle cases cover
default-team, explicit-team, org-owner and platform-only authority. Each of these
subcases exercises both the legacy route and its `/api/mcp-platform/servers/{name}`
counterpart (with `/refresh` for POST), returning 404 without mutation or discovery.
One case protects a platform-only row from default-team toggle, four
missing-store cases fail closed (503), and four authorized toggles verify persisted
scoped state and response state while preserving unrelated snapshots.
Five platform-only standard install/uninstall cases set both platform web-tool
references and clear only matching references on uninstall: `both`, `search_only`,
`extract_only`, `neither`, and `other_scope_matching`. They compare complete team
settings and unrelated platform fields, including preservation of matching team
references, alongside MCP/secret/file snapshots.

The original 56 leaves are preserved; **167 additional outcomes** cover:

- `scoped_mutation/{org_admin_default,org_admin_team,org_admin_org,platform_only}/{create,update,toggle,inline_install,legacy_settings,legacy_patch,delete}`: 28 cases.
- `existing_refresh/{org_admin_default,org_admin_team,org_admin_org,platform_only}/{modern,legacy}`: eight cases, accepting an existing row and verifying the selected scoped command in terminal discovery records while preserving persistence and cached tools.
- `org_admin_standard_install_uninstall/{org_admin_default,org_admin_team,org_admin_org}`: three cases. Narrow installs/uninstalls preserve global secrets and update the current team's web-tool references even for org-scoped MCP rows, matching the existing contract.
- `remaining_rejected/{create,update,delete,toggle,refresh,inline_install,legacy_settings,standard_install,standard_uninstall}/{unauthenticated,member_default,member_team,member_org,member_platform,org_admin_platform,team_admin_org,platform_only_default,effective_scope,unknown_scope,missing_default_team_store,missing_org_store,missing_team_store,missing_platform_store}`: 126 cases.
- `remaining_rejected/{legacy_patch,legacy_refresh}/org_admin_platform`: two cases; the original cases retain the other legacy denials.

That is 28 scoped mutations + 8 existing-row refreshes + 3 install/uninstall pairs + 128 rejections.
The org admin has persisted org-admin authority, only team-member membership, and
no platform role; platform-only authority has no org/team admin role. The added
cases compare target and unrelated scoped rows, caches/timestamps, secrets, files,
and team/org/platform settings. Mutation expectations allow only the intended
row changes (or scoped replacement for legacy settings); new identities/timestamps
are checked rather than treated as arbitrary side effects. Rejections preserve
all snapshots and start no discovery.

An invalid local sandbox backend (`e2e-no-discovery`) stops asynchronous discovery
before external access; the test waits for terminal records, not successful tool
discovery. It clears inherited PreWarm and Studio chat initialization hooks,
resets the chat manager, and clears inherited API credential-store state before
config isolation, so mutations cannot launch unrelated runtime initialization.
Its **377 entries** (parent + 376 subcases: original 56 + 167 + 153 masked updates) are mandatory in both
urgent matrix legs. Slash-separated case names are actual Go subtests, not extra
synthetic parent groups.

Run this regression independently without external infrastructure:

```bash
go test -tags=e2e -count=1 -timeout=2m -run '^TestE2E_SecurityUrgentLegacyMCPLocalAuthorization$' -v ./tests/e2e/chat_auth
```

It uses temporary SQLite regardless of `ASTONISH_E2E_BACKEND`, loopback HTTP,
test-issued JWTs validated by production middleware, and persisted team membership.
It does not exercise login, PostgreSQL, a real daemon binary, Kubernetes, provider
calls, an MCP process, or successful discovery. Its inclusion in a PostgreSQL-labeled
report does not make it PostgreSQL coverage, and a standalone pass is not live
PostgreSQL/SQLite urgent-matrix acceptance. Existing storage/runtime selection and
preflight requirements remain unchanged.

Including real nested parents, the strict manifest requires **594 entries**
in SQLite and **597 entries** in PostgreSQL. The 594 common entries are
94 existing + 17 platform-provider + 22 MCP-install + 18 setup + 5 independent-role
+ 377 local HTTP MCP + 9 scoped memory cleanup + 52 full-config masks.
PostgreSQL additionally requires CHAT-115's parent, `/setup` and `/restart`.
No skipped PostgreSQL placeholder is selected in the SQLite leg.
This preserves the prior 380 entries and nine scoped-memory entries, adding 153
MCP mask leaves and 52 full-config entries. Parser regressions omit each new
parent/leaf independently, including all nine scoped memory cleanup entries in
both backend report labels, all 377 local HTTP MCP entries and all 52 full-config
entries, the four absent-global cases and builtin restart/login subcase, and
assert exact required/executed/passed counts for both backend report labels.
These synthetic event tests do not run the E2E matrix.

The **153 masked MCP updates** are actual slash-named leaves under
`TestE2E_SecurityUrgentLegacyMCPLocalAuthorization`, with no intermediate
`masked_update`, scope or route parent events. Each of team/org/platform has
20 modern PUT, 18 legacy bulk PUT and 13 inline POST cases. They check masked
GETs, exact selected-row/key restoration rather than inherited or decoy secrets,
wrong targets/keys, nonexistent masks and changed URL/command/args/transport
boundaries. Modern omission/null preserves env and URL; empty env clears it.
Legacy invalid batches include a valid item and omitted rows, proving validation
before any save/delete. Inline creation rejects even masks of an existing row;
explicit replacement and clearing remain allowed. Persisted snapshots include
unrelated scopes, and invalid masks must not start discovery.

**Unresolved notfound inconsistency:** the four modern team cases
`masked_update/team/modern/{nonexistent_env,nonexistent_url,inherited_env,inherited_url}`
expect bounded 500 storage failures because team-store missing-row lookup returns
an error where org/platform return `(nil, nil)`. These cases remain mandatory and
check secret-free bounded errors, unchanged persistence and no discovery. They
are not authorization denial success; no 5xx is proof of authorization. Do not
count them as successful mask-validation rejection or erase them to hide the gap.

`TestE2E_SecurityUrgentFullConfigMasksLocal` (`CHAT-114`) adds **52 entries**:
40 leaves and 12 real parents (top-level, three group parents, three rejected
field parents and five denied-role parents). `/api/settings/full` GET/PUT runs
through local registered routes, JWT and tenant middleware with temporary SQLite.
An independent platform admin round-trips masked browser proxy/CDP URLs and
embedding API keys, preserving encoded/repeated URL query data and trusted VNC
settings while persisting nonsecret edits. Twenty-three invalid/nonexistent or
changed-destination cases require 400 and byte-identical configuration. Ten
masked/malformed requests from anonymous, org-owner, independent org-admin,
team-admin and member actors require 401/403, never 5xx. Six legitimate cases
cover omission, null, clearing, replacement and destination changes with explicit
clear/replacement; masked readback and persisted values are checked.

Run only these two local proofs and capture their actual Go hierarchy:

```bash
go test -tags=e2e -count=1 -timeout=3m -json -run '^TestE2E_SecurityUrgent(FullConfigMasksLocal|LegacyMCPLocalAuthorization)$' ./tests/e2e/chat_auth > /tmp/strict-mask-local.json
ASTONISH_MASK_HTTP_EVENTS=/tmp/strict-mask-local.json node --test tests/scenarios/parse-run.test.mjs
```

The local selection emits 429 run/pass entries (377 MCP + 52 full-config), not
all 594 required entries. Strict parsing of this partial stream must fail for
missing unrelated coverage. These tests do not invoke external infrastructure,
login, provider calls, a real daemon or Kubernetes; the full-config test uses
temporary SQLite regardless of the external backend selection. Local success
and synthetic complete parser events are not live PostgreSQL/SQLite acceptance.

`TestE2E_SecurityUrgentScopedMemoryCleanup` adds local SQLite HTTP/JWT proof
for `source|duplicate/personal|org/success|failure`: eight slash-named leaves plus
one required parent. It reuses the temporary SQLite registered-route fixture from
`TestE2E_Memory_LocalDeletionFailures`, validating JWTs and tenant routing normally.
The team destination is updated and retained while the authorized personal/org
participant with the same handler-visible ID is deleted exactly once. Injected
failures return a bounded 500 without `applied:true` or storage canaries, preserving
the updated destination and failed participant. Snapshots verify unrelated rows
in all seven seeded scopes remain unchanged.

MemoryStore.Add generates IDs rather than accepting caller-assigned IDs. Scoped
wrappers therefore use **ID aliasing** for Get/Delete over distinct persisted
SQLite rows; this is **not a SQL-level collision**. Successful deletion still
reaches the real personal/org database. Failure injection happens before Delete;
this proves error handling and partial persistence, not transaction rollback or
an actual SQLite engine failure. This test always uses local SQLite even in a
PostgreSQL-labeled report; it does not establish PostgreSQL, Kubernetes, a real
daemon, provider, login, or full urgent-matrix acceptance. No external Bootstrap
or acknowledgement is used. The existing six local deletion-failure cases remain
outside the urgent prefix and manifest, with their earlier-delete persistence
checks unchanged.

```bash
go test -tags=e2e -count=3 -timeout=3m -run '^(TestE2E_Memory_LocalDeletionFailures|TestE2E_SecurityUrgentScopedMemoryCleanup)$' ./tests/e2e/chat_auth
go test -race -tags=e2e -count=1 -timeout=3m -run '^(TestE2E_Memory_LocalDeletionFailures|TestE2E_SecurityUrgentScopedMemoryCleanup)$' ./tests/e2e/chat_auth
```

`TestE2E_FirstRunSetupProcessContinuity` is a separate-process **local HTTP proof**,
not selected by the `^TestE2E_SecurityUrgent` prefix and not added to the strict
manifest count. Run it without infrastructure:

```bash
go test -tags=e2e -count=1 -timeout=2m -run '^TestE2E_FirstRunSetupProcessContinuity$' -v ./tests/e2e/chat_auth
```

For `builtin` and `none`, separate setup/restart test-binary subprocesses use an
allowlisted environment, temporary SQLite and loopback HTTP with production route,
auth and tenant middleware. They check persisted identity/configuration and secret
continuity after restart. This is **not a real daemon binary** launch and provides
**no PostgreSQL setup proof**; neither this local check nor synthetic parser events
establishes live PostgreSQL/SQLite urgent-matrix acceptance.
The full role matrix remains a separate obligation; team app collaboration policy
is unrelated to settings authority.
Memory snapshots
cover unrelated rows too; these cases prove preauthorization only when executed,
not transaction rollback after injected database failures. Channel tests inspect
persisted state, not only status codes;
SMTP delivery and injected persistence failures remain separate obligations.
It rejects skips, failures, zero matches,
missing subcases, unfinished runs, pass-before-run/duplicate lifecycle events,
unknown actions, malformed JSON/events and missing successful package completion.
The manifest protects existing coverage; passing it is not proof that every
planned security scenario has already been implemented. Expand it together with
the tests in each remediation track.

Raw per-backend output and its JSON report are retained under the printed
`/tmp/astonish-security-*` path. Each successful report records the executed Go
command, Git revision (with `-dirty` for tracked changes), track, storage, runtime
and executed/passed/required/package counts for that backend. Failed runs retain
raw output and fail the command; they do not produce a success report. The full
make target succeeds only after **both** backend commands and strict reports pass.

### PostgreSQL first-run acceptance (CHAT-115)

`TestE2E_SecurityPostgresFirstRunSetup` implements real registered HTTP setup,
first-user registration/login and persisted owner/superadmin/team-admin checks,
then reopens the same config and identities in a separate test-binary process.
Both configured init endpoints must reject retries with unchanged config/identity.
This is not the real daemon launch path. Actual PostgreSQL positive execution is
still required; compilation and local rejection tests do not prove it.

In addition to the ordinary preflight, the operator must explicitly provide:

- `ASTONISH_E2E_FIRST_RUN_POSTGRES_DEDICATED=1`, approving an **entire disposable
  instance** and its cluster-wide role changes, not just a fresh database.
- `ASTONISH_E2E_FIRST_RUN_POSTGRES_INSTANCE`, the approved `host:port`.
- `ASTONISH_E2E_FIRST_RUN_POSTGRES_DSN`, securely supplied PostgreSQL URL with
  explicit user/password, matching host/port, `/postgres` and exactly one
  non-fallback `sslmode`. No routing overrides or `ASTONISH_TEST_DSN` fallback.

Make preflight checks this additional approval before any bootstrap or Kubernetes
query. Children inherit only explicit target/approval and private path variables;
PG defaults, proxies and other ambient configuration are rejected. The helper
requires matching cwd/HOME/TMPDIR and private config path. HTTP closes and tracked
audit writes drain before store close. No database/role cleanup sweep is attempted:
the operator must destroy the whole dedicated instance after success, failure or
partial setup. Supplying these variables is never automatic authorization.

Local no-infrastructure safety checks cover missing approval, malformed DSNs,
wrong target/storage, routing overrides, child isolation and rejected HTTP setup.
`TestSecurityPreflightMakeFirstRunAuthority` runs both actual Make targets in a
private copy without `.env` files. Its Go shim reenters the real preflight test;
Kubernetes and acceptance commands are trapped. Missing or mismatched first-run
authority must stop before those effects. The Node parser suite separately runs
the real Make recipes with synthetic Go/Kubernetes responses to verify both
backend selectors, reports, and failure propagation. These orchestration tests
are not live database/runtime acceptance:

```bash
go test -tags=e2e ./tests/e2eboot -run '^(TestPostgresFirstRunPreflightValidation|TestSecurityPreflightEnvironmentFirstRunAuthority|TestSecurityPreflightMakeFirstRunAuthority)$' -count=1
go test -tags=e2e ./tests/e2e/chat_auth -run '^TestPostgresFirstRun(GateBeforeEffects|IsolationBeforeEffects|SymlinkIsolationBeforeEffects|RejectionsLocal)$' -count=1
```

### Operator prerequisites (not automatic authorization)

Before running either security make target, an operator must explicitly approve:

- The actual cluster/context selected by an explicit `KUBECONFIG`. Confirm its
  current context and target cluster; do not merely point at an arbitrary file.
- An isolated, disposable, admin-capable PostgreSQL target supplied securely as
  `ASTONISH_TEST_DSN`. The harness creates/drops databases and shared roles; do
  not use a production instance. SQLite uses temporary local data but still
  invokes Kubernetes cleanup and is **not** a runtime-free alternative.
- Two distinct dedicated namespaces: the approved name pair is `astonishe2e`
  (`ASTONISH_E2E_CONTROL_PLANE_NAMESPACE`) and `astonishe2e-sandbox`
  (`ASTONISH_E2E_SANDBOX_NAMESPACE`). Eligible dedicated alternatives must match
  the preflight prefixes. Naming and namespace existence do not prove disposability.
- The `astonish-layers` and `astonish-uppers` PVCs in the sandbox namespace,
  explicitly eligible for test cleanup, Bound, and prepared with the E2E base
  layer/RBAC described below. No live deployment may share these volumes.
- `kubectl`, runtime readiness, and securely configured `BIFROST_API_KEY`,
  `OPENAI_API_KEY`, `GOOGLE_API_KEY` or `ANTHROPIC_API_KEY`, including the provider's
  normal endpoint/model configuration. Never include secret values in reports.

Only the operator supplies `ASTONISH_E2E_DEDICATED=1` after approving those actual
targets. Neither make nor the agent sets it on their behalf. Inspector reuse
(`ASTONISH_E2E_KEEP_ALIVE=1`) is rejected, not silently overridden. Run
`make test-security-preflight SECURITY_TRACK=urgent` first: it validates both
storage profiles without Bootstrap and checks namespaces/PVC status read-only.
A successful preflight is readiness evidence, **not** live security acceptance.
The bootstrap gate is also applied to every `TestE2E_Security*` test even if the
acceptance environment flag is omitted. Tests with unsafe synthetic environments
prove the gate executes before cleanup; an expected rejection is not a passed
live matrix.

### Revised remediation split

Urgent-independent acceptance preserves transcript-artifact ownership cases but
does **not** close ART-01/ART-02. The original full confined-reader contract moves
to the sandbox and final tracks: live/shell-created binary files, stopped-session
resume, live/restored previews, browser and remote-terminal compatibility, rooted
race-resistant reads and successful authorized downloads. Disabled platform file
reads remain containment, never a replacement positive test. The historical
urgent exit-2 prerequisite failure remains recorded until applicable fresh live
evidence exists. No production key rotation or receiving-MTA action is implied
by test success.

The initial authority regression covers an org-owner positive provider read,
same-team member and cross-org denied platform reads/deployment writes, and
unchanged deployment configuration after denial. This is a starting harness,
not exhaustive closure of the urgent inventory.

Configuration boundary invariants: either configured backend blocks bootstrap
before request-body processing; both init handlers share a process-local lock
through persistence (not a distributed lock). Platform deployment writes require
platform admin even over loopback; explicit non-platform behavior is retained.
Provider management responses mask copies; masked team replacements preserve
only same-team, same-instance fields, never inherited values.

## Test Types

| Type | Tag / Mechanism | External Deps | Run When | Command |
|------|----------------|---------------|----------|---------|
| **Unit** | No build tag | None | Every commit / PR | `make test-unit` |
| **Integration** | `//go:build integration` | Postgres, K8s | CI with infra | `make test-integration` |
| **E2E** | `//go:build e2e` | Postgres, live LLM, K8s | Post-release / nightly | `make test-e2e` |

---

## Choosing the Right Test Type

When adding a test for a new feature or bug fix, pick the **cheapest** layer that can actually demonstrate the bug. The cheaper the layer, the faster the feedback loop and the lower the maintenance cost. Promoting a test to a more expensive layer is justified only when the cheaper layer cannot exercise the failure path.

### Boundary rule

> **If it can be tested without a real LLM + auth + DB combo, it should not be an E2E test.**

This is the generalization of the rule in `tests/scenarios/README.md`: *"if it can be tested with mocked fetch, it's a unit test, not an E2E scenario."* The same logic applies in reverse — if a real database query is what's broken, a Go unit test with a mocked DB is not enough; promote to integration.

### Decision tree

Match the **failure shape** (what code path the bug lives on) to the test home:

| Failure shape | Test home | Build tag |
|---|---|---|
| Pure function, parser, formatter, helper | `pkg/<area>/*_test.go` | none |
| HTTP handler with mocked LLM, no DB | `pkg/api/integration_*_test.go` (despite the name, no tag — see footnote at end of doc) | none |
| Real Postgres / pgvector / SQL semantics / migrations | `pkg/store/pgstore/*_integration_test.go` | `integration` |
| Real K8s sandbox primitives (PVCs, exec) | `pkg/sandbox/*_integration_test.go` | `integration` |
| Cross-tier user journey (auth + provider + persisted session + cascade) | `tests/e2e/<feature>/*_test.go` | `e2e` |
| Frontend rendering of an SSE event sequence | `web/src/test/scenarios/*.test.tsx` + JSON fixture | none (Vitest) |
| Frontend backend integration (real `ChatRunner` + MockLLM, no HTTP) | `pkg/api/integration_scenarios_test.go` | none |
| System prompt regex contract (frontend/backend depend on a string) | `pkg/agent/system_prompt_test.go` (golden + structural) | none |
| `StudioChat` rendering invariant (e.g. report-fence gate) | Prompt contract test **plus** SSE scenario test — see `AGENTS.md` "Inline Report Rendering Contract" | none |

### Worked example: hyphen-vs-underscore tool name normalization (CHAT-070)

The cascade fix in `pkg/api/tool_discovery.go:432` had to handle MCP tool names supplied by config in hyphen form (`tavily-search`) when the live tool registry uses underscore form (`tavily_search`). The bug had three plausible test homes:

1. **Unit test** on `isWebSearchConfiguredWith` directly — cheapest, fastest, but tests only the helper in isolation.
2. **Backend integration test** with MockLLM — possible, but the bug is about static config parsing, not LLM behavior; MockLLM adds no signal.
3. **E2E test** that walks the full platform→org→team cascade and asserts `tavily_search` shows up in `GET /api/tools` — most expensive, but exercises every code path that consumes the helper.

**Decision: E2E only (CHAT-070).** The same `tool_discovery` boundary is used by the chat agent, the system prompt builder, and `/api/tools`. An E2E test that walks one of these paths transitively protects the others. A unit test would be cheaper but adds maintenance for the same bug class without catching anything the E2E doesn't.

**When the unit test would have been the right call instead:** if the helper had complex branching (e.g. five aliases, regex normalization, locale rules) that the E2E couldn't exhaustively cover with a single fixture, the cost calculus inverts — unit-test the matrix, then add a single E2E to prove the wiring.

### When to add to the YAML scenario catalog

Every test under `//go:build e2e` that represents a **user-facing journey** (not an infrastructure smoke check) MUST have a matching entry in `tests/scenarios/<view>.yaml` with a stable ID, and the test function MUST carry a `// COVERS: <ID>` comment ≤5 lines above its `func Test...` declaration. The `make scenario-coverage` reconciler enforces this by greppping for `COVERS:` and cross-checking against the catalog. The ≤5-line rule is enforced by `tests/scenarios/parse-run.mjs:60`.

Tests that exercise only platform plumbing (e.g. `TestE2E_StandardMCPInstall_PlatformEncryptionEnvelope`) do not need a YAML entry — the catalog is for user journeys.

---

## Running Tests

### Unit Tests

Fast, deterministic, no external dependencies. Uses mocks for LLM, DB, and network.

```bash
make test-unit
```

This runs:
- `go test ./...` — all Go tests without build tags (includes handler integration tests that use MockLLM)
- `cd web && npm test -- --run` — all frontend vitest tests (component, API, util, SSE scenario tests)

**Prerequisites:** Go 1.24+, Node.js 20+

### Integration Tests

Tests that need real infrastructure (Postgres, K8s) but still mock AI providers.

```bash
make test-integration
```

This runs:
- `go test -tags=integration -count=1 -timeout=10m ./...`

**Required env vars:**
- `ASTONISH_TEST_DSN` — Postgres admin connection string (e.g. `postgres://user:pass@localhost:5432/testdb`)
- For K8s sandbox tests: `kubectl` access to a cluster with sandbox namespace

### E2E Tests

Full end-to-end tests that bootstrap a complete Astonish platform: fresh database, real `StudioServer`, real auth flow, real LLM provider (via platform settings). Every E2E test uses the same framework (`tests/e2eboot`).

```bash
make test-e2e
```

This runs:
- `go test -tags=e2e -count=1 -p 1 -timeout=15m ./tests/e2e/...`

The `-p 1` flag serializes package execution. This prevents Postgres role creation
races (`astonish_app` is a shared role that `BootstrapPlatform` creates/alters).

**Required env vars:**
- `ASTONISH_TEST_DSN` — Postgres admin connection string
- Provider API key (at least one of: `BIFROST_API_KEY`, `OPENAI_API_KEY`, `GOOGLE_API_KEY`, `ANTHROPIC_API_KEY`)
- For sandbox tests: `kubectl` access to a cluster + provisioned e2e infra (see below)
- `ASTONISH_E2E_CONTROL_PLANE_NAMESPACE` (default `astonish`) — control-plane namespace expected by `e2eboot`
- `ASTONISH_E2E_SANDBOX_NAMESPACE` (default `astonish-sandbox`) — sandbox namespace expected by `e2eboot`

Tests that don't exercise sandboxes (e.g. `flow_assistant`) don't need kubectl — but the platform config still includes sandbox settings for consistency.

### E2E K8s Sandbox Infrastructure

Sandbox-aware E2E tests (any test that exercises `write_file`, `edit_file`, `shell_command`, or any other tool routed through the sandbox runtime) require an isolated set of K8s namespaces, PVCs, and a seeded `@base` layer. **The e2e suite MUST own this infrastructure exclusively** — never reuse a live install (e.g. `astonishdev`, production), or you destroy both isolation and the test's value.

The repo ships everything needed:

- `deploy/helm/astonish/values-e2e.yaml` — values file installing only the sandbox slice of the chart (api/worker `replicaCount: 0`) under the `astonishe2e` prefix.
- `make e2e-k8s-up` — provisions namespaces, RBAC, PVCs, and runs the seed Job.
- `make e2e-k8s-down` — uninstalls the helm release and force-deletes both namespaces.

```bash
# One-time setup per cluster (or after `make e2e-k8s-down`)
make e2e-k8s-up

# Run the e2e suite — uses the env vars from .env to find the namespaces
make test-e2e

# Optional cleanup
make e2e-k8s-down
```

Namespaces created:

| Resource | Name |
|----------|------|
| Control plane | `astonishe2e` |
| Sandbox | `astonishe2e-sandbox` |
| Layers PVC | `astonish-layers` (in `astonishe2e-sandbox`) |
| Uppers PVC | `astonish-uppers` (in `astonishe2e-sandbox`) |
| Helm release | `astonishe2e` |

These names MUST NOT collide with any live install. If your dev install uses `astonishdev`/`astonishdev-sandbox` (per `values-dev-proxmox.yaml`) the e2e suite stays disjoint by default.

The seed Job tars the `astonish-sandbox-base:dev` rootfs into `astonish-layers/@base/rootfs`. First `make e2e-k8s-up` is slow (image pull + tar onto NFS); subsequent runs reuse the seeded `@base`.

**Seed verification.** The seed Job uses `helm.sh/hook-delete-policy: hook-succeeded`, so the Job and its pod are garbage-collected immediately after success — there is nothing left to `kubectl wait` on. Instead, `make e2e-k8s-up` invokes `scripts/verify-e2e-seed.sh`, which spins up a short-lived busybox pod, mounts the layers PVC read-only, and asserts that `/@base/rootfs/usr/bin` exists and is non-empty. This is a positive end-to-end check (the bytes are really on disk) rather than a process-level proxy.

**Test-time preflight.** `make test-e2e` will not blindly run against a missing or partial install. Before invoking `go test`, it verifies (Option A — fail loudly):

1. `kubectl` is on `PATH`
2. The control-plane namespace exists (`ASTONISH_E2E_CONTROL_PLANE_NAMESPACE`, default `astonish`)
3. The sandbox namespace exists (`ASTONISH_E2E_SANDBOX_NAMESPACE`, default `astonish-sandbox`)
4. Both PVCs (`astonish-layers`, `astonish-uppers`) exist in the sandbox namespace

On any miss it prints the exact command needed (`make e2e-k8s-up`) and exits non-zero — there are no silent skips and no auto-provision. Provisioning is always an explicit action by the developer or CI.

### Streamed test output

`make test-e2e` consumes Go's `-json` test events through `tests/scenarios/stream.mjs` to render results live, instead of waiting until the suite ends. Each package emits a header (`▶ tests/e2e/<pkg>`) and each test prints `· <name>` when it starts and a colored `PASS`/`FAIL`/`SKIP` line with elapsed seconds when it finishes. A summary table follows at the end with totals and the list of failing tests. The raw `-json` stream is still preserved verbatim at `/tmp/e2e-results.json` for `parse-run.mjs` and post-mortem inspection.

The same streamer is reused by `make test-e2e-inspect`. Exit status is preserved end-to-end — failing tests propagate to make.

### Inspecting test runs in the UI

E2E tests normally run isolated: every test bootstraps a fresh platform DB, fresh per-test org DBs, runs, and tears them down. Useful for CI but unhelpful when a test fails in a way that requires browsing chat sessions, reports, or audit logs.

`make test-e2e-inspect` reuses the same suite but runs it against a long-lived inspector instance:

```bash
make test-e2e-inspect          # boot inspector, run suite, leave inspector running
# … browse http://localhost:9394 (or via ssh -L) …
make test-e2e-inspect-stop     # stop inspector, drop databases, clean tmp files
```

`make test-e2e-inspect-stop` is fully idempotent: it kills any running inspector (using the PID file or `pgrep` as fallback), then runs `e2e-inspector --cleanup` which drops every `astonish_e2einspect_*` database and removes `/tmp/astonish-e2e-inspect-*` directories and state files. Safe to run multiple times, on a clean system, or when only orphaned databases remain (e.g. from a process that was killed without going through the make target).

Architecture:

- A separate binary `bin/e2e-inspector` (built from `tools/e2e-inspector`) boots a `StudioServer` on **port 9394** with `InstanceSuffix=e2einspect`. It writes `/tmp/astonish-e2e-inspect.json` (state) and `/tmp/astonish-e2e-inspect.pid` (PID), then blocks on `SIGINT`/`SIGTERM`.
- The make target sets `ASTONISH_E2E_KEEP_ALIVE=1` for the test process. `e2eboot.Bootstrap(t)` detects this, reads the state file, and *attaches* to the running inspector instead of starting a new server.
- **Package-scoped seeding (Plan D)**: instead of one fresh world per test, all tests in the same Go package share one acme+globex pair. `Bootstrap()` uses `runtime.Callers` to derive a stable suffix from the test file path (e.g. `chatauth`, `chatcore`). The first test in the package provisions orgs/users/teams/data via `Seed()`; subsequent tests detect the existing world and reuse it. After a full suite run there are exactly 5 organisations: `default`, `acme-chatauth`, `globex-chatauth`, `acme-chatcore`, `globex-chatcore`.
- In shared mode `provisionOrg` skips its `t.Cleanup(DecommissionOrg)` so all data persists for the developer to browse.
- `TestE2E_SandboxLayerChain` is incompatible with shared seeding (mutates global `sandbox_templates` rows and the `astonish-layers` PVC) and skips itself when `ASTONISH_E2E_KEEP_ALIVE=1`. Run it via `make test-e2e` instead.

#### Logging in to browse data

Two login paths are available:

- **As the bootstrap user** — `e2e@test.local` / `E2ETest2024!`. This user is `platform_role=superadmin` and a member of `default` only. Useful for platform-level admin pages (settings, users, providers).
- **As a seeded test actor** — Alice/Bob/Carol/Dave/Eve. Each package has its own copy with a plus-tagged email and the same password. Use these to see the world from the test's perspective, including chat sessions:

| Email | Password | Org | Team | Role |
|---|---|---|---|---|
| `alice+chatauth@acme.test` | `E2ETestSeed2024!` | acme-chatauth | red | owner / admin |
| `bob+chatauth@acme.test` | `E2ETestSeed2024!` | acme-chatauth | red | member |
| `carol+chatauth@acme.test` | `E2ETestSeed2024!` | acme-chatauth | blue | member |
| `dave+chatauth@globex.test` | `E2ETestSeed2024!` | globex-chatauth | engineering | owner / admin |
| `eve+chatauth@globex.test` | `E2ETestSeed2024!` | globex-chatauth | engineering | member |
| `alice+chatcore@acme.test` | `E2ETestSeed2024!` | acme-chatcore | red | owner / admin |
| … (same shape for chatcore) | | | | |

The shared password is `e2eboot.SeededUserPassword` and is the same for all seeded users in all packages.

The `make test-e2e-inspect` post-run footer prints this table dynamically by listing the seeded users actually present in the platform DB. You can also re-print it any time the inspector is running with:

```bash
bin/e2e-inspector --info
```

`flow_assistant` tests do not call `Seed()`; they exercise only the bootstrap user. `sandbox_layerchain` is skipped under `ASTONISH_E2E_KEEP_ALIVE=1`.

What each login can see in the UI:

- **Bootstrap user**: `default` org only. No test data.
- **Alice (chatauth)**: `acme-chatauth` org. Org-scoped settings, audit log, the team(s) she belongs to, team memories/skills/MCP/credentials, her personal memories/credentials/sessions.
- **Eve (chatauth)**: `globex-chatauth` org and its `engineering` team — the adversarial perspective most boundary tests use to verify isolation.
- Cross-org or cross-package switching is **not** possible — the `/api/orgs/switch` endpoint requires membership, and seeded users are members of exactly one org by design (matches what tests assert).

Sessions are per-user. When `ASTONISH_E2E_KEEP_ALIVE=1` is set (which `make test-e2e-inspect` does automatically), tests skip their **hygienic** session DELETEs so the data they created remains browsable in the UI. The helper `e2eboot.RetainSessions()` gates this behavior. **Asserted** DELETEs — tests that explicitly verify deletion semantics (e.g. CHAT-026 "another user cannot delete", `TestE2E_Chat_SessionCreateListDelete`) — still run their delete unconditionally because the DELETE response itself is the subject of the assertion. Net effect: log in as Alice and you'll see sessions from `TestE2E_Chat_SendAndReceive`, `TestE2E_Chat_MultiTurnContext`, the artifact tests, etc.; you won't see sessions from tests where deletion is the test.

#### Resource sizing

Postgres pools are deliberately small in inspect mode (`MaxOpenConns=2`, `MinConns=0`). With Plan D the inspector now keeps only ~5 long-lived org pools (down from ~52 in the per-test design), so connection pressure on Postgres is low and `make test-e2e-inspect` finishes much faster on re-runs (~135s vs ~376s) because subsequent tests in a package skip provisioning.

Each `make test-e2e-inspect` always starts with a fresh inspector platform DB (`DropExisting=true`). Re-running individual tests against the running inspector also works: `Seed()` is idempotent in shared mode and detects existing data, so re-runs cost milliseconds rather than seconds.

If you see `FATAL: sorry, too many clients already`, drop any orphaned `astonish_<old-suffix>_*` databases left behind by killed runs (they hold idle connections):

```sql
SELECT datname, count(*) FROM pg_stat_activity GROUP BY datname ORDER BY count(*) DESC;
DROP DATABASE astonish_<old-suffix>_<orgname>;
```

### Using `.env` Files

The Makefile auto-loads `.env` and `.env.local` from the repo root (if present). This avoids re-exporting credentials every shell session.

```bash
cp .env.example .env        # Shared defaults (DSN)
# Edit .env with your values

# Optional: per-developer secrets (API keys)
touch .env.local
echo "BIFROST_API_KEY=your-key-here" >> .env.local
```

**Precedence:** shell env vars > `.env.local` > `.env`

Both files are gitignored. Only `.env.example` is committed as a reference template.

**Caveat:** Make's `include` does not support shell interpolation (`${VAR}`) inside `.env` files.
Each line must be a simple `KEY=VALUE` assignment (no quotes needed for simple strings, but
quotes are tolerated: `KEY="value with spaces"`).

### Scenario Coverage Report

Track which user-facing scenarios have passing tests:

```bash
make scenario-coverage
```

The catalog lives in `tests/scenarios/` (one YAML file per view: `chat.yaml`, `flows.yaml`, `fleet.yaml`, `drill.yaml`, `apps.yaml`). See `tests/scenarios/README.md` for full details on how to add and mark scenarios.
### Docker Test Environment

To bring up an isolated Docker environment for running integration/e2e tests:

```bash
make e2e-env-up       # Start isolated test environment
make e2e-env-down     # Stop test environment
make e2e-env-rebuild  # Rebuild and restart test environment
```

---

## E2E Framework (`tests/e2eboot`)

All E2E tests use the shared `e2eboot.Bootstrap(t)` harness. It provides:

- **Fresh platform DB** per test (suffix derived from `crc32(t.Name())` for parallel safety)
- **Real `StudioServer`** on a random port
- **Real auth** — registers a default user and returns a Bearer token
- **Provider seeded** in platform settings (Bifrost via `BIFROST_API_KEY`)
- **Full config** including sandbox/K8s settings (always wired, tests opt in to using them)
- **Automatic cleanup** — database dropped, server shut down via `t.Cleanup`

### Usage

```go
//go:build e2e

package my_feature

import (
    "testing"
    "time"

    "github.com/SAP/astonish/tests/e2eboot"
)

func TestE2E_MyFeature(t *testing.T) {
    h := e2eboot.Bootstrap(t)

    // Authenticated POST
    resp := h.Post(t, "/api/studio/some-endpoint", map[string]string{"key": "value"})
    defer resp.Body.Close()

    // SSE endpoint (reads full stream)
    events := h.SSE(t, "/api/ai/chat", reqBody, 90*time.Second)
    completeEvent := e2eboot.FindEvent(events, "complete")

    // Sandbox pod assertions
    sessionID, podName := h.ChatAndWaitForPod(t, "Run echo hello")
    e2eboot.AssertCommandPresent(t, podName, "node", "should have node")
    h.CleanupSession(t, sessionID, podName)
}
```

### Parallelism

E2E tests currently run sequentially within their package because `Bootstrap` uses
`t.Setenv("XDG_CONFIG_HOME", ...)` to point `config.LoadAppConfig()` at a per-test
config directory. Go's testing framework forbids `t.Setenv` + `t.Parallel()`.

Each test still gets its own isolated DB (per-test suffix), so there is no shared
state between tests. If the config-loading mechanism is refactored to accept an
explicit path (rather than relying on env vars), parallel execution can be enabled.

---

## Adding New Tests

**Before writing the file, decide which layer.** Walk the [Choosing the Right Test Type](#choosing-the-right-test-type) decision tree above. The cost of putting a test in the wrong layer is real — a test that lives in `tests/e2e/` when it could have been a unit test pays the e2e tax (DB bootstrap, real LLM key, kubectl) on every CI run, forever.

### Adding a unit test

Drop a `*_test.go` file in the relevant Go package (no build tag), or a `*.test.ts` / `*.test.tsx` in the relevant frontend `__tests__/` directory. `make test-unit` picks it up automatically.

### Adding an integration test

Add `//go:build integration` at the top of the file. Use `t.Skip` with a helpful message if a required env var is missing:

```go
//go:build integration

package mypackage

func TestMyIntegration(t *testing.T) {
    dsn := os.Getenv("ASTONISH_TEST_DSN")
    if dsn == "" {
        t.Skip("ASTONISH_TEST_DSN not set; skipping integration test")
    }
    // ...
}
```

### Adding an E2E test

Create a new package under `tests/e2e/<feature>/` and use the harness:

```go
//go:build e2e

package my_feature

import (
    "testing"
    "github.com/SAP/astonish/tests/e2eboot"
)

// COVERS: CHAT-NNN
func TestE2E_MyFeature(t *testing.T) {
    h := e2eboot.Bootstrap(t)
    // Use h.Post, h.SSE, h.Get, h.Delete, h.ChatAndWaitForPod, etc.
}
```

The test is automatically discovered by `make test-e2e` (runs `./tests/e2e/...`).

**If the test represents a user journey**, also:

1. Add an entry in `tests/scenarios/<view>.yaml` (one of `chat.yaml`, `flows.yaml`, `fleet.yaml`, `drill.yaml`, `apps.yaml`) with a fresh stable ID, `status: covered`, and a `test_refs:` pointer to the test function.
2. Add `// COVERS: <ID>` ≤5 lines above the `func Test...` declaration. The reconciler at `tests/scenarios/parse-run.mjs:60` enforces this distance — comments further away are not associated with the test.
3. Run `make scenario-coverage` to verify the catalog and the COVERS comments agree.

Tests that protect platform plumbing rather than user journeys (e.g. encryption envelope shape, role provisioning) do not need a YAML entry.

---

## File Organization

### Backend (Go)

```
pkg/
├── api/
│   ├── *_test.go                          # Unit tests (MockLLM, httptest)
│   ├── integration_*_test.go              # Handler integration tests (MockLLM, no build tag — these are unit tests by our taxonomy)
│   └── *_integration_test.go              # DB-dependent tests (//go:build integration)
├── store/pgstore/
│   └── *_integration_test.go              # PG-dependent tests (//go:build integration)
└── ...

tests/
├── e2eboot/                               # Shared E2E harness (//go:build e2e)
│   ├── bootstrap.go                       # Bootstrap(t) → *Harness
│   ├── http.go                            # Post/Get/Delete helpers
│   ├── auth.go                            # registerUser, loginUser
│   ├── sandbox.go                         # Pod helpers (wait, assert, cleanup)
│   └── sse.go                             # SSE stream parsing and helpers
└── e2e/
    ├── sandbox_layerchain/
    │   └── layerchain_test.go             # Full sandbox layer-chain pipeline (NOT parallel)
    └── flow_assistant/
        └── flow_assistant_test.go         # Flow AI assistant (parallel)
```

### Frontend (Vitest)

```
web/src/
├── api/__tests__/                         # API client unit tests
├── components/__tests__/                  # Component unit tests
├── hooks/__tests__/                       # Hook unit tests
├── utils/__tests__/                       # Utility unit tests
└── test/
    ├── setup.ts                           # Vitest setup (jest-dom, matchMedia mock)
    ├── fixtures/scenarios/                # SSE scenario JSON fixtures
    └── scenarios/                         # SSE scenario integration tests
```

---

## Key Patterns

### MockLLM (Backend Unit Tests)

The `pkg/api/mock_llm_test.go` file provides a reusable `MockLLM` that implements `model.LLM`. Use the builder functions:

```go
mockLLM := NewMockLLM(
    StreamChunk("Here is "),
    StreamFinal("the answer."),
)
```

Available builders: `TextTurn`, `TextTurnWithUsage`, `ToolCallTurn`, `MultiToolCallTurn`, `StreamChunk`, `StreamFinal`, `ErrorTurn`, `EmptyTurn`.

### Provider Injection (AIChatHandler Unit Tests)

`AIChatHandler` uses `getProviderFn` (a package-level variable) to obtain an LLM client. Unit tests override this to inject MockLLM:

```go
func TestAIChatHandler_LargePayload(t *testing.T) {
    original := getProviderFn
    defer func() { getProviderFn = original }()

    getProviderFn = func(ctx context.Context, ...) (model.LLM, error) {
        return NewMockLLM(TextTurn(largeYAML)), nil
    }
    // ... httptest.NewRequest + httptest.NewRecorder ...
}
```

E2E tests do NOT use this injection — they go through the real platform settings path.

### Frontend SSE Testing

Mock `globalThis.fetch` with a `ReadableStream` that emits controlled chunks:

```typescript
const encoder = new TextEncoder()
const stream = new ReadableStream({
    start(controller) {
        controller.enqueue(encoder.encode('event: chunk\ndata: {"content":"hi"}\n\n'))
        controller.close()
    },
})
globalThis.fetch = vi.fn().mockResolvedValue({ ok: true, body: stream })
```

---

## Naming Conventions

- `*_test.go` — standard unit test (no tag)
- `*_integration_test.go` with `//go:build integration` — needs external infra
- `tests/e2e/<feature>/*_test.go` with `//go:build e2e` — full platform E2E
- `web/src/**/__tests__/*.test.ts` — frontend unit test
- `web/src/test/scenarios/*.test.tsx` — frontend SSE scenario test

---

## Note on Existing `integration_*_test.go` Files

The files `pkg/api/integration_test.go`, `integration_scenarios_test.go`, `integration_expanded_test.go`, and `integration_gaps_test.go` use `MockLLM` and have **no build tag**. Despite their "integration" naming, they run under `make test-unit` (no external deps needed). This naming predates the three-tier taxonomy. They are effectively handler-level unit tests that wire multiple internal components together with mocked boundaries.
ries.
