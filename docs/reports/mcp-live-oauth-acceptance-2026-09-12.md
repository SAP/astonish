# MCP Progressive Loop — Live OAuth Acceptance Results

Date: 2026-09-12

## Environment

- Rebuilt local daemon launched on `http://127.0.0.1:19473`.
- The normal configured local issuer remained `http://127.0.0.1:9393`.
- A disposable confidential OAuth client with only `tool:execute` was created against the local development database and used to obtain a real client-credentials bearer.
- The daemon on `19473` accepted that bearer, proving issuer/resource validation across the configured local OAuth service and the rebuilt daemon.

## Passed live checks

1. **Unauthenticated rejection**
   - `POST /api/mcp` without a bearer returned `401`, `WWW-Authenticate: Bearer error="invalid_token"`, and `MCP authentication required`.

2. **`tool:execute` authorization**
   - The temporary client successfully obtained a bearer with scope `tool:execute`.
   - The rebuilt daemon initialized an MCP session using that bearer.

3. **Exact public tool list**
   - `tools/list` from the rebuilt daemon returned exactly:
     - `describe_tools`
     - `execute_tool`
     - `get_agent_context`
     - `search_tools`
   - It did not include `astonish_chat`.

4. **Context-token gate**
   - Calling `search_tools` with an invalid token returned a structured result with `state: "context_stale"`.

5. **Guidance/context retrieval**
   - `get_agent_context(session_id="live-acceptance-session", user_message="What memory tools can I use?")` returned:
     - `state: "ready"`
     - a non-empty opaque `context_token`
     - non-empty external-loop `instructions`
     - `context_version: "v1"`
     - `memory_state: "unavailable"` (reported explicitly rather than inventing memory)
     - a 163-entry tenant-scoped catalog.

6. **A2A discovery and execution**
   - The live catalog included configured `a2a` virtual tools.
   - `search_tools` found `a2a_sci_autonomous_operation_racks_with_space`.
   - `describe_tools` returned its schema.
   - `execute_tool` invoked the A2A virtual tool successfully and returned `state: "ready"` with a completed remote-task result.

7. **Principal-bound token replay rejection**
   - A second, separately authenticated OAuth client attempted to use the first client’s context token.
   - The daemon returned `state: "context_stale"`, confirming that context tokens are bound to the authenticated principal.

8. **Unscoped bearer rejection**
   - A temporary OAuth client with only `chat` obtained a valid bearer, but `POST /api/mcp` returned `401 MCP authentication required` before MCP dispatch.

## Failed / incomplete acceptance checks

1. **Deterministic first-party tool execution is blocked by sandbox readiness**
   - The sequence `get_agent_context → search_tools(filter_json) → describe_tools(filter_json) → execute_tool(filter_json, ...)` reached protected execution but returned:

     ```json
     {
       "state": "execution_error",
       "tool": "filter_json",
       "error": "backend wait for session ready: sandbox/docker: session live-acceptance-session terminated unexpectedly"
     }
     ```

   - This means the live drill did not prove successful execution of a deterministic first-party tool. The A2A execution did succeed, but it does not replace this acceptance requirement.

2. **Catalog-mutation staleness was not exercised**
   - Cross-principal rejection was proven. Expiry and a changed-catalog invalidation path were not exercised against the running daemon.

3. **Studio chat was not exercised**
   - Native Studio-chat preservation remains unproven in the live daemon drill.

4. **`search_tools` phrase matching is limited**
   - Searching `"filter JSON"` returned an empty tool list, while the exact query `"filter_json"` returned the expected entry. This may be acceptable for the current substring matcher but does not meet the intended semantic search quality implied by the API name.

## Operational note

The local daemon’s configured channels attempted to start concurrently with an existing daemon and produced Telegram polling conflicts. This did not affect the completed MCP protocol calls, but the verification daemon was stopped after the drill to avoid leaving a duplicate background service.

## Conclusion

The breaking public-interface migration is live for authentication, four-tool exposure, context issuance, A2A catalog visibility/execution, and token ownership enforcement. It is **not ready to be declared complete** because the required deterministic first-party `execute_tool` success path failed due to a sandbox session lifecycle error, and catalog-staleness plus Studio-chat live checks remain outstanding.
