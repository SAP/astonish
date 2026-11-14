# Constrained mandate reads

Durable source-bound task admission is separate from ephemeral interactive turn authorization. API admission opts into `Services.BotMandatePolicy` only for verified platform JWT or local-loopback authentication. OAuth turn authority is not persistent authority. A title does not grant schedule purpose.

A worker must reconstruct `bot.MandateExecution` from the persisted run reference and trusted current policy. Both the API Chat adapter and launcher validate the sealed principal, exact reference and run identity. A persisted interactive flag does not require an interactive registry when a mandate was verified, and a missing mandate cannot fall back to an interactive caller ceiling. The factory still requires live isolation, budget, run lease and action guards.

`BotMandatedReadTools` builds concrete `web_fetch` and GET/HEAD `http_request` adapters. Only these adapter instances receive mechanically verified read classification. Native tools with the same names, shell and browser tools remain unknown effects. Saved tool resources filter the catalogue before progressive indexing; bound dispatch independently checks the saved resource. Discovery cannot recover a filtered raw adapter.

`BotRoutineReadPolicy` reloads platform, organization and team network rules per request; deny wins. The public-read resource permits ordinary public web retrieval without hardcoded sites. Private access additionally requires a saved exact origin plus a current trusted allow rule. DNS/IP checks happen at dial time, every redirect is reauthorized, and HTTP proxies are disabled. Credentials, custom headers, URL userinfo, credential placeholders, request bodies and uploads are forbidden. Retrieval is time/size bounded. Web-fetch extraction modes, truncation, final URL and untrusted-content warnings remain available.

Composition must supply all applicable network-policy tiers and a live membership resolver. Reconstruct the finite ceiling inside the current-policy lookup rather than capturing mutable operator settings as startup authority. `NewBotRoutineReadMandatePolicy` alone is not membership authentication. The legacy `BotMandatedConfiguredSearchTool` remains protocol-limited and does not widen historical grants.

## Configured search-first public research

New owner-backed public research carries a versioned `BotPublicResearchScope`: an admitted public subject/source event, exact configured route fingerprint and symbolic credential handles, typed facets, and finite query/result/call limits. Historical or refreshed mandates never acquire this authority implicitly. `BotConfiguredResearchTool` uses the effective scoped Chat Perplexity provider/model via `RunPerplexityWebSearch` with confined provider secrets and shared model accounting, rather than translating SAP or Sonar configuration into an unrelated fixed HTTP transport. Tavily uses only the selected cached concrete sandbox MCP executable with a private authorization handoff; sibling MCP tools remain unknown effects requiring approval.

The run/session-bound `BotResearchEpisode` blocks external detail retrieval until the exact search returns source URLs. Empty or failed searches do not unlock retrieval, and cancellation prevents late unlock. Explicit admitted URL and supplied-material modes do not force external discovery. The logical executable guard follows progressive/delegated tool dispatch. Search snippets remain discovery/background evidence, not verification of actionable facts.

Search call reservations are durable in scoped mandate usage and survive closing all SQLite connections and rebuilding the runtime. Reopening preserves owner admission provenance and the source-bearing search/detail response journals, but neither persisted prose nor an old receipt unlocks a new episode. Run/session and dispatch-generation checks precede cancellation mutation, so a cancelled child, foreign run, or stale callback cannot interrupt its parent's current research. `TestBotPublicResearchRestartQuota` proves exact remaining ceilings, immutable disclosure provenance and released-run no-replay in both personal and team stores; `TestBotConfiguredSearchHTTPAcceptance/*/restart-receipts` proves retained ordered response bytes after a full backend close/reopen. The stage requires those exact leaves together with `TestBotSearchFirstRestart`.

`python3 -B tests/e2eboot/bot_task_acceptance.py --stage search` builds this checkout into a temporary Linux node candidate and requires exact fresh HTTP/physical-MCP evidence with no skipped tests. It uses owned SQLite, controlled models and a network-disabled disposable sandbox, never normal user resources or paid provider calls. This scoped stage is not full live-model/continuity certification.

## Coverage-guided query limitation

The current version-1 disclosure scope authorizes only `Subject` plus the four typed facet suffixes rendered by `BotPublicResearchScope.Query`. `botPublicResearchArgs` has only `facet`; its decoder rejects unknown fields before quota reservation or provider dispatch. Employee `missing_dimensions`, `source_needs` and `public_queries` are advisory and cannot currently become configured search arguments. Deliverable-planning prompt guidance does not remove this execution limitation.

`TestBotConfiguredSearchCoverageGuidanceAdmissionBlocker` exercises the configured executable and records actual Perplexity request contents: public dimension/query/entity fields, private query text and foreign subjects are rejected without dispatch; accepted overview/recent calls still send only generic subject/facet queries. This is a known capability gap, not coverage certification.

A free-text dimension or query suffix is not safely authorized merely by prepending the admitted subject: it can include private portfolio data, credentials or unrelated subjects. A byte ceiling, character filter, or a model's assertion that text is public cannot establish public provenance. Resolving the gap without a new classifier requires an explicit owner-backed typed disclosure extension (for example, exact public coverage terms/query values with source-event provenance and current-policy checks), or a mechanically public input source with retained provenance. Such an extension must be separately admitted and must not silently broaden historical version-1 grants. Current quotas, credential confinement, exact route binding and search-before-detail enforcement remain unchanged.

## Scoped discovery in Home turns

The launcher composes `search_tools` and `describe_tools` as sealed metadata-only
capabilities for mandate-backed and ephemeral interactive Bot turns. Native
callbacks recognize the concrete wrapper, not the tool name. Helpers operate on
the already filtered catalog under captured owner context, with live lease,
isolation, cancellation, budget and mandate/caller checks on every invocation.
Interactive callers still need `tool:execute`; declaration alone grants nothing.
Discovery neither consumes an external-action grant/quota nor adds helper names
to `allowed_tools` or mandate resources. Model usage remains metered normally.

`execute_tool` is not a metadata capability: it resolves the original bound tool
and retains exact-resource, policy, approval and budget enforcement. Raw tools
named like discovery helpers acquire no exemption. Non-mandated autonomous runs
retain their original policy path. No Studio/TUI event or rendering contract
changes.

`TestBotMandateDiscoveryNativeContract` reproduces the schedule-turn discovery
sequence through the real factory and ChatRunner with a scripted model: search
for clarification, describe it, execute the permitted fixture capability, then
reject an excluded shell target. `TestBotDiscoveryLiveBinding` covers both
interactive caller and mandate revocation, cancellation, lease fencing and disabled
helpers; `TestBotDiscoveryIdentityNotName` defends wrapper identity and dispatch
registration. These are local regression checks, not the employee live gate or
proof that a natural schedule was created.

## Transaction-scoped current policy

Team Bot admission and mandate verification pass a private transaction context to the policy resolver. The team network-policy List adapter substitutes the transaction client only when the captured outer client matches exactly. Title admission, owner messages, operation enqueue/apply, exception decisions and scheduled-task verification therefore read current team policy on the same SQL transaction as their state changes, without taking a second SQLite connection. Personal admission continues reading team policy from its separate database. Platform/organization membership and policy lookups remain live; no preflight policy result is cached or substituted for commit-time revalidation. Pool limits and SQLite pragmas are unchanged.

`TestBotPolicyTransactionRevalidatesAfterPreflight` changes persisted allow to deny after authorization preflight and verifies that enqueue rejects without activity/revision, operation or schedule writes. `TestBotPolicyTransactionAdmission` covers title and message admission in both scopes. The production-resolved `TestBotAuthorizedScheduleHTTPAcceptance` retains actual scheduling-tool execution and recurring execution.

## Evidence and limits

`TestBotMandateRuntimeHTTPAcceptance` exercises actual HTTP title admission, persisted mandate verification with the interactive registry closed, local-server web retrieval and HEAD, rejected POST/invalid extraction, denied schedule purpose, and live policy revocation. `TestBotChatFactoryMandateWithoutTurnRegistry` constructs the actual factory through the API runtime adapter with a sealed test verifier, proves catalogue filtering, rejects missing/mixed authority, and rechecks revocation.

These tests do not prove a full process/database restart or the daemon/model/lease/quota journey. The HTTP acceptance reconstructs the verifier against an existing SQLite connection. Production daemon composition and a configured search adapter require separate acceptance evidence.
