# Multi-Tenant Platform Architecture

## Overview

Astonish is transitioning from a single-user personal assistant to a multi-tenant AI Operations Platform. In its current form, Astonish runs as a local daemon with all data stored under `~/.config/astonish/` — sessions, memories, credentials, apps, flows, and fleet configurations are all file-based and scoped to a single user (the hardcoded `studio_user` identity).

The platform evolution enables multiple organizations (companies), each with multiple teams and users, to share a single Astonish deployment. The core value proposition: **knowledge learned by one team member is shared with the entire team**, turning Astonish from a personal tool into an organizational knowledge multiplier.

### Deployment Modes

Astonish supports two deployment modes, selected by configuration:

| Mode | Storage | Auth | Use Case |
|------|---------|------|----------|
| **Personal** (default) | File-based (`~/.config/astonish/`) | Device auth code (current) | Single user, local machine |
| **Platform** | PostgreSQL (+ future HANA) | Built-in + OIDC federation | Multi-org SaaS, cloud deployment |

Personal mode is 100% backward compatible. Platform mode activates when `storage.backend: postgres` is configured. No existing behavior changes for users who don't opt in.

### Key Architectural Decisions

| Decision | Choice | Rationale |
|----------|--------|-----------|
| **Data layer** | Dual-backend (file + PostgreSQL) | Personal mode stays zero-config; platform mode uses PG |
| **Org isolation** | Separate PostgreSQL database per organization | Connection-level isolation; impossible to query cross-org |
| **Team isolation** | Schema-per-team within org database | Structural namespace isolation with GRANT/REVOKE |
| **User privacy** | Personal schemas per user | Private data in isolated namespace |
| **Defense-in-depth** | RLS on shared tables + restricted application role | Even buggy code can't leak cross-tenant data |
| **Identity** | Built-in auth + OIDC federation | Self-contained for small deployments, enterprise SSO for large |
| **Knowledge sharing** | 3-tier: Personal → Team → Org | Weighted search across scopes; users control what they share |
| **App sharing** | Publish-to-team model | Personal by default; explicit publish to share; fork to customize |
| **Sandbox isolation** | Per-org bridge networks | Containers from different orgs can't reach each other |
| **API evolution** | In-place modification with context injection | No API versioning; handlers gain scope via middleware |
| **Future DB support** | Store abstraction layer | Interface works for PostgreSQL and future HANA HDI |

---

## Database Architecture

### Layout

```
┌───────────────────────────────────────────────────────────────────────┐
│                         Platform Database                             │
│  (astonish_platform)                                                  │
│                                                                       │
│  organizations     users          org_memberships    platform_config  │
│  login_sessions    oidc_providers platform_skills    global_audit_log │
└───────────────────────────────────────────────────────────────────────┘
            │                      │                      │
            ▼                      ▼                      ▼
┌─────────────────────┐ ┌─────────────────────┐ ┌─────────────────────┐
│   astonish_org_001  │ │   astonish_org_002  │ │   astonish_org_NNN  │
│                     │ │                     │ │                     │
│ public schema:      │ │ public schema:      │ │ public schema:      │
│   org_memories      │ │   org_memories      │ │   org_memories      │
│   org_skills        │ │   org_skills        │ │   org_skills        │
│   org_apps          │ │   org_apps          │ │   org_apps          │
│   org_audit_log     │ │   org_audit_log     │ │   org_audit_log     │
│   teams             │ │   teams             │ │   teams             │
│   team_memberships  │ │   team_memberships  │ │   team_memberships  │
│                     │ │                     │ │                     │
│ team_{slug} schema: │ │ team_{slug} schema: │ │ team_{slug} schema: │
│   sessions          │ │   sessions          │ │   sessions          │
│   memories          │ │   memories          │ │   memories          │
│   credentials       │ │   credentials       │ │   credentials       │
│   apps              │ │   apps              │ │   apps              │
│   app_state         │ │   app_state         │ │   app_state         │
│   flows             │ │   flows             │ │   flows             │
│   scheduled_jobs    │ │   scheduled_jobs    │ │   scheduled_jobs    │
│   fleet_templates   │ │   fleet_templates   │ │   fleet_templates   │
│   fleet_plans       │ │   fleet_plans       │ │   fleet_plans       │
│   team_audit_log    │ │   team_audit_log    │ │   team_audit_log    │
│                     │ │                     │ │                     │
│ personal_{uid}:     │ │ personal_{uid}:     │ │ personal_{uid}:     │
│   memories          │ │   memories          │ │   memories          │
│   apps              │ │   apps              │ │   apps              │
│   sessions          │ │   sessions          │ │   sessions          │
│   app_state         │ │   app_state         │ │   app_state         │
│   credentials       │ │   credentials       │ │   credentials       │
│   scheduled_jobs    │ │   scheduled_jobs    │ │   scheduled_jobs    │
│   session_events    │ │   session_events    │ │   session_events    │
└─────────────────────┘ └─────────────────────┘ └─────────────────────┘
```

### Why Database-per-Organization

The primary tenancy boundary is the **organization** (company). Different companies accessing Astonish as a SaaS must have absolute data isolation. A separate PostgreSQL database per organization provides this:

- A connection to `astonish_org_001` literally cannot query tables in `astonish_org_002`. The isolation is structural at the PostgreSQL connection level -- no WHERE clause, no RLS policy, no session variable can be misconfigured to leak data.
- Backups, restores, and data exports are naturally per-org.
- Resource-intensive queries from one org cannot scan another org's tables.
- A coding mistake in a handler that forgets tenant filtering cannot access another org's data because the connection itself is scoped.

Using a simple `WHERE tenant_id = ?` on shared tables is the weakest form of multi-tenancy. A single forgotten filter, a reporting query, a JOIN without the scope condition, or a new developer who doesn't know the pattern -- any of these can leak cross-tenant data. Database-level separation eliminates this entire class of bugs.

### Why Schema-per-Team

Within an organization, teams share a trust boundary (they're in the same company) but still need data separation. PostgreSQL schemas provide structural namespace isolation:

- Each team's tables exist in a separate schema (`team_ops`, `team_sre`, etc.)
- PostgreSQL `GRANT USAGE ON SCHEMA` controls which schemas a connection can access
- Unqualified queries resolve via `search_path` -- if the team schema isn't in the path, those tables don't exist from the query's perspective
- Cross-team queries (for org-level data) work naturally since all schemas are in the same database

### Why Personal Schemas

User-private data (personal memories, personal apps, personal sessions, personal credentials, personal scheduled jobs) lives in `personal_{user_id}` schemas. This ensures that even within a team, one user's private data is inaccessible to other team members at the structural level. Personal scheduled jobs use the owner's personal credentials at cron time; team jobs remain in the team schema with team credentials only (see `daemon-scheduler.md`).

### Isolation Guarantees

| Boundary | Mechanism | Strength | Failure Mode |
|----------|-----------|----------|--------------|
| Between organizations | Separate PG databases | **Absolute** | Connection-level; impossible to query cross-org |
| Between teams (same org) | Separate schemas + GRANT | **Strong** | Table doesn't exist if schema not in search_path |
| Between users (same team) | Personal schemas + GRANT | **Strong** | Schema only accessible to owning user's connections |
| Defense-in-depth on shared tables | RLS policies | **DB-enforced** | Rows silently filtered even if app code has a bug |
| Audit trail | Append-only tables, no DELETE grant | **Tamper-resistant** | Application role cannot delete audit records |

### Connection Flow

```
Request arrives with JWT
  → Middleware extracts user_id, org_id from JWT
  → TenantRouter.ForOrg(org_id) → obtains connection from org-specific pool
  → Middleware executes on the connection:
      SET search_path TO personal_{user_id}, team_{active_team_slug}, public;
      SET app.current_user TO '{user_id}';
      SET app.current_team TO '{team_id}';
  → Connection passed to handler via request context
  → All unqualified queries resolve to correct schemas automatically
  → Connection returned to pool
  → Pool lifecycle hook resets search_path
```

Team and personal stores are opened lazily and auto-migrated on open. Credential access also performs a one-shot schema repair when the selected tenant schema is present but the `credentials` table is missing. The repair runs through the already-selected Ent client/search path, so it preserves schema-level tenant isolation while recovering from partial provisioning or stale cached store state without requiring a daemon restart.

Runtime PostgreSQL pools use pgx `describe_exec` query execution. Astonish can reconcile schemas while other pods still have active connections during rolling deployments; describing each query before execution avoids reusing a server-side prepared plan whose result type predates that reconciliation. Database errors still propagate to callers rather than being retried or suppressed.

### PostgreSQL Roles & Permissions

```sql
-- Created once per PG cluster
CREATE ROLE astonish_platform_admin;   -- owns all databases, runs migrations
CREATE ROLE astonish_app;              -- application connections (restricted)

-- Per-org database:
-- Tables owned by astonish_platform_admin (so RLS applies to astonish_app)
-- astonish_app has CONNECT privilege
-- astonish_app gets SELECT/INSERT/UPDATE/DELETE on team and personal schemas
-- astonish_app gets INSERT-only on audit tables (no UPDATE, no DELETE)
-- astonish_app does NOT have BYPASSRLS
```

---

## Store Abstraction Layer

### Interface Design (`pkg/store/`)

The store layer is database-engine agnostic, supporting PostgreSQL now and HANA HDI in the future. The application never knows which database engine is underneath.

```go
// PlatformStore manages cross-org data (auth, org registry)
type PlatformStore interface {
    Users() UserStore
    Organizations() OrgStore
    LoginSessions() LoginSessionStore
    PlatformSkills() SkillStore
    Close() error
}

// TenantRouter routes to the correct org's data store
type TenantRouter interface {
    ForOrg(orgID string) (OrgDataStore, error)
    ProvisionOrg(orgID, slug string) error
    DecommissionOrg(orgID string) error
}

// OrgDataStore is the root of all data access within an organization
type OrgDataStore interface {
    ForTeam(teamSlug string) TeamDataStore
    ForUser(userID string) PersonalDataStore
    OrgMemories() MemoryStore
    OrgSkills() SkillStore
    OrgApps() AppStore
    OrgAudit() AuditStore
    Teams() TeamStore
    ProvisionTeam(slug string) error
    ProvisionPersonalSchema(userID string) error
    Close() error
}

// TeamDataStore accesses a specific team's data
type TeamDataStore interface {
    Sessions() SessionStore
    Memories() MemoryStore
    Credentials() CredentialStore
    Apps() AppStore
    AppState() AppStateStore
    Flows() FlowStore
    ScheduledJobs() SchedulerStore
    FleetTemplates() FleetStore
    FleetPlans() FleetPlanStore
    Audit() AuditStore
}

// PersonalDataStore accesses a user's private data
type PersonalDataStore interface {
    Memories() MemoryStore
    Apps() AppStore
    Sessions() SessionStore
    AppState() AppStateStore
}
```

### Implementations

| Implementation | Package | Backend | For |
|---------------|---------|---------|-----|
| `FileStore` | `pkg/store/filestore/` | Local filesystem | Personal mode (wraps existing code) |
| `PGStore` | `pkg/store/pgstore/` | PostgreSQL | Platform mode |
| `HANAStore` | `pkg/store/hanastore/` | SAP HANA HDI | Future: platform mode with HANA |

### Engine Mapping

| Concept | PostgreSQL | HANA HDI |
|---------|-----------|----------|
| Org isolation | Separate database | HDI container |
| Team isolation | Schema per team | Schema per team within HDI container |
| Context switching | `SET search_path` | `SET SCHEMA` |
| Vector search | pgvector extension | HANA vector engine (PAL) |
| Row-level security | RLS policies | Column/analytic privileges |

### Schema Migration System

```
migrations/
  platform/           -- platform database migrations
    001_init.sql
    002_add_oidc.sql
    ...
  org/                 -- org database migrations (public schema)
    001_init.sql
    002_add_pgvector.sql
    ...
  team/                -- team schema template migrations
    001_init.sql
    002_add_memories.sql
    ...
  personal/            -- personal schema template migrations
    001_init.sql
    ...
```

Migrations are versioned SQL files applied in order. When a new org is provisioned, the system creates the database and runs all org-level migrations. When a new team or user is added, the system creates the schema and runs the corresponding template migrations.

---

## Schema Definitions

### Platform Database (`astonish_platform`)

```sql
CREATE TABLE organizations (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name        TEXT NOT NULL,
    slug        TEXT NOT NULL UNIQUE,
    db_name     TEXT NOT NULL UNIQUE,
    status      TEXT NOT NULL DEFAULT 'active'
                CHECK (status IN ('active','suspended','decommissioned')),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    settings    JSONB DEFAULT '{}'
);

CREATE TABLE users (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email           TEXT NOT NULL UNIQUE,
    display_name    TEXT NOT NULL,
    password_hash   TEXT,               -- bcrypt, NULL if OIDC-only
    oidc_subject    TEXT,
    oidc_issuer     TEXT,
    status          TEXT NOT NULL DEFAULT 'active',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_login_at   TIMESTAMPTZ,
    UNIQUE(oidc_issuer, oidc_subject)
);

CREATE TABLE org_memberships (
    user_id     UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    org_id      UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    role        TEXT NOT NULL DEFAULT 'member'
                CHECK (role IN ('owner','admin','member')),
    joined_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, org_id)
);

CREATE TABLE oidc_providers (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id          UUID REFERENCES organizations(id),  -- NULL = platform-wide
    issuer_url      TEXT NOT NULL,
    client_id       TEXT NOT NULL,
    client_secret   TEXT NOT NULL,    -- encrypted at rest
    scopes          TEXT[] DEFAULT ARRAY['openid','email','profile'],
    team_claim      TEXT,             -- OIDC claim for auto team mapping
    enabled         BOOLEAN DEFAULT true,
    UNIQUE(org_id, issuer_url)
);

CREATE TABLE login_sessions (
    token_hash  TEXT PRIMARY KEY,     -- SHA-256 of refresh token
    user_id     UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    org_id      UUID NOT NULL REFERENCES organizations(id),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at  TIMESTAMPTZ NOT NULL,
    user_agent  TEXT,
    ip_address  INET
);
```

### Org Database — Public Schema (shared tables within an org)

```sql
CREATE TABLE teams (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name        TEXT NOT NULL,
    slug        TEXT NOT NULL UNIQUE,
    schema_name TEXT NOT NULL UNIQUE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    settings    JSONB DEFAULT '{}'
);

CREATE TABLE team_memberships (
    user_id     UUID NOT NULL,
    team_id     UUID NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    role        TEXT NOT NULL DEFAULT 'member'
                CHECK (role IN ('admin','member','viewer')),
    joined_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, team_id)
);

ALTER TABLE team_memberships ENABLE ROW LEVEL SECURITY;
CREATE POLICY tm_isolation ON team_memberships
    USING (
        user_id = current_setting('app.current_user')::UUID
        OR team_id IN (
            SELECT team_id FROM team_memberships
            WHERE user_id = current_setting('app.current_user')::UUID
            AND role = 'admin'
        )
    );

CREATE EXTENSION IF NOT EXISTS vector;

CREATE TABLE org_memories (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    chunk_text  TEXT NOT NULL,
    embedding   vector(384),
    category    TEXT,
    source_path TEXT,
    metadata    JSONB,
    promoted_by UUID NOT NULL,
    promoted_from_team TEXT,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX ON org_memories USING ivfflat (embedding vector_cosine_ops);

CREATE TABLE org_skills (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name        TEXT NOT NULL UNIQUE,
    content     TEXT NOT NULL,
    frontmatter JSONB,
    created_by  UUID NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE org_apps (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    slug        TEXT NOT NULL UNIQUE,
    name        TEXT NOT NULL,
    definition  JSONB NOT NULL,
    promoted_by UUID NOT NULL,
    promoted_from_team TEXT,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE org_audit_log (
    id          BIGSERIAL PRIMARY KEY,
    timestamp   TIMESTAMPTZ NOT NULL DEFAULT now(),
    user_id     UUID NOT NULL,
    team_id     UUID,
    action      TEXT NOT NULL,
    resource    TEXT NOT NULL,
    detail      JSONB,
    ip_address  INET,
    session_id  TEXT
);
-- App role: INSERT only on audit tables (no UPDATE, no DELETE)
```

### Team Schema Template (applied to each `team_{slug}` schema)

```sql
CREATE TABLE sessions (
    id              TEXT PRIMARY KEY,
    user_id         UUID NOT NULL,
    title           TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    message_count   INT DEFAULT 0,
    parent_id       TEXT,
    fleet_key       TEXT,
    fleet_name      TEXT,
    workspace_dir   TEXT,
    metadata        JSONB DEFAULT '{}'
);

CREATE TABLE session_events (
    id          BIGSERIAL PRIMARY KEY,
    session_id  TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    event_data  JSONB NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE memories (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    created_by  UUID NOT NULL,
    chunk_text  TEXT NOT NULL,
    embedding   vector(384),
    category    TEXT,
    source_path TEXT,
    metadata    JSONB,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX ON memories USING ivfflat (embedding vector_cosine_ops);

CREATE TABLE credentials (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name        TEXT NOT NULL UNIQUE,
    cred_type   TEXT NOT NULL,
    encrypted   BYTEA NOT NULL,     -- AES-256-GCM encrypted JSON blob
    created_by  UUID NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE apps (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    slug        TEXT NOT NULL UNIQUE,
    name        TEXT NOT NULL,
    definition  JSONB NOT NULL,
    published_by UUID NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE app_state (
    app_id      UUID NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
    user_id     UUID NOT NULL,
    key         TEXT NOT NULL,
    value       JSONB NOT NULL,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (app_id, user_id, key)
);

CREATE TABLE flows (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name        TEXT NOT NULL UNIQUE,
    definition  JSONB NOT NULL,
    created_by  UUID NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE scheduled_jobs (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name        TEXT NOT NULL,
    schedule    TEXT NOT NULL,       -- cron expression
    mode        TEXT NOT NULL,       -- routine, adaptive, fleet_poll
    payload     JSONB NOT NULL,
    status      TEXT NOT NULL DEFAULT 'active',
    last_run    TIMESTAMPTZ,
    created_by  UUID NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE fleet_templates (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name        TEXT NOT NULL UNIQUE,
    definition  JSONB NOT NULL,
    created_by  UUID NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE fleet_plans (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name        TEXT NOT NULL UNIQUE,
    definition  JSONB NOT NULL,
    created_by  UUID NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE team_audit_log (
    id          BIGSERIAL PRIMARY KEY,
    timestamp   TIMESTAMPTZ NOT NULL DEFAULT now(),
    user_id     UUID NOT NULL,
    action      TEXT NOT NULL,
    resource    TEXT NOT NULL,
    detail      JSONB,
    session_id  TEXT
);
```

### Personal Schema Template (applied to each `personal_{user_id}` schema)

```sql
CREATE TABLE memories (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    chunk_text  TEXT NOT NULL,
    embedding   vector(384),
    category    TEXT,
    source_path TEXT,
    metadata    JSONB,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX ON memories USING ivfflat (embedding vector_cosine_ops);

CREATE TABLE apps (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    slug        TEXT NOT NULL UNIQUE,
    name        TEXT NOT NULL,
    definition  JSONB NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE app_state (
    app_id      UUID NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
    key         TEXT NOT NULL,
    value       JSONB NOT NULL,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (app_id, key)
);

CREATE TABLE sessions (
    id              TEXT PRIMARY KEY,
    title           TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    message_count   INT DEFAULT 0,
    metadata        JSONB DEFAULT '{}'
);

CREATE TABLE session_events (
    id          BIGSERIAL PRIMARY KEY,
    session_id  TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    event_data  JSONB NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Personal credentials and scheduled_jobs mirror the team-schema tables
-- (same columns). Personal jobs capture team_slug in payload for
-- credential/flow fallback at tick time; see daemon-scheduler.md.
CREATE TABLE credentials ( /* same as team.credentials */ );
CREATE TABLE scheduled_jobs ( /* same as team.scheduled_jobs */ );
```

---

## Authentication & Identity

### Authentication Modes

Personal mode retains the existing device-authorization code flow unchanged. Platform mode introduces two auth options:

**Built-in auth** — email + password with bcrypt hashing. JWT access tokens (short-lived, 15min) plus refresh tokens (long-lived, 90 days) stored as HttpOnly cookies. Registration can be open or invite-only.

**OIDC federation** — Standard Authorization Code flow with PKCE. Supports SAP Identity Authentication Service (IAS), Microsoft Entra ID (Azure AD), Okta, and any OpenID Connect provider. On first login, a user record is auto-created from OIDC claims. OIDC groups can be auto-mapped to teams via a configurable claim.

### Configuration

```yaml
# Personal mode (default, unchanged)
# No auth config needed

# Platform mode — built-in auth
auth:
  mode: builtin
  session_ttl_days: 90
  allow_registration: true

# Platform mode — OIDC federation
auth:
  mode: oidc
  issuer: "https://accounts.sap.com"
  client_id: "astonish-prod"
  client_secret_env: "OIDC_CLIENT_SECRET"
  scopes: ["openid", "email", "profile"]
  team_claim: "groups"
```

### JWT Claims Structure

Access tokens contain the full user context. Refresh tokens contain only `uid` and `org`.

```json
// Access token (PlatformClaims)
{
  "typ": "access",
  "uid": "user-uuid",
  "email": "user@company.com",
  "name": "Alice Smith",
  "org": "my-company",
  "team": "ops",
  "role": "admin",
  "iss": "astonish-platform",
  "sub": "user-uuid",
  "exp": 1714500000,
  "iat": 1714499100,
  "jti": "random-token-id"
}

// Refresh token (minimal claims)
{
  "typ": "refresh",
  "uid": "user-uuid",
  "org": "my-company",
  "iss": "astonish-platform",
  "sub": "user-uuid",
  "exp": 1722276000,
  "iat": 1714499100,
  "jti": "random-token-id"
}
```

> **Note:** The `team` field is `DefaultTeamSlug`, the user's default team. Per-request team context can be overridden via the `X-Astonish-Team` header. Full team membership details are NOT embedded in the JWT — they are looked up from the database at runtime to avoid stale data in long-lived tokens.

### Request Context

Every API handler in platform mode receives a `RequestContext` injected by middleware:

```go
type RequestContext struct {
    UserID         string
    OrgID          string
    OrgSlug        string
    OrgRole        string            // owner, admin, member
    Teams          map[string]string // team_id -> role
    ActiveTeamID   string            // from X-Astonish-Team header
    ActiveTeamSlug string
}
```

The `X-Astonish-Team` header (sent by the UI) determines which team context the request operates in. The middleware validates that the user is a member of the specified team before proceeding.

In personal mode, a synthetic context with `studio_user` is injected for backward compatibility.

---

## 3-Tier Knowledge System

### Architecture

```
┌─────────────────────────────────────────────┐
│            Organization Memory              │
│   (public.org_memories)                     │
│   Curated by org admins. Read by all.       │
│   - Platform-wide runbooks                  │
│   - Standard operating procedures           │
│   - Tool best practices                     │
├─────────────────────────────────────────────┤
│             Team Memory                     │
│   (team_{slug}.memories)                    │
│   Shared by team members. Written by any.   │
│   - Team-specific infrastructure knowledge  │
│   - Past incident patterns & solutions      │
│   - Team conventions and preferences        │
├─────────────────────────────────────────────┤
│            Personal Memory                  │
│   (personal_{uid}.memories)                 │
│   Private to the user.                      │
│   - Personal preferences                   │
│   - Draft notes                            │
│   - Individual workflow customizations      │
└─────────────────────────────────────────────┘
```

### Memory Search

When the agent searches memory, the `ThreeTierMemoryStore` composite (`pkg/store/three_tier_memory.go`) queries all three tiers **in parallel** via separate `MemoryStore.Search()` calls, then merges results:

```
ThreeTierSearcher.SearchAllTiers(query, maxResults, minScore)
  ├── goroutine: personal.Search(query, perTierMax, minScore)  → results tagged "personal"
  ├── goroutine: team.Search(query, perTierMax, minScore)      → results tagged "team"
  └── goroutine: org.Search(query, perTierMax, minScore)       → results tagged "org"
  
  → Apply tier weight multipliers (personal 1.2x, team 1.0x, org 0.8x)
  → Deduplicate by chunk text
  → Sort by weighted score descending
  → Trim to maxResults
```

Each individual `MemoryStore.Search()` in PG mode uses **hybrid search** — pgvector cosine distance for semantic similarity + tsvector/tsquery for keyword matching, merged via Reciprocal Rank Fusion (RRF, k=60).

> **Design note:** An earlier version of this document showed a single cross-schema `UNION ALL` query. The actual implementation uses a composite wrapper instead, because: (1) it works identically for both file and PG backends, (2) each tier can use a different `MemoryStore` implementation, and (3) nil stores (e.g., no org memories configured) are silently skipped without query changes.

Results are merged with scope-weighted scoring:

| Scope | Weight | Rationale |
|-------|--------|-----------|
| Personal | 1.2x | Slight boost for personal relevance |
| Team | 1.0x | Standard weight |
| Org | 0.8x | Slightly lower; more generic knowledge |

### Memory Save Flow

```
User solves a problem in conversation
  → Agent calls memory_save tool
  → Embedding computed via ONNX MiniLM-L6-v2
  → Default: saved to personal_{uid}.memories
  → UI prompt: "Share this knowledge with your team?"
      Yes → also inserted into team_{slug}.memories
      No  → stays personal only
  → Team admin can later promote team memory → public.org_memories
```

### Knowledge Promotion Chain

```
Personal memory → (user publishes) → Team memory → (admin promotes) → Org memory
```

### The Learning Loop

```
Day 1:  Engineer A encounters "nova-compute stuck in error state"
        → Solves it by restarting service + evacuating VMs
        → Saves solution to team memory

Day 15: Engineer B encounters similar issue on different hypervisor
        → Astonish searches team memory, finds Engineer A's solution
        → Suggests: "A similar issue was resolved previously:
           Restart nova-compute and evacuate VMs. Apply?"
        → Engineer B applies it, confirms success
        → Astonish reinforces the memory (increases relevance)
```

---

## App Sharing Model

### Lifecycle

```
User creates app in conversation
  → Saved to personal_{uid}.apps (private by default)

"Publish to team"
  → Definition copied to team_{slug}.apps
  → Team members see it in their app catalog
  → Team admins can moderate (edit, unpublish, delete)

"Fork" a team app
  → Definition copied back to personal_{uid}.apps
  → User can customize their copy independently

"Promote" to org (org admin)
  → Definition copied to public.org_apps
  → All teams in the org can see and fork it
```

### App State Isolation

Even when multiple users use the same team app, each gets independent state:

- App definition in `team_{slug}.apps` — shared, read-only for non-admins
- User's state in `team_{slug}.app_state` — keyed by `(app_id, user_id, key)` — private per user

---

## Sandbox Isolation

### Per-Organization Network Isolation

Each organization gets its own Incus bridge network:

```
Personal mode:    incusbr0 (default, current behavior)
Org A containers: org-a-br0 (10.100.1.0/24)
Org B containers: org-b-br0 (10.100.2.0/24)
```

No routing between org bridges. Containers from different organizations cannot reach each other at the network level.

Within an organization, all team containers share the org's bridge. Teams within the same company have a trust relationship; the hard isolation boundary is between organizations.

### Container Naming

```
Personal mode:    astn-sess-{sessionID}             (unchanged)
Platform mode:    astn-{orgSlug}-{teamSlug}-{userShort}-{sessionShort}
Fleet mode:       astn-{orgSlug}-{teamSlug}-fleet-{planKey}-{agentKey}
```

### Credential Injection

```
Request context → resolve user's active team
  → Query team_{slug}.credentials (within org database)
  → Inject as env vars into the user's sandbox container
  → Container never sees credentials from other teams/orgs
```

---

## API Changes

### New Endpoints (Platform Mode Only)

```
Authentication:
  POST   /api/auth/register           Create account + org (or join)           ✅ Implemented
  POST   /api/auth/login              Login → JWT cookies                      ✅ Implemented
  POST   /api/auth/refresh            Refresh JWT                              ✅ Implemented
  POST   /api/auth/logout             Logout (clear cookies)                   ✅ Implemented
  GET    /api/auth/me                 Current user info from JWT               ✅ Implemented
  GET    /api/auth/setup-status       Check if first user exists               ✅ Implemented
  GET    /api/auth/oidc/login         Initiate OIDC flow                       ⏳ Deferred (3.3)
  GET    /api/auth/oidc/callback      OIDC callback                            ⏳ Deferred (3.3)

User:
  GET    /api/user/profile            Current user profile                     ⏳ Not yet implemented
  PUT    /api/user/profile            Update profile                           ⏳ Not yet implemented
  GET    /api/user/orgs               List user's organizations                ⏳ Not yet implemented

Organization:
  GET    /api/org                     Current org details                      ✅ Implemented (team_handlers.go)
  PUT    /api/org                     Update org settings (owner/admin)        ⏳ Not yet implemented
  GET    /api/org/members             List org members                         ⏳ Not yet implemented
  POST   /api/org/invite              Invite user to org                       ⏳ Deferred (3.9)

Teams:
  GET    /api/teams                   List teams in current org                ✅ Implemented
  POST   /api/teams                   Create team (org admin)                  ✅ Implemented
  GET    /api/teams/{id}              Team details                             ✅ Implemented
  DELETE /api/teams/{id}              Delete team (org admin)                  ✅ Implemented
  GET    /api/teams/{id}/members      List team members                        ✅ Implemented
  POST   /api/teams/{id}/members      Add member to team                       ✅ Implemented
  DELETE /api/teams/{id}/members/{uid} Remove member                           ✅ Implemented
  PUT    /api/teams/{id}/members/{uid}/role Change role                        ✅ Implemented

Apps (additions):
  POST   /api/apps/{id}/publish       Publish to team                          ✅ Implemented
  POST   /api/apps/{id}/fork          Fork team/org app to personal            ✅ Implemented
  POST   /api/apps/{id}/promote       Promote to org (org admin)               ✅ Implemented

Memory (platform 3-tier):
  POST   /api/memories/search         Cross-tier search (personal+team+org)    ✅ Implemented
  POST   /api/memories/team           Share memory to team                     ✅ Implemented
  GET    /api/memories/team           List team memories                       ✅ Implemented
  DELETE /api/memories/team/{id}      Delete team memory                       ✅ Implemented
  POST   /api/memories/personal       Save personal memory                     ✅ Implemented
  GET    /api/memories/org            List org memories                        ✅ Implemented
  DELETE /api/memories/org/{id}       Delete org memory                        ✅ Implemented
  POST   /api/memories/promote        Promote team memory to org (admin)       ✅ Implemented

Audit:
  GET    /api/audit                   Query audit log (team/org admin)         ✅ Implemented (via AuditMiddleware)

Platform Setup:
  POST   /api/platform/init           Initialize platform DB + config          ✅ Implemented (Phase 8B)
  GET    /api/platform/init/status    Check if platform is initialized         ✅ Implemented (Phase 8B)
  GET    /api/platform/mode           Get current deployment mode              ✅ Implemented (Phase 8B)

Migration:
  GET    /api/migration/status        Check if file→DB migration is available  ✅ Implemented (Phase 4B)
  POST   /api/migration/start         Start migration (with email/password)    ✅ Implemented (Phase 4B)
  GET    /api/migration/progress      SSE stream of migration progress         ✅ Implemented (Phase 4B)
```

### Existing Endpoint Modifications

All existing endpoints gain org/team context through the middleware-injected `RequestContext`. The hardcoded `studio_user` constant is replaced with the real user identity from JWT. The `X-Astonish-Team` header determines which team's data is accessed.

In personal mode, existing endpoints work identically to today.

---

## Frontend Changes

### New UI Components

| Component | Purpose |
|-----------|---------|
| Login/Register page | Replaces device-auth page in platform mode |
| Org switcher (header) | Switch between organizations (multi-org users) |
| Team switcher (header) | Switch between teams within current org |
| Team management page | Members, roles, invitations |
| App catalog | Browse team/org shared apps, publish/fork |
| Knowledge browser | View/search/manage memories across tiers |
| Memory sharing prompt | "Save for me only" vs "Share with team" |
| Audit log viewer | For team/org admins |

### Team Context

The UI stores the active org + team selection in local storage and sends `X-Astonish-Team` header on every API call. Switching teams refreshes the session list, app catalog, and available credentials.

---

## Automated Provisioning

When a new organization signs up:

1. Platform DB: insert `organizations` row
2. `CREATE DATABASE astonish_org_{slug}`
3. Run org-level migrations (public schema tables, pgvector extension)
4. Create application role grants
5. Set up RLS policies on shared tables
6. Create first team schema + first admin user's personal schema

When a new team is created:

1. Org DB: insert into `teams`
2. `CREATE SCHEMA team_{slug}`
3. Run team-schema migrations
4. `GRANT USAGE ON SCHEMA team_{slug} TO astonish_app`
5. Create Incus bridge network for the org (if not exists)

When a user joins an org:

1. Platform DB: insert into `org_memberships`
2. Org DB: `CREATE SCHEMA personal_{user_id}`
3. Run personal-schema migrations
4. `GRANT USAGE` on the personal schema

---

## Migration Path

### Existing Users (Personal Mode)

No changes. If `storage.backend` is `file` (default), everything works exactly as before. The `Store` interface delegates to `FileStore` which wraps the existing file-based implementations.

### Upgrading to Platform Mode

```bash
# 1. Deploy PostgreSQL with pgvector extension

# 2. Initialize platform (generates suffix + creates database)
astonish platform init --host localhost --user postgres --password secret
# Output includes the platformDSN and instanceSuffix to use below.

# 3. Configure storage backend (values from platform init output)
storage:
  backend: postgres
  postgres:
    platform_dsn: "postgres://postgres:secret@localhost:5432/astonish_a1b2c3_platform?sslmode=prefer"
    instance_suffix: "a1b2c3"

# 4. Create first organization
astonish platform org create --name "My Company" --slug my-company

# 5. Import existing personal data (optional)
astonish platform migrate --from-file --to-org my-company --to-team default

# 6. Configure auth
auth:
  mode: builtin  # or oidc

# 7. Invite team members
astonish platform org invite --email user@company.com --team ops --role member
```

---

## Future: HANA HDI Support

The `Store` abstraction is designed so HANA HDI maps naturally:

| Concept | PostgreSQL | HANA HDI |
|---------|-----------|----------|
| Org isolation | Separate database | HDI container |
| Team isolation | Schema per team | Schema per team within HDI container |
| Context switching | `SET search_path` | `SET SCHEMA` |
| Vector search | pgvector | HANA vector engine (PAL) |
| Row-level security | RLS policies | Column/analytic privileges |

A `pkg/store/hanastore/` implementation would implement the same interfaces with HANA-specific SQL.

---

## Implementation Tracking

Each phase is tracked below. Update status as work progresses.

### Overall Status

| Phase | Name | Status | Done/Total | Notes |
|-------|------|--------|------------|-------|
| 1 | Store Abstraction Layer | **COMPLETE** | 13/13 | Zero behavior change — pure refactor |
| 2 | PostgreSQL Backend | **COMPLETE** | 24/25 | 1 deferred (live PG tests) |
| 3 | Authentication & Identity | **COMPLETE** | 15/19 | 4 deferred (OIDC, invitations, PG tests) |
| 4 | Data Scoping, Audit & Migration | **COMPLETE** | 24/26 | 2 deferred (flow/scheduler scoping) |
| 5 | Knowledge Sharing | **COMPLETE** | 9/9 | — |
| 6 | App Sharing & Sandbox Isolation | **COMPLETE** | 10/10 | — |
| 7 | Frontend | **COMPLETE** | 11/12 | 1 deferred (E2E browser tests) |
| 8 | Platform CLI & Migration Tools | **COMPLETE** | 6/6 | — |
| 8B | Setup Wizard — Deployment Mode | **COMPLETE** | 10/10 | — |
| **Total** | | | **122/130** | **8 deferred** (all non-blocking for MVP) |

### Status Legend

| Status | Meaning |
|--------|---------|
| `NOT STARTED` | Work has not begun |
| `IN PROGRESS` | Actively being implemented |
| `BLOCKED` | Waiting on a dependency or decision |
| `DONE` | Implemented, tested, merged |
| `DEFERRED` | Postponed to a future cycle |

### Phase 1: Store Abstraction Layer

**Goal:** Introduce the `pkg/store/` interfaces and wrap all existing file-based storage behind them. Zero behavior change -- pure refactor.

**Target:** Weeks 1-3

| # | Task | Package/Files | Status | Notes |
|---|------|--------------|--------|-------|
| 1.1 | Define all Store interfaces | `pkg/store/store.go` | `DONE` | PlatformStore, TenantRouter, OrgDataStore, TeamDataStore, PersonalDataStore |
| 1.2 | Define sub-store interfaces | `pkg/store/` | `DONE` | SessionStore, MemoryStore, CredentialStore, AppStore, FlowStore, SchedulerStore, FleetStore, AuditStore, SkillStore + Services struct |
| 1.3 | Implement FileStore wrapper for sessions | `pkg/store/filestore/sessions.go` | `DONE` | Wrap `pkg/session/FileStore` behind SessionStore interface |
| 1.4 | Implement FileStore wrapper for memory | `pkg/store/filestore/memory.go` | `DONE` | Wrap `pkg/memory/Manager` + `Store` behind MemoryStore/MemoryManager |
| 1.5 | Implement FileStore wrapper for credentials | `pkg/store/filestore/credentials.go` | `DONE` | Wrap `pkg/credentials/Store` behind CredentialStore interface |
| 1.6 | Implement FileStore wrapper for apps | `pkg/store/filestore/apps.go` | `DONE` | Wrap `pkg/apps/` types behind AppStore interface |
| 1.7 | Implement FileStore wrapper for flows | `pkg/store/filestore/flows.go` | `DONE` | Wrap `pkg/flowstore/` behind FlowStore interface |
| 1.8 | Implement FileStore wrapper for scheduler | `pkg/store/filestore/scheduler.go` | `DONE` | Wrap `pkg/scheduler/Store` behind SchedulerStore interface |
| 1.9 | Implement FileStore wrapper for fleets | `pkg/store/filestore/fleets.go` | `DONE` | Wrap `pkg/fleet/` registries behind FleetTemplateStore + FleetPlanStore |
| 1.10 | Implement FileStore wrapper for skills | `pkg/store/filestore/skills.go` | `DONE` | Wrap `pkg/skills/` behind SkillStore interface |
| 1.11 | Wire FileStore into daemon/launcher startup | `pkg/daemon/run.go`, `pkg/launcher/` | `DONE` | Services constructed in daemon.Run(), passed via WithServices option to StudioServer, standalone RunStudio creates its own |
| 1.12 | Wire FileStore into all API handlers | `pkg/api/` | `DONE` | `store.Middleware` applied via `RegisterRoutes(router, svc)`, context injection + fallback pattern proven with app_handlers.go |
| 1.13 | Verify all existing tests pass unchanged | `go test ./...` | `DONE` | All tests pass, `golangci-lint run` 0 issues |

**Phase 1 Status: COMPLETE** — 13/13 tasks done. All existing code wrapped behind store interfaces with zero behavior change.

### Phase 2: PostgreSQL Backend

**Goal:** Implement the PGStore with all schema management, migrations, connection pooling, and org provisioning.

**Target:** Weeks 3-6

| # | Task | Package/Files | Status | Notes |
|---|------|--------------|--------|-------|
| 2.1 | Create migration system | `pkg/store/pgstore/migrate.go` | `DONE` | Embed-based, forward-only, schema_migrations tracking, `{{schema}}` placeholder |
| 2.2 | Write platform DB migrations | `pkg/store/pgstore/migrations/platform/001_init.sql` | `DONE` | organizations, users, org_memberships, oidc_providers, login_sessions |
| 2.3 | Write org DB migrations | `pkg/store/pgstore/migrations/org/001_init.sql` | `DONE` | teams, team_memberships, org_memories, org_skills, org_apps, org_audit_log, RLS |
| 2.4 | Write team schema migrations | `pkg/store/pgstore/migrations/team/001_init.sql` | `DONE` | sessions, memories, credentials, apps, flows, scheduled_jobs, fleet_templates, fleet_plans, team_audit_log |
| 2.5 | Write personal schema migrations | `pkg/store/pgstore/migrations/personal/001_init.sql` | `DONE` | memories, apps, sessions, app_state, session_events |
| 2.6 | Implement org provisioning | `pkg/store/pgstore/provision.go` | `DONE` | CREATE DATABASE, run migrations, set up roles/grants |
| 2.7 | Implement team provisioning | `pkg/store/pgstore/provision.go` | `DONE` | CREATE SCHEMA, run migrations, GRANT USAGE |
| 2.8 | Implement personal schema provisioning | `pkg/store/pgstore/provision.go` | `DONE` | CREATE SCHEMA, run migrations, GRANT USAGE |
| 2.9 | Implement per-org connection pool manager | `pkg/store/pgstore/pool.go` | `DONE` | Pool per org DB, search_path + RLS session vars, lazy double-check locking |
| 2.10 | Implement PGStore for sessions | `pkg/store/pgstore/sessions.go` | `DONE` | ADK session.Service + Astonish-specific methods |
| 2.11 | Implement PGStore for memories | `pkg/store/pgstore/memories.go` | `DONE` | pgvector cosine distance search |
| 2.12 | Implement PGStore for credentials | `pkg/store/pgstore/credentials.go` | `DONE` | Encrypted BYTEA storage, full CredentialStore interface |
| 2.13 | Implement PGStore for apps | `pkg/store/pgstore/apps.go` | `DONE` | AppStore + AppStateStore |
| 2.14 | Implement PGStore for flows | `pkg/store/pgstore/flows.go` | `DONE` | DB-only (no taps in PG mode) |
| 2.15 | Implement PGStore for scheduler | `pkg/store/pgstore/scheduler.go` | `DONE` | Combined JSON payload storage |
| 2.16 | Implement PGStore for fleets | `pkg/store/pgstore/fleets.go` | `DONE` | FleetTemplateStore + FleetPlanStore |
| 2.17 | Implement PGStore for skills | `pkg/store/pgstore/skills.go` | `DONE` | |
| 2.18 | Implement PGStore for audit | `pkg/store/pgstore/audit.go` | `DONE` | Append-only, TeamManagementStore included |
| 2.19 | Set up RLS policies on shared tables | `migrations/org/001_init.sql` | `DONE` | team_memberships RLS policy |
| 2.20 | Set up PostgreSQL role permissions | `pkg/store/pgstore/provision.go` | `DONE` | EnsureRoles: astonish_platform_admin + astonish_app, audit INSERT-only grant |
| 2.21 | Add config parsing for `storage.backend: postgres` | `pkg/config/app_config.go` | `DONE` | StorageConfig + PostgresConfig structs |
| 2.22 | Wire backend selection in daemon.Run() | `pkg/daemon/run.go` | `DONE` | pgstore.NewPlatformServices + TenantMiddleware |
| 2.23 | Compile-time interface assertions | `pkg/store/pgstore/assertions.go` | `DONE` | All 18 PG types verified against store interfaces |
| 2.24 | Platform services + tenant middleware | `pkg/store/pgstore/platform_services.go` | `DONE` | NewPlatformServices, TenantContext, TenantMiddleware |
| 2.25 | Run existing test suite against PG backend | Tests | `DEFERRED` | Requires live PG instance; all tests pass with file backend |

**Phase 2 Status: COMPLETE** — 24/25 tasks done. 1 deferred (live PG integration tests). All PG stores implemented with compile-time assertions.

### Phase 3: Authentication & Identity

**Goal:** Replace the single-user device-auth with proper multi-user auth supporting built-in and OIDC modes.

**Target:** Weeks 5-8

| # | Task | Package/Files | Status | Notes |
|---|------|--------------|--------|-------|
| 3.1 | Implement user registration + login | `pkg/api/auth_platform.go` | `DONE` | Email/password, bcrypt, register/login/logout/me/setup-status handlers |
| 3.2 | Implement JWT generation and validation | `pkg/api/jwt.go`, `jwt_test.go` | `DONE` | HMAC-SHA256 dual-token (access 15min + refresh 90d), `PlatformClaims`, 6 tests pass |
| 3.3 | Implement OIDC provider flow | `pkg/api/oidc.go` | `DEFERRED` | Authorization Code + PKCE — deferred to post-Phase 3 |
| 3.4 | Implement OIDC team auto-mapping | `pkg/api/oidc.go` | `DEFERRED` | Map OIDC groups claim to team memberships — deferred to post-Phase 3 |
| 3.5 | Platform auth middleware (JWT validation) | `pkg/api/auth_platform_middleware.go` | `DONE` | Validates JWT cookie, populates `TenantContext` + `PlatformUser`, loopback bypass, `/api/auth/*` bypass |
| 3.6 | Implement RequestContext injection | `pkg/api/auth_platform_middleware.go` | `DONE` | Extracts user/org/team from JWT, sets `TenantContext` in context via `pgstore.TenantMiddleware` |
| 3.7 | Implement team context override | `pkg/api/auth_platform_middleware.go` | `DONE` | X-Astonish-Team header override (membership validation deferred to Phase 4 scope handlers) |
| 3.8 | Implement org/team CRUD handlers | `pkg/api/team_handlers.go` | `DONE` | List/create/get/delete teams, add/remove/set-role members, org info endpoint, role-based access |
| 3.9 | Implement user invitation flow | `pkg/api/invite_handlers.go` | `DEFERRED` | Invite by email — deferred to post-Phase 3 |
| 3.10 | Implement automated org provisioning on registration | `pkg/api/auth_platform.go` | `DONE` | First registration creates default org + "general" team + personal schema, makes user org owner |
| 3.11 | Add auth config parsing | `pkg/config/app_config.go` | `DONE` | `PlatformAuthConfig`, `OIDCConfig`, JWT secret, TTLs, registration toggle, default org settings |
| 3.12 | Test JWT auth flows | `pkg/api/jwt_test.go` | `DONE` | 6 tests: generate/validate access+refresh, expired token, wrong signing method, claims preservation, refresh-as-access rejection |
| 3.13 | Test team membership and role enforcement | Tests | `DEFERRED` | Requires live PG — deferred with task 2.25 |
| 3.14 | Wire platform auth into daemon | `pkg/daemon/run.go` | `DONE` | Creates `PlatformAuth` when postgres backend, passes to `WithPlatformAuth()` |
| 3.15 | Wire platform auth into Studio server | `pkg/launcher/studio.go` | `DONE` | `WithPlatformAuth` option, registers auth+team routes, applies JWT middleware chain |
| 3.16 | Frontend: auth API client | `web/src/api/auth.ts` | `DONE` | register, login, refresh, logout, checkAuth, getSetupStatus |
| 3.17 | Frontend: auth state hook | `web/src/hooks/useAuth.ts` | `DONE` | Auth state management, periodic token refresh (12min), login/register/logout callbacks |
| 3.18 | Frontend: login/register page | `web/src/components/LoginPage.tsx` | `DONE` | Login/register with first-setup detection, password visibility toggle, error display |
| 3.19 | Frontend: platform mode detection + auth gating | `web/src/App.tsx` | `DONE` | Probes `/api/auth/setup-status`, gates SetupWizard behind auth, conditional rendering |

**Phase 3 Status: COMPLETE** — 15/19 tasks done. 4 deferred (OIDC flow, OIDC team mapping, email invitations, live PG team role tests).

### Phase 4: Data Scoping, Audit & Migration

**Goal:** Wire all handlers through RequestContext so every data operation is team/user-scoped. Add audit logging. Implement file→database migration for users transitioning from personal to platform mode.

**Target:** Weeks 7-10

#### 4A: Data Scoping

| # | Task | Package/Files | Status | Notes |
|---|------|--------------|--------|-------|
| 4.1 | Update session handlers for scoped access | `pkg/api/session_handlers.go` | `DONE` | Platform mode branches use `svc.Sessions` + `effectiveUserID(r)`, personal falls back to `cm.fileStore()` |
| 4.2 | Update chat handlers for scoped access | `pkg/api/chat_handlers.go`, `chat_runner.go`, `chat_utils.go` | `DONE` | `ChatRunner.UserID` field, `userID` param threaded through `persistRunError`, `persistSessionMessage`, `readArtifactContentFromSession`, `persistDistillPreview/Saved`, `persistAppPreview`, `handleSlashCommand`; fleet handlers updated with `effectiveUserID(r)` and `DefaultUserID()` |
| 4.3 | Update memory handlers for scoped access | `pkg/api/` memory handlers | `DONE` | No code changes needed — `store.Services.Memory/MemoryMgr` already wired through context middleware; memory data accessed via agent tool context, not HTTP handlers directly |
| 4.4 | Update credential handlers for scoped access | `pkg/api/credentials_handlers.go`, `request_helpers.go` | `DONE` | CRUD handlers use `effectiveCredentialStore(r)` (checks `svc.Credentials` from context, falls back to file-based singleton); master key handlers personal-mode only (`isPlatformMode` guard); response types use `store.Credential/CredentialType` |
| 4.5 | Update app handlers for scoped access | `pkg/api/app_handlers.go` | `DONE` | CRUD already had dual-path (`svc.Apps` → `apps.*` fallback); data/state handlers deferred (require per-tenant SQLite management) |
| 4.6 | Update flow handlers for scoped access | `pkg/api/handlers.go`, `flowstore_handler.go` | `DEFERRED` | Requires per-tenant flowstore instances; flow handlers deeply coupled to filesystem scanning. Store interface exists but insufficient for full dual-path |
| 4.7 | Update scheduler handlers for scoped access | `pkg/api/scheduler_handlers.go` | `DEFERRED` | Requires per-tenant scheduler instances (runtime + store); uses package-level global `schedulerInstance` |
| 4.8 | Update fleet handlers for scoped access | `pkg/api/fleet_handlers.go` | `DONE` | Fleet template CRUD uses `svc.FleetTemplates` dual-path; fleet plan and lifecycle handlers deferred (require per-tenant plan registries + activators) |
| 4.9 | Implement TenantMiddleware wiring | `pkg/api/handlers.go`, `pkg/store/pgstore/platform_services.go` | `DONE` | `TenantMiddleware` applied via `router.Use()` inside `RegisterRoutes()` after `store.Middleware`; reads `TenantContext` + base `Services`, resolves per-tenant stores |
| 4.10 | Implement audit logging middleware | `pkg/api/audit_middleware.go` | `DONE` | `AuditMiddleware` logs all API requests in platform mode; captures user, team, action, resource, IP, status; async writes; wired after auth and tenant middleware in `RegisterRoutes()` |
| 4.11 | Write cross-tenant isolation test suite | `pkg/api/data_scoping_test.go` | `DONE` | Unit tests for `effectiveUserID`, `isPlatformMode`, `effectiveCredentialStore`, `ChatRunner.UserID`; live PG integration tests deferred |
| 4.12 | Write audit log integrity tests | `pkg/api/data_scoping_test.go` | `DONE` | Tests for audit middleware: personal-mode noop, platform-mode logging, X-Forwarded-For handling, status code capture |

#### 4B: File → Database Migration

When a user switches from personal mode (`storage.backend: "file"`) to platform mode (`storage.backend: "postgres"`), existing file-based data must be migrated to the database. This is a **one-time, non-reversible** operation triggered on first platform-mode startup when:
- The platform database has zero organizations (fresh DB)
- File-based data exists at `~/.config/astonish/`

The user is prompted (in Studio UI or CLI) to either migrate or start fresh. Migration creates a platform user account (email from identity config, password set by user), provisions the default org/team, then transfers all data.

**Migration sequence:** credentials → sessions → apps → flows → scheduler → fleets → skills → memory (memory last because re-embedding is slowest)

**After migration:** Data directories are renamed (e.g., `sessions/` → `sessions.pre-migration/`) so the check doesn't trigger again.

| # | Task | Package/Files | Status | Notes |
|---|------|--------------|--------|-------|
| 4.13 | Migration engine core | `pkg/migration/migrate.go` | `DONE` | `Migrator` struct, `Run()`, progress callbacks, file detection |
| 4.14 | Migration: credentials handler | `pkg/migration/credentials.go` | `DONE` | Decrypt with `.store_key`, re-encrypt per PG row, insert |
| 4.15 | Migration: sessions handler | `pkg/migration/sessions.go` | `DONE` | Parse `index.json` + JSONL transcripts, insert meta + events |
| 4.16 | Migration: apps handler | `pkg/migration/apps.go` | `DONE` | Read `*.yaml` from apps dir, insert as rows |
| 4.17 | Migration: flows handler | `pkg/migration/flows.go` | `DONE` | Read `store.json` + installed flow YAMLs, store as JSONB |
| 4.18 | Migration: scheduler handler | `pkg/migration/scheduler.go` | `DONE` | Read `jobs.json`, insert each job |
| 4.19 | Migration: fleet templates + plans handler | `pkg/migration/fleets.go` | `DONE` | Read `fleets/*.yaml` + `fleet_plans/*.yaml`, store as JSONB |
| 4.20 | Migration: skills handler | `pkg/migration/skills.go` | `DONE` | Read `memory/skills/*/SKILL.md`, parse frontmatter, insert into `org_skills` |
| 4.21 | Migration: memory handler | `pkg/migration/memory.go` | `DONE` | Read `.md` files, chunk, re-embed with all-MiniLM-L6-v2, insert into pgvector |
| 4.22 | Migration API handlers + SSE progress | `pkg/api/migration_handlers.go` | `DONE` | `GET /api/migration/status`, `POST /api/migration/start`, `GET /api/migration/progress` (SSE) |
| 4.23 | Migration frontend: MigrationPage | `web/src/components/MigrationPage.tsx` | `DONE` | Account setup form + progress display, pre-fills from identity config |
| 4.24 | Migration frontend: App.tsx integration | `web/src/App.tsx` | `DONE` | Detect `migration_available` in setup-status, show MigrationPage |
| 4.25 | Wire migration detection into daemon startup | `pkg/daemon/run.go` | `DONE` | Detect fresh DB + file data, expose migration API |
| 4.26 | CLI `astonish migrate` command | `cmd/astonish/migrate.go` | `DONE` | Terminal prompts for email/password, runs migration with progress output |

**Phase 4 Status: COMPLETE** — 24/26 tasks done. 2 deferred (flow handler scoping 4.6, scheduler handler scoping 4.7 — both require per-tenant runtime instances).

### Phase 5: Knowledge Sharing

**Goal:** Implement the 3-tier memory system with weighted search across personal, team, and org scopes.

**Target:** Weeks 9-12

| # | Task | Package/Files | Status | Notes |
|---|------|--------------|--------|-------|
| 5.1 | Expand MemoryStore interface + tsvector migration | `pkg/store/memory.go`, migrations | `DONE` | Add/Delete/List methods, MemoryEntry type, tsvector+GIN for all 3 schemas |
| 5.2 | Implement ThreeTierMemoryStore + hybrid search | `pkg/store/three_tier_memory.go`, `pgstore/memories.go` | `DONE` | Composite wrapper, RRF merge, tsvector+pgvector hybrid, scope weighting (1.2/1.0/0.8) |
| 5.3 | Implement "share with team" save flow | `pkg/api/memory_handlers.go` | `DONE` | POST /api/memories/team, POST /api/memories/personal |
| 5.4 | Implement knowledge promotion (team → org) | `pkg/api/memory_handlers.go` | `DONE` | POST /api/memories/promote, admin-only role check |
| 5.5 | Add team/org memory browsing API | `pkg/api/memory_handlers.go` | `DONE` | GET/DELETE team+org memories, POST /api/memories/search cross-tier |
| 5.6 | Update agent memory_save tool for scope selection | `pkg/tools/memory_save.go` | `DONE` | NewPlatformMemorySaveTool writes to PG via store.MemoryStore |
| 5.7 | Update agent memory_search tool for 3-tier search | `pkg/tools/memory_search.go` | `DONE` | NewPlatformMemorySearchTool uses ThreeTierSearcher, results tagged with scope |
| 5.8 | Test memory isolation between tiers | `pkg/store/three_tier_memory_test.go` | `DONE` | 4 isolation tests (personal/team/org/delete) |
| 5.9 | Test cross-tier search accuracy and weighting | `pkg/store/three_tier_memory_test.go` | `DONE` | 7 tests (cross-tier, weighting order, category, nil stores, limit, dedup, minScore) |

**Phase 5 Status: COMPLETE** — 9/9 tasks done. ThreeTierMemoryStore with hybrid pgvector+tsvector search fully implemented.

### Phase 6: App Sharing & Sandbox Isolation

**Goal:** Implement app publish/fork/promote lifecycle and per-org sandbox network isolation.

**Target:** Weeks 11-14

| # | Task | Package/Files | Status | Notes |
|---|------|--------------|--------|-------|
| 6.1 | Implement app publish-to-team | `pkg/api/app_sharing_handlers.go` | `DONE` | Copy personal app to team schema |
| 6.2 | Implement app fork | `pkg/api/app_sharing_handlers.go` | `DONE` | Copy team/org app to personal schema |
| 6.3 | Implement app promote to org | `pkg/api/app_sharing_handlers.go` | `DONE` | Admin-only, copy to org_apps (JSONB) |
| 6.4 | Implement per-user app state for shared apps | `pkg/store/pgstore/apps.go` | `DONE` | pgAppStateStore.userID scopes (app_id, user_id) |
| 6.5 | Create per-org bridge networks in Incus | `pkg/sandbox/org_network.go` | `DONE` | org-{slug}-br0 with isolated subnet, org profile |
| 6.6 | Update container creation for org-scoped networking | `pkg/sandbox/overlay.go` | `DONE` | CreateOverlayContainerWithProfiles + org profile |
| 6.7 | Update container naming convention | `pkg/sandbox/incus.go` | `DONE` | OrgSessionContainerName, OrgFleetContainerName |
| 6.8 | Update credential injection for team scope | `pkg/sandbox/node.go` | `DONE` | SetOrgContext propagated to LazyNodeClient |
| 6.9 | Test app sharing lifecycle | `pkg/api/app_sharing_handlers_test.go` | `DONE` | 12 tests: publish, fork, promote, RBAC, full lifecycle |
| 6.10 | Test sandbox network isolation between orgs | `pkg/sandbox/org_network_test.go` | `DONE` | 11 tests: naming, subnets, bridge limits, pool context |

**Phase 6 Status: COMPLETE** — 10/10 tasks done. App publish/fork/promote lifecycle and per-org bridge networks.

### Phase 7: Frontend

**Goal:** Build all new UI components for multi-tenant mode.

**Target:** Weeks 8-16 (parallel with backend phases)

| # | Task | Package/Files | Status | Notes |
|---|------|--------------|--------|-------|
| 7.1 | Login/register page | `web/src/components/` | `DONE` | Implemented as Phase 3.18 — `LoginPage.tsx` |
| 7.2 | OIDC login flow | `web/src/components/LoginPage.tsx` | `DONE` | SSO button redirects to `/api/auth/oidc/login` when auth_mode=oidc |
| 7.3 | Org switcher component | `web/src/components/TopBar.tsx` | `DONE` | Org badge + user menu in TopBar (platform mode only) |
| 7.4 | Team switcher component | `web/src/components/TopBar.tsx` | `DONE` | Dropdown in TopBar, calls onTeamChange; teams loaded from `/api/teams` |
| 7.5 | Team management page | `web/src/components/TeamManagement.tsx` | `DONE` | Full CRUD: create/delete teams, manage members/roles |
| 7.6 | App catalog page | `web/src/components/AppCatalog.tsx` | `DONE` | Three-tab (Personal/Team/Org) with publish/fork/promote actions |
| 7.7 | Knowledge browser page | `web/src/components/KnowledgeBrowser.tsx` | `DONE` | Cross-tier search, browse team/org memories, add new, promote |
| 7.8 | Memory sharing prompt in chat | `web/src/components/chat/MemorySharingPrompt.tsx` | `DONE` | Inline "Share with team" button after memory_save tool result |
| 7.9 | Audit log viewer | `web/src/components/AuditViewer.tsx` | `DONE` | Admin-only, filterable table with pagination + auto-refresh |
| 7.10 | Update API client for auth headers | `web/src/api/` | `DONE` | Implemented as Phase 3.16 — `auth.ts` with cookie-based JWT |
| 7.11 | Update session list for team context | Backend scoping | `DONE` | Sessions already scoped by TenantMiddleware (Phase 4A) |
| 7.12 | E2E tests for all new UI flows | Tests | `NOT STARTED` | Deferred — requires browser test infrastructure |

**Phase 7 Status: COMPLETE** — 11/12 tasks done. 1 deferred (E2E browser tests). All platform UI components built and integrated.

### Phase 8: Platform CLI & Migration Tools

**Goal:** Build CLI commands for platform administration and data migration from file-based to PostgreSQL.

**Target:** Weeks 14-16

| # | Task | Package/Files | Status | Notes |
|---|------|--------------|--------|-------|
| 8.1 | Implement `astonish platform init` | `cmd/astonish/platform.go` | `DONE` | Creates roles, runs platform migrations, verifies connectivity |
| 8.2 | Implement `astonish platform org create` | `cmd/astonish/platform.go` | `DONE` | Provisions org DB, default "general" team, optional owner |
| 8.3 | Implement `astonish platform org invite` | `cmd/astonish/platform.go` | `DONE` | Creates/finds user, adds to org+team, temp password |
| 8.4 | Implement `astonish platform migrate --from-file` | `cmd/astonish/` | `DONE (4.26)` | Implemented as `astonish migrate` in Phase 4B |
| 8.5 | Implement `astonish platform status` | `cmd/astonish/platform.go` | `DONE` | Shows org/team/user counts, PG version, per-org details |
| 8.6 | Test migration from file to PG round-trip | `pkg/migration/migrate_test.go` | `DONE` | 16 tests: data detection, marker lifecycle, format round-trips |

**Phase 8 Status: COMPLETE** — 6/6 tasks done. Full platform CLI with `init`, `status`, `org create/list/invite`, and `migrate`.

### Phase 8B: Setup Wizard — Deployment Mode

**Goal:** Integrate platform mode configuration into both the CLI setup wizard (`astonish setup`) and the Studio UI Setup Wizard, so users never need to manually edit config.yaml to enable platform mode.

**Design Decisions:**
- **Deployment mode choice** is the first substantive step in both wizards (after Welcome screen)
- **Two modes:** Personal (file-based, zero-config) and Platform (PostgreSQL, multi-user)
- **Automatic database creation:** When Platform is selected, the wizard connects to PostgreSQL using admin credentials, creates the `astonish_platform` database, creates roles, and runs all migrations — no manual SQL required
- **Credential model:** A single admin DSN is stored in config. It must have `CREATEDB` privilege. The same credentials are used at runtime for all connections (platform + org pools). The `astonish_app` role + grants provide defense-in-depth RLS. Production hardening (switching runtime to `astonish_app` role) is a future step.
- **JWT secret:** Auto-generated (32 random bytes, hex-encoded) during setup, stored in config
- **Daemon auto-init:** If `storage.backend == "postgres"` but the platform DB is not initialized, the daemon attempts `BootstrapPlatform()` automatically before failing
- **UI restart requirement:** When Platform mode is configured via the UI Setup Wizard, the daemon must restart to switch from file-backed to PostgreSQL-backed mode. The UI shows "Restart Required" after successful initialization.
- **API guard:** `POST /api/platform/init` only works when the system is NOT already in platform mode

**Shared Bootstrap Logic:** `pgstore.BootstrapPlatform(ctx, platformDSN, suffix)` — used by CLI wizard, `platform init` CLI command (admin-credentials flow with auto-suffix), daemon auto-init, and `POST /api/platform/init` API handler.

| # | Task | Package/Files | Status | Notes |
|---|------|--------------|--------|-------|
| 8B.1 | `BootstrapPlatform()` shared function | `pkg/store/pgstore/bootstrap.go` | `DONE` | Create DB + roles + migrations in one call; `BuildDSN()` helper |
| 8B.2 | `GenerateJWTSecret()` helper | `pkg/config/app_config.go` | `DONE` | 32 random bytes → 64 hex chars |
| 8B.3 | `POST /api/platform/init` API endpoint | `pkg/api/platform_setup_handlers.go` | `DONE` | Accept PG params, bootstrap, save config; also `GET /api/platform/mode` and `/init/status` |
| 8B.4 | Route registration | `pkg/api/handlers.go` | `DONE` | `/api/platform/init`, `/api/platform/mode`, `/api/platform/init/status` |
| 8B.5 | Refactor `platform init` CLI to use shared bootstrap | `cmd/astonish/platform.go` | `DONE` | Replace inline logic with BootstrapPlatform(); removed pgx import |
| 8B.6 | CLI wizard deployment mode step | `cmd/astonish/setup.go` | `DONE` | huh form: mode choice, PG params, org defaults; calls BootstrapPlatform() |
| 8B.7 | Daemon auto-init on startup | `pkg/daemon/run.go` | `DONE` | Try bootstrap if NewPlatformServices fails |
| 8B.8 | Frontend platform init API | `web/src/api/platform.ts` | `DONE` | `initializePlatform()`, `getDeploymentMode()`, `getPlatformInitStatus()` |
| 8B.9 | UI Setup Wizard deployment mode step | `web/src/components/SetupWizard.tsx` | `DONE` | New Step 1: Personal/Platform cards, PG form, org fields, restart banner; 10 steps total |
| 8B.10 | Tests | Various | `DONE` | 8 BuildDSN tests, 8 JWT tests, 20 handler/cleanPGError tests — all pass |

**Phase 8B Status: COMPLETE** — All 10 tasks done. CLI + UI wizards both support deployment mode selection with automated DB bootstrap.

---

## Known Deferred Work

The following items were designed but explicitly deferred during implementation. They are tracked here as a consolidated reference for future planning.

### Authentication & Identity
| Item | Phase | Reason |
|------|-------|--------|
| OIDC provider flow (Authorization Code + PKCE) | 3.3 | Requires external IdP for testing; builtin auth covers MVP |
| OIDC team auto-mapping from group claims | 3.4 | Depends on 3.3 |
| Email-based user invitation flow | 3.9 | Requires email transport; CLI `org invite` covers MVP |
| Team membership validation on `X-Astonish-Team` header | 3.7 | Deferred to Phase 4 scope handlers; currently trusts the header |

### Data Scoping
| Item | Phase | Reason |
|------|-------|--------|
| Flow handler scoping | 4.6 | Flow handlers deeply coupled to filesystem scanning; needs per-tenant flowstore instances |
| Scheduler handler scoping | 4.7 | Uses package-level global `schedulerInstance`; needs per-tenant scheduler runtime |

### API Endpoints
| Item | Phase | Notes |
|------|-------|-------|
| `GET/PUT /api/user/profile` | — | User profile CRUD; JWT `me` endpoint exists but no profile editing |
| `GET /api/user/orgs` | — | List user's orgs; needed when multi-org support is added |
| `PUT /api/org` | — | Update org settings (name, logo, etc.) |
| `GET /api/org/members` | — | List all org members; partially covered by team member endpoints |
| `PUT /api/teams/{id}` | 3.8 | Update team settings (rename, description); create/delete/members exist |

### Multi-Org Support
| Item | Phase | Notes |
|------|-------|-------|
| Org switcher (user in >1 org) | 7.3 | TopBar shows org badge but doesn't support switching; single-org assumed for MVP |
| Cross-org user lookup | — | Required for inviting users from other orgs |

### Testing
| Item | Phase | Reason |
|------|-------|--------|
| Live PG integration tests | 2.25/3.13 | Requires running PostgreSQL; all unit tests use mocks/file backend |
| E2E browser tests for platform UI | 7.12 | Requires browser test infrastructure (Playwright or similar) |

### Infrastructure
| Item | Phase | Notes |
|------|-------|-------|
| Runtime role switching (`astonish_app` vs admin DSN) | — | Defense-in-depth; currently uses admin DSN at runtime |
| HANA HDI store implementation | — | Interface designed; needs `pkg/store/hanastore/` |
| Connection pooling refinement (idle timeouts, pool sizing per org) | 2.9 | Basic pool implemented; production tuning deferred |

---

## Cross-Reference to Existing Architecture Docs

This design touches many existing subsystems. See these docs for current architecture details:

> **Note:** Most of these docs describe the original file-based personal mode. They have not yet been updated to document the platform mode store abstraction. The implementations are dual-path (file + PG), but the docs below still reflect only the file-based approach.

- [Memory & Knowledge](memory.md) — current file-based memory system being abstracted
- [Sessions](sessions.md) — current JSONL session storage being abstracted
- [Credentials](credentials.md) — current encrypted credential store being abstracted
- [Sandbox & Containerization](sandbox.md) — container isolation being extended with per-org networking
- [Generative UI / Apps](generative-ui.md) — app system being extended with publish/fork
- [Flows](flows.md) — flow storage being abstracted (handler scoping deferred, see Known Deferred Work)
- [Fleet](fleet.md) — fleet system being scoped to teams
- [Daemon & Scheduler](daemon-scheduler.md) — scheduler being scoped to teams (handler scoping deferred)
- [API & Studio](api-studio.md) — API layer being extended with auth and team context
- [Skills](skills.md) — skills being extended with org-level sharing
- [Channels](channels.md) — channels being scoped to teams
- [Configuration](configuration.md) — config being extended with storage and auth sections
- [Chat Rendering Pipeline](chat-rendering-pipeline.md) — authoritative reference for SSE transport (unchanged by platform mode)
- [Testing Chat Scenarios](testing-chat-scenarios.md) — test infrastructure (E2E platform tests deferred)
