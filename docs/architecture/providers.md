# Provider Integrations

## Overview

Astonish supports 12+ LLM providers through a factory pattern that creates ADK-compatible `model.LLM` instances. Each provider adapter handles the specifics of API authentication, request formatting, streaming, and error mapping, while presenting a uniform interface to the rest of the system.

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

Context window sizes are maintained in a registry within each provider adapter.

### Why Canonical Tool Serialization

Provider adapters canonicalize tool schemas before constructing outbound requests. Function declarations are globally sorted by name independent of their original grouping; JSON object keys and safe set-like arrays (`required`, `enum`, and union `type`) are normalized recursively; empty object schemas use one representation; and allowed function names are sorted. Semantically ordered arrays remain in source order. Canonicalization failures stop request construction rather than silently sending unstable data.

OpenAI-compatible responses map `prompt_tokens_details.cached_tokens` to ADK's cached-content token count when the server reports it. Cache fingerprints log only truncated SHA-256 hashes and counts, never prompt contents or credentials.

### Why Connection Pooling

HTTP connection pooling (`httpool`) is used to reuse TCP connections across LLM API calls. This reduces latency for sequential calls (common in tool-heavy turns) by avoiding TCP handshake and TLS negotiation overhead.

### Why Structured Error Types

The `pkg/provider/llmerror` package defines structured error types for LLM API failures:

- **Retryable errors**: 429 (rate limit), 502/503 (server overload), timeouts. The agent engine retries these with exponential backoff.
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

### Error Handling Flow

```
LLM API call fails
    |
    v
llmerror.Classify(err):
  - Rate limit (429) -> RetryableError{Wait: Retry-After header}
  - Server error (502, 503) -> RetryableError{Wait: exponential backoff}
  - Context length -> ContextLengthError
  - Auth error -> AuthError
  - Unknown tool -> UnknownToolError
    |
    v
Agent engine:
  - RetryableError -> wait and retry (up to 3x)
  - UnknownToolError -> inject synthetic error response, let LLM self-correct
  - ContextLengthError -> trigger compaction
  - AuthError -> surface to user
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
