# Provider Integrations

## Overview

Astonish supports 12+ LLM providers through a factory pattern that creates ADK-compatible `model.LLM` instances. Each provider adapter handles the specifics of API authentication, request formatting, streaming, and error mapping, while presenting a uniform interface to the rest of the system.

## Startup defaults and scope

`api.EffectiveAppConfigFromContext` and the HTTP helper share one resolution path: explicitly supplied platform → org → team → personal defaults cascade even when no team store exists. Empty fields inherit. Host YAML providers/defaults are a fallback only for trusted file-backed single-tenant deployments; SQLite and PostgreSQL are both multi-user and must not implicitly inherit host credentials when scoped stores are absent. An empty platform installation has no deployable default and Studio pre-warm defers LLM binding; this is not a reason to block serving configuration/auth endpoints. Config resolution never persists its effective result.

## Skill-validation provider isolation

Platform skill validation resolves the request's effective provider cascade even when the global ChatManager is warm. A global/operator model cannot substitute for a missing scoped provider: validation returns HTTP 503 without persisting status or consuming its success cooldown. Non-platform requests retain warm-model reuse. Missing-provider tests isolate host configuration; SQLite/PostgreSQL cases additionally exercise host-only defaults and a warm global model.

## Bot per-run budget wrapper

`NewBudgetLLM(inner, budget, BudgetLLMOptions)` is a per-run decorator, never a pooled provider mutation. It reserves conservative input/output/cost before dispatch, clones request output limits and settles cumulative usage maxima (including thought/tool-prompt tokens), not the sum of repeated stream metadata. Missing/incomplete usage, cancellation or interruption retains the conservative reservation. Providers unable to enforce a reduced output allowance fail before dispatch. Parent, delegate, flow and compaction calls share the run ledger; approval continuation restores committed totals, not a new allowance.

Unknown price is not zero cost. Production Bot readiness rejects `MaxModelCostMicros` until a conservative price oracle is supplied. Hidden billable retries beneath the decorator cannot be individually metered; billable retries must re-enter it. Controlled provider tests establish wrapper behavior, not external billing accuracy or every provider's output-limit enforcement. See [Phase 3 boundaries](bots.md).

The default input estimator (`pkg/provider/budget_input.go`) starts with the full request JSON byte bound. After a successfully settled, completely consumed response with non-partial positive prompt usage, that **per-decorator** observation may price the identical prompt prefix again at its full observed input count. This is not a cached-token discount or a learned bytes/token ratio. SHA-256 snapshots require identical model name, request model, serialized non-history configuration (including system instructions and model-facing tool declarations), and every complete prior history message. Appended messages are charged at JSON byte length plus 64 tokens per message and 256 tokens of framing headroom. Changed/compacted history or configuration falls back to the byte bound; explicit `EstimateInput` callbacks remain authoritative and do not learn. One immutable observation is retained behind a mutex, with no prompt text copied into long-lived state. Runtime tool implementations excluded by ADK JSON are not model input.

This assumes the same effective provider's prompt accounting is stable for an identical serialized prefix; it does not establish tokenizer-exact bounds for arbitrary provider-side transformations or hidden same-name model changes. Remote/cached/media requests still require a model-aware estimator, and cold or changed large prompts can still conservatively fail admission. Finite run/mandate token limits, output clamping, actual-usage settlement, unknown-cost rejection and overrun blocking are unchanged. Tests cover the observed 80,920 reservation → 33,018 settlement shape followed by an append-only continuation under the same 100,000-token limit, plus invalidation and concurrent accounting. This is controlled regression evidence, not a live schedule acceptance claim.

## Serial generation for Bot checkpoints

`botApprovalCaptureModel` clones each parent-turn request and sets the typed
`common.ParallelToolCallsOption` boolean to `false` using ADK's existing
`GenerateContentConfig.HTTPOptions.ExtraBody` extension carrier. ADK v1.9.0 /
GenAI v1.76.0 has no native parallel-call configuration field. The shared OpenAI
adapter explicitly reads this one boolean and emits `parallel_tool_calls: false`
on both Chat Completions and Responses requests when function tools are present,
including LiteLLM and positively detected compatible Responses routes. It does
not enable arbitrary ExtraBody forwarding. Absent values retain provider defaults;
malformed explicit values fail before HTTP dispatch. Explicit `true` remains
available to non-checkpoint callers, but checkpoint capture overrides it locally.
Tool-less requests omit the parameter.

This is per request, not a pooled model setting. Budget clamping, swappable models
and tool canonicalization preserve it. Ordinary Chat, auxiliary Bot calls and
provider factory/cache identities are unchanged. Native Google strips the
OpenAI-only extension before GenAI dispatch to avoid an unsupported native field;
native Anthropic/Bedrock/Vertex converters do not forward it. This change does
**not** establish serial-generation support for those native protocols. All still
retain the checkpoint's complete-response batch rejection before execution.
OpenAI-compatible servers that reject the option fail normally; there is no retry
that removes the constraint. Servers that ignore it cannot bypass batch rejection.

`TestBotApprovalCaptureSerialProviderWire` exercises actual serialized requests
through capture → budget → swappable model → OpenAI/LiteLLM/compatible adapters →
a loopback HTTP fixture, including compatible Responses discovery. It asserts
single-action identity, rejects malicious multi-action replies with no forwarded
response, proves pending checkpoints make no further request, and reuses the same
provider with ordinary Chat to prove no setting leak. `TestParallelToolCallsWire`
covers streaming/non-streaming, both endpoints, absent/true/false/malformed options
and tool-less requests. These are controlled tests, not external provider or live
employee-journey acceptance.

## Key Design Decisions

### Why a Factory Pattern

The `NewLLM()` factory function takes a provider name and model name, returning an `model.LLM` interface. This decouples the agent engine from specific providers -- switching from Anthropic to OpenAI requires only a config change, not code changes. The factory dispatches based on provider name to the appropriate constructor.

### Why ADK-Native Where Possible

Google's ADK provides built-in support for Google GenAI (Gemini). For OpenAI and Anthropic, Astonish uses their native Go SDKs wrapped in ADK's model interface. For all other providers (OpenRouter, Groq, xAI, LiteLLM, etc.), the OpenAI-compatible API adapter is used since most providers have standardized on OpenAI's API format.

### Why Context Window Tracking

Each provider/model combination has a known context window size. The system tracks this to enable:

- **Context compaction**: The Compactor knows when to summarize history.
- **Prompt budgeting**: The system prompt builder can estimate remaining capacity.
- **Error prevention**: Avoid sending requests that exceed the model's limit.

Context budgets and Code mode indicators share `pkg/provider/context_window.go`. Resolution precedence is an explicit `general.context_length` override, provider API metadata, an exact SAP model configuration (for SAP instances) followed by the shared curated model map, then the universal 200,000-token default. Only the universal default is marked `IsFallback`; a known model whose conservative capacity happens to be 200k is not.

For SAP AI Core, authoritative capacity comes from the authenticated `GET /v2/lm/scenarios/foundation-models/models` catalog's `versions[].contextLength`, matching the exact `model` ID. A unique latest version must have a positive capacity. Without a latest designation, the available positive capacities must agree; ambiguous latest entries, invalid latest capacity, or differing positive lengths are not guessed or resolved by taking the largest value. Model-name-only configuration cannot establish pinned deployment-version accuracy.

Successful catalog maps, including empty maps, and deployment metadata lists use one-hour connection-scoped caches. Connection identity includes normalized base/auth URLs, client ID, client secret and effective resource group in a SHA-256 fingerprint; secret inputs are not logged. Failed catalog requests are not cached, and SAP context resolution bypasses the generic indefinite provider-name/model cache, so credential rotation, overrides and outage recovery are not stranded there. Local Code mode retains environment fallback; confined platform contexts refuse operator environment credentials.

SAP deployment listing and deployment-ID resolution bound both success and error response bodies to 8 MiB, rejecting oversized responses before JSON decoding or diagnostic formatting. The limit does not change the running-deployment-only discovery contract.

The platform model metadata response overlays catalog capacity onto **running deployments only**: undeployed catalog models are never added. Catalog failure preserves deployment discovery and static fallback. Independently learned maximum-output and tool-support limits remain intact; capacity enrichment does not alter generation defaults. Existing Studio and terminal formatting display 1,100,000 as `1.1M` without new UI or transport fields.

The Code `/model` picker loads capacities for discovered IDs through the optional `backend.ModelContextBackend` capability, implemented by the local backend and forwarded by the lazy Code wrapper. It uses the same resolver and fallback provenance as the header, stores values with the selected provider's model list, and performs no network requests while rendering or filtering. Backends without that capability retain static display hints.

Deterministic checks use loopback OAuth/catalog/deployment servers, the actual platform HTTP handler and Code backend, and the existing renderers:

```bash
go test -race ./pkg/provider/... -count=1 -timeout=10m
go test ./pkg/api -run TestSAPCatalogMetadataHTTP -count=1
go test ./pkg/launcher -run TestCodeSAPCatalogContext -count=1
go test ./pkg/tui -run 'TestRenderHeader.*(SAP|Fallback)|TestFormatTokenCount' -count=1
(cd web && npm test -- src/test/scenarios/panel-interactions.test.tsx && npm run typecheck)
```

The 1,100,000 fixture verifies propagation, not any model's production capacity; a real model's value remains whatever its authenticated SAP catalog reports.

### Optional OpenAI-compatible metadata

OpenAI-compatible and LiteLLM instances probe the nonstandard LiteLLM `/model/info` extension using the configured URL and key. For `http://localhost:6655/litellm/v1`, probes are `/litellm/v1/model/info` then `/litellm/model/info`; only the final `/v1` component is removed, never the proxy prefix. All redirects are rejected, responses are bounded to 8 MiB, and the entire probe sequence has a three-second timeout. Scoped platform requests do not inherit operator environment secrets.

Exact `model_name` entries enrich only IDs discovered by `/models`. Positive `context_length` or `context_window` fields and `max_input_tokens` supply a conservative budget (the smaller positive value when both exist). With only `max_input_tokens`, the context indicator is an input budget, **not a claimed total context capacity**. `max_output_tokens` remains separate and is exposed as `max_completion_tokens` for Studio. `max_tokens` is never interpreted as context capacity, and input/output limits are never summed. Generation output defaults are unchanged.

For the narrowly defined same-origin layout where a configured URL ends in `/litellm/v1`, GPT-6 generation also probes the sibling `/openai/v1/models` with the same credentials and exact model ID. A positive match routes that model's generation through `/openai/v1/responses`; Chat Completions and ordinary discovery remain on `/litellm/v1`. This capability probe is a bounded, non-billable GET with redirect rejection, and caches positive results for one hour and definitive unsupported results for one minute by URL, credential and model fingerprint. Other paths, missing exact IDs, outages and incompatible servers retain Chat Completions; Astonish never guesses Responses support from a model name alone.

Successful metadata is cached for one hour by URL/key fingerprint; unsupported schemas/endpoints and denied access are cached for one minute. Transient outages are not cached. The indefinite outer context cache is bypassed for these connections. No model-specific lookup is added; absent usable metadata retains existing static/default resolution and basic model discovery. Code `/model`, its header and runtime budget use the shared resolver; Studio receives the same conservative capacity and output limit from the metadata HTTP handler.

```bash
go test -race ./pkg/provider/... -count=1
go test ./pkg/api -run TestCompatibleMetadataHTTP -count=1
go test ./pkg/launcher -run TestCodeCompatibleMetadataContext -count=1
```

### Why Canonical Tool Serialization

Provider adapters canonicalize tool schemas before constructing outbound requests. Function declarations are globally sorted by name independent of their original grouping; JSON object keys and safe set-like arrays (`required`, `enum`, and union `type`) are normalized recursively; empty object schemas use one representation; and allowed function names are sorted. Semantically ordered arrays remain in source order. Canonicalization failures stop request construction rather than silently sending unstable data.

For Gemini model IDs (`gemini-*`) using the shared OpenAI-compatible adapter, nullable JSON Schema type arrays with exactly one distinct non-null type are converted recursively to a scalar `type` plus `nullable: true`. This handles ADK-generated nested slices such as `announce_plan.steps` and `steps.items.files`, which otherwise lose their type through the proxy's Gemini translation. Normalization operates only on the canonical request copy and schema positions; descriptions, required fields, items, defaults, examples and enum payloads remain intact. Multi-type ambiguous unions are not guessed. Non-Gemini requests and native Vertex conversion remain unchanged. `TestGeminiAnnouncePlanNullableSchema` exercises the real tool declaration across the HTTP boundary in streaming and non-streaming modes.

OpenAI-compatible responses map `prompt_tokens_details.cached_tokens` to ADK's cached-content token count when the server reports it. Cache fingerprints log only truncated SHA-256 hashes and counts, never prompt contents or credentials.

### Why Connection Pooling

HTTP connection pooling (`httpool`) is used to reuse TCP connections across LLM API calls. This reduces latency for sequential calls (common in tool-heavy turns) by avoiding TCP handshake and TLS negotiation overhead.

### Why Structured Error Types

The `pkg/provider/llmerror` package defines structured error types for LLM API failures:

- **Retryable errors**: 429 (rate limit), transient server failures, and timeouts. The agent engine retries these with exponential backoff within its existing three-attempt budget. Authentication failures, including plain-text or JSON JWT/token-expiry 401 responses, are not credential-blind retry triggers. Renewable providers recover in their HTTP transport before SDK error classification; exhausted authentication failures remain terminal, and failures after emitted model output must never blindly replay.
- **Context length errors**: The request exceeded the model's context window.
- **Authentication errors**: Invalid API key or expired credentials.
- **Unknown tool errors**: The model hallucinated a tool name that doesn't exist.

These structured errors enable the agent engine's retry logic to make informed decisions.

## Architecture

### Supported Providers

| Provider | Adapter | Notes |
|---|---|---|
| **Anthropic** | Native SDK | Claude models, native streaming |
| **Google GenAI** | ADK built-in | Gemini models |
| **OpenAI** | Native SDK | GPT models, native function calling |
| **Amazon Bedrock** | Custom | AWS credential chain, multiple model families |
| **Google Vertex AI** | Custom | GCP authentication, Gemini models |
| **SAP AI Core** | Custom | Enterprise, OAuth authentication |
| **OpenRouter** | OpenAI-compatible | Multi-provider routing |
| **Groq** | OpenAI-compatible | Fast inference |
| **xAI** | OpenAI-compatible | Grok models |
| **Poe** | OpenAI-compatible | Multi-model access |
| **LiteLLM** | OpenAI-compatible | Local proxy, multi-provider |
| **Ollama** | OpenAI-compatible | Local models |
| **LM Studio** | OpenAI-compatible | Local models |
| **OpenAI Compatible** | OpenAI-compatible | Any OpenAI-format API |

### Provider Initialization

```
Daemon startup
    |
    v
Config: providers section lists configured providers
    |
    v
For each provider:
  1. Resolve API key from credential store (or config, or env var)
  2. Set environment variables (ANTHROPIC_API_KEY, OPENAI_API_KEY, etc.)
  3. Provider is ready for use
    |
    v
ChatAgent creation:
  NewLLM(providerName, modelName) -> model.LLM interface
```

### Authentication Renewal and Recovery

Renewable adapters use `httpool.RoundTripWithToken` below the SDK layer for generation and model discovery. Before each request they check cached expiration and acquire usable credentials when needed. If the first response rejects authentication, they invalidate the exact rejected credential, renew it (or reuse a newer valid credential installed by another request), and replay the identical request **once**. Reactive renewal still runs if proactive renewal happened immediately before that rejected request. A successful replay is returned normally, without exposing the intermediate authentication failure or introducing UI/SSE events.

| Authentication path | Renewal mechanism | Proactive check | Reactive recovery |
|---|---|---|---|
| SAP AI Core, including its OpenAI, Vertex and Bedrock model paths | OAuth client-credentials exchange | 30-second expiry buffer | HTTP 401 only; SAP permission-denied 403 is not expiry |
| xAI OAuth | Refresh-token exchange with rotation and persistence callback | 60-second buffer; legacy missing expiry triggers renewal when a refresh token exists | HTTP 401/403 with a refresh token |
| GitHub Copilot OAuth | Persistent GitHub token exchanged for a session token | Missing expiry or 60-second buffer | HTTP 401/403 |
| Static API-key/token adapters, including access-token-only xAI setup | No configured renewal mechanism | No arbitrary token regeneration | Authentication rejection is terminal; no blind same-credential retry |
| Direct AWS/GCP credential-chain adapters | SDK-owned credential lifecycle | Existing SDK behavior | Not integrated with this shared replay helper |

Token caches and exchanges are mutex-protected. Compare-and-invalidate ensures a late rejection of an older token does not discard a newer credential or unnecessarily rotate it again. Renewal rejects empty tokens and invalid/nonpositive lifetimes; token exchange diagnostics do not expose raw secret-bearing responses. xAI refresh-token rotation updates the cache atomically. Persistence callbacks run outside the token mutex, with serialized notifications and generation checks so delayed older callbacks cannot overwrite newer persisted credentials; callbacks can read/reenter an already-valid transport.

The helper clones request headers and URL before decoration. It closes the first rejected response before renewal, recreates nonempty request bodies only through `GetBody`, and preserves nil/`http.NoBody` requests. A non-replayable body fails with `ErrAuthReplayUnavailable` after invalidating the rejected token: it is never sent empty or buffered without a bound. Cancellation is preserved and unsent bodies are closed. Successful response bodies are neither read nor consumed by authentication recovery, so a successful stream is delivered once; errors inside an already-started stream do not trigger authentication replay.

A second authentication rejection invalidates the renewed cached token and returns the final response without another replay. Renewal failures retain OAuth reauthentication sentinels and context-cancellation identity; OAuth model discovery and generation retain their existing final reauthentication handling. A subsequent request can attempt fresh acquisition, but outer authentication classification cannot restart an identical doomed retry loop. Existing provider constructors, access-token-only `ListModels` signatures, configuration/persistence contracts and tenant-confined environment resolution remain unchanged; renewable xAI listing uses `ListModelsWithOAuth`.

Deterministic tests exercise the actual adapters and transports against loopback token and inference endpoints, using the same provider instance across rejection, renewal, identical replay and later successful calls. They cover streaming/non-streaming generation, model discovery, concurrent rejection, refresh failure, second rejection, cancellation, body ownership and static-key failure. They do not exercise production credentials, prove overnight upstream reliability, or establish why the original upstream token was rejected.

```bash
go test -race ./pkg/provider/... -count=1 -timeout=10m
go test ./pkg/tui -run 'Test.*(Reauth|OAuth)' -count=1
```

### Error Handling Flow

```
Renewable provider HTTP request:
  Check expiry -> acquire token -> send request
  First eligible auth rejection -> renew -> replay once
  Success -> return response/stream normally
  Renewal failure or second rejection -> final error
    |
    v
SDK / llmerror classification:
  - Rate limit (429) -> retryable, Retry-After header
  - Transient server error / timeout -> retryable, exponential backoff
  - Context length -> context overflow
  - Authentication failure, including JWT/token expiry -> non-retryable
  - Unknown tool -> unknown-tool error
    |
    v
Agent engine:
  - Retryable error -> wait and retry (existing bounded budget)
  - Unknown tool -> inject synthetic error response, let LLM self-correct
  - Context overflow -> trigger compaction
  - Final authentication error -> surface to user / existing OAuth reauth handling
```

## Streaming Safety and Generation Progress

### Completion is a protocol boundary, not merely EOF

Partial text can be displayed before a response completes. Executable function calls must remain buffered until the adapter validates the whole tool batch and its terminal boundary; a closed tool-content block alone does not authorize execution. Invalid identities, mismatched block/index state, incomplete or non-object JSON arguments, provider error events, and unsuccessful terminal states must not silently become executable calls. A read failure or EOF without the required completion evidence is an error, not successful generation.

The implemented boundaries differ by adapter:

| Streaming path | Completion and tool publication |
|---|---|
| SAP Bedrock protocol (`pkg/provider/bedrock/protocol.go`) | Requires `message_stop`, no unfinished tool, and an accepted stop reason. Older streams may omit the reason, but not `message_stop`. Truncation, refusal and unknown reasons fail; validated tools, aggregate text and usage are published together only at completion. |
| Direct Anthropic (`pkg/provider/anthropic/anthropic.go`) | Requires `message_stop` with all blocks closed; rejects truncation, refusal and unknown stop reasons. Validated tool calls and aggregate content/usage are held until then. |
| OpenAI Chat Completions and compatible adapters (`pkg/provider/openai/openai.go`) | Requires accepted `finish_reason` (`stop` or `tool_calls`) and SDK stream EOF; usage-only tail chunks are retained. Validates all accumulated calls before publishing any. A missing finish reason can preserve accumulated prose before returning an error, but never publishes unfinished tools. |
| OpenAI Responses (`pkg/provider/openai/responses.go`) | Requires `response.completed` and SDK stream EOF before converting the final response and validating tools; failed/incomplete/error events fail. |
| SAP Vertex protocol (`pkg/provider/vertex/protocol.go`) | Requires `STOP` and clean EOF, because usage can follow the finish chunk. Tool names and object arguments are validated; truncation, blocked output and other non-`STOP` finishes return errors without a successful final aggregate. |

These are the hardened adapter paths, not a claim about every provider in the registry. In particular, direct Google GenAI uses ADK's implementation and is not covered by this contract. Existing non-streaming behavior is separate. Vertex preserves `Thought` and `ThoughtSignature` on their original parts through conversion; this describes observed field handling, not a blanket guarantee about signed payloads across providers.

### Payload-free progress

`pkg/provider/streaming` defines context-scoped `WithProgress` / `Report`, typed kinds (`KindPhase`, `KindHeartbeat`, `KindRecovery`), and phases (`PhaseThinking`, `PhaseToolArguments`, `PhaseWaiting`, `PhaseRecovering`). The zero kind remains a legacy phase notification. `Progress` carries only kind/phase, one-based attempt/max-attempt counts, generation `Elapsed` and time-since-upstream-bytes `LastActivity` durations: no reasoning, tool names or argument fragments. Delivery is synchronous and bounded by phase guards or heartbeat intervals, not per token. It is a status notification, not a synthetic assistant response or a tool call, and does not imply successful completion.

`ChatRunner.Run` maps these notifications onto the **existing** `thinking` SSE event with status text:

- `Model is thinking…`
- `Preparing tool arguments…`
- `Waiting for model response (Ns)…`
- `Restoring model stream (attempt N)…`

Structured `Thought` responses also have a bounded runner-side status path. Raw reasoning and unfinished arguments must not be substituted into these messages. Studio's send/reconnect handlers and the remote terminal TUI already consume `thinking`; no new wire event is needed. See [Chat Rendering Pipeline](chat-rendering-pipeline.md#streaming-progress-completion-and-delivery-boundaries).

### Typed truncation signaling

OpenAI Chat Completions signals a missing `finish_reason` with `llmerror.TruncatedStreamError`. The API truncation-retry gate uses `errors.As`, not diagnostic text, and retries only before content has been observed and when no `InterruptedStreamError` wraps the failure. This marker does not enable generic provider retries or context-overflow recovery. Other providers' terminal-error policies are unchanged.

### SAP Bedrock reset recovery boundary

`Provider.generateBedrockStream` owns a narrow recovery policy: **at most three total attempts**, with context-aware 50 ms and 100 ms waits. Recognized HTTP/2 `INTERNAL_ERROR` or `REFUSED_STREAM` resets and `ErrBedrockStreamIdle` share this single budget. Before text is delivered it replays the serialized request; progress and usage alone do not count as visible output. General network failures, ordinary EOF, malformed tools, provider error events and cancellation are not recovery triggers.

SAP Bedrock response-body reads default to a **120-second idle interval after upstream bytes have begun** and **15-second waiting heartbeat**. `NewProviderWithConfig(..., options ...ProviderOption)` remains compatible with existing calls; `WithBedrockStreamTiming(idle, heartbeat)` validates positive durations before deployment lookup. These timings do not change other providers or impose a total generation/global HTTP timeout. Initial model computation is not canceled merely because no first byte has arrived; it remains bounded by caller cancellation while heartbeats expose elapsed waiting time. After the first positive bytes, the guard is armed only while awaiting upstream `Read`: any positive bytes (including SSE comments or private tool/thinking fragments) reset read-idle, while zero-byte reads do not. Parsing time and downstream consumer/yield delays are excluded. Before initial bytes, last-activity age starts at attempt start; elapsed age spans recovery attempts.

Each attempt owns its child context and response body. Idle closes/cancels only that attempt; parent cancellation wins over idle/reset recovery. Parsing, progress callbacks and downstream yield are serialized on the iterator goroutine; only an outstanding body read uses a worker, which is joined before returning or retrying. No read-ahead occurs during yield. Timers stop when each read ends; normal `message_stop`, failure, cancellation and `yield(false)` close the attempt, so progress cannot outlive iteration. Recovery notifications identify the next attempt; waiting heartbeats identify the current attempt. Heartbeats report waiting, not proof that the model is generating tokens.

After partial ordinary text, a read-idle is terminal: the adapter preserves the partial chunks already delivered, emits an interrupted-stream error, and does not issue a continuation that could repeat/diverge or multiply the 120-second wait. For recognized non-idle HTTP/2 resets after partial text, the adapter does **not** replay the original request. Continuation construction requires a non-null `messages` array; missing, null or non-array history is rejected without producing a replacement payload. It builds a request-local continuation from the original serialized fields/history, appends all text already delivered as an assistant message, then a user instruction to continue without repetition. Ending with a user message avoids reliance on assistant prefilling. Failed attempts' tool blocks (even closed ones) are discarded, not executed or fed back as completed calls. Earlier completed tool/result pairs remain unchanged. The new response must still reach a valid `message_stop` before any tools are published. Its final aggregate prepends previously delivered text so live deltas and persisted final content agree. Only the successful attempt's reported usage is available; failed inference can incur additional unreported cost. Continuation requests are internal to the adapter and are not separate canonical ADK diagnostic records.

This is another generation, **not exact stream resumption**. The model is instructed not to repeat prose or earlier completed actions, but semantic repetition cannot be ruled out. There is no transport-level replay of executed tools. Recovery cannot guarantee completion during a persistent outage or for an oversized tool call that repeatedly exceeds an upstream limit. Continuation context consumes additional tokens and does not invoke a separate compaction step. Exhaustion or noneligible failures remain real errors; no incomplete tool is promoted to success. Durable large reports therefore use the destination Markdown file as their checkpoint: a unique HTML continuation marker identifies where the next bounded semantic section belongs. Recovery reads the file and inspects the marker and tail before continuing. This has no expected count, chunk ledger, generation ID, hash, or assembly step, and retries are not automatically idempotent; uncertain writes must be inspected before retrying.

Terminal post-output failures are wrapped in `llmerror.InterruptedStreamError`. The marker preserves the cause for `errors.Is` / `errors.As`, while `IsRetryable` and `IsContextOverflow` both return false, preventing outer retry or compaction from blindly replaying visible output. Consumer `yield(false)` and context cancellation close the stream and stop recovery. Anthropic, OpenAI and SAP Vertex retain their existing post-output interruption behavior; automatic continuation is specific to SAP Bedrock.

This is recovery, not evidence that the upstream reset root cause has been solved. Observed failures clustered around 605–612 seconds, consistent with an upstream time limit but insufficient to identify its owner. There is no new global stream timeout or HTTP version override. Provider iteration, downstream `yield` and progress callbacks remain synchronous. Payload-free warning logs record the recovery attempt and whether it is a continuation; no prompts, credentials or unfinished arguments are logged.

### Focused regression checks

Run from the repository root (commands document coverage, not recorded pass counts):

```bash
go test ./pkg/provider/streaming ./pkg/provider/llmerror
go test -race ./pkg/provider/sap ./pkg/provider/bedrock ./pkg/provider/streaming -count=1 -timeout=5m
go test ./pkg/provider/bedrock ./pkg/provider/vertex ./pkg/provider/anthropic ./pkg/provider/openai ./pkg/provider/sap -run 'Test.*(Stream|Streaming|Responses)'
```

Key cases include `TestStreamToolsAndUsageOnlyAtMessageStop`, `TestStreamingSafetyHeldOpenHTTP`, `TestOpenAIStreamSafetyTerminalAndBatch`, `TestResponsesStreamSafetyTerminal`, `TestVertexHeldOpenToolsAndUsage`, `TestBedrockStreamResetRecovery`, `TestBedrockStreamPostOutputNeverReplay`, and `TestBedrockStreamCancelBackoff`. They cover held-open streams, private progress, terminal/tool validation, usage, cancellation and replay boundaries; they do not prove live upstream reliability.

Real TLS/HTTP2 loopback cases `TestBedrockStreamIdleHTTP2Recovery`, `TestBedrockIdleHTTP2ContinuationAndExhaustion`, `TestBedrockIdleHTTP2ActiveReadsAndSlowConsumer`, and `TestBedrockIdleHTTP2StopAndYieldCleanup` inject short intervals to cover held bodies, combined reset/idle budgets, continuation, active bytes, slow consumers, cancellation and no late progress. These tests do not exercise live SAP credentials, actual upstream 120-second waits, or identify the owner of the observed upstream reset.

## Key Files

| File | Purpose |
|---|---|
| `pkg/provider/factory.go` | NewLLM() factory, provider registry |
| `pkg/provider/llmerror/` | Structured error types, classification |
| `pkg/provider/anthropic/` | Anthropic Claude adapter |
| `pkg/provider/google/` | Google GenAI (Gemini) adapter |
| `pkg/provider/openai/` | OpenAI GPT adapter |
| `pkg/provider/bedrock/` | Amazon Bedrock adapter |
| `pkg/provider/vertexai/` | Google Vertex AI adapter |
| `pkg/provider/sapaicore/` | SAP AI Core adapter |
| `pkg/provider/openai_compat/` | Generic OpenAI-compatible adapter |
| `pkg/provider/httpool/` | HTTP connection pooling |
| `pkg/config/provider_env.go` | Provider environment variable setup from credential store |

## Interactions

- **Agent Engine**: ChatAgent and AstonishAgent receive a `model.LLM` from the factory. Error classification drives retry logic.
- **Credentials**: Provider API keys are resolved from the encrypted credential store via `provider_env.go`.
- **Configuration**: Provider selection and model choice are in the app config.
- **Daemon**: Provider environment is set up during daemon initialization.
- **Memory Reflection**: The reflector makes its own LLM call using the same provider.
- **Flow Distillation**: The distiller uses the LLM for trace-to-YAML conversion.
