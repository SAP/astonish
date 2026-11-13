# Bot production capability adapters

Bot runs use Chat's existing implementations, but catalogue tools are wrapped by
`botBoundTool` before dispatch: owner scope, live lease/revocation, exact action
policy and the shared run budget precede adapter execution. There is no host
fallback when an adapter is unavailable.

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

## Memory, plans and delegated results

`BotMemoryTools` preserves Chat's memory argument/result schemas while binding
`memory_save`, `memory_search`, `memory_get` and `memory_delete` to
`NewBotRunMemoryService`. Reads prefilter to this Bot's linked home/run sessions;
mutations require the current run lease. Saves carry run attribution, redact
credential values and may overwrite a matching category only in the current run.
Deletes cannot remove prior-run evidence; verified attributed content cannot be
silently rewritten. Generic owner-wide memory and three-tier search context slots
are shadowed, with no personal/team fallback.

`newBotPlanTools` provides `announce_plan`, `update_plan` and
`announce_completion` using Chat's plan lifecycle. Session state is authoritative
across attempts; `PLAN.md` is projected through the guarded sandbox `write_file`.
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
