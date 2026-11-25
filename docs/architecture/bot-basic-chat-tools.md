# Bot basic Chat tools

Production Bot Chat supplies `memory_save`, `memory_search`, `memory_get`, and
`memory_delete` through `launcher.BotMemoryTools`, before composing the isolated
catalogue. Argument/result types are the existing Chat contracts. Search paths
are memory IDs, not daemon filesystem paths.

## Scope and authorization

Memory reads use the Bot's session-prefiltered disk service in the selected
personal or team scope. The immutable catalogue retains IDs, attribution and
superseded history; SQL stores its typed head reference, not memory bodies or
nested source snapshots. Generic SQL Add/Update reject Bot-linked sessions.
The factory shadows generic owner-wide memory/search context
slots. Runtime mutations use `NewBotRunMemoryService` and a transactional live
lease check. Writes, overwrites and deletion are confined to the producing run's
session; autonomous tools cannot rewrite prior-run or home-session evidence.
Attributed content retains its existing reviewed-edit protections. Credentials
are placeholderized before saving.

The production classifier treats save as a reversible write and deletion as a
delete, not as reads. Autonomy/allow-list/exact approval gates therefore still
apply. Reads remain read effects. Unsupported backends fail explicitly.

## Retained file tools

### Shared Chat construction

The current production construction path is `newBotScopedChatRuntime` →
`NewScopedChatRuntime` → `BuildChatCatalog`. Ordinary `read_file`, `find_files`,
`file_tree`, `write_file`, and `edit_file` are Chat's built-in node tools, not the
confined adapters described below. Bot dispatch still binds the owner/run session,
checks current authorization/lease/budget and never falls back to the daemon host.
`botSharedFileClassifier` treats built-in reads/listings as reads; generic writes
and edits remain unknown effects requiring exact approval. A built-in write must
return a verified receipt; an invalid receipt is an uncertain effect, not permission
to replay. The shared file tools do **not** inherit the confined adapters' CAS,
`outputs/`-only or byte-window contracts. `publish_file` remains an additive Bot
snapshot capability, not a replacement for ordinary Chat file tools.

`TestChatRuntimeCapabilityParity/configured-three-mode-subset` executes the real
built-in `read_file` and `filter_json` through ordinary scoped Chat, interactive
Bot and autonomous Bot construction. It verifies exact line-numbered file output,
filtered output, pinned session IDs, cancellation, paused-Bot refusal, unapproved
autonomous shell refusal and exactly-once Bot resource cleanup. Node transport and
authoritative repository are controlled fixtures; this is not Docker/Kubernetes,
HTTP conversation, scheduler-trigger or complete capability-matrix evidence.

### Production sandbox-template dependency handoff

The ordinary `NewWiredChatAgent` bootstrap captures `ChatFactoryResult.SharedResources`.
Production daemon assembly calls `productionBotChatResources` to borrow that complete
bundle for Bot composition, retaining the **same** template registry, session registry,
backend and pool. The helper copies the bundle wrapper, so request preparation cannot
mutate bootstrap state; request cleanup must not close the borrowed pool. Missing or
incomplete captured bundles fail closed rather than reconstructing a pool-only catalog
or falling back to host/global resources. Construction itself does not switch templates
or destroy a session. Sandbox tool templates are separate from reviewed Bot-definition
and intake templates; neither is execution permission.

Both `runtime-template-wiring` and `controller-daemon` require four exact tests from
`pkg/daemon/bot_template_wiring_test.go`: `TestProductionBotTemplateAssemblySource`,
`TestProductionBotTemplateBootstrapCapture`,
`TestProductionBotTemplateMissingBundleFailsClosed`, and
`TestProductionBotTemplateWiringScopedDispatch`. The last requires all six exact
`personal|team` × `ordinary|interactive|autonomous` leaves: **10** package-qualified
parent/leaf entries in the dedicated runtime gate. Missing, skipped or wrong-package
entries fail even when the process/package exits successfully.

The dispatch regression discovers the concrete shared `list_sandbox_templates`,
`use_sandbox_template` and `save_sandbox_template` entries, executes list and selected
switch against the captured registry, and verifies exact session/organization/team
transport binding. It rejects foreign scope, changed Bot arguments, autonomous
allowlisting without an exact grant, consumed-grant replay and revoked interactive
turns before additional backend work. Save is catalog-presence coverage only, not a
successful template-save lifecycle. The ordinary bootstrap uses a mock backend;
scoped dispatch uses controlled backend transport and a test-owned exact-action hook
with production authorization. These are production-code dependency/dispatch
regressions, **not physical tenant isolation**, Docker/Kubernetes session creation,
scheduler-trigger evidence, durable approval-store evidence or hostile-root protection.
All full capability, authorization, production-wiring and lifecycle blockers remain;
template success alone cannot close them.

### Configured local service acceptance

`TestChatRuntimeCapabilityParityConfiguredServices` executes real email and drill
implementations for personal/team ordinary Chat, interactive Bot, and autonomous
Bot. A loopback SMTP peer checks delivered subject/body bytes; the real drill
engine executes the node dispatcher and persists its report to the scoped store.
Autonomous execution consumes separate exact grants for email, drill, and its
nested shell call. Changed email arguments, grant replay, foreign scope, and
cancellation are denied without extra dispatch.

`TestChatRuntimeCapabilityParityConfiguredPlan` verifies announce → running →
verified completion → persisted Results through ordinary Chat and interactive Bot
in both scopes. The separate `TestBotPreparedPlanAnnouncementResume` now verifies
successful autonomous prepared-plan execution through the production factory and
API adapter (details below). `TestChatRuntimeAutonomousPlanRequiresMirrorGrant`
remains denial coverage for missing exact grants on generated plan-file writes.
These controlled fixtures do not prove physical backend or scheduler lifecycle
parity. The acceptance driver names each scope/mode explicitly and retains the
outstanding full-matrix blockers.

### Restricted children and uncertain-effect recovery

`TestChatRuntimeRestrictedChildAuthority` executes an actual strict sub-agent in
personal/team ordinary Chat, interactive Bot and autonomous Bot. The child cannot
invoke a parent-only shell through `execute_tool`; parent execution afterward
proves its capability/grant remained usable. Autonomous child drill listing is
currently denied by the conservative classifier, not counted as a successful read.
`TestChatRuntimeNestedDrillFences` runs the real two-step drill engine: cancellation
stops the second step in all three modes; Bot lease replacement and shared tool
budget exhaustion also stop it without a successful persisted report. These are
controlled node/owner fixtures, not physical child-session alias or async-reaping
proof, and do not cover all flow/plan child paths.
`TestChatRuntimeChildCancellationJoinedReturn` additionally cancels an actual
asynchronous sub-agent after it enters a blocking model iterator. In both scopes
and all three modes, the manager waits for iterator exit, preserves parent
identity/credentials and unused grants, and produces no late node work. This is
joined local model execution, not physical process/transport reaping.

`TestBotUncertainEffectRestartHTTPNoReplay` uses the actual shared `http_request`
and dispatcher against a local endpoint that commits one write then drops its
response. Both scopes persist a partial report with runtime-owned
`EffectState=uncertain`, consume the exact approval, reopen SQLite and reconstruct
the worker without replay. A new independently authorized receipt GET observes the
write without rewriting the original report or reusing its consumed grant.
Exact-action resume assigns typed uncertainty before regenerating the employee
message digest, keeping stored/report facts consistent with `RunError.UncertainEffect`.
No new replay, approval, or effect-classification permission is introduced. This
covers explicit approval resumption with a controlled classifier and transport,
not every interactive/scheduled origin, physical container or process-crash case.

### Trusted delegate join receipts

The `runtime-delegate-receipts` acceptance gate requires five real tool/manager
regressions and 28 exact subcases, for **33** package-qualified run/pass entries:
`TestDelegateJoinOutcomeBoundToServerChildren` (joined, wrong-child, wrong-batch,
failed, missing, no-identity), `TestDelegateTasksRealManagerRecordsJoin`,
`TestDelegateTasksRealManagerRetryRecordsJoin`,
`TestDelegateRetryJoinRejectsAlteredIdentity` (joined, forged, json-copy, child,
batch, task, parent, initial-child, attempt-count, attempt-number, attempt-child,
attempt-status, attempt-error, reordered, missing-attempt, failed, error,
missing-result, wrong-call, cancelled), and
`TestDelegateJoinRequiresWholeBatchSuccess` (false, true).
Missing or skipped parents/leaves fail even when the package exits successfully.

`TaskResult.MatchesJoinedTask` checks manager-private provenance against the
original admitted batch/task/parent/initial child and the manager-observed final
child and ordered attempt history. This provenance is not serialized in public
JSON and cannot be reconstructed from child output or a JSON-copy result. The
actual retry fixture fails after a read, evaluates continuation, and successfully
joins a distinct second child; altered identity or attempt metadata cannot attest
that join. Partial-batch failure and cancellation produce no successful receipt.

A receipt attests a joined server task only: it is **not verified child text**,
verified child effects, changed files or published artifacts. Child prose remains
untrusted even when its server task joined successfully. There are no broader grants:
restricted child authority, exact-action approvals, current membership/lease checks
and uncertain-effect no-replay remain independent requirements. These in-memory
manager/controlled-model fixtures do not prove physical child-process reaping,
Docker/Kubernetes parity or full runtime/controller acceptance.

The controller inventory also requires current activation/resume/schedule
rebinding regressions: `TestBotDraftScheduledActivationConsent`,
`TestBotDraftScheduledActivationRejectsCorruption`,
`TestBotDraftScheduledActivationRollback`,
`TestBotDraftScheduledActivationLegacyReadinessCAS`,
`TestBotResumeDisabledScheduleApproval`,
`TestBotResumeDisabledScheduleRejectsCorruption`, and
`TestBotAuthorizedScheduleQueuedReconciliation`, with every exact personal/team
leaf. Reviewed draft recurrence stays inert until separate activation consent;
readiness/CAS and receipt rollback failures cannot enable it. Exact approval
resumption keeps recurrence disabled; rebinding is not new consent or a new grant.
Existing `TestReviewedConfigurationRejectsStaleScheduleBinding` and
`TestReviewedConfigurationResumeKeepsSchedulesDisabled` remain required too.
These SQLite regressions do not waive disposable PostgreSQL or physical
scheduler lifecycle evidence. All full runtime/controller blockers remain.

### Actual delegated reads, alias ownership and persisted leases

`TestChatRuntimeActualDelegateReadFreshAuthority` requires 26 exact leaves:
personal/team × all three modes, with successful reads and membership revocation
before the next model/tool boundary; Bot modes additionally cover lease-fence loss
at those boundaries. An actual sub-agent calls the shared node `read_file`, receives
exact bytes through its child alias, and cannot execute the parent's shell action
or consume its shell grant. The parent positive control executes afterward. The
membership repository is SQLite-backed; delegate lease-fence mutation and node
transport are controlled fixtures, not persisted lease-transition or physical
container evidence.

`scopedChatDelegates` tracks this request's active delegate calls and freshly
announced aliases. Cleanup cancels and joins active children **before** releasing
owned aliases; repeated cleanup is once-only. The optional
`sandbox.ChildAliasLeaser.LeaseChildAlias` returns an alias-only release function,
not permission to invoke `ToolNodePool.Remove`: that lifecycle operation can evict
the parent and siblings. Backend and node-adapter implementations leave borrowed
aliases and replaced clients untouched; single-client views borrow their pinned
client. Legacy/custom pools without the optional interface retain existing alias
behavior and are never destructively removed by request cleanup. This does not
promise alias reclamation for those legacy pools.

`TestChatRuntimeDelegateAliasCleanupConcurrent` and
`TestChatRuntimeDelegateCleanupJoinsBeforeAliasRemoval` exercise concurrent cleanup,
late-call rejection, cancellation/join ordering, once-only release, and preservation
of parent/preexisting aliases. The separate `runtime-child-alias-leases` gate
requires `TestChildAliasLeasePreservesBorrowedClients` for both `backend` and
`node-adapter`, plus `TestChildAliasLeaseLeavesReplacementUntouched`. These are
in-process pool fixtures, not physical remote-process termination/reaping evidence.

`TestChatRuntimePersistedLeaseTransitions` requires 12 exact personal/team ×
interactive/autonomous Bot × renewed/expired/replaced leaves (ordinary Chat has no
Bot lease). Actual SQLite admission, claim, renewal and recovery run against the
same virtual clock as dispatch. Renewal preserves worker/fence and permits a
retained executable beyond its original lease snapshot TTL by reading fresh
persisted state. Expired/replaced authority is rejected before node dispatch or
grant consumption; recovery admits new work under a new worker/fence, reconstructs
the runtime, and consumes a fresh exact autonomous grant without reusing the old
one. Grant consumption and node transport are controlled; this is not durable
approval-store, scheduler recovery or Docker/Kubernetes acceptance.

The driver rejects missing/skipped parents and leaves even if the package passes.
All runtime/controller remaining requirements stay blocked.

### Prepared autonomous plans: exact approval and recovery

`TestBotPreparedPlanAnnouncementResume` exercises announce → approved mirror →
running → separately approved verifier → separately approved evidence mirror →
Results in personal/team scopes. It uses the production factory, API adapter,
shared file/shell dispatcher and SQLite, with a controlled model, approval guard,
node transport and activation readiness. This is a successful integrated autonomous
plan lifecycle, not HTTP routes, scheduler or physical backend acceptance.
Trusted plan-definition consent is supplied explicitly; it is not inferred from
an editable `PLAN.md` or conflated with per-effect approvals.

The trusted content-backed prepared record binds the engine operation/candidate,
base identity, exact action/digest, approval and dispatch identity, settled output
and publication state. SQL retains coordination metadata, hashes and content
references, not prepared content bodies. Current membership and lease authority
are checked again on recovery. The verifier's exact approval cannot authorize the
subsequent mirror: that mirror needs its own exact approval for the saved bytes.
Resuming reconstructs the engine without a model call, publishes the saved candidate
and settled evidence, and does not replay the verifier or an already published
mirror/Results. Unknown dispatch outcomes remain uncertain, never replay authority.
Ordinary exact-action resume remains unchanged when no prepared operation is bound.

`TestBotPreparedPlanVerifierFailureResume` runs a real exit-7 verifier through the
same dispatcher, retains failure evidence after mirror approval and reconstruction,
rejects Results publication, and proves the verifier ran once.
`TestBotPreparedReviewMissingEngineNeverDispatches` rejects both
`awaiting_approval` and `published` recovery without the private engine **before**
any saved-leaf effects. Engine tests additionally cover stable candidates, callback
isolation, concurrent waves, stale bases, uncertain restart, and settled evidence
without command replay. The personal/team store tests cover atomic approval and
dispatch intent, rollback/content integrity, SQL-body exclusion, SQLite reopen and
idempotent publication; they are not PostgreSQL or physical retained-storage proof.

Mandatory runtime gates `runtime-prepared-plan`, `runtime-prepared-review`,
`runtime-prepared-engine` and `runtime-prepared-store` require exact parents and
scope/stage leaves, including missing/skipped-leaf rejection. These focused passes
do not close the full three-mode capability, authority or backend matrices.

### Confined adapter contract (not the ordinary production file catalog)

`BotFileToolsWithReceipts` supplies six concrete confined adapters for explicit
adapter callers and their tests. These contracts do not describe the shared
production file-tool catalog. Matching native names do not acquire their trust
marker; capability presence is **not** permission. Title-confirmation remains
question-only; paused viewing does not start background work.

| Tool | Contract |
|---|---|
| `read_file` | Home-relative UTF-8 byte window, default 64 KiB/max 256 KiB; digest and continuation offset. Binary bytes do not enter model context. |
| `find_files` | One bounded page of immediate children, optional basename glob and cursor; no recursion or provisioning. |
| `file_tree` | Same lazy immediate-child listing; expand directories explicitly. |
| `write_file` | UTF-8 below `outputs/`, max 1 MiB; create-only or expected SHA-256 CAS, followed by independent retained readback. |
| `edit_file` | Exact `outputs/` replacement with expected SHA-256; ambiguous matches require `replace_all`. |
| `publish_file` | Snapshot saved `outputs/` bytes into immutable storage with bounded title, purpose and memory summary; binary files need not enter model context. |

Paths cannot escape Home or expose hidden/private/authority/credential/policy
objects. The private Task/content workspace is not the ordinary Home tree.
Instruction and memory edits require dedicated reviewed/trusted paths, not generic
file writes. Tools retain scope, cancellation, live lease, current policy, exact
approval and budget checks, including deferred dispatch. No shell/network privilege
follows from writing a file. Readback attests only the local file effect, never
execution of its contents or an external result.

`BotFileReceipts.Snapshot()` is an out-of-band accumulator of actual snapshots;
model-authored JSON cannot supply it. Repeated same-path/digest snapshots reuse
the run-local receipt. This is not itself a committed chat publication or proof
that `MEMORY.md` was updated; publication/index acceptance remains separate.

## Configured model-backed search

`BotChatSafeResources.ProviderFactory` is an explicit trusted dependency, just
like ordinary Chat's `chatruntime.BuildOptions.ProviderFactory`. Production
supplies `provider.GetProvider` with `BotScopedChatConfig`, resolved strictly from
the authorized platform/org/team settings rather than the daemon bootstrap
provider map. Missing settings or missing factory fail closed. Bot confines
provider secrets at invocation (no process-environment fallback), checks the live
binding, and wraps the auxiliary search model in the same run budget before
passing the factory to the shared constructor. The ordinary
`perplexity_web_search` implementation remains behind shared dispatch; its presence
does not grant autonomous permission. The bounded mandated-search extension and
its route/quota checks remain separate and unchanged.

`TestChatRuntimeConfiguredSearchProviderParity` executes that real search tool and
provider adapter against a loopback Chat Completions endpoint across personal/team
Chat, interactive Bot and autonomous Bot. It checks selected credentials, request
and sourced result, lazy initialization, exact-action approval/consumption,
cancellation, foreign principals, stale leases, run-budget reservation/settlement
and denied model-budget dispatch. `TestBotSharedSearchFailsClosed` verifies missing
injection, absent scoped credentials despite an environment key, nil models and
factory errors. `TestBotScopedChatConfigStrict` checks strict cascade and detached
provider maps. These are controlled package tests, not a paid provider, live
conversation, scheduler trigger, Docker or Kubernetes acceptance tier.

## Plans

The factory installs the existing `announce_plan`, `update_plan`, and
`announce_completion` implementations with invocation-local callbacks onto the
Bot's ChatAgent. Announced plans are approved for bookkeeping inside an already
published Bot run; that does **not** approve their verification commands.
Replacement of an active approved plan is rejected. Completion is rejected until
all phases have passing verification evidence, and it does not rerun commands.

Verification calls the same bound `shell_command` adapter as ordinary Bot tool
execution, retaining budget, policy, lease and sandbox checks. It never invokes
Chat's daemon-host default verifier. Plan markdown is placed in ADK session state
(`bot:execution_plan`) for restoration on subsequent attempts. A bound file-mirror
attempt is not guaranteed: the confined `write_file` rejects root `PLAN.md`, absolute
paths and writes without create-only/digest CAS. Do not treat plan state as proof
of a retained file. Existing plan
progress events are reused; no UI/TUI protocol or Code-mode interface changes.

## Delegated output

`delegate_tasks` uses the factory's native per-invocation SubAgentManager. Its
large-result store and `read_task_result` share a captured request-local store.
No global cleanup goroutine or daemon-global output store is used by Bot runs.
Results are invocation-local, not durable across worker restart; small outputs
continue to be returned inline under the existing schema. Ordinary non-Bot Chat
retains its compatibility fallback.

All these tools remain in Chat's existing lexical catalogue/progressive dispatch
path; their policy checks are not bypassed by direct or deferred invocation.

## Evidence

- `pkg/launcher/bot_basic_tools_test.go`: factory-built personal/team SQLite memory
  save/search/get/overwrite; plan replacement and premature-completion rejection,
  failed/passing guarded verification and session-state restoration; actual
  controlled delegated child output retrieval and cross-invocation isolation.
- `pkg/store/entstore/bot_memory_runtime_test.go`: personal/team runtime memory
  CRUD/search, foreign/prior session rejection, stale-fence rejection.
- `pkg/tools/scoped_plan_tools_test.go`: scoped callback selection and global
  delegated-result isolation.

- `pkg/launcher/bot_file_tools_test.go`: confined catalogue adapters, invalid and
  unsupported access, bounded reads, ambiguous edits and independent readback.
- `pkg/daemon/bot_filefirst_worker_http_test.go`: production-worker file-first
  HTTP fixture; this is not a substitute for physical backend acceptance.

These are controlled package acceptance tests, not proof of a live daemon,
container, external model provider, or PostgreSQL deployment. Fresh file-first
acceptance must additionally prove committed publication/index deduplication,
reopen with critical files plus bounded MEMORY but no old full report, explicit
prior-file retrieval, denied stale/foreign/cancelled writes, and ordinary Chat
regression coverage.

### Shared constructor capability matrix

`TestChatRuntimeCapabilityParityConfiguredHTTPAndFlow` in
`pkg/launcher/chat_construction_test.go` exercises `http_request` directly and
as a nested `run_flow` step through the actual Studio Chat, interactive Bot, and
autonomous Bot constructors in personal and team scopes. It uses the existing
in-process node transport plus an authenticated loopback HTTP fixture; responses
are produced by the real HTTP tool and flow engine, not a canned capability
result. It checks selected versus opposite-scope flow/credential stores, pinned
session identity, lazy transport use, cancellation, foreign principals, stale
Bot leases, and exactly-once Bot attempt cleanup. The existing fixture policy
classifier and budget remain test dependencies; this does not establish the
production authorization/budget matrix.

This is a scoped package-test contract, **not physical backend evidence**. It
does not establish file/shell/process/search/browser/MCP/A2A/drill/email/plan/
template parity, scheduler lifecycle acceptance, or the dedicated Kubernetes
tier. Those configured operations and the full three-mode authority/lifecycle
matrices still require their separate acceptance fixtures. Missing infrastructure
must remain blocked rather than becoming a skip or a claimed pass.
