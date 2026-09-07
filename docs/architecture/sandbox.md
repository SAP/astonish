# Sandbox & Containerization

> **Scope:** Local sessions (macOS and Linux) use **Docker OverlayFS**
> (`pkg/sandbox/docker/`). Kubernetes and OpenShell are separate backends;
> see [sandbox-backends.md](sandbox-backends.md) and
> [openshell-sandbox-backend.md](openshell-sandbox-backend.md).
>
> Incus is removed. `sandbox.backend: incus` is a legacy alias for `docker`.
> There is no `astonish-incus` image.

## Overview

Astonish executes agent tool calls — file edits, shell commands, code
execution, MCP servers — inside isolated Linux containers rather than on the
host. Local Studio and daemon use the same overlay contract as Kubernetes:

- Session containers named `astonish-session-*` from
  `ghcr.io/sap/astonish-sandbox-base`
- Template layers on the host (`LayersDir`)
- Live upper in a named Docker volume
- `fuse-overlayfs` (default) composes `/sandbox/rootfs`
- Tools and the in-container browser run via `astonish-shell` chroot

macOS runs that Docker engine in Colima or Docker Desktop. Linux uses a native
`dockerd`. Same backend, same CloakBrowser path.

## Key Design Decisions

### Why Docker OverlayFS

Incus (LXC) was the original local backend. It required a nested Incus daemon
inside `ghcr.io/sap/astonish-incus` on macOS, and native Incus on Linux. That
split is gone: both OSes create Docker session containers and compose the same
overlay as the K8s sandbox-base entrypoint.

Docker application containers are a poor fit for a full agent workspace on
their own (init, nested Docker, package managers). Overlay composition inside
`sandbox-base` gives a real rootfs without cloning a 900MB tree per session.

### Why OverlayFS instead of full clones

Cloning a full template rootfs takes 10–30 seconds. OverlayFS layers a thin
writable directory on top of a read-only template:

```
Session rootfs = overlayfs(
  lowerdir = [@base :] custom-template layers    (read-only, shared)
  upperdir = Docker volume astonish-session-*-overlay/upper
  workdir  = Docker volume astonish-session-*-overlay/work
)
composed at /sandbox/rootfs
```

- **@base**: content-addressed layer with Debian + core tools + optional
  browser stack (CloakBrowser lives under `/home/browser/.cloakbrowser`, not
  on `PATH` as `chromium`).
- **Custom templates**: additional layers stacked on `@base`.
- **Session**: writes go to the per-session upper. Templates are shared.

### Why a custom NDJSON protocol

Tools execute inside containers via `astonish node` — a headless tool
execution server that speaks newline-delimited JSON over stdin/stdout.

- **HTTP**: Requires networking and port management.
- **gRPC**: Heavy for request-response RPC.
- **Raw exec per tool call**: hundreds of milliseconds of `docker exec`
  overhead. A persistent process eliminates that.
- **NDJSON over stdio**: Zero extra network, trivial framing, works with
  `docker exec`.

## Architecture

### Container lifecycle

```
Template creation (`astonish sandbox init` / Studio Base Sandbox rebuild):
  1. Ensure @base exists as an overlay layer
  2. Run BuildTemplate with ParentLayers: [@base]
  3. Install core tools, optional tools, then browser (CloakBrowser)
  4. Capture the overlay upper as a content-addressed layer

Session creation (per chat session, on first tool call):
  1. docker run --name astonish-session-* sandbox-base
  2. Entrypoint composes overlay at /sandbox/rootfs
  3. Launch `astonish node` inside the chroot
  4. Wait for ready signal over NDJSON

Idle/cleanup:
  - Idle watchdog stops containers after the configured timeout
  - Overlay upper is preserved on the Docker volume
  - Session deletion removes the container and overlay volume
```

### Template system

- **@base**: Root layer. Created during `sandbox init` or Studio Rebuild Base
  Layer. Has a real rootfs that every session stacks.
- **Custom templates**: Diffs from `@base` (or another parent). Saving a
  session as a template captures the live upper.
- **Promotion / overwrite**: Flattening a custom template onto `@base` is
  explicit. See `pkg/sandbox/AGENTS.md` template-overwrite rules.

Template metadata is persisted in the template registry (JSON in personal
mode, Postgres in platform mode).

### Binary / image refresh

The session runs `ghcr.io/sap/astonish-sandbox-base`. The overlay holds user
tools. Rebuilding `@base` does not rewrite running session uppers; new
sessions pick up the new layer chain.

### Node protocol

The `NodeClient` manages a persistent NDJSON connection to `astonish node`:

- Sequential dispatch (mutex-protected).
- Auto-restart if the node process crashes.
- 10MB scanner buffer; 30-second startup timeout.

`LazyNodeClient` defers init: phase 1 creates the container (needed by MCP);
phase 2 starts the node process (needed by built-in tools).

`NodeClientPool` maps session IDs to clients, with `Alias()` for sub-agents
and an idle watchdog.

### Cross-platform

```
Linux:   Host --> dockerd --> astonish-session-* (overlay at /sandbox/rootfs)
macOS:   Host --> Colima / Docker Desktop --> same
Windows: Same as macOS via Docker Desktop / WSL2
K8s:     pods + layers PVC (separate backend; same overlay contract)
```

### Sandboxed MCP transport

MCP servers run inside the overlay via `ContainerMCPTransport`:

1. Start the MCP process via backend `Exec` with separate stderr.
2. Filter stdout so only JSON-RPC reaches the SDK `IOTransport`.
3. Default `PATH` includes `/root/.local/bin` (uv/npm).

### Security

| Setting | Docker OverlayFS | Kubernetes | OpenShell |
|---|---|---|---|
| Isolation | Docker container + overlay chroot | Pod + NetworkPolicy | Landlock + seccomp + L7 |
| Org network | `astonish-org-<slug>` bridge | NetworkPolicy labels | Gateway policy |
| Browser | CloakBrowser in overlay via CDP | Same in-pod | OpenShell browser wire |

On Docker Desktop / Colima the VM is an additional boundary. Nested Docker
(`docker.io` in the base layer) is supported inside the session overlay.

## Key files

| File | Purpose |
|---|---|
| `pkg/sandbox/docker/` | Local OverlayFS backend: session, exec, capture, overlay |
| `pkg/sandbox/k8s/` | Kubernetes backend (same overlay contract) |
| `pkg/sandbox/openshell/` | OpenShell gateway backend |
| `pkg/sandbox/baseconfig/` | `@base` install recipe (core / optional / browser) |
| `pkg/sandbox/node.go` | NodeClient, LazyNodeClient, NodeClientPool |
| `pkg/sandbox/backend.go` | Backend interface |
| `pkg/sandbox/backend_from_config.go` | Kind selection (`incus` → `docker`) |
| `docker/sandbox-base/Dockerfile` | Session/pod image (entrypoint + wrappers) |

## Interactions

- **Agent Engine**: `WrapToolsWithNode()` wraps built-in tools with node
  proxies. Host-side tools (memory, credentials, scheduler) pass through.
- **MCP**: `ContainerMCPTransport` runs MCP servers inside the overlay.
- **Sessions**: `SessionRegistry` maps session IDs to container names.
  Deletion destroys the container and overlay volume.
- **Fleet**: Dedicated node clients per fleet agent.
- **Daemon**: `BackendFromAppConfig` selects Docker / K8s / OpenShell.
  Empty `sandbox.backend` and legacy `incus` both become Docker.
