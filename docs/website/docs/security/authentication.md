# Authentication

Astonish uses a layered authentication architecture. **Upstream authentication** proves who a human is (built-in email/password, OIDC federation). **Downstream authorization** grants applications and workloads constrained access to Astonish through a built-in OAuth 2.0 authorization server that issues Astonish-signed tokens.

## Built-in Authentication

Built-in auth uses bcrypt-hashed passwords with JWT bearer tokens for the Studio web UI:

| Token Type    | Lifetime | Storage        | Algorithm   |
|---------------|----------|----------------|-------------|
| Access token  | 15 min   | Authorization header | HMAC-SHA256 |
| Refresh token | 90 days  | HttpOnly cookie | HMAC-SHA256 |

Access tokens are short-lived to limit blast radius. Refresh tokens are stored as `HttpOnly`, `Secure`, `SameSite=Strict` cookies — inaccessible to client-side JavaScript.

### JWT Claims Structure

```json
{
  "sub": "user-uuid",
  "org": "org-uuid",
  "teams": ["backend", "infra"],
  "role": "member",
  "iat": 1718900000,
  "exp": 1718900900
}
```

The `teams` array drives authorization decisions across the platform — sandbox access, credential resolution, and audit filtering all reference these claims.

## OIDC Federation

For enterprises with existing identity providers, Astonish federates upstream authentication via **Authorization Code + PKCE**. Supported providers include:

- SAP Identity Authentication Service (IAS)
- Microsoft Entra ID (Azure AD)
- Okta
- Any standards-compliant OIDC provider

### Configuration

OIDC providers are configured per-organization in the platform database. Each provider stores:

| Field | Description |
|-------|-------------|
| `issuer_url` | OIDC issuer URL |
| `discovery_url` | OpenID Connect discovery endpoint |
| `client_id` | OAuth2 client ID |
| `client_secret` | OAuth2 client secret (if required) |
| `scopes` | Requested scopes (default: `openid, email, profile`) |
| `team_claim` | JWT claim to map to team memberships |

Multiple OIDC providers can be configured per organization — users choose their provider during login.

### Team Auto-Mapping

When the OIDC provider includes a groups/teams claim in the ID token, Astonish can automatically map those groups to internal team memberships using the `team_claim` field.

On each login, team memberships are reconciled — users are added to teams matching their group claims. This keeps access in sync with your identity provider without manual administration.

## Built-in OAuth Authorization Server

Astonish includes a built-in OAuth 2.0 authorization server that issues **Astonish-signed RS256 access tokens**. These tokens are used by:

- The **CLI** (`astonish login --sso`) for remote chat sessions
- The **Chrome extension** for browser side-panel chat
- **MCP hosts** and external tools for programmatic access
- **A2A (Agent-to-Agent)** integrations
- **Service workloads** via Client Credentials grants

The built-in server deliberately separates upstream identity (who you are) from downstream authorization (what you can do in Astonish). An external identity provider does not become an implicit Astonish token issuer — Astonish first validates the upstream login, resolves user and tenant memberships, and then issues its own narrowly scoped credential.

### OAuth Scopes

| Scope | Description |
|-------|-------------|
| `openid` | Standard OIDC identity scope |
| `offline_access` | Enables refresh token issuance |
| `chat` | Access to Studio chat sessions |
| `tool:execute` | Execute tools through MCP |
| `a2a` | Agent-to-Agent communication |

Scopes are intersected with what each client is allowed to request. The CLI requests only `openid offline_access chat`; the Chrome extension requests `chat tool:execute offline_access`.

### Grant Flows

**Authorization Code + PKCE** is the primary flow for human-delegated access. S256 PKCE is mandatory. The user authenticates through the browser, Astonish issues a short-lived authorization code, and the client exchanges it for tokens.

**Refresh Token** grants use opaque rotating handles. Every successful refresh consumes the old handle and issues a new one. Replay of a consumed refresh token revokes the entire token family.

**Client Credentials** is available for service workloads (confidential clients only). The resulting principal is a service identity with no user subject — it cannot access personal memory.

### Token Format

OAuth access tokens are short-lived **RS256 JWTs** (distinct from the HMAC-SHA256 tokens used by the built-in web UI auth). Claims include:

| Claim | Description |
|-------|-------------|
| `iss` | Astonish issuer URL |
| `aud` | Protected resource URL |
| `sub` | User subject (user grants) |
| `org_id` | Organization binding |
| `team_id` | Team binding |
| `scope` | Granted scopes |
| `exp` | Expiration |
| `act.sub` | Delegated actor (when applicable) |

### Discovery Endpoints

| Endpoint | Description |
|----------|-------------|
| `/.well-known/oauth-authorization-server` | Authorization server metadata |
| `/.well-known/oauth-protected-resource` | Protected resource metadata |
| `/oauth/jwks` | Public signing key set |

### Client Types

**First-party public clients** are embedded in Astonish itself:

| Client | ID | Use case |
|--------|----|----------|
| CLI | `astonish-cli` | Remote terminal chat via `astonish login --sso` |
| Chrome Extension | `astonish-chrome-extension` | Browser side-panel chat |

**Registered clients** are created by organization admins through the Settings → OAuth tab in Studio. Each client is bound to an organization and team, and only the creating admin can manage it.

### OAuth Server Configuration

```yaml
storage:
  auth:
    oauth_server:
      enabled: true
      issuer: https://astonish.example.com
      resource: https://astonish.example.com/api/mcp
      access_token_ttl_minutes: 15
      refresh_token_ttl_days: 30
```

When `issuer` or `resource` are omitted, the server defaults to `http://127.0.0.1:<port>` (development convenience). Production deployments require a stable public HTTPS issuer.

## CLI Login

Connect to a platform instance:

```bash
# Email/password login
astonish login https://astonish.example.com

# OAuth login (opens browser for sign-in)
astonish login https://astonish.example.com --sso

# Specify org and team
astonish login https://astonish.example.com --org acme --team backend
```

The `--sso` flag initiates an **OAuth Authorization Code + PKCE** flow: the CLI starts a temporary loopback HTTP server, opens your browser to the Astonish authorization endpoint, receives the authorization code on the callback, and exchanges it for tokens. No device code or manual code entry is required.

When multiple organizations or teams are available, the CLI prompts for interactive selection.

If a stale login is detected, the CLI automatically replaces the old credentials without requiring a manual `astonish logout` first.

## Token Lifecycle

1. User authenticates (built-in, OIDC, or OAuth).
2. Server issues an access token (15 min) and refresh token.
3. API requests include the access token in the `Authorization: Bearer` header.
4. On expiry, the client silently exchanges the refresh token for a new access token.
5. Refresh token rotation: each use invalidates the previous refresh token.
6. Replay of a consumed refresh token revokes the entire token family.

## Revocation

Token revocation follows RFC 7009 non-disclosure behavior — revoking an unknown token returns success. A client may revoke only its own token families. Organization administrators can also revoke tokens or deactivate clients through the Settings → OAuth tab.

## Configuration Reference

```yaml
auth:
  # HMAC-SHA256 signing key for built-in web auth (required, generate with: openssl rand -hex 32)
  jwt_secret: "$<JWT_SECRET>"

  # Built-in auth settings
  builtin:
    enabled: true
    password_min_length: 12
```

OIDC providers are managed through the platform API and stored per-organization in the database, not in the config file. The OAuth server is configured under `storage.auth.oauth_server` (see [OAuth Server Configuration](#oauth-server-configuration) above).

## See Also

- [Credential Security](./credential-security.md) — how credentials are protected after authentication
- [Audit Logging](./audit-logging.md) — authentication events are recorded in the audit trail
