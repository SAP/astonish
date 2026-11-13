# Bot release acceptance

## Strict agent all-stage gate (not yet live-certified)

```sh
ASTONISH_BOTS_TEST_DB_ISOLATED=1 \
ASTONISH_TEST_DSN="$DISPOSABLE_BOT_TEST_DSN" \
python3 -B tests/e2eboot/employee_http_acceptance.py --agent-stage all \
  --config "${LOCAL_BOT_AGENT_CONFIG:?Set an explicit private isolated configuration}"
```

`bots_acceptance.sh --agent-stage all --config /absolute/private/config.json`
forwards to the same gate. `--preflight`, `--local-only`, other stage families,
and `--state` cannot turn this into a partial pass. Individual `execution`,
`progress`, `learning`, `continuity`, and `postgres` stages remain available;
they do not constitute full agent acceptance and do not accept `--config`.

The all-stage config must already exist as an absolute, private (0600),
non-symlink JSON file using the launcher's version 1 provider configuration.
It must identify the intended AI Core provider/model and explicit executable,
browser, base-layer and verified embedding assets. The driver validates it
before any test subprocess or paid call; missing files, malformed data, wrong
provider identity, and missing disposable PostgreSQL opt-in block with exit 2.
There is no ambient config/credential fallback and no normal-daemon discovery.
Validation errors are redacted rather than echoing secret-bearing values/paths.

Every invocation runs these dependent gates, stopping on the first failure:

1. Exact controlled `execution`, `progress`, and `learning` HTTP suites.
2. Exact SQLite/scheduler `continuity` and guarded integration `postgres` suites.
3. `npm run build` in `web`, then
   `bot_skill_surface_acceptance.py --browser --terminal --agent` against built
   Studio and the real remote-command PTY.
4. A fresh `local_bot_smoke_launcher.py --verify-bot-agent --config CONFIG`,
   without state reuse or any employee/quality/skills fallback mode. The launcher
   must validate its own current-invocation live receipts and provider parity;
   an unavailable journey or failed sample is a failure, not a diagnostic pass.

No historical receipts or restored evidence satisfy this orchestration. Go gates
use `-count=1`, exact package/test run-pass evidence, and reject skipped, missing,
foreign or duplicate events. Provider secrets and production database/cluster
settings are not passed to controlled suites; only the explicit disposable test
DSN reaches PostgreSQL tests. The live launcher receives the explicit config path,
not copied credentials. A successful all-stage result is agent acceptance,
not Kubernetes certification or the unrelated full Bot release gate. Python
harness unit tests alone do not certify provider, browser, PTY or database behavior.


## Retained-source inspection in the fresh agent invocation

After capturing the three stock samples and remaining journey evidence, the agent
browser harness saves `browser-evidence.json` and emits
`STAGE=agent-grounding-inspection` with the assessment path and deadline. If all
three stock samples are `diagnostic-observed`, it waits for
`ROOT/agent-grounding-assessment.json`, a regular non-symlink file with mode 0600.
The optional `LOCAL_BOT_AGENT_INSPECTION_TIMEOUT_SECONDS` environment variable
sets this bounded wait (default 600 seconds, integer range 1..1800); the launcher
already inherits it into the browser. Invalid configuration is rejected before
agent work starts. Failed or incomplete stock diagnostics skip the wait, not the
final semantic checks.

An accountable human or agent must inspect the retained answers and fetched source
bodies, then atomically publish the assessment using the exact schema in
`agent_grounding_contract.py`. Bind it to the current nonce, candidate hashes,
run identities and source digests; inventory all answer content, decisive source
quotations and temporal support. Record unsupported/unverified claims honestly.
Each sample also requires a `task_adequacy` judgment under `bot-agent-grounding-v2`:
`task` must equal the original `Stock Market News` prompt, `useful_full_result`
must be the JSON boolean `true`, and `rationale` and `limitations` must be
nonblank. The named human/agent evaluator must explain why the complete answer
usefully fulfills the original task, not merely why some peripheral facts are
supported, and assess disclosed limitations against that judgment. There is no
required recommendation or topic count. Negative or missing adequacy fails even
when every claim is supported; an honest partial result is not full acceptance.
See `README.agent-candidate.md` for the exact schema.
This checkpoint generates no assessment, makes no model calls while waiting,
and grants no passing receipt. File arrival only permits structural validation
of the accountable evaluator's recorded semantic judgments; the code cannot
establish entailment or task adequacy mechanically. Malformed, unsupported or
inadequate assessments still fail. Missing assessment
at the deadline or unsafe file permissions block final acceptance in this
invocation; a later file cannot rescue it. This is acceptance tooling only, not a
production Bot reviewer or publication prerequisite.

## Owner messages during scheduled work

Ordinary employee messages use the native Home composer without waiting for global
execution idle. Every-minute reports may take longer than a minute; queued or
running recurring work must not prevent the harness from exercising owner-priority
admission. The harness still clicks once, requires the exact persisted owner event
and new manual Home run, and refuses replay. It does not pause or edit schedules to
manufacture an idle window. Physical idle/deletion barriers and source-termination
checks before operation application remain unchanged.

Controlled native-browser regression (not real-provider completion proof):

```sh
ASTONISH_MEMORY_BROWSER_ACCEPTANCE=1 go test ./tests/e2eboot/chat_first_browser -run 'TestEmployeeOwnerSend.*' -count=1 -v
```

This keeps recurring rows running/queued while one native Send POST admits the
exact queued owner run; a historical identical message cannot supply admission.

## Fresh real-model quality contract (not yet live-certified)

After the broad backend/frontend/lint and strict controlled publication, runtime,
browser and PTY gates pass, run **without `--state`**:

```sh
LOCAL_BOT_STARTUP_TIMEOUT_SECONDS=300 python3 -B tests/e2eboot/local_bot_smoke_launcher.py --verify-bot-quality --config .tmp/bot-employee-acceptance.json
```

This is a distinct `bot-quality-live-v1` contract, not a reinterpretation of old
employee evidence. The existing explicit config must provide the Linux ARM64
candidate and Docker CLI, native browser, read-only base layers, pinned embedding
cache and real provider URL/model/key. Config is validated without printing its
secrets; no credentials or shared configuration are changed. Missing prerequisites
block. `--state`, refresh/restart flags and other verification modes are rejected.
The quality driver has a bounded 60-minute outer deadline (individual stages keep
shorter deadlines); no normal `:9393` daemon or shared Docker cleanup is involved.

All **17 original employee checks** still execute in this fresh journey and are
retained under `employee` in the final evidence. In addition, these six exact
quality receipts are mandatory:

- `persistent-requirements`: original assignment plus saved goal identity/revision.
- `supplied-draft-corrected`: real-model inspection/correction of a supplied stale,
  unsourced, incorrect weather sample using captured observations and a committed
  accepted quality report. The harness does not tell the reviewer what verdict to
  emit, or assume the probabilistic producer must fail its first internal review.
- `three-offline-assessed-results`: three new exact-trigger executions after actual
  browser closure and the owned daemon restart, read from scoped SQLite before login.
- `live-reload-final-only`: the three canonical reports each appear once on login
  and reload; raw run drafts and the supplied defective sample cannot be assistant
  Home answers.
- `cadence-preserves-requirements`: the cadence-only change leaves the exact source
  goal identity, revision and content unchanged (run-specific contract digests are
  deliberately not compared across occurrences).
- `owner-update-next-run`: a later owner criterion revision (wind speed) governs a
  fresh scheduled report without another owner reminder.

Every quality receipt binds the launch nonce, Bot/Home, daemon/browser hashes,
source filename and SHA-256 of actual saved bytes. Missing/skipped/duplicate,
foreign, old-candidate and manually resumed evidence fails closed. Observers only
read persisted private Work and public committed reports; they never inject quality
verdicts or mutate database state. Model semantic assessment is not factual proof;
controlled HTTP tests remain responsible for deterministic fail→repair→pass,
uncertainty and exhaustion coverage. Production external effects, deployed/Kubernetes
certification and PostgreSQL coverage are separate obligations.

Both employee and quality title gates read real tool evidence from the private
run-session stream, not Home. Persisted run/Bot/session/Home/source identities and
source invocation must agree; HTTP/web-fetch call IDs, names and successful bodies
must pair, and the fetched URL must match a captured report observation. Home must
contain the exact canonical `bot-report:<run>` publication once, with no private
draft/tool messages. Model quality assertions alone never satisfy this gate.

Read-only replay of a captured title failure (does not certify live DOM):

```sh
ASTONISH_EMPLOYEE_PRIVATE_EVIDENCE=/absolute/owned/evidence/root go test ./tests/e2eboot/chat_first_browser -run TestEmployeePrivate -count=1
```

Harness-only checks (no live launch/provisioning):

```sh
python3 -B -m unittest discover -s tests/e2eboot -p '*_test.py'
go test ./tests/e2eboot/chat_first_browser -run 'TestQuality|TestEmployee' -count=1
```

## Owned launcher readiness and diagnostic resume

`LOCAL_BOT_STARTUP_TIMEOUT_SECONDS` sets startup and restart readiness waiting:
default **300 seconds**, integer range **1–900**, rejected in preflight otherwise.
Measured local tool-index initialization was 165–180 seconds. Ten-second stderr
heartbeats classify the owned daemon log (sandbox wiring → pending tool index →
completed index → serve handoff), never log credentials. Readiness requires HTTP
200 plus typed `/api/auth/setup-status` JSON (`initialized` boolean, builtin auth),
not `/health`: that path can return the Studio HTML fallback with status 200.
Ownership/process failures propagate; transient HTTP errors expire with a nonzero
result. Registration/login/provider setup remain separate, named, bounded steps.

`--verify-bot-employee --config CONFIG --state OWNED/state.json` is a **diagnostic
resume**, requiring canonical state and matching live owned/config candidate hashes.
It never repins binaries automatically, imports legacy Bots, or replaces a fresh
invocation. Successful resumed state is `diagnostic-verified`, not `verified`.
Provider publication still uses authenticated platform settings after readiness;
no direct DB seeding or private-scope override is permitted.

## Fresh existing-Bot adoption and historical schedule preservation

The fresh `--verify-bot-employee --config CONFIG` invocation requires **17 receipts**:
the original 14 gates, unchanged, plus `existing-bot-read-no-authority`,
`existing-bot-adoption`, and `historical-disabled-schedule`. Old 14/14 evidence is
not sufficient. No historical artifact is upgraded or rewritten.

During owned setup only, the authenticated service creates a separate inert Bot
(`start_from_title=false`) and a disabled June cron trigger. Setup submits no model
turn, grants no mandate, and never writes the database. The browser opens its Home
and reads list/snapshot/version/history; private read-only observation must still
show no mandate heads, runs, or operations. A new natural owner message then asks
for a separate weekly reminder. Admission must bind that exact persisted owner
event to a new successful Home-interactive run carrying a persisted mandate and
an applied scheduling operation, without synthetic confirmation or approval.

The historical trigger must remain byte-for-field equivalent to the initial
`TriggerSpec`, with its original scheduler identity paused, no next run, and no
historical execution. Required observations: fixture, read, schedule,
restart-active, cadence, pause, restart-paused, resume, restart-resumed. The new
weekly reminder is changed Monday → Tuesday, paused, and resumed using natural
browser messages. Exactly two triggers/jobs remain; cadence edits cannot create a
third job or reuse the historical identity. Existing owned restarts are reused;
no additional paid due-boundary wait is introduced. The deletion preservation
peer and all original employee gates remain independent.

Local readiness checks (no provider or user daemon):

```sh
python3 -m unittest discover -s tests/e2eboot -p '*launcher_test.py'
go test ./tests/e2eboot/chat_first_browser -count=1
ASTONISH_MEMORY_BROWSER_ACCEPTANCE=1 go test ./tests/e2eboot/chat_first_browser -run '^TestEmployeeLegacyAdoptionBrowser$' -count=1 -v
```

The native browser test uses an isolated controlled HTTP/DOM fixture to check the
real sender, exact admission binding, and single submission. It is **not** proof
of actual-provider adoption or schedule preservation on a fresh deployed run;
that release acceptance remains pending until the full fresh invocation passes.

## Employee-operation strict selectors — 2026-11-08

The `daemon-http` gate runs `TestBotAuthorizedScheduleHTTPAcceptance` and
`TestBotEmployeeOperationsHTTPAcceptance` in **`./pkg/daemon`**, not `./pkg/api`.
The PostgreSQL gate includes `TestIntegrationPostgresBotEmployeeOperations`,
which fails without explicit owned disposable-server consent and exercises actual
private/team routing. Named `go test -json` run/pass evidence is mandatory; empty
selectors, no-tests warnings, missing names and skips are not acceptance.
The current isolated PostgreSQL run exposes concurrent schedule-insert conflicts;
see `.tmp/employee-final-gates.md` for command outcomes and cleanup. UI implementation
is present, but fresh employee-operation **LIVE acceptance remains pending**.
Historical 9/9 receipts below do not establish this contract or close release gates.

## Chat-first mandatory slice

The browser gate now requires `TestBotChatFirstWorkspaceBrowserAcceptance`,
`TestBotChatFirstOrderedConversationBrowserAcceptance`,
`TestBotChatFirstConversationHTTPAcceptance`, `TestBotProposalRoutesStrictHTTP`,
`TestBotSourceIntakeProposalHTTPAcceptance`, `TestBotSourceIntakeAdaptiveHTTP`, and
`TestBotSourceIntakeTemplateHTTP`, in addition to all legacy browser selectors.
The existing mandatory `TestBotParityPTYAcceptance` now exercises Chat-first
messages, exact proposals and combined approvals; explicit-intent CLI tests remain
mandatory compatibility coverage. Missing or skipped names fail closed.

These are controlled HTTP/model, browser and terminal acceptance, not proof of a
real deployed provider/sandbox. Wiring tests do not run the selected Go suites.
See [current contracts](../../docs/architecture/bot-chat-first.md). Historical
local results do not establish this slice. The scoped local smoke now has 9/9
same-candidate receipts after safe checkpoint resumes; it is not a fresh uninterrupted
single invocation. All ten larger deployed scenarios remain unaccepted/missing.
No schema generator or DB
reset is needed for the additive JSON contracts. Use this driver for safe full
`make-test` verification rather than sourcing production `.env` files.

Run from any directory:

```sh
bash tests/e2eboot/bots_acceptance.sh --preflight
bash tests/e2eboot/bots_acceptance.sh
```

The shell entry point uses the adjacent Python 3 standard-library driver. No
credentials are sourced and the driver never deploys a cluster, switches its
context, pulls an image, or provisions a database. Supply prerequisites explicitly
in the caller's environment; **never source a production `.env`**. The repository
Makefile is copied to an owned temporary directory with only its `.env` and
`.env.local` includes removed. Its real `test` and `lint` targets remain intact.

## Isolated Chat-first smoke: scoped 9/9 verified

At **21:32 UTC on 2026-11-06**, launcher `d00eb582` exited **0** and independent
ownership/evidence validation passed all nine receipts on the same unchanged
candidate `795786c9f627555a8562b2df7ef6c01019450fceb6837f30b1d8530af0ed20b9`.
Evidence: `.tmp/astonish-chat-first-08xel0sl/browser-evidence.json` and
`/tmp/chat-first-paused-stage-recovery.md`. Bot
`196936cd-488f-4812-8a41-de09911e70d2` remains paused at
`http://127.0.0.1:63512/#/chat/bots/personal/196936cd-488f-4812-8a41-de09911e70d2`.
Recurrence is disabled/no catch-up; 18 runs, two cards and exact 36 artifact bytes
remain. The read-only pending task has zero tools; approval
`67f1196e-b1d1-44b5-a292-d6c124f94ace` is approved but unconsumed after restart.

**Resume, not a fresh uninterrupted single invocation:** the original 7/9 checkpoint
is historical. A terminal tool-free refusal was retained, then a distinct exact
read-only task was separately reviewed. The eighth-receipt continuation performed
one owned restart but exited 1 at Sign In. An idle harness-only refresh and ordinary
sign-in resumed the saved pre-restart snapshot without another restart or consent
replay. All preexisting Sent/Before/IDs/Consents/Receipts remained unchanged.
Original/before/after histories match at 108302 bytes; the final 65-second observation
admitted no work. Do not combine other candidates' receipts or rewrite old outcomes.
This scoped success does not close BOTS-001–010, direct Kubernetes or other broader
release gates, and this documentation update does not mark any plan phase complete.


The separate local launcher is **not the full release driver**:

```sh
python3 tests/e2eboot/local_bot_smoke_launcher.py --verify-chat-first
# Resume only the launcher's own checkpoint, never another daemon:
python3 tests/e2eboot/local_bot_smoke_launcher.py --verify-chat-first --state /absolute/owned/root/state.json
# Explicit harness-only refresh after a harness correction (idle browser required):
python3 tests/e2eboot/local_bot_smoke_launcher.py --refresh-browser --state /absolute/owned/root/state.json
```

`--refresh-browser` checks the existing daemon/container ownership and both pinned
binaries before rebuilding only the native browser harness. It refuses a running
owned harness/Chrome profile, rechecks ownership after building, preserves the
checkpoint bytes (including Candidate, IDs and Consents), and records old/new
harness hashes plus the checkpoint digest in `browser-refresh-*.json`. It does
not reset consent or replace/restart the daemon. Default resume keeps hash checks.

Private artifact attachment uses `load_file` on the existing exact retained path;
it attaches to the authenticated Home's private Files/download card, not external
publication. An unambiguous terminal, tool-free publishing refusal with no new
proposal may receive **one** distinct `artifact-load-clarification` request. The
original Sent/Before and refusal/proposal-absence snapshot are retained in
`artifact-load-absence.json`. Saved single-action runs are validated read-only:
only literal succeeded/one-tool/exact-consumed approval can proceed. Partial,
ambiguous, or repeated consent never permits replay of the verified write.


These are scoped reproduction commands; the recorded same-candidate resume passed,
but a fresh uninterrupted run is not established. Supply `LOCAL_BOT_BINARY`, `LOCAL_BOT_DOCKER`, `LOCAL_BOT_LAYERS` and
`ASTONISH_BOT_BROWSER_PATH` explicitly as described in the launcher. It creates an
owned loopback Docker/SQLite daemon and expects a working local model proxy; it
does not prove Kubernetes or configure arbitrary remote production environments.
Never source production credentials, clear Bot DB fields, fabricate cleanup
receipts, replay consumed approvals or replace reviewed paths to get a pass.

Optional `LOCAL_BOT_EMBEDDING_CACHE` names an absolute product model directory
for the pinned non-quantized `sentence-transformers_all-MiniLM-L6-v2` assets.
On fresh creation only, the launcher verifies all six model/tokenizer/config
SHA-256 hashes, copies only those regular files into the new owned root before
starting Docker, rechecks destination hashes, and saves
`embedding-cache-receipt.json`. Invalid hashes, symlinks, or an existing destination
fail closed. It does not copy credentials or daemon configuration, mutate the
source cache, change model selection, or alter the resume/restart path.

The schedule harness resolves server-assigned IDs from the current immutable
version after exact confirmation, checking the proposal-bound schedule identity
and every reviewed trigger field. A missing, duplicate, unrelated, or changed
schedule blocks before activation; a zero proposal ID is never treated as an
applied trigger ID. This harness correction does not establish live acceptance.

The launcher's nine mandatory evidence names are:

1. `minimal-conversation`
2. `responsibility-confirmed`
3. `tool-artifact-reload`
4. `scheduled-tick-return`
5. `exact-approval-continued`
6. `paused-approval-blocked`
7. `memory-next-answer`
8. `details-no-reconnect`
9. `paused-restart-bytes`

**Current harness:** the default path now runs the bounded `coherentWorkflow` in
`chat_first_browser/coherent.go`, with candidate/Bot/Home-bound checkpoint
receipts, exact native reviews, terminal-source and compute-teardown checks,
independent write/load approvals, memory follow-up, scheduled admission, paused
nonexecution and ownership-checked restart. Diagnostic `remaining`, retained and
permission branches remain explicit opt-ins; their partial exits cannot satisfy
the nine-name gate. Missing, skipped, stale, ambiguous or nonzero results still
fail closed. Checkpoint resume must never replay a consumed action or reset consent.

### Historical failed workflow and validation snapshots

The following snapshots predate the current 9/9 and retain their original evidence
limits; statements about outstanding smoke stages apply only to those checkpoints.

**Earlier live workflow attempt: 5/9, failed**, not acceptance. The owned
`:49348` instance (`.tmp/astonish-chat-first-bbvtaeo2`, candidate
`c4425cce33f7624d3da9dc5d7cf67aa6a5ce714e8c950ab9633b01729b348ff7`)
passed `minimal-conversation`, `details-no-reconnect`,
`responsibility-confirmed`, `exact-approval-continued` and
`tool-artifact-reload`. Two distinct write/load approvals were consumed once;
both chronological artifact cards survived reload and desktop/mobile light/dark
checks, and the downloaded artifact contained the expected 36 bytes after compute
cleanup. The original one-card harness assertion failed, then an explicit idle
harness refresh preserved checkpoint and consent identity before this progress.

The resumed process exited **1** at `memory-next-answer` around 20:44 UTC on
2026-11-06. Its actual `memory_remember` call omitted `expected_revision`; no
memory proposal or record was saved. The Bot remained Draft, with zero triggers
and all seven runs terminal. Memory follow-up, scheduled tick, paused approval and
paused restart did **not** pass on this candidate. See
`/tmp/chat-first-artifact-fixed-smoke.md` and `/tmp/chat-first-final-four.md`.
The subsequent runtime schema correction now requires integer revisions for
memory/schedule proposals and goal edits, using the same schema advertised to
the model before decoding. Code checks are not a replacement for fresh live proof
of that correction; do not merge historical receipt lists into a passing run.

**Independent clean paused-approval evidence: verified, separate from 5/9.**
On the older owned `:60064` instance, the 19:26 UTC receipt in
`/tmp/chat-first-clean-paused-approval.md` waited for a fresh run's actual
`waiting_approval` and physical compute teardown before native Pause and
`Approve & continue`. The decision persisted as approved but unconsumed; the Bot
remained paused, the same run remained waiting with zero tool calls, no
continuation was created, and its schedule remained disabled. This establishes
that focused clean lifecycle, not the new candidate's whole workflow or restart.
Earlier retained-byte/paused-restart observations remain historical evidence only.

Current schema-candidate validation is recorded in
`/tmp/chat-first-schema-final-checks.md`: sanitized full Go tests and Go lint,
scoped launcher proposal/current-memory race checks, native harness contract tests,
and strict controlled browser/HTTP evidence are distinct from live provider and
sandbox acceptance. No web rebuild or live-state mutation is part of those checks.
Exact fresh-candidate live memory follow-up and the complete nine-stage journey
remain outstanding; all ten deployed release scenarios remain unaccepted.

See the [stage-by-stage gaps](../../docs/architecture/bot-release-checklist.md#chat-first-smoke-compliance-gaps)
and [current evidence boundary](../../docs/architecture/bots.md#latest-chat-first-reconciliation--2026-11-06).
No phase completion, final acceptance, or deployed direct Kubernetes is claimed.

## Explicit prerequisites

- Bash, Python 3, Go, Node/npm, Make and the repository-required golangci-lint;
  install `web/node_modules` beforehand.
- `ASTONISH_BOT_BROWSER_PATH`: absolute executable browser path. The driver enables
  `ASTONISH_BOT_BROWSER_ACCEPTANCE`, `ASTONISH_MEMORY_BROWSER_ACCEPTANCE`, and
  `ASTONISH_STREAM_BROWSER_ACCEPTANCE`, mapping the memory/stream browser paths to
  that same executable. This is the host Studio test browser, not a claim about
  whether a sandbox overlay has a browser installed.
- `ASTONISH_TEST_DSN`: **isolated admin-capable PostgreSQL test instance**; tests
  create/drop per-run databases. `ASTONISH_BOTS_TEST_DB_ISOLATED=1` is the operator's
  explicit confirmation. Confirmation is not a technical proof of isolation.
- Docker daemon; locally available
  `ASTONISH_SANDBOX_IMAGE=ghcr.io/sap/astonish-sandbox-base:dev`;
  `ASTONISH_BOT_HOME_TEST_LAYERS`: absolute, existing layers directory containing
  `@base/rootfs/bin/sh`. The production-factory Docker test owns its temporary
  registry, sessions, and homes. The caller's layers are not deleted.
- `kubectl`; explicit `KUBECONFIG` pointing to the dedicated acceptance cluster;
  `ASTONISH_BOTS_K8S_CONTEXT` must equal that config's **existing current-context**.
  An absent context is a blocker, never a pass.
- `ASTONISH_BOTS_K8S_ISOLATED=1` and shared harness acknowledgement
  `ASTONISH_E2E_DEDICATED=1`. Supply distinct
  `ASTONISH_E2E_SANDBOX_NAMESPACE` and `ASTONISH_E2E_CONTROL_PLANE_NAMESPACE`.
  Names must use the harness's `astonishe2e-*` / `astonish-e2e-*` convention
  (control-plane `astonishe2e` is also permitted).
- Pre-provisioned Bound RWX `astonish-layers` and `astonish-uppers` claims plus an
  independent claim named by `ASTONISH_E2E_HOMES_PVC`. The sandbox image must also
  be available to cluster nodes. No implicit deployment provisioning is performed.
- One of `BIFROST_API_KEY`, `OPENAI_API_KEY`, `GOOGLE_API_KEY`, or
  `ANTHROPIC_API_KEY` for the full live Kubernetes platform bootstrap.

**Important:** the existing shared E2E bootstrap sweeps sandbox resources inside
its dedicated test namespace and cleans suffix-owned test databases. Never share
that namespace/instance with production or other test runs. The driver itself
only removes its own temporary directory and terminates its own child process
group; it has no global Docker/Kubernetes/database cleanup command.

## Required gates and evidence

1. `make test`, frontend typecheck/build/lint, and `make lint` (failures retained).
2. Strict Go JSON browser evidence for **both**
   `TestBotWorkspaceBrowserAcceptance` and
   `TestBotScheduledWorkspaceBrowserAcceptance`, plus ordinary chronology/memory
   HTTP and browser acceptance.
3. Full explicit Bot CLI and PTY selectors, including parity, bare code, explicit
   intents, ambiguous names, local gating, terminal queries, and stop-run.
4. Scoped SQLite Bot storage tests, then isolated PostgreSQL backup/recovery and
   spend/correction/cleanup integration tests.
5. Production Docker `TestBotHomeDockerPersistenceLive`.
6. Direct deployed **both** `TestBotsLiveLifecycle` and
   `TestBotsLiveKubernetesRetainedHome` in `./tests/e2e/bots`, required by the
   `kubernetes-home` gate (`-tags=e2e`, not a fake-client substitute). Neither
   test may be absent or skipped. The lifecycle uses real HTTP, worker and
   provider execution with activation, manual run, report read, pause, deletion
   and restore-to-paused. This first slice is toolless; its retained-home artifact
   is a preparation fixture, not model-produced output. Live execution remains
   unverified until the dedicated Kubernetes/provider prerequisites are supplied.
   It does not fully establish any BOTS-001–010 scenario.

7. `spec-coverage`: **all** Bot release scenarios in `tests/scenarios/bots.yaml`
   must be `covered`, with nonempty `test_refs` naming existing Go test functions
   under `tests/e2e/`. BOTS-001 through BOTS-010 cannot disappear from the gate.
   Every reference must match package-qualified run/pass events and a completed
   passing package in a successful strict gate **in this driver invocation**.
   Skips (including children), failures, missing execution, and stale evidence
   cannot satisfy this requirement. New lifecycle tests must also be wired into
   the driver's strict command selectors; editing catalog status alone is not proof.

**Current release blocker:** all ten deployed specification scenarios remain
missing. Even if every infrastructure and component gate passes, the driver
reports `spec-coverage: FAIL` and
`GATE FAILED: full lifecycle acceptance not implemented/evidenced`.
Component acceptance is not full lifecycle acceptance, and this is not an
implicit user waiver. See `docs/architecture/bot-release-checklist.md`.

The existing `node tests/scenarios/report.mjs` is useful for catalog review, but
its `--strict` checks drift rather than rejecting missing scenarios. It exposes
no reusable parser/JSON catalog API; this driver strictly reads the existing flat
catalog format. Neither its optional `.last-run.json` nor the generic result
parser's name-only scenario summary is accepted as current release evidence.
The driver does not modify catalog status or create passing coverage records.

Every strict Go gate requires actual `run` events and passing mandatory test
names. Any selected test/subtest skip, package failure, empty selection, missing
mandatory name, or nonzero command exits fails that gate. `make test` retains the
normal unit-suite opt-in skips; opt-in release tests have their own strict gates.
All commands and sanitized output are emitted, with DSNs/provider secrets
redacted. Capture stdout/stderr in your CI evidence store. Long quiet commands
emit a heartbeat at least every 30 seconds and have a 20-minute driver timeout
(Go gates have a 15-minute test timeout). Failures do not suppress later independent
gates. A final summary lists PASS, FAIL, RUNNING (interrupted), or NOT RUN.

OpenShell live acceptance is **explicitly deferred**, not silently skipped and not
a release gate in this driver.

## Modes and exits

- Default: full release, exit **0 only if every gate passes**.
- `--preflight`: checks environment/tooling and read-only infrastructure probes;
  exit 0 means prerequisites checked **only**, never release acceptance. Database
  privileges, PVC readiness, provider validity and browser launch are exercised by
  the actual gates, not certified by preflight.
- `--local-only`: explicit partial evidence without PostgreSQL/Docker/Kubernetes;
  exits **3 even when all local component gates pass**, and prints NOT RELEASE READY.
  Coverage is evaluated after collection, not used to abort local commands early;
  missing lifecycle acceptance still prints `spec-coverage: FAIL` (not a waiver).
- Exit **1**: test/command/coverage failure or interruption; **2**: blocked prerequisites
  (or invalid command arguments). No acceptance tests run after failed preflight.

Run focused driver regression tests without credentials:

```sh
bash -n tests/e2eboot/bots_acceptance.sh
python3 -B tests/e2eboot/bots_acceptance_driver_test.py
```

## Fresh skills final acceptance (separate from full Bot release)

Run from the repository root, after the operator provisions the explicit local
launcher configuration documented above and a **disposable admin-capable**
PostgreSQL test instance:

```sh
ASTONISH_TEST_DSN="$DISPOSABLE_SKILLS_TEST_DSN" \
ASTONISH_BOTS_TEST_DB_ISOLATED=1 \
python3 -B tests/e2eboot/bots_acceptance_driver.py \
  --skills-stage all --config /absolute/path/to/isolated-bot-config.json
```

The equivalent entrypoint is `employee_http_acceptance.py --skills-stage all
--config ...`. `skills_acceptance_driver.py` implements both. No infrastructure,
provider configuration, proxy, credentials or normal daemon at `:9393` is
provisioned or modified by the driver. Supply the candidate Linux arm64 binary,
Linux Docker CLI, native browser, sandbox layers, verified embedding cache and
explicit provider URL/model/key in the config; do not substitute normal user Bot
state. The local launcher creates its own root, port, candidate container and
browser profile. Its only skills mode is fresh execution: `--state`,
`--refresh-browser`, restart-only commands and all other verify modes are rejected.
Config JSON rejects duplicate keys. No browser path or model credentials are
inferred from the normal daemon configuration.

`all` requires, in the current invocation and with uncached exact Go run/pass
identities: storage and runtime HTTP skill scenarios, daemon scheduling scenarios,
personal/team recovery, integration-tagged PostgreSQL pre-skill archive and
personal/team recovery, the browser + remote terminal surface gate, then the
fresh real-model journey. Missing, foreign, duplicate/replayed, skipped or failed
mandatory Go cases fail closed. PostgreSQL is run with
`ASTONISH_REQUIRE_SKILL_POSTGRES=1`; absent DSN or isolation acknowledgement blocks
before execution. There is no DSN skip waiver. The surface wrapper remains
`tests/e2eboot/bot_skill_surface_acceptance.py` and always checks both surfaces.

To execute only the fresh real-model journey (not complete skills acceptance):

```sh
python3 -B tests/e2eboot/local_bot_smoke_launcher.py \
  --verify-bot-skills --config /absolute/path/to/isolated-bot-config.json
```

The journey preserves **17 employee + 6 quality receipts** and adds seven
mandatory, candidate/browser-hash and fresh-nonce-bound skill receipts:

1. `assignment-without-schedule`: natural owner responsibility, accepted assessed
   output, actual saved skill revision and zero scheduler jobs/cron runs.
2. `reuse-pinned-skill`: a distinct manual request reuses the same immutable skill.
3. `owner-feedback-revision`: humidity feedback advances the same skill with exact
   owner run/event provenance, retains prior expectations and immutable history.
4. `owned-restart-three-scheduled`: schedule is a thin enabled invocation of that
   skill; native browser closes, only the owned daemon restarts, then three distinct
   scheduled assessed outcomes are observed read-only before login.
5. `manual-and-scheduled-outcomes`: each manual/scheduled pin and public receipt
   matches its actual immutable revision from authenticated Bot skill endpoints.
6. `schedule-removal-manual`: natural schedule removal retains the skill; a new
   manual report succeeds without recreating jobs.
7. `skill-inspection-readonly`: actual list/head/revision/run endpoints and Skills
   UI inspect the same Bot, reject foreign/duplicate history, and leave head/runs
   unchanged across reload.

All mutations are owner messages through the real product. No acceptance SQL or
seeded model verdict creates skill success. The read-only observer reads private
run work; authenticated endpoints independently verify immutable revisions and
committed learning revisions. A persisted selection with no new learning is valid
only when its digest matches the pin; deferred/rejected/conflicting learning is
not a pass. Hashed source documents also bind receipt run identities, preventing
an unhashed manual/scheduled-ID substitution.

For scoped recovery/PG proof (neither means full acceptance):

```sh
python3 -B tests/e2eboot/bots_acceptance_driver.py --skills-stage recovery
ASTONISH_TEST_DSN="$DISPOSABLE_SKILLS_TEST_DSN" ASTONISH_BOTS_TEST_DB_ISOLATED=1 \
  python3 -B tests/e2eboot/bots_acceptance_driver.py --skills-stage postgres
```

These reject `--config`; `all` requires it. A successful scoped result explicitly
prints `full_skills_acceptance: false`. Harness unit tests need no live daemon,
provider or PostgreSQL:

```sh
python3 -B -m unittest discover -s tests/e2eboot -p '*test.py'
go test ./tests/e2eboot/chat_first_browser -run 'Test(Skills|Quality)' -count=1
```

**Release checklist:** do not mark skills accepted until `all` exits zero with
fresh evidence and all earlier Bot release gates remain satisfied. Unit harness
success and earlier controlled/runtime phases are not real-model acceptance.
The live journey and PostgreSQL final acceptance were **not executed** as part of
this harness implementation. The full browser harness package currently also
exposes `TestEmployeeReportFormattedExactlyOnce` failing: the existing fixture and
observer require a `Run:` footer, while `bot.RenderReport` now projects the employee
message without that diagnostic footer. Resolve that prior employee-proof contract
mismatch before launching final acceptance; do not waive or delete its receipt.
