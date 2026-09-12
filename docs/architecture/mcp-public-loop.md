
## Public inbound MCP tool loop

`/api/mcp` is Astonish's **inbound** MCP server for OAuth-authenticated external
clients. It is separate from the outbound MCP client integrations described in
this document. Outbound servers configured under `mcp_servers` remain deferred
tools for Astonish's native runtime; they are not public MCP methods.

### Breaking contract change

The former public `astonish_chat` tool has been removed with no alias,
compatibility mode, or fallback. `tools/list` exposes exactly four tools:
`get_agent_context`, `search_tools`, `describe_tools`, and `execute_tool`.

At the beginning of every user turn, an external client calls
`get_agent_context(session_id, user_message)`. It receives the authenticated
user's external-loop instructions, relevant visible memory, sorted tenant-scoped
catalog entries (including configured A2A virtual tools), and an opaque context
token. The client then searches that snapshot, requests exact schemas, and
executes one tool at a time while retaining control of its own reasoning loop
and final response.

The returned instructions define tool provenance explicitly. Clients inspect the
Available Skills before concluding that a capability is unavailable. When a skill
matches, they find and invoke Astonish's `skill_lookup` through the progressive
operations, then find and invoke each tool named by the skill the same way. A name
such as `http_request`, `shell_command`, or a browser tool means the tool in the
Astonish catalog, not a similarly named client or host function. Missing local
binaries, environment variables, SDKs, or packages therefore say nothing about
whether the Astonish capability is available.

Credentials cross the protocol only by store name or placeholder. For example,
the OpenStack skill directs the client to invoke Astonish's `http_request` with
`credential="openstack"`; the direct executor resolves that name from the
request's personal-first credential store, substitutes it only for the protected
call, restores arguments afterward, and redacts output before serialization. Raw
credential values are not included in instructions, context tokens, schemas, or
results. There is no `astonish_chat` or native-agent delegation fallback: the
client retains its reasoning loop while Astonish owns tool execution and secrets.

The opaque, short-lived token is server-side state bound to the OAuth principal,
tenant, stable session ID, and runtime snapshot. It contains no secrets or
memories. Calls without a token return `context_required`; expired, cross-
principal, cross-tenant, or unavailable snapshots return `context_stale`.
Normal recoverable results also use typed payloads for invalid arguments,
unavailable tools, authorization denial, approval requirements, and execution
failures rather than malformed-MCP protocol errors.

`execute_tool` delegates to the shared progressive executor. It preserves
OAuth `tool:execute` authorization, disabled-tool policy, request-scoped
credentials, approval wrappers, sandbox-aware runners, redaction, and artifact
handling. Context retrieval exposes existing memory only; this API does not add
external memory writes or a turn-finalization protocol. Tool descriptions guide
clients to obtain context before a final response, but an arbitrary host cannot
be forced to inject or follow those instructions.
