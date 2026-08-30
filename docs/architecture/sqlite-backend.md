# SQLite Backend Architecture

## Overview

Astonish supports two database backends, selected by configuration:

| Backend | Engine | Dependencies | Use Case |
|---------|--------|--------------|----------|
| **SQLite** (default) | `modernc.org/sqlite` (pure Go) | None — embedded in binary | Single user, small teams, local/edge deployment |
| **PostgreSQL** | `pgx` + pgvector | External PostgreSQL server | Large teams, multi-org SaaS, cloud deployment |

Both backends implement identical store interfaces (`pkg/store/`), use the same JWT authentication, the same multi-tenant data model (orgs, teams, users), and expose the same API. The only difference is the persistence engine and what the operator must provision.

This document supersedes the "Personal mode" concept. The file-based storage backend (`pkg/store/filestore/`) and the device-auth flow are removed. All deployments — even single-user local — use the platform architecture with SQLite as the lightweight default.

### Why Remove Personal Mode

The dual-backend architecture (files vs PostgreSQL) created two completely separate code paths:

- **Two search systems**: chromem-go + in-memory BM25 (personal) vs pgvector + tsvector (platform)
- **Two auth systems**: device authorization codes (personal) vs JWT + OIDC (platform)
- **Two service wiring paths**: static singleton (personal) vs per-request TenantMiddleware (platform)
- **Two data layouts**: scattered JSON/gob/YAML files vs structured relational tables
- **No upgrade path**: moving from personal to platform required manual data migration

By converging on a single architecture with SQLite as the lightweight backend, we get:

- One auth system (JWT) that works identically everywhere
- One data model (orgs/teams/users) that scales from solo use to enterprise
- One set of API handlers, one set of tests, one mental model
- A trivial upgrade path: switch `storage.backend` from `sqlite` to `postgres` and run a migration tool

### Key Architectural Decisions

| Decision | Choice | Rationale |
|----------|--------|-----------|
| SQLite driver | `modernc.org/sqlite` (pure Go) | CGO-free builds (`CGO_ENABLED=0`); already a project dependency |
| Vector storage | BLOB columns + Go-side cosine similarity | No CGO extensions available for `modernc.org/sqlite`; brute-force is fast at local scale |
| Full-text search | FTS5 virtual tables | Native to SQLite, no extensions needed, supported by `modernc.org/sqlite` |
| Hybrid fusion | Reciprocal Rank Fusion in Go | Same algorithm as pgstore; results are backend-agnostic |
| Embedding model | Hugot (local ONNX, all-MiniLM-L6-v2) | Already used by pgstore; pure Go via GoMLX |
| File layout | Separate `.db` files per tenant boundary | Maps to PG's per-org DB + per-team schema; enables independent backup/restore |
| Concurrency | WAL mode + `database/sql` connection pool | Supports concurrent reads; writes are serialized (acceptable for local/small-team) |
| Multi-tenancy | Same `TenantMiddleware` as pgstore | Handlers remain completely backend-agnostic |
| Auth | JWT everywhere (same as pgstore) | Unified; CLI stores token locally after `astonish login`, browser gets HttpOnly cookies |

---

## Database File Layout

```
~/.local/share/astonish/
├── platform.db                    # Platform-level: users, orgs, login sessions, OIDC, settings
├── orgs/
│   └── {org_slug}/
│       ├── org.db                 # Org-level: teams, memberships, org memories, org skills
│       ├── teams/
│       │   ├── {team_slug}.db     # Team-scoped: sessions, memories, credentials, apps, flows
│       │   └── ...
│       └── personal/
│           ├── {user_id}.db       # User-private: personal memories, apps, sessions, flows
│           └── ...
```

### Why Separate Files

The PostgreSQL backend uses separate databases per org and separate schemas per team. SQLite has no schema concept, so we use separate `.db` files to achieve equivalent isolation:

- **Structural isolation**: A connection to `team_general.db` cannot query `team_sre.db`. Same guarantee as PG's per-database isolation.
- **Independent lifecycle**: Individual databases can be backed up, restored, or deleted without affecting others.
- **No table-name collisions**: Each database uses clean, unqualified table names (e.g., `sessions`, `memories`) — identical DDL across all team databases.
- **Connection pooling**: Each `.db` file gets its own `*sql.DB` pool. WAL mode allows concurrent readers per file.
- **Atomic provisioning**: Creating a new team = creating a new file + running migrations. No ALTER TABLE, no shared-table coordination.

### Data Directory Selection

The default data directory follows XDG conventions:

| Platform | Default Path | Override |
|----------|-------------|----------|
| Linux | `~/.local/share/astonish/` | `$XDG_DATA_HOME/astonish/` or `storage.sqlite.data_dir` config |
| macOS | `~/Library/Application Support/astonish/` | `storage.sqlite.data_dir` config |

Configuration data (YAML configs, agent definitions) remains in `~/.config/astonish/`. The data directory holds only SQLite databases.

### macOS Daemon Startup Diagnosis

On macOS, distinguish three independent readiness boundaries:

1. **launchd service startup** — launchd starts the configured LaunchAgent process.
2. **HTTP readiness** — the daemon has completed synchronous setup and logged `HTTP server listening`.
3. **First-session readiness** — Studio has constructed its cached ChatAgent for the first usable chat request.

A slow launchd invocation does not by itself show whether the HTTP listener or the first Studio session is slow. With `sandbox.enabled: false`, Astonish bypasses the Docker+Incus sandbox branch; this does not bypass database, provider, embedding, skills, MCP-cache, scheduler, fleet, channel, or Studio initialization.

#### Locations and configuration

For a per-user installation, inspect the installed LaunchAgent plist under `~/Library/LaunchAgents/` and use its configured `ProgramArguments`, `WorkingDirectory`, and `EnvironmentVariables` as the source of truth. The LaunchAgent's standard-output and standard-error paths normally identify the daemon log and error log; inspect those configured paths rather than assuming they are inherited from an interactive shell.

Keep these locations separate when troubleshooting:

- macOS configuration and log locations are under the user Library/Application Support and LaunchAgent configuration selected by the installation.
- SQLite defaults to `~/Library/Application Support/astonish/`.
- `storage.sqlite.data_dir` explicitly overrides the SQLite location.
- YAML configuration and agent definitions remain under `~/.config/astonish/`.

Do not put the SQLite data directory on a synchronized, cloud-backed, removable, or network-mounted location. Prefer a local filesystem path so WAL, locking, and file-latency behavior remain predictable.

#### Safe troubleshooting flow

1. Confirm `sandbox.enabled: false` in the effective daemon configuration, then restart the installed LaunchAgent rather than starting a second interactive daemon.
2. Collect timestamped `daemon.log` and `daemon.err` entries from process start through `HTTP server listening`. Read the ordered elapsed phase records to identify whether time is spent in store open, migration, embedding/provider setup, channels, scheduler/fleet restoration, or listener setup.
3. Treat repeating roughly 5- or 10-second plateaus as evidence to investigate SQLite lock/busy-timeout behavior. Check for duplicate or repeatedly restarted LaunchAgent processes, and verify explicit LaunchAgent environment variables do not select an unexpected configuration or data directory.
4. Verify that the effective SQLite data directory is local and that its parent directories are writable by the LaunchAgent user.
5. After HTTP readiness, make one Studio chat request and then a second equivalent request. Compare the cold `component=studio-chat` cache-miss and `component=chat-factory` elapsed records with the second request, which should not trigger another cached Studio initialization record.

Enabled channels can intentionally require an expensive ChatAgent construction. The daemon begins Studio chat pre-warming as soon as the HTTP listener is available, and only then starts the optional channel bootstrap. This avoids two heavyweight ChatAgent factories contending during a cold start: Studio sessions get priority, while channel status remains unavailable or transitional until its bootstrap completes. Inbound channel messages are not accepted until their adapter has started. Separately, the first Studio session may lazily initialize provider, embedding, skills, or MCP-cache work even while sandboxing is disabled. These records identify the bottleneck; they do not establish a fixed root cause or imply that functional subsystems should be disabled.

---

## Schema Design

### Type Mapping (PostgreSQL to SQLite)

| PostgreSQL | SQLite | Notes |
|-----------|--------|-------|
| `UUID` | `TEXT` | Store as lowercase hex with hyphens; generate with `uuid.New()` in Go |
| `TIMESTAMPTZ` | `TEXT` | RFC 3339 format; SQLite's `datetime()` functions work on this |
| `JSONB` | `TEXT` | Store as JSON string; use `json_extract()` for queries |
| `BYTEA` | `BLOB` | Direct mapping |
| `BIGSERIAL` | `INTEGER PRIMARY KEY` | SQLite auto-increment via `rowid` |
| `TEXT[]` | `TEXT` | JSON array string |
| `INET` | `TEXT` | Store IP as string |
| `vector(384)` | `BLOB` | 1536 bytes (384 x float32, little-endian) |
| `BOOLEAN` | `INTEGER` | 0/1 |
| RLS policies | Application-level enforcement | SQLite has no RLS; enforced by connection scoping |

### Platform Database (`platform.db`)

```sql
-- Schema version tracking
CREATE TABLE schema_migrations (
    version INTEGER PRIMARY KEY,
    applied_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE organizations (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    slug TEXT NOT NULL UNIQUE,
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','suspended','decommissioned')),
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    settings TEXT DEFAULT '{}'
);

CREATE TABLE users (
    id TEXT PRIMARY KEY,
    email TEXT NOT NULL UNIQUE,
    display_name TEXT NOT NULL,
    password_hash TEXT,
    oidc_subject TEXT,
    oidc_issuer TEXT,
    platform_role TEXT NOT NULL DEFAULT 'member' CHECK (platform_role IN ('superadmin','admin','member')),
    status TEXT NOT NULL DEFAULT 'active',
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    last_login_at TEXT,
    UNIQUE(oidc_issuer, oidc_subject)
);

CREATE TABLE org_memberships (
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    org_id TEXT NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    role TEXT NOT NULL DEFAULT 'member' CHECK (role IN ('owner','admin','member')),
    joined_at TEXT NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (user_id, org_id)
);

CREATE TABLE oidc_providers (
    id TEXT PRIMARY KEY,
    org_id TEXT REFERENCES organizations(id),
    name TEXT NOT NULL,
    issuer_url TEXT NOT NULL,
    discovery_url TEXT,
    client_id TEXT NOT NULL,
    client_secret TEXT NOT NULL,
    scopes TEXT DEFAULT '["openid","email","profile"]',
    team_claim TEXT,
    enabled INTEGER NOT NULL DEFAULT 1,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE(org_id, issuer_url)
);

CREATE TABLE login_sessions (
    token_hash TEXT PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    org_id TEXT NOT NULL REFERENCES organizations(id),
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    expires_at TEXT NOT NULL,
    user_agent TEXT,
    ip_address TEXT
);

CREATE TABLE user_channels (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    channel_type TEXT NOT NULL,
    external_id TEXT NOT NULL,
    display_name TEXT,
    enabled INTEGER NOT NULL DEFAULT 1,
    verified INTEGER NOT NULL DEFAULT 0,
    verified_at TEXT,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE(channel_type, external_id)
);

CREATE TABLE platform_secrets (
    key TEXT PRIMARY KEY,
    value BLOB NOT NULL,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE platform_settings (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL,
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE tool_index (
    id TEXT PRIMARY KEY,
    content TEXT NOT NULL,
    embedding BLOB,
    metadata TEXT,
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE platform_mcp_servers (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    command TEXT,
    args TEXT,
    env TEXT,
    transport TEXT,
    url TEXT,
    enabled INTEGER NOT NULL DEFAULT 1,
    cached_tools TEXT,
    created_by TEXT,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);
```

### Organization Database (`orgs/{slug}/org.db`)

```sql
CREATE TABLE schema_migrations (
    version INTEGER PRIMARY KEY,
    applied_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE teams (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    slug TEXT NOT NULL UNIQUE,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    settings TEXT DEFAULT '{}'
);

CREATE TABLE team_memberships (
    user_id TEXT NOT NULL,
    team_id TEXT NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    role TEXT NOT NULL DEFAULT 'member' CHECK (role IN ('admin','member','viewer')),
    joined_at TEXT NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (user_id, team_id)
);

CREATE TABLE org_memories (
    id TEXT PRIMARY KEY,
    chunk_text TEXT NOT NULL,
    embedding BLOB,
    category TEXT,
    source_path TEXT,
    metadata TEXT,
    promoted_by TEXT,
    promoted_from_team TEXT,
    session_id TEXT,
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);

-- FTS5 index for org memories keyword search
CREATE VIRTUAL TABLE org_memories_fts USING fts5(
    chunk_text,
    content='org_memories',
    content_rowid='rowid'
);

-- Triggers to keep FTS5 synchronized
CREATE TRIGGER org_memories_ai AFTER INSERT ON org_memories BEGIN
    INSERT INTO org_memories_fts(rowid, chunk_text) VALUES (new.rowid, new.chunk_text);
END;
CREATE TRIGGER org_memories_ad AFTER DELETE ON org_memories BEGIN
    INSERT INTO org_memories_fts(org_memories_fts, rowid, chunk_text) VALUES('delete', old.rowid, old.chunk_text);
END;
CREATE TRIGGER org_memories_au AFTER UPDATE ON org_memories BEGIN
    INSERT INTO org_memories_fts(org_memories_fts, rowid, chunk_text) VALUES('delete', old.rowid, old.chunk_text);
    INSERT INTO org_memories_fts(rowid, chunk_text) VALUES (new.rowid, new.chunk_text);
END;

CREATE TABLE org_skills (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    content TEXT NOT NULL,
    frontmatter TEXT,
    created_by TEXT,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE org_mcp_servers (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    command TEXT,
    args TEXT,
    env TEXT,
    transport TEXT,
    url TEXT,
    enabled INTEGER NOT NULL DEFAULT 1,
    cached_tools TEXT,
    created_by TEXT,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE org_apps (
    id TEXT PRIMARY KEY,
    slug TEXT NOT NULL UNIQUE,
    name TEXT NOT NULL,
    description TEXT,
    definition TEXT NOT NULL,
    promoted_by TEXT,
    promoted_from_team TEXT,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE org_audit_log (
    id INTEGER PRIMARY KEY,
    timestamp TEXT NOT NULL DEFAULT (datetime('now')),
    user_id TEXT NOT NULL,
    team_id TEXT,
    action TEXT NOT NULL,
    resource TEXT NOT NULL,
    detail TEXT,
    ip_address TEXT,
    session_id TEXT
);

CREATE TABLE org_encryption_keys (
    id TEXT PRIMARY KEY,
    key_name TEXT NOT NULL UNIQUE,
    key_data BLOB NOT NULL,
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);
```

### Team Database (`orgs/{slug}/teams/{team_slug}.db`)

```sql
CREATE TABLE schema_migrations (
    version INTEGER PRIMARY KEY,
    applied_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE sessions (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    title TEXT,
    message_count INTEGER DEFAULT 0,
    parent_id TEXT,
    fleet_key TEXT DEFAULT '',
    fleet_name TEXT DEFAULT '',
    issue_number INTEGER,
    repo TEXT,
    workspace_dir TEXT DEFAULT '',
    metadata TEXT DEFAULT '{}',
    last_seq INTEGER DEFAULT 0,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE session_events (
    id INTEGER PRIMARY KEY,
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    event_data TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_session_events_session ON session_events(session_id);

CREATE TABLE memories (
    id TEXT PRIMARY KEY,
    created_by TEXT NOT NULL,
    chunk_text TEXT NOT NULL,
    embedding BLOB,
    category TEXT,
    source_path TEXT,
    metadata TEXT,
    session_id TEXT,
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE VIRTUAL TABLE memories_fts USING fts5(
    chunk_text,
    content='memories',
    content_rowid='rowid'
);

CREATE TRIGGER memories_ai AFTER INSERT ON memories BEGIN
    INSERT INTO memories_fts(rowid, chunk_text) VALUES (new.rowid, new.chunk_text);
END;
CREATE TRIGGER memories_ad AFTER DELETE ON memories BEGIN
    INSERT INTO memories_fts(memories_fts, rowid, chunk_text) VALUES('delete', old.rowid, old.chunk_text);
END;
CREATE TRIGGER memories_au AFTER UPDATE ON memories BEGIN
    INSERT INTO memories_fts(memories_fts, rowid, chunk_text) VALUES('delete', old.rowid, old.chunk_text);
    INSERT INTO memories_fts(rowid, chunk_text) VALUES (new.rowid, new.chunk_text);
END;

CREATE TABLE credentials (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    cred_type TEXT NOT NULL,
    encrypted BLOB NOT NULL,
    created_by TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE apps (
    id TEXT PRIMARY KEY,
    slug TEXT NOT NULL UNIQUE,
    name TEXT NOT NULL,
    description TEXT,
    code TEXT,
    version INTEGER DEFAULT 1,
    session_id TEXT,
    published_by TEXT,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE app_state (
    app_id TEXT NOT NULL,
    user_id TEXT NOT NULL,
    key TEXT NOT NULL,
    value TEXT NOT NULL,
    updated_at TEXT NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (app_id, user_id, key)
);

CREATE TABLE flows (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    definition TEXT,
    yaml_content TEXT,
    type TEXT,
    created_by TEXT,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE scheduled_jobs (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    schedule TEXT NOT NULL,
    mode TEXT NOT NULL CHECK (mode IN ('routine','adaptive','fleet_poll')),
    payload TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','paused','disabled')),
    last_run_at TEXT,
    next_run_at TEXT,
    last_status TEXT,
    last_error TEXT,
    consecutive_failures INTEGER DEFAULT 0,
    created_by TEXT,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE fleet_templates (
    id TEXT PRIMARY KEY,
    key TEXT NOT NULL UNIQUE,
    name TEXT,
    definition TEXT NOT NULL,
    created_by TEXT,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE fleet_plans (
    id TEXT PRIMARY KEY,
    key TEXT NOT NULL UNIQUE,
    name TEXT,
    definition TEXT NOT NULL,
    yaml_content TEXT,
    active INTEGER NOT NULL DEFAULT 1,
    created_by TEXT,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE fleet_monitor_state (
    plan_key TEXT PRIMARY KEY,
    state TEXT NOT NULL,
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE drill_reports (
    id TEXT PRIMARY KEY,
    suite TEXT NOT NULL,
    status TEXT NOT NULL,
    summary TEXT,
    duration_ms INTEGER,
    report_data TEXT,
    started_at TEXT,
    finished_at TEXT,
    created_by TEXT,
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE skills (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    content TEXT NOT NULL,
    frontmatter TEXT,
    created_by TEXT,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE mcp_servers (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    command TEXT,
    args TEXT,
    env TEXT,
    transport TEXT,
    url TEXT,
    enabled INTEGER NOT NULL DEFAULT 1,
    cached_tools TEXT,
    created_by TEXT,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE settings (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL,
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE team_audit_log (
    id INTEGER PRIMARY KEY,
    timestamp TEXT NOT NULL DEFAULT (datetime('now')),
    user_id TEXT NOT NULL,
    action TEXT NOT NULL,
    resource TEXT NOT NULL,
    detail TEXT,
    session_id TEXT
);

-- Sandbox sessions (optional, for deployments with sandbox support)
CREATE TABLE sandbox_sessions (
    id TEXT PRIMARY KEY,
    chat_session_id TEXT,
    backend TEXT,
    container_name TEXT,
    template_id TEXT,
    upper_layer_id TEXT,
    state TEXT NOT NULL DEFAULT 'pending' CHECK (state IN ('pending','starting','running','stopped','failed','destroyed')),
    pod_name TEXT,
    node_name TEXT,
    exposed_ports TEXT,
    base_domain TEXT,
    pinned INTEGER NOT NULL DEFAULT 0,
    created_by TEXT,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now')),
    last_active_at TEXT
);

CREATE TABLE chat_session_events (
    chat_session_id TEXT NOT NULL,
    seq INTEGER NOT NULL,
    event_type TEXT NOT NULL,
    payload TEXT,
    producer_pod TEXT,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (chat_session_id, seq)
);
```

### Personal Database (`orgs/{slug}/personal/{user_id}.db`)

```sql
CREATE TABLE schema_migrations (
    version INTEGER PRIMARY KEY,
    applied_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE memories (
    id TEXT PRIMARY KEY,
    chunk_text TEXT NOT NULL,
    embedding BLOB,
    category TEXT,
    source_path TEXT,
    metadata TEXT,
    created_by TEXT,
    session_id TEXT,
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE VIRTUAL TABLE memories_fts USING fts5(
    chunk_text,
    content='memories',
    content_rowid='rowid'
);

CREATE TRIGGER memories_ai AFTER INSERT ON memories BEGIN
    INSERT INTO memories_fts(rowid, chunk_text) VALUES (new.rowid, new.chunk_text);
END;
CREATE TRIGGER memories_ad AFTER DELETE ON memories BEGIN
    INSERT INTO memories_fts(memories_fts, rowid, chunk_text) VALUES('delete', old.rowid, old.chunk_text);
END;
CREATE TRIGGER memories_au AFTER UPDATE ON memories BEGIN
    INSERT INTO memories_fts(memories_fts, rowid, chunk_text) VALUES('delete', old.rowid, old.chunk_text);
    INSERT INTO memories_fts(rowid, chunk_text) VALUES (new.rowid, new.chunk_text);
END;

CREATE TABLE apps (
    id TEXT PRIMARY KEY,
    slug TEXT NOT NULL UNIQUE,
    name TEXT NOT NULL,
    description TEXT,
    code TEXT,
    version INTEGER DEFAULT 1,
    session_id TEXT,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE app_state (
    app_id TEXT NOT NULL,
    key TEXT NOT NULL,
    value TEXT NOT NULL,
    updated_at TEXT NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (app_id, key)
);

CREATE TABLE sessions (
    id TEXT PRIMARY KEY,
    title TEXT,
    message_count INTEGER DEFAULT 0,
    parent_id TEXT,
    fleet_key TEXT DEFAULT '',
    fleet_name TEXT DEFAULT '',
    workspace_dir TEXT DEFAULT '',
    metadata TEXT DEFAULT '{}',
    last_seq INTEGER DEFAULT 0,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE session_events (
    id INTEGER PRIMARY KEY,
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    event_data TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_session_events_session ON session_events(session_id);

CREATE TABLE flows (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    definition TEXT,
    yaml_content TEXT,
    type TEXT,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE credentials (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    cred_type TEXT NOT NULL,
    encrypted BLOB NOT NULL,
    created_by TEXT,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);
```

---

## Vector Search Architecture

### Constraint: CGO-Free

The project builds with `CGO_ENABLED=0` across all targets (Linux amd64/arm64, macOS, Docker). This eliminates C-extension-based solutions:

- `sqlite-vec` requires either CGO (`mattn/go-sqlite3`) or a WASM-based driver (`ncruces/go-sqlite3`)
- Neither is compatible with `modernc.org/sqlite`

### Solution: Application-Level Vector Search

Store vectors as BLOBs in SQLite. Perform similarity computation in Go. This is functionally identical to what `chromem-go` already does (brute-force cosine similarity in memory), but uses SQLite for durable persistence instead of `.gob` files.

```
┌────────────────────────────────────────────────────────────────┐
│                     Vector Search Flow                          │
│                                                                │
│  Query Text                                                    │
│    │                                                           │
│    ├──► Hugot Embedder ──► query_embedding []float32           │
│    │                                                           │
│    ├──► FTS5 Search ─────► keyword_results (id, bm25_score)   │
│    │    (SQLite native)                                        │
│    │                                                           │
│    └──► Vector Search ───► vector_results (id, cosine_score)  │
│         (Go-side)                                              │
│              │                                                  │
│              ├── Load embeddings from SQLite (BLOB column)      │
│              ├── Compute cosine similarity in Go                │
│              └── Return top-K by score                          │
│                                                                │
│  keyword_results + vector_results                              │
│    │                                                           │
│    └──► RRF Fusion (k=60) ──► final ranked results            │
│                                                                │
└────────────────────────────────────────────────────────────────┘
```

### Embedding Storage Format

Embeddings are stored as little-endian `[]float32` BLOBs:

```go
// Serialize: []float32 → []byte (1536 bytes for 384 dimensions)
func serializeEmbedding(vec []float32) []byte {
    buf := make([]byte, len(vec)*4)
    for i, v := range vec {
        binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(v))
    }
    return buf
}

// Deserialize: []byte → []float32
func deserializeEmbedding(data []byte) []float32 {
    vec := make([]float32, len(data)/4)
    for i := range vec {
        vec[i] = math.Float32frombits(binary.LittleEndian.Uint32(data[i*4:]))
    }
    return vec
}
```

### Vector Search Implementation

```go
// Brute-force cosine similarity over all embeddings in the table.
// For typical local usage (< 10,000 memory entries), this completes in < 5ms.
func (s *memoryStore) vectorSearch(ctx context.Context, queryVec []float32, maxResults int, minScore float64) ([]scoredResult, error) {
    rows, err := s.db.QueryContext(ctx, "SELECT id, embedding FROM memories WHERE embedding IS NOT NULL")
    if err != nil {
        return nil, err
    }
    defer rows.Close()

    var results []scoredResult
    for rows.Next() {
        var id string
        var embBlob []byte
        if err := rows.Scan(&id, &embBlob); err != nil {
            continue
        }
        docVec := deserializeEmbedding(embBlob)
        score := cosineSimilarity(queryVec, docVec)
        if score >= minScore {
            results = append(results, scoredResult{ID: id, Score: score})
        }
    }

    sort.Slice(results, func(i, j int) bool { return results[i].Score > results[j].Score })
    if len(results) > maxResults {
        results = results[:maxResults]
    }
    return results, nil
}
```

### In-Memory Vector Index (Optimization)

For databases with more than a few hundred memories, loading all BLOBs per query is wasteful. The `sqlitestore` maintains an **in-memory vector index** that is:

1. **Lazily loaded** on first search query
2. **Incrementally updated** on insert/update/delete operations
3. **Bounded by the database content** — the index is a read-through cache of the BLOB column

```go
type vectorIndex struct {
    mu      sync.RWMutex
    vectors map[string][]float32  // id → embedding
    loaded  bool
}

func (vi *vectorIndex) search(query []float32, maxResults int, minScore float64) []scoredResult {
    vi.mu.RLock()
    defer vi.mu.RUnlock()
    // ... brute-force cosine over vi.vectors
}

func (vi *vectorIndex) add(id string, vec []float32) {
    vi.mu.Lock()
    vi.vectors[id] = vec
    vi.mu.Unlock()
}

func (vi *vectorIndex) remove(id string) {
    vi.mu.Lock()
    delete(vi.vectors, id)
    vi.mu.Unlock()
}
```

The index consumes approximately 1.5 KB per memory entry (384 dims * 4 bytes + map overhead). At 10,000 entries this is ~15 MB — trivial for any modern system.

### FTS5 Keyword Search

SQLite's FTS5 provides BM25-ranked full-text search natively. These virtual tables and their synchronization triggers are SQLite-specific post-DDL objects, not Ent schemas. Tenant database provisioning and every tenant database open apply them after `Schema.Create`; opening an older database repairs missing objects and rebuilds a newly created FTS index from existing memory rows. Failure to create or rebuild a required index aborts that open or provisioning operation rather than disabling keyword retrieval.

```go
func (s *memoryStore) keywordSearch(ctx context.Context, query string, maxResults int) ([]scoredResult, error) {
    // FTS5's built-in bm25() function returns negative scores (lower = better match)
    rows, err := s.db.QueryContext(ctx, `
        SELECT m.id, -fts.rank AS score
        FROM memories_fts fts
        JOIN memories m ON m.rowid = fts.rowid
        WHERE memories_fts MATCH ?
        ORDER BY fts.rank
        LIMIT ?
    `, fts5Query(query), maxResults)
    // ...
}

// Convert natural language query to FTS5 query syntax
// "kubernetes pod crash" → "kubernetes OR pod OR crash"
func fts5Query(input string) string {
    tokens := strings.Fields(input)
    return strings.Join(tokens, " OR ")
}
```

### Hybrid Fusion (RRF)

Identical to the pgstore implementation:

```go
func hybridSearch(vectorResults, keywordResults []scoredResult, k int) []scoredResult {
    scores := make(map[string]float64)
    for rank, r := range vectorResults {
        scores[r.ID] += 1.0 / float64(k+rank+1)
    }
    for rank, r := range keywordResults {
        scores[r.ID] += 1.0 / float64(k+rank+1)
    }
    // Sort by fused score, return top results
}
```

### Performance Characteristics

| Corpus Size | Vector Search (brute-force) | FTS5 Search | Total Hybrid |
|-------------|---------------------------|-------------|--------------|
| 100 entries | < 0.1 ms | < 1 ms | < 2 ms |
| 1,000 entries | < 1 ms | < 2 ms | < 4 ms |
| 10,000 entries | ~ 5 ms | < 5 ms | ~ 12 ms |
| 100,000 entries | ~ 50 ms | < 10 ms | ~ 65 ms |

For deployments exceeding 100,000 memory entries, the recommendation is to switch to the PostgreSQL backend which uses pgvector's IVFFlat index for sublinear search.

---

## Multi-Tenancy on SQLite

### TenantRouter Implementation

The SQLite `TenantRouter` maps the same `ForOrg`/`ForTeam`/`ForUser` pattern to file paths:

```go
type SQLiteStore struct {
    dataDir     string
    platformDB  *sql.DB
    embedFunc   store.EmbedFunc
    orgPools    sync.Map  // org_slug → *orgStore (lazy-loaded)
}

// Implements store.TenantRouter
func (s *SQLiteStore) ForOrg(slug string) (store.OrgDataStore, error) {
    if cached, ok := s.orgPools.Load(slug); ok {
        return cached.(*orgStore), nil
    }
    orgDir := filepath.Join(s.dataDir, "orgs", slug)
    db, err := openSQLite(filepath.Join(orgDir, "org.db"))
    if err != nil {
        return nil, fmt.Errorf("open org database %q: %w", slug, err)
    }
    org := &orgStore{
        slug:      slug,
        db:        db,
        dir:       orgDir,
        embedFunc: s.embedFunc,
        teamPools: sync.Map{},
    }
    s.orgPools.Store(slug, org)
    return org, nil
}

type orgStore struct {
    slug      string
    db        *sql.DB
    dir       string
    embedFunc store.EmbedFunc
    teamPools sync.Map  // team_slug → *teamStore
}

// Implements store.OrgDataStore
func (o *orgStore) ForTeam(teamSlug string) store.TeamDataStore {
    if cached, ok := o.teamPools.Load(teamSlug); ok {
        return cached.(*teamStore)
    }
    db, _ := openSQLite(filepath.Join(o.dir, "teams", teamSlug+".db"))
    ts := &teamStore{db: db, embedFunc: o.embedFunc}
    o.teamPools.Store(teamSlug, ts)
    return ts
}

func (o *orgStore) ForUser(userID string) store.PersonalDataStore {
    db, _ := openSQLite(filepath.Join(o.dir, "personal", userID+".db"))
    return &personalStore{db: db, embedFunc: o.embedFunc}
}
```

### TenantMiddleware Integration

The SQLite backend plugs into the **same** `TenantMiddleware` pattern as pgstore. The middleware:

1. Reads `TenantContext` (org_slug, team_slug, user_id) from the JWT auth middleware
2. Calls `sqliteStore.ForOrg(orgSlug)` to get the `OrgDataStore`
3. Calls `orgStore.ForTeam(teamSlug)` / `orgStore.ForUser(userID)` to get scoped stores
4. Clones `Services` with resolved stores and injects into request context

Handlers call `store.FromRequest(r)` and receive fully-scoped stores — identical behavior regardless of backend.

```go
func SQLiteTenantMiddleware(sqlStore *SQLiteStore) func(http.Handler) http.Handler {
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            baseSvc := store.FromRequest(r)
            if baseSvc == nil {
                next.ServeHTTP(w, r)
                return
            }

            tc := TenantContextFrom(r.Context())
            if tc == nil || tc.OrgSlug == "" {
                next.ServeHTTP(w, r)
                return
            }

            orgStore, err := sqlStore.ForOrg(tc.OrgSlug)
            if err != nil {
                http.Error(w, "failed to resolve organization", 500)
                return
            }

            // Build request-scoped Services (same pattern as pgstore)
            reqSvc := buildRequestServices(baseSvc, orgStore, tc)
            ctx := store.WithServices(r.Context(), reqSvc)
            next.ServeHTTP(w, r.WithContext(ctx))
        })
    }
}
```

### Connection Management

Each SQLite database file is opened with:

```go
func openSQLite(path string) (*sql.DB, error) {
    os.MkdirAll(filepath.Dir(path), 0750)
    db, err := sql.Open("sqlite", path)
    if err != nil {
        return nil, err
    }
    // WAL mode: concurrent reads, serialized writes
    db.Exec("PRAGMA journal_mode=WAL")
    // Busy timeout: wait up to 5s for write lock
    db.Exec("PRAGMA busy_timeout=5000")
    // Foreign key enforcement
    db.Exec("PRAGMA foreign_keys=ON")
    // Synchronous NORMAL: safe with WAL, better performance than FULL
    db.Exec("PRAGMA synchronous=NORMAL")
    // Connection pool sizing
    db.SetMaxOpenConns(1)   // SQLite writes are serialized; one writer
    db.SetMaxIdleConns(2)   // Keep connections warm
    db.SetConnMaxLifetime(0) // No timeout on persistent connections
    return db, nil
}
```

**Important**: `SetMaxOpenConns(1)` ensures write serialization at the Go level, preventing `SQLITE_BUSY` errors. WAL mode allows concurrent reads from separate connections, but `database/sql` with a single connection handles this correctly for SQLite's threading model.

For read-heavy workloads (search queries), a separate read-only connection pool can be added:

```go
// Optional: dedicated reader pool for search-heavy databases (memories)
func openSQLiteReader(path string) (*sql.DB, error) {
    db, err := sql.Open("sqlite", path+"?mode=ro")
    if err != nil {
        return nil, err
    }
    db.Exec("PRAGMA journal_mode=WAL")
    db.SetMaxOpenConns(4)  // Multiple concurrent readers
    return db, nil
}
```

---

## Bootstrap Flow

### First-Run Detection

On daemon startup, the system checks for the existence of `platform.db`:

```go
func (s *SQLiteStore) NeedsBootstrap() bool {
    _, err := os.Stat(filepath.Join(s.dataDir, "platform.db"))
    return os.IsNotExist(err)
}
```

If `platform.db` does not exist, the system enters **setup mode** — it serves only the setup endpoints and the setup UI page until bootstrapping completes.

### CLI Bootstrap (`astonish setup`)

Interactive TUI wizard for first-time setup:

```
$ astonish setup

  Welcome to Astonish!
  Let's set up your instance.

  Display name: Alice Smith
  Email: alice@example.com
  Password: ********
  Confirm password: ********

  Organization name [Alice's Workspace]: 
  Organization slug [alice]: 

  ✓ Created platform database
  ✓ Created organization "Alice's Workspace"
  ✓ Created team "General"
  ✓ Created admin account
  ✓ Generated JWT signing key

  Setup complete! Start Astonish with:
    astonish studio

  Or connect the CLI:
    astonish chat
```

The wizard:
1. Collects display name, email, password
2. Derives org name/slug (with defaults from hostname or user input)
3. Generates a random JWT secret (32 bytes, base64-encoded)
4. Creates `platform.db` + runs migrations
5. Inserts user (with bcrypt password hash), org, org_membership (role: owner)
6. Creates org directory + `org.db` + runs org migrations
7. Creates "General" team in `org.db`
8. Creates `teams/general.db` + runs team migrations
9. Creates `personal/{user_id}.db` + runs personal migrations
10. Writes config:

```yaml
storage:
  backend: sqlite
  auth:
    jwt_secret: "base64-encoded-random-secret"
```

11. Issues a JWT token pair and stores it in `~/.config/astonish/credentials` — the user is immediately authenticated for CLI use without a separate `astonish login` step.

### Headless Bootstrap

For automated deployments (Docker, scripts):

```bash
astonish setup \
  --non-interactive \
  --email admin@company.com \
  --password "$ADMIN_PASSWORD" \
  --display-name "Admin" \
  --org-name "My Company" \
  --org-slug "my-company"
```

Or via environment variables:

```bash
ASTONISH_ADMIN_EMAIL=admin@company.com \
ASTONISH_ADMIN_PASSWORD=secret \
ASTONISH_ORG_NAME="My Company" \
astonish setup --non-interactive
```

### Studio Setup Page

If the daemon starts without `platform.db`, the Studio UI shows a setup page at `/setup` (the only accessible route). This provides the same flow as the CLI wizard via a web form. Useful for users who start the daemon before running CLI setup.

The setup page is served by a minimal handler that:
1. Returns the setup form (React component)
2. Accepts POST with user/org details
3. Calls the same bootstrap logic as the CLI
4. Redirects to login page on success

---

## Authentication

### Unified JWT Auth

Both SQLite and PostgreSQL backends use identical JWT authentication. There is no loopback bypass or IP-based exception — every request must carry a valid token, regardless of source address. This is required for correct multi-user behavior: the system must always know which user is making a request.

- **Access tokens**: HMAC-SHA256, 15-minute TTL, contain full user context (uid, email, org, team, role)
- **Refresh tokens**: HMAC-SHA256, 90-day TTL, minimal claims (uid, org), hash stored in `login_sessions`
- **Transport**: HttpOnly cookies for browser; `Authorization: Bearer` header for CLI
- **Token storage**: CLI stores tokens in `~/.config/astonish/credentials` after `astonish login`; refreshes automatically

### CLI Authentication Flow

The CLI experience remains frictionless despite requiring tokens:

```
$ astonish login
  Email: alice@example.com
  Password: ********
  ✓ Logged in. Token saved to ~/.config/astonish/credentials

$ astonish chat
  # Works immediately — CLI sends stored token automatically
```

After initial login:
1. The CLI reads the stored refresh token from `~/.config/astonish/credentials`
2. On each command, if the access token is expired, the CLI silently refreshes it
3. The refresh token is valid for 90 days — users rarely need to re-authenticate
4. `astonish setup` (first-run) automatically stores the initial token after creating the user

The result is that a local user runs `astonish setup` once, and never thinks about auth again for 90 days. But the system always knows exactly which user is making each request, which is essential for correct multi-tenant behavior even on a single machine with multiple users.

---

## Configuration

### New Config Schema

```yaml
storage:
  # Backend selection: "sqlite" (default) or "postgres"
  backend: sqlite

  # SQLite-specific settings (only used when backend: sqlite)
  sqlite:
    # Data directory for .db files
    # Default: ~/.local/share/astonish/ (Linux) or ~/Library/Application Support/astonish/ (macOS)
    data_dir: ""

  # PostgreSQL-specific settings (only used when backend: postgres)
  postgres:
    platform_dsn: "postgres://user:pass@host:5432/astonish_platform?sslmode=prefer"
    instance_suffix: "a1b2c3"

  # Authentication (shared between backends)
  auth:
    mode: "builtin"                 # "builtin" or "oidc"
    jwt_secret: ""                  # Auto-generated on setup
    access_token_ttl: "15m"
    refresh_token_ttl: "2160h"      # 90 days
    allow_registration: true
    default_org_name: ""
    default_org_slug: ""
    oidc:
      issuer_url: ""
      client_id: ""
      client_secret: ""
      scopes: ["openid", "email", "profile"]
      team_claim: "groups"
```

### Backend Detection at Startup

```go
func resolveBackend(cfg *config.StorageConfig) string {
    if cfg.Backend != "" {
        return cfg.Backend  // Explicit: "sqlite" or "postgres"
    }
    return "sqlite"  // Default
}
```

The `"file"` backend value is no longer recognized. If present in an old config, the daemon prints a clear error message directing the user to run `astonish setup`.

---

## Daemon Startup Flow

```go
func Run(ctx context.Context, appCfg *config.AppConfig) error {
    switch resolveBackend(&appCfg.Storage) {
    case "postgres":
        svc, pgStore, err = pgstore.NewPlatformServices(ctx, appCfg.Storage.Postgres, embedFunc)
        if err != nil {
            // Auto-bootstrap if platform DB doesn't exist
            pgstore.BootstrapPlatform(ctx, appCfg.Storage.Postgres)
            svc, pgStore, err = pgstore.NewPlatformServices(ctx, appCfg.Storage.Postgres, embedFunc)
        }
        tenantMW = pgstore.TenantMiddleware(pgStore)
        platformAuth = api.NewPlatformAuth(appCfg.Storage.Auth, pgStore, appCfg.Storage)

    case "sqlite":
        sqlStore, err = sqlitestore.New(dataDir, embedFunc)
        if err != nil {
            return fmt.Errorf("open sqlite store: %w", err)
        }
        if sqlStore.NeedsBootstrap() {
            // Enter setup-only mode: serve only /api/platform/setup and /setup UI
            return runSetupMode(ctx, sqlStore, appCfg)
        }
        svc = sqlStore.Services()
        tenantMW = sqlitestore.TenantMiddleware(sqlStore)
        platformAuth = api.NewPlatformAuth(appCfg.Storage.Auth, sqlStore, appCfg.Storage)
    }

    // From here on, code is identical regardless of backend
    studio := launcher.NewStudioServer(
        launcher.WithServices(svc),
        launcher.WithPlatformAuth(platformAuth),
        launcher.WithTenantMiddleware(tenantMW),
    )
    return studio.Run(ctx)
}
```

---

## Package Structure

### New Package: `pkg/store/sqlitestore/`

```
pkg/store/sqlitestore/
├── sqlitestore.go          # SQLiteStore struct, New(), NeedsBootstrap(), Close()
├── platform.go             # PlatformStore implementation (users, orgs, login sessions)
├── org.go                  # OrgDataStore implementation (teams, org memories, org skills)
├── team.go                 # TeamDataStore implementation (sessions, memories, credentials, etc.)
├── personal.go             # PersonalDataStore implementation
├── memories.go             # MemoryStore: FTS5 + vector search + hybrid fusion
├── sessions.go             # SessionStore: ADK session.Service + meta operations
├── credentials.go          # CredentialStore: AES-256-GCM encrypted storage
├── apps.go                 # AppStore + AppStateStore + AppStateSQLStore
├── flows.go                # FlowStore implementation
├── scheduler.go            # SchedulerStore implementation
├── fleet.go                # FleetTemplateStore + FleetPlanStore
├── skills.go               # SkillStore implementation
├── mcp_servers.go          # MCPServerStore implementation
├── audit.go                # AuditStore implementation
├── settings.go             # SettingsStore + OrgSettingsStore + PlatformSettingsStore
├── sandbox.go              # SandboxSessionStore + ChatEventJournal (optional)
├── tenant.go               # TenantMiddleware, TenantRouter implementation
├── provision.go            # ProvisionOrg, ProvisionTeam, ProvisionPersonalSchema
├── bootstrap.go            # Bootstrap logic (first-run user/org/team creation)
├── vector.go               # Vector index, cosine similarity, embedding serialization
├── migrate.go              # Migration runner (embed SQL, track versions)
├── open.go                 # openSQLite helper, PRAGMA configuration
├── migrations/
│   ├── platform/
│   │   └── 001_init.sql
│   ├── org/
│   │   └── 001_init.sql
│   ├── team/
│   │   └── 001_init.sql
│   └── personal/
│       └── 001_init.sql
└── sqlitestore_test.go     # Integration tests (use temp directories)
```

### Packages to Remove

| Package | Reason |
|---------|--------|
| `pkg/store/filestore/` | Replaced by `sqlitestore` |
| `pkg/memory/store.go` | chromem-go wrapper replaced by `sqlitestore/memories.go` |
| `pkg/memory/bm25.go` | Replaced by FTS5 |
| `pkg/api/auth.go` (device auth) | Replaced by unified JWT auth |

### Packages to Keep (Modified)

| Package | Changes |
|---------|---------|
| `pkg/memory/hugot_embedder.go` | Return `store.EmbedFunc` instead of `chromem.EmbeddingFunc` |
| `pkg/memory/chunker.go` | No changes — used by indexer |
| `pkg/memory/indexer.go` | Modify to write to `store.MemoryStore` instead of chromem collection |
| `pkg/memory/embedder.go` | Remove `chromem` import, use `store.EmbedFunc` type |
| `pkg/daemon/run.go` | Replace mode branching with backend switch |
| `pkg/config/app_config.go` | Add `SQLiteConfig` struct, update `StorageConfig` |
| `pkg/api/auth_platform.go` | Accept `store.PlatformStore` interface (works for both backends) |

### Dependency Changes

| Dependency | Action |
|-----------|--------|
| `github.com/philippgille/chromem-go` | **Remove** |
| `modernc.org/sqlite` | **Keep** (already present) |
| All other dependencies | Unchanged |

---

## Migration from PostgreSQL to SQLite (and vice versa)

A future `astonish migrate` command can transfer data between backends:

```bash
# Export from PostgreSQL to SQLite
astonish migrate --from postgres --to sqlite --data-dir ./export/

# Import from SQLite to PostgreSQL
astonish migrate --from sqlite --to postgres --platform-dsn "postgres://..."
```

This is not required for the initial implementation but the architecture supports it since both backends implement identical interfaces. A migration tool simply reads from one `store.Services` and writes to another.

---

## Comparison: SQLite vs PostgreSQL Backend

| Aspect | SQLite | PostgreSQL |
|--------|--------|------------|
| **Setup** | Zero-config (embedded) | Requires external PG server |
| **Deployment** | Single binary, no dependencies | Binary + PostgreSQL + pgvector |
| **Concurrency** | Single writer, multiple readers | Full MVCC, unlimited concurrency |
| **Vector search** | Brute-force cosine (Go-side) | IVFFlat index (sublinear) |
| **FTS** | FTS5 (BM25) | tsvector + ts_rank |
| **Isolation** | File-per-tenant (structural) | DB-per-org + schema-per-team + RLS |
| **Backup** | Copy `.db` files | pg_dump or filesystem snapshot |
| **Scale limit** | ~10 concurrent users, ~100K memories | Thousands of users, millions of memories |
| **Recommended for** | Solo, small team, edge, development | Production SaaS, large organizations |

### When to Upgrade from SQLite to PostgreSQL

Recommended migration triggers (documented, not enforced):

- More than 10 concurrent active users
- More than 100,000 total memory entries across all tenants
- Need for high-availability (replicas, failover)
- Regulatory requirement for database-level audit logging
- Multi-region deployment

---

## Security Considerations

### SQLite-Specific

1. **File permissions**: Data directory created with `0750`. Individual `.db` files inherit directory permissions.
2. **No network exposure**: SQLite has no network listener. Data access is strictly through the application process.
3. **Credential encryption**: Same AES-256-GCM envelope encryption as pgstore. Encryption keys stored in `org_encryption_keys` table within `org.db`.
4. **JWT secrets**: Stored in `~/.config/astonish/config.yaml` (not in the database). File permissions should restrict access to the running user.
5. **No RLS equivalent**: SQLite has no row-level security. Isolation is enforced structurally (separate files) and at the application level (stores are scoped by TenantMiddleware). A bug in a handler that bypasses `store.FromRequest()` could theoretically access wrong-tenant data — but this is mitigated by the fact that stores are file-scoped and never cross tenant boundaries.

### Shared (Both Backends)

1. **Passwords**: bcrypt with cost 12
2. **Tokens**: HMAC-SHA256 with 256-bit secret
3. **Refresh token revocation**: Token hash stored in `login_sessions`; checked on every refresh
4. **OIDC**: Standard Authorization Code + PKCE flow

---

## Implementation Phases

### Phase 1: Core Infrastructure

1. Create `pkg/store/sqlitestore/` package skeleton
2. Implement `openSQLite()` helper with WAL mode, busy timeout, foreign keys
3. Implement migration runner (embed SQL files, track `schema_migrations` table)
4. Write all migration SQL files (platform, org, team, personal)
5. Implement `SQLiteStore` struct with `New()`, `Close()`, `NeedsBootstrap()`
6. Implement vector serialization/deserialization + cosine similarity
7. Implement in-memory vector index with lazy loading

### Phase 2: Store Implementations

1. Platform stores: `UserStore`, `OrganizationStore`, `LoginSessionStore`, `OIDCProviderStore`, `UserChannelStore`
2. Org stores: `TeamManagementStore`, org-level `MemoryStore`, `SkillStore`, `MCPServerStore`, `AppStore`, `AuditStore`
3. Team stores: `SessionStore`, `MemoryStore`, `CredentialStore`, `AppStore`, `AppStateStore`, `FlowStore`, `SchedulerStore`, `FleetTemplateStore`, `FleetPlanStore`, `DrillReportStore`, `SkillStore`, `MCPServerStore`, `SettingsStore`, `AuditStore`
4. Personal stores: `MemoryStore`, `AppStore`, `AppStateStore`, `SessionStore`, `FlowStore`, `CredentialStore`
5. `TenantRouter` implementation (`ForOrg`, `ForTeam`, `ForUser`)
6. Provisioning functions (`ProvisionOrg`, `ProvisionTeam`, `ProvisionPersonalSchema`)

### Phase 3: Bootstrap and Auth Integration

1. Implement bootstrap logic (create first user/org/team)
2. CLI setup wizard (`astonish setup` for SQLite)
3. Studio setup page (web UI for first-run)
4. Wire `SQLiteStore` into `PlatformAuth` (JWT auth uses `PlatformStore` interface)
5. Implement `TenantMiddleware` for SQLite
6. Wire into daemon startup (`pkg/daemon/run.go`)

### Phase 4: Remove Legacy Code

1. Delete `pkg/store/filestore/` package
2. Remove chromem-go from `pkg/memory/store.go` — replace with `store.MemoryStore` interface usage
3. Remove in-memory BM25 (`pkg/memory/bm25.go`)
4. Remove device auth flow (`pkg/api/auth.go` device auth handlers)
5. Remove `store.ModePersonal` constant and all conditional branches
6. Update frontend: remove mode detection probe, always use platform auth flow
7. Remove `chromem-go` from `go.mod`
8. Update `pkg/memory/hugot_embedder.go` to return `store.EmbedFunc`
9. Update `pkg/memory/indexer.go` to write to `store.MemoryStore`

### Phase 5: Testing and Documentation

1. Unit tests for all `sqlitestore` implementations
2. Integration tests using temporary directories
3. Verify all existing pgstore tests still pass
4. Update CLI help text and setup commands
5. Update `docs/architecture/multi-tenant-platform.md` to reflect unified architecture
6. Document SQLite limitations and upgrade path in user-facing docs
