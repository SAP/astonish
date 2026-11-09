# Configuration

## Overview

Astonish uses a YAML-based configuration system with two main config types: the application config (`config.yaml`) for global settings, and agent configs (flow YAML files) for workflow definitions. Configuration is loaded from `~/.config/astonish/` and supports provider credentials, channel settings, sandbox options, scheduler jobs, MCP servers, and more.

## OpenShell retained Bot home configuration

Bot-home storage is **off by default**, independent of ordinary session storage.
The real `LoadAppConfig` → `BackendFromAppConfig` → registered OpenShell factory
path carries these application YAML settings to `openshell.Config`:

```yaml
sandbox:
  backend: openshell
  openshell:
    bot_home_mounts_enabled: true
    bot_home_claim_name: astonish-openshell-bot-homes
```

Both settings are required to use Bot homes; a claim name alone does not opt in.
There are no new environment variables or secrets. Helm emits only these
non-secret settings into the existing application ConfigMap.

For chart-managed storage, set `sandbox.enabled=true`, `sandbox.backend=openshell`
and `sandbox.openshell.botHomes.enabled=true`. The `botHomes` values are:

| Value | Default | Meaning |
| --- | --- | --- |
| `enabled` | `false` | Enable retained Bot home mounts, not the gateway deployment |
| `existingClaim` | `""` | Reuse an operator-managed claim without creating or owning it |
| `pvcName` | `astonish-openshell-bot-homes` | Name of a new retained claim |
| `namespace` | `""` | Defaults to `namespaces.sandbox`; any other namespace is rejected |
| `storageClassName` | `""` | Inherit `sandbox.storage.storageClassName` for new claims |
| `size` | `50Gi` | Positive whole storage quantity for a new claim |
| `accessMode` | `ReadWriteMany` | RWX recommended; RWO requires single-node placement |

The chart validates namespace/name syntax, claim collisions with workspace,
layers, uppers, Kubernetes homes and cert bundles, writable access modes and new
claim storage class/size. `ReadOnlyMany` and `ReadWriteOncePod` are unsupported:
Bot sessions need writes and multiple Bots may share the claim concurrently.
Existing claims must already have suitable access modes and filesystem permissions.
The chart cannot prove a StorageClass supports RWX, that an existing PVC is bound,
or that an external gateway uses the configured sandbox namespace.

New claims carry `helm.sh/resource-policy: keep`, no Sandbox owner reference,
and are not deleted by run cleanup. Keep is not a backup: operators own backup,
retained-claim adoption on reinstall, and immutable access-mode/storage-class
migration. Prefer `existingClaim` to adopt an already retained claim.

Neither `sandbox.openshell.enabled` (gateway subchart), normal session `/sandbox`
volumes, `ephemeralWorkspace`/Kyverno emptyDir behavior, certificate mounts nor the
Kubernetes backend's `sandbox.storage.homes` settings are changed by this opt-in.
Before enabling it, verify the gateway supports scoped writable `volume_mounts`
with `sub_path`, correct scope-directory permissions, and actual data survival
across pod recreation. See [OpenShell backend](openshell-sandbox-backend.md).

## Key Design Decisions

### Why YAML

YAML was chosen over JSON, TOML, or environment variables because:

- **Readability**: Complex nested structures (providers, channels, MCP servers) are easy to read and edit.
- **Comments**: YAML supports comments, which are important for documenting configuration choices.
- **Ecosystem familiarity**: Developers are accustomed to YAML from Kubernetes, Docker Compose, and CI/CD tools.
- **LLM-friendly**: The agent can read and suggest configuration changes in YAML format.

### Why a Centralized Config Directory

All configuration lives under `~/.config/astonish/`:

```
~/.config/astonish/
  config.yaml           # Main application config
  credentials.enc       # Encrypted credential store
  .store_key           # Credential encryption key
  agents/              # Flow YAML files
  flow_registry.json   # Flow index
  sessions/            # Session data
  memory/              # Knowledge files
  skills/              # Skill documents
  logs/                # Daemon logs
```

This provides a single location for all Astonish state, making backup and migration straightforward.

### Daemon Log Destination

The daemon supports two application log destinations: `file` and `stdout`. Local/default mode writes to `~/.config/astonish/logs/daemon.log` so `astonish daemon logs` and service-managed installs keep their existing behavior. Container roles selected with `ASTONISH_MODE=api` or `ASTONISH_MODE=worker` write to stdout by default, which is the cloud invariant for Kubernetes: `kubectl logs` and cluster log collectors must see application logs without reading files inside the container filesystem.

Operators can override the default with `ASTONISH_LOG_DEST=file` or `ASTONISH_LOG_DEST=stdout`. `daemon.log_dir` only affects the `file` destination.

### Why Config-Driven Provider Setup

Provider API keys can come from three sources (in priority order):

1. **Credential store**: The encrypted store is the recommended source.
2. **Config file**: `config.yaml` providers section (legacy, migrated automatically).
3. **Environment variables**: Standard env vars (ANTHROPIC_API_KEY, OPENAI_API_KEY, etc.).

The `provider_env.go` module resolves credentials from the store and sets them as environment variables before any provider adapter is initialized. This means provider adapters don't need to know about the credential store -- they just read their standard env vars.

### First-Run Setup Bootstrap Authority

Before initializing a database through Studio, the operator must set a secret
`ASTONISH_BOOTSTRAP_TOKEN` in the Astonish server process environment and supply
that value in the setup wizard's **Bootstrap Token** password field. Both
`POST /api/platform/init` (PostgreSQL) and `POST /api/platform/init/sqlite`
require the matching `X-Astonish-Bootstrap-Token` request header. This applies
on loopback and to SQLite single-user (`auth_mode: none`) setup too; missing
server configuration or a missing/incorrect header is rejected. The server does
not generate or disclose the token over HTTP.

Studio keeps this token only in setup-component memory, never in browser
storage, URL/query parameters, or the JSON setup body. Initialization is disabled
until a nonblank token is entered. Errors preserve the value for correction and
retry; successful initialization clears it. The API wrappers in
`web/src/api/platform.ts` accept the token as a separate required argument, not
as a database-configuration field. This bootstrap authority does not replace
normal post-setup authentication.

SQLite builtin setup creates an active superadmin, an active organization with
that administrator as owner, and a General team with that administrator as a
team admin. After the required restart, Studio loads the persisted SQLite
configuration with platform authentication and tenant middleware; the operator
logs in using the setup email/password. The bootstrap token is not a substitute
for that login or for authorization to protected settings.

SQLite setup creates missing nested data directories after bootstrap authorization
and request validation. Builtin setup prepares its encryption key before database
creation/seeding: it uses `ASTONISH_MASTER_KEY` when set, otherwise preserves an
existing valid hex-encoded `.store_key`, or creates a private 0600 temporary key
file in the config directory. Key generation, read, or persistence failures
return a bounded setup error instead of reporting success. Invalid existing key
material requires operator repair; setup never rotates or overwrites it. New keys
must be fully written, synced, and closed before atomic no-replace publication
via a same-directory hard link. A competing final key is read and validated, not
overwritten. This requires filesystem hard-link support (including on Windows);
unsupported publication fails closed, with no rename-overwrite fallback.
Write/sync/close failures cannot expose a provisional final key. Cleanup attempts
to remove only the private temporary name, never the final key or a concurrent
winner; cleanup is best-effort if removal also fails. Key failures precede
database creation and configuration/JWT persistence. The parent directory is not
fsynced: publication is atomic for concurrent readers, but does not guarantee
that the directory entry survives a machine/power failure or make setup a
crash-atomic transaction. A crash may also leave an owner-only temporary file.
`TestPrepareSQLiteSetupMasterKey_ConcurrentAdoption`, `PublicationConflict`, and
`ConcurrentCreators` (the latter two with the same test-name prefix) cover
provisional-key isolation, winner validation, and competing publication.
`TestSQLitePlatformInitHandler_KeyFileOperationFailures` deterministically injects
all four failures through a private per-call file-operation adapter, retaining
real exclusive `os.OpenFile` creation and checking bounded HTTP errors, cleanup,
and unchanged existing configuration/JWT values. Existing valid keys bypass this
adapter and remain untouched. Legacy
binary-key migration remains outside this setup path. Single-user setup retains
its existing key lifecycle. SQLite setup failure responses identify the failed
operation with a fixed message, never raw database/filesystem errors or echoed
request values. `TestSQLitePlatformInitHandler_SanitizesStorageFailures` injects
local filesystem/database failures; request-reader failures and the remaining
error branches also have regression coverage. A sanitized failure does not
imply rollback of earlier successful provisioning steps.

`TestE2E_SecurityUrgentFirstRunSetup` exercises both SQLite auth modes using only
temporary config/data paths that must both be absent before requests; the harness
must not pre-create them. Unauthorized requests leave them absent, and authorized
setup creates them. Its builtin restart case shuts down the
setup server, reopens the saved databases in an auth-wired Studio server, performs
a fresh password login, verifies the resolved org/team identity, and checks that
protected settings reject anonymous requests but accept the administrator.

## Architecture

### Application Config Structure

```yaml
# Provider configuration
provider: anthropic
model: claude-sonnet-4-20250514

# Alternative providers
providers:
  anthropic:
    api_key: ""          # Migrated to credential store
  openai:
    api_key: ""
  google:
    api_key: ""

# Sandbox settings
sandbox:
  enabled: true
  limits:
    memory: "2GB"
    cpu: 2
    processes: 500
  network: bridged
  prune:
    orphan_check_hours: 6
    idle_timeout_minutes: 10

# Channel configuration
channels:
  telegram:
    bot_token: ""
    allow_from: [123456789]
  email:
    imap_host: "imap.gmail.com"
    smtp_host: "smtp.gmail.com"
    username: ""
    password: ""
    allow_from: ["user@example.com"]

# MCP servers
mcp_servers:
  github:
    command: npx
    args: ["-y", "@modelcontextprotocol/server-github"]
    env:
      GITHUB_TOKEN: "{{CREDENTIAL:github:token}}"

# Scheduler
scheduler:
  jobs:
    daily_report:
      cron: "0 9 * * *"
      mode: adaptive
      instruction: "Generate the daily status report"

# Memory
memory:
  embedding:
    provider: local            # local, openai, ollama, openai-compat
    timeout_seconds: 30        # Remote embedding HTTP deadline; nonzero by default

# Chat runtime deadlines
chat:
  pre_provider_retrieval_timeout_seconds: 10

# Session management
sessions:
  expiry_hours: 720            # 30 days

# Agent behavior
custom_prompt: ""              # Additional system prompt text
auto_approve: false            # Skip tool approval in chat mode
debug_mode: false

# Agent identity (for web portal interactions)
identity:
  name: "Astonish Agent"
  username: "astonish"
  email: "agent@example.com"
```

### Config Loading

```
Daemon startup:
  |
  v
LoadAppConfig(configDir):
  1. Read config.yaml
  2. Parse YAML into AppConfig struct
  3. Resolve accessor defaults for unset values
  4. Validate settings (sandbox limits, network mode, etc.)
    |
    v
Provider setup:
  1. Resolve API keys from credential store
  2. Set environment variables (ANTHROPIC_API_KEY, etc.)
    |
    v
Config available to all subsystems
```

### Provider Settings API Secret Boundary

Provider responses mask registered secret fields on independent map copies,
including legacy providers whose instance name supplies an omitted `type`.
Secret masks contain at most eight bytes (`****` plus the last four bytes).

Platform/org provider replacements and team provider patches/replacements accept
only the exact mask of a nonempty field in the same instance of the authorized
writable settings store. Stale masks, nonexistent instances, and missing/empty
fields return HTTP 400 before saving; inherited secrets are not copied into a
new writable instance. A failed platform/org settings read aborts the save.

Omitted or `null` provider maps remain untouched. Platform/org maps replace the
provider set, so `{}` deletes all providers and omitted fields are deleted.
Team ordinary updates patch existing maps; the `__replace_all__` form replaces
the provider set. An explicit empty field clears its value in either form.

### Full Configuration API Secret Boundary

`GET /api/settings/full` masks secrets in response copies, never in the trusted
runtime configuration. Browser `proxy` and `remote_cdp_url` retain the URL
scheme, host, port, and path but replace userinfo, every nonempty query value
(including repeated and vendor-specific parameters), and fragments with `****`.
Malformed or opaque URLs are masked as a whole rather than exposed through a
parser error. The browser handoff VNC password is also masked. URL paths and
query parameter names remain visible; operators must not put credentials there.

`PUT /api/settings/full` requires platform superadmin authority in platform
mode, including on loopback, before decoding the body. Trusted non-platform
local mode retains its existing access. JSON section names and scalar value
formats are unchanged; URL and embedding credential fields distinguish omitted
(or `null`) from an explicit empty string:

- Omitted credentials retain the stored value; `""` explicitly clears it.
- An exact masked browser URL returned by GET restores only that same stored
  field, byte-for-byte, preserving signed URL encodings and repeated parameters.
  Missing or stale placeholders fail with HTTP 400 before config is saved.
- Editing a masked URL's destination or any other component is rejected; submit
  a complete unmasked replacement URL (or clear it) instead. This deliberately
  prevents moving retained credentials to a different scheme, host, port, path,
  or query. Partial replacement of masked URL components is not supported.
- Embedding API key placeholders must match an existing stored secret. Provider
  and base URL omissions retain the existing destination. Changing either while
  retaining a key requires an explicit new key or explicit clear; preserved keys
  are never silently forwarded to a different destination.

These protections cover the API view and updates, not encryption of browser
URLs in `config.yaml`. Channels remain ignored by this PUT endpoint and keep
their dedicated administration path.

### MCP Server Config

MCP servers have their own configuration section with a consistent structure:

```yaml
mcp_servers:
  server_name:
    command: "executable"
    args: ["arg1", "arg2"]
    env:
      KEY: "value"
    sandbox: true              # Run inside container (optional)
```

The `standard_servers.go` file defines well-known MCP servers with their standard configurations, used by the MCP Store for one-click installation.

### Agent Config (Flow YAML)

Flow definitions use a separate schema defined in `yaml_loader.go`:

```yaml
description: "Flow description"
type: ""                       # "", "drill", "drill_suite"
template: ""                   # Sandbox template (optional)
nodes:
  - name: step_name
    type: llm | tool | input
    prompt: "..."
    tools: true
    output_model:
      key: "description"
flow:
  - from: START
    to: step_name
  - from: step_name
    to: END
```

See the [Flow System](flows.md) document for full details.

## Key Files

| File | Purpose |
|---|---|
| `pkg/config/app_config.go` | AppConfig struct, LoadAppConfig, defaults, validation |
| `pkg/config/yaml_loader.go` | AgentConfig struct (flows), Node, FlowItem, Edge definitions |
| `pkg/config/mcp_config.go` | MCP server configuration parsing |
| `pkg/config/standard_servers.go` | Well-known MCP server definitions |
| `pkg/config/provider_env.go` | Provider credential resolution and env var setup |

## Interactions

- **Daemon**: Loads config at startup. Hot-reloads channel changes.
- **Credentials**: Provider keys resolved from credential store. MCP env vars can reference credentials via `{{CREDENTIAL:name:field}}` (resolved at MCP process start).
- **Agent Engine**: Custom prompt, auto_approve, debug_mode, identity all feed into the agent.
- **Sandbox**: Sandbox section configures container limits, networking, pruning.
- **Channels**: Channel section configures Telegram and email adapters.
- **Scheduler**: Jobs section defines scheduled tasks.
- **MCP**: MCP servers section configures external tool servers.
- **Memory**: Embedding provider choice configured here.
