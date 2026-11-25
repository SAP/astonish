# Bot release acceptance

## Objective-owned employee acceptance

Long full runs must run as owned background jobs watched through actual exit.
Each mandatory Docker latency parent has its own exact `go test -json -count=1`
command with `-timeout=15m`; no tests/subcases or measured five-second dispatch /
fifteen-second between-call bounds are relaxed.

After observing the complete `all` process exit zero (not merely its start):

```sh
python3 -B tests/e2eboot/bots_acceptance_driver.py --completed-evidence seal \
  --evidence-kind employee --employee-report /absolute/completed/report.json \
  --employee-exit 0 --receipt /absolute/new/employee-receipt.json
python3 -B tests/e2eboot/bots_acceptance_driver.py --completed-evidence verify \
  --evidence-kind employee --receipt /absolute/new/employee-receipt.json \
  --sha256 EXACT_PRINTED_COMPLETED_RECEIPT_SHA256
```

The short verifier rechecks the current full ordered manifest, exact raw
package/subcase run-pass events, subcase-bound diagnostics, no skips, real
build/gate zero exits, top-level all context, unchanged source, report/log hashes
and the exact current Studio/candidate/node artifact set. Missing, duplicated,
stale, edited or merely RUNNING evidence blocks. Optional `--quality` and
repeatable `--coverage-report` attach the existing strict broad Go/frontend/lint
quality receipts and raw logs; omission makes no quality claim. Live
Kubernetes/PostgreSQL, paid-provider quality/latency and deployment remain
unverified. Only actual full completion permits an approved plan revision to a
short hash-pinned verification; sealing never launches or substitutes acceptance.


```sh
ASTONISH_SANDBOX_IMAGE=astonish-storage-completion-a1474521:local \
ASTONISH_BOT_HOME_TEST_LAYERS='/Users/I851355/Library/Application Support/astonish/sandbox/layers' \
ASTONISH_TEST_DOCKER=1 ASTONISH_MEMORY_BROWSER_ACCEPTANCE=1 \
ASTONISH_BOT_BROWSER_PATH='/Applications/Google Chrome.app/Contents/MacOS/Google Chrome' \
python3 -B tests/e2eboot/bots_acceptance_driver.py --stage employee --employee-phase all
```

Phases: `contracts`, `dispatch`, `delivery`, `latency`, `presentation`, `all`.
Each invocation builds current Studio and retained isolated candidate/node
binaries and records source/artifact hashes. Exact named parent/subcase tests
must RUN/PASS in the correct package; any skip (including an unknown child),
malformed raw JSON, unfinished test, nonzero exit or missing physical prerequisite
fails/blocks. `--report /absolute/new/report.json` selects the report; otherwise
an owned temporary directory retains report, build logs, JSONL and binaries.
No inherited deployment/provider credentials, real daemon, existing Bot or
production database is used. HTTP servers select disposable loopback ports.

`all` requires `TestBotEmployeeObjectiveInquiryHTTP` in personal/team and both
adequate-first states: ordinary Chat compares identical actual configured
Perplexity tool/source observations and final sourced body with Bot. Exact
subcase-bound markers and counters (adaptive: three reads/four model calls;
adequate-first: one read/two model calls) are mandatory. Historical
`TestBotSharedResearchAdvertisedSchema/live-six|live-eight` replay both verbatim
Apple envelopes, preserving original digest and SQL 36263/36268 provenance while
bounding effective results to five. The driver rejects missing/spoofed markers,
wrong package/test, and output outside a running exact test. Mandatory HTTP
`TestBotInteractiveAuthorityHTTPAcceptance`, `TestBotEventsHTTPAcceptance` and
`TestBotHistoryStreamHTTPAcceptance` cover authority and cold event/history
restore without skips. These exact bindings replace the previously declared
comparison/provenance gaps, not the need for a fresh completed integrated run.
Scoped phase PASS remains only that phase's controlled evidence. Browser/native
PTY workers and adapter `read_file` sufficiency fixtures are distinct tiers.

Physical Docker is mandatory for local acceptance; unavailable live Kubernetes/
PostgreSQL, paid-provider judgment/latency and deployment remain UNVERIFIED,
never PASS. Broad Go/frontend/lint/race gates run separately. Long-running
commands emit 30-second heartbeats and terminate only their owned process group;
started builds are not completion. Historical completed-evidence sealing cannot
promote this new stage or substitute old receipts for current exact diagnostics.


## Current verification ledger — 2026-11-23

Revision 12 phase 6 is recorded complete at sequence 485 with strict receipt
`f2577352711339b7e2fd30b552da5f98318693f99aa935ffc764d1bb7eebd41e`.
Fresh scoped Go suite `/tmp/bot-release-go-final.jsonl` and scoped lint
`/tmp/bot-release-lint-retry.log` both exited 0 (supplied evidence; no rerun here).
**Final integrated acceptance is still pending**: final baseline and `all` runs
are in flight, targeting `/tmp/bot-final-baseline.json` and `/tmp/bot-final-all.json`.
Require completed per-invocation evidence and successful exits before final closure.
`all` executes the original baseline assertions as well as the integrated manifests;
it must not replace them with a placeholder or promote an implemented subset.
Preserve original historical baseline captures and failures; a fresh verification
is not permission to recapture or rewrite those artifacts.

Local evidence covers shared ordinary Chat/Bot runtime/catalog/dispatch while
retaining scope/approval/budget fences, compiled no-overlay-Python storage, exact
private revision-stage crash cleanup with historical publication IDs preserved,
and joined terminal/delegate bookkeeping. SQL reconciliation performance remains
**report-only**; read-count assertions and the controlled **15-second** limit remain
binding. See [current evidence](../../docs/architecture/bot-runtime-evidence.md#current-consolidation-evidence--2026-11-23)
for exact staging and physical/nonphysical boundaries.

Live Kubernetes/PVC and PostgreSQL remain explicitly **DEFERRED, NOT PASS** in the
user-requested local profile. Mocked helper/adapter tests remain mandatory but are
not physical evidence. Paid-provider replay and real-provider quality are not
claimed or authorized. No user daemon restart/Bot retry, deployment or data deletion
is implied. Older progress/blocker descriptions below are **historical** snapshots,
not current phase status; their receipts remain unchanged.


## Explicit user-requested local infrastructure scope

Append `--defer-kubernetes-postgresql` to the current plan's commands:

```sh
python3 -B tests/e2eboot/bots_acceptance_driver.py --stage chat-consolidation --phase runtime --defer-kubernetes-postgresql
python3 -B tests/e2eboot/bots_acceptance_driver.py --stage chat-consolidation --phase controller --defer-kubernetes-postgresql
python3 -B tests/e2eboot/bots_acceptance_driver.py --stage chat-consolidation --phase storage --defer-kubernetes-postgresql
python3 -B tests/e2eboot/bots_acceptance_driver.py --stage file-first --defer-kubernetes-postgresql
```

This boolean flag is accepted only for file-first or chat-consolidation (all seven
phase choices, including baseline/all). It selects the report profile
`local-deferred-kubernetes-postgresql`; default remains strict. Live Kubernetes and
PostgreSQL integration tiers/prerequisites are explicitly **DEFERRED/user-requested,
NOT PASS**, nonblocking for this profile and retained for later user testing.
No cluster probe or live DSN is required/run for these exclusions. Runtime,
controller and storage mappings retain every nonphysical dependency: PostgreSQL
mocked store tests, Kubernetes bootstrap/safety and local adapter fixtures remain
mandatory without ambient DSN/KUBECONFIG/PG routing. Cross-backend cold restore is
Docker-only locally, with the other backend portion visibly deferred.

Docker, performance, browser/PTY, authority, mailbox persistence and all other
unresolved requirements remain binding; 0 means remaining mandatory evidence is
PASS, **not** full Kubernetes/PostgreSQL acceptance. `all` does not bypass existing
full-phase missing-evidence blockers. Reports support `--report PATH`; excluded
gates/scenarios have no successful/fresh physical evidence. See
`docs/architecture/bot-runtime-evidence.md` for the scope contract. Historical
strict failure receipts below are unchanged.

## Chat consolidation baseline (not green)

```sh
ASTONISH_TEST_DOCKER=1 \
ASTONISH_SANDBOX_IMAGE="${BOT_TEST_IMAGE:?Existing local image}" \
ASTONISH_BOT_HOME_TEST_LAYERS="${BOT_TEST_LAYERS:?Absolute default layers}" \
python3 -B tests/e2eboot/bots_acceptance_driver.py \
  --stage chat-consolidation --phase baseline
```

This stage runs `TestBotEnhancedChatBaseline` in launcher, API and daemon packages
sequentially, using controlled models and isolated HTTP/store fixtures. It records
package-qualified run/pass evidence and each executed subtest; skipped children,
missing tests, foreign-package passes and unfinished packages fail. Separate owned
JSONL logs and a JSON report are printed under `CHAT_CONSOLIDATION_REPORT`.

It then runs the success-asserting `TestBotEnhancedChatNoPythonDocker` against a
new disposable session using the unchanged default template. Its **specific
physically diagnosed** missing-Python failure may be labeled REPRODUCED for
historical diagnosis only: **REPRODUCED now fails the baseline (exit 1)**. The
revised plan requires fresh FIXED evidence and all preserved baseline assertions;
an old expected failure cannot authorize phase completion. Missing Docker/layers/
image is BLOCKED, not an expected regression. No provider keys or production
configuration enter these commands. No daemon restart, live Bot retry or Python
installation is performed. Assertions have not been weakened.

### Runtime/storage progress reports (full phases remain BLOCKED)

```sh
python3 -B tests/e2eboot/bots_acceptance_driver.py \
  --stage chat-consolidation --phase runtime --report /tmp/chat-runtime-progress.json

ASTONISH_TEST_DOCKER=1 \
ASTONISH_SANDBOX_IMAGE="${BOT_TEST_IMAGE:?Explicit source-built local test image}" \
ASTONISH_BOT_HOME_TEST_LAYERS="${BOT_TEST_LAYERS:?Absolute existing default layers}" \
python3 -B tests/e2eboot/bots_acceptance_driver.py \
  --stage chat-consolidation --phase storage --report /tmp/chat-storage-progress.json
```

The available local image at handoff was `astonish-storage-proper-20585a9b:local`;
this is **not** a driver default or proof of source freshness. The operator must
explicitly select a test-owned image built from the sources under review and the
absolute retained layers path. The driver never builds/pulls an image, installs
Python, provisions a cluster, or discovers production credentials. These commands
can perform disposable fixture operations only when explicitly configured.

| Gate | Fresh evidence and precise boundary |
| --- | --- |
| `runtime-http` | `pkg/api::TestStudioChatHTTPScopedRuntimeFactory`, including success, construction-failure, missing-agent and missing-result children; actual isolated POST `/api/studio/chat` and SSE with controlled model, not deployed UI |
| `runtime-construction` | `pkg/launcher::TestStudioChatRuntimeFactoryUsesSharedScopedConstructor` and the explicitly named current `TestChatRuntime*` catalog/dispatch/lifetime/scoped flow/drill fixtures; not physical backend parity |
| `storage-docker` | `pkg/sandbox/docker::TestCompiledHomeFilesNoPythonDocker`, `TestBotHomeStreamArchiveDockerLive`, `TestBotHomeArchivePrivateWriterContentionDockerLive`, `TestBotHomeCompiledArchiveDockerLive`; physical retained-file/compiled archive subset only |
| `storage-docker-backup-cli` | `cmd/astonish::TestBotFileFirstBackupCLI`, `TestBotFileFirstStoppedBackupCLI`; built public CLI create/verify/restore, failed allocation cleanup/retry, detached or stopped Chat source, fresh target and exact reopened HTTP bytes; personal/team SQLite/Docker only. Requires the same explicit Docker image/layers opt-ins. |
| `storage-compiled-linux` | `pkg/sandbox::TestCompiledHomeFilesProtocol`, `TestCompiledHomeFilesScopedRoots`, `TestCompiledHomeFilesFenceAndStreamErrors`; compiled root protocol fixtures, require Linux and explicit `ASTONISH_TEST_COMPILED_HOME_FILES=1` |
| `storage-kubernetes-adapter-fixture` | `pkg/sandbox/k8s::TestBotHomeFilesFilesystem`, `TestBotHomeFilesConfinement`, `TestCompiledWorkspaceUnicodeReceipt`, `TestCompiledWorkspaceJoinsRequestWriter`, `TestCompiledWorkspaceWarmNoHelper`; fake Kubernetes API/transport fixtures, **not live Kubernetes** |

On an explicitly prepared Linux fixture host, the adapter gate additionally requires
`ASTONISH_BOT_FILES_TEST_BINARY=/absolute/path/to/fresh/astonish` naming an existing
executable built from the changed sources. That binary is passed only to the adapter
gate; there is no implicit build, cross-platform emulation or infrastructure setup.
The root protocol gate runs compiled Go fixtures in process and needs no external
node binary. Missing opt-ins/platform/binary block the corresponding gate without
launching it. Live Kubernetes has its own `storage-live-kubernetes` BLOCKED entry;
passing an adapter fixture can never satisfy that requirement. Dedicated live
namespace/PVC/KUBECONFIG acceptance still needs a separate fully wired physical gate.

Reports contain `gates` with exact `package`, `required` parent/child names,
`command` (`-count=1`), `boundary`, `status`, `passed`, `fresh`, redacted owned JSONL
`log`, `errors` and elapsed time. `fresh` means execution was attempted in this
invocation, not that it passed. No historical log or receipt is imported. Skips,
missing children, wrong-package passes, absent run events, unfinished tests/packages
and nonzero exits fail the gate. A PASS is **only that named subset**.

`evidence_policy`, `phase_acceptance`, and named `remaining_requirements` make the
non-completion explicit. Runtime still requires three-mode full capability parity,
authority/budget/no-replay coverage, lazy/reused/recovered sessions on both backends
and complete production/external-capability wiring. Storage still requires live
Kubernetes, the full offline browse/download/cold backup-restore path, ordinary
Chat-tool parity, complete fence/transport and cancellation/reaping matrices,
durable data/authorization coverage and the complete compiled migration. Each
remaining requirement stays BLOCKED even if related subset tests pass.

Thus successful subset runs still return **2 / BLOCKED**, while an executed gate
failure returns **1 / FAIL**; no runtime/storage/controller subset returns 0. This
does not change approvals, reclassify a capability or mark any whole phase complete.
`--phase all` executes runtime/storage/controller/events/clients manifests and
keeps the outstanding full-acceptance obligations; it is not a release shortcut.
`--phase events` runs canonical HTTP, projection and production-capture tests.
`--phase clients` runs the built workspace browser, native PTY, terminal projection
and client transport fixtures. The browser tier requires explicit
`ASTONISH_MEMORY_BROWSER_ACCEPTANCE=1` and an absolute executable
`ASTONISH_BOT_BROWSER_PATH`, plus a previously built Studio bundle. Without those
inputs, it is BLOCKED while independent terminal tiers still run. Both phases
retain full conversation/visual parity blockers; a named subset pass is not phase
completion.
`--phase` is rejected for other stages; partial/config/paid-provider flags remain
rejected. File-first and durable-progress gates are unchanged. This is not
Kubernetes, PostgreSQL, built-browser, native-PTY or release certification.

### Controller executable evidence (full controller remains BLOCKED)

```sh
python3 -B tests/e2eboot/bots_acceptance_driver.py \
  --stage chat-consolidation --phase controller --report /tmp/chat-controller-progress.json
```

The controller now executes these fixed selectors, not an unconditional placeholder:

| Tier | Required tests and boundary |
| --- | --- |
| `controller-api` | `pkg/api`: `TestBotEnhancedChatBaseline`, `TestBotTitleConfirmedLaterQuestionContinuation`, `TestBotProgressRecoveryHTTP`, `TestBotProgressLifecycleHTTP`, `TestBotProgressRestartHTTP`, `TestBotTitleAnswerAuthorityHTTPRegression`, `TestBotHistoryStreamHTTPAcceptance`, `TestBotResumeNoReplayAfterEffect`. Isolated HTTP/worker progress, typed answers and history plus adapter no-replay; controlled models, not physical compute. |
| `controller-daemon` | `pkg/daemon`: `TestBotEnhancedChatBaseline`, `TestBotFileFirstWorkerHTTP`, `TestBotWorkerPollRecoversCrashAfterDatabaseReopen`, `TestBotWorkerPublicationIdlePollAndUncertainReplay`, `TestProductionBotWorkerBorrowsChatResources`, `TestBotHomeSessionRecoveryPreflight`, `TestBotIntentHTTPWorkerDraftApprovalExactOnce`, `TestBotApproveContinueHTTPWorkerExactOnce`, `TestBotUncertainEffectRestartHTTPNoReplay`. HTTP title/search/schedule/owner-priority and file-first fixtures plus narrower lifecycle/resource checks; approval journeys revoke membership after admission and approval/requeue before claim. |
| `controller-store` | `pkg/store/entstore`: `TestBotRuntimeSupervisorLeaseLossCancels`, `TestBotHomeFileCommitFence`, `TestBotApprovalResumeFenced`, `TestBotApprovalRequeueCurrentAuthority`, `TestBotApprovalConcurrentConsumption`, `TestBotProgressQueuedAuthorityLoss`, `TestBotAuthorizedScheduleQueuedReconciliation`, `TestBackupFileFirstRestoreGuards`, `TestBackupFileFirstRestoreFilesBeforeSQL`, `TestBackupFileFirstRestore`, `TestBackupFileFirstMappingRunAuthorityAndOpaqueContent`, `TestBackupFileFirstMappingIndependentOwnership`, `TestBackupFileFirstReconciliationPreservesQuiescence`. Temporary SQLite authority plus the files-before-SQL fault fixture; not physical cross-backend restore. |
| `controller-docker` | `pkg/daemon`: `TestBotEnhancedChatNoPythonDocker`, `TestBotNewMessageDockerBetweenCallReproduction`, `TestBotEnhancedChatTeamNoPythonDocker`. Explicit opt-in physical personal and team default-layer subset, including a stopped-compute read; no all-mode or live Kubernetes claim. |

The runtime manifest also requires `TestChatRuntimeRestrictedChildAuthority`,
`TestChatRuntimeNestedDrillFences`, and `TestChatRuntimeChildCancellationJoinedReturn`
with exact scope/mode/fence leaves. These exercise actual strict sub-agent and
nested drill dispatch plus joined child-model cancellation; physical aliasing,
transport reaping, and the full flow/plan authority matrix remain separate.
The controller uncertain-effect case performs one actual HTTP write whose response
is lost, then reopens SQLite and reconstructs the worker after terminal persistence.
Repeated admission/resume/polls cannot repeat it; a separate authorized receipt GET
cannot rewrite the old uncertain report. Models, classifier, isolation and sandbox
transport/Home are controlled, so this is not mid-effect process-crash or generic
production HTTP classification evidence.

The first three tiers execute without Docker. The Docker tier requires explicit
`ASTONISH_TEST_DOCKER=1`, `ASTONISH_SANDBOX_IMAGE` (test-owned, built from reviewed
sources), and absolute `ASTONISH_BOT_HOME_TEST_LAYERS` containing
`@base/rootfs/bin/sh`, as for storage above. No implicit image build/pull, Python
installation, daemon restart, paid provider, cluster or production configuration
is launched. Missing inputs block that tier, not the independent controlled tiers.

`CONSOLIDATION_CONTROLLER_SUBTESTS` pins the personal/team children, title-answer
options, schedule/owner-priority/paused-recovery wrappers, durable authority cases,
publication failures, no-replay failures and restore guard cases. Concurrent approval
consumption requires both scope children, each with eight contenders and one winner.
Names are fixed,
not discovered from test source or historical logs. Every required child must have
this invocation's exact package-qualified run then pass, and every started test and
package must finish. Malformed JSON evidence, missing children, skips, nonzero
exits or execution failures are FAIL, never BLOCKED or accepted regression evidence.
Owned logs and diagnostics are scrubbed; provider/configuration secrets and
infrastructure opt-ins are excluded from controlled child environments.

The strengthened no-Python fixture requires SUCCESS, exact **165 downloadable
bytes after two disposable-compute evictions**, original retained session identity,
the unchanged **24 total / 8 per-key hydration-open limits** and **15-second**
controlled between-call limit. A reported earlier 22/7-open run is historical context only:
the driver never imports it as evidence. Only a fresh successful execution can pass
the named Docker tier, and that still cannot pass the controller phase.

Explicit remaining BLOCKED obligations are:

- `controller-all-mode-no-python`: combined scheduled/autonomous/continuation/delegate
  physical no-Python execution and retained reuse/recovery across all modes/backends.
- `controller-team-physical`: physical team ownership/lifecycle and exact bytes.
- `controller-files-before-sql`: fault-injected restore ordering, interruption and rollback, covered by `TestBackupFileFirstRestoreFilesBeforeSQL`.
- `controller-cold-cross-backend`: stopped-compute browse/download/cold restore without
  GET provisioning or Bot startup, paused schedules and fresh ownership on both backends.
- `controller-authority-matrix`: complete personal/team, three-mode lease, membership,
  budget, exact approval, child authority and uncertain-effect no-replay matrix.

A controller report therefore exits 2 when the available named tiers pass but gaps
remain; any executed tier failure takes precedence and exits 1. `phase_acceptance`
remains BLOCKED in either case. Built Studio, native PTY, live Kubernetes,
PostgreSQL concurrency and paid-provider workflow are separately unverified.

## File-first Bots (strict mandatory-tier gate; acceptance pending)

```sh
ASTONISH_MEMORY_BROWSER_ACCEPTANCE=1 \
ASTONISH_TEST_DOCKER=1 \
ASTONISH_TEST_TASK_CONTINUITY=1 \
ASTONISH_BOT_BROWSER_PATH="${BOT_BROWSER:?Absolute executable browser}" \
ASTONISH_SANDBOX_IMAGE="${BOT_TEST_IMAGE:?Existing local image}" \
ASTONISH_BOT_HOME_TEST_LAYERS="${BOT_TEST_LAYERS:?Absolute existing layers}" \
ASTONISH_TEST_DSN="${DISPOSABLE_BOT_TEST_DSN:?Dedicated disposable PostgreSQL}" \
ASTONISH_BOTS_TEST_DB_ISOLATED=1 \
KUBECONFIG="${BOT_TEST_KUBECONFIG:?Absolute dedicated config}" \
ASTONISH_BOTS_K8S_CONTEXT="${BOT_TEST_CONTEXT:?Explicit current context}" \
ASTONISH_BOTS_K8S_ISOLATED=1 ASTONISH_E2E_DEDICATED=1 \
ASTONISH_E2E_SANDBOX_NAMESPACE="${BOT_TEST_NAMESPACE:?Dedicated sandbox namespace}" \
ASTONISH_E2E_CONTROL_PLANE_NAMESPACE="${BOT_TEST_CONTROL_NAMESPACE:?Distinct control namespace}" \
ASTONISH_E2E_HOMES_PVC="${BOT_TEST_HOMES_PVC:?Independent Bound RWX claim}" \
python3 -B tests/e2eboot/bots_acceptance_driver.py --stage file-first \
  --report /tmp/astonish-file-first-report.json
```

All inputs are explicit; no host paths, provider credentials or image defaults
are inferred. On the development Mac the supplied browser may be
`/Applications/Google Chrome.app/Contents/MacOS/Google Chrome`, image
`ghcr.io/sap/astonish-sandbox-base:dev`, and layers
`/Users/I851355/Library/Application Support/astonish/sandbox/layers`; these are
operator examples, not script defaults. Layers must contain
`@base/rootfs/bin/sh`. Frontend dependencies must already be installed. The
sandbox image must also be available to the dedicated cluster nodes.

The driver first checks **all** required infrastructure, then makes read-only
Docker image/daemon and Kubernetes context/namespace/Bound-RWX PVC probes. It
never pulls images, switches contexts, provisions a database or changes a daemon.
Kubernetes additionally needs pre-provisioned `astonish-layers` and
`astonish-uppers` claims; namespace conventions and the shared E2E cleanup warning
below apply. Explicit isolation acknowledgements are required, not proof of
isolation. No provider secrets, production DSN/config, inherited GOFLAGS/MAKEFLAGS,
live override or restart-stage variables enter controlled subprocesses. Only the
PostgreSQL and Kubernetes tiers receive the disposable DSN, and only Kubernetes
receives the dedicated kubeconfig. Local fixtures require no provider credentials.
The existing Kubernetes bootstrap may still block/skip without its own setup;
that cannot count as passing evidence and this driver never supplies fake keys.

Required current-invocation gates after building Studio:

| Tier | Exact tests |
| --- | --- |
| HTTP | `pkg/api`: `TestBotFileContentPersistenceHTTP` (personal/team), `TestBotFileDeliveryHTTP`, `TestBotFileFirstPublicationSSE` |
| Worker | `pkg/daemon`: `TestBotFileFirstWorkerHTTP` (personal/team) |
| Built browser | `pkg/api`: `TestBotFileWorkspaceBrowserAcceptance` (personal/team) |
| Native PTY | `cmd/astonish`: `TestBotFileWorkspacePTYAcceptance` (personal/team) |
| Physical Docker | `pkg/daemon`: `TestProductionBotWorkspaceDockerLive`; `pkg/sandbox/docker`: `TestBotHomeDockerPersistenceLive` |
| Task continuity | `pkg/daemon`: `TestBotTaskContinuityHTTPAcceptance`, explicitly opted in with `ASTONISH_TEST_TASK_CONTINUITY=1` |
| Public backup CLI | `cmd/astonish`: **`TestBotFileFirstBackupCLI`** — required implementation/evidence gap, never replaced by a library backup test |
| Dedicated PostgreSQL | `pkg/store/entstore`, `-tags=integration`: `TestBotAgentPostgresContinuityCASProjection`, `TestBotAgentPostgresContinuityRestoreQuiescence`, `TestBotTaskWorkspaceArchivePostgresDocker` (all personal/team) |
| Dedicated Kubernetes | `tests/e2e/bots`, `-tags=e2e`, `-timeout=20m`: `TestBotsLiveKubernetesRetainedHome` |

Each Go command uses `-json -count=1`, exact top-level selectors, and requires
package-qualified **run then pass** events plus package completion. Mandatory
personal/team subtests must also run and pass. Empty/no-tests exit 0, skipped
children, absent tests, foreign packages, unfinished tests/packages, and nonzero
subprocess exits cannot pass. Independent test gates continue after failures;
`TestBotFileFirstBackupCLI` not executing is explicitly **BLOCKED**. The JSON
report includes every mandatory tier even when no commands could start, elapsed
time, errors and unverified boundaries. No historical receipts are accepted.

`--local-only`, `--preflight`, `--config`, `--postgres`, `--soak`, and
`--real-provider` are rejected for this stage: PostgreSQL and Kubernetes are
mandatory, not optional flags. Missing prerequisites exit **2/BLOCKED** before
any launch. Failed tests exit **1/FAIL**; zero is possible only when every named
gate passes. These are named-gate results, not full release certification.
Full raw PostgreSQL sentinel/body-free coverage and the full deployed file-first
workflow beyond the retained-home case remain unverified obligations, as does
real-provider output quality. Backup implementation is still underway; no
passing backup or full file-first acceptance is claimed here. Controlled Python
self-tests test orchestration, not physical browser/PTY/Docker/cluster behavior.

## Durable progress (strict controlled acceptance)

```sh
ASTONISH_MEMORY_BROWSER_ACCEPTANCE=1 \
ASTONISH_TEST_DOCKER=1 \
ASTONISH_BOT_BROWSER_PATH="${BOT_BROWSER:?Absolute executable browser}" \
ASTONISH_SANDBOX_IMAGE="${BOT_TEST_IMAGE:?Existing local Docker image}" \
python3 -B tests/e2eboot/bots_acceptance_driver.py --stage durable-progress \
  --report /tmp/astonish-durable-progress-report.json
```

The stage builds Studio and requires fresh exact package/test **run and pass**
events, including all three OS-process restart modes and personal/team clock
subtests. Zero matching tests, any skipped descendant, missing package completion,
a foreign package's similarly named test, or a failing subprocess fails the gate.
Its bounded JSON report lists every required test, gate result, elapsed time and
unverified tier. No historical receipts substitute for a current invocation.

`TestBotProgressProcessRestartAcceptance` owns child test executables, isolated
SQLite and a loopback effect sink. The parent SIGKILLs at a drained read checkpoint,
a drained write with an out-of-band exact sink receipt, and an in-flight write.
A fresh process reopens the store, worker and empty authorization registry. Settled
work requests fresh authentication, keeps the same run/session and cumulative
ledger, then finishes without replaying a read or write. The settled-write case also
requires employee assessment and confirmed effect evidence after restoring the
receipt; both settled cases repoll and require exactly one final publication.
An unresolved write waits
for **real lease expiry** and stays interrupted without automatic replay or false
success. This is host-process proof, distinct from injected crashes and from a
Docker-host restart. No normal daemon or current user Bot is involved.

`TestBotProgressSixHourClockAcceptance` advances only the employee controller clock
past six hours; lease, authentication and individual-operation clocks stay real.
Its three evaluations use the same employee adapter and include continue/replan,
original assignment/evidence checks and exactly one published final. It is protocol
wiring evidence, not model judgment quality or a six-hour wall-clock run. Separate
required tests cover compacted context, transient failures, bounded calls, quota
and legacy limits, schedule coalescing, pause/continuation, revocation, deletion,
trusted synthesis nonexecution, employee-only built browser and the real PTY.

Docker daemon readiness and the explicitly named **local** image are mandatory;
there is no image pull. The required title-confirmation Docker browser fixture
owns its UUID Home/helper, verifies mounting/persistence and cleans up only those
resources. Cleanup failure fails that test. No shared prune or live resource change
is permitted. Physical Home proof is not full tool-node overlay proof.

Optional `--soak`, `--real-provider` and `--postgres` requests currently **BLOCK**
(exit 2), never silently skip or launch a paid provider. Soak/provider additionally
require `--config` identifying isolated settings and finite spending limits;
PostgreSQL requires explicit `ASTONISH_TEST_DSN` and
`ASTONISH_BOTS_TEST_DB_ISOLATED=1`. Their durable-progress harnesses remain
unimplemented/unverified. A passing controlled report cannot claim a two-hour
wall-clock soak, real-provider quality, PostgreSQL concurrency or Kubernetes.

## Initial title confirmation (controlled built-client gate)

New title-created Bots must ask one focused question with three or four distinct
interpretations and a custom-answer input before preparing a Home, researching,
or creating a Task. Owner waiting is not active runtime. Confirmation selects the
assignment; it is not permission for external effects or recurring work. Legacy
runs, ordinary direct messages and schedules retain their existing semantics.

```sh
ASTONISH_MEMORY_BROWSER_ACCEPTANCE=1 \
ASTONISH_BOT_BROWSER_PATH="${BOT_BROWSER:?Absolute executable browser}" \
ASTONISH_SANDBOX_IMAGE="${BOT_TEST_IMAGE:?Existing local Docker image}" \
bash tests/e2eboot/bots_acceptance.sh --stage title-confirmation
```

This distinct stage builds Studio, runs exact controlled lifecycle/HTTP cases,
launches disposable Rod profiles against production handlers/worker factories,
and requires the real title PTY case. Missing prerequisites, skipped tests,
missing exact package/test run-pass evidence, or partial-mode flags fail closed.
No `.env`, provider credentials, production database settings or normal daemon
are used. `--stage title-confirmation-contract` remains the narrower contract gate.

`TestBotTitleConfirmationDockerBrowserAcceptance` independently checks no owned
Home volume/container before the answer, then production worker Home registration,
Docker volume ownership, an owned helper's mount and persisted receipt. Helpers
use the existing image (`--pull=never`), no network and UUID-owned cleanup. This
proves physical Home ordering, **not** full tool-node overlay, configured research
or Task-file publication. The browser worker replacement retains the same live
HTTP authorization registry; it is not an OS-process/registry restart claim.

The strict manifest also requires built team-workspace option and custom-answer
journeys, configured-search HTTP (personal/team with configured and missing
service), ordered title-confirmation state, and later-question continuation.
Team title admission uses the production HTTP endpoint before opening the built
team workspace: `NewBotDialog` currently offers personal creation only. These
team browser cases do not claim a team creation dialog. Both strict and contract
manifests require `TestBotTitleConfirmationProcessRestart`: two separate test
processes reopen the same SQLite database with a fresh authorization registry and
recover the waiting confirmation before accepting its owner answer. This passing
controlled question-repository subprocess proof closes the SQLite/authorization
restart gap; it is distinct from browser worker replacement and does not claim
an actual daemon restart or end-to-end browser recovery across daemon processes.

Earlier browser attempts remain failed evidence: `.tmp/title-confirmation-browser.log`
and `.tmp/title-confirmation-browser-authorized.log` reached the built UI and real
worker but were blocked by the legacy `botBoundTool.Run` ControlOnly guard before
question persistence. Do not turn these into passing receipts. A narrow production
fix must be judged by a fresh rerun, not these historical receipts. Real-provider
title-to-card latency, suggestion
relevance and post-confirmation research quality require an explicit isolated
provider configuration and remain unverified; controlled models cannot certify
those qualities or supersede historical live failures.

## Bot-owned Task all-stage gate (not live-certified)

```sh
python3 -B tests/e2eboot/bot_task_acceptance.py --stage all \
  --config "${LOCAL_BOT_TASK_CONFIG:?Explicit isolated AI Core and search config required}"
```

The distinct workflow runs compatibility, workspace, search, execution and owned
PostgreSQL/daemon continuity in dependency order, then invokes
`local_bot_smoke_launcher.py --verify-bot-tasks --config CONFIG`. State reuse is
forbidden; historical employee/skills/quality/agent modes remain separate.

Use an absolute, private regular non-symlink version 1 launcher JSON config plus
`search_config`. Exact Bot identity: `sap_ai_core_sci` / `sap_ai_core` /
`anthropic--claude-4.6-sonnet`. Tavily supplies `engine: tavily` and `api_key`;
Perplexity supplies `engine: perplexity`, `provider_instance`, `provider_config`
and `model`. Missing explicit config blocks before startup; no ambient secret
fallback, normal-daemon mutation or spending-limit change is allowed.

The read-only owned observer compares scoped registry revisions with actual
workspace-volume bytes, never substituting archival bundles. Three unchanged
Stock Market News starts retain search-before-detail ordering, Task assets, run
pins, transcripts, answers/sources and adapter identities. The validator emits
all eighteen exact obligations and keeps missing/failed cases outstanding.
Accountable grounding and fresh-candidate correction/helper/state/scheduler/
restart/backup/terminal journeys remain implementation and live-evidence gaps;
providing config alone cannot close them. No paid Task candidate has been run in
this checkpoint. Kubernetes/OpenShell certification is outside this gate.

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
