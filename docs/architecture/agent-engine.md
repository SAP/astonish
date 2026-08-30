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
- **Auto-knowledge retrieval**: Every turn triggers a vector search to inject relevant guidance into the system prompt.
- **Execution tracing**: Every tool call is recorded for flow distillation and memory reflection.
- **Think-tag filtering**: Chain-of-thought content (`<think>` blocks) must be stripped from streaming output.

ADK's callback system (BeforeToolCallbacks, AfterToolCallbacks, BeforeModelCallbacks) provides the extension points, but the coordination between them is Astonish-specific.

### Why a Three-Tier System Prompt

The system prompt uses a deliberate tiered architecture to balance token efficiency with comprehensive guidance:

- **Tier 1 (Session Snapshot)**: Identity, behavior rules, environment information, capability summaries, and other static inputs. In the cache-stable path, the exact built prompt is persisted in session state and reused after resume.
- **Tier 2 (Indexed Guidance)**: Detailed how-to documentation for each capability (browser, credentials, scheduling, etc.). Stored as `memory/guidance/*.md` files indexed in the vector store and retrieved only when relevant.
- **Tier 3 (Per-Turn Context)**: Retrieved knowledge, catalog matches, channel/scheduler hints, session instructions, skill indexes, and mode guidance. In the cache-stable path this is a marked user-role event persisted byte-for-byte and hidden from transcript, memory, reflection, and distillation views.

Every provider and session uses this cache-stable path. There is no legacy dynamic-declaration path or rollout switch. Automatic relevance matches, pinned groups, and explicit `search_tools` results are model-facing context only and never become provider tool declarations. Existing sessions keep the prompt snapshot stored when they were created; prompt changes apply to new sessions unless an operator explicitly starts a new session. Rebuilding an existing session prompt is intentionally not automatic because it invalidates the stable provider prefix.

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
   Results injected into SystemPromptBuilder.RelevantKnowledge
   A content-less `_knowledge_injection` diagnostic event records the query,
   BM25 context length, result count, token estimate, and result provenance
   (id, scope, creator, created_at, session_id when available).
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
   - Cache-stable path: reuse the persisted system prompt and persist dynamic context as a hidden user-role event
   - Legacy path: rebuild the system prompt with per-turn fields
    |
    v
5. LLM Agent Creation: llmagent.New() with callbacks
   - BeforeToolCallbacks: credential substitution, secret token resolution
   - AfterToolCallbacks: credential restoration, redaction, trace recording, image stripping
   - BeforeModelCallbacks: tool response truncation and context compaction
    |
    v
6. Execution Loop (with retry):
   - llmAgent.Run() produces streaming events
   - Retryable errors (429, 502, 503) -> exponential backoff, retry up to 3x
   - Deferred tools are called through `execute_tool`; unknown direct names keep ADK's default error
   - Tool call count cap (default 25) -> pause and ask user to continue
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
- **Tool groups**: Tools are organized into named groups (core, browser, mcp:*). The LLM specifies which groups each sub-agent needs.
- **Depth limiting**: Default max depth of 2 prevents infinite delegation chains.
- **Container sharing**: Sub-agent sessions are aliased to the parent's sandbox container via `NodeClientPool.Alias()`.
- **Event forwarding**: When `UIEventCallback` is set, sub-agent events (tool calls, text) are streamed to the UI in real-time.
- **Bounded liveness**: Every child has the existing absolute task timeout plus a shorter inactivity watchdog (two minutes by default). Meaningful activity is a tool call/result or non-thought text; hidden thought tokens never reset the watchdog. The watchdog cancels the child runner context, so a provider wait cannot hold a semaphore slot indefinitely.
- **Observable state**: Structured progress events distinguish `queued`, `running`, `waiting_on_model`, `retrying`, `complete`, and `failed`, with attempt, elapsed duration, last-activity age, and an explicit inactivity reason. Heartbeats report state but do not themselves count as progress.
- **Progress-gated retry**: The outer one-time retry is permitted only after meaningful partial progress (`ToolCalls > 0` or visible result text). Inert children fail promptly without retry; all cancellation paths release the concurrency semaphore.

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
| `pkg/agent/ephemeral_knowledge.go` | BeforeModelCallback for non-persisted knowledge injection |
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
