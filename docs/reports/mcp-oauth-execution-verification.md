# MCP OAuth execution verification

## Completed implementation

The OAuth/MCP permission work is built into the local `astonish` binary and the daemon was restarted successfully.

- OAuth discovery at `http://127.0.0.1:9393/.well-known/oauth-authorization-server` advertises `openid`, `offline_access`, `tool:execute`, and `chat`.
- The daemon is running as PID `26424` and listening on TCP port `9393`.
- An unauthenticated JSON-RPC `tools/list` request to `/api/mcp` returns `401` in approximately 2 ms with `MCP authentication required`.
- The MCP adapter is configured for Streamable HTTP responses and does not propagate MCP request cancellation into Studio chat execution.
- When a client supplies an MCP progress token, non-empty Studio SSE activity is translated into `notifications/progress`; the MCP tool call still emits one final result.
- The OAuth authorization architecture document records that progress does not defeat an MCP client’s absolute deadline and that a resumable run/status contract is still future work.

## Automated verification

The following succeeded after the final edits:

```text
go test ./pkg/api -run 'TestMCP.*(Scope|Chat|Bearer|Authorization|Recorder|Progress)'
go test ./pkg/oauthserver -run 'TestOAuth.*Scope'
go test ./pkg/oauthserver ./pkg/api -run 'OAuth|MCP'
cd web && npm test -- --run src/api/__tests__/oauth.test.ts src/api/__tests__/platformAdmin.test.ts src/components/__tests__/OAuthTab.test.tsx src/components/platformAdmin/__tests__/OAuthTab.test.tsx src/components/oauth/__tests__/OAuthScopeSelector.test.tsx
cd web && npm run typecheck
make build
```

The focused frontend suite passed 5 files / 10 tests. The Go binary built successfully.

## Remaining live verification blocker

A full OpenStack MCP chat call needs a *fresh* OAuth bearer token containing both `tool:execute` and `chat`, because daemon restart invalidated the previous ephemeral-token signing context. No reusable token or local persisted OAuth-client record was available in this session, and the browser automation endpoint blocks local-loopback navigation in this environment. Therefore this run did not exercise the final OpenStack VM query or a client-timeout recovery flow.

The implementation deliberately does not claim recovery after a hard client deadline: progress notifications provide liveness only. A resumable server-side run/status API is required before an MCP host that aborts a long request can later retrieve the final result.

## Plan verification note

The persisted plan’s top-level verification field begins with natural-language text (`Acceptance sequence:`). The execution runtime attempted to run that literal text as a shell command and failed with shell syntax error, despite the automated checks above passing. That is a plan-metadata verification failure, not an implementation or test failure.
