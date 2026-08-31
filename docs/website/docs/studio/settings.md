# Settings

The Settings panel is accessible from the gear icon in Studio's navigation. It provides configuration for all Astonish subsystems.

## Settings Sections

### Personal Settings

These sections are available to all users:

| Section | Description |
|---------|-------------|
| **Channels** | Configure communication channels (Telegram, Email, etc.) |
| **Knowledge** | Browse and manage knowledge base files |
| **Credentials** | Manage encrypted secrets (API keys, tokens, passwords) |

### Resource Settings

Manage reusable resources (available in personal/local mode):

| Section | Description |
|---------|-------------|
| **Skills** | CLI tool skills available to the agent |
| **Scheduler** | Scheduled jobs and recurring tasks |
| **Repositories** | Git repositories (taps) for skills and tools |
| **Flow Store** | Browse and manage saved flows |

### System Settings

Admin-level configuration for the platform:

| Section | Description |
|---------|-------------|
| **General** | Core platform settings |
| **Chat** | Chat behavior and defaults |
| **Providers** | AI model provider configuration |
| **Memory** | Semantic memory and embedding settings |
| **MCP Servers** | Model Context Protocol server management |
| **Sessions** | Session management and history |
| **Sub-Agents** | Configure delegated sub-agent behavior |
| **Browser** | Browser automation configuration |
| **Daemon** | Background service monitoring |
| **Sandbox** | Container sandbox settings |

## Providers

Configure AI model providers:

| Setting | Description |
|---------|-------------|
| Provider name | OpenAI, Anthropic, Google, Azure, etc. |
| API key | Authentication credential |
| Base URL | Custom endpoint (for proxies or self-hosted models) |
| Default model | Model to use when none is specified |

Multiple providers can be active simultaneously. Use the **Model** control in Chat (and in an open App's header) to pin a provider/model for that session or app. Team-wide defaults are still set here under Providers.

## MCP Servers

Manage Model Context Protocol servers that extend agent capabilities:

- **Add server** — Specify command, args, and environment variables
- **Enable/disable** — Toggle servers without removing config
- **Tool list** — View tools provided by each server
- **Refresh** — Reconnect and reload tool definitions

## Credentials

The credential store manages sensitive values (API keys, tokens, passwords) used by tools and integrations:

- Credentials are encrypted at rest using [envelope encryption](../security/)
- Add, edit, or remove credentials from this panel
- Reference credentials in flows and tool configs by name

## Browser

Settings for browser automation:

| Setting | Description |
|---------|-------------|
| Headless | Run browser without visible window |
| Navigation timeout | Maximum time for page loads |
| Viewport | Default viewport dimensions |
| Chrome path | Custom Chromium binary path |
| Proxy | HTTP proxy for browser traffic |
| Remote CDP URL | Connect to an existing Chrome DevTools Protocol instance |

## Memory

Configure the semantic memory system for RAG (Retrieval-Augmented Generation):

| Setting | Description |
|---------|-------------|
| Enable Memory | Toggle the memory system on/off |
| Memory Directory | Path where memory markdown files are stored |
| Vector Directory | Path for the vector index |
| Embedding Provider | Provider for generating embeddings (local, OpenAI, Ollama) |
| Chunking | Max characters and overlap for document splitting |
| Search Defaults | Max results and minimum score threshold |
| File Watcher | Auto-index changes to memory files |

## Daemon

Monitor and control the background daemon:

- **HTTP Port** — Port for Studio UI (default: 9393)
- **Log Directory** — Where daemon logs are written
- **Studio Authentication** — Enable/disable login and session TTL

## Sandbox

Configure the container sandbox environment for agent tool execution:

- Backend type (Incus for local, Kubernetes for cloud)
- Resource limits (CPU, memory, processes)
- Network policies — multi-tier allow/deny rules (platform, org, team) controlling which endpoints the sandbox can reach. See [Network Policy](../security/network-policy.md).

## Cloud Deployment: Admin Panels

In cloud deployments, additional panels appear for users with admin roles:

### Team Admin

- Manage team members and roles
- Team-level provider configuration
- Team MCP servers and skills
- Team scheduler and flows
- Import and delete team-owned **Slides Templates**
- Container/sandbox settings per team

### Org Admin

- Manage organization users
- Org-wide provider configuration
- Org-level skills and MCP servers
- Import and delete organization-owned **Slides Templates**
- Audit log viewer

### Platform Admin (Superadmin)

- Manage all organizations
- Platform-wide user management
- Global provider and skill configuration
- Platform MCP servers and channels
- Import and delete platform-owned **Slides Templates**
- Authentication settings
- Sandbox infrastructure management

Shared templates inherit into the Slides catalog according to scope. See [Slides Templates](../slides/templates.md) for user actions, administrator ownership, and PowerPoint import constraints.
