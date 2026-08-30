# Config Reference

Astonish always runs in platform mode, whether backed by SQLite (lightweight local) or PostgreSQL (scalable cloud). Configuration is split between two locations:

- **`config.yaml`** — System-level infrastructure settings (daemon, browser, sandbox, sessions, memory, etc.) shared across all tenants. In Kubernetes, this is rendered from the Helm ConfigMap.
- **Database** — Tenant-specific settings (providers, models, tools, credentials) managed through Studio Settings with a 3-tier cascade (Platform → Org → Team).

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│  Studio Settings UI                                         │
│                                                             │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────────┐  │
│  │  Providers   │  │  MCP Servers │  │  Team Settings   │  │
│  │  (per-tier)  │  │  (per-tier)  │  │  (web tools,etc) │  │
│  └──────┬───────┘  └──────┬───────┘  └────────┬─────────┘  │
│         │                  │                   │            │
│         ▼                  ▼                   ▼            │
│  ┌─────────────────────────────────────────────────────┐    │
│  │           Database (SQLite or PostgreSQL)            │    │
│  │  Platform Settings → Org Settings → Team Settings   │    │
│  └─────────────────────────────────────────────────────┘    │
│                                                             │
│  ┌─────────────────────────────────────────────────────┐    │
│  │  config.yaml (system-level, shared infrastructure)  │    │
│  │  daemon, browser, sandbox, sessions, memory, etc.   │    │
│  └─────────────────────────────────────────────────────┘    │
└─────────────────────────────────────────────────────────────┘
```

## Database-Managed Settings (3-Tier Cascade)

These settings are managed through Studio Settings and stored in the database. They cascade with increasing priority:

```
Platform → Org → Team
(base)            (highest priority)
```

| Tier | Managed By | Scope |
|------|-----------|-------|
| **Platform** | Platform superadmin | All orgs and teams |
| **Org** | Org admin/owner | All teams in the organization |
| **Team** | Team admin/owner | Single team |

### What's in the Database

| Setting | Cascade Tiers | Managed In |
|---------|--------------|------------|
| AI Providers (API keys, endpoints, types) | Platform → Org → Team | Settings → Providers |
| Default provider and model | Platform → Org → Team | Settings → Providers |
| Web search/extract tools | Team | Settings → General |
| Context length | Team | Settings → General |
| Web server configs (Tavily, Brave, etc.) | Team | Settings → General |
| Memory embedding provider/model | Team | Settings → Memory |
| Memory entries and embeddings | Personal / Team / Org | Stored per-scope in DB |
| Chat sessions and history | Personal / Team | Stored per-scope in DB |
| Sandbox template name | Team | Settings → Sandbox |
| Disabled tools | Team | Settings → General |
| MCP servers | Platform → Org → Team | Settings → MCP Servers |
| Credentials (API keys, tokens) | Team (encrypted) | Settings → Credentials |
| Channel configs (Telegram, Email, Slack) | Platform | Settings → Channels |

Providers are merged additively across tiers — each tier can add new providers or override existing ones by name. Default provider/model override from the closest tier that sets them.

## `config.yaml` — System-Level Settings

These settings live in `~/.config/astonish/config.yaml` and are shared across all tenants on the same instance. In Kubernetes, they come from the Helm ConfigMap.

Changing these settings requires org admin or platform admin privileges in cloud deployments.

```yaml
# Storage backend (determines database engine)
storage:
  backend: "sqlite"            # sqlite | postgres
  sqlite:
    data_dir: ""               # Default: ~/.local/share/astonish
  postgres:
    platform_dsn: ""           # PostgreSQL connection string
    instance_suffix: ""        # Database name suffix
    max_open_conns: 25
    max_idle_conns: 5
    conn_max_lifetime_minutes: 30
  auth:
    mode: "builtin"            # builtin | oidc
    jwt_secret: ""
    access_token_ttl_minutes: 15
    refresh_token_ttl_days: 90
    allow_registration: true
    require_email_verification: true
    default_org_name: "Default Organization"
    default_org_slug: "default"
    oidc:
      issuer_url: ""
      client_id: ""
      client_secret: ""
      redirect_url: ""
      scopes: ["openid", "profile", "email"]

# Daemon (Studio web server)
daemon:
  port: 9393
  log_dir: ""                  # Default: ~/.config/astonish/logs/ when logging to files
  # Log destination is selected by ASTONISH_LOG_DEST=file|stdout.
  # Default: file in local/default mode, stdout in ASTONISH_MODE=api|worker.
  auth:
    disabled: false
    session_ttl_days: 90

# Chat behavior
chat:
  system_prompt: ""            # Custom system prompt text
  max_tool_calls: 0            # Unused (the agent loop no longer pauses at N tool calls)
  max_tools: 0                 # Max tools exposed to model (0 = all)
  auto_approve: false          # Auto-approve tool executions
  flow_save_dir: ""            # Where distilled flows are saved
  pre_provider_retrieval_timeout_seconds: 10  # Memory/tool retrieval deadline before provider calls

# Session management
# Note: In daemon mode, sessions are stored in the database.
# These settings only affect the CLI console mode.
sessions:
  compaction:
    enabled: true
    threshold: 0.8             # Context usage threshold to trigger compaction
    preserve_recent: 4         # Number of recent messages to preserve
  cleanup:
    max_age_days: 5            # Auto-delete sessions older than this

# Semantic memory
# Note: Memory content (entries, embeddings) is stored in the database.
# These settings control the embedding engine and search behavior.
memory:
  enabled: true
  embedding:
    provider: "auto"           # auto | openai | ollama | openai-compat
    model: ""
    base_url: ""
    api_key: ""
    timeout_seconds: 30        # Remote HTTP deadline; timeout is an error, with no retry/fallback
  chunking:
    max_chars: 1600
    overlap: 320
  search:
    max_results: 6
    min_score: 0.35

# Browser automation
browser:
  headless: false
  viewport_width: 1920
  viewport_height: 1080
  no_sandbox: null
  chrome_path: ""              # Custom Chromium binary
  user_data_dir: ""
  navigation_timeout: 30       # Seconds
  proxy: ""
  remote_cdp_url: ""           # Connect to existing Chrome instance
  fingerprint_seed: ""
  fingerprint_platform: ""
  handoff_bind_address: "127.0.0.1"
  handoff_port: 9222

# Sub-agent delegation
sub_agents:
  enabled: true
  max_depth: 2                 # Maximum delegation depth
  max_concurrent: 5            # Max parallel sub-agents
  task_timeout_sec: 300        # Per-task timeout

# Skills system
skills:
  enabled: true
  user_dir: ""                 # Custom skills directory
  extra_dirs: []               # Additional skill directories
  allowlist: []                # Restrict to specific skills

# Agent identity (for web registrations)
agent_identity:
  name: ""
  username: ""
  email: ""
  bio: ""

# Job scheduler
scheduler:
  enabled: true

# Container sandbox
sandbox:
  enabled: false
  privileged: false
  backend: "incus"             # incus | k8s | openshell
  network: ""
  limits:
    memory: "4GB"
    cpu: 2
    processes: 500
    requests:
      cpu_millis: 100
      memory_mib: 256
  prune:
    orphan_check_hours: 0
    idle_timeout_minutes: 10
  kubernetes:                  # Only for backend: k8s
    namespace: "astonish-sandboxes"
    sandbox_image: "ghcr.io/sap/astonish-sandbox-base:latest"
    overlay_mode: "fuse"       # fuse | kernel | auto
    layers_pvc_name: "astonish-layers"
    uppers_pvc_name: "astonish-uppers"
  openshell:                   # Only for backend: openshell
    gateway_addr: ""
    gateway_tls: true
    sandbox_image: "ghcr.io/sap/astonish-sandbox-openshell:latest"
    network_policy:
      presets: ["default"]
      extra_endpoints: []

# Security
security:
  secret_scanner:
    enabled: false  # opt-in; set to true to enable proactive secret detection
    entropy_threshold: 4.0
    min_token_length: 16
```

## Kubernetes: Helm ConfigMap

In Kubernetes deployments, the Helm chart renders only the infrastructure settings into the ConfigMap. Provider and tenant settings are managed via Studio Settings (stored in the database):

```yaml
# What the Helm chart renders into config.yaml
storage:
  backend: "postgres"
  postgres:
    instance_suffix: ""
    max_open_conns: 25
    max_idle_conns: 5
    conn_max_lifetime_minutes: 30
  auth:
    mode: "builtin"
    access_token_ttl_minutes: 15
    refresh_token_ttl_days: 90

sandbox:
  backend: "k8s"               # or "openshell"
  limits:
    memory: "2GB"
    cpu: 2
    processes: 500
  kubernetes:
    namespace: "astonish-sandbox"
    sandbox_image: "ghcr.io/sap/astonish-sandbox-base:latest"
    overlay_mode: "fuse"
```

Secrets (master key, JWT secret, platform DSN) are injected as environment variables from Kubernetes Secrets — never in the ConfigMap.

## Environment Variable Substitution

Any value in `config.yaml` can reference environment variables using `${VAR_NAME}` syntax:

```yaml
storage:
  postgres:
    platform_dsn: "${ASTONISH_PLATFORM_DSN}"
```

## Initial Setup

The `astonish setup` wizard creates the initial configuration:

```bash
astonish setup
```

This walks you through selecting a storage backend (SQLite or PostgreSQL), configuring your first AI provider, and bootstrapping the platform database with an initial organization and admin user.

## Managing Settings

| What | Where to Configure |
|------|-------------------|
| Providers, models, API keys | Studio Settings → Providers (stored in DB) |
| MCP servers | Studio Settings → MCP Servers (stored in DB) |
| Credentials | Studio Settings → Credentials (encrypted in DB) |
| Browser, daemon, sandbox | Studio Settings → System sections (writes to config.yaml, requires admin) |
| Infrastructure (storage, auth) | Helm values (Kubernetes) or `astonish setup` (local) |

See [Providers](./providers.md) for supported AI backends and [MCP Servers](./mcp-servers.md) for tool extension.
