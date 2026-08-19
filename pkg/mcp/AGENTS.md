# pkg/mcp — AGENTS.md

MCP configuration-to-toolset bridge. This package owns transport creation, runtime credential expansion, initialization diagnostics, and cleanup.

## Lifecycle
- `NewManager` loads personal-mode config; `NewManagerFromConfig` accepts already-scoped platform configuration.
- Initialize only enabled/requested servers, retain successful transports/toolsets, and expose per-server results instead of making one bad server erase healthy ones.
- Every retained transport must be closed by `Cleanup`; keep cleanup safe after partial initialization and clear retained state.

## Invariants
- Persisted `MCPServerConfig.Env` keeps `{{CREDENTIAL:name:field}}` placeholders. Resolve a copied config only immediately before transport creation; never mutate or persist resolved values.
- Preserve transport semantics for stdio, SSE, and streamable HTTP. Add a transport by updating validation, construction, diagnostics, tests, and callers together.
- Diagnostics are secret-safe and bounded. Log env keys/counts, never env values; redact known secret values from stderr/output and preserve truncation limits.
- Disabled servers remain non-runnable. Selective initialization must not silently initialize unrequested servers.
- Avoid stale manager/transport captures during reloads; callers that need live state should resolve it at use time.

## Verification

```bash
go test ./pkg/mcp/...
go test ./pkg/mcp -run 'Test.*(Manager|Transport|Credential|Diagnostic|Cleanup)'
```

Include `./pkg/config/... ./pkg/credentials/...` when changing config resolution or redaction.

## References
- [`pkg/config/AGENTS.md`](../config/AGENTS.md)
- [`pkg/credentials/AGENTS.md`](../credentials/AGENTS.md)
- [`pkg/agent/AGENTS.md`](../agent/AGENTS.md)
