# pkg/provider — AGENTS.md

Model-provider adapters and model selection infrastructure. All providers implement the ADK `model.LLM` contract consumed by the agent runtime.

## Invariants
- Preserve both streaming and non-streaming behavior, tool calls/results, usage metadata, finish/error semantics, inline media, and system instructions when translating provider protocols.
- Streaming adapters may emit partial text, but session persistence also needs the correct final/non-partial content. Tool-only responses must not lose usage metadata.
- Normalize provider HTTP failures through shared error handling and never include API keys, authorization headers, or secret response data in diagnostics.
- Use shared HTTP transports/clients where provided; streaming requests must not gain a short global timeout.
- `Pool.Get` caches by the complete effective provider/model/config identity. Invalidate after provider settings or credentials change; do not let one tenant's effective configuration reuse another's instance.
- `SwappableLLM.GenerateContent` snapshots the current inner LLM under a read lock and releases it before long generation. `Swap` affects subsequent calls without blocking an in-flight stream.
- Provider-specific learned model limits/capabilities are durable overrides with cache invalidation; keep conservative static fallbacks for unknown models.

## Adding or changing a provider
1. Update construction/registration, model listing/metadata, effective config resolution, and frontend selectors together.
2. Add conversion tests for text, tools, errors, usage, streaming aggregation, and provider-specific edge cases.
3. Check agent compaction/context-window assumptions and pool invalidation callers.

## Verification

```bash
go test ./pkg/provider/...
go test ./pkg/provider -run 'Test.*(Pool|Swappable|Stream|Tool|Usage|Error)'
```

For configuration changes also run `go test ./pkg/config/... ./pkg/agent/...`.

## References
- [`pkg/config/AGENTS.md`](../config/AGENTS.md)
- [`pkg/agent/AGENTS.md`](../agent/AGENTS.md)
