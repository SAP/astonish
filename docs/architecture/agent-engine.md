# Agent Execution Engine

## Overview

The agent execution engine is the core of Astonish -- it processes user messages, orchestrates LLM calls, manages tool execution, and produces responses. There are two distinct agent types serving different use cases:

- **ChatAgent**: Open-ended conversational agent. No predefined flow. The LLM decides which tools to call and how to proceed. This is the primary mode used in Studio, console, and external channels.
- **AstonishAgent**: Flow-based agent that follows YAML-defined node graphs with LLM nodes, tool nodes, and conditional branching. Used for deterministic, repeatable workflows.

Both agents are built on top of **Google's Agent Development Kit (ADK)**, specifically wrapping ADK's `llmagent` for LLM interaction and `runner` for session-aware execution.

## Key Design Decisions

### Why Wrap ADK Instead of Using It Directly

ADK provides a solid foundation (session management, tool dispatch, streaming) but Astonish needs capabilities that ADK doesn't offer:

- **Cache-stable execution**: Selected sessions freeze the system prompt and model-visible tool declarations while discovering deferred capabilities through a fixed bridge.
- **Credential security**: Before/after tool callbacks must substitute and restore credential placeholders without leaking secrets into session history.
- **Context compaction**: When the context window fills up, the system must compress history without losing critical information.
- **Auto-knowledge retrieval**: Every native non-Code turn retrieves automatic memory once and attaches it only to that invocation's outgoing requests.
- **Execution tracing**: Every tool call is recorded for flow distillation and memory reflection.
- **Think-tag filtering**: Chain-of-thought content (`<think>` blocks) must be stripped from streaming output.

ADK's callback system (BeforeToolCallbacks, AfterToolCallbacks, BeforeModelCallbacks) provides the extension points, but the coordination between them is Astonish-specific.

### Why a Three-Tier System Prompt

The system prompt uses a deliberate tiered architecture to balance token efficiency with comprehensive guidance:

- **Tier 1 (Session Snapshot)**: Identity, behavior rules, environment information, capability summaries, and other static inputs. In the cache-stable path, the exact built prompt is persisted in session state and reused after resume. Studio and Code both include a short always-on **Live Evidence** block here (`LiveEvidenceSection`): claim-done needs this turn's tool output; running surfaces need a live observation or a drill; CloakBrowser is not `chromium` on PATH. This is not a substitute for Tier 2 how-tos — it exists because vector retrieval does not fire on a `which chromium` probe. Keep it short; do not copy Grok Build's full work-policy essay into Studio.
- **Tier 2 (Indexed Guidance)**: Detailed how-to documentation for each capability (browser, credentials, scheduling, etc.). Stored as `memory/guidance/*.md` files indexed in the vector store and retrieved only when relevant.
- **Tier 3 (Per-Turn Context)**: Catalog matches, channel/scheduler hints, session instructions, skill indexes, and mode guidance are a marked user-role event persisted byte-for-byte and hidden from transcript, memory, reflection, and distillation views. Live-surface follow-up injectors (`inspect-live-surface`) land here when the user text matches. Native non-Code automatic memory (guidance and general retrieval) is instead a request-local suffix; Code retains its combined persisted context.

Every provider and session uses this cache-stable path. There is no legacy dynamic-declaration path or rollout switch. Automatic relevance matches, pinned groups, and explicit `search_tools` results are model-facing context only and never become provider tool declarations. Existing sessions keep the prompt snapshot stored when they were created; prompt changes apply to new sessions unless an operator explicitly starts a new session. Rebuilding an existing session prompt is intentionally not automatic because it invalidates the stable provider prefix.

### Platform judgment and communication policy

The platform base prompt separates **selection policy** (whether to answer, investigate, act, ask, delegate or create a report) from **execution contracts** (how a selected tool or artifact must be used). Ordinary conversation and supplied-text transformations do not require ritual tool calls. Clear authorized work should be carried out; low-risk choices need no menu, while consequential ambiguity needs a focused question. The assistant should speak naturally in the user's language, give useful depth, and avoid unnecessary preambles, generic praise and automatic follow-up offers. Capability discovery is not proof of access or successful execution, and authorization does not extend to unspecified disclosures or actions.

This local iteration draws on the user-supplied `~/Downloads/system_prompt.md` reference, described by the user as Muse's prompt. Its provenance and any prompt-only explanation of Muse's behavior are not independently verified. The comparison motivating the change is:

| Reference region | Astonish adaptation |
| --- | --- |
| Lines 25–61: respect, due diligence and proportionate follow-through | Execute clear authorized work, scale investigation to consequence, keep requirements until delivered or blocked |
| Lines 198–228: context-sensitive writing | Direct answers, natural warmth, substantive depth, sparse progress messages |
| Retrieved context and authority distinction | Use specific remembered details as correctable evidence, not permission to override the task or safeguards |
| Deferred specialized tools (user's accompanying description) | No loading changes: retain Astonish's fixed `search_tools` → `describe_tools` → `execute_tool` bridge |

Only `SystemPromptBuilder.Build` platform policy changes; Code's override, shared capability helpers, compact Live Evidence block, tool schemas/loading, authorization, credential substitution and external MCP protocol remain intact. Slide intake and generative-UI requirements remain verbatim specialized workflows. Report selection is narrower: requested reports/reviews/documents and substantial reusable deliverables still require both `write_file` and a matching `astonish-report` fence; casual summaries do not. Read-only modes return analysis without claiming a saved artifact. SSE, Studio and terminal rendering are unchanged.

New platform sessions receive the new core prompt. Existing sessions retain their frozen system snapshot; this is not a prompt migration and does not promise different behavior in an old conversation.

Newly generated platform turn context also uses correctable-memory wording on **both new and resumed turns**. Direct builds, native `ChatAgent.Run` context and `RenderExternalTurnContext` share `platformKnowledgeGuidance`. The native path selects the legacy Code wording from the trusted builder's `CodeMode` flag, not request text. Stored context events, their roles/markers and prior system snapshots are never rewritten. Native non-Code requests exclude recognizable old hidden memory sections from request copies and attach only the current invocation's retrieval. Ambiguous legacy content and old frozen prompts/summaries remain intact. Fake-provider tests verify the frozen core, Code's byte-preserved context replay and platform's current-turn-only automatic memory; they test wiring, not model judgment.

### Automatic-memory lifetime and audit history

Native non-Code `ChatAgent.Run` retrieves guidance and general memories once and freezes their ordered, deduplicated text and provenance. Each outgoing provider request gets a fresh request copy with this automatic-memory suffix at the absolute end, including tool follow-ups, transient retries and overflow retries within that invocation. An invocation with no hits gets no suffix. The next `Run`, including an approval response, starts a new retrieval lifetime: no previous suffix is replayed, whether compaction ran or never ran.

The frozen system prompt and non-memory hidden turn context remain durable. Explicit memory-tool outputs remain ordinary tool history; this change affects automatic retrieval only. Code's combined persisted context and public external MCP `RenderExternalTurnContext` behavior are unchanged because external clients own their history. No memory corpus, source session, prior answer or preexisting summary is rewritten, and facts already repeated there are not guaranteed to disappear.

Legacy projection uses hidden-event provenance and exact content matching, removing only a recognizable terminal `## Knowledge For This Task` section from outgoing copies while preserving preceding context. Ambiguous boundaries, user/tool lookalikes and uncertain provenance are preserved instead of deleting potentially genuine instructions. Persisted session objects are not mutated.

The same frozen snapshot supplies request injection and content-less `_knowledge_injection` audit records with explicit invocation identity. Preparation records have `dispatched=false`; the first provider dispatch produces a replacement with `dispatched=true`, drained on the owning Run goroutine. Authorized Debug capture records the formatted suffix and ordered snippets; capture disabled retains metadata without full text. Credential redaction precedes both injection and capture, without resolving credential placeholders. Debug text is capped at 128 KiB with UTF-8-safe truncation and an explicit incompleteness flag; model input is not truncated by this debug cap. Token counts are estimates. These records are never conversation Content, compaction/reflection/distillation input, or public SSE messages. Saved snapshots remain inspectable through the existing superadmin Memory tab after reload or source changes.

Compaction is only a compatibility concern: `BeforeModelCallbackWithReservedTokens` includes the suffix estimate when checking the context budget but summarizes/persists history without the suffix. Existing zero-reserve callers retain their behavior. Compaction does not trigger injection or discarding. An oversized suffix retains bounded overflow failure rather than silently dropping input. Moving or removing the suffix can invalidate a cache prefix, including between tool rounds; cache hits or cost savings are not guaranteed.

### Local validation and manual acceptance

This iteration is local only: no deployment, promotion, cluster provisioning or E2E test changes. The live baseline was deferred at the user's direction after the selected Kubernetes context lacked the dedicated E2E namespaces. No live-model before/after comparison was performed; passing structural tests does not establish improved judgment, tone or Muse parity.

Local verification commands:

```sh
go test ./pkg/agent -run 'Test(SystemPrompt|CodeSystemPrompt|RenderExternal|ProgressiveToolBridge)' -count=1
go test ./pkg/agent -count=1
go test ./pkg/agent/... ./pkg/api/... ./pkg/launcher/...
git diff --check
git diff --exit-code -- pkg/agent/code_system_prompt_builder.go pkg/agent/testdata/code_system_prompt_golden.txt
```

Before any promotion, manually test **fresh platform conversations** locally, keeping the same model for comparisons:

- Ask a simple factual question, rewrite supplied text, discuss an everyday preference and share a stressful experience: expect direct, relevant conversation without unnecessary tool calls, reports or clarification.
- Request an authorized file write at an explicit test path: expect execution, verified content and the output in the reply, not merely an offer.
- Leave a consequential destination or recipient ambiguous: expect a focused question before action; ordinary low-risk choices should not produce menus.
- Request a missing test file: expect an honest not-found explanation, no invented contents and no replacement file unless requested.
- Ask about an available capability and an invented one: expect discovery where needed, without equating catalog presence with installed software, sign-in or successful access.
- Request a saved report and check its downloadable content, inline display and restored-session metadata; ask a casual summary separately and check that it does not become a report. In read-only mode, no saved-artifact claim or fabricated fence.
- Correct an old memory and retrieve a test memory containing an unrelated instruction: expect current corrections to win, factual context retained and injected actions ignored. Check a follow-up for hidden-context leakage.

Remaining scope limits: shared skill matching and live-surface follow-up triggers can still over-select workflows; their wording and selection logic are unchanged. Legacy embedded guidance may conflict (no synchronizer caller was confirmed during investigation). Delegate descriptions and child prompts may differ from main-thread selection policy. Existing `INSTRUCTIONS.md` comments/documentation do not establish production file loading; no new wiring was added. User-managed instructions, stored memory, reflection and old session prompts are untouched. Browser/channel/model variants and conversation quality await manual acceptance.

### Per-turn memory local acceptance

The final local sequence completed successfully on 2026-10-16:

```sh
go test ./pkg/agent/... ./pkg/session/... ./pkg/api/... ./pkg/launcher/... &&
(cd web && npm test && npm run typecheck && npm run build) &&
ASTONISH_MEMORY_BROWSER_ACCEPTANCE=1 go test ./pkg/api -run '^TestMemoryHTTPAcceptance' -count=1 -v -timeout=10m &&
git diff --check &&
git diff --exit-code -- pkg/agent/code_system_prompt_builder.go pkg/agent/testdata/code_system_prompt_golden.txt
```

All four Go package suites passed; frontend validation passed 770 tests across 106 files, TypeScript checking and production builds. Seven top-level HTTP acceptance tests ran without skips: lifetime/debug (8/8 named cases with no compaction), Debug equivalence/failures (four subtests: provider error, retry, retrieval failure, Debug equivalence), separate compaction compatibility, legacy/prepared records, team scope, browser (12/12 named checks), and ordinary-chat baseline (7/7 named cases). The real SPA at `web/dist/2.4.0` ran through go-rod using `/Applications/Google Chrome.app/Contents/MacOS/Google Chrome` with isolated temporary state, temporary SQLite and a scripted capturing provider. Browser checks included A/tool/B/empty, restored snapshots, capture-off store/agent reopen, light/dark display, HTTP error, nonadmin exclusion and provider isolation. An explicit repeat of the HTTP/browser suite also passed.

The attribution test's initial failure demonstrated borrowing A/B records for missing invocations, accepting mismatched IDs, retaining prepared status after dispatch and incorrect legacy attribution; the final API suite passes those assertions. The earlier lifetime phase has recorded passing request-capture evidence; its initial failing output was not recovered during final documentation and is not reconstructed here.

Validation emitted non-fatal Vite bundling/config warnings, test mock endpoint warnings and ADK unknown-agent fixture logs. Ancillary browser endpoints such as brand settings and sandbox status are fixture-only/unhandled; these are not evidence of those production capabilities. No deployment, live-model quality comparison, provider cache measurement or remote-terminal interactive run was performed. Terminal non-leakage is covered at the shared SSE/history boundary. The unchanged Code builder/golden guard passed; earlier platform prompt work and unrelated local files were left intact.

### Go integration contract

The cache-stable tool bridge changes exported integration points. `ToolVectorStore` implementations must provide `AllIDs(context.Context)` so initialization can verify exact semantic-index membership. Request-scoped MCP lookup returns an error rather than a boolean so lookup failures cannot be mistaken for an absent tool. Search-tool construction also propagates errors. Integrators must update implementations and callers; compatibility fallbacks are intentionally absent.

### Why Sequential Tool Dispatch

ADK processes tool calls sequentially within a single invocation (a for-loop, not goroutines). This simplifies credential substitution: a shared `var credentialRestore func()` variable between the before and after callbacks is safe without synchronization. If ADK ever moves to concurrent tool dispatch, the credential flow would need per-call scoping.

### Why Hybrid Tool Discovery

Most agent frameworks require all tools to be declared upfront. With 60+ built-in tools plus MCP tools, this wastes context window tokens listing tools the user doesn't need. Instead, Astonish uses a two-layer approach:

- **Static tools**: A small core set (file ops, shell, memory) remains directly available.
- **Fixed progressive bridge**: `search_tools` searches only the catalog, `describe_tools` returns selected schemas, and `execute_tool` invokes deferred tools. The model-visible declaration set does not grow during a turn.

## Architecture

### ChatAgent Execution Flow

```
User Message
    |
    v
1. Secret Extraction: PendingVault.Extract() replaces <<<secret>>> with <<<SECRET_N>>> tokens
    |
    v
2. Knowledge Retrieval: Two partitioned vector searches
   - Guidance docs (max 3, min score 0.3) -- how-to instructions
   - General knowledge (max 5, min score 0.3) -- memory, skills, flows
   Native non-Code: freeze request-local automatic memory once for this invocation.
   Code: retain combined persisted turn context.
   Content-less `_knowledge_injection` audit events record query, provenance,
   estimates, invocation identity and dispatch status; authorized Debug also
   captures sanitized text independently of conversation Content.
    |
    v
3. Tool Discovery: Hybrid search on ToolIndex
   - Vector similarity + BM25 keyword matching (RRF fusion)
   - Top 8 matches formatted as catalog hints; declarations remain fixed
   - Knowledge and tool retrieval share the explicit `chat.pre_provider_retrieval_timeout_seconds` deadline (default 10 seconds)
   - Any retrieval error or timeout ends the turn before provider invocation; semantic failures never fall back to lexical-only results
   - A configured semantic catalog must synchronize and pass full document-identity validation during agent initialization; failure aborts initialization rather than starting with degraded retrieval
   - Successful validation is cached for the published catalog generation, and synchronization blocks semantic searches until a complete generation is validated
    |
    v
4. Prompt and Turn Context
   - Reuse the persisted system prompt and persist non-memory context as a hidden user-role event
   - Native non-Code: project recognized legacy memory out of request history without mutating stored events
   - Code: preserve combined context replay
    |
    v
5. LLM Agent Creation: llmagent.New() with callbacks
   - BeforeToolCallbacks: credential substitution, secret token resolution
   - AfterToolCallbacks: credential restoration, redaction, trace recording, image stripping
   - BeforeModelCallbacks: non-progressing discovery circuit breaker, tool response truncation, and context compaction
    |
    v
6. Execution Loop (with retry):
   - llmAgent.Run() produces streaming events
   - Every native non-Code provider call appends the invocation's frozen memory suffix to a request copy
   - Tool follow-ups and retries reuse that suffix; a new invocation never replays it automatically
   - Retryable errors (429, 502, 503) -> exponential backoff, retry up to 3x
   - Deferred tools are called through `execute_tool`; unknown direct names keep ADK's default error
   - Discovery circuit breaker: before another provider dispatch, stop after 2 identical `search_tools` / `describe_tools` / `skill_lookup` calls or 12 total discovery calls in the current invocation; direct and `execute_tool`-wrapped calls share the budget, while prior turns form the baseline and are not charged again
   - Approval pause -> yield event and return, resume on next user message
    |
    v
7. Post-Task Processing:
   - Memory reflection: silent LLM call to identify durable knowledge worth saving
   - Trace storage: execution trace saved for on-demand /distill
```

### AstonishAgent Execution Flow

The AstonishAgent follows a YAML-defined node graph:

```
YAML Flow Definition
    |
    v
Parse nodes: START -> node_1 -> node_2 -> ... -> END
    |
    v
For each node:
  - LLM node: executeLLMNode() with intelligent retry
  - Tool node: executeToolNode() with direct tool invocation
  - Condition: evaluateCondition() to choose next node
    |
    v
State machine: each node reads/writes session state
    |
    v
Approval gates: if tool is protected, pause for user approval
```

LLM nodes within flows use the same callback architecture as ChatAgent (credential substitution, redaction, tracing) but with flow-specific error recovery: an `ErrorRecoveryNode` uses a separate LLM call to analyze failures and decide whether to retry with a modified strategy or abort.

### Tool Callback Architecture

The callback chain runs for every tool call:

```
LLM requests tool call
    |
    v
BeforeToolCallback #1: Credential Substitution
  - Scans args for {{CREDENTIAL:name:field}} placeholders
  - Replaces with real values in-place (same map ADK uses)
  - Stores restore function in shared variable
    |
    v
BeforeToolCallback #2: Secret Token Resolution
  - Scans args for <<<SECRET_N>>> tokens
  - Replaces with real values from PendingVault
  - Stores restore function
    |
    v
Tool Executes (with real credential values)
    |
    v
AfterToolCallback:
  1. Restore credential placeholders (undoes in-place substitution)
  2. Restore secret tokens (undoes in-place substitution)
  3. Redact any credential values from tool output
  4. Strip image_base64 from output (stash for channel delivery)
  5. Strip large flow output (stash for direct delivery)
  6. Record step in execution trace
  7. After save_credential: retroactively redact session transcript
```

The critical invariant: the session event (which shares the same args map by reference due to an ADK design choice) always retains placeholder tokens, never real secrets.

### Fixed Progressive Tool Bridge

When the cache-stable path is enabled, ChatAgent exposes a fixed declaration set for the session. `search_tools` is catalog-only and never mutates the request. The model calls `describe_tools(names)` to retrieve deferred schemas, then `execute_tool(name, arguments)` to invoke one. Resolution prefers first-party ToolIndex entries, rejects ambiguous bare request-scoped names, accepts qualified `group/tool` references, and enforces disabled-tool and effective team → org → platform MCP access rules.

ADK dispatches `execute_tool` through the normal callback chain. ChatAgent unwraps the selected tool identity and nested arguments inside every BeforeTool callback and the AfterTool callback, preserving mode and authorization gates, credential and pending-secret substitution/restoration, output redaction, execution tracing, image handling, and artifact capture. Request-scoped MCP and A2A catalogs are merged rather than replacing one another. Historical direct tool calls remain executable from transcript history, but no discovery result adds a new declaration.

Before every model call, Astonish records secret-safe hashes of the system instruction and canonical ordered declarations, plus declaration count and whether either hash changed within the turn or session. These diagnostics expose cache instability without logging prompt or schema contents.

The semantic catalog is a required startup dependency when embeddings are configured. Schema migration, embedding initialization, and tool-vector-store initialization fail closed instead of advertising readiness with degraded retrieval. Background catalog refresh remains asynchronous after the lexical catalog is published, but request-time semantic embedding failures are returned explicitly and are never retried or converted into BM25-only results.

### Sub-Agent System

The `SubAgentManager` enables the ChatAgent to delegate work to specialized child agents via the `delegate_tasks` tool:

- **Concurrent execution**: Multiple sub-agents run in parallel, each with its own session, tool set, and optional model override.
- **Tool groups**: Tools are organized into named groups (core, browser, mcp:*). Explicit tool/group filters and override instances are hard boundaries: each child freezes an immutable executable catalog, and its search/describe/execute helpers share that catalog. A qualified single MCP tool never grants its whole server. The web group adds only the configured request-local search tool. Wrapped sandbox instances are preserved; helper and requested declarations are deduplicated.
- **Depth limiting**: Default max depth of 2 prevents infinite delegation chains.
- **Container sharing**: Sub-agent sessions are aliased to the parent's sandbox container via `NodeClientPool.Alias()`.
- **Event forwarding**: When `UIEventCallback` is set, sub-agent events (tool calls, text) are streamed to the UI in real-time.
- **Bounded liveness**: Every child has the existing absolute task timeout plus a shorter inactivity watchdog (two minutes by default). Meaningful activity is a new successful non-discovery tool result or non-thought text; tool errors, repeated identical results, discovery helpers, and hidden thought tokens never reset the watchdog. The watchdog cancels the child runner context, so a provider wait cannot hold a semaphore slot indefinitely.
- **Observable state**: Structured progress events distinguish `queued`, `running`, `waiting_on_model`, `retrying`, `complete`, and `failed`, with attempt, elapsed duration, last-activity age, and an explicit inactivity reason. Heartbeats report state but do not themselves count as progress.
- **Progress-gated retry**: The outer one-time retry (at most two attempts) requires successful non-discovery tool evidence and is suppressed after parent cancellation. Both continuation and restart carry a redacted checkpoint: last eight useful tool results, at most 1 KiB per excerpt and 12 KiB total, UTF-8-safe, with partial answer, blocker and next action. Arguments are omitted. Tool counts, durations and traces accumulate across attempts; inert/error/discovery-only children do not earn a retry. All cancellation paths release the concurrency semaphore.

Local scripted acceptance: `go test ./pkg/agent ./pkg/tools ./pkg/launcher -count=1` and `go test ./pkg/api -run '^TestChildDiscoveryHTTPAcceptance$' -count=1 -timeout=3m -v`. The latter sends a real loopback HTTP chat request through delegate_tasks and an isolated scripted child, observes scoped search/describe/execute and fixed declarations, and counts the allowed tool execution without paid model credentials. This scripted acceptance does not exercise paid provider inference, real remote MCP servers, or a live sandbox/container; wrapper identity and override boundaries are covered by local tests instead.

Each sub-agent gets its own system prompt built by `buildChildPrompt()` which includes the task instructions, available tool names, and a reminder that it's a focused worker with a specific mission.

### Authorization and plan lifecycle invariants

Code-mode authorization has one pending owner per session. A gate atomically claims that slot before emitting an approval; concurrent callbacks cannot replace it, and the user's decision atomically consumes it once. Sub-agent prompts are additionally serialized across their complete blocking prompt/response lifecycle, while a sub-agent decision resumes the existing parent event stream rather than creating a new parent turn.

Folder preflight is schema-aware: declared path arguments and parsed `shell_command` operands are checked, but arbitrary nested prose, URL strings, and glob patterns are not recursively reinterpreted as paths. Containment and symlink-escape checks remain centralized in `pkg/pathscope`.

`announce_plan` exists only in Plan mode (and in Graph-Optimized Plan, only in the PLAN phase). It is stripped from the model's tool list in Normal/Ask and refused if still called. An approved execution turn carries `ApprovedPlanExecution` independently of Normal/Plan mode — including subsequent Normal turns while the session lifecycle is `approved` and `PLAN.md` still exists. While that flag is set, the runtime rejects `announce_plan`, inlines `PLAN.md` into the turn's system context, and continues to allow `update_plan`. The active approved `PlanState` is sealed against racing replacement, and persistence callbacks from superseded plan versions cannot rewrite the current `PLAN.md`. There is no per-turn tool-call pause on the main agent (the previous 100-call stop is removed) and no per-child tool-call cap on `delegate_tasks` (the previous 25-call stop is removed); inactivity watchdog and absolute task timeout still bound sub-agents.

### Think-Tag Filtering

Some models (especially open-source ones) emit chain-of-thought in `<think>` or `<thinking>` blocks. The `thinkTagFilter` is a stateful streaming filter that:

- Tracks whether the current position is inside a think block.
- Buffers partial tag matches across streaming chunks (a single `<think>` tag may be split across multiple events).
- Strips all content between open and close tags.
- Returns only the non-think content to the user.

This must be stateful because regex doesn't work on streaming chunks where tags span multiple events.

### Context Compaction

When the conversation approaches 80% of the context window, the `Compactor` (from `pkg/session`) triggers:

1. A `BeforeModelCallback` checks token usage before each LLM call.
2. If over threshold, it invokes an LLM-based summarization of the conversation history.
3. The summary replaces the full history, preserving key facts and recent tool results.
4. Fallback: if summarization fails, truncation removes oldest messages.

### Memory Reflection

In platform mode, `PlatformReflector` in `pkg/agent/platform_reflector.go` runs a silent post-task LLM call after non-trivial turns:

1. Feeds the execution trace (tool calls, results, errors) to a specialized prompt.
2. The LLM decides whether durable knowledge was discovered (workarounds, non-obvious patterns, API quirks).
3. If yes, it calls `memory_save` to persist the knowledge.
4. The platform memory merger first tries to upsert that knowledge into a structured scenario card (`scenario_card/efficient_successful_path`) so future turns retrieve the efficient successful path rather than scattered raw notes.
5. This is the "insurance" layer -- the system prompt already instructs the LLM to save knowledge during execution, but the reflector catches anything it missed.

Scenario-card saves keep source IDs/session IDs as lineage in the card, then delete or discard the raw source memory rows. The invariant is to store the shortest reusable successful recipe, not a broad “this failed once, never use it” rule. If a raw memory cannot form a usable card, it is not kept as durable memory.

### Execution Tracing

Every user turn creates an `ExecutionTrace` that records:

- The user's request text
- Each tool call: name, args, result, success/failure, duration
- Sub-agent traces (nested, for `delegate_tasks`)
- The LLM's final text output

Traces serve two purposes:
1. **On-demand distillation**: The `/distill` command converts traces into reusable YAML flows.
2. **Memory reflection**: The reflector analyzes traces for knowledge worth persisting.

Traces are stored in-memory per session (max 20 per session) and can be reconstructed from persisted session events across daemon restarts.

### Provider Resolution Cascade

Provider and model selection follows a 5-tier cascade (later tiers override earlier):

1. **Platform** — global defaults from `PlatformSettings`
2. **Org** — org-level overrides from `OrgSettings`
3. **Team** — team-level overrides from `TeamSettings`
4. **User Default** — personal preference via `ApplyUserDefault(cfg, personalSettings)` (`pkg/provider/resolve.go`)
5. **Session/App Pin** — per-chat or per-app override via `ApplyProviderOverride(cfg, provider, model)` (`pkg/provider/resolve.go`)

Empty strings at any tier mean "inherit from the tier below." The base cascade (tiers 1-3) is computed by `ResolveEffectiveConfig`. Tiers 4-5 are applied as chained overlays at each call site.

Missing credentials for a pinned provider trigger a `slog.Warn` and fall back to the cascade result in-memory. The pin is never auto-cleared.

CLI flags `-p`/`-m` pin by default onto new sessions. Use `--no-pin` to restore ephemeral behavior, or `--clear-model` on a resumed session to remove an existing pin.

## Key Files

| File | Purpose |
|---|---|
| `pkg/agent/chat_agent.go` | ChatAgent struct definition, fields, image/flow output side-channels |
| `pkg/agent/chat_agent_run.go` | ChatAgent.Run() -- the main execution loop with all phases |
| `pkg/agent/astonish_agent.go` | AstonishAgent struct, flow state machine, approval handling |
| `pkg/agent/node_llm.go` | LLM node execution for flows, callback wiring, retry logic |
| `pkg/agent/sub_agent.go` | SubAgentManager, SubAgentTask, tool groups, concurrent delegation |
| `pkg/agent/system_prompt_builder.go` | Three-tier system prompt construction |
| `pkg/agent/tool_index.go` | Hybrid vector+BM25 tool discovery index |
| `pkg/agent/tool_categories.go` | Safe (read-only) vs protected (write/exec) tool classification |
| `pkg/agent/execution_trace.go` | Execution trace recording for distillation and reflection |
| `pkg/agent/chat_distill.go` | Trace reconstruction from session events, distill preview/confirm |
| `pkg/agent/flow_distiller.go` | LLM-powered trace-to-YAML flow conversion |
| `pkg/agent/platform_reflector.go` | Platform post-task knowledge extraction via silent LLM call |
| `pkg/agent/think_filter.go` | Streaming chain-of-thought tag stripping |
| `pkg/agent/error_recovery.go` | LLM-powered error analysis and retry decisions (flows) |
| `pkg/agent/ephemeral_knowledge.go` | Request-local automatic-memory snapshot/wrapper and legacy history projection; preserved Code/external context rendering |
| `pkg/agent/guidance_content.go` | Guidance documents for indexed retrieval (Tier 2) |
| `pkg/agent/tool_response_truncate.go` | BeforeModelCallback to truncate oversized tool responses |
| `pkg/agent/protected_tool.go` | Approval-gated tool wrapper |
| `pkg/agent/lazy_mcp_toolset.go` | Deferred MCP toolset initialization |

## Interactions

- **Sandbox**: Tools execute inside containers via the node protocol. The ChatAgent receives sandbox-wrapped tools from `WrapToolsWithNode()`.
- **Credentials**: BeforeToolCallback substitutes `{{CREDENTIAL:...}}` placeholders; AfterToolCallback restores them. PendingVault handles `<<<SECRET_N>>>` tokens.
- **Sessions**: The `SessionService` persists conversation history. Context compaction rewrites history when the window fills.
- **Memory**: Auto-knowledge retrieval queries the vector store before each turn. Memory reflection saves knowledge after turns.
- **Flows**: The `FlowDistiller` converts execution traces into YAML flow definitions. `AstonishAgent` executes those flows.
- **Channels**: Channel-specific hints are injected into the system prompt. Image side-channels deliver screenshots to Telegram/email.
- **Fleet**: Fleet agents use `SubAgentManager` with custom prompts, override tools, and dedicated sandbox containers.
