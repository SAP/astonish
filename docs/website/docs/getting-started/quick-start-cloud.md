# Quick Start: Cloud

The cloud deployment uses PostgreSQL with pgvector, enabling full multi-tenant features: three-tier memory, envelope encryption, database-per-org isolation, and team collaboration.

## Prerequisites

- Astonish binary [installed](./installation.md)
- PostgreSQL 15+ with the `pgvector` extension enabled
- A database user with CREATE DATABASE permissions

## 1. Run the Setup Wizard

Run the interactive setup wizard and select PostgreSQL as the backend:

```bash
astonish setup
```

The wizard walks you through:

1. **Deployment mode** — Choose PostgreSQL for cloud/multi-tenant deployment.
2. **Database connection** — Enter your PostgreSQL DSN (e.g., `postgres://user:password@host:5432/astonish?sslmode=require`).
3. **Organization** — Create your organization name and slug.
4. **AI provider** — Select a provider and enter your API key.

The wizard automatically initializes the platform database, sets up the encryption key hierarchy, and creates the first organization.

::: tip Environment Variable
You can also set the DSN via the `ASTONISH_PLATFORM_DSN` environment variable or `platform_dsn` in the YAML config file. This is useful for Kubernetes deployments where secrets are injected via environment.
:::

## 2. Start the Daemon

Start the platform:

```bash
astonish daemon install
astonish daemon start
```

Studio is now available at `http://localhost:9393` (or your configured port).

## 3. Log In

Open Studio at `http://localhost:9393` and log in with the admin credentials created during setup. The first admin user has full organization owner access.

## 4. Invite Team Members

Invite users via the CLI. Specify the org, assign roles, and optionally assign to a team:

```bash
astonish platform org invite --org engineering --email alice@company.com --role admin
astonish platform org invite --org engineering --email bob@company.com --team backend
astonish platform org invite --org engineering --email carol@company.com
```

Roles: `owner`, `admin`, `member` (default: `member`). Users are added to the `general` team by default unless `--team` is specified.

## 5. Team Members Connect

Invited users install the Astonish binary and log in to your platform:

```bash
astonish login https://your-astonish-server.com
```

This authenticates via password or OIDC/SSO (if configured), selects the org and team, and stores credentials locally. After login:

```bash
astonish chat          # Chat through the platform
astonish flows list    # Browse team flows
astonish status        # Show connection info
```

Studio is also available at your server's URL for browser-based access.

## Configure OIDC/SSO (Optional)

For enterprise environments, connect your identity provider by configuring OIDC settings in the platform configuration file:

```yaml
# In your Astonish config (e.g., ~/.config/astonish/config.yaml)
auth:
  oidc:
    issuer: https://accounts.google.com
    client_id: YOUR_CLIENT_ID
    client_secret: YOUR_CLIENT_SECRET
```

OIDC group claims auto-map to Astonish team memberships. Users authenticate through your existing SSO flow.

## What Cloud Deployment Adds

| Capability | Description |
|-----------|-------------|
| Three-tier memory | Personal + team + org knowledge with hybrid search |
| Cascading config | Provider defaults, MCP servers, skills flow from org → team → user |
| Envelope encryption | Per-org data encryption keys (AES-256-GCM) |
| Audit logging | Immutable, team-scoped audit trail |
| Sandboxes | Per-org network-isolated execution (Kubernetes or OpenShell) |
| Channels | Telegram, Email, Slack with team-scoped routing |
| Remote CLI | Team members connect from anywhere |

## Next Steps

- [Choose Your Interface](./choose-your-interface.md) — Studio, CLI, Remote CLI, channels
- [Architecture](./architecture.md) — Understand the layer model
