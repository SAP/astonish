# Daemon & Scheduler

## Overview

The Astonish daemon is a background service that orchestrates the entire runtime: configuration loading, credential store, provider setup, agent initialization, channels, scheduler, fleet plan activation, sandbox management, and the Studio HTTP server. It installs as a platform-native service (systemd on Linux, launchd on macOS, Windows service on Windows).

The scheduler provides cron-based job execution with three modes: routine (headless flow execution), adaptive (chat-based instruction execution), and fleet poll (GitHub issue monitoring).

## Key Design Decisions

### Why a Daemon Instead of On-Demand Processes

Astonish needs long-running capabilities:

- **Channels**: Telegram long polling and IMAP email polling require persistent connections.
- **Scheduler**: Cron jobs need a process to fire them.
- **Fleet**: Fleet sessions run for minutes to hours with continuous agent collaboration.
- **Sandbox**: Container lifecycle management, idle watchdog, and orphan cleanup need a coordinator.

A daemon provides all of these with a single process, proper service management (auto-restart on crash), and clean shutdown.

### Why Platform-Native Service Installation

Instead of requiring users to set up systemd units or launchd plists manually, `astonish daemon install` generates the correct service definition for the current platform:

- **Linux**: systemd service unit with auto-restart, journald logging.
- **macOS**: launchd plist with keep-alive.
- **Windows**: Windows service registration.

`astonish daemon start/stop/status` abstract the platform-specific commands.

### Why Hot-Reload for Channels

Channel configuration (Telegram bot token, email IMAP settings, allowlists) can change without restarting the daemon. The daemon watches for config changes and hot-reloads channel adapters, avoiding downtime for the rest of the system.

### Why Three Scheduler Execution Modes

Different use cases need different execution models:

- **Routine**: Runs a predefined flow YAML headlessly. Best for deterministic, repeatable tasks (backups, reports, deployments).
- **Adaptive**: Sends instructions as a chat message to the shared ChatAgent. The agent decides how to handle it, can use any tool, and adapts to the current situation. Best for tasks that need judgment.
- **Fleet poll**: Delegates to the PlanActivator for GitHub Issues monitoring. Not a direct execution mode but a scheduling trigger for fleet activation.

### Why Dual-Scope Jobs (Personal vs Team)

Platform mode has **two scheduler lanes**, mirroring credentials:

| | Personal job | Team job |
|---|---|---|
| Storage | `personal_{uid}.scheduled_jobs` | `team_{slug}.scheduled_jobs` |
| Who manages | Owner only | Team admin |
| Credentials at tick | `MergedCredentialStore(personal, team)` — same as Studio chat | Team credentials only |
| Identity | Runs as `OwnerID` | Headless (`SystemUserID`) |
| Delivery | `owner` only | `owner` / `team` / `members` / `target` |
| Use case | User OAuth / personal API keys | Shared service accounts |

**Do not** inject an owner's personal vault into a team job. That would let shared team automation silently use private secrets and fan results out to other members. Users who need personal credentials should create a **personal** job (or **fork** a team job to personal) instead of publishing the secret to the team.

Scope transfer (team admin only), same shape as credentials:

| Action | Direction | Semantics | Endpoint |
|---|---|---|---|
| Promote | personal → team | **Move** (personal removed) | `POST /api/scheduler/jobs/publish` |
| Fork | team → personal | **Copy** (both remain); delivery forced to `owner` | `POST /api/scheduler/jobs/fork` |

`test_first` / `RunNow` use the same credential injection as cron for that job's scope, so a dry-run cannot succeed with personal creds then fail on a team-only tick.

Personal jobs store `team_slug` (captured from the active team at create time) so team credential/flow/MCP fallback still works.

### Why Adaptive Scheduler Needs Network Policy Parity

Studio chat applies platform/org/team network allow rules via `ChatRunner` (pre-seed + PolicyAllow auto-approve). Adaptive scheduler jobs use a separate sandbox (`scheduler-adaptive-{jobID}`) and previously skipped that path, so hosts allowed in chat (e.g. `**.cloud.sap`) stayed CONNECT-403 on cron.

The scheduler injects `NetworkPolicyStores` and OpenShell gateway config into the exec context. **Persisted allow rules must be active before the first in-sandbox egress**: `NodeTool` PreSeeds (and waits for policy load) after `EnsureReady` and before the first tool `Call`, and OpenShell `CreateSession` also merges DB allow endpoints into the create-time policy when known. Post-result PreSeed in `SessionBridge` / `ChatRunner` is a no-op once that has run. Chat “Approve broader” persists an allow rule to the team/org store so future scheduler sandboxes inherit it — without that rule, headless jobs still cannot reach the host.

**Routine scheduler jobs** (`executeRoutine` → headless flow) share the same contract: `NetworkPolicyStores` from the multi-tenant tick plus `WithGatewayConfig` so PreSeed runs before the first curl/CONNECT. Adaptive already had gateway; routine previously omitted it and no-oped PreSeed.

**Interactive Flows** share Chat PreSeed semantics as well:
- Classic Flows UI (`HandleChat` / `POST /api/chat`) and headless `FlowRunHandler` call `withRuntimeNetworkPolicyContext` (+ sandbox template context) so DB allows land in the flow sandbox.
- `run_flow` via `InteractiveFlowRunner` uses `withFlowNetworkPolicyContext` (gateway + stores from parent ChatRunner ctx or request `Services`) so nested flow sandboxes do not start with presets-only egress.

### Adaptive Sandboxes Are Ephemeral Per Run

The ADK session id stays stable (`scheduler-adaptive-{jobID}`) for transcript continuity, but the **OpenShell sandbox is destroyed at the start and end of each adaptive run**. That avoids reusing a registry row whose gateway pod was idle-evicted or deleted (which previously caused `EnsureReady` to no-op and credential vault `PushFile` to fail with “sandbox not found”). OpenShell `CreateSession` also heals stale registry rows when `GetSandbox` returns NotFound/Gone. After destroy, the ToolNodePool client for that session id is **Remove**d so the next tick’s `EnsureReady` rebinds instead of treating a gone pod as still ready.

### Adaptive Delivery Is Last-Wins (Not Full Transcript)

Adaptive job email/channel delivery must not concatenate every model turn. Mid-run narration (“I need to paginate…”) is dropped: only the **final** complete text turn is kept (same batch semantics as interactive email). When the job uses the report contract (`write_file` + `` ```astonish-report ``), delivery prefers the **written report markdown body** over a bare fence. Oversized messages truncate from the **end** (suffix kept) so the conclusion is preserved.

### Why Exponential Backoff on Failures

When a scheduled job fails, the scheduler applies exponential backoff before retrying. This prevents:

- Hammering an unavailable external service every minute.
- Accumulating costs from repeated LLM calls that always fail.
- Flooding logs with identical errors.

## Architecture

### Daemon Startup Sequence

```
astonish daemon start
    |
    v
1. Load configuration (config.yaml)
2. Open credential store (AES-256-GCM encrypted)
3. Run credential migration (plaintext config -> encrypted store)
4. Set provider environment variables from credential store
5. Initialize session store (FileStore)
6. Run device authorization check
7. Initialize memory system (indexer, vector store, embeddings)
8. Create ChatAgent (SystemPromptBuilder, tools, callbacks)
9. Initialize channels (Telegram, email) with hot-reload
10. Initialize scheduler with job registry
11. Initialize fleet PlanActivator with GitHub monitoring
12. Setup sandbox runtime (Docker OverlayFS / K8s / OpenShell from config)
13. Prune stale containers from previous runs
14. Start idle watchdog for sandbox containers
15. Start Studio HTTP server
16. Register signal handlers (SIGINT, SIGTERM)
    |
    v
Running: serving API, processing channels, executing schedules
    |
    v
Shutdown signal:
  1. Stop channels
  2. Stop scheduler
  3. Stop fleet sessions
  4. Cleanup sandbox containers (stop, don't destroy)
  5. Close credential store
  6. Close HTTP server
```

### Scheduler Architecture

```
Job Definition (API / schedule_job tool):
  - Scope: personal (default) | team
  - Name, cron, mode: routine | adaptive | fleet_poll
  - OwnerID + team_slug (personal jobs)
    |
    v
MultiTenantScheduler.tick() every 30s:
  - orgs → teams → due team jobs (team creds only)
  - orgs → members → due personal jobs (merged creds)
    |
    v
Execution:
  - Routine: load flow YAML, run headless
  - Adaptive: ChatAgent turn (personal = OwnerID session; team = SystemUserID)
  - Fleet poll: PlanActivator.CheckForWork()
    |
    v
Result delivery:
  - Personal: forced to owner channels
  - Team: owner / team / members / target
  - On failure: exponential backoff
```

### PID File Management

The daemon writes a PID file on startup and removes it on clean shutdown. This enables:

- `astonish daemon status` to check if the daemon is running.
- Detection of stale daemons (PID file exists but process doesn't).
- Prevention of multiple daemon instances.

### Periodic Cleanup

The daemon runs periodic maintenance:

- **Expired session cleanup**: Removes sessions older than the configured TTL.
- **Orphan container pruning**: Removes sandbox containers whose sessions no longer exist.
- **Interval**: Configurable, default is every few hours.

## Key Files

| File | Purpose |
|---|---|
| `pkg/daemon/run.go` | Main daemon Run() orchestration, startup/shutdown sequence |
| `pkg/daemon/multi_tenant_scheduler.go` | Platform tick loop: team + personal lanes, credential injection |
| `pkg/daemon/service.go` | Platform-native service installation (systemd/launchd/Windows) |
| `pkg/daemon/config_reload.go` | Hot-reload for channel configuration |
| `pkg/scheduler/scheduler.go` | Cron-based job scheduler (legacy single-instance path) |
| `pkg/scheduler/store.go` | Job definition, delivery modes |
| `pkg/scheduler/executor.go` | Job execution: routine, adaptive, fleet poll |
| `pkg/api/scheduler_handlers.go` | REST CRUD + RunNow with scope-aware stores |
| `pkg/tools/schedule_tool.go` | LLM tools (`schedule_job`, etc.) with personal default |
| `ent/personal/schema/scheduled_job.go` | Personal job schema |
| `ent/team/schema/scheduled_job.go` | Team job schema |

## Interactions

- **Agent Engine**: The daemon creates and owns the ChatAgent instance. Adaptive scheduler jobs use it.
- **Channels**: Initialized during daemon startup, hot-reloaded on config changes.
- **Fleet**: PlanActivator manages fleet plan lifecycle, GitHub monitoring.
- **Sandbox**: Runtime setup, container pruning, idle watchdog all run within the daemon.
- **API/Studio**: The HTTP server is started as part of the daemon.
- **Sessions**: Session cleanup runs as a periodic daemon task.
- **Credentials**: Credential store is opened at daemon startup, used throughout.
- **Configuration**: Config is loaded once at startup, channel changes trigger hot-reload.
