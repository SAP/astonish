# Bot production capability adapters

Bot runs use Chat's existing implementations, but catalogue tools are wrapped by
`botBoundTool` before dispatch: owner scope, live lease/revocation, exact action
policy and the shared run budget precede adapter execution. There is no host
fallback when an adapter is unavailable.

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
