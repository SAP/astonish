# Fleet — Generic Orchestration Extensions (Backend + Editor UI + Software-Dev Migration)

**Status:** IMPLEMENTED (M1–M12) — worker session completed schema, durable stores, parallel dispatcher, API/SSE, editor UI, and software-dev upgrade.

**Current product mode (post cutover):** Durable **mailbox** is the sole agent-context substrate. The **task board is always on** (`claim_policy` defaults to `capability_match`). `CommunicationMode` / `shared_channel` and `TaskBoard.Enabled` have been removed. Channel remains for SSE, transcript, and external posts only.

**Author:** Prometheus (planning).
**Base commit:** current `HEAD` on the working branch.
**Scope:** `pkg/fleet/**`, `pkg/api/fleet_*.go`, `pkg/tools/fleet_plan_*.go`, `ent/team/schema/*.go`, `pkg/store/**` (interfaces) + `pkg/store/entstore/**` (impls), `pkg/daemon/run.go` (wiring), `web/src/components/**` (Fleet views + editor) + `web/src/api/fleetChat.ts`, `pkg/fleet/bundled/software-dev.yaml`, tests under `tests/e2e/fleet_*`.
**Non-scope:** other channel bridges (Slack/Telegram/Email), scheduler cron semantics, sandbox backend selection, personal/org/platform schemas, `pkg/fleet/monitor_state.go` (unrelated GitHub polling store).

**Revision log (rev.2 corrections applied):**
- §2.4 rewritten — no Atlas at HEAD; auto-migrate via `client.Schema.Create` in `tenant_router.go`.
- §3.2/§3.3/§3.4 rewritten — `entstore.Router` does not exist; correct pattern is `Store → .ForOrg → .ForTeam → teamDataStore` holding `*ent/team.Client`. Interfaces in `pkg/store/`, impls in `pkg/store/entstore/team_fleet_runtime.go`.
- §3.2 corrected — `monitor_state.go` is GitHub polling cursor, NOT ball state. Left untouched.
- §3.6 corrected — `FleetRecoverFunc` signature unchanged; new logic in sibling `RecoverActiveSessions` helper.
- §4.1 route conventions corrected — `/api/fleets/{key}` (templates), `/api/fleet-plans/{key}` (plans), `/api/studio/fleet/sessions/{id}/*` (sessions).
- §4.2 SSE location corrected — `FleetSessionStreamHandler` in `fleet_session_handlers.go`, NOT `chat_runner.go`.
- §4.4 added — `FleetStores` update explicit.
- §4.5 added — `pkg/daemon/run.go` wiring explicit.
- §5.3 corrected — `FleetExecutionPanel` is a vertical timeline today; parallel = multi-column redesign, not simple lane addition.
- §7 tests — `//go:build e2e` (no `integration` tag); YAML round-trip test for `software-dev.yaml` added.
- §8 M13 removed; M8/M9 SSE + frontend-handlers merged per `web/src/AGENTS.md` same-commit contract; renumbered to 12 milestones total.
- §9 invariants extended with `Channel` interface stability, `FleetRecoverFunc` stability, SSE same-commit contract, `monitor_state.go` untouched.
- §10 open questions pruned (resolved: progress-tracker concurrency, migration tooling, recovery signature, SSE location).

---

## 1. Motivation & Decisions Locked

Fleet today is domain-agnostic in spirit but constrained in practice:

- `FleetSettings` has only `MaxTurnsPerAgent`. There is no bounded parallel activation, no wall-clock cap, no explicit routing/communication policy.
- `FleetAgentConfig` has no capability declaration, no per-agent execution knobs (timeout, parallelizable, workspace isolation), no memory scoping, no task-claim policy.
- `session_manager.go` routes strictly serially through LLM `RouteWithLLM` — no parallel lanes, no task board, no durable mailbox (messages live in channel + JSONL only).
- `FleetPlan.Artifacts` is a flat `map[string]PlanArtifactConfig` — fine for now, but destinations are constrained to `local` / `git_repo`.
- The frontend renders a single-lane timeline and has no in-Studio editor for templates or per-agent config.
- `software-dev.yaml` hard-codes serial `PO → Architect → UX → Dev → QA → E2E` flow and cannot express "QA and E2E run in parallel after Dev commits."

**Owner decisions locked in the prior interview:**

1. **Backend scope:** Full — schema + durable state + bounded parallel activation + durable task board + durable mailbox (replaces in-memory `Message` envelope for cross-agent handoffs).
2. **Software-dev fleet:** Sequential Dev → QA → (PO) → E2E with direct-coding agents, mailbox + task board, and `project_context.load_file`. Parallel dispatcher remains available for custom templates.
3. **Frontend:** Full editor UI — refactor `TemplateDetail`, `PlanDetail`, `SessionTrace`, `FleetExecutionPanel` (per-agent lanes when parallelism > 1) **and** add a form-based in-Studio editor for templates and per-agent config.
4. **Domain neutrality:** Free-form `map[string]bool` capabilities plus an optional registry hint (no hardcoded enum).
5. **High-accuracy review:** Skipped — ship the plan as soon as approved.

**Non-goals (explicit):**

- Do NOT couple fleet logic to any specific channel implementation (per `pkg/fleet/AGENTS.md` §"When editing" rule 2).
- Do NOT hardcode software-dev role names into `FleetConfig` — every new field must remain domain-neutral.
- Do NOT bypass `pkg/store/entstore` — every new durable table must be team-scoped through the router (per root `AGENTS.md` §"Multi-Tenant Data Boundary").
- Do NOT loosen the Inline Report Rendering Contract; new SSE events must not overlap `report_marker` semantics.

---

## 2. Backend — Schema Deltas

### 2.1 `pkg/fleet/config.go`

Extend `FleetSettings`:

```go
type FleetSettings struct {
    // Existing
    MaxTurnsPerAgent int `yaml:"max_turns_per_agent,omitempty" json:"max_turns_per_agent,omitempty"`

    // NEW — bounded parallel activation
    MaxParallelAgents int `yaml:"max_parallel_agents,omitempty" json:"max_parallel_agents,omitempty"` // 0 or 1 = serial (today's behavior); >=2 enables the parallel dispatcher

    // NEW — global session budget
    MaxWallClockMinutes int `yaml:"max_wall_clock_minutes,omitempty" json:"max_wall_clock_minutes,omitempty"` // 0 = unlimited

    // NEW — routing policy
    // "llm_mentions" (default; today's RouteWithLLM behavior)
    // "explicit_queue" (agents write next-target to the mailbox, no LLM route)
    // "supervisor"    (a designated supervisor agent selects the next target)
    RoutingMode string `yaml:"routing_mode,omitempty" json:"routing_mode,omitempty"`

    // NEW — inter-agent communication substrate
    // "shared_channel" (today; every agent sees every message on the Channel)
    // "mailbox"        (durable per-recipient mailbox; agents only see messages addressed to them)
    CommunicationMode string `yaml:"communication_mode,omitempty" json:"communication_mode,omitempty"`

    // NEW — durable task board (optional)
    TaskBoard *TaskBoardConfig `yaml:"task_board,omitempty" json:"task_board,omitempty"`

    // NEW — memory visibility policy
    // "scoped"                (today; MemoryKeys stamp per routing decision)
    // "shared"                (every agent sees the full thread)
    // "private_plus_handoffs" (agent-private memory + explicit handoff snapshots)
    MemoryVisibility string `yaml:"memory_visibility,omitempty" json:"memory_visibility,omitempty"`
}

type TaskBoardConfig struct {
    Enabled     bool   `yaml:"enabled" json:"enabled"`
    ClaimPolicy string `yaml:"claim_policy,omitempty" json:"claim_policy,omitempty"` // "first_come" | "capability_match" | "supervisor_assigned"
}
```

Extend `FleetAgentConfig`:

```go
type FleetAgentConfig struct {
    // Existing
    Name        string          `yaml:"name" json:"name"`
    Description string          `yaml:"description,omitempty" json:"description,omitempty"`
    Identity    string          `yaml:"identity" json:"identity"`
    Mode        string          `yaml:"mode,omitempty" json:"mode,omitempty"`
    Tools       ToolsConfig     `yaml:"tools,omitempty" json:"tools,omitempty"`
    Delegate    *DelegateConfig `yaml:"delegate,omitempty" json:"delegate,omitempty"`
    Behaviors   string          `yaml:"behaviors" json:"behaviors"`

    // NEW — free-form capability declarations (domain-neutral)
    Capabilities map[string]bool `yaml:"capabilities,omitempty" json:"capabilities,omitempty"`

    // NEW — per-agent execution knobs
    Execution *AgentExecutionConfig `yaml:"execution,omitempty" json:"execution,omitempty"`

    // NEW — per-agent memory policy (overrides FleetSettings.MemoryVisibility for this agent)
    Memory *AgentMemoryConfig `yaml:"memory,omitempty" json:"memory,omitempty"`

    // NEW — task-board claim policy for this agent
    TaskPolicy *AgentTaskPolicy `yaml:"task_policy,omitempty" json:"task_policy,omitempty"`
}

type AgentExecutionConfig struct {
    MaxTurns       int    `yaml:"max_turns,omitempty" json:"max_turns,omitempty"`             // overrides FleetSettings.MaxTurnsPerAgent
    TimeoutMinutes int    `yaml:"timeout_minutes,omitempty" json:"timeout_minutes,omitempty"` // overrides the 60-min hardcode in activateAgent
    Parallelizable bool   `yaml:"parallelizable,omitempty" json:"parallelizable,omitempty"`   // MUST be true for the dispatcher to schedule this agent concurrently with another
    Workspace      string `yaml:"workspace,omitempty" json:"workspace,omitempty"`             // "shared" (today) | "isolated" (own worktree/branch) | "none" (no filesystem access)
}

type AgentMemoryConfig struct {
    Receives    []string `yaml:"receives,omitempty" json:"receives,omitempty"`         // agent keys whose outputs THIS agent sees (whitelist; empty = default routing rules)
    PrivateWork bool     `yaml:"private_work,omitempty" json:"private_work,omitempty"` // true = private turns never enter shared memory, only handoff summaries
}

type AgentTaskPolicy struct {
    Claims        []string `yaml:"claims,omitempty" json:"claims,omitempty"`                 // capability names this agent will claim from the task board
    MaxConcurrent int      `yaml:"max_concurrent,omitempty" json:"max_concurrent,omitempty"` // 0 = 1 (single-slot); >1 = agent may hold multiple tasks
}
```

Add optional registry hint (**advisory only**, no enforcement):

```go
// CapabilityRegistry — advisory list of domain-neutral capability names surfaced in the editor UI as hints.
// The runtime never rejects an unknown capability; this is documentation, not enforcement.
var CapabilityRegistry = []string{
	// Discovery & reasoning
	"research", "analysis", "synthesis",
	// Planning & orchestration
	"planning", "coordination", "supervisor",
	// Content lifecycle
	"writing", "review", "editing", "publishing",
	// Creation & delivery
	"design", "implementation", "prototyping",
	// Quality
	"validation", "quality-assurance",
	// Data
	"data-collection", "data-processing",
	// Interaction
	"customer-facing",
}
```

Getters:

```go
func (s *FleetSettings) GetMaxParallelAgents() int      { if s.MaxParallelAgents <= 0 { return 1 }; return s.MaxParallelAgents }
func (s *FleetSettings) GetRoutingMode() string         { if s.RoutingMode == "" { return "llm_mentions" }; return s.RoutingMode }
func (s *FleetSettings) GetCommunicationMode() string   { if s.CommunicationMode == "" { return "shared_channel" }; return s.CommunicationMode }
func (s *FleetSettings) GetMemoryVisibility() string    { if s.MemoryVisibility == "" { return "scoped" }; return s.MemoryVisibility }
func (a *FleetAgentConfig) IsParallelizable() bool      { return a.Execution != nil && a.Execution.Parallelizable }
func (a *FleetAgentConfig) GetWorkspace() string        { if a.Execution == nil || a.Execution.Workspace == "" { return "shared" }; return a.Execution.Workspace }
```

Validation additions in `FleetConfig.Validate()`:

- If `MaxParallelAgents > 1`: at least two agents must have `Execution.Parallelizable = true`, otherwise return an error (dispatcher would be pointless).
- If `RoutingMode == "supervisor"`: exactly one agent must have `Capabilities["supervisor"] == true`.
- If `TaskBoard.Enabled == true`: at least one agent must declare `TaskPolicy.Claims`.
- `CommunicationMode` must be one of `""`, `"shared_channel"`, `"mailbox"`.
- `MemoryVisibility` must be one of `""`, `"scoped"`, `"shared"`, `"private_plus_handoffs"`.
- Each `Execution.Workspace` must be one of `""`, `"shared"`, `"isolated"`, `"none"`.

### 2.2 `pkg/fleet/plan_config.go`

`PlanArtifactConfig` gains typed destinations (backward compatible — existing `local` and `git_repo` types keep working):

```go
type PlanArtifactConfig struct {
    // Existing
    Type          string `yaml:"type" json:"type"` // "local" | "git_repo" | "s3" (NEW) | "http_upload" (NEW)
    Path          string `yaml:"path,omitempty" json:"path,omitempty"`
    Repo          string `yaml:"repo,omitempty" json:"repo,omitempty"`
    BranchPattern string `yaml:"branch_pattern,omitempty" json:"branch_pattern,omitempty"`
    SubPath       string `yaml:"sub_path,omitempty" json:"sub_path,omitempty"`
    AutoPR        bool   `yaml:"auto_pr,omitempty" json:"auto_pr,omitempty"`

    // NEW — s3 destination
    S3 *S3ArtifactConfig `yaml:"s3,omitempty" json:"s3,omitempty"`

    // NEW — http upload destination
    HTTPUpload *HTTPUploadArtifactConfig `yaml:"http_upload,omitempty" json:"http_upload,omitempty"`
}

type S3ArtifactConfig struct {
    Bucket        string `yaml:"bucket" json:"bucket"`
    Prefix        string `yaml:"prefix,omitempty" json:"prefix,omitempty"`
    CredentialRef string `yaml:"credential_ref" json:"credential_ref"` // logical name in Credentials map
    Region        string `yaml:"region,omitempty" json:"region,omitempty"`
}

type HTTPUploadArtifactConfig struct {
    URL           string            `yaml:"url" json:"url"`
    Method        string            `yaml:"method,omitempty" json:"method,omitempty"` // default "POST"
    Headers       map[string]string `yaml:"headers,omitempty" json:"headers,omitempty"`
    CredentialRef string            `yaml:"credential_ref,omitempty" json:"credential_ref,omitempty"`
}
```

Note: actual upload/put implementations land in a follow-up worker task; the plan only requires the **schema** to accept them and the validator to accept them without erroring. The S3 destination will require adding `github.com/aws/aws-sdk-go-v2` to `go.mod` when implemented — it is NOT currently vendored. Worker at the S3-impl milestone adds the dependency then.

### 2.3 `ent/team/schema/*.go` — Three New Entities

Per `ent/AGENTS.md`, these are hand-edited schemas; regenerate with `go generate ./ent/team/...` after adding them.

**`ent/team/schema/fleet_run_state.go`** — durable session state (replaces the ad-hoc JSONL + `monitor_state.go` combo for runtime state that must survive daemon restart with parallel/mailbox semantics):

```go
package schema

import (
    "time"

    "entgo.io/ent"
    "entgo.io/ent/dialect"
    "entgo.io/ent/dialect/entsql"
    "entgo.io/ent/schema"
    "entgo.io/ent/schema/field"
    "entgo.io/ent/schema/index"
    "github.com/google/uuid"
)

type FleetRunState struct{ ent.Schema }

func (FleetRunState) Fields() []ent.Field {
    return []ent.Field{
        field.UUID("id", uuid.UUID{}).Default(uuid.New),
        field.String("session_id").NotEmpty(),      // maps to FleetSession.ID
        field.String("plan_key").NotEmpty(),
        field.String("state").NotEmpty(),           // "idle" | "processing" | "waiting_for_customer" | "stopped"
        field.JSON("active_agents", []string{}),    // slice of agent keys currently activated (parallel dispatcher)
        field.String("waiting_agent").Optional().Nillable(),
        field.String("ball").Default("agents"),     // "agents" | "customer"
        field.JSON("progress", map[string]any{}),   // ProgressTracker snapshot
        field.Time("last_heartbeat_at").Default(time.Now),
        field.Time("created_at").Default(time.Now).Immutable().Annotations(
            &entsql.Annotation{DefaultExprs: map[string]string{
                dialect.Postgres: "now()",
                dialect.SQLite:   "(datetime('now'))",
            }},
        ),
        field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).Annotations(
            &entsql.Annotation{DefaultExprs: map[string]string{
                dialect.Postgres: "now()",
                dialect.SQLite:   "(datetime('now'))",
            }},
        ),
    }
}

func (FleetRunState) Indexes() []ent.Index {
    return []ent.Index{
        index.Fields("session_id").Unique(),
        index.Fields("plan_key", "state"),
    }
}

func (FleetRunState) Annotations() []schema.Annotation {
    return []schema.Annotation{entsql.Table("fleet_run_states")}
}
```

**`ent/team/schema/fleet_task.go`** — durable task board:

```go
type FleetTask struct{ ent.Schema }

func (FleetTask) Fields() []ent.Field {
    return []ent.Field{
        field.UUID("id", uuid.UUID{}).Default(uuid.New),
        field.String("session_id").NotEmpty(),
        field.String("title").NotEmpty(),
        field.Text("description"),
        field.JSON("required_capabilities", []string{}), // capability names, matched against FleetAgentConfig.Capabilities
        field.String("claimed_by").Optional().Nillable(), // agent key that claimed the task
        field.String("status").Default("open"),           // "open" | "claimed" | "in_progress" | "done" | "failed" | "cancelled"
        field.JSON("result", map[string]any{}).Optional(),
        field.String("parent_task_id").Optional().Nillable(),
        field.Time("claimed_at").Optional().Nillable(),
        field.Time("completed_at").Optional().Nillable(),
        field.Time("created_at").Default(time.Now).Immutable(),
        field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
    }
}

func (FleetTask) Indexes() []ent.Index {
    return []ent.Index{
        index.Fields("session_id", "status"),
        index.Fields("claimed_by"),
    }
}

func (FleetTask) Annotations() []schema.Annotation {
    return []schema.Annotation{entsql.Table("fleet_tasks")}
}
```

**`ent/team/schema/fleet_mailbox_message.go`** — durable per-recipient mailbox (replaces the volatile in-memory `Message` for cross-agent handoffs; the `Channel` interface still emits SSE and posts external; the mailbox is the source of truth for "what has agent X been told"):

```go
type FleetMailboxMessage struct{ ent.Schema }

func (FleetMailboxMessage) Fields() []ent.Field {
    return []ent.Field{
        field.UUID("id", uuid.UUID{}).Default(uuid.New),
        field.String("session_id").NotEmpty(),
        field.String("recipient").NotEmpty(), // agent key OR "customer"
        field.String("sender").NotEmpty(),    // agent key, "customer", or "system"
        field.Text("body"),
        field.JSON("mentions", []string{}).Optional(),
        field.JSON("metadata", map[string]any{}).Optional(),
        field.String("delivery_status").Default("pending"), // "pending" | "delivered" | "read"
        field.Time("delivered_at").Optional().Nillable(),
        field.Time("read_at").Optional().Nillable(),
        field.Time("created_at").Default(time.Now).Immutable(),
    }
}

func (FleetMailboxMessage) Indexes() []ent.Index {
    return []ent.Index{
        index.Fields("session_id", "recipient", "delivery_status"),
        index.Fields("session_id", "created_at"),
    }
}

func (FleetMailboxMessage) Annotations() []schema.Annotation {
    return []schema.Annotation{entsql.Table("fleet_mailbox_messages")}
}
```

### 2.4 Migrations

Astonish at HEAD **does NOT use Atlas migrations**. There is no `schema/*.sql`, no `pkg/store/team/migrations/`, no `make migrate-diff` target, no Atlas hook logic in `.githooks/pre-commit`. The only remaining Atlas artifact is `scripts/atlas-baseline.sh` (legacy). Every ent client auto-migrates via `client.Schema.Create(ctx)` inside `pkg/store/entstore/tenant_router.go` at boot. Non-standard changes (renames, custom indexes, backfills, non-unique-to-unique flips) are handled by hand-written Go fixups following `pkg/store/entstore/pg_legacy_migrate.go` and `pkg/store/entstore/sqlite_legacy_migrate.go`.

Flow for the three new entities:

1. Add schemas under `ent/team/schema/`.
2. Regenerate: `cd ent/team && go run generate.go` (or `make ent-generate` if the target exists in the working branch; if not, worker runs `go generate ./ent/team/...`).
3. `go build ./...` must pass; `client.Schema.Create` will create the three tables on next daemon boot.
4. **No hand-written fixup is required** for M2 — every field on the new schemas is a supported ent type with defaults; auto-migrate handles it cleanly.
5. Commit ent schema + generated code in a **single commit** (per `ent/AGENTS.md` §"Regeneration flow" rule 3).

**Naming caution:** `ent/team/schema/fleet_monitor_state.go` already exists (table `fleet_monitor_state`, GitHub polling state). The new run-state table is `fleet_run_states` (plural, distinct). Do not conflate the two — the monitor state is per-plan GitHub cursor; the run state is per-session parallel/mailbox runtime snapshot.

---

## 3. Backend — Runtime Changes

### 3.1 Bounded Parallel Dispatcher (`pkg/fleet/session_manager.go`)

The current `Run()` is a serial loop: `WaitForMessage → activateAgent → RouteWithLLM → pendingTarget`. Refactor to a **dispatcher** that maintains an active-agent set of size ≤ `Settings.GetMaxParallelAgents()`.

**Contract:**

- When `MaxParallelAgents ≤ 1`, behavior MUST be byte-identical to today's serial loop (regression floor).
- When `MaxParallelAgents ≥ 2`, the dispatcher:
  1. On each tick, examines pending targets (from routing decisions or task-board claims).
  2. Filters targets to agents with `Execution.Parallelizable == true`.
  3. Starts up to `MaxParallelAgents - len(active)` concurrent `activateAgent` goroutines, each with its own `waitgroup` slot.
  4. As each goroutine completes, its response is routed exactly as today (LLM/mailbox/task-board depending on `RoutingMode`), potentially populating new pending targets.
- Non-parallelizable agents (default) run **serially** — the dispatcher blocks until they finish.
- Wall-clock cap enforced via `context.WithTimeout(ctx, Settings.MaxWallClockMinutes * time.Minute)` wrapping the whole `Run()`.

**Concurrency invariants:**

- The `FleetSession.mu` mutex now guards a new `active map[string]struct{}` field. All state transitions go through it.
- `OnAgentMessage` / `OnMessagePosted` / `OnBallChange` callbacks may fire from multiple goroutines — they were already documented to be safe; add a doc comment enforcing this contract.
- `ProgressTracker.AddMilestone` must be safe under concurrent callers (audit + add mutex if needed).
- Cross-agent handoffs in parallel mode use the durable mailbox (see §3.3), not the in-memory `Message` on the shared channel, so no read/write races on channel state.

### 3.2 Persistent Run-State (interface in `pkg/store/`, impl in `pkg/store/entstore/team_fleet_runtime.go` — new files)

**Important correction:** `pkg/fleet/monitor_state.go` is the **GitHub Monitor State Store** (`GitHubMonitorState` with `SeenIssues` map) — it tracks polled GitHub issues, NOT session ball state. Ball state today lives in-memory in `FleetSession.state` + `OnBallChange` callback; recovery re-reads the JSONL transcript via `RecoverFleetSession`. The new `FleetRunState` is **net-new durable runtime state** for parallel/mailbox semantics, not a replacement for `monitor_state.go`. Do NOT touch `monitor_state.go` in this plan.

Follow the existing entstore pattern (there is no `entstore.Router` type — the router surface is `Store → .ForOrg(slug) → orgDataStore → .ForTeam(slug) → teamDataStore` returning stores that hold a `*ent/team.Client`; see `pkg/store/entstore/team_fleet.go` for the canonical shape).

**Interface declared in `pkg/store/fleet_runtime.go` (new file):**

```go
package store

import (
    "context"
    "time"

    "github.com/google/uuid"
)

type FleetRunStateSnapshot struct {
    SessionID       string
    PlanKey         string
    State           string // "idle" | "processing" | "waiting_for_customer" | "stopped"
    ActiveAgents    []string
    WaitingAgent    string
    Ball            string
    Progress        map[string]any
    LastHeartbeatAt time.Time
}

type FleetRunStateStore interface {
    Upsert(ctx context.Context, snap FleetRunStateSnapshot) error
    Get(ctx context.Context, sessionID string) (*FleetRunStateSnapshot, error)
    ListRecoverable(ctx context.Context, planKey string) ([]FleetRunStateSnapshot, error)
    Heartbeat(ctx context.Context, sessionID string, at time.Time) error
    Delete(ctx context.Context, sessionID string) error
}
```

**Impl in `pkg/store/entstore/team_fleet_runtime.go` (new file):**

```go
package entstore

// fleetRunStateStore holds *ent/team.Client — pattern mirrors fleetPlanStore in team_fleet.go
type fleetRunStateStore struct { client *teament.Client }

func (s *fleetRunStateStore) Upsert(...) error         { /* ent upsert */ }
func (s *fleetRunStateStore) Get(...)                  { ... }
func (s *fleetRunStateStore) ListRecoverable(...)      { ... }
func (s *fleetRunStateStore) Heartbeat(...)            { ... }
func (s *fleetRunStateStore) Delete(...)               { ... }
```

**Wiring:** add `FleetRunStates() store.FleetRunStateStore` factory method on `teamDataStore` (see `pkg/store/entstore/team_fleet.go` for the pattern with `FleetPlans()`); expose to handlers via `FleetStores.RunStates` in `pkg/api/fleet_stores.go` (§4.4 below).

**Callers:**
- `FleetSession.setState` / `notifyBallChange` / parallel-dispatcher `active`-set changes → `Upsert`.
- Periodic `Heartbeat` from the running session (interval TBD by worker; 30s is a reasonable default).
- `PlanActivator.RestoreActivated` → on each restored plan, call a new `RecoverActive(ctx, planKey)` helper that reads `ListRecoverable` and, for each snapshot with `state != "stopped"`, invokes `FleetRecoverFunc` per §3.6.
- On graceful session end → `Delete`.

**Note on monitor_state.go**: it remains as-is (GitHub polling cursor store); the file rename/retirement milestone (previously M13) is **removed** from this plan — those are two independent concerns.

### 3.3 Durable Mailbox (interface in `pkg/store/fleet_runtime.go`, impl in `pkg/store/entstore/team_fleet_runtime.go`)

```go
// pkg/store/fleet_runtime.go
type FleetMailboxMessage struct {
    ID            uuid.UUID
    SessionID     string
    Recipient     string // agent key OR "customer"
    Sender        string // agent key, "customer", or "system"
    Body          string
    Mentions      []string
    Metadata      map[string]any
    DeliveryStatus string // "pending" | "delivered" | "read"
    DeliveredAt   *time.Time
    ReadAt        *time.Time
    CreatedAt     time.Time
}

type FleetMailboxStore interface {
    Deliver(ctx context.Context, sessionID string, msg FleetMailboxMessage, recipients []string) error
    Poll(ctx context.Context, sessionID, recipient string, sinceCreatedAt time.Time) ([]FleetMailboxMessage, error)
    MarkRead(ctx context.Context, ids []uuid.UUID) error
    ListForSession(ctx context.Context, sessionID string) ([]FleetMailboxMessage, error) // for SessionTrace UI
}
```

Behavior (gated on `FleetSettings.CommunicationMode == "mailbox"`):

- **Handoff write path.** When an agent's response contains routing decisions targeting other agents, `session_manager` writes one `FleetMailboxMessage` per target via `Deliver`, in addition to whatever the current `Channel` does for SSE. The mailbox is the source of truth for "what has agent X been told"; the `Channel` remains the source of truth for SSE + external post-out. **The `Channel` interface itself is NOT modified** — mailbox layers *alongside* it.
- **Agent read path.** `activateAgent` calls a new `BuildMailboxThreadContext(ctx, mailboxStore, sessionID, agentKey)` in place of `BuildThreadContext(fs.Channel, agentKey)`. Same 50k-char budget + progressive summarization as `BuildThreadContext`, but sourced from the recipient's inbox rows instead of the shared channel history. See §7.1 for the mailbox-aware unit test.
- **Marking read.** After successful `activateAgent`, mark all polled messages `read` in a single batch call so replays are idempotent.
- **Default `shared_channel` mode.** Zero code path change; mailbox store is not read or written.

Migration path: existing sessions continue on `shared_channel`; new templates opt in via `settings.communication_mode: mailbox`.

### 3.4 Task Board (interface in `pkg/store/fleet_runtime.go`, impl in `pkg/store/entstore/team_fleet_runtime.go`)

```go
// pkg/store/fleet_runtime.go
type FleetTask struct {
    ID                   uuid.UUID
    SessionID            string
    Title                string
    Description          string
    RequiredCapabilities []string
    ClaimedBy            string
    Status               string // "open" | "claimed" | "in_progress" | "done" | "failed" | "cancelled"
    Result               map[string]any
    ParentTaskID         string
    ClaimedAt            *time.Time
    CompletedAt          *time.Time
    CreatedAt            time.Time
    UpdatedAt            time.Time
}

type FleetTaskBoardStore interface {
    Post(ctx context.Context, task FleetTask) (*FleetTask, error)
    Claim(ctx context.Context, sessionID, agentKey string, capabilities map[string]bool, policy string) (*FleetTask, error)
    Complete(ctx context.Context, id uuid.UUID, result map[string]any) error
    Fail(ctx context.Context, id uuid.UUID, reason string) error
    List(ctx context.Context, sessionID string, statuses ...string) ([]FleetTask, error)
}
```

Impl in `pkg/store/entstore/team_fleet_runtime.go` next to `fleetRunStateStore` and mailbox impl. `Claim` uses a per-session serializable transaction (PG) / immediate-write txn (SQLite) to prevent double-claim races.

New tools exposed to agents (registered in `pkg/tools/fleet_task_tool.go` — new file, following the `RunnableTool` + `Declaration()` pattern in `pkg/tools/`):

- `fleet_task_post({title, description, required_capabilities})` — an agent posts a subtask.
- `fleet_task_complete({task_id, result})` — an agent marks its claimed task done.
- `fleet_task_fail({task_id, reason})` — an agent marks its claimed task failed.

`fleet_task_claim` is NOT an agent tool — the dispatcher calls `Claim` internally when routing decisions or task-board polling elects a claimant. This keeps agents from second-guessing dispatcher assignments.

**Claim policies (`FleetSettings.TaskBoard.ClaimPolicy`):**

- `first_come` — dispatcher hands the next open task to any polling agent whose declared `TaskPolicy.Claims` intersects `RequiredCapabilities`.
- `capability_match` — dispatcher assigns tasks to the agent whose declared capabilities are the closest superset of `RequiredCapabilities`.
- `supervisor_assigned` — only the supervisor agent (`Capabilities["supervisor"] == true`) may claim, then dispatches via mailbox handoff.

**SubAgentManager concurrency note:** M6's worker must audit `pkg/fleet/sub_agent.go` (`SubAgentManager.RunTask`) — today it is invoked serially from `session_manager.Run()`; parallel dispatch will invoke it from N goroutines concurrently. Any shared mutable state (e.g., per-agent tool-cache, session-scoped memory refs) must be either sync-guarded or per-invocation.

### 3.5 `pkg/fleet/prompt.go` Injection

`BuildAgentPrompt` gains sections:

- **Capabilities:** list the agent's declared capabilities so the LLM knows what it is trusted to do.
- **Execution constraints:** if `Execution.TimeoutMinutes` is set, tell the agent it has N minutes.
- **Workspace mode:** if `Execution.Workspace == "isolated"`, tell the agent its workspace is a private clone/worktree and it may commit freely.
- **Memory rules:** if `Memory.PrivateWork == true`, tell the agent its intermediate turns are private; only final messages become handoffs.
- **Task board:** if enabled, describe the `fleet_task_post` / `fleet_task_complete` / `fleet_task_fail` tools inline.

`BuildThreadContext` — today reads from `Channel` history with a 50k-char budget + progressive summarization. In mailbox mode, add a sibling `BuildMailboxThreadContext(ctx, mailboxStore, sessionID, agentKey)` with **identical budget + summarization behavior**, sourced from the recipient's inbox rows. Call site in `activateAgent` switches on `Settings.GetCommunicationMode()`. Do NOT modify `BuildThreadContext` in place — keep the current version working byte-identically for `shared_channel` (regression floor).

`BuildAgentPrompt` also receives a `taskSlug` today; verify no per-agent customization is required — if the injected sections need per-agent conditional rendering that varies with `taskSlug`, thread it through the new sections. Worker audits this at M7 and keeps the signature stable if possible.

### 3.6 Planner-Activator Wiring (`pkg/fleet/plan_activator.go` + new helper)

**Correction:** the existing `FleetRecoverFunc` signature is `func(ctx, RecoverFleetConfig) error` — it recovers **exactly one** session and does not scan for candidates. `PlanActivator.RestoreActivated()` already handles the list-and-recover loop via each plan's `CheckForWork()`. Do NOT change `FleetRecoverFunc`'s signature.

New wiring:

- `FleetStartFunc`: after successfully spawning a `FleetSession`, upsert an initial `FleetRunState` row via `FleetStores.RunStates.Upsert`.
- **New helper** `RecoverActiveSessions(ctx, planKey, runStates, recoverFn)` in `pkg/fleet/recovery.go` (new file): reads `runStates.ListRecoverable(ctx, planKey)`; for each snapshot whose `State != "stopped"`, builds a `RecoverFleetConfig` and invokes `recoverFn` (i.e. `FleetRecoverFunc`) once. Call this helper from `PlanActivator.RestoreActivated` after each plan's channel is attached.
- Session-scoped heartbeat goroutine (started in `FleetSession.Run`) periodically calls `runStates.Heartbeat(ctx, sessionID, time.Now())`; stops on `Run` return.
- On graceful session end (state → `"stopped"`), the recovery helper skips the row; a separate janitor (deferred to a follow-up worker task, not in this plan's scope) may GC old `"stopped"` rows after a TTL. Explicitly out-of-scope here.

---

## 4. API Surface (`pkg/api/fleet_*.go`)

### 4.1 New / Extended Handlers

**Correction:** existing route conventions must be respected. Templates live at `/api/fleets/{key}` (already has GET/PUT/DELETE — no new POST/PUT/DELETE needed), plans at hyphenated `/api/fleet-plans/{key}` (not `/api/fleet/plans/{key}`), sessions at `/api/studio/fleet/sessions/{id}/*`. All new endpoints must match this convention.

New routes (register in `pkg/api/handlers.go` near the existing fleet block, lines 1246–1274):

- `PATCH /api/fleet-plans/{key}/agents/{agent_key}` → `PatchFleetPlanAgentHandler` — patch a single agent config (used by the per-agent editor drawer). Body = partial `FleetAgentConfig` JSON.
- `GET /api/studio/fleet/sessions/{id}/tasks` → `FleetSessionTasksHandler` — list task-board entries (uses `FleetStores.TaskBoard.List`).
- `GET /api/studio/fleet/sessions/{id}/mailbox/{recipient}` → `FleetSessionMailboxHandler` — inspect a recipient's inbox read-only (uses `FleetStores.Mailbox.Poll` or a new `ListForSession(...)` filtered by recipient).

Handlers must be registered with `router.HandleFunc(..., HandlerName).Methods("...")` (gorilla-mux, matching the existing style) and go through `TenantMiddleware` — no direct DB reads (per `pkg/api/AGENTS.md`). Template CRUD (list/get/save/delete) already exists at `/api/fleets/*`; extending its behavior for the new schema fields comes automatically once the `FleetConfig` JSON round-trip covers them (§4.3).

### 4.2 SSE Additions — on the Fleet Stream, NOT chat_runner

**Correction:** fleet SSE is served by `FleetSessionStreamHandler` at `GET /api/studio/fleet/sessions/{id}/stream` in `pkg/api/fleet_session_handlers.go` — NOT by `chat_runner.go`. Existing events emitted there: `fleet_session`, `fleet_state`, `fleet_message`, `fleet_done` (see `safeSendSSE` calls at lines ~890–981). New events land in the same handler using the same `safeSendSSE` helper.

Additive events (existing consumers ignore unknown types — `web/src/AGENTS.md` same-commit contract still applies):

- `fleet_agent_started {session_id, agent, lane_index}` — dispatcher activates an agent (parallel mode `lane_index >= 0`, serial mode `-1`).
- `fleet_agent_finished {session_id, agent, lane_index, duration_ms}`.
- `fleet_task_posted {session_id, task_id, title, required_capabilities}`.
- `fleet_task_claimed {session_id, task_id, agent}`.
- `fleet_task_completed {session_id, task_id, status}`.
- `fleet_mailbox_delivered {session_id, recipient, sender}` (only when `CommunicationMode == "mailbox"`).

No overlap with `report_marker` semantics (Inline Report Rendering Contract remains frozen).

### 4.3 `pkg/tools/fleet_plan_tool.go` & `fleet_plan_validate_tool.go`

`SaveFleetPlanArgs` today has an `Artifacts map[string]SaveFleetPlanArtifact`. Extend `SaveFleetPlanArtifact` with `S3` / `HTTPUpload` sub-structs mirroring §2.2 and let the validator pass them through. Extend `SaveFleetPlanArgs` (or add `SettingsOverride` / `AgentsOverride` fields) so the wizard can round-trip the new settings + per-agent config. `validateFleetPlan` picks up the extended `FleetConfig.Validate()` automatically.

### 4.4 `FleetStores` Update (`pkg/api/fleet_stores.go`)

The `FleetStores` bundle (used by `RecoverFleetSession`, `StartHeadlessFleetSession`, `StartFleetSessionFromPlan`, and injected into runtime context via `InjectIntoContext`) MUST gain the three new stores:

```go
type FleetStores struct {
    // ...existing (Templates, Plans, MCP, Skills, ...)
    RunStates FleetRunStateStore
    Mailbox   FleetMailboxStore
    TaskBoard FleetTaskBoardStore
}
```

Update BOTH constructors:

- `FleetStoresFromTeam(team store.TeamDataStore, org store.OrgDataStore, platformMCP, platformSkills)` — pull the three new stores off `team.FleetRunStates()`, `team.FleetMailbox()`, `team.FleetTaskBoard()` (new factory methods on `teamDataStore`).
- `FleetStoresFromServices(svc *store.Services)` — same, via `svc.FleetRunStates` / `svc.FleetMailbox` / `svc.FleetTaskBoard` fields added to `store.Services`.

`InjectIntoContext` extends its map to include the three new stores under stable context keys so `pkg/fleet` runtime can retrieve them without importing `pkg/api`.

### 4.5 Daemon Wiring (`pkg/daemon/run.go` ~lines 1040–1250)

The fleet wiring block that constructs stores from tenant router output and passes them to `FleetStoresFromTeam` / channel constructors must be updated to instantiate the three new stores. Ordering is critical: the run-state store must be constructed before `PlanActivator` (which reads it during `RestoreActivated`). Worker audits this block at M3.

---

## 5. Frontend Refactor

### 5.1 Shared Types (`web/src/components/fleet/fleetUtils.ts`)

Extend TypeScript types to mirror the new Go shape:

```ts
export interface FleetSettings {
  max_turns_per_agent?: number;
  max_parallel_agents?: number;
  max_wall_clock_minutes?: number;
  routing_mode?: "llm_mentions" | "explicit_queue" | "supervisor";
  communication_mode?: "shared_channel" | "mailbox";
  task_board?: { enabled: boolean; claim_policy?: string };
  memory_visibility?: "scoped" | "shared" | "private_plus_handoffs";
}

export interface AgentExecutionConfig {
  max_turns?: number;
  timeout_minutes?: number;
  parallelizable?: boolean;
  workspace?: "shared" | "isolated" | "none";
}

export interface AgentMemoryConfig { receives?: string[]; private_work?: boolean; }
export interface AgentTaskPolicy   { claims?: string[]; max_concurrent?: number; }

export interface FleetAgentDef {
  // ...existing
  capabilities?: Record<string, boolean>;
  execution?: AgentExecutionConfig;
  memory?: AgentMemoryConfig;
  task_policy?: AgentTaskPolicy;
}
```

### 5.2 `TemplateDetail.tsx` / `PlanDetail.tsx` Refactor

Break each into three tabs (URL-hash-routed for deep-linking):

- **Overview** — name, description, communication graph, project source (existing content).
- **Settings** — the new `FleetSettings` fields, form-controlled with tooltips fed from `CapabilityRegistry` and the enum getters.
- **Agents** — a list; clicking an agent opens a right-side drawer with the full editor form (name, identity, behaviors, tools, delegate, capabilities, execution, memory, task_policy).

Save flow:

- Template edits → existing `PUT /api/fleets/{key}` (already handles full `FleetConfig` JSON — the new fields ride the same round-trip once §4.3 is done).
- Plan edits → existing `PUT /api/fleet-plans/{key}`.
- Per-agent drawer save → new `PATCH /api/fleet-plans/{key}/agents/{agent_key}` (§4.1).
- Live validation before save: call the existing fleet-plan validate tool endpoint used by the wizard; show inline errors.

**API client additions** (`web/src/api/fleetChat.ts` — new module or extend existing fleet API client):

```ts
export async function patchFleetPlanAgent(planKey: string, agentKey: string, patch: Partial<FleetAgentDef>): Promise<void>;
export async function listFleetSessionTasks(sessionId: string): Promise<FleetTask[]>;
export async function listFleetSessionMailbox(sessionId: string, recipient: string): Promise<FleetMailboxMessage[]>;
```

### 5.3 `FleetExecutionPanel.tsx` — Per-Agent Lanes (significant visual redesign)

**Correction:** the current `FleetExecutionPanel` is a **vertical phase timeline** — a continuous vertical line with circular status dots, expandable phase details, per-agent icons, tool-call rendering, and inline markdown. It is NOT a horizontal lane layout today. Adding parallel lanes is a substantial visual refactor, not a small tweak.

Design:

- When `session.max_parallel_agents ≤ 1` (or the SSE stream never emits `fleet_agent_started` with `lane_index >= 0`), the current single-column timeline is rendered unchanged. This is the regression floor.
- When parallelism > 1, switch to a **multi-column layout**: one column per parallelizable agent lane, each column an independent vertical timeline. Non-parallelizable agents share a "coordinator" column pinned left.
- Task-board events (`fleet_task_posted`, `fleet_task_claimed`, `fleet_task_completed`) render as pill-shaped nodes on a separate "Tasks" strip below the columns.
- Both layouts share the phase-card component (`PlanPanel.tsx` / `TaskPlanPanel.tsx`); only the containing flex/grid changes.

Worker will produce a Figma-parity mock at M9 start and confirm the visual direction before coding. Delegate visual polish to the `shared/frontend` skill during M9.

### 5.4 `SessionTrace.tsx`

- Add a "Mailbox" tab per recipient (uses `GET /api/studio/fleet/sessions/{id}/mailbox/{recipient}` — see §4.1).
- Add a "Tasks" tab listing every posted/claimed/completed task with timing (uses `GET /api/studio/fleet/sessions/{id}/tasks`).

### 5.5 New Editor Entry Points (`FleetSidebar.tsx`)

- "+ New Template" button → routes to `/fleet/templates/new` with a blank form.
- "Clone" affordance on each template card.
- Import/export YAML (existing YAML round-trip via `MarshalYAML`/`UnmarshalYAML` in `plan_config.go`).

---

## 6. `pkg/fleet/bundled/software-dev.yaml` — Current contract

Bundled software-dev is **sequential** (Dev → QA → PO review → E2E). Agents code with native tools. Project context is **load_file** (AGENTS.md produced during setup with core tools).

```yaml
settings:
  max_turns_per_agent: 30
  max_parallel_agents: 1           # serial activation for software-dev
  max_wall_clock_minutes: 180
  routing_mode: llm_mentions
  memory_visibility: scoped
  task_board:
    claim_policy: capability_match

project_context:
  generator: load_file
  output_file: AGENTS.md
  max_size_kb: 10

agents:
  po:
    capabilities: { planning: true, requirements: true, supervisor: true }
    execution: { parallelizable: false, workspace: shared, timeout_minutes: 30 }

  architect:
    capabilities: { "design.architecture": true }
    execution: { parallelizable: false, workspace: shared, timeout_minutes: 45 }

  ux:
    capabilities: { "design.ux": true }
    execution: { parallelizable: false, workspace: shared, timeout_minutes: 30 }

  dev:
    capabilities: { "code.write": true, "code.test": true }
    execution: { parallelizable: false, workspace: shared, timeout_minutes: 90 }
    # talks_to: [po, qa] — hands off directly to QA after implementation

  qa:
    capabilities: { "code.test": true, "code.review": true }
    execution: { parallelizable: false, workspace: shared, timeout_minutes: 45 }

  e2e:
    capabilities: { "code.test": true, "ops.observe": true }
    execution: { parallelizable: false, workspace: shared, timeout_minutes: 60 }
```

Flow: Dev `@mention`s QA; QA reports to PO; PO sends E2E only after QA approval. PO must not `@qa` and `@e2e` in the same message. Parallel dispatcher remains available for **custom** templates with `max_parallel_agents ≥ 2`.

---

## 7. Tests

### 7.1 Unit

- `pkg/fleet/config_test.go` — extend to cover every new field + every new validation rule (parallel-with-no-parallelizable-agent, supervisor-mode-with-no-supervisor, invalid workspace/memory/comm enums, malformed task-board, communication-graph-aware supervisor reachability, parallelizable siblings sharing predecessor).
- `pkg/fleet/prompt_test.go` (new) — snapshot the prompt for an agent with capabilities/execution/memory/task_policy set to verify each section is rendered; snapshot `BuildMailboxThreadContext` output equivalence with `BuildThreadContext` for the shared-channel-vs-mailbox parity case (same 50k budget behavior).
- `pkg/store/entstore/team_fleet_runtime_test.go` (new) — deliver/poll/mark-read/concurrent-poll safety for mailbox; post/claim (each policy)/complete/fail/list for task board; upsert/list-recoverable/heartbeat for run-state.
- `pkg/fleet/recovery_test.go` (new) — `RecoverActiveSessions` invokes the recover fn exactly once per non-stopped snapshot; skips stopped.
- **YAML round-trip test** — `pkg/fleet/config_test.go` MUST include a round-trip case that loads `pkg/fleet/bundled/software-dev.yaml` (1786 lines), marshals it back, and verifies structural equivalence. Prevents any new field from silently dropping data.

### 7.2 Integration (build tag `e2e`)

Note: this repo uses `//go:build e2e` throughout `tests/e2e/`; there is no separate `integration` build tag. All backend-with-DB tests use the `e2e` tag and run under `make test-integration` (which sets `ASTONISH_TEST_DSN`) or `make test-e2e` (full stack).

- `tests/e2e/fleet_parallel_test.go` (new) — starts a two-agent template with `max_parallel_agents: 2` and both agents `parallelizable: true`; verifies both activate concurrently (measured by overlapping `fleet_agent_started` / `fleet_agent_finished` timestamps in the SSE stream).
- `tests/e2e/fleet_mailbox_test.go` (new) — round-trips a message through the mailbox store; verifies agent B only sees messages addressed to B.
- `tests/e2e/fleet_task_board_test.go` (new) — one agent posts a task, another claims it, dispatcher activates the claimer.
- `tests/e2e/fleet_recovery_test.go` (new) — starts a session, kills the daemon, restarts, verifies session recovers from `FleetRunState` via `RecoverActiveSessions`.

### 7.3 E2E (build tag `e2e`)

- `tests/e2e/fleet_software_dev_parallel_test.go` (new) — runs the upgraded `software-dev.yaml` through a scripted bug-fix flow; verifies `qa` and `e2e` agents overlap in wall-clock time by at least one activation window.

### 7.4 Frontend

- `web/src/components/fleet/__tests__/TemplateDetail.test.tsx` — form renders every new field, saves round-trip.
- `web/src/components/chat/__tests__/FleetExecutionPanel.test.tsx` — renders N lanes when parallelism > 1; falls back to single lane when ≤ 1.

---

## 8. Rollout Order (Worker-Executable Milestones)

Each milestone is a **single commit or single PR**. A worker session executes them sequentially; each is independently reviewable and revertible. **12 milestones total** (previous M13 removed — `monitor_state.go` is a distinct concern and not touched by this plan).

**M1 — Schema-only (no runtime change).**
Add all new fields to `FleetConfig`, `FleetAgentConfig`, `FleetSettings`, `PlanArtifactConfig` with zero-value defaults. Extend `Validate()` including communication-graph-aware supervisor reachability + parallelizable-siblings-share-predecessor checks. Extend YAML/JSON round-trip tests including the full `software-dev.yaml` round-trip. `FleetPlan` already embeds `FleetConfig` via `yaml:"-"`, so plan JSON/YAML picks up new settings automatically — no `plan_config.go` restructuring needed. **No behavior changes.** Pre-existing configs must parse identically. Ship.

**M2 — Ent schemas.**
Add `FleetRunState`, `FleetTask`, `FleetMailboxMessage` under `ent/team/schema/`. Regenerate via `cd ent/team && go run generate.go` (no Atlas migration files — `client.Schema.Create` auto-migrates at boot; see §2.4). `go build ./...` must pass. Commit schema + generated code together. Ship.

**M3 — Persistent run-state store + FleetStores wiring + daemon wiring.**
Implement `FleetRunStateStore` interface (`pkg/store/fleet_runtime.go`) + ent impl (`pkg/store/entstore/team_fleet_runtime.go`) + `teamDataStore.FleetRunStates()` factory. Extend `FleetStores` struct + both `FleetStoresFromTeam` / `FleetStoresFromServices` constructors + `InjectIntoContext`. Update `pkg/daemon/run.go` fleet block (~lines 1040–1250) — instantiate before `PlanActivator`. Wire into `FleetSession.setState` / `notifyBallChange` / heartbeat goroutine. Add `RecoverActiveSessions` helper and call it from `PlanActivator.RestoreActivated`. Ship with recovery e2e test.

**M4 — Durable mailbox (opt-in).**
Add `FleetMailboxStore` interface + ent impl in the same store files. Wire into `FleetStores`. Implement `BuildMailboxThreadContext` sibling of `BuildThreadContext` with identical 50k budget + summarization. Gate reads/writes behind `Settings.GetCommunicationMode() == "mailbox"`. Default remains `shared_channel` — every existing plan is unaffected. **`Channel` interface is NOT modified** — mailbox layers alongside. Ship with unit + e2e tests.

**M5 — Task board (opt-in).**
Add `FleetTaskBoardStore` interface + ent impl. Wire into `FleetStores`. Implement `fleet_task_post` / `fleet_task_complete` / `fleet_task_fail` tools in `pkg/tools/fleet_task_tool.go` following `RunnableTool` + `Declaration()` pattern. Gate behind `Settings.TaskBoard.Enabled`. Ship with unit + e2e tests (each claim policy covered).

**M6 — Bounded parallel dispatcher.**
Refactor `FleetSession.Run()`. Serial path (`MaxParallelAgents ≤ 1`) is byte-identical to today. Parallel path gated by settings. Audit `SubAgentManager.RunTask` for concurrency safety; add sync guards or per-invocation state where needed. Audit `ProgressTracker.AddMilestone` — evaluator confirms it is already `sync.RWMutex`-protected, so no change needed there. Ship with a parallel e2e test + a serial-regression test proving the serial path is unchanged.

**M7 — Prompt injection.**
Update `BuildAgentPrompt` to render capability/execution/memory/task sections. Verify `taskSlug` needs no new per-agent conditional. Snapshot tests. Ship.

**M8 — API + SSE + Frontend types + SSE handlers (bundled per `web/src/AGENTS.md` same-commit contract).**
Add new handlers (`PatchFleetPlanAgentHandler`, `FleetSessionTasksHandler`, `FleetSessionMailboxHandler`) and their routes. Add the six new SSE events to `FleetSessionStreamHandler`. Simultaneously add the matching frontend TS types (`web/src/components/fleet/fleetUtils.ts`) and the SSE event handlers in `StudioChat.tsx` / `FleetExecutionPanel.tsx` receiving side. Add API client (`web/src/api/fleetChat.ts` additions). Extend `SaveFleetPlanArgs` (§4.3). No visual changes yet — handlers may be no-ops that store the event, awaiting M9. Ship.

**M9 — Frontend: FleetExecutionPanel per-agent lanes visual redesign.**
Multi-column layout when parallelism > 1; single-column timeline preserved when ≤ 1. Task-board pill strip. Delegate polish to `shared/frontend` skill during M9. Ship.

**M10 — Frontend: TemplateDetail / PlanDetail refactor into tabs + per-agent drawer editor.**
Overview / Settings / Agents tabs with URL-hash routing. Drawer save calls `PATCH /api/fleet-plans/{key}/agents/{agent_key}`. Live validation. Ship.

**M11 — Frontend: SessionTrace mailbox + tasks tabs, sidebar "+ New Template" / clone / import-export.**
Ship.

**M12 — `software-dev.yaml` migrate + upgrade.**
Edit the bundled template as in §6. Add the E2E parallel-flow test (`fleet_software_dev_parallel_test.go`). Ship.

Each milestone must:

1. Pass `make build-all` (Go lint + web lint gate).
2. Pass `make test-unit`.
3. Pass `make test-integration` (from M3 onward — first milestone touching DB).
4. Update the deepest applicable AGENTS.md when it introduces a new invariant (`pkg/fleet/AGENTS.md` gains scope entries; `web/src/AGENTS.md` gains the parallel-lane rule; `ent/AGENTS.md` needs no update — pattern already documented).

---

## 9. Invariants Preserved (must not regress)

1. **Multi-tenant data boundary** (root `AGENTS.md`): every new durable table lives in `ent/team/` and is accessed only through `pkg/store/entstore` via the `Store → .ForOrg(slug) → .ForTeam(slug)` router. No raw connections; no cross-scope reads. New store interfaces live in `pkg/store/`, impls in `pkg/store/entstore/team_fleet_runtime.go`, exposed to handlers through `FleetStores`.
2. **Sandbox contract** (`pkg/sandbox/AGENTS.md`): fleet sessions with `Execution.Workspace == "isolated"` still route filesystem access through the sandbox `Backend`; the workspace field only changes *which* directory the agent sees, not *how* it is accessed.
3. **Inline Report Rendering Contract** (root `AGENTS.md`): new SSE events do not overlap `report_marker`; `` ```astonish-report `` semantics are frozen. New events fire on the fleet stream (`fleet_*` prefix), not the chat stream.
4. **`pkg/fleet/AGENTS.md` "When editing" rules**: schema changes are followed by validation + `pkg/daemon/run.go` update; no channel-specific code lands in `pkg/fleet`; `PlanActivator` changes coordinate with `pkg/scheduler`.
5. **Ent regeneration discipline** (`ent/AGENTS.md`): schema-only hand-edits, `go generate ./ent/team/...` (or `cd ent/team && go run generate.go`), single commit. **No Atlas migration files** — auto-migrate via `client.Schema.Create`.
6. **Serial-path regression floor**: when `MaxParallelAgents ≤ 1`, activation is serial; mailbox + task board remain always on.
7. **`Channel` interface stability**: the mailbox layers *alongside* `Channel`, not through it. `Channel.WaitForMessage` / `PostMessage` / `GetAgentMemory` signatures do not change.
8. **`FleetRecoverFunc` signature stability**: the existing `func(ctx, RecoverFleetConfig) error` is unchanged. New recovery logic lives in a sibling `RecoverActiveSessions` helper that *invokes* the recover fn per snapshot.
9. **SSE same-commit contract** (`web/src/AGENTS.md`): every new fleet SSE event lands with its frontend handler in the same PR (M8 bundles both).
10. **`monitor_state.go` untouched**: the GitHub Monitor State Store is not modified, renamed, or retired by this plan. It is orthogonal to session run-state.

---

## 10. Open Questions Deferred to Workers (NOT blockers)

These do not block plan approval — worker sessions decide during implementation using the guidance below:

- **S3 / HTTP upload artifact implementation.** M1 accepts the schema; actual upload code is a follow-up milestone. `github.com/aws/aws-sdk-go-v2` is NOT currently in `go.mod` — the S3-impl worker adds it then.
- **Task-board LLM tool schemas.** M5's worker writes the JSON schemas for `fleet_task_post` / `fleet_task_complete` / `fleet_task_fail` following the existing `Declaration() *genai.FunctionDeclaration` pattern in `pkg/tools/`.
- **Editor form UX polish.** M10 delegates visual detail (spacing, tab order, autocomplete presentation of `CapabilityRegistry`) to the `shared/frontend` skill during that milestone.
- **`FleetExecutionPanel` multi-column visual direction.** M9's worker produces a Figma-parity mock and confirms the visual direction before coding, delegating to `shared/frontend`.
- **Stopped-row TTL / GC janitor.** Deferred to a follow-up worker task, out of scope here.

Resolved during this revision (no longer open):

- ~~ProgressTracker concurrency~~ — evaluator confirmed `AddMilestone` is already `sync.RWMutex`-protected.
- ~~Migration tooling~~ — auto-migrate via `client.Schema.Create`; no Atlas.
- ~~Recovery signature~~ — `FleetRecoverFunc` unchanged; wrap it in `RecoverActiveSessions`.
- ~~SSE stream location~~ — `FleetSessionStreamHandler`, not `chat_runner`.

---

## 11. Approval Gate

**This plan is decision-complete. It requires user approval before a worker session starts M1.**

To approve: reply "approved" (or "approved, start M1" to hand off immediately). To revise a decision: name the section + the change you want. Plan mode is sticky; execution begins only when a user starts a worker session (e.g. `/start-work` or a dedicated `build` agent).
