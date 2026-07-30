# MCP Servers

The Model Context Protocol (MCP) allows Astonish to connect to external tool servers, extending the agent's capabilities beyond its built-in tools.

## What Is MCP?

MCP is an open protocol for AI tool integration. An MCP server exposes tools (functions with schemas) that the agent can discover and invoke at runtime. This enables:

- Third-party tool integrations without modifying the agent
- Team-specific internal tools
- Dynamic tool discovery and versioning

## How MCP Servers Are Managed

MCP servers are stored in the **database** and managed through Studio Settings or the CLI. They follow the same **3-tier cascade resolution** as providers:

```
Platform (base) → Organization (overrides) → Team (overrides)
```

Each tier can define MCP servers. When names collide, the closest tier to the user wins:

| Tier | Managed By | Scope | Overrides |
|------|-----------|-------|-----------|
| Platform | Platform admin | All orgs and teams | — (base layer) |
| Organization | Org admin | All teams in the org | Platform |
| Team | Team admin | Single team | Org + Platform |

At runtime, Astonish merges all three tiers by server name — team-level definitions override org-level, which override platform-level.

::: tip No Personal Level
Unlike some other settings, MCP servers do not have a personal/user tier. They are always managed at the team level or above.
:::

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

Studio Settings also shows a "Standard Web Servers" section with one-click install for popular MCP servers (Tavily, Brave Search, etc.).

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
Stdio-based MCP servers require the sandbox to be enabled, since the child process runs inside the sandbox container.
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

**Platform level** (set by platform admin):
- `github` — GitHub MCP server for all users
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

## Debugging startup failures

When an MCP server fails during discovery or first tool use, check the daemon logs:

```bash
# Local service/default mode
astonish daemon logs -f

# Kubernetes
kubectl logs -n astonish -l app.kubernetes.io/component=api --tail=100
kubectl logs -n astonish -l app.kubernetes.io/component=worker --tail=100
```

Failure logs include the server name, transport, stdio command and args, remote URL scheme/host/path, env var names, the underlying error, and captured stderr. Secret env values and URL credentials/query strings are not printed.

## Best Practices

- Use **stdio** transport for development and local tools
- Use **SSE** or **streamable-http** transport for production shared servers
- Define broadly-used servers at the **Platform** or **Org** level to avoid duplication
- Use **Team** level for team-specific tools or to override credentials for shared servers
- Keep sensitive tokens in environment variables or the credential store, not inline in server config
- Use the **enable/disable** toggle to temporarily deactivate servers without losing configuration

See [Tools Overview](../agent/tools/index.md) for how MCP tools integrate with the built-in tool system.
