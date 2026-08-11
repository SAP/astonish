# MCP Servers

The Model Context Protocol (MCP) allows Astonish to connect to external tool servers, extending the agent's capabilities beyond its built-in tools.

## What Is MCP?

MCP is an open protocol for AI tool integration. An MCP server exposes tools (functions with schemas) that the agent can discover and invoke at runtime. This enables:

- Third-party tool integrations without modifying the agent
- Team-specific internal tools
- Dynamic tool discovery and versioning

## How MCP Servers Are Managed

MCP servers can come from two places, which are merged together — mirroring how [providers](./providers.md) merge a config-file base with the platform database:

1. **Config file** (`~/.config/astonish/mcp_config.json`) — the **base layer**. Servers declared here are available to Astonish Code (personal mode) with no platform login, and in platform mode they become **defaults visible to every org and team**. This lets you ship an installation pre-configured with standard MCP servers.
2. **Database** — managed through Studio Settings or the CLI, scoped per platform / org / team.

The full **cascade resolution** (later tiers override earlier ones by server name):

```
Config file (mcp_config.json)  →  Platform  →  Organization  →  Team
        (base layer)              (DB base)     (overrides)     (overrides)
```

Each tier can define MCP servers. When names collide, the closest tier to the user wins:

| Tier | Managed By | Scope | Overrides |
|------|-----------|-------|-----------|
| Config file | Operator (edits `mcp_config.json`) | All orgs and teams (defaults) | — (base layer) |
| Platform | Platform admin | All orgs and teams | Config file |
| Organization | Org admin | All teams in the org | Config file + Platform |
| Team | Team admin | Single team | Config file + Org + Platform |

At runtime, Astonish merges every tier by server name — team-level definitions override org-level, which override platform-level, which override the config-file base.

::: tip Config-file servers as defaults
A server declared in `mcp_config.json` shows up in **Settings → MCP Servers** marked as a `config` source. It is a read-only default from the config file; an org or team can override it by installing a same-named database entry, which then wins in the cascade above.
:::

### How MCP Tools Reach the Agent

How a server's tools become callable depends on where you're running Astonish:

- **Astonish Code (`astonish code`)** — MCP servers are **first-class**. Every configured server's tools are loaded onto the agent directly, so it can call any of them immediately with no discovery step. A coding session is personal and usually has only a handful of servers, so this keeps them all one call away.
- **Studio / platform** — MCP tools are **discovered on demand**. Each server appears in the agent's tool catalog as a short summary, and the agent pulls in the specific tools it needs when a task calls for them. This keeps the agent efficient even when an organization exposes thousands of tools across many teams.

Either way you configure servers the same way — only the way the agent reaches their tools differs.


## Managing via Studio Settings

The primary way to manage MCP servers is through **Settings → MCP Servers** in the Studio UI:

- **Add servers** manually or browse the MCP Store for community servers
- **Enable/disable** servers with a toggle (without removing the configuration)
- **Test connections** with the built-in MCP Inspector
- **View discovered tools** provided by each server
- **Refresh** tool definitions from connected servers
- **Switch scope** (Team / Org / Platform) to manage servers at the appropriate tier

The UI provides two editing modes:
- **Editor** — Card-based GUI with per-server forms
- **Source** — Raw JSON editing for bulk configuration

### Standard Web Servers

Studio Settings has two related controls for web search:

1. **MCP Servers → Web Search Providers** — install/configure providers (API keys for Tavily/Brave/Firecrawl, or provider+model for Perplexity/Sonar). Multiple providers can be configured at once.
2. **General → Web Tools** — choose **which** installed provider the agent should use for web search and extraction. Only the selected tool is exposed to chat.

- **Configured** — credentials (or Perplexity provider/model) are stored; available to select in General.
- **Setup** — not configured yet; opens a dialog for API keys or Perplexity model selection.

Supported options include Tavily, Brave Search, Firecrawl, and **Perplexity / Sonar** as a model-backed option: instead of a separate web-search API key, choose an already configured model provider (for example SAP AI Core) and a model whose ID contains `perplexity`, `sonar`, or `pplx`. When selected in General, Astonish exposes `perplexity_web_search`; the selected Sonar model performs the search and returns sourced data to the main chat model.

## Transport Types

### stdio

The server runs as a child process. Astonish communicates via stdin/stdout. Best for local tools.

```json
{
  "name": "filesystem",
  "command": "npx",
  "args": ["-y", "@modelcontextprotocol/server-filesystem", "/home/user/projects"],
  "transport": "stdio"
}
```

::: warning Stdio + Sandbox
Stdio-based MCP servers require the sandbox to be enabled, since the child process runs inside the sandbox container. The Studio **Test connection** action follows the same rule: it creates a disposable sandbox session/pod for stdio servers instead of running the command on the Astonish host.
:::

### SSE (Server-Sent Events)

The server is a remote HTTP endpoint using the SSE transport. Best for shared team servers.

```json
{
  "name": "remote-tools",
  "url": "https://mcp.internal.company.com/sse",
  "transport": "sse"
}
```

### Streamable HTTP

A newer HTTP-based transport for network MCP servers:

```json
{
  "name": "remote-tools",
  "url": "https://mcp.internal.company.com/mcp",
  "transport": "streamable-http"
}
```

## Server Configuration Fields

Each MCP server entry supports these fields:

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | Yes | Unique identifier for the server |
| `command` | string | For stdio | Path or command to execute |
| `args` | string[] | No | Arguments passed to the command |
| `env` | map | No | Environment variables for the process |
| `url` | string | For SSE/HTTP | Remote server endpoint URL |
| `transport` | string | Yes | `stdio`, `sse`, or `streamable-http` |
| `enabled` | boolean | No | Whether the server is active (default: true) |

## Managing via CLI

MCP servers can also be managed through the `astonish tools` command:

```bash
# List all available tools (built-in + MCP)
astonish tools list

# List MCP servers with their enabled/disabled status
astonish tools servers

# Enable or disable a specific server
astonish tools enable <name>
astonish tools disable <name>

# Refresh tool cache (reconnects and re-discovers tools)
astonish tools refresh

# Browse and install from the MCP server store
astonish tools store

# Open MCP config in your editor
astonish tools edit
```

### Store Sub-commands

The `tools store` command provides access to community MCP servers:

```bash
# List available servers in the store
astonish tools store list

# Interactive installer
astonish tools store install
```

## 3-Tier Resolution Example

Consider a scenario where MCP servers are defined at multiple tiers:

**Config file** (`mcp_config.json`, set by the operator):
- `github` — default GitHub MCP server shipped with the installation
- `internal-docs` — a keyless internal docs server

**Platform level** (set by platform admin):
- `github` — Override with a platform-managed GitHub token
- `slack` — Slack integration

**Org level** (set by org admin):
- `github` — Override with org-specific GitHub token
- `jira` — Org-wide Jira integration

**Team level** (set by team admin):
- `github` — Override with team-specific repo access
- `figma` — Team-specific design tool

**Effective result for the team:**
- `github` → Team definition wins (most specific)
- `slack` → Inherited from Platform (no override)
- `jira` → Inherited from Org (no team override)
- `figma` → Team-specific (only exists at team level)
- `internal-docs` → Inherited from the config-file base (no DB override at any tier)

## Debugging startup failures

When an MCP server fails during discovery or first tool use, check the daemon logs:

```bash
# Local service/default mode
astonish daemon logs -f

# Kubernetes
kubectl logs -n astonish -l app.kubernetes.io/component=api --tail=100
kubectl logs -n astonish -l app.kubernetes.io/component=worker --tail=100
```

Failure logs include the server name, transport, stdio command and args, remote URL scheme/host/path, env var names, the underlying error, captured stderr, and non-JSON stdout that was discarded from the MCP protocol stream. This makes `initialize: EOF` failures actionable when npm/node/package-manager output was printed to stdout instead of stderr. Secret env values, URL credentials, URL query strings, and JSON-RPC protocol messages are not printed.

### Network authorization for stdio servers

Stdio servers often download their package the first time they start. For example, `npx -y @upstash/context7-mcp` needs access to `registry.npmjs.org:443`. In sandboxed cloud deployments, that egress may be blocked by network policy.

When **Test connection** detects a network-looking MCP startup failure, Studio shows an **Outbound network access is required** prompt with the host and port that appear to be needed. Click **Grant access and retry** to save a durable allow rule for the selected MCP scope, then retry discovery in a fresh sandbox. The approval is explicit; Astonish does not automatically whitelist hosts.

The same Settings network policy rules are also applied to background MCP discovery jobs started by install, refresh, Standard Web Servers, and MCP Store installs. Those jobs run after the HTTP response, but Astonish carries a detached runtime policy context so the disposable `mcp-discover-*` sandbox receives the saved OpenShell allow rules before `npx`, `uvx`, or another package manager starts.

If no host can be parsed from the process output, Astonish may still show package-manager hints such as npm or PyPI registry endpoints so an admin can approve the expected dependency source.

## Best Practices

- Use **stdio** transport for development and local tools
- Use **SSE** or **streamable-http** transport for production shared servers
- Define broadly-used servers at the **Platform** or **Org** level to avoid duplication
- Use **Team** level for team-specific tools or to override credentials for shared servers
- Keep sensitive tokens in environment variables or the credential store, not inline in server config
- Use the **enable/disable** toggle to temporarily deactivate servers without losing configuration

See [Tools Overview](../agent/tools/index.md) for how MCP tools integrate with the built-in tool system.
