# Progressive MCP Completion Audit

## Status

The source builds and the focused/broad package tests recorded in the prior execution pass. However, the approved plan's end-to-end acceptance outcome has not been proven, and the current implementation does not yet satisfy every requirement stated in the approved plan.

## Completed and verified

- Public MCP registration exposes the four intended names and omits `astonish_chat` in the route test.
- The shared external system-prompt and progressive executor foundation has focused unit coverage.
- `go test` passed for the affected agent, API, launcher, OAuth, execution, A2A, and store packages.
- `make build` produced the `astonish` binary.
- Architecture documentation now identifies the breaking public MCP migration and `tool:execute` authorization boundary.

## Missing or not proven

### 1. Live configured-daemon protocol drill

The required live MCP protocol sequence was not run. The plan-level acceptance wrapper failed before execution because it attempted to execute prose containing unmatched quotes. No replacement live daemon exercise was performed.

Therefore these user-visible outcomes remain unproven against a running OAuth-configured daemon:

- Exact `tools/list` response.
- A valid OAuth `tool:execute` connection and an unscoped-bearer rejection.
- Context retrieval with real three-tier memory and an enabled A2A fixture.
- Search, description, and deterministic read-only execution through the full HTTP/MCP protocol.
- Cross-principal/session token replay rejection.
- Catalog mutation and stale-token rejection.
- A normal Studio chat turn after the MCP migration.

### 2. Durable per-turn runtime context

`MCPProgressiveRuntime` currently retains the HTTP request context from the `get_agent_context` request. Streamable MCP calls are separate requests, so a request context can be canceled after the context call finishes. The runtime must own a detached, request-safe context containing only the authenticated/scoped values needed by later calls, rather than retaining a completed HTTP request context.

### 3. Request-scoped tool construction is incomplete

The plan requires construction of request-scoped outbound MCP groups via `buildRequestMCPToolGroups`, plus configured A2A virtual tools, then merging those groups into the snapshot. The current runtime reads any groups already on the context, but does not build outbound MCP groups itself. This can leave tenant-configured MCP tools absent from the catalog and execution runtime.

### 4. Snapshot freshness binding is incomplete

Tokens currently bind a principal key, session ID, expiry, and runtime pointer. The plan requires binding to prompt/catalog fingerprints and rejection of stale catalog snapshots. No fingerprint or catalog-generation validation is currently implemented, so a configuration/catalog mutation does not produce the required `context_stale` result.

### 5. Token session validation is not externally enforceable

The token records the session ID, but the subsequent three MCP tools accept no session ID and do not compare one. The token cannot currently be rejected based on a caller claiming another session. Principal replay is checked; session replay semantics need an explicit design/test compatible with the four-tool schemas.

### 6. Planned regression coverage is incomplete

The plan named tests for memory empty/error/populated states, A2A visibility, disabled tools, approval propagation, sandbox scope before first tool, stale/cross-principal tokens, OAuth capability isolation, and native Studio behavior. The passing route tests do not demonstrate that full matrix. Several files listed by the plan were not changed.

## Recommendation

Do not treat the migration as production-complete yet. Next work should first correct the durable runtime/context-token model and request-scoped MCP/A2A catalog creation, then add protocol-level integration tests and run the configured live daemon drill. After that, the plan can be truthfully marked complete.
