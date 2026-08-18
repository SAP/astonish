# A2A Authentication Redesign — Identity Propagation via OAuth 2.0 Token Exchange

## Problem Statement

The current A2A implementation has a fundamentally broken identity model:

1. **Unconstrained impersonation**: When `AllowIdentityPropagation=true`, an external agent can claim to be ANY user by simply putting `"user_email": "anyone@company.com"` in the message metadata. No verification occurs.
2. **Single token → all users**: One API key with propagation enabled gives access to every user's context (credentials, memory, tools, sessions).
3. **No cryptographic proof**: Identity is asserted via a plaintext string, not a signed token from a trusted authority.
4. **Violates Astonish's core model**: Every other channel (Telegram, Slack, Email) links a verified external identity to a specific platform user. A2A bypasses this entirely.

## Design Goals

1. **Same security model as other channels**: Every A2A request executes in the context of a specific, verified platform user — with their team, credentials, memory, MCP servers, and skills.
2. **Enterprise-grade identity propagation**: External systems (like SAP Joule) can call Astonish on behalf of their authenticated users, with cryptographic proof of who the user is.
3. **No trust in the requester's self-assertion**: Identity must be proven by a token signed by a trusted Identity Provider (IdP), not asserted in metadata.
4. **Standards-based**: Uses OAuth 2.0 Token Exchange (RFC 8693) semantics, OpenID Connect for user identity, and the A2A protocol's native `securitySchemes` mechanism.
5. **Multi-tenant safe**: Different organizations can configure different trusted IdPs; an agent authenticated in one org cannot access another.

## Architecture Overview

### Authentication Layers

```
External Agent (e.g., SAP Joule)
    |
    | HTTPS + Authorization: Bearer <delegated-jwt>
    |
    v
┌─────────────────────────────────────────────────────────────────────┐
│  A2A Auth Middleware                                                  │
│                                                                       │
│  1. Extract Bearer token from Authorization header                    │
│  2. Determine token type (service token vs delegated user token)      │
│  3. Validate signature against configured trusted issuers (JWKS)      │
│  4. Extract identity:                                                 │
│     - sub  = user identity (email or user ID)                         │
│     - act  = calling agent/service identity (RFC 8693)                │
│     - aud  = must include Astonish's A2A audience                     │
│     - org/tenant = routing hint                                       │
│  5. Resolve user via PlatformResolver (same as Telegram/Slack/Email)  │
│  6. Inject authenticated context                                      │
└─────────────────────────────────────────────────────────────────────┘
```

### Two Authentication Modes

The redesigned A2A supports two authentication modes, both producing the same outcome: a verified platform user context.

#### Mode 1: Direct User Token (OIDC)

The external agent holds a valid **user access token** (or ID token) from a trusted IdP that Astonish is configured to accept. This is the simplest case — the user authenticated directly with the IdP, and the agent forwards their token.

```
User authenticates with IdP (e.g., IAS, Azure AD, Okta)
    → IdP issues JWT with sub=user@company.com, aud=astonish-a2a
    → External agent includes this JWT in Authorization: Bearer header
    → Astonish validates JWT signature via JWKS, extracts sub as user identity
    → PlatformResolver maps user@company.com → platform user → team context
```

**When to use**: When the external agent can obtain a user-scoped token with Astonish as the audience. This is the standard OIDC pattern.

#### Mode 2: Delegated Token (OAuth 2.0 Token Exchange / On-Behalf-Of)

The external agent authenticates as a **service** (client credentials) but acts on behalf of a user. The token carries both identities using RFC 8693 `act` claim semantics.

```
External system authenticates user internally
    → System calls its own IdP to exchange user token for a delegated token:
       - subject_token = user's token
       - actor_token = system's service credential
       - audience = astonish-a2a
    → IdP issues delegated JWT:
       {
         "sub": "user@company.com",      ← the user
         "act": { "sub": "joule-prod" }, ← the acting service
         "aud": "astonish-a2a",
         "iss": "https://accounts.sap.com",
         "exp": 1723620000
       }
    → External agent includes this JWT in Authorization: Bearer header
    → Astonish validates:
       1. Signature via JWKS from trusted issuer
       2. Audience matches configured A2A audience
       3. Actor (act.sub) is in the allowed agents list for this org
       4. Subject (sub) resolves to a platform user
    → PlatformResolver maps sub → platform user → team context
```

**When to use**: Enterprise integrations where the external system (Joule, Copilot, etc.) has its own authentication and acts on behalf of users who already exist in both systems.

### How This Maps to SAP Joule

```
┌──────────────────────────────────────────────────────────────┐
│  SAP Joule (BTP)                                              │
│                                                               │
│  1. User is authenticated via IAS (OIDC)                      │
│  2. Joule needs to call Astonish on behalf of user            │
│  3. Joule calls IAS token exchange:                           │
│     - subject_token = user's IAS token                        │
│     - client_credentials = Joule's service identity           │
│     - audience = "astonish-a2a" (configured in IAS)           │
│  4. IAS issues delegated JWT:                                 │
│     { sub: "user@company.com", act: { sub: "joule" }, ... }   │
│  5. Joule sends A2A request with this JWT                     │
└──────────────────────────┬───────────────────────────────────┘
                           │
                           v
┌──────────────────────────────────────────────────────────────┐
│  Astonish A2A Server                                          │
│                                                               │
│  1. Validates JWT signature (IAS JWKS endpoint)               │
│  2. Checks audience = "astonish-a2a"                          │
│  3. Checks act.sub = "joule" is in allowed agents for org     │
│  4. Extracts sub = "user@company.com"                         │
│  5. PlatformResolver: email → user → org/team context         │
│  6. Executes request as that user (same as if they used       │
│     Studio directly)                                          │
└──────────────────────────────────────────────────────────────┘
```

## Data Model

### Trusted Issuer Configuration (per-org)

Each organization configures which Identity Providers are trusted for A2A:

```go
// TrustedIssuer represents an IdP trusted for A2A token validation.
type TrustedIssuer struct {
    ID          string `json:"id"`
    Name        string `json:"name"`            // "SAP IAS Production"
    Issuer      string `json:"issuer"`          // "https://accounts.sap.com" (must match JWT iss)
    JWKSURL     string `json:"jwks_url"`        // "https://accounts.sap.com/.well-known/jwks.json"
    Audience    string `json:"audience"`        // Expected aud claim ("astonish-a2a" or custom)
    UserClaim   string `json:"user_claim"`      // Which claim identifies the user: "sub", "email", "preferred_username"
    OrgID       string `json:"org_id"`          // Owning organization in Astonish
}
```

### Allowed Agent Configuration (per-org)

Each org specifies which service identities (actors) are permitted to make delegated calls:

```go
// AllowedAgent represents an external service authorized to make A2A calls for an org.
type AllowedAgent struct {
    ID          string   `json:"id"`
    Name        string   `json:"name"`           // "SAP Joule Production"
    ActorSub    string   `json:"actor_sub"`      // The act.sub value to match (e.g., "joule-prod")
    IssuerID    string   `json:"issuer_id"`      // Which trusted issuer this agent uses
    OrgID       string   `json:"org_id"`         // Owning organization
    RateLimit   int      `json:"rate_limit"`     // Requests per minute (0 = unlimited)
    MaxTasks    int      `json:"max_tasks"`      // Max concurrent tasks (0 = unlimited)
    Description string   `json:"description"`    // Human-readable description
    Enabled     bool     `json:"enabled"`
}
```

### User Identity Resolution

The identity chain works exactly like other channels:

```
JWT sub claim (e.g., "user@company.com")
    → UserChannelStore.GetByExternalID(ctx, "a2a", "user@company.com")
    → UserChannel { UserID: "platform-user-123", Verified: true, Enabled: true }
    → PlatformResolver enriches context with team-scoped stores
```

Users link their A2A identity the same way they link Telegram or Email:
- Admin bulk-imports user mappings (email → platform user)
- Or: if the platform user's email matches the JWT sub, auto-link on first request (configurable)

### Platform Settings (DB)

```go
// PlatformA2AConfig — redesigned
type PlatformA2AConfig struct {
    Enabled             bool   `json:"enabled"`
    BaseURL             string `json:"base_url,omitempty"`
    TaskTTL             string `json:"task_ttl,omitempty"`
    DefaultAudience     string `json:"default_audience,omitempty"`     // e.g., "astonish-a2a"
    AutoLinkByEmail     bool   `json:"auto_link_by_email"`            // Auto-create user-channel link if email matches
    RequireActorClaim   bool   `json:"require_actor_claim"`           // If true, reject tokens without act claim
}
```

## Token Validation Flow

```go
func validateA2AToken(tokenStr string, orgIssuers []TrustedIssuer, allowedAgents []AllowedAgent) (*A2AIdentity, error) {
    // 1. Parse JWT header to get kid (key ID)
    // 2. Try each trusted issuer's JWKS to find matching key
    // 3. Validate signature, expiry, not-before
    // 4. Validate audience matches issuer's configured audience
    // 5. Extract user identity from configured user_claim
    // 6. If act claim present: validate actor is in allowed agents list
    // 7. If no act claim and RequireActorClaim=false: treat as direct user token
    // 8. Return resolved identity
}

type A2AIdentity struct {
    UserIdentifier string // The user's external ID (email, sub, etc.)
    ActorIdentifier string // The service acting on behalf (empty if direct user token)
    Issuer         string // Which IdP issued this token
    OrgID          string // Which org this resolves to
}
```

## Agent Card (Updated)

The Agent Card now advertises OpenID Connect as the primary auth mechanism:

```json
{
  "name": "Astonish",
  "url": "https://astonish.example.com/api/a2a",
  "version": "1.0.0",
  "securitySchemes": {
    "oidc": {
      "type": "openIdConnect",
      "openIdConnectUrl": "https://astonish.example.com/.well-known/openid-configuration"
    },
    "bearerJWT": {
      "type": "http",
      "scheme": "bearer",
      "bearerFormat": "JWT"
    }
  },
  "security": [
    { "oidc": ["a2a"] },
    { "bearerJWT": [] }
  ]
}
```

Note: Astonish doesn't need to BE an OIDC provider. The `openIdConnectUrl` can point to the org's configured IdP, or we can use the simpler `bearerJWT` scheme which just says "send a JWT Bearer token" — the validation happens server-side against configured JWKS endpoints.

## What Gets Removed

1. **`InMemoryAgentRegistry`** — replaced by `AllowedAgent` records in the DB (persistent, per-org)
2. **`AllowIdentityPropagation` flag** — identity is ALWAYS from the JWT; there's no "propagation" toggle
3. **`LinkedUserID` on RegisteredAgent** — identity comes from the token, not from agent registration
4. **Plaintext API keys (`a2a_<uuid>`)** — replaced by JWT Bearer tokens from trusted IdPs
5. **`extractIdentityFromMetadata`** — no more trusting metadata strings
6. **Admin endpoints for agent CRUD** — replaced by trusted issuer + allowed agent configuration (admin UI)

## What Gets Added

1. **JWKS client** — fetches and caches public keys from trusted issuers (with rotation support)
2. **JWT validation** — signature verification, audience check, expiry, issuer matching
3. **Trusted Issuer store** — per-org configuration of which IdPs to trust
4. **Allowed Agent store** — per-org allowlist of service identities (act.sub values)
5. **Auto-link capability** — optionally create user-channel links when JWT email matches platform user
6. **A2A user-channel links** — users get `channel_type="a2a"` entries in UserChannels, same as telegram/email

## Configuration Example

### Organization Admin UI / API

```
POST /api/admin/a2a/issuers
{
  "name": "SAP IAS Production",
  "issuer": "https://mycompany.accounts.ondemand.com",
  "jwks_url": "https://mycompany.accounts.ondemand.com/oauth2/certs",
  "audience": "astonish-a2a",
  "user_claim": "email"
}

POST /api/admin/a2a/agents
{
  "name": "SAP Joule",
  "actor_sub": "joule-prod-client-id",
  "issuer_id": "<issuer-id-from-above>",
  "rate_limit": 60,
  "max_tasks": 10
}
```

### config.yaml (for self-hosted / simple setups)

```yaml
channels:
  a2a:
    enabled: true
    base_url: "https://astonish.example.com"
    default_audience: "astonish-a2a"
    auto_link_by_email: true
    require_actor_claim: false  # Allow direct user tokens too
    trusted_issuers:
      - name: "Company IdP"
        issuer: "https://login.company.com"
        jwks_url: "https://login.company.com/.well-known/jwks.json"
        audience: "astonish-a2a"
        user_claim: "email"
    allowed_agents:
      - name: "Joule"
        actor_sub: "joule-client-id"
        issuer: "Company IdP"  # references trusted issuer by name
```

## Request Flow (Complete)

### Happy Path: Joule calling Astonish on behalf of user

```
1. User is logged into SAP system, authenticated via IAS
2. User triggers Joule action that needs Astonish capabilities
3. Joule performs token exchange with IAS:
   - subject_token = user's IAS access token
   - client_credentials = Joule's service identity
   - audience = "astonish-a2a"
   - scope = "a2a:task"
4. IAS issues delegated JWT:
   {
     "iss": "https://mycompany.accounts.ondemand.com",
     "sub": "user@company.com",
     "aud": "astonish-a2a",
     "act": { "sub": "joule-prod-client-id" },
     "exp": <now + 5min>,
     "iat": <now>
   }
5. Joule sends A2A JSON-RPC request:
   POST /api/a2a
   Authorization: Bearer <delegated-jwt>
   Content-Type: application/json

   {
     "jsonrpc": "2.0",
     "id": "req-1",
     "method": "message/send",
     "params": {
       "message": { "role": "user", "parts": [{"type": "text", "text": "Analyze Q3 report"}] },
       "configuration": { "contextId": "joule-session-abc" }
     }
   }

6. Astonish A2A Auth Middleware:
   a. Extracts Bearer JWT
   b. Parses header → gets kid
   c. Fetches JWKS from IAS (cached)
   d. Validates signature, expiry, audience
   e. Extracts: sub="user@company.com", act.sub="joule-prod-client-id"
   f. Looks up org by issuer → finds org "mycompany"
   g. Checks "joule-prod-client-id" is in allowed agents for "mycompany"
   h. Looks up UserChannel: (type="a2a", external_id="user@company.com") → user-123
      (or auto-links if auto_link_by_email=true and user with that email exists)
   i. Calls PlatformResolver.ResolveChannelUser → enriched context with team stores

7. A2A Handler processes request in user-123's context
   (same credentials, memory, MCP servers, skills as if they used Studio)

8. Response returned as JSON-RPC result
```

### Error Cases

| Scenario | Response |
|----------|----------|
| No Authorization header | 401 + JSON-RPC error: "Authentication required" |
| JWT signature invalid | 401 + JSON-RPC error: "Invalid token signature" |
| JWT expired | 401 + JSON-RPC error: "Token expired" |
| Issuer not trusted | 403 + JSON-RPC error: "Untrusted issuer" |
| Audience mismatch | 403 + JSON-RPC error: "Invalid audience" |
| Actor not in allowed list | 403 + JSON-RPC error: "Agent not authorized" |
| User not found in platform | 403 + JSON-RPC error: "User not provisioned" |
| User channel link disabled | 403 + JSON-RPC error: "A2A access disabled for user" |

## Comparison with Current Channels

| Aspect | Telegram | Email | A2A (Redesigned) |
|--------|----------|-------|------------------|
| External ID | Telegram user ID | email address | JWT sub claim (email/ID) |
| Verification | Bot interaction + /link | Email verification code | JWT signature from trusted IdP |
| Link storage | UserChannel(telegram, tg_id) | UserChannel(email, addr) | UserChannel(a2a, email/sub) |
| Auth mechanism | Telegram bot API validates sender | IMAP credentials | JWT Bearer + JWKS validation |
| Per-user? | Yes (each TG user is distinct) | Yes (each email is distinct) | Yes (each JWT sub is distinct) |
| Service-to-service? | N/A | N/A | Yes, via `act` claim (service acts for user) |

## Migration Path

1. **Phase 1**: Implement JWKS-based JWT validation alongside existing API key auth
   - Existing API key auth continues to work (backward compat)
   - New JWT auth available for new integrations
   - Remove `AllowIdentityPropagation` — API key auth always uses fixed linked user

2. **Phase 2**: Add trusted issuer + allowed agent configuration (DB + admin API)
   - Org admins can configure their IdPs
   - Users can link their A2A identity (or auto-link by email)

3. **Phase 3**: Deprecate and remove API key auth
   - All A2A auth goes through JWT
   - API keys sunset after migration period

## Security Properties

1. **No trust in requester assertions**: Identity comes from cryptographically signed JWT, not from message metadata.
2. **Audience binding**: Token must be issued specifically for Astonish (`aud` claim), preventing token reuse from other services.
3. **Short-lived tokens**: Delegated tokens should be short-lived (5-15 min), limiting exposure window.
4. **Actor verification**: Even with a valid user token, the acting service must be in the org's allowlist.
5. **Per-user scoping**: Each request resolves to exactly one platform user, with their specific permissions.
6. **Issuer isolation**: Each org configures its own trusted issuers; one org's IdP cannot authenticate users in another org.
7. **Audit trail**: Both user (sub) and actor (act.sub) are logged, enabling "who did what on behalf of whom" auditing.
8. **Revocation**: Tokens are validated on every request; revoking a user's account or disabling their channel link immediately blocks access.

## Dependencies

- **`github.com/golang-jwt/jwt/v5`** — already in use for platform auth
- **JWKS fetching** — need a JWKS client with caching (e.g., `github.com/MicahParks/keyfunc/v3` or custom)
- No new external IdP integration needed — Astonish validates tokens, it doesn't issue them

## A2A Protocol Alignment

This design aligns with the A2A v1.0 specification:

1. **Agent Card `securitySchemes`**: We advertise `bearerJWT` (type: http, scheme: bearer, bearerFormat: JWT)
2. **Transport-layer auth**: Identity is in HTTP headers, not in JSON-RPC payloads (per spec)
3. **`tenant` field**: We can use the A2A `tenant` field as the org/team routing hint
4. **No protocol extensions needed**: Standard A2A + standard OAuth/OIDC

## Open Questions

1. **JWKS cache TTL**: How aggressively to cache JWKS? Recommend 1h with forced refresh on unknown kid.
2. **Auto-link policy**: Should auto-linking require the user to already exist, or also auto-provision? Recommend: user must exist, link is auto-created.
3. **Fallback for simple setups**: Should we keep a simplified "shared secret JWT" mode for self-hosted instances without an external IdP? (Astonish issues its own JWTs for A2A, validated with its own key.)
4. **Rate limiting**: Per-agent or per-user or both? Recommend: per-agent global + per-user within agent.
