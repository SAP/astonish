# MCP Integration

## Overview

Astonish integrates with the Model Context Protocol (MCP) to extend the agent's capabilities with external tool servers. MCP servers are standalone processes that expose tools via a JSON-RPC protocol over stdio. Astonish manages the lifecycle of these servers, caches their tool definitions, and optionally runs them inside sandbox containers for security.

## Key Design Decisions

### Why MCP

MCP provides a standardized way to extend AI agents with new tools without modifying the agent itself. The ecosystem of MCP servers is growing rapidly, covering GitHub, databases, APIs, file systems, and more. By supporting MCP, Astonish gains access to this ecosystem.

### Why Sandboxed MCP Transport

MCP servers are arbitrary executables that could access the host filesystem, network, and credentials. Running them inside sandbox containers provides:

- **Isolation**: An MCP server can't access host files or network.
- **Reproducibility**: The same container environment across machines.
- **Security**: Even a malicious MCP server is contained.

The `ContainerMCPTransport` implements the MCP SDK's `Transport` interface by starting the server process inside a container via `ExecNonInteractive` and bridging stdin/stdout to the MCP JSON-RPC connection.

### Why Separate Stderr

MCP uses JSON-RPC over stdout. If an MCP server writes log messages to stdout (a common mistake), it corrupts the JSON-RPC stream. The `ExecNonInteractive` call uses `SeparateStderr: true` to keep stderr separate, and the captured stderr is available for diagnostics.

MCP startup and discovery failures must be logged with enough context to diagnose cloud deployments from container logs alone: server name, transport, command and args for stdio servers, URL scheme/host/path for remote servers, env var names, the underlying error, and captured stderr. The sandbox stdio transport also captures the non-JSON stdout lines it discards before forwarding the stream to the MCP SDK. That keeps JSON-RPC messages private to the protocol while still exposing package-manager or runtime failures that incorrectly print to stdout before an `initialize` EOF. Env var values and URL credentials/query strings are intentionally omitted from logs.

### Why Tool Caching

Querying MCP servers for their tool definitions involves starting the server process, performing the JSON-RPC handshake, and listing tools. This can take seconds. The tool cache:

- Persists tool definitions to disk (`~/.config/astonish/tools_cache.json`).
- Refreshes in the background so the agent always has a warm cache.
- Allows the agent to know what MCP tools are available without waiting for server startup.

### Why a MCP Store

The MCP Store provides a curated catalog of MCP servers with pre-built configurations. Users can browse available servers, install them with one click, and the correct command, args, and environment variables are configured automatically. The store data is embedded in the binary as JSON.

## Architecture

### MCP Server Lifecycle

```
Configuration (config.yaml or Studio):
  mcp_servers:
    github:
      command: npx
      args: ["-y", "@modelcontextprotocol/server-github"]
      env:
        GITHUB_TOKEN: "{{CREDENTIAL:github:token}}"
    |
    v
Daemon startup:
  1. Parse MCP server configs
  2. Load tool cache from disk
  3. For each server: start in background, list tools, update cache
  4. Register tools with the agent via LazyMCPToolset
    |
    v
Tool call from agent:
  1. Agent calls an MCP tool (e.g., "github_create_issue")
  2. LazyMCPToolset starts the MCP server if not running
  3. JSON-RPC call: {"method": "tools/call", "params": {...}}
  4. Response returned to agent
    |
    v
Shutdown:
  - MCP server processes are terminated
```

### Sandboxed MCP Execution

```
Host:
  Agent requests MCP tool call
    |
    v
  LazyNodeClient.EnsureContainerReady() -- Phase 1 only (no node process needed)
    |
    v
Container:
  ContainerMCPTransport.Connect():
    1. ExecNonInteractive(command, args, env, SeparateStderr=true)
    2. Filter stdout so only JSON-RPC lines reach mcp.IOTransport
    3. Capture discarded non-JSON stdout for diagnostics
    4. Return mcp.Connection
    |
    v
  JSON-RPC over stdio:
    Host stdin -> Container process stdin
    Container process stdout -> Host stdout
```

### LazyMCPToolset

The `LazyMCPToolset` defers MCP server startup until tools are actually needed:

- At agent creation time, tool definitions are loaded from cache (fast).
- On first tool call, the MCP server is started and the JSON-RPC connection is established.
- This avoids starting servers that may never be used in a session.

### MCP Inspector

Studio provides an MCP Inspector that allows:

- Viewing all registered MCP servers and their status.
- Listing available tools from each server.
- Testing individual tools with custom inputs.
- Viewing server logs and errors.

For stdio servers, the inspector uses the same sandbox rule as chat and app data: the server process never runs on the Astonish host. Tool listing uses a disposable `mcp-discover-*` sandbox; tool invocation uses the per-user app sandbox. Before a stdio package manager such as `npx` or `uvx` starts, persisted Astonish Network Policy allow rules are pre-seeded into the OpenShell sandbox policy.

The same pre-seeding requirement applies to background MCP discovery after install, refresh, standard-server install, and MCP Store install. Those jobs intentionally detach from the HTTP request context so they can continue after the response, but they must carry a detached runtime context containing the Network Policy stores and OpenShell gateway config. Without that detached runtime context, a Settings allow rule can exist in the database but never reach the disposable `mcp-discover-*` sandbox.

If sandboxed MCP startup fails with network-looking diagnostics, the inspector response includes `network_authorization`. The payload contains parsed denied endpoints from the MCP process output and package-manager preflight hints such as `registry.npmjs.org:443` for npm-based servers. The Studio UI shows these endpoints and lets an admin explicitly approve a durable network policy rule for the selected MCP scope. The retry then creates or reuses a fresh sandbox with the persisted allow rule pre-seeded. Astonish does not silently whitelist hosts.

## Key Files

| File | Purpose |
|---|---|
| `pkg/mcp/manager.go` | MCP server lifecycle management |
| `pkg/sandbox/mcp_transport.go` | ContainerMCPTransport: sandboxed MCP execution |
| `pkg/agent/lazy_mcp_toolset.go` | LazyMCPToolset: deferred MCP server startup |
| `pkg/cache/tools_cache.go` | Persistent tool definition cache |
| `pkg/config/mcp_config.go` | MCP server configuration |
| `pkg/config/standard_servers.go` | Standard MCP server definitions |
| `pkg/mcpstore/` | MCP server catalog with embedded data |
| `pkg/api/mcp_handlers.go` | MCP management API endpoints |
| `pkg/api/mcp_inspector.go` | Studio MCP Inspector tool-list and run handlers |
| `pkg/api/mcp_network_grants.go` | Durable MCP network allow-rule approval endpoint |
| `pkg/api/mcp_diagnostics.go` | Secret-safe MCP diagnostics and network authorization payloads |

## Interactions

- **Agent Engine**: MCP tools are registered alongside built-in tools. The ToolIndex indexes MCP tool names for semantic discovery.
- **Sandbox**: MCP servers can run inside containers via ContainerMCPTransport. The LazyNodeClient provides the container.
- **Configuration**: MCP servers are configured in `config.yaml` or via the Studio UI.
- **Credentials**: MCP server environment variables can reference secrets from the credential store via `{{CREDENTIAL:name:field}}` placeholders (e.g. `GITHUB_TOKEN: "{{CREDENTIAL:github:token}}"`). Placeholders are stored in config/DB and expanded only when the MCP process starts (host, sandbox, discovery, inspector). The Studio Editor binds env keys via a credential picker / create-secret flow; the Source view shows the raw placeholder form. Blank sensitive env values such as `TOKEN`, `KEY`, `SECRET`, `PASSWORD`, or `AUTH` keys are omitted during install so `NAME=""` does not shadow a real process environment variable.
- **Flows**: Flow MCP dependencies declare which MCP servers a flow requires.
- **API/Studio**: MCP endpoints manage servers, the inspector provides debugging.
