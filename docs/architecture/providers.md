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
