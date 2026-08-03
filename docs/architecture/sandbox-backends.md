# Sandbox Backends (Incus and Kubernetes)

> **Status (Phase F shipped).**
> Sysbox is no longer required. The K8s backend selects one of four
> overlay strategies at deploy time — `fuse-overlayfs` via a device
> plugin (production default), kernel overlayfs via `hostUsers: false`
> (K8s 1.33+), `fuse-overlayfs` via `privileged: true` (dev/lab), or
> Sysbox (`runtimeClassName: sysbox-runc`, kept for operators who
> already run it). The same `astonish-sandbox-base` image and the
> same Go binary serve all four. `pkg/sandbox/k8s/image.go`
> auto-derives `imagePullPolicy` (`Always` for mutable tags such as
> `dev`/`latest`/`edge`/`nightly`; `IfNotPresent` for immutable tags
> and digest pins) so the same chart values work in dev and production.
> §10 captures the matrix explicitly. For step-by-step deployment,
> see `docs/deployment/kubernetes.md`.
>
> **Reading guide.** Where this document still says "Sysbox pod",
> read it as "selected privilege path"; the contract holds across
> all four. Sections that have shipped on top of the original Round 2
> design — multi-replica session registry (§5.16), team-template
> editor lifecycle (§5.17), synchronous `DeleteTemplate` GC pod
> (§5.6), `@base` UUID normalization at the pgstore boundary
> (§3.9) — are called out with **Status:** annotations.

## 1. Context & Motivation

Astonish executes agent tool calls inside isolated Linux containers. Today this is implemented against **Incus** (the LXD fork) as documented in `docs/architecture/sandbox.md`. The Incus-based implementation works well for single-host deployments -- personal mode on a developer laptop, or platform mode on a single VM.

As Astonish transitions to an enterprise platform used by many teams within a company, the sandbox tier must acquire **cloud qualities**:

- **Resilience** -- no single point of failure in the application tier.
- **Horizontal scalability** -- capacity grows by adding pods/nodes, not by vertical scaling.
- **Native Kubernetes operations** -- standard tooling (kubectl, Helm, RBAC, NetworkPolicy) applies to the whole system.
- **No externally-operated infrastructure** -- the sandbox tier should live in the same Kubernetes cluster as the application tier, not on separate VMs.

The current Incus-on-a-single-host model cannot satisfy these requirements: Incus is a stateful daemon; its containers, overlay layers, and templates are local to one host; multi-pod deployments would diverge because each Incus has its own private state.

This document specifies a **pluggable sandbox backend architecture** that preserves the Incus implementation for personal mode and existing deployments while introducing a new **Kubernetes** backend for cloud-native platform deployments.

The goal is capability parity: every operation that works on Incus works on K8s. The deployment choice is an operator decision, not a feature compromise.

### Why elevated capabilities (and four ways to get them)

Standard Kubernetes pods cannot do what Astonish sandboxes need:

- `mount -t overlay` from inside the pod (or `fuse-overlayfs` as a userspace equivalent) — required for Astonish's overlay template model.
- Run `systemd` as PID 1 (required for many sandbox workloads).
- Run nested Docker (required when user tools use containerized services).
- Install packages whose postinstall scripts use `mount`/`chroot`.

A naïvely privileged pod (`privileged: true`) has these capabilities but breaks the Kubernetes security posture: full host access on escape, blocked by Pod Security Admission `restricted`/`baseline`, and unacceptable on multi-tenant clusters.

**Phase F supports four privilege paths**, all driven from the same backend code and the same base image (see §10 for the full matrix):

1. **FUSE device plugin** (production default). PSA `baseline`. The cluster's FUSE device plugin advertises `smarter-devices/fuse`; sandbox pods request one device and run `fuse-overlayfs` from inside the user namespace.
2. **User namespaces** (K8s 1.33+ beta-on / 1.36+ GA). PSA `baseline`. `hostUsers: false` enables an in-pod kernel overlayfs mount with userxattrs.
3. **Privileged pods** (dev / lab). PSA `privileged`. The fastest path to working overlays on Proxmox/LXC nodes that block `mount -t overlay` regardless of capabilities; sandbox pod uses `fuse-overlayfs` plus in-container `mknod /dev/fuse`.
4. **Sysbox** (optional). PSA `baseline`. `runtimeClassName: sysbox-runc` plus kernel overlayfs. Kept for operators who already run Sysbox in their fleet and prefer it.

| Capability | Regular pod | Privileged pod | FUSE / userns / Sysbox |
|---|---|---|---|
| `mount -t overlay` (or `fuse-overlayfs`) inside pod | Blocked | Works | Works |
| Run `systemd` as PID 1 | Broken | Works | Works |
| Nested Docker | Blocked | Works | Works |
| `privileged: true` in spec | No | Yes | **No** |
| PSA baseline-compatible | Yes | No | **Yes** (paths 1, 2, 4) |
| Host kernel escape if compromised | N/A | Full | User-namespace remapped UID only |
| Requires nested virtualization | No | No | **No** |

Alternatives considered and rejected:

- **gVisor** — blocks `mount`, breaks systemd, breaks nested Docker. Incompatible with Astonish's design.
- **Kata Containers** — VM isolation is stronger but requires nested virtualization on nodes (limits deployment targets) and does not solve the template-distribution problem. Overkill for the trusted/semi-trusted enterprise threat model.
- **Privileged StatefulSet by default** — works but violates PSA posture and hands full node access to any sandbox-escape; only acceptable as the explicit dev/lab path 3.

## 2. Architecture Overview

A single `SandboxBackend` interface abstracts all sandbox operations. Three production implementations and one test implementation exist:

```mermaid
graph TB
    Callers["Astonish Callers<br/>pkg/api/sandbox_handlers.go<br/>pkg/api/team_template_handlers.go<br/>pkg/agent (flow/tool exec)<br/>pkg/chat (chat runner)<br/>pkg/fleet (monitors)"]

    Callers --> Interface["SandboxBackend<br/>(interface)"]

    Interface --> Incus["IncusBackend<br/>pkg/sandbox/incus/"]
    Interface --> K8s["K8sBackend<br/>pkg/sandbox/k8s/"]
    Interface --> OpenShell["OpenShellBackend<br/>pkg/sandbox/openshell/"]
    Interface --> Mock["MockBackend<br/>pkg/sandbox/mock/"]

    Incus --> IncusTarget["Local Incus Daemon<br/>Unix socket or TCP"]
    K8s --> K8sTarget["Kubernetes API<br/>+ sandbox pods (4 privilege paths, §10)<br/>+ RWX PVCs (CephFS/NFS/EFS)"]
    OpenShell --> OSTarget["OpenShell Gateway<br/>gRPC (Helm subchart)<br/>+ Istio mTLS<br/>+ Landlock/seccomp<br/>+ L7 egress policy"]
```

**Backend selection:**

| Mode | Backend |
|------|---------|
| Personal (`astonish studio`) | Always `IncusBackend`; config ignored |
| Platform (`astonish daemon run`), `sandbox.backend: incus` | `IncusBackend` |
| Platform (`astonish daemon run`), `sandbox.backend: k8s` | `K8sBackend` |
| Platform (`astonish daemon run`), `sandbox.backend: openshell` | `OpenShellBackend` |
| Unit tests | `MockBackend` (in-memory) |

The abstraction lives in `pkg/sandbox/backend.go`. Existing Incus code is reorganized under `pkg/sandbox/incus/` and wrapped to implement the interface. K8s code is new under `pkg/sandbox/k8s/`. The OpenShell backend (`pkg/sandbox/openshell/`) delegates sandbox lifecycle to an NVIDIA OpenShell gateway deployed as a Helm subchart, gaining per-process Landlock/seccomp isolation and L7 egress policy enforcement on top of standard Kubernetes pod boundaries. See [`openshell-sandbox-backend.md`](openshell-sandbox-backend.md) for full details. Callers see only the interface.

## 3. `SandboxBackend` Interface Specification

The interface encapsulates every operation Astonish performs against the sandbox tier. Concrete Go signatures will follow existing style in `pkg/sandbox/`; this is the conceptual contract.

### 3.1 Session lifecycle

```go
type SandboxBackend interface {
    // Create a new session container from a template.
    // Returns the session handle or an error.
    CreateSession(ctx context.Context, req SessionSpec) (*Session, error)

    // Start a session container that is currently stopped/evicted.
    // For K8s backend: may recreate the pod and re-mount persisted upper layer.
    StartSession(ctx context.Context, sessionID string) error

    // Stop a session container (idle eviction, pause).
    // For K8s backend: deletes pod after streaming upper layer via tar+zstd to
    // the uppers PVC for later resume. See §7 for the canonical tar pipeline.
    StopSession(ctx context.Context, sessionID string) error

    // Permanently destroy a session and all its data. Idempotent.
    // MUST remove the underlying container, writable layer, and any persisted state.
    DestroySession(ctx context.Context, sessionID string) error

    // Query current state.
    SessionState(ctx context.Context, sessionID string) (SessionState, error)
}
```

### 3.2 Exec and file I/O

```go
    // Execute a command non-interactively. Captures stdout/stderr/exit code.
    Exec(ctx context.Context, sessionID string, opts ExecOpts) (ExecResult, error)

    // Execute with a PTY. Streams are attached; supports window-resize.
    ExecInteractive(ctx context.Context, sessionID string, opts PTYOpts) (ExecStream, error)

    // Push a file into the sandbox at the given path with the given mode.
    PushFile(ctx context.Context, sessionID, path string, content []byte, mode os.FileMode) error

    // Pull a file from the sandbox.
    PullFile(ctx context.Context, sessionID, path string) ([]byte, error)
```

### 3.3 Templates

```go
    // List all templates visible to this backend.
    ListTemplates(ctx context.Context) ([]TemplateMeta, error)

    // Create a new template from base (typical wizard flow).
    CreateTemplate(ctx context.Context, req TemplateSpec) (*TemplateMeta, error)

    // Capture a live session as a new template. MUST NOT require pushing to a registry.
    SaveSessionAsTemplate(ctx context.Context, sessionID, name, description string) (*TemplateMeta, error)

    // Refresh a template in place (re-run apt update/install).
    RefreshTemplate(ctx context.Context, name string) error

    // Delete a template. Fails if any active session references it, unless force=true.
    DeleteTemplate(ctx context.Context, name string, force bool) error
```

### 3.4 Networking

```go
    // Ensure the org-scoped network primitives exist.
    // Incus: per-org bridge + profile.
    // K8s:   NetworkPolicy for labels matching the org.
    EnsureOrgNetwork(ctx context.Context, orgSlug string) error

    // Remove an org-scoped network. Used when an org is deleted.
    DeleteOrgNetwork(ctx context.Context, orgSlug string) error

    // Expose a port from a session container.
    ExposePort(ctx context.Context, sessionID string, port int, proto string) (ExposedAddr, error)
    UnexposePort(ctx context.Context, sessionID string, port int) error
```

### 3.5 Fleet containers

```go
    // Create (idempotent) a fleet-scoped container.
    EnsureFleetContainer(ctx context.Context, spec FleetSpec) error

    // List all sandbox containers visible to the backend (sessions + fleet).
    ListSessionContainers(ctx context.Context, filter ContainerFilter) ([]Session, error)
```

### 3.6 Diagnostics / introspection

```go
    // Return backend-specific capabilities for UI gating.
    Capabilities() BackendCapabilities

    // Return backend-specific health info.
    Health(ctx context.Context) (BackendHealth, error)
}
```

### 3.7 Shared types

All `SessionSpec`, `TemplateSpec`, `FleetSpec`, `ExecOpts`, `PTYOpts`, `ExecResult`, `ExecStream`, `Session`, `TemplateMeta`, `SessionState`, `ExposedAddr`, `BackendCapabilities`, `BackendHealth`, `ContainerFilter` live in `pkg/sandbox/types.go`. They are deliberately backend-neutral. Backend-specific fields (Incus snapshot names, K8s pod names) go into an opaque `backend_ref` string and are not interpreted by callers.

### 3.8 Template hierarchy, scope, and resolution

Templates form a **DAG** through a `parent_template_id` pointer. Every template except the global `@base` points at a parent; cycles are rejected at write time by a check constraint + application-side validation.

Scope is an enum:

| Scope | Meaning | Who may create / save-as |
|---|---|---|
| `global` | Deployment-wide; reserved for `@base` singleton | `superadmin` only |
| `org` | Visible to one org | org `owner` |
| `team` | Visible to one team within an org | team `admin` |
| `personal` | Visible only to the owning user | the user |

Fleet templates are not a separate scope: they are `scope='team'` rows with `purpose='fleet'` set. This keeps RBAC aligned with team admin authority and avoids duplicating the scope machinery. The `fleet_plans.template_slug` column continues to reference templates by slug.

Each template owns a **top layer** (`top_layer_id`) and inherits all ancestor layers. A session from template `T` composes the full chain from the bottom layer of the root ancestor to `T.top_layer_id` as ordered `lowerdir`s (see §5.3). Layers are content-addressed and shared (see §5.11).

Template creation, refresh, and deletion are interface operations on `SandboxBackend` (§3.3). Scope-aware default resolution (§5.13) is implemented in calling code over the interface, not in the backend itself -- backends do not know about orgs/teams/users.

### 3.9 The `@base` template

`@base` is a **singleton per deployment**, materialized as a single row with `slug='base'`, `scope='global'`, `parent_template_id=NULL`, `is_default_for_scope=true`. It cannot be deleted. Only `superadmin` can edit it.

First-run bootstrap (see §5.6) seeds `@base.top_layer_id` from a layer extracted from the prebuilt `astonish-sandbox-base:<version>` image.

**`@base` UUID normalization (Status: shipped).** The Go runtime refers to `@base` by the literal string `"@base"` (`sandbox.BaseTemplateID`), but the `sandbox_templates.id` column is `UUID NOT NULL`. The pgstore boundary normalizes both directions:

- The well-known UUID `a0000000-0000-4000-8000-000000000001` is reserved for the `@base` row and seeded by migration `platform/005_seed_base_template.sql`.
- `pkg/store/pgstore/sandbox_sessions.go::Put` rewrites `template_id == "@base"` to `baseTemplateUUID` before the SQL `INSERT`. Reads perform the inverse mapping when returning rows that match the well-known UUID.

This keeps the K8s backend free of database-schema knowledge: the backend speaks only string template IDs; UUID coercion happens at the persistence boundary.

**Updating `@base`** is done by the admin starting a session from `@base`, customizing it, and invoking **save-as-@base**:

1. Tar-stream the session upper to the layers PVC (while computing `sha256`); register as a new `sandbox_layers` row with `scope='global'`, `parent_layer_id=<old @base.top_layer_id>`.
2. In a **single PG transaction**:
   - Insert new layer row (ref_count starts at 0)
   - `UPDATE sandbox_templates SET top_layer_id = <new>, version = version + 1, refreshed_at = now() WHERE slug = 'base' AND version = <expected>` -- optimistic concurrency; conflicting simultaneous saves cause the second to retry
   - Increment ref_count on the new layer (template reference)
   - Decrement ref_count on the old layer
   - Insert audit row (`base_template_updated`, old/new layer IDs, actor_id, actor_pod)
3. Running sessions are **unaffected** (the kernel holds the old lowerdir inodes; overlay still works). New session creations pick up the new chain.
4. Team and fleet templates that inherit from `@base` stay pinned to the old `@base.top_layer_id` until they are **explicitly rebuilt** via `RefreshTemplate` by a team admin (decision: no automatic cascade).

This lazy-refresh model is a deliberate safety property: a single `@base` edit cannot accidentally disrupt every running session and every team template. The old layer stays alive as long as anything references it (ref_count > 0); GC reclaims it only after all references drop and the grace period elapses.

## 4. Incus Backend (Reference Implementation)

The existing code in `pkg/sandbox/` is refactored into `pkg/sandbox/incus/` with no behavioral changes. `IncusBackend.CreateSession` wraps today's `EnsureSessionContainer` / `EnsureOrgSessionContainer` logic. `IncusBackend.Exec` wraps `ExecInstance`. All current features -- overlay fast-clone, UID-shift, `org_network.go` bridges, tunnel.go socat-over-exec, template snapshots -- continue to work exactly as documented in `docs/architecture/sandbox.md`.

Personal mode (`astonish studio`) hardcodes this backend and is unaffected by the new abstraction.

Platform mode deployments that already use Incus continue to work by setting `sandbox.backend: incus` (the default).

## 5. K8s Backend

### 5.1 Deployment shape

```mermaid
graph TB
    subgraph Cluster["Kubernetes Cluster"]
        subgraph NS1["Namespace: astonish (Helm release namespace)"]
            API1["API pod 1<br/>unprivileged<br/>default runtime"]
            API2["API pod 2<br/>unprivileged<br/>default runtime"]
            Worker["Worker pod<br/>unprivileged<br/>default runtime"]
        end

        subgraph NS2["Namespace: astonish-sandbox"]
            Sess1["astn-sess-...<br/>Privilege path from §10<br/>Labels: org=my-org, team=general"]
            Sess2["astn-sess-...<br/>Privilege path from §10<br/>Labels: org=other, team=ops"]
            Fleet1["astn-fleet-...<br/>Privilege path from §10<br/>Labels: type=fleet"]
        end

        subgraph Storage["Storage"]
            Layers["RWX PVC: astonish-layers<br/>/mnt/astonish-layers<br/>(RW in template-builder, RO in chat)"]
            Uppers["RWX PVC: astonish-uppers<br/>/mnt/astonish-uppers<br/>(RW; persisted uppers for resume)"]
        end

        subgraph DB["PostgreSQL"]
            PG["platform.sandbox_layers<br/>platform.sandbox_templates<br/>team_slug.sandbox_sessions<br/>team_slug.chat_session_events"]
        end

        SeedJob["Helm hook: release-sandbox-seed<br/>(idempotent @base seeder, §5.6)"]
    end

    API1 -->|K8s API| Sess1
    API2 -->|K8s API| Sess2
    Worker -->|K8s API| Fleet1

    Sess1 --- Layers
    Sess1 --- Uppers
    Sess2 --- Layers
    Sess2 --- Uppers
    Fleet1 --- Layers
    Fleet1 --- Uppers

    SeedJob --> Layers
```

Each sandbox pod also uses an `emptyDir` volume at `/var/astonish/overlay`
for the overlay upper + work directories (must be on the same filesystem
for `fuse-overlayfs` `renameat` to work).

**Pod-name conventions** (deterministic, exposed in `kubectl get pods`):

- Chat sessions: `astn-sess-<sessionID>` (truncated to 27 chars after the prefix; DNS-1123 sanitized).
- Fleet members: `astn-fleet-<plan>-<instance>`.
- Template editor sessions: `astn-sess-<sessionID>` with label `astonish.io/purpose=team-template-editor`.
- Layer-GC pods: `astn-layer-gc-<layerID-prefix>` (short-lived; see §5.6).
- Helm seed Job pods: `{release}-sandbox-seed-...`.

### 5.2 Namespace model (decision Q2 = single namespace + labels)

All sandbox pods — sessions, fleet members, and template editors — live in a single namespace. The Helm chart derives the name from `namespaces.prefix` (default `astonish`), yielding `astonish-sandbox`. Operators can override `namespaces.sandbox` directly when they need a non-derived name; the Go runtime accepts whatever the chart writes into `config.sandbox.kubernetes.namespace`.

Isolation is via **labels + NetworkPolicy**:

- `astonish.io/org` — organization slug
- `astonish.io/team` — team slug
- `astonish.io/session-id` — session identifier (for session pods)
- `astonish.io/type` — `session` | `fleet` | `template-builder`
- `astonish.io/template` — source template slug
- `astonish.io/purpose` — `team-template-editor` (only set on team-template editor sessions; controls RW vs RO mount of the layers PVC)

NetworkPolicy rules (applied namespace-wide):

- Ingress: only from the `astonish` namespace (API/Worker pods) or from matching `astonish.io/org` labels (intra-org traffic between fleet members and sessions).
- Egress: DNS; registry for base image pulls; configured LLM/tool endpoints; explicit allow-list per org.
- Cross-org ingress/egress denied by default.

This is decision Q2(b): simpler initial implementation; upgrade to namespace-per-org can be done later without breaking the interface contract.

### 5.3 Session lifecycle

**Volumes mounted into every session pod:**

| Volume | Type | Mount | Mode | Purpose |
|---|---|---|---|---|
| `layers` | RWX PVC `astonish-layers` | `/mnt/astonish-layers` | RO (chat) / RW (template editor) | Shared content-addressed layer store + `@base/rootfs` |
| `uppers` | RWX PVC `astonish-uppers` | `/mnt/astonish-uppers` | RW | Persisted uppers for resume after eviction |
| `overlay` | `emptyDir` | `/var/astonish/overlay` | RW | Hosts both `upper/` and `work/` on the same filesystem |

**Why a single `emptyDir` for both upperdir and workdir.** `fuse-overlayfs` performs `renameat(workdir, …, upperdir, …)` during directory copy-up. If `upper` and `work` lived on separate bind mounts — even from the same underlying device — the kernel would return `EXDEV`, because `renameat` refuses to cross mount boundaries. A single `emptyDir` with two subdirectories (`/var/astonish/overlay/upper` and `/var/astonish/overlay/work`) guarantees they share a mount.

**RW vs RO layers PVC.** Chat-session pods mount `/mnt/astonish-layers` read-only. Team-template editor pods (label `astonish.io/purpose=team-template-editor`, see §5.17) mount it read-write so `SaveSessionAsTemplate` can stage the new layer directory directly without an intermediate exec channel.

**Pod entrypoint** (`pkg/sandbox/k8s/overlay_entrypoint.go`, baked into `astonish-sandbox-base` at `/usr/local/bin/astonish-sandbox-entrypoint`):

The entrypoint runs as PID 1 with the privilege path selected at chart-install time and performs, in order:

1. **Resume path** — if `/mnt/astonish-uppers/<session-id>/upper.tar.zst` exists, stream it back into the local `emptyDir`:
   ```sh
   tar --numeric-owner --xattrs --acls -I zstd \
       -xf /mnt/astonish-uppers/<session-id>/upper.tar.zst \
       -C /var/astonish/overlay/upper
   ```
2. **Pre-seed first-level directories.** Create the top-level FS layout (`/proc /sys /dev /mnt /sandbox /var/astonish` and friends) inside `/var/astonish/overlay/upper` so subsequent bind mounts have valid mount points after the overlay is composed. This is what makes `chroot /sandbox/rootfs` resolve `/proc`, `/sys`, `/dev`, the layer PVC mounts, and the persisted-uppers mount.
3. **Compose the overlay.** Build `lowerdir` from the layer chain (top-most layer first, so the leaf wins on conflicts):
   ```sh
   LOWER=$(echo "$ASTONISH_LAYER_CHAIN" | awk -F, '{
       for (i = NF; i > 0; i--)
           printf "/mnt/astonish-layers/%s/rootfs%s", $i, (i > 1 ? ":" : "")
   }')
   fuse-overlayfs \
       -o lowerdir=$LOWER,upperdir=/var/astonish/overlay/upper,workdir=/var/astonish/overlay/work,squash_to_root \
       /sandbox/rootfs
   ```
   `squash_to_root` flattens UID/GID inside the merged tree to 0/0 so user-namespaced pods see consistent ownership (apt/dpkg, systemd, etc. all expect root-owned `/etc`, `/usr`, …).
4. **Bind kernel pseudo-filesystems** into the merged tree: `/proc`, `/sys`, `/dev`, `/dev/pts`, `/dev/shm` rebound at `/sandbox/rootfs/{proc,sys,dev,dev/pts,dev/shm}`.
5. **Bind `/etc/resolv.conf`** from the host (pod) into the merged tree so DNS works without copy-up.
6. **Bind the layer + uppers PVCs** under `/sandbox/rootfs/mnt/{astonish-layers,astonish-uppers}` so save/eviction tar pipelines running inside the chroot see them at the same paths.
7. **`chroot /sandbox/rootfs`** and `exec /usr/local/bin/astonish node` (or `exec /sbin/init` if the template requires systemd). Before the chroot, the entrypoint bind-mounts the trusted base-image binary `/usr/local/bin/astonish-host` over `/sandbox/rootfs/usr/local/bin/astonish`; later `Backend.Exec` calls use the base-namespace `/usr/local/bin/astonish` wrapper to chroot into the same composed rootfs.

**Sandbox binary loader contract.** The intended sandbox `astonish-host` build is static (`CGO_ENABLED=0`), but the `astonish-sandbox-base` image also creates `/lib/ld-musl-x86_64.so.1 -> x86_64-linux-gnu/ld-linux-x86-64.so.2` as a compatibility guard. Without that loader path, a dynamically linked musl binary bind-mounted into the chroot fails with exit `127` and no node handshake (`backend exec: no node response in stdout`) even though `/usr/local/bin/astonish` exists.

**Browser-in-sandbox.** Direct K8s uses the same browser `Manager` callbacks as the other sandbox backends, but tunnels through `Backend.ExecStreaming` instead of Incus or OpenShell APIs. Browser tools resolve the current chat session, launch CloakBrowser/Chromium inside `/sandbox/rootfs` via `/usr/local/bin/astonish-shell`, keep that exec stream open so the browser stack stays alive, and connect Rod to CDP through a `socat` tunnel on port `9222`. When KasmVNC is installed, the launcher uses `Xkasmvnc :0` and exposes the web handoff endpoint on port `6901`; otherwise it falls back to `Xvfb :99` for browser automation without visual handoff. The browser dependency catalog explicitly installs GLib/GObject (`libglib2.0-0` on Debian bookworm, `libglib2.0-0t64` on Ubuntu noble) because CloakBrowser is a standalone Chromium binary and apt cannot infer its shared-library dependencies.

**Rejected alternative.** A separate init container performing the overlay mount would require either a `hostPath` volume (rejected — violates Pod Security Admission `baseline`) or a CSI volume that supports `mountPropagation: Bidirectional` between init and main containers (complex; not all CSI drivers honour it). Doing the mount in the main container's PID 1 keeps the pod spec minimal and works with any CSI driver.

**Create** (`CreateSession`):

1. Resolve the requested template and its **layer chain**:
   - Read `sandbox_templates` row from PG; if `template_slug` is not supplied, apply default-template resolution (§5.13).
   - Walk `parent_template_id` from the chosen template up to the root (`@base`), collecting each ancestor's `top_layer_id`. Reverse to obtain bottom-up order.
   - Verify every layer exists on the layers PVC at `/mnt/astonish-layers/<layer-id>/rootfs/`.
   - Reject if chain depth > `sandbox.layers.maxChainDepth` (default 20; §5.11).
2. Generate deterministic pod name: `astn-sess-<sessionID>` (truncated to 27 chars after the prefix; DNS-1123 sanitized; `pkg/sandbox/k8s/session.go::podNameForSession`).
3. Build `PodSpec`:
   - Privilege path from §10 (FUSE plugin / userns / privileged / Sysbox), driven by `sandbox.overlay.*` Helm values.
   - `imagePullPolicy` derived by `pkg/sandbox/k8s/image.go::imagePullPolicy()` (`Always` for mutable tags such as `dev`/`latest`/`edge`/`nightly`; `IfNotPresent` for digest pins and immutable tags).
   - Namespace: configured sandbox namespace (default `astonish-sandbox`).
   - Labels: `astonish.io/{org,team,session-id,type=session,template}`; optional `astonish.io/purpose=team-template-editor` for editor sessions.
   - Annotations: `astonish.io/created-by`, `astonish.io/created-at`, `astonish.io/layer-chain=<comma-separated-ids>`.
   - Volumes: `layers` (RO or RW depending on purpose), `uppers` (RW), `overlay` (`emptyDir`).
   - Main container entrypoint as above; resource requests/limits from `sandbox.requests.*` and `sandbox.limits.*` (auto-derived from limits when requests are zero — see §6).
4. **Self-heal step (Status: shipped).** Before returning, `CreateSession` verifies the pod actually exists in the cluster — not just in any cached registry entry. This protects against stale registry rows pointing at pods that were deleted out-of-band (e.g., node failure, manual `kubectl delete`); when the verification fails, the registry entry is dropped and the create proceeds normally.
5. `kubectl apply` via client-go. Watch pod to `Ready`.
6. Insert row into `sandbox_sessions` (team schema) with `session_id`, pod name, namespace, `template_id` (UUID-normalized; §3.9), `chat_session_id`, `created_at`, `user_id`.
7. Return `Session` with `backend_ref` = pod name.

**Start / Stop** (decision Q1(b) — evict via tar stream to the uppers PVC):

- Sessions stay running while active. On idle timeout:
  - Each session pod mounts only its own uppers PVC subdirectory (`subPath: <session-id>`) at `/mnt/astonish-uppers`. The full PVC root is not visible to the chrooted sandbox, so a session cannot enumerate or read another session's persisted upper directory through `/mnt/astonish-uppers`.
  - `StopSession` first marks the row `evicting`, then persists the live pod's writable layer by streaming `/var/astonish/overlay/upper` to that mounted session subdirectory via
    `tar --numeric-owner --xattrs --acls -I "zstd --adapt -T0" --exclude=/var/astonish/overlay/upper/mnt/astonish-uppers -C /var/astonish/overlay/upper -cf /mnt/astonish-uppers/upper.tar.zst.tmp . && mv .../upper.tar.zst.tmp .../upper.tar.zst`.
    Only after that capture succeeds does it delete the pod. If capture fails, the pod stays running so data is not lost.
  - The same persistence script is installed as a Kubernetes container `preStop` hook with a 90-second termination grace period. This covers manual `kubectl delete pod` and normal Kubernetes termination paths that bypass the backend's `StopSession` method.
  - Row in `sandbox_sessions` stays; status updates `evicting` → `evicted` and clears the pod binding after deletion. At eviction time, the original resolved layer chain is retained in the registry so resume does not silently fall back to plain `@base` for configured base/team-template sessions.
- On next tool interaction, `StartSession` recreates the pod with the same `ASTONISH_SESSION_ID`; the entrypoint resume step (above) streams `/mnt/astonish-uppers/upper.tar.zst` back into the local `emptyDir` before mounting the overlay. Resume latency: 1–5 s depending on upper-layer size.
- Isolation invariant: the persisted-upper path is derived only from the registry row's exact session ID. API callers never provide an upper path, unsafe session IDs are rejected before any pod/tar/cleanup script is built, and cleanup deletes only the validated session subdirectory.

This preserves Incus's "container exists but stopped, resume later" semantics while keeping each session's upper layer isolated under its own uppers-PVC directory.

**Destroy** (`DestroySession`) — the parity-critical operation:

1. Delete pod via the K8s API.
2. Kubelet unmounts overlay and deletes the `emptyDir`-backed upper layer automatically.
3. Delete any Services / Ingress rules for the session.
4. Remove persisted upper (if previously evicted): `rm -rf /mnt/astonish-uppers/<session-id>/`.
5. Delete row from `sandbox_sessions`.
6. Emit audit event.

**Guarantee:** when `DestroySession` returns successfully, no trace of the session remains on any node, in the layers/uppers PVCs, or in PG. This matches Incus's `incus delete --force` semantics. Every deletion code path (session delete API, org delete cascade, idle orphan pruning) calls `DestroySession` and obtains the same guarantee.

**State** (`SessionState`):

| K8s phase | Astonish state |
|---|---|
| `Pending` | `creating` |
| `Running` (Ready=true) | `running` |
| `Running` (Ready=false) | `starting` |
| `Succeeded` / `Failed` | `stopped` |
| `NotFound` + row in PG with `stopped` | `evicted` |
| `NotFound` + no row | `not_found` |

### 5.4 Exec

**Non-interactive** (`Exec`) — client-go `remotecommand` using SPDY (decision Q5):

- POST `/api/v1/namespaces/{sandboxNamespace}/pods/<pod>/exec?command=...&stdin=false&stdout=true&stderr=true&container=sandbox`
- Stream stdout/stderr to buffers; capture exit code from SPDY channel 3.
- Match current Incus `ExecNonInteractive` semantics (exec helpers in `pkg/sandbox/exec.go`).

**Interactive PTY** (`ExecInteractive`):

- Same API with `tty=true&stdin=true`.
- Resize via SPDY control frames (client-go `remotecommand.TerminalSize` channel).
- Preserves Astonish's current PTY behavior (window-resize control frames in today's Incus websocket path).

### 5.5 File push / pull

Kubernetes has no native file API for pods. Use **tar over exec** (same pattern as `kubectl cp`):

**Push:**
```
exec: tar -xf - -C <dirname(path)>  (stdin = tar stream containing the file)
```

**Pull:**
```
exec: tar -cf - <path>              (stdout = tar stream)
```

Implemented in `pkg/sandbox/k8s/files.go`. Matches capability of Incus Files API.

### 5.6 Templates

**Storage model (layer store; see §5.11 for the canonical spec):**

- Templates hold **identity and inheritance**; layers hold **content**.
- Each template row (`sandbox_templates`) has:
  - `scope`, `scope_ref_id` (see §3.8)
  - `parent_template_id` (nullable; only `@base` has `NULL`)
  - `top_layer_id` -- the content-addressed layer containing this template's own additions
  - `version` (optimistic concurrency)
  - `is_default_for_scope` (at most one per scope, scope_ref_id)
  - `purpose` (`NULL` or `'fleet'`)
- Layers live on the RWX layers PVC at `/mnt/astonish-layers/<sha256>/rootfs/` and are indexed in `sandbox_layers` (§5.11). A session composes the full chain as ordered lowerdirs (§5.3).
- No template ever owns its layers exclusively -- layer lifetime is governed by reference counting (§5.12).

**First-run bootstrap (Status: shipped as a Helm hook).**

The `@base` template is seeded by a Helm `post-install,post-upgrade` Job (`{release}-sandbox-seed`) defined at `deploy/helm/astonish/templates/sandbox/seed-job.yaml`. The Job:

- Runs the same `astonish-sandbox-base` image used by sandbox pods, with `imagePullPolicy: IfNotPresent` hard-pinned (the chart's runtime auto-detection does not apply here — operators bump the chart, expecting determinism).
- Mounts the `astonish-layers` PVC RW at `/mnt/astonish-layers`.
- Tars its own rootfs (excluding `/proc /sys /dev /mnt /sandbox /var/astonish`) into `/mnt/astonish-layers/@base/rootfs/`.
- **Idempotency guard:** if `@base/rootfs` exists and is non-empty, the Job exits 0 without re-tarring. To force a reseed, an operator clears `@base/rootfs/` (e.g., from a debug pod) and runs `helm upgrade`.
- `helm.sh/hook-delete-policy: before-hook-creation,hook-succeeded` — pods are auto-cleaned on success.
- Hook weight `5` so it runs after PVCs/RBAC are ready.

Migration `platform/005_seed_base_template.sql` inserts the `@base` row with the well-known UUID `a0000000-0000-4000-8000-000000000001` (see §3.9). The seed Job and the migration are independent: the migration creates the catalog row; the Job populates the layer bytes. Either one can be re-run independently.

**NFS / RWX caveat.** When `rm -rf` runs against `@base/rootfs/` while another pod has files open, the underlying RWX filesystem may surface `.nfs00...` silly-rename markers. Operational practice: kill consuming sandbox pods before clearing `@base/rootfs/`, and prefer GNU `rm` (e.g., from `debian:bookworm-slim`) over BusyBox `rm` to avoid spurious "directory not empty" failures.

**CreateTemplate** (building a new template by customizing a parent):

Input: `parent_template_slug`, new `slug`, `scope`, `scope_ref_id`, optional `customization_commands` (list of shell commands), optional `purpose`.

1. Resolve the parent's layer chain (walk `parent_template_id`).
2. Create a short-lived **template-builder pod**:
   - Privilege path from §10, label `astonish.io/type=template-builder`.
   - Overlay composed from the parent's full layer chain as lowerdirs + fresh `emptyDir` upper.
   - `/mnt/astonish-layers` mounted RW (builder is trusted).
3. Exec customization commands in the builder pod (apt install, file copies, project clone, build steps — typical "ready-to-code" fleet template setup falls here).
4. On success: run the in-pod tar-to-layer pipeline (see below), yielding a new content-addressed layer.
5. In a single PG transaction: insert layer row, insert template row, increment ref_count on the new layer.
6. Delete the builder pod.

If any step fails, the transaction rolls back and the staging directory is removed; no partial state.

**SaveSessionAsTemplate** — capability-critical, no registry push (decision Q4(a)):

Input: live session ID, new `slug`, `scope`, `scope_ref_id`.

The difference from `CreateTemplate` is that the content source is the **session's upper layer** (the user's effective changes since the template was composed). Parent is the session's current template.

1. Session pod is running; `/mnt/astonish-layers` is mounted RW inside it (team-template editor sessions get this automatically via the `astonish.io/purpose=team-template-editor` label; see §5.17).
2. Astonish exec's into the session pod and runs the in-pod tar-to-layer pipeline, streaming **only `/var/astonish/overlay/upper`** (not the merged view):
   ```sh
   tar --numeric-owner --xattrs --acls -I "zstd --adapt -T0" \
       -C /var/astonish/overlay/upper -cf - . \
     | tee >(sha256sum > /tmp/sha) \
     | tar --numeric-owner --xattrs --acls -I zstd \
       -C /mnt/astonish-layers/__staging-<session-id>/rootfs -xf -
   ```
   (In-pod pipe; the layers PVC sees a single sequential writer.)
3. Rename staging directory to `/mnt/astonish-layers/<sha256>/`. If a directory with that sha already exists (content already stored as a layer under a different scope, for example), skip the rename and remove staging.
4. In a single PG transaction:
   - `INSERT INTO sandbox_layers ... ON CONFLICT DO NOTHING` — **deduplication falls out automatically**: identical upper contents produce identical sha256 and therefore reuse an existing layer.
   - `INSERT INTO sandbox_templates (slug, scope, parent_template_id=<session.template_id>, top_layer_id=<sha256>, ...)`.
   - Increment `ref_count` on the layer (template reference).
5. Typical duration: 1–5 seconds for normal sandboxes. No registry round-trip.

The `pkg/sandbox/k8s/template.go::buildCaptureScript` helper builds the capture command line; the API-layer `TemplatePersister` callback (set on `k8s.Config`) is invoked after a successful capture so the calling code can persist the template metadata into the application store without coupling the backend to schema details.

Authorization: scope-appropriate actor required. `save-as-@base` is a privileged variant that updates `@base.top_layer_id` in place (§3.9) instead of creating a new template row — restricted to `superadmin`.

**Rationale (tar+zstd vs rsync).** rsync's cost on RWX filesystems is dominated by per-file metadata round-trips (`readdir`, `stat`, `setattr`, open/close per file). For a typical `node_modules` or Python venv with tens of thousands of files, that is 100k+ metadata RPCs (CephFS MDS, NFS, or equivalent). A single tar stream reads the source tree sequentially (local disk, fast) and produces one sequential writer on the PVC, bypassing most metadata chatter. Empirically 3–10× faster for cold copies of many-small-file trees. `zstd --adapt` scales compression level with CPU headroom; `-T0` uses all cores.

**Preservation requirements (both eviction and save paths):**
- `--numeric-owner` — required because user-namespaced pods remap UID/GID; textual user lookups on the shared PVC would not resolve.
- `--xattrs --acls` — preserves `security.capability`, SELinux labels, and ACLs set by apt/dpkg.
- Sparse files handled natively by GNU tar (`--sparse` added if profiling shows value).
- `tar` (GNU with xattr support) and `zstd` must be present in `astonish-sandbox-base` image (they are).

**RefreshTemplate (Status: stub — returns "not yet implemented").**

The Round 2 design called for `RefreshTemplate` to walk the parent chain, run the stored `build_spec`, and atomically swap `top_layer_id` with optimistic CAS on `version`. The K8s backend currently returns `errors.ErrUnsupported` for this path; the API layer surfaces a clear "not yet implemented" message. The semantics described above (running sessions unaffected because the kernel retains old lowerdir inodes; ref-count decrement on the old layer; explicit invocation by a scope-appropriate admin; **no automatic cascade** when `@base` updates) remain the design contract for when the implementation lands.

Until then, refresh = manual: a team admin starts a new session from the parent, re-applies customizations, and saves a new template (which gets a new slug or, with explicit superadmin action, replaces `top_layer_id` on the existing slug).

**DeleteTemplate (Status: synchronous GC pod shipped).**

The original Round 2 design imagined a deferred reconciler. The shipped implementation is more direct: `DeleteTemplate` synchronously launches a short-lived GC pod that reclaims layer bytes before returning.

`pkg/sandbox/k8s/template.go::DeleteTemplate`:

1. Refuse if any `sandbox_sessions` row references the template, unless `force=true` (in which case matching sessions are destroyed via §5.3 `Destroy`).
2. Refuse if any `sandbox_templates` row has this template as its `parent_template_id`, unless `force=true` (in which case still refuse — descendants must be migrated or deleted explicitly; force does not cascade template deletion).
3. Resolve the layer to free.
4. Launch a GC pod named `astn-layer-gc-<layer-id-prefix>` in the sandbox namespace:
   - Image: `alpine:3.21` (small, fast pull).
   - Mounts the `astonish-layers` PVC RW.
   - Command: `rm -rf /mnt/astonish-layers/<layer-id>/`.
   - `restartPolicy: Never`; `activeDeadlineSeconds` bounded.
5. Wait for the GC pod to terminate `Succeeded`; delete it.
6. In a single PG transaction:
   - Decrement `ref_count` on `top_layer_id`.
   - If `ref_count` reaches zero, `DELETE FROM sandbox_layers WHERE layer_id = ...`.
   - `DELETE FROM sandbox_templates WHERE id = ...`.
   - Audit.
7. `@base` cannot be deleted (enforced by the API-layer guard plus a check constraint on `slug != 'base'` in the delete path).

The application-layer `reclaimLayerBytes` helper (`pkg/api/team_template_handlers.go`) wraps this for the team-template editor flow: it re-reads the layer; if `ref_count == 0` it calls `Backend.DeleteTemplate` (which spawns the GC pod) followed by `layers.DeleteLayer`. It skips `@base` and empty-bytes layers.

**Why synchronous, not deferred.** Operators expect `DELETE /templates/<slug>` to free the bytes by the time it returns. A deferred reconciler with a 24h grace period (Round 2 design) is still useful as a safety net for refcount drift but is not the primary reclamation path. See §5.12 for the optional deferred reconciler (still on the roadmap).

### 5.7 Networking

**Org networks** (decision Q2(b) — labels + NetworkPolicy):

- `EnsureOrgNetwork(orgSlug)`:
  - Apply/update a `NetworkPolicy` in the sandbox namespace that selects pods with `astonish.io/org=<slug>`.
  - Ingress: from same-org pods + from the control-plane namespace.
  - Egress: DNS, allowed external endpoints, same-org pods.
- `DeleteOrgNetwork(orgSlug)`:
  - Remove the NetworkPolicy.
  - Caller is responsible for deleting the org's sandbox pods (the org-delete cascade handles this).

**Port exposure:**

- `ExposePort(sessionID, port, proto)`:
  - Create a `Service` (ClusterIP) in the sandbox namespace named `sess-<sessionID>-<port>` with selector `astonish.io/session-id=<id>` and port mapping.
  - Optional: create an `Ingress` (Gateway) entry if external URL exposure is requested (org-configured base domain).
  - Returns `ExposedAddr` = in-cluster DNS + optional external URL.
- `UnexposePort`:
  - Delete the Service and any Ingress entry.

**Tunnel to in-container services** (equivalent to current `pkg/sandbox/tunnel.go`/`dialer.go`):

- `socat STDIO TCP:127.0.0.1:<port>` via exec stream.
- Works identically on K8s backend; no change needed in calling code.

### 5.8 Fleet containers

Naming: `astn-fleet-<plan>-<instance>`.

Lifecycle is session-like (create pod, exec, destroy) with label `astonish.io/type=fleet`. `EnsureFleetContainer` is idempotent: check-then-create with optimistic concurrency.

`ListSessionContainers` filters pods by labels across `session` and `fleet` types.

### 5.9 RBAC

`astonish` API/Worker ServiceAccount needs:

- On the sandbox namespace:
  - `pods`: get, list, watch, create, delete, deletecollection
  - `pods/exec`: create
  - `pods/log`: get
  - `pods/portforward`: create
  - `services`: get, list, create, delete
  - `networkpolicies`: get, list, create, update, patch, delete
  - `persistentvolumeclaims`: get, list, create, delete (for optional per-session PVCs)
  - `jobs`: get, list, create, delete (for the Helm seed Job and any future bootstrap/builder Jobs)
- Cluster-scoped: none beyond discovery.

The Helm chart creates the `Role` and `RoleBinding` in the sandbox namespace bound to the control-plane ServiceAccount when `sandbox.rbac.create=true` (default).

### 5.10 Observability

- Session lifecycle events are logged through Astonish's existing event pipeline.
- Backend health is exposed via `Health(ctx)` and surfaced in `/api/healthz`:
  - K8s API reachable
  - Sandbox namespace exists
  - Layers PVC mount accessible from at least one node (probe pod or DaemonSet)
  - Base template present in the layers PVC (`/mnt/astonish-layers/@base/rootfs/`)
- `kubectl logs` is available for support/debugging; Astonish also provides UI-accessible log retrieval via backend interface.
- Pod lifecycle events are watched (not polled) -- improvement over the current Incus polling model.

### 5.11 Layer store

The layer store is the content-addressed backing for every template and every evicted session upper. It is shared across all Astonish pods via the same RWX PVC mount (CephFS / NFS / EFS / Manila / Azure Files), indexed in PG, and reclaimed by a combination of synchronous GC pods (§5.6) and an optional deferred reconciler (§5.12).

**Layout on the layers PVC:**

```
/mnt/astonish-layers/
  @base/
    rootfs/         ← seeded by the Helm hook Job (§5.6)
  <sha256>/
    rootfs/         ← read-only layer contents (extracted tar tree)
    meta.json       ← optional: { layer_id, parent_layer_id, size_bytes, created_at, scope }
```

`meta.json` is documented as an operator-friendly sidecar but is not currently written by the shipped K8s backend. PG remains the authoritative source of truth for ref_counts and scope; operators inspecting layers without PG access read directories and ask `psql` for metadata. Writing `meta.json` is a small, low-risk follow-up if operator tooling demands it.

**Properties:**

- **Content-addressed.** Layer ID is `sha256sum` of the canonical tar stream produced during layer creation (fixed options: `tar --numeric-owner --xattrs --acls --sort=name --mtime=@0`). Two independent `SaveSessionAsTemplate` operations that produce byte-identical uppers resolve to the same `layer_id`; the second operation finds the existing row via `ON CONFLICT DO NOTHING` and reuses it.
- **Immutable.** Once written, a layer's contents never change. Refreshes and edits produce a new layer; old layer remains until reclaimed.
- **Shared across pods.** Every Astonish pod (control-plane + sandbox) mounts `/mnt/astonish-layers` (RO in chat-session pods, RW in template editor / builder / save pods). No per-pod caching; the PVC is the canonical location.
- **Ordered by `parent_layer_id`.** The layer DAG mirrors the template DAG but at content-addressed granularity. A layer may appear in many templates' chains (sharing is the default).

**Chain-depth cap:**

- Overlayfs (kernel and `fuse-overlayfs`) supports hundreds of lowerdirs on modern kernels, but practical limits vary with distro and kernel build. Astonish caps template-chain depth at **`sandbox.layers.maxChainDepth` (default 20)**.
- Exceeding the cap on `CreateTemplate` / `SaveSessionAsTemplate` / `RefreshTemplate` is a validation error.
- A **flatten job** (Phase E) walks the DAG, identifies chains approaching the cap, and merges them into a single new layer. Not yet implemented.

**Operator introspection:**

- `psql -c "SELECT layer_id, parent_layer_id, scope, ref_count, size_bytes FROM platform.sandbox_layers ORDER BY created_at DESC LIMIT 50"`
- `ls /mnt/astonish-layers/` shows every layer dir.
- `kubectl -n astonish-sandbox get pods -o=jsonpath='{.items[*].metadata.annotations.astonish\.io/layer-chain}'` shows which chain each running session uses.

**Orphan detection** (runs hourly inside the deferred GC reconciler when enabled; see §5.12):

- PG layers without PVC dir → **error**: raise alert; do not auto-heal (a missing directory means data loss, which must be investigated).
- PVC dirs without PG row + older than 1 hour → **GC candidate**: deleted on next sweep. The 1-hour grace allows in-flight layer writes to complete their PG insert.

### 5.12 Layer reclamation and reference counting

Layer lifetime is managed by reference counting maintained in PG.

**What increments ref_count:**
- `INSERT INTO sandbox_templates`: `+1` on `top_layer_id`.
- `UPDATE sandbox_templates SET top_layer_id = <new>`: `+1` on new, `-1` on old.
- `UPDATE sandbox_sessions SET upper_layer_id = <new>` (set when an idle eviction is promoted into a real layer rather than a simple tar blob — deferred feature): `+1` on new, `-1` on old (nullable).

**What decrements ref_count:**
- `DELETE FROM sandbox_templates`: `-1` on `top_layer_id`.
- `DELETE FROM sandbox_sessions` (when `upper_layer_id IS NOT NULL`): `-1` on that layer.

**Transactional invariant.** Every write that changes a template's or session's `top_layer_id`/`upper_layer_id` column adjusts ref_counts in the **same PG transaction**. The application updates ref_count explicitly. A backstop trigger from migration `platform/004_sandbox_ref_count_triggers.sql` is **installed but disabled by default in production** — kept available so operators can flip it on for forensic runs without redeploying.

**Check constraint:** `CHECK (ref_count >= 0)` on `sandbox_layers`. A negative ref_count is a schema-level invariant violation and the transaction aborts.

#### 5.12.1 Synchronous reclamation (Status: shipped)

The primary reclamation path is the synchronous GC pod fired from `DeleteTemplate` (§5.6) and from the editor lifecycle's `reclaimLayerBytes` helper (§5.17). Operators delete a template; the bytes are gone before the API returns. No grace period, no scheduling delay.

This is sufficient for the team-template editor flow (operator-driven, low frequency, immediate-feedback expectation) and for explicit `DELETE` calls.

#### 5.12.2 Deferred reconciler (Status: shipped for direct K8s)

Some reclamation patterns benefit from background batching: orphan detection (§5.11), refcount drift recovery if the disabled trigger is ever needed, cleanup after migrations, and retention of old evicted session uppers. Direct K8s runs a single leader-elected reconciler in `pkg/sandbox/k8s/gc_reconciler.go` when platform mode uses PostgreSQL and `sandbox.backend: k8s`.

- Runs in the daemon, guarded by a PG advisory lock (`pg_try_advisory_lock(1267985313)`) so only one pod executes at a time across the deployment.
- Runs on a schedule from `sandbox.kubernetes.gc.interval_minutes` (default 60).
- Reclaims unreferenced layer rows after `sandbox.kubernetes.gc.layer_grace_hours` (default 24).
- Reclaims uppers-PVC directories with no matching `sandbox_sessions` row only after `sandbox.kubernetes.gc.orphan_upper_grace_minutes` (default 60), so races during session creation/eviction do not become data loss.
- Reclaims stale evicted session uppers after `sandbox.kubernetes.gc.evicted_upper_retention_hours` (default 336 / 14 days; set `evicted_upper_retention_disabled: true` to disable this retention sweep). This policy deletes `/mnt/astonish-uppers/<session-id>/` first and deletes the owning `sandbox_sessions` row only after the directory deletion succeeds.
- Skips pinned sessions, active states (`creating`, `running`, `resuming`, `evicting`), rows with a pod name, rows with live Running/Pending/Unknown K8s pods, non-K8s backend rows, and unsafe session IDs.
- Cleans crashed template-builder staging directories, orphan session/fleet pods whose registry row disappeared, and old completed GC helper pods.

**Retention semantics.** Stale evicted upper cleanup removes sandbox resumability, not chat history. The chat transcript, artifact metadata, and transcript fallback content remain in the session store. If the user later interacts with an old chat after its upper was reclaimed, Astonish creates a fresh sandbox from the template rather than restoring the old writable filesystem.

**Grace period rationale.** Short-lived zero-ref or untracked-upper windows happen legitimately during template refreshes, manual rollbacks, and eviction/create races. The layer and orphan-upper grace periods are safety margins around those windows. The evicted-upper retention period is a production storage bound for the `astonish-uppers` RWX PVC.

### 5.13 Default template resolution

> **Status: design only.** The cascading resolver below is the Round 2 design. The shipped K8s backend currently resolves to `@base` directly when no template is supplied; the per-scope `is_default_for_scope` machinery (settable by org/team admins) is not yet wired into the chat / session creation paths. Templates are explicitly selected by callers in the team-template editor flow (§5.17) and by the chat UI when the user picks one. Implementing the resolver is a follow-up.

When a user creates a session without specifying a template, Astonish resolves the default with a deterministic cascade. Resolution is performed in the calling code (chat / session API), not in the backend — backends do not know about orgs/teams/users.

```go
func resolveDefaultTemplate(user User) Template {
    // 1. Personal default
    if t := findDefault("personal", user.ID); t != nil { return t }
    // 2. Team default (if user belongs to a team)
    if user.TeamID != nil {
        if t := findDefault("team", *user.TeamID); t != nil { return t }
    }
    // 3. Org default
    if t := findDefault("org", user.OrgID); t != nil { return t }
    // 4. Global @base (always exists)
    return findDefault("global", nil)  // the @base singleton
}
```

`findDefault(scope, scope_ref_id)` queries:
```sql
SELECT * FROM sandbox_templates
 WHERE scope = $1
   AND (scope_ref_id = $2 OR ($2 IS NULL AND scope_ref_id IS NULL))
   AND is_default_for_scope = true
 LIMIT 1
```

**Setting a default:**

- `personal`: user sets their own via `PUT /users/me/default-template`.
- `team`: team admin sets via `PUT /teams/<team>/default-template`.
- `org`: org owner sets via `PUT /orgs/<org>/default-template`.
- `global` (`@base`): fixed; cannot be changed via API -- always points at the `@base` row.

All setters use the partial unique index on `sandbox_templates (scope, COALESCE(scope_ref_id::text, ''))` where `is_default_for_scope = true` to guarantee at most one default per scope tuple. Setting a new default unsets the previous one in the same transaction.

**Explicit override:** callers that supply `template_slug` in their session create request bypass resolution entirely. Authorization still applies (the user must be able to see the template in their scope).

### 5.14 Cross-pod session continuity and PG event journal

In a multi-pod Astonish deployment, any pod must be able to:

- **Create** a sandbox session (solved by shared PG + shared RWX PVC + shared K8s namespace; §5.3).
- **Exec** against any session created by any pod (solved by shared K8s namespace; `pods/exec` is stateless).
- **Continue a long-lived stream** (SSE chat, interactive PTY) when the originating pod dies or the client reconnects to a different pod.

The first two are trivially solved. The third requires a shared event journal.

**Data model:**

```sql
CREATE TABLE chat_session_events (
  id              BIGSERIAL PRIMARY KEY,
  chat_session_id UUID NOT NULL REFERENCES chat_sessions(id),
  seq             BIGINT NOT NULL,
  event_type      TEXT NOT NULL,           -- 'token'|'tool_call'|'tool_result'|'state'|'final'|...
  payload         JSONB NOT NULL,
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  producer_pod    TEXT NOT NULL,           -- diagnostic only
  UNIQUE (chat_session_id, seq)
);
CREATE INDEX chat_session_events_session_seq_idx
  ON chat_session_events (chat_session_id, seq);

ALTER TABLE chat_sessions
  ADD COLUMN last_seq BIGINT NOT NULL DEFAULT 0,
  ADD COLUMN producer_pod TEXT,            -- diagnostic: current advisory-lock holder
  ADD COLUMN status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','paused','closed'));
```

**Producer path** (the pod currently running the LLM / tool loop for a chat):

1. Acquire per-chat advisory lock: `pg_try_advisory_lock(hashtext('chat-' || chat_id))`. If not acquired, defer to the current holder (the pod returns a 409 / redirects the client; client hits any pod, which discovers the current producer from `chat_sessions.producer_pod` or simply streams from the journal).
2. Run the LLM / tool loop normally. Each produced event:
   - `INSERT INTO chat_session_events (chat_session_id, seq, event_type, payload, producer_pod) VALUES (..., $nextseq, ...)`
   - `UPDATE chat_sessions SET last_seq = $nextseq` (same tx).
   - `NOTIFY chat_session_<id>, '<seq>'`.
   - Ack the source (flush SSE to any currently-connected consumer) only **after** the PG commit -- durability before ack.
3. On loop completion, release the advisory lock and set `status = 'closed'`.
4. Event batching: up to N events or 250 ms worth, whichever first, per INSERT batch, to bound PG write rate. Token streams use bigger batches than tool calls.
5. Pod death releases the advisory lock automatically (PG server-side). Next request that tries to produce picks up the lock; the loop state is derived from `last_seq` and the journal (idempotent replay of the last un-acked tool call is the loop's responsibility).

**Consumer path** (any pod that has an SSE client connected):

1. Client connects to pod P: `GET /chat/<id>/stream?since=<client_last_seq>`.
2. P queries: `SELECT * FROM chat_session_events WHERE chat_session_id = $1 AND seq > $2 ORDER BY seq`. Stream each event to the client.
3. After replay, P opens a `LISTEN chat_session_<id>` on a shared listener connection (one per Astonish pod, multiplexed to all subscribed SSE handlers via an in-process pub/sub).
4. On each NOTIFY, P queries for events with `seq > last_delivered` and streams them.
5. Client disconnect: close the SSE; drop the in-process subscription. Journal persists.
6. If pod P dies, client reconnects (auto-retry in the SSE client) to any pod; new pod resumes from the client-supplied `since` or from `chat_sessions.last_seq` (whichever is authoritative per client protocol). No data loss; no duplicated events.

**What this replaces.** Sticky sessions on Ingress are no longer required for correctness. They may still be used for latency reasons (reduced replay volume when the same pod keeps serving the same client) but Astonish does not assume them.

**Retention.** `chat_session_events` rows are retained until the `chat_sessions.status` transitions to `'closed'`. On close, the rows are deleted by a cascading `DELETE FROM chat_session_events WHERE chat_session_id = $1` in the same transaction. No time-based retention beyond that (decision: until chat closes).

**Sandbox exec connections** (short-lived, stateless) do **not** use the journal. Any pod can call `pods/exec` against the session pod directly. Output is streamed live to the calling pod's HTTP handler and forwarded to the client. If the client disconnects, the stream is terminated; reconnect does not resume an exec.

**Producer affinity is per-chat, not per-sandbox.** One sandbox session may host multiple chat sessions (present or future). Each chat acquires its own advisory lock. The sandbox is shared, the LLM-loop ownership is per-chat.

### 5.15 Admin API surface

| Method & path | Authz | Status | Purpose |
|---|---|---|---|
| `POST /admin/base/save` | `superadmin` | Deferred | Save current session upper as the new `@base.top_layer_id` (§3.9). Body: `{session_id}`. |
| `POST /templates/{slug}/refresh` | scope admin | Deferred (`RefreshTemplate` returns "not yet implemented" — §5.6) | Rebuild template from current parent chain. |
| `PUT /users/me/default-template` | self | Deferred | Set personal default template (§5.13). |
| `PUT /teams/{team}/default-template` | team admin | Deferred | Set team default. |
| `PUT /orgs/{org}/default-template` | org owner | Deferred | Set org default. |
| `GET /admin/layers` | `superadmin` | Deferred | List all layers with ref_count, scope, size_bytes. |
| `GET /admin/layers/{id}/usage` | `superadmin` | Deferred | Which templates/sessions reference this layer. |
| `POST /admin/layers/gc` | `superadmin` | Deferred | Force-run GC reconciler (§5.12.2). Returns counts of removed layers and reclaimed bytes. |
| `GET /admin/chat-sessions/{id}/events` | `superadmin` + owner | Deferred | Operator introspection of the event journal (§5.14). |
| `POST /teams/{team}/templates` | team admin | **Shipped** | Create a team template editor session (§5.17). |
| `POST /teams/{team}/templates/{id}/save` | team admin | **Shipped** | Save the editor session as the team template (§5.17). |
| `POST /teams/{team}/templates/{id}/restore` | team admin | **Shipped** | Discard editor changes and reset to `@base` (§5.17). |
| `DELETE /teams/{team}/templates/{id}` | team admin | **Shipped** | Delete the team template; reclaims layer bytes synchronously (§5.6, §5.17). |

All endpoints are authenticated against the JWT / session middleware already in place. Scope-admin authority is derived from the caller's roles in the org/team schemas.

### 5.16 Cross-replica session-registry consistency (Status: shipped)

In a multi-replica API deployment, any pod must be able to read and update sandbox-session metadata that another pod created. The K8s backend keeps the running container in the cluster (already shared) but historically maintained the **session ↔ pod ↔ template binding** in a per-pod local JSON file. That broke as soon as Pod A created a session and Pod B tried to read it.

**Symptom (live-reproduced).** Pod A's local registry had the row; Pod B's status-poll handler returned `Gone` because no record existed locally. The chat UI saw the session disappear during a normal interaction.

**Resolution.** The team-template path now uses a pgstore-backed `SessionRegistry` that is shared across all replicas:

- `pkg/store/pgstore/sandbox_sessions.go` implements `Put` / `Get` / `Delete` / `List` against `team_<slug>.sandbox_sessions`. The schema (migration `team/002`) keys on `session_id TEXT PRIMARY KEY`, with `template_id UUID NOT NULL` and `chat_session_id TEXT NOT NULL`.
- `pkg/sandbox/backend_from_config.go::BackendFromAppConfigWithSessions(appCfg, *SessionRegistry)` constructs the K8s backend with the pgstore-backed registry injected.
- `pkg/api/sandbox_backend.go::sandboxBackendForTeamTemplate` and `buildPGSessionRegistry` are the API-layer constructors that wire this up.
- `template_id == "@base"` is rewritten to the well-known UUID at the pgstore boundary (§3.9) so the K8s backend never has to know about the schema's UUID requirement.

**Scope of the fix.** The team-template editor path is converted; the chat session and fleet handler paths are not yet on the shared registry (a small Phase E follow-up — the same pattern, applied to `pkg/api/fleet_session_handlers.go` and the chat session creation handler).

**Why pgstore (not LISTEN/NOTIFY).** Sessions are catalog state, not streaming events. A simple read-after-write store is enough; LISTEN/NOTIFY is reserved for the chat event journal (§5.14) where sub-second latency between producer and consumer matters.

### 5.17 Team-template editor lifecycle (Status: shipped)

Team admins customize team templates through a dedicated editor session. The flow is exposed through three buttons in the Studio UI (`web/src/components/TeamContainerTab.tsx` + `TeamContainerTerminal.tsx`); each maps to one shipped API endpoint.

**Editor session pod.** When a team admin opens the editor, the API creates a sandbox session with:

- `astonish.io/purpose=team-template-editor` (the only label that triggers RW mount of the layers PVC; see §5.2 / §5.3).
- A normal layer chain anchored at `@base` (or at the current team-template top layer, when one exists).
- Pod name `astn-sess-<sessionID>` like any other session.

The editor session is a regular sandbox pod in every other respect: same image, same entrypoint, same overlay, same exec/files API.

**Buttons (UI is the spec):**

| UI button | API endpoint | Semantics |
|---|---|---|
| **Create** | `POST /teams/{team}/templates` | Always creates a fresh editor session anchored at `@base`. Existing team-template top layer is **not** carried over. |
| **Save** | `POST /teams/{team}/templates/{id}/save` | Run `SaveSessionAsTemplate` on the editor session: tar-stream the upper layer, register the new layer + new top, swap `top_layer_id` on the team-template row, decrement ref_count on the previous top, reclaim layer bytes synchronously if ref_count reaches zero. |
| **Restore** | `POST /teams/{team}/templates/{id}/restore` | Discard the editor session's upper. The next chat from this template starts from `@base` again. (Note: "Restore" resets to `@base`, **not** to the previous saved top — by design, because we want a clear path to "scrap and start over.") |
| **Delete** | `DELETE /teams/{team}/templates/{id}` | Delete the team-template row, decrement ref_count, **synchronously** reclaim layer bytes via the GC pod (§5.6). The bytes are gone before the API returns. Editor sessions referencing this template are destroyed first. |

**`reclaimLayerBytes` helper** (`pkg/api/team_template_handlers.go`):

- Re-reads the layer row.
- If `ref_count == 0`: calls `Backend.DeleteTemplate` (which spawns the GC pod) and then `layers.DeleteLayer` (PG row).
- Skips `@base` and zero-byte layers.
- Idempotent: safe to call from multiple paths.

**`deleteTeamTemplateState` helper** removes the application-layer rows (template, session bindings, audit) in a single PG transaction.

**Chroot shell wrapper.** The editor terminal (`pkg/api/sandbox_terminal.go`) connects via WebSocket and execs `astonish-shell` inside the pod. `astonish-shell` is a thin wrapper baked into the base image that:

- Re-execs the trusted `astonish` binary inside the chroot (the same binary that runs as PID 1's payload).
- Bind-mounts the host-layer binary over the overlay's wrapper so chroot entry and subsequent `Backend.Exec` tool calls resolve to the same trusted build.
- Provides a stable command surface for the UI's "Open shell" button regardless of what the user has installed inside the overlay.

**UI status polling.** The UI polls `/teams/{team}/templates/{id}/status` every 1.5 s for up to 30 s after Create, displaying "Starting container..." until the editor pod reports `Ready`. This cleanly handles the 5–15 s pod-create + entrypoint-setup window without the user staring at a blank terminal.

**Failure handling.** Each endpoint is idempotent in the "destination state" sense: re-Save after a network error produces the same final layer (deduplicated via `ON CONFLICT DO NOTHING`); re-Delete on an already-deleted template returns 200/404 without leaking state. The backend never leaves the cluster + PVC + PG triple in an inconsistent state; if any step fails, the API returns the error and operators retry.

## 6. Configuration & Backend Selection

### 6.1 Helm values (the authoritative shape)

The Helm chart at `deploy/helm/astonish/values.yaml` is the source of truth for sandbox configuration. The schema below documents the keys that drive the K8s backend; non-sandbox keys are unchanged.

```yaml
# Namespace layout. One prefix drives all derived names; override
# controlPlane / sandbox explicitly only when you need a non-derived name.
namespaces:
  prefix: astonish              # → controlPlane: astonish, sandbox: astonish-sandbox
  controlPlane: ""              # "" → "{prefix}"
  sandbox: ""                   # "" → "{prefix}-sandbox"

sandbox:
  enabled: true

  # PodSecurity admission profile for the sandbox namespace.
  # baseline    : production default; FUSE plugin OR userns path.
  # privileged  : dev/lab only; required on LXC nodes.
  # restricted  : not supported.
  podSecurity: baseline

  # Backend selector. Canonical Go token written into config.
  # "k8s" (this chart) or "incus" (local dev).
  backend: k8s

  rbac:
    create: true                # creates Role + RoleBinding in sandbox ns

  image:
    repository: ghcr.io/sap/astonish-sandbox-base
    tag: dev                    # mutable tag → backend auto-uses Always
    pullPolicy: IfNotPresent    # used for runtime pods; seed Job overrides

  # Overlay strategy → one of the four privilege paths in §10.
  overlay:
    mode: fuse                  # "fuse" | "kernel" | "auto"
    privileged: false           # true on dev clusters using in-container mknod
    hostUsers: null             # null = omit; true/false = emit as-is
    runtimeClassName: ""        # e.g. "sysbox-runc"
    fuseDeviceResource: ""      # e.g. "smarter-devices/fuse"

  # RWX PVCs. StorageClass MUST provide ReadWriteMany access mode.
  # Suitable: CephFS, NFS, EFS, Azure Files, OpenStack Manila.
  # NOT suitable: Cinder, EBS, Azure Disk (these are RWO block storage).
  storage:
    storageClassName: ""        # REQUIRED — see docs/deployment/kubernetes.md
    layers:
      size: 100Gi
      accessMode: ReadWriteMany
      pvcName: astonish-layers
    uppers:
      size: 50Gi
      accessMode: ReadWriteMany
      pvcName: astonish-uppers

  # Deferred storage reconciler. Defaults shown here bound orphan/stale data
  # while keeping evicted sandboxes resumable for two weeks.
  gc:
    interval_minutes: 60
    layer_grace_hours: 24
    orphan_upper_grace_minutes: 60
    evicted_upper_retention_hours: 336
    evicted_upper_retention_disabled: false

  # Helm post-install/post-upgrade hook that seeds @base/rootfs from the
  # sandbox image. Idempotency guard skips re-seeding when @base/rootfs
  # is non-empty. See §5.6.
  seed:
    enabled: true
    backoffLimit: 6
    resources: { requests: { cpu: 100m, memory: 256Mi },
                 limits:   { cpu: "1",  memory: 1Gi } }

  # Per-session resource limits (cgroup ceiling) and scheduler reservations.
  # Limits = burst ceiling per session.
  # Requests = scheduler reservation (idle floor); zero → auto-derive
  # (5 % CPU, 12.5 % memory of limit) for high session density.
  limits:
    cpu: 2
    memory: "2GB"               # accepts "2GB" or "2Gi"
    processes: 500              # Incus-only; ignored by K8s
  requests:
    cpuMillis: 100              # 0 → auto-derive
    memoryMiB: 256

# Optional FUSE device plugin DaemonSet (smarter-device-manager
# reference). Only needed when sandbox.overlay.fuseDeviceResource is
# set and the cluster does not already ship a plugin.
fuseDevicePlugin:
  enabled: false
  namespace: kube-system
  nodeSelector: { smarter-device-manager: enabled }
  nummaxdevices: 20
```

The chart renders these into `config.yaml` (mounted as ConfigMap `astonish-config`) under `config.sandbox.*`. The Go runtime reads the rendered config; the Helm sandbox block is the single source of truth.

### 6.2 Environment variable overrides

- `ASTONISH_SANDBOX_BACKEND` — overrides `sandbox.backend`.
- `ASTONISH_K8S_NAMESPACE` — overrides the sandbox namespace.

### 6.3 Selection logic

- `pkg/launcher/studio.go` (personal mode): always instantiates `IncusBackend`, ignores `sandbox.backend`.
- `pkg/daemon/run.go` (platform mode):
  - Reads `sandbox.backend`.
  - If `k8s`: instantiate `K8sBackend` via `pkg/sandbox/backend_from_config.go::BackendFromAppConfigWithSessions`. Validates Kubernetes connectivity and PVC mount at startup; refuses to serve sandbox requests if either fails.
  - If `incus` (default): instantiate `IncusBackend`.
- Tests: instantiate `MockBackend` via injection.

### 6.4 Image-pull policy auto-detection (Status: shipped)

`pkg/sandbox/k8s/image.go::imagePullPolicy()` derives `imagePullPolicy` from the configured image reference at pod-build time:

- Mutable tags (`dev`, `latest`, `edge`, `nightly`, `master`, `main`) → `Always`. Operators iterating on the sandbox image see new bytes on every pod create.
- Immutable tags + digest pins (`@sha256:...`) → `IfNotPresent`. Production deployments pin a digest and pull once.

The Helm seed Job hard-pins `IfNotPresent` regardless of the runtime auto-detection, because the Job represents an operator-driven version transition and must not silently re-pull mid-upgrade.

When bumping a mutable tag like `:dev` in production, evict the node-level image cache (`crictl rmi <image>`) so the next pod create pulls the new bytes; otherwise containerd's GC will eventually pick it up but the timing is non-deterministic.

### 6.5 Personal mode invariants

`astonish studio` **never** uses the K8s backend and **never** touches the layer store, event journal, or template DAG. These invariants are type-system-enforced via `ErrUnsupported` returns from `filestore`:

1. **Runtime backend is always Incus.** `pkg/launcher/studio.go` hard-codes `IncusBackend`; it never reads `sandbox.backend` from config. Decision Q7. Assumptions: local Incus (Unix socket on Linux; Docker+Incus sidecar on macOS/Windows); local filesystem for registries; no Kubernetes dependency.
2. **Storage backend is always filestore.** Personal mode retains the JSON registries at `~/.local/share/astonish/sandbox/templates.json` and `sessions.json` via the existing `TemplateRegistry` and `SessionRegistry`. No PostgreSQL is required.
3. **Templates remain flat.** The filestore template store ignores `ParentTemplateID` and `TopLayerID`; when the on-disk `TemplateMeta` grows these fields they are present but always `nil`/empty. Personal mode has no notion of a template DAG.
4. **Scope degenerates to personal.** The `scope` enum value is `personal` for every template; there are no `org`/`team`/`global` templates. The default-template resolution cascade collapses to "personal default".
5. **`@base` is a platform-only concept.** Personal mode has no `@base` template and no `SaveAsBase` admin operation; the built-in bundled template fulfills the same role. The `superadmin` role and associated admin API surface (§5.15) do not exist in personal mode.
6. **Layer store returns `ErrUnsupported`.** `filestore.LayerStore` returns `store.ErrUnsupported` for every method.
7. **Event journal returns `ErrUnsupported`.** `filestore.ChatEventJournal` returns `store.ErrUnsupported` for every method.
8. **No migration path personal → platform.** Decision Q8. Personal mode is a terminal deployment shape; there is no export of templates, sessions, or layers into a PostgreSQL-backed deployment.

These invariants MUST hold for every Phase A change: any code added to `pkg/store/filestore/` under the Round 2 interfaces MUST either (a) preserve existing personal-mode behavior verbatim (for template reads via `TemplateRegistry`), or (b) return `store.ErrUnsupported` verbatim. Callers that need to differentiate between "feature disabled in personal mode" and other errors use `errors.Is(err, store.ErrUnsupported)`.

## 7. Database Schema (Phase A Prerequisite)

Two local JSON registries currently persist per host (`~/.local/share/astonish/sandbox/templates.json`, `sessions.json`). In platform mode they must move to PostgreSQL for cross-pod consistency. This is **required for any multi-pod deployment**, independent of backend choice.

Round 2 of the design adds two new tables to support the content-addressed layer store (§5.11) and the cross-pod event journal (§5.14):

- `platform.sandbox_layers` -- content-addressed layers (deployment-wide).
- `{team_schema}.chat_session_events` -- per-chat event log for multi-pod stream continuity.

### 7.1 `platform.sandbox_layers` table (platform schema)

**Scope placement.** Layers live in the `platform` schema (deployment-wide), not per-org. Rationale: `@base` is a single deployment-wide template, and dedup across orgs/teams is a core property. Per-org duplication would waste storage bytes and complicate `@base` updates. Cross-org visibility is constrained at the application layer (queries always filter by `scope` + caller's org/team/user); row-level security is a defense-in-depth overlay.

```sql
CREATE TABLE platform.sandbox_layers (
    layer_id         TEXT PRIMARY KEY,            -- sha256 of canonical tar stream
    parent_layer_id  TEXT REFERENCES platform.sandbox_layers(layer_id) ON DELETE RESTRICT,
    size_bytes       BIGINT NOT NULL,
    scope            TEXT NOT NULL CHECK (scope IN ('global','org','team','personal')),
    scope_ref_id     UUID,                         -- org_id / team_id / user_id; NULL iff scope='global'
    cephfs_path      TEXT NOT NULL,                -- mount path (e.g. /mnt/astonish-layers/<layer_id>); column name is historical — filesystem-agnostic
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_by       UUID REFERENCES platform.users(id),
    ref_count        INT NOT NULL DEFAULT 0 CHECK (ref_count >= 0)
);
CREATE INDEX ON platform.sandbox_layers (scope, scope_ref_id);
CREATE INDEX ON platform.sandbox_layers (parent_layer_id);
CREATE INDEX ON platform.sandbox_layers (ref_count) WHERE ref_count = 0;  -- GC scan

-- RLS as defense-in-depth: app-level queries already filter by scope, but
-- this prevents an application bug from leaking cross-org layer metadata.
ALTER TABLE platform.sandbox_layers ENABLE ROW LEVEL SECURITY;
-- Policy rules: global layers readable by all; org/team/personal layers
-- readable only by members of the owning scope. Details in migration.
```

### 7.2 `platform.sandbox_templates` table (platform schema)

**Scope placement.** Templates also live in the `platform` schema (was team-schema in Round 1). Rationale: templates form a DAG that crosses orgs (team templates inherit from `@base` which is global); putting them in per-org schemas would require cross-schema foreign keys which PostgreSQL handles awkwardly. Consistent with layer placement.

```sql
CREATE TABLE platform.sandbox_templates (
    id                   UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    slug                 TEXT NOT NULL,
    name                 TEXT NOT NULL,
    description          TEXT,

    scope                TEXT NOT NULL CHECK (scope IN ('global','org','team','personal')),
    scope_ref_id         UUID,                     -- NULL iff scope='global'
    parent_template_id   UUID REFERENCES platform.sandbox_templates(id) ON DELETE RESTRICT,
    top_layer_id         TEXT NOT NULL REFERENCES platform.sandbox_layers(layer_id),

    is_default_for_scope BOOLEAN NOT NULL DEFAULT FALSE,
    purpose              TEXT,                      -- NULL | 'fleet'
    version              INT NOT NULL DEFAULT 1,    -- optimistic concurrency
    build_spec           JSONB,                     -- customization recipe for RefreshTemplate

    backend              TEXT NOT NULL,             -- 'incus' | 'k8s' (layers column only used for k8s)
    binary_hash          TEXT,
    fleet_plans          TEXT[] NOT NULL DEFAULT '{}',
    created_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    refreshed_at         TIMESTAMPTZ,
    created_by           UUID REFERENCES platform.users(id),

    -- Slug uniqueness is per (scope, scope_ref_id): 'dev' in team A must not
    -- collide with 'dev' in team B, but 'base' is unique globally.
    UNIQUE (scope, COALESCE(scope_ref_id::text, ''), slug),

    -- No cycles: enforce via deferred trigger; CHECK alone cannot express.
    -- @base invariants:
    CHECK ((slug = 'base') = (scope = 'global' AND parent_template_id IS NULL)),
    -- scope/ref consistency:
    CHECK ((scope = 'global') = (scope_ref_id IS NULL))
);

-- At most one default per scope tuple. NULL scope_ref_id is fine because
-- only @base has NULL, and @base is the sole global default.
CREATE UNIQUE INDEX sandbox_templates_default_uq
  ON platform.sandbox_templates (scope, COALESCE(scope_ref_id::text, ''))
  WHERE is_default_for_scope;

CREATE INDEX ON platform.sandbox_templates (parent_template_id);
CREATE INDEX ON platform.sandbox_templates (top_layer_id);

ALTER TABLE platform.sandbox_templates ENABLE ROW LEVEL SECURITY;
```

A deferred trigger (`BEFORE INSERT OR UPDATE OF parent_template_id`) walks the ancestor chain to reject cycles.

### 7.3 `{team_schema}.sandbox_sessions` table

Sessions remain team-scoped (user-scoped tenancy).

```sql
CREATE TABLE {team_schema}.sandbox_sessions (
    session_id         TEXT PRIMARY KEY,
    backend            TEXT NOT NULL,              -- 'incus' | 'k8s'
    backend_ref        TEXT NOT NULL,              -- Incus container name | K8s pod name
    namespace          TEXT,                       -- K8s namespace (NULL for Incus)
    template_id        UUID NOT NULL REFERENCES platform.sandbox_templates(id),
    upper_layer_id     TEXT REFERENCES platform.sandbox_layers(layer_id),  -- NULL while live; set when evicted + layer-promoted
    user_id            UUID NOT NULL REFERENCES platform.users(id),
    status             TEXT NOT NULL,              -- 'creating' | 'running' | 'stopped' | 'evicted' | 'error'
    exposed_ports      JSONB NOT NULL DEFAULT '[]',
    base_domain        TEXT,
    pinned             BOOLEAN NOT NULL DEFAULT FALSE,
    upper_persisted_at TIMESTAMPTZ,                -- when evicted, marks upper tar-stream persistence time
    created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_active_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX ON {team_schema}.sandbox_sessions (user_id);
CREATE INDEX ON {team_schema}.sandbox_sessions (status);
CREATE INDEX ON {team_schema}.sandbox_sessions (template_id);
CREATE INDEX ON {team_schema}.sandbox_sessions (upper_layer_id) WHERE upper_layer_id IS NOT NULL;
```

### 7.4 `{team_schema}.chat_session_events` table

```sql
CREATE TABLE {team_schema}.chat_session_events (
    id              BIGSERIAL PRIMARY KEY,
    chat_session_id UUID NOT NULL REFERENCES {team_schema}.chat_sessions(id) ON DELETE CASCADE,
    seq             BIGINT NOT NULL,
    event_type      TEXT NOT NULL,                -- 'token'|'tool_call'|'tool_result'|'state'|'final'
    payload         JSONB NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    producer_pod    TEXT NOT NULL,                 -- diagnostic
    UNIQUE (chat_session_id, seq)
);
CREATE INDEX ON {team_schema}.chat_session_events (chat_session_id, seq);

ALTER TABLE {team_schema}.chat_sessions
    ADD COLUMN last_seq BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN producer_pod TEXT,
    ADD COLUMN status TEXT NOT NULL DEFAULT 'active'
        CHECK (status IN ('active','paused','closed'));
```

Retention: rows deleted on `chat_sessions` close (cascading via `ON DELETE CASCADE`). See §5.14.

### 7.5 Reference-count maintenance

Ref_count on `sandbox_layers` is maintained **transactionally** with template and session mutations. Enforcement is dual:

- Application code explicitly `UPDATE platform.sandbox_layers SET ref_count = ref_count + 1 WHERE layer_id = ...` in the same transaction as the template/session INSERT or UPDATE.
- Migration `platform/004_sandbox_ref_count_triggers.sql` ships a trigger backstop on `sandbox_templates` and `sandbox_sessions` that catches drift from application bugs. **Status: disabled by default in production** — installed but not active. Operators flip it on via `ALTER TRIGGER … ENABLE …` when they want forensic-grade enforcement; the application-layer ref-count writes are sufficient for steady-state correctness.

GC runs synchronously from `DeleteTemplate` (§5.6 / §5.12.1); a deferred reconciler (§5.12.2) is on the roadmap.

Migration `platform/005_seed_base_template.sql` inserts the `@base` row with the well-known UUID `a0000000-0000-4000-8000-000000000001`. The pgstore boundary normalizes `template_id == "@base"` to this UUID on write and rewrites it back on read (§3.9).

### 7.6 Store abstraction

New interface in `pkg/store`:

```go
type SandboxStateStore interface {
    // Templates (platform-scoped; implementations enforce scope filtering)
    ListTemplates(ctx context.Context, filter TemplateFilter) ([]TemplateMeta, error)
    GetTemplate(ctx context.Context, id string) (*TemplateMeta, error)
    PutTemplate(ctx context.Context, meta *TemplateMeta) error
    DeleteTemplate(ctx context.Context, id string) error
    ResolveDefaultTemplate(ctx context.Context, user User) (*TemplateMeta, error)

    // Layers
    ListLayers(ctx context.Context, filter LayerFilter) ([]LayerMeta, error)
    GetLayer(ctx context.Context, layerID string) (*LayerMeta, error)
    PutLayer(ctx context.Context, meta *LayerMeta) error   // uses ON CONFLICT DO NOTHING for dedup
    DeleteLayer(ctx context.Context, layerID string) error

    // Sessions (team-scoped)
    ListSessions(ctx context.Context, teamID string, filter SessionFilter) ([]SessionEntry, error)
    GetSession(ctx context.Context, sessionID string) (*SessionEntry, error)
    PutSession(ctx context.Context, entry *SessionEntry) error
    DeleteSession(ctx context.Context, sessionID string) error
    ReapSessions(ctx context.Context, backend SandboxBackend) error

    // Event journal (team-scoped)
    AppendEvent(ctx context.Context, evt ChatSessionEvent) error
    ReadEventsSince(ctx context.Context, chatID string, sinceSeq int64) ([]ChatSessionEvent, error)
    NotifyListeners(ctx context.Context, chatID string, seq int64) error   // LISTEN/NOTIFY or no-op
}
```

Implementations:

- `pkg/store/filestore/sandbox_state.go` -- file-backed (personal mode; existing JSON-file behavior preserved). Layers and event-journal operations return `ErrUnsupported`; personal mode uses Incus snapshots and does not have multi-pod requirements.
- `pkg/store/pgstore/sandbox_state.go` -- PG-backed (platform mode). Layer ops, event-journal ops, and default-template resolution all live here.

The existing `sandbox.TemplateRegistry` / `sandbox.SessionRegistry` types become thin wrappers over `SandboxStateStore`. Same pattern as fleets, credentials, sessions, memory (see `docs/architecture/multi-tenant-platform.md`).

## 8. Capability Parity Matrix

| Capability | Incus backend | K8s backend |
|---|---|---|
| CreateSession | `CreateInstance` | `CreatePod` (privilege path from §10) with overlay entrypoint + self-heal verification (§5.3) |
| StartSession | `UpdateInstanceState(start)` | Recreate pod + restore evicted upper via tar stream |
| StopSession | `UpdateInstanceState(stop)` | Stream upper to uppers PVC via tar+zstd + delete pod |
| **DestroySession (container + data)** | `DeleteInstance` + overlay cleanup | Delete pod + remove persisted upper |
| SessionState | `GetInstanceState` | K8s pod phase |
| Exec (non-interactive) | `ExecInstance` | `remotecommand` (SPDY) |
| Exec interactive (PTY + resize) | WebSocket + resize | SPDY + `TerminalSize` channel; chroot wrapper for editor terminals (§5.17) |
| PushFile / PullFile | Incus Files API | tar-over-exec |
| List containers | `GetInstances` by prefix | `ListPods` by labels |
| CreateTemplate | `CreateInstance` + snapshot | Template-builder pod + tar-stream to layers PVC |
| **SaveSessionAsTemplate (no registry push)** | rsync upper + Incus snapshot | sha256-addressed tar-stream of upper to `platform.sandbox_layers` (auto-dedup) |
| **DeleteTemplate** | `DeleteInstance` (tpl) | PG row delete + ref_count decrement + **synchronous GC pod** that `rm -rf`s the layer dir before returning (§5.6) |
| RefreshTemplate | rebuild + replace snapshot | Stub — returns "not yet implemented" (§5.6) |
| Org network isolation | Per-org bridge + profile | Labels + NetworkPolicy |
| ExposePort | Incus device + proxy | Service + Ingress |
| UnexposePort | remove device | delete Service |
| Tunnel to service in container | socat via exec | socat via exec (identical) |
| Fleet containers | `astn-fleet-` prefix | `astonish.io/type=fleet` label |
| Session pinning | Registry field | Registry field (pgstore-backed; §5.16) |
| Orphan pruning | registry vs Incus list | registry vs K8s list |
| Idle timeout | Prune logic | Prune logic + evict-to-uppers-PVC via tar stream (eviction concurrency: §10) |
| UI container list | Incus `ListInstances` + registry | K8s `ListPods` + registry |
| Binary hash staleness | Registry field | Registry field |
| Template inheritance chain | `based_on` chain (per-host JSON) | `parent_template_id` DAG in PG, composed as ordered lowerdirs |
| Multi-lowerdir mount depth | N/A (single-host Incus snapshot) | Up to `maxChainDepth` (default 20); flatten job beyond (deferred) |
| Layer dedup across templates/sessions | None (every template is its own tree) | Content-addressed by sha256; `ON CONFLICT DO NOTHING` on layer insert |
| Layer lifecycle | Implicit (Incus manages snapshots) | PG ref_count + synchronous GC pod (shipped) + deferred reconciler (planned) |
| Default template resolution | Per-host default | Currently `@base` direct; cascading resolver deferred (§5.13) |
| Cross-replica session catalog | N/A (single host) | pgstore-backed registry shared across API pods (§5.16, shipped for team-template path) |
| Cross-pod session attach (short exec) | N/A (single host) | Any pod → `pods/exec` on any session |
| Cross-pod chat-stream continuation | N/A | PG event journal + LISTEN/NOTIFY + per-chat advisory lock (§5.14, designed; not yet implemented) |
| Team-template editor lifecycle | N/A | Create / Save / Restore / Delete with synchronous byte reclamation (§5.17, shipped) |

No capability lost. Multiple new platform-mode capabilities gained (content dedup, DAG inheritance, cross-pod attach, event-journal replay).

## 9. Data Lifecycle Guarantees

These guarantees are identical across both backends.

### On session delete

- **Incus:** `DeleteInstance(--force)` removes container, overlay upper directory, volatile state. Registry row deleted.
- **K8s:** `DeletePod` triggers containerd overlay unmount and `emptyDir` cleanup. If previously evicted, `rm -rf /mnt/astonish-uppers/<id>/` removes the persisted `upper.tar.zst`. Registry row deleted from `team_<slug>.sandbox_sessions`.

### On template delete

- **Incus:** `DeleteInstance` on template container + remove overlay base dir.
- **K8s:** template row deleted from `platform.sandbox_templates` + ref_count decrement on `top_layer_id`. Layer's directory `/mnt/astonish-layers/<layer_id>/` is removed by the **synchronous GC pod** (§5.6) before `DeleteTemplate` returns when ref_count reaches zero. Content survives as long as anything else (another template, an evicted session's upper_layer_id) still references it.

### On org delete (cascade)

- **Incus:** destroy all containers matching org; delete org network + profile.
- **K8s:** label selector `DELETE` on all pods; remove org's NetworkPolicy; remove per-session persisted uppers.

### On idle timeout

- **Incus:** existing prune logic (stop + reap).
- **K8s:** evict (tar-stream upper to the uppers PVC + delete pod); session row status -> `evicted`. See §10 for eviction concurrency controls.

### On orphan prune

- **Incus:** compare registry vs `GetInstances` by prefix; destroy orphans.
- **K8s:** compare registry (pgstore) vs `ListPods` by labels; destroy orphans (pod delete, uppers PVC cleanup).

**Invariant:** no code path deletes an Astonish entity without also deleting its backing sandbox resource. No backend implements any "soft delete." Every `DestroySession` / `DeleteTemplate` returns only after cleanup is complete and verified.

### Preservation requirements (tar pipelines)

All tar-stream operations (eviction, resume, CreateTemplate, SaveSessionAsTemplate, bootstrap) MUST preserve:

- **Numeric UID/GID** (`--numeric-owner`) — user-namespaced pods remap UID/GID; textual name lookups across the shared PVC would not resolve correctly.
- **Extended attributes and ACLs** (`--xattrs --acls`) — required to keep `security.capability`, SELinux labels, and POSIX ACLs set by apt/dpkg or user software.
- **Symlinks** preserved literally (never dereferenced).
- **Sparse files** preserved where space saving is material (GNU tar `--sparse`; enabled on a per-profile basis if profiling shows benefit).
- **Hard links** preserved (default GNU tar behavior).

The prebuilt `astonish-sandbox-base` image MUST ship GNU tar with xattr/ACL support and `zstd`. The same requirement applies to any custom base templates operators may build.

## 10. Deployment Prerequisites

### K8s backend requires (Phase F)

1. **Kubernetes cluster** with a self-managed node pool. Managed offerings that forbid DaemonSet-based node installs (GKE Autopilot, EKS Fargate, AKS virtual nodes) are **not supported**.
2. **An overlay strategy.** Pick one of the four paths below; all are supported by the same backend binary and the same base image — the operator's only decision is which pod-security path fits the cluster.

   | # | Strategy | `sandbox.overlay.*` config | What the cluster needs | Status |
   |---|----------|----------------------------|------------------------|--------|
   | 1 | **Device plugin** (recommended production path) | `mode: fuse`, `fuseDeviceResource: smarter-devices/fuse` | FUSE device plugin DaemonSet (chart ships `fuseDevicePlugin.enabled: true`); PSA `baseline` on the sandbox namespace. | Supported |
   | 2 | **User namespace** | `mode: kernel`, `hostUsers: false` | K8s 1.33+ (beta-on) or 1.36+ (GA) with UserNamespacesSupport enabled; kernel 5.11+. PSA `baseline`. | Supported |
   | 3 | **Privileged** (current default for dev / Proxmox-LXC) | `mode: fuse`, `privileged: true` | PSA `privileged` on the sandbox namespace (set `sandbox.podSecurity: privileged` in values). No device plugin required. | Supported (current production default for the dev cluster) |
   | 4 | **Sysbox** (optional) | `mode: kernel`, `runtimeClassName: sysbox-runc` | Sysbox installed on sandbox nodes (`sysbox-deploy-k8s`). Kept for operators who already run Sysbox. | Supported |

3. **Shared RWX filesystem** reachable via a RWX StorageClass (mounted into pods through the `astonish-layers` and `astonish-uppers` PVCs).
   - Any provisioner that provides `ReadWriteMany` PVs with POSIX semantics: CephFS, NFS, EFS, Azure Files, OpenStack Manila, or equivalent.
   - Must support extended attributes, symlinks, and ACLs.
   - **Cinder / EBS / Azure Disk are not suitable.** These are block-storage services that provision RWO (ReadWriteOnce) volumes — only one node can attach at a time. The sandbox backend requires multi-node concurrent access. Use the corresponding *filesystem* service instead (Manila for OpenStack, EFS for AWS, Azure Files for AKS).
   - SAP Converged Cloud (OpenStack-based) note: no default RWX class exists; provision via **Manila** (OpenStack's shared-filesystem service — typically backed by CephFS or NFS) and set `sandbox.storage.storageClassName` in your Helm values. Do not confuse Manila with Cinder — Cinder is block storage (RWO only).
4. **PostgreSQL database** (already required by platform mode; see `docs/architecture/multi-tenant-platform.md`). LISTEN/NOTIFY and advisory locks are used by the chat event journal (§5.14, design) and any future deferred GC reconciler (§5.12.2); both are core PG features, no extensions required.
5. **Node kernel** supports overlayfs (kernel path) or FUSE (fuse path). Linux ≥ 5.15 covers both; userns + `overlay -o userxattr` needs ≥ 5.11.
   - `overlay.max_lower` ≥ `sandbox.layers.maxChainDepth` (default 20). Distro defaults (500) are comfortably above; only an issue if an operator has tuned this down.
   - **Nested LXC nodes** (Proxmox dev clusters): kernel `mount -t overlay` is blocked regardless of in-container privileges. Use path 3 (Privileged + fuse).
6. **Ingress controller** (optional, only for external port exposure from sandboxes). Ingress sticky sessions are **not** required for correctness in multi-pod Astonish (event journal §5.14 eliminates pod affinity); still permitted for latency reasons.

**Image-pull policy auto-detection.** `pkg/sandbox/k8s/image.go::imagePullPolicy()` derives `imagePullPolicy` per pod build: mutable tags (`dev`, `latest`, `edge`, `nightly`, `master`, `main`) → `Always`; digest pins and immutable tags → `IfNotPresent`. The Helm seed Job hard-pins `IfNotPresent` regardless. When iterating on a mutable tag like `:dev` in production, evict the kubelet's image cache (`crictl rmi <image>`) on each node so the next pod create pulls fresh bytes. See §6.4.

### Astonish ships

- Helm chart at `deploy/helm/astonish` with templates for the sandbox namespace, RBAC, RWX PVCs, base-layer seed Job, and an optional FUSE device plugin DaemonSet — all driven by one per-environment values file (see `deploy/helm/astonish/values-dev-proxmox.yaml` for a complete example).
- Prebuilt base sandbox image: `ghcr.io/sap/astonish-sandbox-base:<version>` (GitHub Container Registry). Phase F: image bundles `fuse-overlayfs` and the overlay-mode dispatcher in the entrypoint.
- ConfigMap/Secret templates for backend configuration.
- Pre-rendered ServiceAccount + Role bindings for the API/Worker to manage the sandbox namespace.

### Astonish does NOT ship

- Sysbox (operator installs via upstream `sysbox-deploy-k8s` **only if they want path 4**; Phase F paths 1–3 don't need it).
- A production-hardened FUSE device plugin (Astonish ships a reference manifest; operators should install the upstream smarter-device-manager Helm chart for production).
- The cluster RWX StorageClass (operator configures separately).
- The Kubernetes cluster itself (operator provides).

### Eviction concurrency and backpressure

Mass idle-eviction of many sessions concurrently can saturate the underlying RWX filesystem's metadata path (CephFS MDS / NFS server / EFS metadata) with simultaneous tar-stream writers, even though each individual writer is sequential. The eviction reconciler implements four controls:

- **Bounded worker pool.** `sandbox.k8s.maxConcurrentEvictions` (default `8`) caps the number of parallel tar streams. Excess evictions queue behind the pool.
- **Jittered scheduling.** Sessions past the idle threshold are not all dispatched in the same reconcile tick; the reconciler spreads them over the reconcile window (default 30 s) with randomized offsets to avoid thundering-herd behavior.
- **Backpressure signal.** If RWX-write-latency p99 exceeds a configurable threshold (`sandbox.k8s.fsBackpressureP99Ms`, default `500`), new evictions pause until latency recovers. Latency is measured by a lightweight probe writing a small file each reconcile tick.
- **Priority ordering.** Smaller sessions (by persisted upper size; approximated from `emptyDir` usage) are evicted first; the largest sessions drain into the uppers PVC last to prevent head-of-line blocking during storms.

These controls apply symmetrically to `SaveSessionAsTemplate` operations during load spikes, though template saves are expected to be infrequent (human-driven UX events) and normally dominate neither queue depth nor metadata-path load.

Metrics exposed in Phase E:
- `astonish_sandbox_evictions_inflight`
- `astonish_sandbox_eviction_queue_depth`
- `astonish_sandbox_layers_write_latency_p99_ms`
- `astonish_sandbox_eviction_backpressure_active` (gauge, 0/1)

## 11. Phasing & Rollout

The implementation is broken into five phases, each shippable independently.

### Phase A -- Foundation (~1.5 weeks)

Migrate the two local JSON registries to the store abstraction, **and** introduce the platform-level schema additions required by Round 2. **Required for any multi-pod deployment**, independent of backend choice.

- New interface `SandboxStateStore` in `pkg/store` (including layer ops and event-journal ops; see §7.6).
- `filestore.SandboxStateStore` preserves current JSON file behavior (personal mode unchanged; layer + journal ops return `ErrUnsupported`).
- `pgstore.SandboxStateStore` backs platform mode.
- Wrap `TemplateRegistry` and `SessionRegistry` over the interface.
- Migration SQL:
  - Create `platform.sandbox_layers` with scope enum, ref_count invariant, RLS policies (§7.1).
  - Create `platform.sandbox_templates` with scope / parent / top_layer_id / partial-unique default index / cycle-prevention trigger (§7.2).
  - Per-team-schema migration: extend `sandbox_sessions` with `upper_layer_id` + FK; add `chat_session_events` + `chat_sessions` column additions (§7.3, §7.4).
  - Backfill existing template rows (if any) into new platform-schema shape.
- Ref-count backstop triggers on `sandbox_templates` and `sandbox_sessions` (§7.5).
- No visible behavior change to end users until Phase C lands the K8s backend; Incus backend adapted to the new schema shape but continues to behave identically.

### Phase B -- Backend abstraction (~1-2 weeks)

Extract `SandboxBackend` interface and wrap existing Incus implementation.

- New `pkg/sandbox/backend.go` (interface + shared types).
- Move existing code into `pkg/sandbox/incus/` and create `IncusBackend` implementation.
- Update all callers to consume the interface (not `IncusClient` directly): `pkg/api/sandbox_handlers.go`, `pkg/agent/*`, `pkg/chat/*`, `pkg/fleet/*`, `pkg/launcher/studio.go`, `pkg/daemon/run.go`.
- Unit tests for interface contract compliance.
- Ship with `sandbox.backend: incus` as default; no deployment changes.

### Phase C -- K8s backend implementation (~5 weeks)

Build `K8sBackend` plus the layer store, GC, default-template resolver, event journal, and admin API.

- `pkg/sandbox/k8s/backend.go` -- wiring and config
- `pkg/sandbox/k8s/session.go` -- pod lifecycle (multi-lowerdir mount from layer chain), eviction/resume
- `pkg/sandbox/k8s/exec.go` -- SPDY exec, PTY with resize
- `pkg/sandbox/k8s/files.go` -- tar-over-exec push/pull
- `pkg/sandbox/k8s/template.go` -- template operations (CreateTemplate, SaveSessionAsTemplate, RefreshTemplate, DeleteTemplate), bootstrap Job, template builder pod
- `pkg/sandbox/k8s/layer_store.go` -- content-addressed layer writes, sha256 during tar stream, dedup via `ON CONFLICT DO NOTHING`, meta.json writer
- `pkg/sandbox/k8s/layer_gc.go` -- GC reconciler with advisory lock, grace-period sweep, orphan detection
- `pkg/sandbox/k8s/network.go` -- NetworkPolicy, Service, Ingress
- `pkg/sandbox/k8s/fleet.go` -- fleet container lifecycle (template-by-slug with `purpose='fleet'`)
- `pkg/sandbox/k8s/overlay_entrypoint.go` -- in-main-container entrypoint script (layer-chain mount, resume-from-upper.tar.zst)
- `pkg/chat/event_journal.go` -- producer (INSERT + NOTIFY + batching), consumer (LISTEN + replay), per-chat advisory lock leadership, pod-death recovery
- `pkg/chat/default_template.go` -- cascading default resolver (§5.13)
- `pkg/api/admin_handlers.go` -- new admin endpoints (§5.15): save-as-@base, refresh, defaults, layers, GC, event-journal introspection
- `MockBackend` in `pkg/sandbox/mock/` for tests (decision Q6), including layer store and event-journal stubs
- Unit tests + integration tests against `K3s-in-LXC` (dev) and `kind` (CI). End-to-end against real RWX storage in staging.

### Phase D -- Deployment tooling (~1-2 weeks)

Status: shipped as of branch `feature/sandbox-k8s-backend`. Delivered
artefacts:

- Helm chart at `deploy/helm/astonish` (sandbox namespace, RBAC,
  RWX PVCs, base-layer seed Job, optional FUSE device plugin)
  applied via `helm upgrade --install`. Values-file-driven; see
  `deploy/helm/astonish/values-dev-proxmox.yaml` for a complete
  dev-cluster example. In-process validation lives at
  `pkg/sandbox/k8s/manifest_deploy_test.go`; an opt-in integration
  test (`-tags integration`) shells out to `kubectl apply --dry-run=server`.
- `docker/sandbox-base/Dockerfile`: cache-friendly runtime image for
  `astonish-sandbox-base`. Host-cross-compiles the agent binary
  (`make build-linux` / `build-linux-arm64`) and generates the PID-1
  entrypoint (`make sandbox-entrypoint`); Docker only installs apt
  packages, builds the treesitter `.so`, and `COPY`s those artifacts.
  Layer order is stable→volatile (apt → treesitter → wrappers →
  entrypoint → binary) so routine code edits only rebuild the last
  COPY layers.
- `cmd/astonish-sandbox-entrypoint-script`: standalone Go helper that
  emits `pkg/sandbox/k8s.EntrypointScript` to stdout (used by
  `make sandbox-entrypoint` and CI before the image build).
- `astonish sandbox k8s-smoke` subcommand: end-to-end probe that runs
  CreateSession → Exec → PushFile → PullFile → SessionState → Stop →
  Destroy against the cluster pointed at by the operator's kubeconfig.
- `pkg/sandbox/k8s.TemplatePersister` callback on `k8s.Config`: fires
  after successful `BuildTemplate` / `SaveSessionAsTemplate` so
  callers can persist the template/session relationship into their
  application store without coupling the backend to store internals.
- Backend-agnostic tool-node pool (`pkg/sandbox/node_interfaces.go`,
  `pkg/sandbox/backend_pool.go`): `ToolNodePool` / `ToolNodeClient`
  interfaces abstract over the legacy Incus `NodeClientPool` and a
  new per-call `backendPool` that drives any `SandboxBackend` via
  `Exec` with stdin-piped NDJSON requests against the sandbox-side
  `astonish node` server. `SetupFlowSandbox` branches on
  `sandbox.backend`: the Incus path is unchanged; k8s/mock paths
  build the backend through `BackendFromAppConfig` and wrap the
  tool set with `WrapToolsWithPool`. Chat and fleet keep the
  concrete Incus pool verbatim (out of scope for this pass).
- `docker/sandbox-base/Dockerfile` installs the host-built binary at
  `/usr/local/bin/astonish-host` and a chroot wrapper at
  `/usr/local/bin/astonish`; the entrypoint bind-mounts the host-layer
  binary over the overlay's wrapper before PID-1 handoff so both the
  chroot entry and subsequent `Backend.Exec` tool calls resolve to the
  same trusted build.

Deferred to Phase E (intentionally out of scope for Phase D):

- Helm-chart integration — **done in Phase F**. The chart at
  `deploy/helm/astonish` now covers both control plane and sandbox
  (namespace, RBAC, PVCs, seed hook, FUSE device plugin), driven by
  a single per-environment values file.
- Operator-facing runbooks (`docs/deployment/kubernetes-sysbox.md`,
  `docs/operations/sandbox-backend-k8s.md`). The Phase-D smoke
  command is self-documenting for the "does it work?" question; the
  long-form docs are Phase-E polish.

### Phase E -- Hardening (~1 week)

- Observability: metrics (session count, template operations, evictions, RWX write-latency p99, backpressure-active gauge, eviction queue depth, layer GC removed / bytes reclaimed, chat event-journal INSERT rate, advisory-lock contention rate), structured logs, health checks.
- Performance tuning: RWX filesystem read caching, pre-pull base image via DaemonSet, tune tar pipeline compression (`zstd --adapt` level floor/ceiling, `-T` thread count), tune event-journal batching (events-per-insert, flush interval) for the deployment's CPU/bandwidth balance.
- Eviction-storm validation: 50 concurrent evictions on the staging cluster; measure p99 RWX write latency, confirm backpressure engages at threshold, confirm no leaked `upper.tar.zst` files after resume cycle.
- SaveSessionAsTemplate baselining: measure p50/p95 duration across varying upper-layer sizes (100 MB / 1 GB / 10 GB); confirm ≤5 s at p95 for typical workloads, document worst-case; verify dedup reuse when saving identical content twice.
- Layer-chain flatten job: implement and exercise on a synthetic 25-deep chain; verify session resumes still work across flatten.
- Pod-death recovery test: kill the producer pod mid-stream; confirm client reconnects transparently via event-journal replay with no token/tool duplication.
- End-to-end testing on a real multi-node K8s cluster with real RWX storage.
- Load testing: concurrent session creation, save-as-template throughput, event-journal throughput at realistic token rates.

### Total

~8-12 weeks of focused work for a complete Phase A--E rollout (Round 2 adds layer store, GC, event journal, default-template resolver, admin API). Phases A and B are prerequisites; Phase C is the main effort; D and E are polish.

### Phase F — Production hardening (current branch)

Status: shipped on `feature/sandbox-k8s-backend` (commits `4fa7ed6`, `cee00c8`, `406e080`, `c05af10`, `21818e3`, `60cc143`, `c256a49`, `6d0eed4`).

**Shipped:**

- Four-path overlay strategy (FUSE plugin / userns / privileged / Sysbox) selectable via `sandbox.overlay.*`. Same backend binary, same base image (§10).
- `pkg/sandbox/k8s/overlay_entrypoint.go`: PID-1 entrypoint with single-emptyDir, pre-seeded first-level dirs, `fuse-overlayfs` `squash_to_root`, kernel-FS bind, `/etc/resolv.conf` bind, chroot at `/sandbox/rootfs` (§5.3).
- Helm post-install/post-upgrade `{release}-sandbox-seed` hook with idempotency guard (§5.6).
- Synchronous `DeleteTemplate` GC pod (`astn-layer-gc-<id>`) that reclaims layer bytes before returning (§5.6 / §5.12.1).
- `pkg/sandbox/k8s/image.go::imagePullPolicy()` auto-detection (mutable tags → `Always`; immutable tags / digests → `IfNotPresent`) (§6.4).
- `CreateSession` self-heal — re-verifies pod existence before returning a cached registry entry (§5.3).
- pgstore-backed `SessionRegistry` for the team-template path; cross-replica session catalog consistency (§5.16).
- `@base` UUID normalization at the pgstore boundary (`a0000000-0000-4000-8000-000000000001`) (§3.9, §7).
- Migration `platform/005_seed_base_template.sql` (seeds `@base` row).
- Migration `platform/004_sandbox_ref_count_triggers.sql` (installed disabled-by-default).
- Team-template editor lifecycle: Create / Save / Restore / Delete with synchronous reclamation, `astonish-shell` chroot wrapper, 1.5 s status polling up to 30 s (§5.17).
- Single Helm chart at `deploy/helm/astonish` covering both control plane and sandbox; configurable hyphen-only DNS-1123 namespace prefix (§6.1).
- `Makefile` fast targets: `push-dev-fast`, `push-sandbox-base-dev-fast`, `push-incus-dev-fast`, `push-all-dev-fast` (auto-detect `DEV_ARCH`).
- `apt` cosmetic-warning fix baked into the base image's `/etc/apt/apt.conf.d/00no-sandbox`; carried into `@base` deterministically by the seed Job.

**Still open (Phase F+ / E):**

- Cross-replica consistency for the **fleet** session path (`pkg/api/fleet_session_handlers.go:341-357`) and the chat-session creation handler. The pattern from §5.16 applies; small follow-up.
- Admin dry-run/preview endpoint for K8s uppers GC (the background reconciler now handles orphan and stale evicted upper cleanup; operators still need a friendly preview surface).
- Cascading default-template resolution (§5.13 — currently resolves directly to `@base`).
- `RefreshTemplate` implementation (§5.6 — currently returns "not yet implemented").
- Force-run K8s GC admin endpoint (§5.12.2) — the reconciler runs on a schedule today; an on-demand operator trigger remains useful.
- Layer-chain flatten job (§5.11).
- `meta.json` per-layer sidecar (§5.11).
- Default-template setter API endpoints (§5.15 — `PUT /users/me/default-template` etc.).
- `chat_session_events` event journal (§5.14) — designed; producer + consumer plumbing not yet implemented.
- Automatic cascade of template refreshes when a parent changes — explicitly **not** implemented; design calls for explicit `RefreshTemplate` invocation only (see §5.6 and §13).
- Broader cloud-clean state sweep: any remaining file-based per-pod state in API replicas (Phases 1.A / 2 / 3 of the cloud-clean audit).

## 12. Testing Strategy

### Unit

- Interface compliance tests: every backend implementation must pass the same `BackendContract` test suite (`pkg/sandbox/backend_contract_test.go`).
- `MockBackend` enables high-coverage testing of callers without any infrastructure.

### Integration

- **Incus backend:** existing tests in `pkg/sandbox/*_test.go` continue to run.
- **K8s backend -- canonical dev environment:** a 3-node K3s cluster running inside Incus LXC containers on the developer's machine. This reuses the existing Incus workflow and iterates far faster than `kind` or `minikube` for Astonish contributors. Install `sysbox-deploy-k8s` as usual. For initial iteration, a single-node NFS server (or `nfs-subdir-external-provisioner`) provides RWX PVs; Rook/CephFS on LXC is non-trivial and deferred to pre-merge validation. CI uses `kind` with the same RWX-PV shortcut (no CephFS). End-to-end on real RWX storage happens in the staging cluster (see End-to-end below). Tests exercise:
  - session lifecycle (create / exec / files / destroy)
  - template create / save-as-template / delete
  - idle eviction + resume (tar-stream round-trip)
  - org network isolation (pod-to-pod traffic honoring NetworkPolicy)
  - fleet container lifecycle
  - orphan pruning
  - eviction backpressure (simulated latency spike)

### End-to-end

- Real K8s cluster with real RWX storage (CephFS, NFS, or Manila). Run the chat scenario suite (`docs/architecture/testing-chat-scenarios.md`) against both backends. Capability parity is validated by running the **same** scenarios against both.

### Load

- 100 concurrent sessions created / destroyed in a tight loop. Verify:
  - No leaked pods, PVCs, Services.
  - RWX PVC disk usage returns to baseline after destroy.
  - Session registry rows deleted.

## 13. Non-Goals

Explicitly out of scope for this design:

- **Replacing Incus in personal mode.** Personal mode remains Incus-only. Decision Q7.
- **Running Incus inside Kubernetes pods.** The K8s backend does not use Incus at all; it uses Kubernetes pods directly. The previously-explored "Incus cluster in K8s" approach is abandoned.
- **Live migration of sandbox pods across nodes.** Sandboxes are stateful but ephemeral; if a node dies, its running sandboxes are lost (user retries). This matches the Incus-single-host behavior today.
- **Persistent per-session PVCs by default.** Sessions use `emptyDir` for the upper layer. Persistent PVCs may be added later as an opt-in per-session feature, but v1 does not require it.
- **Save-as-template via OCI registry push.** Templates are written directly to the RWX layers PVC (decision: no registry push for saves). Registry-based template distribution is explicitly rejected as a save path because it is too slow for the interactive UX.
- **Multi-cluster or multi-region federation.** One cluster, one sandbox tier. Future work may address this.
- **Migration from prior Incus-based K8s deployments.** Decision Q8: this is the first K8s-native implementation; no legacy migration required.
- **Support for fully-managed Kubernetes** (GKE Autopilot, EKS Fargate). These platforms forbid the DaemonSet-based device plugin and the pod-level RWX mounts and are incompatible with this architecture.
- **gVisor, Kata Containers, Firecracker, KubeVirt.** Considered and rejected in favor of Sysbox (see §1).
- **Namespace-per-org isolation.** Deferred; v1 uses single namespace + labels (decision Q2b). May be added later without changing the interface contract.
- **Ingress sticky-session requirement.** Earlier design drafts required Ingress affinity for SSE/ChatRunner continuity. The PG event journal (§5.14) eliminates this as a correctness requirement; stickiness may still be enabled for latency optimization but is not part of the architecture contract.
- **Automatic cascade of template refreshes.** When `@base` (or any parent template) is updated, descendants are **not** automatically rebuilt. Team/fleet templates remain pinned to the parent layer they were built against. A team admin explicitly invokes `RefreshTemplate` to pull in parent changes. Rationale: safety (an admin edit to `@base` cannot silently disrupt every team) and predictability (rebuilds are never implicit).

## 13.5. Rejected Alternatives

A ledger of options considered and declined, with reasoning, so future readers understand why the current shape was chosen.

| Alternative | Why rejected |
|---|---|
| **rsync for template save / idle-eviction** | rsync's cost on RWX filesystems is dominated by per-file metadata RPCs (`readdir`, `stat`, `setattr`, open/close). For typical sandbox contents (node_modules, venvs), that is 100k+ metadata round-trips. A single `tar \| zstd` stream is one sequential writer against the PVC, empirically 3-10× faster for cold copies. Delta rsync could beat tar on *iterative* re-saves of the same template, but template saves are infrequent human actions and the 1-5 s budget is already met. |
| **Separate init container with `mountPropagation: Bidirectional`** | Requires the mount to live on a host-visible path; either `hostPath` (violates PSA `baseline`/`restricted`) or a CSI volume supporting bidirectional propagation (not universally available). Entrypoint-in-main-container avoids propagation entirely and works with any CSI. |
| **Kata Containers** | Nested virtualization requirement (KVM on nodes). Hypervisor-per-sandbox CPU/memory overhead. Excessive isolation for the semi-trusted workloads Astonish runs. |
| **gVisor** | Breaks mount operations, systemd, and nested Docker -- all capabilities Astonish flows rely on. Not viable as a default runtime for Astonish sandboxes. |
| **Privileged pods (`privileged: true`)** | Violates Pod Security Admission `baseline` and `restricted` policies; unacceptable in enterprise K8s deployments. Sysbox provides the same functional capabilities via user-namespace remapping without requiring privileged mode. |
| **Incus cluster inside Kubernetes pods** | No production-ready Helm chart or operator for running Incus clustered inside K8s. Operational burden of nested daemons (Incus + K8s scheduling conflicts, storage driver layering). Incus REST API is host-oriented, not pod-oriented. Investigated and abandoned in favor of native K8s pods. |
| **External Incus VMs managed by Astonish** | Reintroduces a non-K8s tier operators must manage separately, contradicting the "no external infrastructure outside K8s" constraint. |
| **OCI image registry push for SaveSessionAsTemplate** | Build → push → pull round-trip takes tens of seconds for non-trivial session contents; breaks the 1-5 s interactive UX budget. Direct tar-stream to the shared RWX PVC meets the budget without a registry. |
| **Per-session Persistent Volumes (PVCs)** by default | Unnecessary for the ephemeral upper-layer model. PVC provisioning adds seconds to session creation and creates cleanup obligations. `emptyDir` on node-local disk is fast and cleaned up automatically. PVCs may be offered as an opt-in future feature for workloads requiring persistent scratch space. |
| **Fully-managed Kubernetes (GKE Autopilot / EKS Fargate / AKS virtual nodes)** | These platforms prohibit DaemonSet-based node installs, preventing Sysbox deployment, and in most cases forbid node-mounted shared filesystems. Incompatible with the required runtime and storage model. |
| **Namespace-per-org in v1** | Correct long-term isolation model but not required for security parity (labels + NetworkPolicy suffice). Adds operational complexity to the first cut. Deferred; interface contract is compatible with future migration. |
| **Per-org duplicated `@base` layers** | Each org could clone `@base` on creation for strict per-org isolation. Rejected: storage overhead scales linearly with org count; superadmin `@base` updates would require propagating to every org's copy; dedup benefits across orgs vanish. Shared `@base` in `platform.sandbox_layers` with RLS as defense-in-depth is simpler and strictly better on storage economics. Cross-org sharing is limited to `scope='global'` layers only; org/team/personal layers remain strictly scoped. |
| **Per-sandbox-session advisory lock for chat producer affinity** | Broader than needed: multiple chats on the same sandbox would serialize through a single lock even though they are logically independent streams. Per-chat advisory lock (hashtext('chat-' \|\| chat_id)) gives the correct granularity. |
| **Automatic cascade of template refreshes when a parent changes** | Considered for convenience, rejected for safety: a single `@base` edit could simultaneously rebuild dozens of team/fleet templates, potentially breaking carefully-tuned customizations. Explicit `RefreshTemplate` invocation by a scope-appropriate admin keeps the propagation model predictable. Layer ref-counting ensures the old parent layer stays alive as long as any descendant references it. |

## 14. Open Items for Implementation Time

Minor decisions that do not affect the overall design and will be resolved during implementation:

- Exact Go package layout under `pkg/sandbox/k8s/` (may split or merge modules based on file size).
- Exact `zstd` tuning for tar pipelines: compression-level floor/ceiling for `--adapt`, thread count (`-T0` vs bounded), whether to enable `--sparse` by default. Decide during Phase E profiling against realistic upper-layer sizes.
- Metric names for Prometheus exposition (draft names in §10 are indicative; final names follow Astonish conventions).
- How Astonish surfaces K8s events to the UI (real-time feed vs pull-on-demand).

### Warm pool (future work, forward-compatibility note)

If median session-creation latency on the K8s backend becomes a UX complaint, a pre-warmed pod pool can be introduced **without** changing the `SandboxBackend` interface: `CreateSession` may, at the K8s backend's discretion, acquire a pre-created pod from a pool and rebind its labels/annotations rather than create-from-scratch. Callers remain unaware.

Current expectation: Astonish sessions are minutes-to-hours scale (human chat flows), so just-in-time pod creation is adequate. The interface is deliberately written so pool sizing, pre-warm lifecycle, idle timeouts on pool members, and leak detection can be added later as an internal implementation concern of `K8sBackend`. No other package or API needs to change.

Triggers that would motivate adding the warm pool:
- p50 `CreateSession` latency > 3 s sustained.
- Workload shift toward short-lived sessions (seconds to low-minutes), producing churn that stresses CNI IP allocation or kubelet pod-creation throughput.
- Cost/performance trade-off in managed environments where pod startup is slower than bare-metal K3s.

## 15. References

- `docs/architecture/sandbox.md` -- current Incus-based implementation.
- `docs/architecture/multi-tenant-platform.md` -- store abstraction pattern, team schemas, RLS.
- `docs/architecture/testing-chat-scenarios.md` -- test infrastructure conventions.
- Sysbox project: https://github.com/nestybox/sysbox
- Sysbox on Kubernetes: `sysbox-deploy-k8s`
- Kubernetes exec API: https://kubernetes.io/docs/tasks/debug/debug-application/get-shell-running-container/
- CephFS CSI driver: https://github.com/ceph/ceph-csi
- OpenStack Manila CSI driver: https://github.com/kubernetes/cloud-provider-openstack/tree/master/pkg/csi/manila
- Rook: https://rook.io
