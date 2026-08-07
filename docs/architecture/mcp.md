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

### Tool Surfacing: First-Class in Code, Discoverable in Platform

How MCP tools reach the model depends on the runtime, because prompt size scales
very differently between a personal coding session and a multi-tenant platform.

- **Astonish Code (`astonish code`)** — MCP servers are **first-class**. Every
  configured server's sanitized toolset is injected directly onto the main
  thread (passed to the ChatAgent as llmagent `Toolsets`), so the coding agent
  can call any MCP tool immediately by its bare name, no `search_tools` detour.
  The system prompt advertises them as always-available and lists them under an
  `## MCP Tools (available directly)` section. Code sessions are single-user and
  usually configure only a handful of servers for coding, so eager injection is
  the right ergonomics/size trade-off.
- **Studio / Platform** — MCP tools stay **discoverable**, not injected. Each
  server is registered only as an `mcp:<server>` tool group, surfaced as a
  one-line catalog entry ("high definition") in the Task Delegation section of
  the system prompt. The model reaches the actual tools via `search_tools`
  (which loads them on demand through the `ToolIndex`) or by delegating to a
  sub-agent with the `mcp:<server>` group. This keeps the prompt small even when
  an org/team exposes thousands of tools.

The switch is a single flag: `ChatFactoryConfig.CodeMode` (set only by
`pkg/launcher/tui_code.go`). It drives two things in `NewWiredChatAgent`
(`pkg/launcher/chat_factory.go`): passing the MCP toolsets to `NewChatAgent`,
and setting `SystemPromptBuilder.MCPFirstClass` so the prompt describes them as
directly callable. `PlatformMode` and `CodeMode` are mutually exclusive; a
non-platform Studio install is *not* Code mode, so it also keeps MCP behind
`search_tools`.

### Config-File MCP Servers as the Platform Base Layer

MCP server resolution mirrors provider resolution: servers declared in the local
config file are the **base layer**, and the platform database cascades on top.

- **Personal mode / Astonish Code**: `config.LoadMCPConfig()` reads
  `~/.config/astonish/mcp_config.json` and merges the standard web/model servers
  (Tavily, Brave, Firecrawl, Perplexity) declared under `web_servers` in
  `config.yaml`. No platform login is required — a config-file MCP server is
  immediately available to the agent.
- **Platform mode**: the two platform-mode loaders —
  `loadMCPConfigForRequest` (`pkg/api/request_helpers.go`) and `loadMCPConfig`
  (`pkg/launcher/chat_factory.go`) — seed the merge map from
  `config.FileMCPServers()` **first** (Tier 0), then overlay the database tiers
  (platform → org → team), then merge standard servers. This lets an operator
  ship an installation with default MCP servers declared in `mcp_config.json`
  that are visible to every org/team, exactly like config-file providers become
  platform defaults.

Cascade order (later tiers override earlier ones by server name):

```
config file (mcp_config.json)   ← FileMCPServers(), the base layer
  → platform DB (MCPServerStore, scope=platform)
    → org DB (MCPServerStore, scope=org)
      → team DB (MCPServerStore, scope=team)
        → standard servers (config.yaml web_servers, layered last)
```

`config.FileMCPServers()` intentionally excludes standard-server IDs — those are
layered separately by `MergeStandardServersWithConfig` using the effective
(DB-resolved) `AppConfig` so team-level `WebSearchTool` selection is honored.

**Tool discovery for file servers.** File-declared servers have no per-tenant
`cached_tools` DB column, so their tool declarations live in the shared on-disk
tools cache (`~/.config/astonish/tools_cache.json`), the same cache standard
servers use. Discovery happens automatically — no manual "refresh" step is
required:

- **Standalone chat build (personal mode)**: when the chat factory
  (`pkg/launcher/chat_factory.go`) finds a config-file server with no cached
  tools, it starts the server on the host, lists its tools, and writes them to
  the file cache (`discoverAndCacheHostMCPTools`). Subsequent launches are
  instant.
- **Local platform install (daemon)**: at startup the daemon calls
  `api.DiscoverUncachedFileMCPServers`, which kicks off background discovery for
  every enabled config-file server that is not yet cached (respecting sandbox /
  network policy via `discoverMCPToolsForPlatform`). This is why config-file MCP
  servers "just work" after `astonish daemon restart`.
- **Manual refresh**: `RefreshMCPServerHandler` also handles a file server that
  is not in any DB store (via `asyncDiscoverAndCacheFileTools`), so Settings →
  MCP → Refresh works for config-file servers too.

`cachedToolsForMCPServer` and the chat factory both fall back to this file cache
when the DB has no `cached_tools`.

**Advertised tool list.** `GetCachedToolsForRequest` (`pkg/api/tools_cache.go`)
builds the tool list the model actually *sees* — it feeds the flow-builder AI
system prompt and `GET /api/tools`. In platform mode it MUST also seed Tier 0
from `config.FileMCPServers()` (reading the file server's tool declarations from
the on-disk tools cache via `cache.GetToolsForServer`, honoring
`config.GetExcludedTools`), with DB tiers overriding by name — otherwise a
config-file server is connectable and shown in Settings but never advertised, so
the agent reports it doesn't exist. This is base-layer parity with the three
other platform-mode surfaces below.

**Platform-mode surfaces that must include the file base layer (Tier 0).** All
four seed the merge from `config.FileMCPServers()` before overlaying the DB
tiers, so the cascade above holds uniformly:

- `loadMCPConfigForRequest` (`pkg/api/request_helpers.go`) — the actual MCP
  connection config for a request.
- `loadMCPConfig` (`pkg/launcher/chat_factory.go`) — the chat factory MCP config.
- `GetCachedToolsForRequest` (`pkg/api/tools_cache.go`) — the advertised tool
  list (system prompt + `GET /api/tools`).
- `GetMCPServersHandler` (`pkg/api/handlers.go`) — the Settings list.

**Listing / UI.** `GetMCPServersHandler` seeds its list from
`config.FileMCPServers()` and marks those entries `source: "config"`. Such
entries are read-only defaults from the config file; an org/team can override a
file server by installing a same-named DB entry, which takes precedence in the
cascade above.

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
- **Configuration**: MCP servers are configured in `config.yaml` or via the Studio UI. Servers declared in the local `mcp_config.json` file are the base layer in both personal and platform mode: in platform mode they are visible to every org/team as defaults and can be overridden by a same-named database entry (config file → platform DB → org DB → team DB → standard servers). See "Config-File MCP Servers as the Platform Base Layer" above.
- **Credentials**: MCP server environment variables can reference secrets from the credential store via `{{CREDENTIAL:name:field}}` placeholders (e.g. `GITHUB_TOKEN: "{{CREDENTIAL:github:token}}"`). Placeholders are stored in config/DB and expanded only when the MCP process starts (host, sandbox, discovery, inspector). The Studio Editor binds env keys via a credential picker / create-secret flow; the Source view shows the raw placeholder form. Blank sensitive env values such as `TOKEN`, `KEY`, `SECRET`, `PASSWORD`, or `AUTH` keys are omitted during install so `NAME=""` does not shadow a real process environment variable.
- **Flows**: Flow MCP dependencies declare which MCP servers a flow requires.
- **API/Studio**: MCP endpoints manage servers, the inspector provides debugging.
