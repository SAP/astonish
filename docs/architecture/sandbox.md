# Sandbox & Containerization

> **Scope:** This document covers the **Incus/LXC backend** used in personal
> mode and single-host deployments. For the Kubernetes cloud deployment with
> NVIDIA OpenShell (per-process Landlock/seccomp isolation, L7 network policy,
> Istio mesh), see [openshell-sandbox-backend.md](openshell-sandbox-backend.md).

## Overview

Astonish executes all agent tool calls -- file edits, shell commands, code execution, MCP servers -- inside isolated Linux containers rather than on the host machine. This provides security isolation, reproducible environments, and the ability to give each chat session its own full Linux workspace without risk to the host.

The sandbox system is built on **Incus** (the community fork of LXD), which manages LXC system containers. On Linux, Incus runs natively. On macOS and Windows, Incus runs inside a Docker container that Astonish manages automatically.

## Key Design Decisions

### Why LXC/Incus Instead of Docker

LXC system containers behave like lightweight VMs -- they run a full init system, support multiple processes, and present a standard Linux environment. This is critical because agent tool calls expect a real OS: background processes (`process_start`), package managers, Docker-in-Docker for MCP servers, and interactive shells all work naturally. Docker application containers are designed for single-process workloads and would require significant workarounds.

Incus was chosen over raw LXC for its SDK, snapshot management, networking, and storage pool abstractions.

### Why Overlayfs Instead of Full Clones

The original approach cloned template containers using Incus's built-in copy mechanism. On a `dir` storage backend, this performs a full filesystem copy of ~900MB (Ubuntu + tools), taking 10-30 seconds per session. This was unacceptable for interactive use where the user expects near-instant responses.

The solution uses **overlayfs** to layer a thin writable directory on top of a read-only template snapshot. Container creation drops to ~200ms:

- Create container from a tiny (~670 byte) shell image: ~45ms
- Mount overlayfs on the container's rootfs: ~4ms
- Start the container: ~1s (overlaps with LLM response generation)

The key insight is that we don't need Incus to manage the filesystem at all -- we create an empty container and mount the real filesystem ourselves using the kernel's overlay driver.

### Why Unprivileged by Default

Containers run unprivileged (Linux user namespaces) by default. Container root (UID 0) maps to an unprivileged host UID (e.g., 100000+), so even a container escape doesn't grant host root access. This required solving the UID shifting problem for overlayfs (described below).

Privileged mode is available as a configuration option for environments like Proxmox where nested user namespaces don't work.

### Why a Custom NDJSON Protocol

Tools execute inside containers via `astonish node` -- a headless tool execution server that speaks a simple newline-delimited JSON protocol over stdin/stdout. This was chosen over alternatives because:

- **HTTP**: Requires networking and port management; adds latency and complexity.
- **gRPC**: Heavy dependency for what is essentially request-response RPC.
- **Raw exec per tool call**: ~500ms overhead per `incus exec` invocation. A persistent process eliminates this.
- **NDJSON over stdio**: Zero network overhead, no port conflicts, trivial framing (one JSON object per line), works with Incus's non-interactive exec.

## Architecture

### Container Lifecycle

```
Template Creation (one-time, during `astonish sandbox init`):
  1. Launch Ubuntu 24.04 container from remote image
  2. Install core tools (git, curl, Node.js 22, Python, uv, Docker, build-essential)
  3. Install optional tools (from catalog, if any selected)
  4. Push astonish binary into /usr/local/bin/astonish
  5. Shift rootfs UIDs for unprivileged containers (one-time recursive chown)
  6. Snapshot the container (captures the shifted filesystem)
  7. Create overlay shell image (tiny image for fast container creation)

Session Container Creation (per chat session, on first tool call):
  1. Create container from overlay shell image (~45ms)
  2. Mount overlayfs: lowerdir=template-snapshot, upperdir=per-session dir (~4ms)
  3. Pre-seed idmap state (tells Incus the rootfs is already UID-shifted)
  4. Start container
  5. Launch `astonish node` process inside container
  6. Wait for ready signal over NDJSON protocol

Tool Execution:
  Host sends:  {"id":"1", "tool":"read_file", "args":{"path":"/etc/hosts"}}
  Node replies: {"id":"1", "result":{...}}

Idle/Cleanup:
  - Idle watchdog stops containers after configurable timeout (default 10 min)
  - Overlay is preserved -- restart re-mounts and resumes instantly
  - Session deletion unmounts overlay, removes per-session dirs, destroys container
```

### Overlay Filesystem Architecture

```
Session Container Rootfs = overlayfs(
  lowerdir = [custom-template-upper :] @base-snapshot-rootfs   (read-only)
  upperdir = /var/lib/incus/disks/astonish-overlays/<container>/upper  (writes go here)
  workdir  = /var/lib/incus/disks/astonish-overlays/<container>/work
)
```

The overlay stack supports multiple layers:

- **@base template**: Single lower layer -- the snapshot rootfs containing Ubuntu + all tools.
- **Custom template**: Two lower layers -- the template's own upper directory stacked on top of @base's snapshot. Only the diff from @base is stored.
- **Session**: Writes from the running session go to the per-session upper directory. The template layers are shared (read-only) across all sessions using that template.

This means 100 concurrent sessions using the same template share a single ~900MB base, each adding only their own modifications.

### UID Shifting for Unprivileged Containers

The challenge: unprivileged containers use Linux user namespaces where container UID 0 maps to host UID 100000+. Normally, Incus performs a recursive `chown` (called "ShiftPath") on the entire rootfs every time a container starts. On overlayfs, this triggers copy-up of every file, defeating the purpose of the overlay.

The solution is a two-phase approach:

1. **Template creation (one-time)**: After installing all tools but before taking the snapshot, `ShiftTemplateRootfs()` performs a recursive `chown --from=0:0` on the template's rootfs. The `--from=0:0` flag ensures only unshifted files are changed, preventing double-shifting. The snapshot captures the pre-shifted state.

2. **Session creation (every time)**: `preseedIdmap()` copies the container's `volatile.idmap.next` into `volatile.last_state.idmap`. This tells Incus "the rootfs is already at the correct UIDs" so it skips its own ShiftPath entirely. Container start is instant.

All containers share the same idmap range, so the pre-shifted snapshot lower layers have correct ownership for every session container.

### Template System

Templates form a hierarchy:

- **@base** (`astn-tpl-base`): The root template. Created during `sandbox init`. Has a real Incus snapshot that serves as the overlay lower layer for everything.
- **Custom templates** (`astn-tpl-<name>`): Created from @base using overlay. Their "state" IS the overlay upper directory -- only the diff from @base. Creating a custom template takes ~260ms.
- **Promotion**: A custom template can replace @base by materializing its overlay into a flat rootfs (via rsync) and creating a new snapshot.

Template metadata (name, description, binary hash, nesting requirements, overlay chain) is persisted in a JSON registry at `~/.local/share/astonish/sandbox/templates.json`.

### Binary Refresh

Astonish pushes its own binary into containers so `astonish node` can run. When the host binary changes (new version, development rebuild), `RefreshAllIfNeeded()` detects the SHA-256 mismatch, pushes the new binary, and re-snapshots the template. This runs as an async singleton to avoid blocking session creation.

After pushing, `verifyBinaryInContainer()` checks the file size inside the container matches the source binary. This prevents corrupted binaries from being baked into template snapshots (a real bug that was hit when `go build` was writing the binary simultaneously with the push).

During refresh, `RemountDependentOverlays()` finds all running session containers whose overlay references the old snapshot, stops them, unmounts stale overlays, remounts with fresh snapshot inodes, and restarts. This runs under a write lock (`templateSnapshotMu`) while session creation holds a read lock, preventing races.

### Node Protocol

The `NodeClient` manages a persistent NDJSON connection to an `astonish node` process:

- **Sequential dispatch**: One request at a time (mutex-protected). Tool calls don't run concurrently within a single container.
- **Auto-restart**: If the node process crashes, the next `Call()` restarts it transparently.
- **10MB scanner buffer**: Handles large responses (e.g., reading big files).
- **30-second startup timeout**: Waits for the `{"ready": true}` signal.

The `LazyNodeClient` wraps `NodeClient` with deferred initialization:

- **Two-phase init**: Phase 1 creates the container (needed by MCP transport). Phase 2 starts the node process (needed by built-in tools). These run in the background so the LLM can start generating a response while the container is still spinning up.
- **`BindSession()`**: Triggers initialization. Idempotent -- multiple callers block on the same init.

The `NodeClientPool` maps session IDs to `LazyNodeClient` instances:

- **`Alias()`**: Maps child session IDs (sub-agents) to the parent's client so they share the same container.
- **`ReplaceSession()`**: Destroys the current container and creates a new client with a different template. Updates all aliases pointing to the old client.
- **Idle watchdog**: Background goroutine checks every 60 seconds, stops containers that have been idle longer than the configured timeout.

### Cross-Platform Support

```
Linux:   Host --> Incus (Unix socket) --> LXC containers
macOS:   Host --> Docker (astonish-incus container) --> Incus (TCP:8443) --> LXC containers
Windows: Same as macOS
```

On non-Linux platforms, Astonish manages a Docker container (`astonish-incus`) that runs the Incus daemon. The Docker container uses a persistent volume for all Incus data, supports auto-upgrade on version mismatch, and exposes the Incus API on TCP port 8443 with TLS client certificate authentication.

All filesystem operations that touch the overlay system (mount, chown, stat, rsync) are dispatched through `remote_ops.go`, which transparently routes to either local OS calls (Linux) or `docker exec` commands (macOS/Windows).

### Sandboxed MCP Transport

MCP (Model Context Protocol) servers run inside containers via `ContainerMCPTransport`. This implements the `mcp.Transport` interface by:

1. Starting the MCP server process via `ExecNonInteractive` with `SeparateStderr` (critical -- stderr would corrupt JSON-RPC on stdout).
2. Filtering stdout so only JSON-RPC lines reach the MCP SDK's `IOTransport`.
3. Capturing discarded non-JSON stdout separately for bounded diagnostics when package managers or runtimes print failures before an `initialize` EOF.
4. Providing a default `PATH` that includes `/root/.local/bin` (where uv/npm install tools).

### Security Configuration

Security hardening varies by platform:

| Setting | Linux Native (unprivileged) | Docker+Incus | Privileged |
|---|---|---|---|
| `security.privileged` | false | false | true |
| `security.syscalls.intercept.mknod` | true | -- | -- |
| `security.syscalls.intercept.setxattr` | true | -- | -- |
| `security.syscalls.deny_default` | true | -- | -- |
| `security.syscalls.deny_compat` | true | -- | -- |
| `security.guestapi` | false | -- | -- |

On Docker+Incus, the Docker VM itself is the security boundary, so nested seccomp filtering is unnecessary and may not work.

## Key Files

| File | Purpose |
|---|---|
| `pkg/sandbox/overlay.go` | Overlay filesystem: layer resolution, image creation, mount/unmount, remount |
| `pkg/sandbox/template.go` | Template creation, tool installation, binary pushing, refresh, promote |
| `pkg/sandbox/idmap.go` | UID shifting, idmap pre-seeding for instant container start |
| `pkg/sandbox/lifecycle.go` | Session container creation, destruction, health checks, pruning |
| `pkg/sandbox/node.go` | NodeClient, LazyNodeClient, NodeClientPool -- NDJSON tool RPC |
| `pkg/sandbox/node_tool.go` | ADK tool wrapper that proxies execution through the node protocol |
| `pkg/sandbox/config.go` | Sandbox configuration, defaults, validation, security settings |
| `pkg/sandbox/detect.go` | Platform detection (Linux native vs Docker+Incus) |
| `pkg/sandbox/incus.go` | Core Incus SDK wrapper (create, start, stop, exec, push, pull) |
| `pkg/sandbox/exec.go` | Interactive and non-interactive container process execution |
| `pkg/sandbox/registry.go` | Session-to-container mapping persistence |
| `pkg/sandbox/template_registry.go` | Template metadata persistence |
| `pkg/sandbox/docker.go` | Docker+Incus runtime for macOS/Windows |
| `pkg/sandbox/mcp_transport.go` | Sandboxed MCP server transport |
| `pkg/sandbox/remote_ops.go` | Cross-platform filesystem operation dispatch |
| `pkg/sandbox/setup.go` | Runtime initialization and status reporting |
| `pkg/sandbox/escalate.go` | Sudo self-escalation for Linux |

## Interactions

- **Agent Engine**: `WrapToolsWithNode()` wraps built-in tools with node proxies before they reach the agent. Only tools in the `containerTools` whitelist are wrapped; host-side tools (memory, credentials, scheduler) pass through.
- **MCP Integration**: `ContainerMCPTransport` runs MCP servers inside containers. The `LazyNodeClient.EnsureContainerReady()` method provides the container for MCP without requiring the full node process.
- **Sessions**: The `SessionRegistry` maps session IDs to container names. Session deletion triggers container destruction via `DestroyForSession()`.
- **Fleet**: Fleet sessions use `WrapToolsWithNodeClient()` with dedicated node clients (not pooled), giving each fleet agent its own isolated container with a workspace created via `git clone --local`.
- **Daemon**: The daemon calls `SetupSandboxRuntime()` on startup, `PruneStaleOnStartup()` to clean containers from previous runs, and `StartIdleWatchdog()` for idle timeout management.
