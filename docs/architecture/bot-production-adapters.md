# Bot production capability adapters

## Canonical construction and repair boundary

Production `NewBotChatAgentFactory` adapts owner/run dependencies through
`newBotScopedChatRuntime` (`pkg/launcher/bot_shared_runtime.go`) into
`NewScopedChatRuntime`. Ordinary executables, discovery, children, plans, flows and
drills come from shared Chat construction; Bot supplies scoped authority, accounting,
memory/scheduling and immutable-publication hooks. `BotCatalogueTools` and the
adapter recipes retained below are historical/helper implementation descriptions,
not the production assembly recipe or evidence of production capability parity.
Unused compatibility closures are not removed by these boundary repairs.

The factory pins the actual shared-catalog `*sandbox.NodeTool` for `web_fetch`.
`botSharedDispatch.Dispatch` decorates only that exact executable's ready transport
with `botPublicFetchClient`, using `NodeTool.RunWithTransport`. Argument validation,
client acquisition, readiness, network policy, current authority and shared
accounting still precede dispatch. The private `_bot_public_fetch` operation records
HTTP exchange/body-read failures as failed reads; validation, cancellation, policy,
node-transport and credential-bearing URL failures retain native errors.
The server-only `ToolSourceReadReceipt` binds run, call, capability and argument/result
digests. Remote JSON, a matching name or an error string cannot mint it. Exact final
agent output can reach immutable committed publication after settled read feedback;
this is not factual certification or transparent retry of an external effect.

`BotMetadataOnlyToolset.BotMetadataOnly()` is an explicit declaration-enumeration
contract: no process startup or credential resolution. `LazyMCPToolset` attests it;
`SanitizedToolset`, `FilteredToolset` and `chatRuntimeToolset` forward the underlying
attestation rather than assuming every wrapper safe. Core-only delegated work may
enumerate cached metadata without starting MCP, but any actual executable still
passes the original scoped policy/accounting boundary. Unattested eager toolsets
fail closed; filtering never reconstructs executable identity from names.

Deterministic shared-factory/API and built-client fixtures prove only their recorded
sequences. Helper adapter success alone does not prove shared production wiring.
Physical Docker/Kubernetes/node deployment, PostgreSQL acceptance and corrected-head
GitHub Build/lint are separate gates, not repaired or passed by this documentation.

Bot runs use Chat's existing implementations, but catalogue tools are wrapped by
`botBoundTool` before dispatch: owner scope, live lease/revocation, exact action
policy and shared run accounting precede adapter execution. Accounting records
usage but does not enforce historical runtime, token, tool-call or cumulative
execution caps. There is no host fallback when an adapter is unavailable.
Production workers use a five-minute fenced lease with fresh authorization,
accounting and lease renewal every twenty seconds. The longer expiry is only
crash/stall tolerance for large retained stores;
it does not cache or extend caller authority, suppress heartbeats, or permit a
late result after renewal failure.

## Private Employee-directed Chat (in progress)

The Bot adapter now installs `EmployeeChatService` before factory construction.
`BotChatResources.WorkerFactory` creates fresh shared Chat bindings while retaining
`BotRun.SessionID` for sandbox, lease and budget ownership; a server-generated
`ConversationID` selects only the private ADK history. Worker turns serialize and
persist started/event/settled envelopes under the source run lease. An unsettled
dispatch is not replayed. Worker questions are excluded; private output is returned
to the Employee rather than streamed to Home. The Employee catalog removes direct
execution/discovery/delegation views.

Private worker catalog reconstruction uses `botProviderTools`, not the raw Chat
bridge constructor, so `search_tools` and `describe_tools` retain their sealed
metadata identity, live authorization and verified metadata receipts. Resolved
execution targets still use the original action/approval boundary; no tool grant
is added. `TestEmployeeWorkerDiscoveryPreservesMetadataReceipts` constructs the
actual interactive worker, executes both helpers, verifies their receipts and
checks cancellation. Real-model HTTP acceptance additionally requires the first
private dispatch to search, describe and execute `read_file`, return the exact
controlled source bytes, and deliver a completed result to the Employee. Its
recorded historical sources do not establish fresh web or physical sandbox access.

Evidence lookup reads validated private runtime envelopes from the same scoped run
transcript. It requires a preceding worker dispatch and matching call ID/tool name,
retains the durable outer event ID and hashes only the actual response bytes.
Worker prose, malformed/model-authored envelopes, stale dispatches and responses
after settlement cannot mint source evidence. This keeps successful private reads
available to the original work preflight without publishing them to Home.

`botProgressSessions` uses these same validated private response envelopes for
recovery progress (`snapshot`, `currentResponse`, and `promptEvidence`). Only
successfully persisted events from the current scoped run qualify, with a matching
preceding dispatch and actual function-response bytes. Prior Home worker traces,
worker prose, stale or malformed responses, and model-authored lookalikes cannot
mint progress or justify replay. Evidence tails are bounded context windows, not
execution quotas; recovery retains the original run, lease and usage ledger.

The exact launcher-created `employee_chat` functiontool is now sealed by
`NewBotEmployeeChatTool`. Native dispatch recognizes only its concrete private
wrapper before ordinary action authorization; declaration registration replaces
the raw ADK target with that same wrapper. A plain same-name tool, MCP target or
foreign context does not receive this path, and `allowed_tools` is unchanged.
The wrapper validates arguments and requires matching Bot identity/session plus
an available Employee service. Its trusted production binding rejects worker,
research, control-only and title-question modes, then runs the existing live owner,
run/session, lease, revocation/capability, isolation and original-budget checks.
Internal return evidence attests only a definite typed service response with a
nonzero UUID worker handle and status `completed`, `partial`, `needs_input`,
`failed`, or `cancelled`. Failed/cancelled worker status does not mean the service
failed to return. Nil results, service errors, and invalid handles/statuses mint no
receipt. This observation never attests worker prose or external effects; actual
worker uncertainty remains separately aggregated.

Interactive Employee orchestration accepts a nil autonomy policy only with a
current turn authorizer and the sealed live service binding. Legacy orchestration
still requires its scoped RunPolicy. The server-created WorkerFactory installs
`agent.WithBotPrivateWorkerExecution` in the captured private worker context; no
public request flag can select that boundary. Normal interactive tool authorization
runs first, then the classifier and live guard must authorize the exact action or
request a durable approval through the adapter capture. The factory uses
`api.BindBotChatPolicyRuntime` for these interactive workers as well as legacy
runs, preserving approval capture/resume guards without minting autonomy policy.
Generic interactive Chat dispatch is unchanged. Progressive-to-terminal handoff
uses `AuthorizeBotBoundTool` so an approval is not consumed twice.

`TestEmployeeWorkerInteractiveApprovalBoundary` is the production-factory
failing-first regression: a private source read previously executed with zero
guard calls; now it stops at the exact-action approval boundary. Agent tests cover
nil-policy sealed Employee dispatch, cancellation, exact captured approval,
foreign binding, wrong digest, missing guard and unchanged generic interactive
behavior. `TestEmployeeWorkerProductionLiveGuard` exercises the daemon's actual
nil-policy private boundary with a live turn registry: matching identity/principal,
owner/version, lease/session/fence/expiry and current tool authority are mandatory.
Only mechanically verified reads bypass exact approval; unsafe or unverified
workers request durable approval. Approved actions require fresh current-lease
read-back matching the exact checkpoint and action, approved status and expiry.
Spend remains unsupported, and this guard never consumes an approval: the resume
adapter owns atomic one-use consumption. A private marker alone grants nothing.
API/HTTP one-use continuation remains a separate integration gate.

`TestEmployeeChatSealedDispatch` exercises exact registration, same-name denial,
argument validation, missing/foreign/cancelled contexts and live binding rejection.
The loopback personal/team HTTP fixture now completes a real read and same-history
follow-up through this sealed dispatch. It is **not acceptance-complete**: its
injected `BotChatResources` omits the durable Work adapter, so the historical
producer branch returns fallback provenance and commit rejects the admitted run's
work contract. Production-factory HTTP composition and exact worker action/plan
approval resume ownership remain unverified; no paid-provider/sandbox behavior
is established by these deterministic tests.

## Request-proportional research evidence

`pkg/agent/bot_prompt.go` defines the typed string constant
`botResearchEvidenceGuidance` and appends it to both ordinary Employee prompts
and private-worker prompts before the worker early return. Title-confirmation
question-only stages receive no research rule; ordinary Chat and Code are
unchanged. Initial and durable-work-refreshed prompts retain the same standard.

For ordinary news and factual lookup, a dated substantive configured Perplexity
answer with real source links can adequately support the facts its returned text
covers. This is not unconditional factual certification, proof that linked pages
were read, or a presumption that every query has current event dates. Materially
unknown freshness remains disclosed. Requested depth and coverage govern the
answer; proportionality must not turn detailed work into a shallow summary.
Blanket source audits, raw extracts/quotations and per-item publication metadata
are not default requirements. Targeted follow-up addresses missing explicitly
requested details, material contradictions, high-consequence decisions/actions,
or explicit requests for audits, primary sources or exact quotations. A blocked
article alone does not justify repeated query/URL variants when search evidence
is adequate: reuse retained evidence, qualify or omit disputed claims and retain
unrelated supported coverage. For explicit quotations, both roles use the exact
actual returned primary-source passage, not reconstructed or hypothesized longer
text from prior knowledge. Further inspection addresses actual truncation,
omission or conflict affecting that quotation; a complete returned passage is
quoted faithfully rather than triggering an invented completeness audit.

The Employee passes its chosen depth and evidence stopping condition to the
private worker and assesses the returned substantive sourced result under that
same standard, without demanding raw extracts afterward absent a concrete cause.
General inspect/correct guidance assesses applicable requirements rather than
introducing a second mandatory article audit. Information-sourcing confidence
never certifies tool execution: sends, writes, schedules and other effects still
require current authority and actual committed execution evidence. No runtime
caps, execution, authorization, storage or client protocols change here.

`TestBotPromptResearchEvidenceRoles` is failing-first prompt-contract coverage
for ordinary/audit/contradiction/side-effect rules in initial and refreshed
Employee (interactive/background) and worker prompts, including exactly one
copy of the shared constant. `TestBotPromptResearchEvidenceExclusions` protects
ordinary Chat, Code and the no-research question-only stage. These tests establish
prompt delivery only, not live-model judgment, source accuracy or paid-provider
research quality.

`TestResearchDepthServiceFixtureContrast` checks the controlled primary page's
complete 2026–2028 early-close sentence (including eligible-options hours and the
day-after-Thanksgiving condition), aligned holiday row, and non-truncated fetch
metadata including title, URLs, status, content type and length. The opt-in
`TestBotSAPResearchDepthHTTP` pairs an ordinary search-sufficient request with an
explicit direct-primary-verification request using actual SAP Employee/worker
models and deterministic search/page services. Acceptance requires a succeeded
run and reviewed Employee publication; the ordinary case requires exactly one
search/private dispatch and no page fetch, while explicit verification requires
an actual `_bot_public_fetch` response and the complete fixture sentence in the
final answer. This fixture does not prove live NYSE, Perplexity or Docker access.

## Committed owner-priority dispatch

Production `BotWorker.Start` owns three joined loops: bounded mailbox admission,
independent durable discovery, and maintenance. It never calls the synchronous
`Poll` fixture wrapper. The optional `Services.BotRunStateNotify` callback is
composed once with a request's existing notifier; repository callbacks still run
only after the outer SQL transaction commits. Worker and scheduler owner contexts
carry the same callback.

A hint retains only Bot/run identifiers, routing identifiers, sequence and lane,
never the request context, hydrated content or authentication authority. The
capacity-256 mailbox coalesces newer pending sequences and drops acceleration on
overflow (counted by `dispatchOverflow`); a separate discovery generation recovers
durable queues. Each run page is visited once, with typed teardown, owner and
autonomous priorities. Fresh directory membership, current Bot/run state, original
interactive caller-team/TurnRegistry checks and Supervisor SQL claims remain the
authority. Actual run metadata corrects the hint's lane before execution, so cron
work sharing a Home cannot consume the reserved owner capacity.

Two execution slots allow at most one autonomous run, reserving capacity for owner
work. Process-local per-Bot exclusion lasts through runtime completion and joined
teardown; maintenance locks only its own Bot. Same-Bot cleanup/live-claim checks
remain mandatory, while unrelated maintenance cannot block owner admission.
Pending hints for active Bots survive completion and are freshly resolved; newer
nonqueued states invalidate pending acceleration without undoing committed effects.
Stop and Drain close admission under the same mutex as WaitGroup additions. Stop
cancels and joins loops, polls and executions; Drain stops discovery/maintenance
and requests progress handoff before joining, falling back to Stop on deadline.
`TestBotWorkerDispatchCapacitySameBotAndJoinedStop` holds admitted attempts at
fresh identity resolution to prove autonomous capacity cannot occupy the owner
slot, a newer same-Bot hint survives without overlap, and exclusion is retained
until cancellation has joined even a slow adapter. Concurrent-notifier/Stop and
trusted-routing tests additionally exercise the closed admission gate and request
context lifetime independently of execution authority.

`TestBotPromptDispatchMaintenanceBlocked` exercises real ephemeral loopback HTTP,
scoped SQLite, production routes, `Start`, Supervisor claims and shared Chat's
controlled model in personal/team scopes. Maintenance and another Bot's autonomous
startup remain held while owner work must reach claim/model within five seconds.
It checks duplicate hints, per-Bot slot ownership and an independent worker's live
claim check. This is controlled framework/HTTP evidence, not a paid provider,
physical Docker/Kubernetes or deployed-daemon latency claim.

## Shared-construction policy checks

`NewScopedChatRuntime` rejects concrete catalog entries that cannot implement the
scoped executable boundary when context binding, authorization or accounting is
installed. It must not publish such tools unchanged. A cancelled tool invocation
is rejected before the underlying handler dispatches. The same guarded object is
used in direct and catalog views, and additive hooks cannot replace ordinary
capabilities by name. Explicitly transferred cleanup executes once, including on
constructor failure; injected shared resources are not implicitly owned.

The focused `TestChatRuntimeCapabilityParity` retains the fixture-registration
identity/accounting/denial check and runs `configured-three-mode-subset` from
`chat_runtime_three_mode_test.go`. The subset constructs ordinary scoped Chat and
both actual Bot factory modes with the same configured node pool. It executes
built-in `read_file` and `filter_json`, checking exact file output and filtering,
run-session binding, cancelled calls, paused-Bot reads, autonomous shell refusal
without exact approval, and exactly-once Bot cleanup. Its node transport invokes
`tools.ExecuteTool` on test-owned temporary data; no paid model or user daemon is
involved. The repository authority is a controlled fixture, not fresh SQL
membership evidence.

This is partial constructor/dispatch evidence, **not** full three-mode capability
parity. The full runtime gate remains blocked until identical configured
file/shell/process/search/browser/MCP/A2A/flow/drill/email/plan/template execution,
personal/team authority matrices, production wiring and Docker **and** Kubernetes
lazy-session lifecycle/recovery are exercised. Separate ordinary Chat HTTP/protocol
fixtures cannot stand in for the missing three-mode sequence.

## Complete shared runtime composition

The ordinary host bootstrap now supplies resolved delegation callbacks, context
capacity, flow registry/executor, knowledge retrieval and reflector to
`NewScopedChatRuntime`, then returns that constructor's result with host metadata.
It no longer rebuilds delegation, compaction or the flow distiller afterward.
The shared constructor owns scanner configuration, normalized delegated MCP
search/extract names, child event/artifact/progress forwarding, Code authorization,
and isolated parent/child compactors. It discovers no provider or host resource.
Directly supplied toolsets also reach the flow distiller, alongside grouped
sets, using the same guarded instances as the live agent and flow executor.
`TestChatRuntimeDirectToolsetDistillation` verifies successful validation of a
flow using a direct-only capability without executing it and retains denial on
that capability's executable view.

A supplied flow executor receives the bound guarded catalog for scoped/policy
runtimes, and cancellation is checked before dispatch. Unscoped bootstrap flows
retain the live request catalog rather than replacing it with the startup catalog.
This binds the callback; it does **not** certify that every injected flow engine
uses the supplied catalog or eliminates the host InteractiveFlowRunner's existing
resource discovery. That engine requires separate scoped migration.

Host-only legacy plan/graph-plan and tool-manager global registrations remain in
`chat_factory.go` for compatibility, after successful construction. They are not
installed by the scoped constructor. At this historical constructor checkpoint,
Bot production migration was still separate work; the current canonical path is
stated above. Focused constructor tests are not physical backend,
provider, controller or full acceptance evidence.

## Shared node dispatch

Production Bot dispatch uses the shared `NodeTool.Run` pipeline and
`sandbox.RunNodeTool`; the retained Bot catalogue helper's fixed-session adapter
also uses that dispatcher. Structured/map argument conversion, file-path alias
normalization, schema validation, caller metadata copying, node invocation and
result decoding have one implementation. Bot caller cache identity is the pinned
run session, never a model-supplied `_caller`; Chat retains its agent identity.
Missing Bot context/client/session fails closed, without pool lookup or local
fallback. Bot observation records normalized arguments and transport success or
failure after dispatch; the surrounding `botBoundTool` policy gate is unchanged.

Chat alone acquires pooled clients and prepares network policy and HTTP credential
vaults. The dispatcher never imports host credentials into Bot resources.
Context-aware clients may implement `ContextCallingToolClient`; legacy clients
retain synchronous `Call` semantics (including the existing backend execution
timeout), with cancellation checked before dispatch and no speculative retries.
Non-object results use Chat's existing `result` wrapper in both modes.

Backend-client parity tests exercise actual pool/client NDJSON encoding and
response parsing against a controlled backend. These are transport evidence, not
physical Docker execution or model-performance evidence.

## Model-aware generation limits

Production resolves the context window from the selected owner-scoped provider
configuration using ordinary Chat's `ResolveContextWindow`: explicit
`general.context_length`, metadata, curated model capacity, then the 200,000-token
unknown fallback. The immutable per-invocation adapter carries numeric limits
only; it does not retain the provider configuration or change model provenance.
The Bot factory injects this capacity into its compactor. Explicit worker/factory
context overrides (including 32,768) still take precedence.

The shared `BudgetLLM` remains the reservation/settlement boundary. Production
uses accounting-only meters: historic token/tool/model-cost/active-runtime caps
do not clamp provider output or trigger synthesis. Authority expiry, revocation,
lease fencing, external spending authorization and operation health/timeouts
remain authoritative. A known
output capacity selects at most 32,768 tokens per call (and never above that
capacity); unknown output capacity uses a finite 16,384-token fallback. This gives
reasoning models headroom without reserving their entire 128K output capability
on every call. Explicit worker output options bypass this default policy, but not
known model capacity; smaller request limits still clamp the actual provider
request. Finite legacy helper meters can also clamp by their explicit token
allowance; production accounting has none. SAP metadata includes learned limits with
exact static fallback; compatible/LiteLLM and OpenRouter metadata supply their
reported limits. Missing configuration does not trigger a host provider lookup.
External spending authority, retry counts and approvals are unchanged.
Controlled tests prove metadata/override propagation and legacy-helper token
clamping, not
that a particular live reasoning task succeeds with the new ceiling.

## Bounded final synthesis recovery

`bot_synthesis_generation.go` handles only the factory's final-only synthesis
path. It buffers nonstreaming output and drains the original progress/BudgetLLM
iterator before considering a correction. Explicit output-limit termination
(`MaxTokens` response or a sole typed output-limit `EmptyCompletionError`) permits
one concise, complete rewrite, including visible truncated or reasoning-only
results. A typed empty stop completion retains its existing correction path.
At most 8 KiB of visible UTF-8 draft text is retained in the correction as a
JSON-quoted, untrusted excerpt, never reasoning, tool payloads or attachments.
The replacement must stand alone; a draft is neither published nor treated as
proof of external effects. Request contents/configuration are not mutated.

The same single correction slot covers forbidden-tool format correction: no
third attempt, increased ceiling, reopened research or replay. Missing terminal
metadata, partial-only output without known output-limit evidence, filtering,
interruption, buffer overflow and independent trailing errors fail closed.
A later stop cannot override an earlier output-limit or unsafe ending. Only a
nonpartial visible answer with stop/turn-complete evidence and no veto is
published. Typed errors retain `errors.Is`/`errors.As` cause matching.

Each rejected attempt emits payload-free diagnostics: typed reason, attempt and
response counts, partial/turn-complete flags, stop/output-limit/missing/other
finish counts, interrupted/error-code counts, visible bytes, thought-part count,
tool presence, completion/buffer flags and error count. No raw finish/error
strings, model prose, reasoning, tool arguments or credentials enter this
summary; successful recovery still leaves first-attempt diagnostic evidence.

Before retry, current authorization, parent cancellation, lease and shared budget
are rechecked, and the progress boundary drains settlement and rejects unsafe
active/uncertain dispatches. Both attempts use the same model, run, request output
ceiling and accounting meter; no historic execution quota reduces or refuses the
production correction. Authority, cancellation and operation-health failures
still refuse dispatch.
Failed-response usage reaches BudgetLLM before output is withheld. No tools,
external effects, runs or research are replayed. Assessment/progress calls and
ordinary Chat retain their existing paths. Controlled
`TestBotSynthesisEmptyCompletionHTTP` and
`TestBotSynthesisOutputLimitCompletionHTTP` exercise empty/visible length → final
design over local OpenAI-compatible HTTP through the composed factory and API
adapter (legacy work review policy), checking retained draft privacy, shared
metering and publication. These tests do not establish semantic review or
paid-model quality, nor whether truncation or missing metadata caused a reported
live failure.

## Initial title confirmation

New title runs enter `needs_question`, not executable work. After owner/session/model
resolution and budget wrapping, `NewBotChatAgentFactory` returns a dedicated Chat
agent exposing only the genuine `ask_user` handler. It does not call `Prepare`,
`NewWiredChatAgent`, research, Task, learning, or operation setup. The authoritative
Bot/run/owner/lease and pending state are rechecked at model and tool boundaries;
a dedicated policy permits only a valid three/four-option question with custom
text. Convenience resource pickers and permission requests are rejected.

The model boundary buffers responses and allows one same-budget correction for
plain text, malformed questions or premature work calls. Only a durable question
checkpoint counts as success. The API bypasses work production, journals and review
for this stage. Home preflight retains authorization but skips physical provisioning;
Supervisor similarly skips Ensure/BindRun via `RequiresPhysicalHome`.

After an authenticated correlated answer, the bound confirmed assignment enters
normal execution and physical Home preparation resumes. The original title and
call/result remain intact; the owner answer is a correlated user event, not a second
tool response or permission grant. Nil confirmation preserves historical behavior.
`bots_acceptance_driver.py --stage title-confirmation-contract` exercises controlled
factory and HTTP journeys, including interactive/autonomous/mandated factory
paths, option/custom answers, revoked authorization and stale leases. The Home
HTTP test separately checks that pending-title preflight makes zero backend calls
while ordinary message preflight retains provisioning, retry and cancellation.
Real-provider quality and actual sandbox provisioning require separate isolated
acceptance evidence.

## Configured public research

The mandate branch installs `BotConfiguredResearchTool` before its retrieval-only
return. Both Bot factories retain the same run-bound `BotResearchEpisode` and
check detail retrieval on the logical executable, including progressive dispatch.
New owner-backed scopes pin the effective configured Perplexity provider/model or
Tavily MCP executable. Perplexity reuses `RunPerplexityWebSearch` with confined
provider secrets and shared accounting; Tavily dispatches through the pinned node
and symbolic scoped vault, not a host MCP fallback or sibling-tool exemption.
Missing configuration, empty results and failed searches leave detail retrieval
locked. Search URLs/snippets are discovery evidence, not certified values.
Provider initialization errors retain Chat's diagnostic through the existing
provider-value redaction boundary; authentication or deployment failures must not
be mislabeled as `bot capability unsupported`. The local
`TestBotConfiguredSearchSAPProductionParity` fixture exercises actual SAP token
exchange, deployment discovery and generation for initial mandated and follow-up
research, plus authentication/deployment failures with detail retrieval locked.
It proves adapter parity using controlled HTTP responses, not live SAP health or
the cause of a historical failure whose diagnostic was discarded.

The isolated `bot_task_acceptance.py --stage search` gate builds a fresh temporary
candidate, requires personal/team owner HTTP search-before-detail ordering and
physical node MCP transport, and rejects skipped or foreign test evidence. It
makes no paid calls; live provider quality and full process recovery remain
separate acceptance requirements.

## Prior scheduled occurrence awareness

The owner-bound factory provides `BotChatResources.PriorRun` alongside the
unchanged `PreviousWork` projection. `bot_work_read` and the automatic
provider-bound durable-work prompt refresh expose an optional `prior_run`:
Bot/run/trigger identities, occurrence creation time, persisted outcome time,
terminal/interrupted status and an allowlisted failure category only. No report
summary, technical error, transcript, old assignment or mandate is copied.

Selection is restricted to this Bot's exact cron trigger, before the current
admission, and chooses the latest **occurrence creation time**, not the latest
failure or completion time. A later success therefore supersedes an old failure;
a coalesced older occurrence cannot supersede a newer actual attempt merely
because its state was committed later. Interrupted recovery is eligible without
`FinishedAt` or `Report.Work`: its persisted `StateCommittedAt` supplies the
outcome timestamp. Rows with no known outcome timestamp, current/future rows,
active runs and other triggers are not predecessor context. The UUID-paginated
scan has the existing history timeout/page bounds; incomplete reads never claim
that no predecessor exists.

The service and prompt independently validate the binding and copy only bounded
metadata. Guidance explicitly distinguishes a newly admitted occurrence from a
retry/replay: learn from the predecessor's outcome under the current assignment
and authority, never resume old tasks, inherit permissions, replay uncertain
effects or change schedules. Missing optional context is not proof of success
and does not block otherwise supported work. No UI/API run-history shape,
admission rule or production record is rewritten by this read path.

## Resolved model provenance

The successful Bot factory carries the owner-scoped resolver's provider instance
and model pair in `BotChatResources.ModelSelection`. The runtime copies it into
`RunResult` on both normal and post-factory failure exits; the supervisor passes it
through the existing lease-fenced `BotRunCommit`. The store persists it in the
existing `BotRun.Provider`/`Model` fields, independently of token and price usage.
This records the selected model for the latest committed attempt, not a model
chosen from ambient defaults or the adapter's display name. A continuation with
no successful factory selection preserves the prior identity; historical empty
records remain unknown. Optional commit metadata is omitted from legacy commit
hashes. No model call or pricing lookup is added. Native adapter observation logs
remain separate evidence of actual adapter invocation, not wire-level proof.

## Shared executable Chat catalog

`launcher.BuildChatCatalog(ctx, inputs, cfg)` shares executable registration with
`NewWiredChatAgent`, preserving the same tool objects across the direct and group
views. `ChatFactoryConfig.CatalogResources` supplies explicit sandbox, browser,
email, skill and extension dependencies. The ordinary factory still owns provider,
host vault, browser, MCP discovery and sandbox initialization; the scoped path
never invokes that bootstrap. Scoped construction requires `chatruntime.Inputs`
and a supplied pool; browser/email transports are absent unless explicitly bound.
Disabled tools are filtered from both concrete tools and lazy toolsets. Catalog
construction is not Bot authorization: consumers must retain the autonomous
policy/budget/lease wrappers and use scoped invocation contexts.

`ScopedChatEmailTools` is the interactive account adapter; `BotEmailTools` keeps
its autonomous authorization, reply evidence and approval checkpoint guards. Both
share mailbox metadata resolution and node transport. Mailbox settings and vault
credentials are same-scope only, with no channel-email fallback. Interactive email
rejects Bot-run contexts; its internal `chat:` node marker permits ordinary reply
semantics without weakening the autonomous recipient-bound snapshot protocol.
The marker is composed by the trusted adapter, not accepted from model arguments.
Node execution still requires sandbox network mode and a scoped vault. Declaration-
only email tools fail explicitly if invoked without a configured transport.

## Flow

`BotRunFlowTool` loads YAML from the owner-injected personal/team flow stores and
uses `AstonishAgent` with `InteractiveFlowRunner` turn/session handling. It receives
the already-budgeted model and already-bound child catalogue. Input nodes pause
and resume within the current run; completed/error sessions are cleaned up. A
flow's auto-approval flag does not bypass child Bot authorization. Drills remain
explicitly unsupported by this adapter. Flow pause state is in-memory, not a
crash-resumable durable flow checkpoint.

## HTTP

`BotHTTPTool` pushes the owner-scoped credential vault to the pinned run sandbox
and sends symbolic arguments to the existing node HTTP implementation. Credentials
and placeholders (including OAuth token refresh) resolve inside that node, not on
the daemon. Fresh bearer tokens and static credential fields are redacted from
response bodies, headers and errors before node results leave the sandbox; the
launcher applies another owner-store redaction pass.

HTTP now honors context cancellation and rejects cross-origin redirects,
including TLS downgrade, so custom auth headers and replayable bodies cannot be
forwarded to another origin. This deliberately tightens the shared HTTP tool's
redirect behavior, including unauthenticated requests. With private networks
disallowed, validated addresses are dialed directly to prevent DNS rebinding and
proxy bypass. Existing sandbox/private-network policy remains authoritative;
this change does not install backend network policies. HTTP file bodies and
multipart uploads still read from the node filesystem.

## MCP

`BotMCPTools` reads only supplied owner-authorized stores; disabled servers are
omitted and preparation is metadata-only. Missing/empty caches expose
`mcp-discovery:server`: an explicit, separately reviewed capability, not an
unsupported placeholder or automatic startup. Its exact approval authorizes
`NodeDiscover` through the pinned sandbox's private `_astonish_mcp` operation
only after live lease/revocation checks, policy authorization and tool-budget
reservation. Successful discovery caches declarations; rebuilding the catalogue
then exposes tool calls, each requiring its own authorization. `LazyMCPToolset`
supplies declaration proxies only: its host transport is never connected.
Invocation uses `NodeCall` with server-qualified identities (`mcp:server/tool`).

The factory installs
`WithBotMCPClassifier(base agent.BotActionClassifier, tools []tool.Tool) agent.BotActionClassifier`
before capturing bound contexts. It binds trusted executable adapters to a
versioned SHA-256 fingerprint of the symbolic command, arguments, URL, transport,
enabled state and environment/header configuration, including credential
references. Resolved vault secrets are not fingerprinted; approval descriptors
persist only the fingerprint, not configuration values. Both discovery and calls
remain unknown effects requiring exact review; model arguments and MCP annotations
cannot supply target authority. The adapter revalidates current configuration
before dispatch, and a changed target invalidates the approved action digest even
when its alias and tool arguments are unchanged. A private factory handoff blocks
unwrapped direct execution. `BotMCPTools` retains its existing signature.

The node supports `stdio` (including empty transport), `sse` and
`streamableHTTP`. It resolves symbolic owner credential configuration inside the
synced session vault, including supported dynamic-token refresh, and redacts
results/errors there and again at the launcher boundary. Remote `Env` entries
are HTTP headers; every request, including an SSE-supplied endpoint, must retain
the original origin before headers attach. Redirects and unsafe transport
headers are rejected. Existing sandbox network policy remains authoritative.

Discovery preserves JSON schemas, pages up to 10,000 tools and rejects repeated
cursors. Each discovery/call has a fresh connection, a 120-second context bound
and an 8-MiB serialized result limit; failed calls are never automatically
replayed. Cached declarations avoid separately approved discovery, not policy
checks. Uncached preparation performs no protocol I/O, credential provisioning
or server startup. There is no daemon credential refresh or host MCP fallback.

## Browser

`BotBrowserTools` allocates one private Chat browser manager wired to the backend
and pinned run, including delegated/viewer-child calls. Besides navigation,
interaction, cookies and storage, it reuses the real async JavaScript, emulation
(offline, headers, HTTP auth, geolocation, media, timezone, locale, device), demo
(overlay, cursor, fullscreen, pause) and DOM action-capture handlers. Every call
receives the pinned request context; canceled calls are rejected before dispatch.
Availability does not exempt any browser action from exact effect review.

`browser_pdf` prints that same run page through CDP, reads at most 32 MiB with a
60-second CDP timeout, and writes using `Backend.PushFile` to the pinned run with
0600 permissions. It accepts only a PDF basename or `/workspace/<basename>` and
never creates a daemon temp file or directory. The sandbox/backend owns symlink
and file isolation. The path is a sandbox artifact reference, not a host download.

The following shared handlers deliberately remain unavailable with their original
schemas and explicit diagnostic errors:
- response-body interception replays requests with the daemon's HTTP client;
- native browser handoff has no durable detached Bot checkpoint adapter; it does
  not open a host listener or wait indefinitely for a human;
- recording needs sandbox-only stat/artifact metadata handling (the shared stop
  handler probes a host path). Profile persistence and new download management
  are not added.

Slides/drill handlers are not blindly wrapped as sandbox tools: the node's
`ExecuteTool` does not dispatch those operations, and the shared drill runner can
invoke children outside Bot-bound effect/budget checks. They require a separately
reviewed transport and child-authorization adapter, not catalogue-only wrappers.

## Retained documents and confined file work

Production file-first composition requires scoped `BotFiles` and content storage;
missing capabilities fail explicitly without daemon-host or legacy SQL fallback.
Docker/Kubernetes backend support must be distinguished from physical acceptance;
OpenShell file-first creation/execution is unsupported, not a reduced-content mode.
Ordinary OpenShell sessions and existing Home/OverlayFS lifecycle are unchanged.

`Documents.Load` reads actual SOUL/IDENTITY/USER/CHARTER files (16 KiB each) and
`MEMORY.md` (12 KiB including a retrieval hint). `LoadMapped` reads available Home
indexes instead of SQL projections. Oversized files are not truncated on disk.
GETs never initialize Home or start work. Bootstrap excludes full historical
reports and latest-N memory dumps; prior content is retrieved explicitly.

`BotFileToolsWithReceipts` installs confined `read_file`, `write_file`, `edit_file`,
`find_files`, `file_tree` and `publish_file` in ordinary execution, interactive and
mandate composition before normal catalogue early returns. Reads/listings are
bounded and lazy; writes/edits/snapshots are restricted to `outputs/`, with digest
CAS for overwrites and independent write readback. Private content receipts,
credentials, policy and executable setup are not writable through these tools.
The private Task workspace is not the ordinary Home tree. Concrete trusted
wrappers replace generic names; presence does not grant permission, enrich old
mandates or waive owner scope, live lease, current policy, approval or budget.
Title-confirmation remains question-only; paused browsing performs no work.

Snapshot receipts come from the trusted `BotFileReceipts` accumulator, not final
model JSON. They attest saved bytes, not execution or committed chat delivery.
Publication-to-MEMORY integration is not certified by this documentation update.
Acceptance must show one committed identity per delivery, immutable prior bytes,
compact deduplicated goals/outcomes/paths and publication refs in MEMORY, and
metadata-only index retry without replaying work. File text never grants rights.

Run `TestBotFileFirstWorkerHTTP` and scoped file/memory/document/mandate regressions;
then separately exercise retained physical storage, reopen/provider-prompt capture,
explicit old-file retrieval, paused/stale/cancelled/foreign access, large binary
download, SQL sentinel absence and backup/restore. Docker, dedicated Kubernetes/
PostgreSQL, built Studio and native PTY evidence are distinct gates; absent
prerequisites block their tier rather than count as a pass. No live validation
was performed for this documentation-only update.

## Memory, plans and delegated results

`BotMemoryTools` preserves Chat's memory argument/result schemas while binding
`memory_save`, `memory_search`, `memory_get` and `memory_delete` to
`NewBotRunMemoryService` over the scoped disk-memory catalogue. SQL retains a
CAS-guarded typed head reference, not memory bodies, embeddings or nested source
snapshots; generic SQL memory Add/Update exclude Bot-linked sessions. Reads
prefilter to this Bot's linked home/run sessions;
mutations require the current run lease. Saves carry run attribution, redact
credential values and may overwrite a matching category only in the current run.
Deletes cannot remove prior-run evidence; verified attributed content cannot be
silently rewritten. Generic owner-wide memory and three-tier search context slots
are shadowed, with no personal/team fallback.

`newBotPlanTools` provides `announce_plan`, `update_plan` and
`announce_completion` using Chat's plan lifecycle. Session state is authoritative
across attempts. A `PLAN.md` mirror is not guaranteed by the confined file tool:
root/absolute paths and writes lacking create-only or digest CAS are rejected.
Plan state alone is not evidence of a physical retained file.
Verification invokes the already-bound `shell_command` in the run workspace,
never Chat's daemon-host default verifier. Authorizing planning does not authorize
the verification command's effects or waive its budget/approval checks.
`read_task_result` uses the request-local delegated-result context rather than a
daemon-global result store. These tools are real implementations, not unsupported
catalogue placeholders.

## Catalogue boundary

`BotCatalogueTools` reuses isolated internal/process tools plus scoped memory,
HTTP, flow, browser and MCP adapters; `newBotWiredChatAgent` adds guarded
`delegate_tasks`, `read_task_result`, the three plan tools and ordinary progressive
discovery. See [basic Chat tools](bot-basic-chat-tools.md) for memory, planning and
delegated-result isolation. Availability is not effect authorization:
`BotCatalogueClassifier` classifies memory save as a reversible write and memory
delete as deletion; memory reads, plan bookkeeping, result retrieval,
`filter_json` and `git_diff_add_line_numbers` are read effects. Other actions
remain unknown and require exact durable approval unless a trusted classifier
establishes their constraints. Plan verification still authorizes the bound
`shell_command` separately.

Browser extras replace 19 formerly unsupported catalogue entries with concrete
implementations. Counts vary with configured adapters and dynamic MCP additions;
source wiring is not permission to execute or proof of a live-backend install.

The production catalogue currently exposes explicit unsupported placeholders for
these names (source: `pkg/tools/declarations.go`, `pkg/launcher/bot_catalogue.go`
and the allowlist in `pkg/launcher/bot_scoped_capabilities.go`):

| Group | Unsupported tools |
|---|---|
| Credentials | `save_credential`, `list_credentials`, `remove_credential`, `test_credential`, `resolve_credential` |
| Scheduling | `schedule_job`, `list_scheduled_jobs`, `remove_scheduled_job`, `update_scheduled_job` |
| Email | `email_list`, `email_read`, `email_search`, `email_send`, `email_reply`, `email_mark_read`, `email_delete`, `email_wait` |
| Skills/search | `skill_lookup`, `perplexity_web_search` |
| Distillation | `distill_flow` |
| Drills/tutorials | `save_drill`, `validate_drill`, `delete_drill`, `list_drills`, `read_drill`, `edit_drill`, `run_drill`, `inject_drill_credentials`, `draft_drill_from_action_log`, `draft_tutorial_blueprint`, `validate_tutorial_blueprint`, `present_tutorial_blueprint`, `blueprint_to_tutorial_drill` |
| Fleet | `save_fleet_plan`, `validate_fleet_plan`, `update_setup_draft`, `get_setup_profile` |
| Slides | `create_deck`, `write_slide`, `get_deck`, `list_decks`, `validate_deck` |
| Templates | `save_sandbox_template`, `list_sandbox_templates`, `use_sandbox_template` |
| Browser extras | `browser_response_body`, `browser_request_human`, `browser_start_recording`, `browser_stop_recording`, `browser_recording_status` |

If backend browser wiring is unavailable, all browser declarations fail explicitly.
Bot-management Chat tools are not added to this run catalogue. Flow recursion via
`run_flow`, `drill`/`drill_suite` YAML, crash-resuming paused flow inputs, host
fallback, production purchase/spend adapters and model-cost-capped production
activation are not supported. `read_pdf` extraction is distinct from the scoped
`browser_pdf` rendering adapter. This list does not change ordinary Chat's catalogue or the
public delegate-preference JSON.

## Dispatch-target security verification

Focused race-enabled regression tests passed for the classified one-use handoff,
email dispatcher/account replacement, email post-vault revocation, MCP target
approval/replay, and drill credential payload binding. The core handoff, email
interleaving and payload tests also passed ten repetitions with `-race`; the full
`pkg/execution` race suite and `TestBotChatFactoryExactGrantDispatchOnce` passed
separately. Logs: `/tmp/astonish-dispatch-hardening-race.log`,
`/tmp/astonish-dispatch-hardening-race-repeat.log`,
`/tmp/astonish-dispatch-hardening-execution-race.log`, and
`/tmp/astonish-dispatch-hardening-factory-race.log`. The existing non-race full
agent/launcher/execution/API pass is recorded in
`/tmp/astonish-phase3-security-final.log`; these results do not complete broader
Phase 3 live acceptance.

- **Handoff:** terminal/lazy dispatch reclassifies the approved action, checking
  target, recipient and effect without consuming the grant twice. Changed
  arguments/identity/policy and handoff replay fail closed. The MCP checkpoint
  fixture JSON-round-trips an approval: changed target has zero effects and zero
  consumption; unchanged target executes once. This is not a real process restart
  or external provider receipt.
- **Email:** the real progressive dispatcher rejects account and same-name adapter
  replacement before credential reads or node calls. After a deliberately blocked
  vault push, revocation, cancellation, isolation loss or account change prevents
  the node call. This does not undo the completed vault push or cancel an already
  dispatched provider operation. `email_reply` now uses an account/content/recipient-
  bound snapshot from a separately authorized read; controlled Graph and SMTP
  fixtures verify the reviewed MIME target, changed-source rejection, one-use
  dispatch and no replay after an ambiguous HTTP 504 response. See
  [email verification](bot-email-tools.md#verification-and-limitations) for commands
  and limits; this is not proof of production delivery or a real worker restart.
  These post-push guarantees are email-specific: the
  current MCP helper checks cancellation before vault sync, not live lease,
  isolation and cancellation again after that blocking operation.
- **Drill payload/privacy:** injection separately authorizes mapping, destination,
  physical sandbox and a process-keyed HMAC of the resolved static credentials.
  Pending approval contains the opaque `payload_ref`, not resolved secret bytes;
  credentials are nevertheless read into daemon memory before this inner approval.
  Rotation before authorization invalidates the old receipt; rotation after
  authorization injects the already-reviewed snapshot, not the replacement secret.
  The snapshot is credential payload, not a digest of the entire drill suite.
  Process restart invalidates the reference; no durable secret snapshot or
  restart-resumable drill credential approval is established. The pending fixture
  only captures/marshals the descriptor, while successful injection fixtures
  preinstall both grants: they do not prove API approval-to-resume of this nested
  action. Privacy assertions cover token/password-like fixtures, not every raw
  `content` credential, report/error path, or memory-erasure property. Mapping
  mutation fixtures still omit `payload_ref`, so their rejection alone does not
  isolate mapping-field binding from missing payload approval.

These are controlled fixtures (including local protocol servers), not deployed
sandbox isolation, production mail delivery, arbitrary concurrent store mutation,
or Studio/terminal approval acceptance. No production code was changed for this
verification pass.

## Isolated candidate provider configuration (preparation only)

`tests/e2eboot/local_bot_smoke_launcher.py --config /absolute/private.json`
accepts the existing unversioned LiteLLM configuration unchanged, or this explicit
version 1 object. Keep the file mode 0600; do not copy a live daemon configuration
or extract secrets from sessions. All values below are placeholders:

```json
{
  "schema_version": 1,
  "binary": "/absolute/fresh-linux-arm64-astonish",
  "docker": "/absolute/linux-arm64-docker",
  "layers": "/absolute/isolated-layers",
  "browser": "/absolute/native-browser",
  "embedding_cache": "/absolute/hash-pinned-model-cache",
  "provider_instance": "New AI Core Provider",
  "provider_config": {
    "type": "sap_ai_core",
    "base_url": "https://api.example.test",
    "auth_url": "https://auth.example.test",
    "client_id": "operator-supplied",
    "client_secret": "operator-supplied",
    "resource_group": "operator-supplied"
  },
  "model": "anthropic--claude-4.6-sonnet",
  "intended": {
    "provider": "New AI Core Provider",
    "provider_type": "sap_ai_core",
    "model": "anthropic--claude-4.6-sonnet"
  }
}
```

The optional `bot_routine_policy` retains its existing restricted semantics.
Unknown versions/fields, duplicate JSON keys, incomplete credentials, and mismatched
intended identities fail preflight without echoing supplied values. Version 1
supports explicit `sap_ai_core` fields above, or `litellm` with exactly `type`,
`base_url`, and `api_key`. AI Core resource group is explicit rather than inherited.
URLs cannot embed credentials, query strings or fragments. Startup YAML and the
existing authenticated platform-settings provisioning use the same named instance,
not a rewritten `local-litellm` alias. Secrets are staged only inside the fresh
owned candidate's private configuration; the startup receipt contains identity and
configuration-presence metadata, not credential values.

`--verify-bot-agent` requires a fresh invocation with only an absolute private
version 1 `--config`; `--state`, legacy modes and alternate provider/model aliases
are rejected. **It currently fails closed even with valid configuration** because
the complete agent browser journey and runtime parity receipts are not implemented.
It starts no candidate and makes no model call. A staging receipt explicitly says
`runtime_parity_verified: false`: configuration equality is not observed parity
between Bot Home model-status, worker resolver input and actual adapter metadata.
Those production-path observations, three fresh title-only Stock Market News runs,
document/comparison tasks, browser/PTY, scheduler/feedback/restart, and guarded
PostgreSQL continuity remain required before full employee acceptance. Local Python
safety tests are preparation evidence only; no readiness or live-provider claim is
made by this configuration support.

## Native adapter provenance: payload-free worker observations

The native browser cannot derive concrete adapter identity from Bot Home model
status, `BotRun.Provider`/`Model`, aggregate usage, or transcript text. The concrete
boundary is `pkg/daemon/bot_worker.go`, `BotWorker.loadScope`'s `ResolveProvider`
closure: its callback returns the value constructed by
`pkg/daemon/bot_production.go`, `newProductionBotWorker`'s `ResolveModel` closure,
which currently returns `provider.GetProvider(ctx, name, modelName, local)` directly.
The preceding `store.ResolveBotModelSelection` uses the owner-scoped Home pin and
settings. `pkg/provider/streaming.Progress` contains only phases, liveness and
recovery timing, not adapter/model/request identity. Cache diagnostics describe
canonical ADK requests, not concrete factory-return types or wire identity, and
are not a native Bot worker provenance producer.

**Production boundary:** the browser/report/contract files alone cannot capture
that object in another process. A second factory invocation in the browser or a
separate Go diagnostic is not evidence about the native worker. The existing
`stockAgentConfig.observeAdapter` test observer is explicitly diagnostic-only.

The production-native implementation is payload-free worker telemetry, with no
new endpoint, configuration, schema or provider call:

- `BotWorker.loadScope.ResolveProvider` retains the exact successful resolver
  return and wraps it with `observeBotResolvedModel` before the existing budget
  wrapper. The mandate guard, scopes, resolver errors, provider instance and
  limits are unchanged. Nil and typed-nil models emit no successful identity.
- `pkg/daemon/bot_model_observation.go` emits bounded identity-only JSON prefixed
  `[bot-model-observation-v1]` through the existing daemon logger. Resolution
  records include worker/PID, fresh adapter UUID, Bot/run/session/owner/scope,
  resolver provider/model, the actual object's concrete Go type and `Name()`.
  No object/config serialization, prompts, responses, raw errors, credentials,
  authorization tokens or hidden thought parts are emitted. Overlong/control
  metadata becomes an explicit invalid-metadata marker rather than an empty
  request-model observation. The observer never rejects a production adapter.
- The transparent iterator forwards the exact context, request, streaming flag,
  responses/errors and consumer stop signal. Generation start/end records carry
  the same identity, fresh call UUID/ordinal, request model (honestly empty when
  omitted), response count and complete/error/cancelled/consumer-stopped outcome.
  An interrupted iterator is not complete evidence. Random-ID/logging failures
  do not gate execution. There are no extra model calls or background goroutines.
- `agent_live.go` captures the fresh owned candidate's `logs/daemon.log` through
  `agent_adapter.go`, using a closed typed mirror into a private hashed manifest.
  Symlink/missing logs, malformed/unknown fields, incomplete reads, missing calls
  or rotated-away records fail acceptance; raw daemon logs are never published.
  The source starts at this browser invocation, spans the owned learning restart,
  and is bound to candidate/browser hashes and nonce. Standard logger routing to
  `daemon.log` is exercised with `TestBotModelObservationPrivateLoggerRoute`.
- `agent_live_contract.py` invokes `agent_adapter_contract.py` independently of
  source grounding. It validates all records against retained title, general and
  learning runs, exact Bot/run/session identities and run timestamps, consistent
  PID/worker/adapter/call identities, paired starts/ends and contiguous ordinals.
  Resolver/Home parity alone is insufficient: actual `*sap.Provider` and actual
  model name must match the target, and nonempty substituted request models fail.
  Missing/foreign/duplicate/factory-only evidence cannot clear adapter provenance.

These are **native-worker-adapter** observations with **wire_verified: false**.
They prove invocation of the observed in-process object, not authenticated upstream
wire/model identity or successful factual work. An error/cancellation observation
is still an observation, not a successful task. Ownership/process provenance relies
on the existing fresh-candidate launcher verification and private owned log path,
not signatures in these records. Full source grounding remains a separate gate.

Focused zero-inference fixtures cover actual-instance identity, nil/typed nil,
stream/nonstream transparency, early stop/error/cancellation, concurrent run/call
isolation, logger failure, private log routing, owned-log extraction and rejecting
wrong concrete adapters, changed models, calls, runs, candidate hashes/nonces and
payload-bearing fields. No live candidate has yet been rebuilt or exercised with
this hook; earlier candidates cannot retroactively supply this telemetry.

## Verification and limits

Controlled production-factory tests use the real node HTTP implementation and
saved YAML flow engine with an HTTP child. `TestBotMCPNodeRemote` covers real local
SSE/streamableHTTP initialize/list/call exchanges, uncached discovery, preserved
schemas, symbolic credentials and redaction; `TestBotMCPNodeStdio` uses a controlled
subprocess. `TestBotMCPNodeCachedAndDisabled` and `TestBotMCPNodeRequiresOwner`
cover no eager cached startup, disabled servers and missing owner scope. Node
transport tests cover origin/header rejection; shared HTTP tests cover OAuth
refresh, redirects, private-address dialing and cancellation.

`TestBotMCPSecurityPreparationHasNoEffects` and
`TestBotMCPSecurityDiscoveryFences` cover no-permission/review-required preparation,
revocation, exhausted budget, changed target and exact-once approved discovery.
`TestBotMCPTargetFingerprintSymbolicConfig` and
`TestBotMCPTargetDescriptorChanges` cover immutable symbolic target snapshots,
command/URL/transport/argument/credential-reference changes and model target
spoofing. API `TestBotMCPApprovalCheckpointTargetBinding` round-trips persisted
approval state: a changed target produces zero effects and no consumption; the
same target executes once and rejects replay.

`TestBotBasicPlanLifecycle`, `TestBotBasicPlanVerifierCannotBypassPolicy`,
`TestBotBasicMemoryFactorySQLite`, `TestBotBasicMemoryClassification` and
`TestBotBasicDelegateResultIsolation` exercise the wired basic tools, scoped
memory and guarded verification/result isolation. Trusted outcome extraction is
integrated with ChatRunner and the named runtime journey; a tool result is not
itself independently verified evidence (see [runtime evidence](bots.md#trusted-runtime-evidence)).

The named `TestBotRuntimeHTTPAcceptance` passed with two scheduled runs/sessions,
approval reconstruction, four distinct fake physical workspaces, changed/unchanged
observations and one controlled threshold notification. The main thread reports
that the full six-package suite passed after the latest edits; the final full
Phase 3 check remains pending.

These are controlled backend/node transport tests, not deployed Docker/K8s,
real CDP navigation, arbitrary external MCP compatibility or provider-delivery
proof. Model/HTTP fixtures do not establish all Chat paths. Physical durable-home
and full Studio/terminal acceptance remain separate; final Phase 3 verification
is pending. See [runtime and receipt boundaries](bots.md).
