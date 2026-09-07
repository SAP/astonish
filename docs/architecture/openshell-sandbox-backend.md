# OpenShell Sandbox Backend

> **Status: Deployed and operational.**
> NVIDIA OpenShell is integrated as a Helm subchart and deployed in the
> same namespace as Astonish. Istio manages all service-to-service
> authentication (mTLS via SPIFFE), eliminating custom cert management.

---

## 1. Context & Motivation

The K8s backend (`pkg/sandbox/k8s/`) creates and manages sandbox pods
directly via the Kubernetes API. This works well for single-tenant
clusters but has limitations in regulated or multi-tenant environments:

- **No in-pod process isolation.** Once code executes inside the pod, it
  has full access to the overlay filesystem, can make arbitrary syscalls,
  and has unrestricted network egress within the pod's NetworkPolicy.
- **No per-process audit trail.** Kubernetes audit logs record pod/exec
  events but not what commands run or what files they access.
- **No inference routing control.** Agent tool calls that reach external
  LLM APIs are not mediated by any privacy or compliance layer.
- **No credential isolation.** Environment variables visible to PID 1 are
  visible to all exec'd processes.

NVIDIA OpenShell (https://github.com/NVIDIA/OpenShell) provides:

| Layer | Mechanism | What It Protects |
|-------|-----------|-----------------|
| **Filesystem** | Landlock LSM | Per-process read/write/exec access to paths |
| **Syscalls** | seccomp BPF | Blocks mount, mknod, pivot_root, ptrace for agent code |
| **Network** | Network namespace + policy proxy | Per-destination, per-binary, L7-aware egress control |
| **Audit** | OCSF structured logging | Every process spawn, file access denial, network connection |

---

## 2. Architecture Overview

OpenShell is deployed as a **Helm subchart** in the same namespace as
Astonish. Istio provides mesh-level mTLS between the Astonish control
plane and the OpenShell gateway. Sandbox pods run in a separate namespace
without Istio sidecars — they connect to the gateway via plaintext,
protected by NetworkPolicy.

```mermaid
graph TB
    subgraph "Namespace: astonishdev<br/>(istio-injection: enabled)"
        API["Astonish API Pod<br/>+ Envoy Sidecar"]
        Worker["Astonish Worker Pod<br/>+ Envoy Sidecar"]
        GW["OpenShell Gateway<br/>StatefulSet + Envoy Sidecar<br/>:8080 gRPC / :9090 metrics"]
    end

    subgraph "Namespace: astonishdev-sandbox<br/>(istio-injection: disabled, PSA: privileged)"
        SB1["Sandbox Pod 1<br/>Supervisor PID 1<br/>+ astonish node"]
        SB2["Sandbox Pod 2<br/>Supervisor PID 1<br/>+ astonish node"]
    end

    API -->|"gRPC over Istio mTLS<br/>(SPIFFE identity)"| GW
    Worker -->|"gRPC over Istio mTLS<br/>(SPIFFE identity)"| GW
    GW -->|"Agent Sandbox CRD<br/>(creates/manages pods)"| SB1
    GW -->|"Agent Sandbox CRD<br/>(creates/manages pods)"| SB2
    SB1 -->|"plaintext :8080<br/>(NetworkPolicy restricted)"| GW
    SB2 -->|"plaintext :8080<br/>(NetworkPolicy restricted)"| GW
```

### Component Responsibilities

#### NVIDIA Provides (Helm Subchart)

| Component | Image / Artifact | Purpose |
|-----------|-----------------|---------|
| **Gateway** | `ghcr.io/nvidia/openshell/gateway:0.0.86` | Sandbox lifecycle, exec relay, policy distribution |
| **Supervisor** | `ghcr.io/nvidia/openshell/supervisor:0.0.86` | PID 1 inside sandboxes, Landlock/seccomp enforcement |
| **Helm Chart** | `oci://ghcr.io/nvidia/openshell/helm-chart:0.0.86` | Deploys gateway + RBAC (subchart of Astonish); ≥0.0.81 required for cert-bundle PVC mounts via `driver_config` |

#### Astonish Provides

| Component | Location | Purpose |
|-----------|----------|---------|
| **OpenShell Backend** | `pkg/sandbox/openshell/` | Implements `SandboxBackend` interface via gRPC client |
| **Custom Sandbox Image** | `docker/sandbox-openshell/Dockerfile` | NVIDIA base + Astonish agent binary |
| **Istio Resources** | `templates/openshell/istio.yaml` | PeerAuth + AuthorizationPolicies |
| **NetworkPolicy** | `templates/openshell/networkpolicy.yaml` | Sandbox egress restriction |
| **Namespace Config** | `templates/sandbox/namespace.yaml` | PSA auto-promotion, Istio injection disabled |

---

## 3. Sandbox Spin-up Protocol

When a user sends a chat message that triggers tool execution, the system
provisions a sandbox through the following sequence:

```mermaid
sequenceDiagram
    participant User
    participant Studio as Astonish Studio
    participant API as Astonish API/Worker
    participant Pool as BackendPool
    participant Client as GRPCClient
    participant GW as OpenShell Gateway
    participant CRD as Agent Sandbox Controller
    participant Pod as Sandbox Pod
    participant SV as Supervisor (PID 1)

    User->>Studio: Send chat message
    Studio->>API: POST /api/chat (SSE)
    API->>Pool: Get sandbox for session

    Note over Pool: Check if session already has a sandbox

    alt No sandbox exists
        Pool->>Client: CreateSandbox(image, env, labels, policy)
        Client->>GW: gRPC CreateSandbox(SandboxSpec + SandboxPolicy)
        GW->>CRD: Create AgentSandbox CR
        CRD->>Pod: Create Pod in sandbox namespace
        Note over Pod: Pod starts with supervisor injected<br/>(init-container copies openshell-sandbox binary)
        Pod->>SV: Supervisor starts as PID 1
        SV->>GW: IssueSandboxToken (K8s SA token → gateway JWT)
        GW-->>SV: JWT token (signed with gateway signing key)
        SV->>GW: GetSandboxConfig (fetch assigned policy)
        GW-->>SV: SandboxPolicy (Landlock rules, compatibility mode)
        SV->>SV: Apply Landlock LSM filesystem policy
        SV->>SV: Apply seccomp BPF syscall filter
        SV->>GW: ConnectSupervisor (bidirectional gRPC stream)
        Note over SV: Sandbox state → READY
        GW-->>Client: CreateSandbox response (sandbox_id, pod_name)
        Client->>Client: WaitForSessionReady<br/>(poll GetSandbox until state=Ready)
        Pool->>Pool: Store ContainerName=sandbox_name<br/>Store PodName=gateway_pod_uuid
    end

    Note over Pool: Sandbox ready — execute tool

    Pool->>Client: ExecSandbox(pod_uuid, cmd, env)
    Client->>GW: gRPC ExecSandbox
    GW->>SV: Relay exec via ConnectSupervisor stream
    SV->>SV: Spawn child process with:<br/>- Landlock FS restrictions<br/>- seccomp syscall filter<br/>- User: sandbox<br/>- WorkDir: /sandbox
    SV-->>GW: stdout/stderr/exit_code
    GW-->>Client: ExecSandbox response
    Client-->>Pool: ExecResult
    Pool-->>API: Tool output
    API-->>Studio: SSE event (tool result)
    Studio-->>User: Display result
```

### Key Protocol Details

**AlreadyExists Handling:** If `CreateSandbox` returns `ALREADY_EXISTS`
(gRPC code 6), the client adopts the existing sandbox — it calls
`GetSandbox` to retrieve the current state and proceeds as if it had
just created it. This handles restarts and race conditions gracefully.

**WaitForSessionReady:** After `CreateSandbox` returns, the client polls
`GetSandbox` until the sandbox reaches `Ready` state. The supervisor must
complete token exchange, policy fetch, and Landlock setup before the
gateway marks it ready. Typical time: 5-11 seconds.

**Dual-Field Routing:** The session store maintains two identifiers:
- `ContainerName` = sandbox name (used for `GetSandbox`, `DeleteSandbox`)
- `PodName` = gateway's internal UUID (used for `ExecSandbox`)

This is because the gateway uses different identifiers for lifecycle
operations (human-readable name) vs exec routing (internal UUID that
maps to the supervisor's relay stream).

### Workspace Storage (Ephemeral)

The OpenShell Gateway K8s driver attaches a workspace PVC (`/sandbox`)
to each sandbox pod by default — a 2Gi `ReadWriteOnce` PVC that adds
significant latency (~35s on Cinder/OpenStack StorageClasses) to pod
startup.

**Astonish eliminates this** via a Kyverno ClusterPolicy that mutates
sandbox **Pods** at admission time (after the Sandbox controller creates
them):

1. Intercepts Pods in the sandbox namespace that have a `workspace`
   volume backed by `persistentVolumeClaim`
2. Replaces that volume entry with `emptyDir: { sizeLimit: "2Gi" }`

The Sandbox controller still creates the PVC from `volumeClaimTemplates`
(because mutating the CRD's VCTs causes the controller to skip the
workspace volume injection entirely, breaking the pod). However, since
the pod no longer references the PVC, it starts immediately without
waiting for provisioning. The orphaned PVC has an ownerReference to
the Sandbox and is cascade-deleted on Sandbox destruction — no cleanup
needed.

The `/sandbox` directory is fast, ephemeral, and lost on pod restart.

**Why not a gRPC-level fix?** The `volume_claim_templates` proto field
is `google.protobuf.Struct` (serializes as JSON Object), but the Sandbox
CRD's `spec.volumeClaimTemplates` expects a JSON Array. The gateway
passes the Struct directly to the CRD, causing schema validation errors.
The `driver_config` field has no volume-related options. The
`workspace_default_storage_size = "0"` config suppresses PVC creation
but not the volume mount reference (pod stuck Pending). No gRPC-level
mechanism works in OpenShell v0.0.63.

**Configuration:**

```yaml
# values.yaml
sandbox:
  openshell:
    ephemeralWorkspace: true  # Requires Kyverno installed
```

**Prerequisite:** Kyverno 1.12+ must be installed in the cluster.
See the [OpenShell Deployment Guide](../website/docs/deployment/openshell.md#4-install-kyverno-recommended-for-production)
for detailed installation instructions, version requirements, HA configuration,
and troubleshooting.

**Policy location:** `templates/openshell/kyverno-policy.yaml`

**Disposable:** Remove once OpenShell fixes workspace PVC injection
upstream (issues #967/#971).

---

## 4. Agent Tool Execution

Once a sandbox is ready, the `astonish node` process runs as a
long-lived NDJSON-over-stdio tool server inside the sandbox. All
tool calls from the agent are proxied through this process:

```mermaid
sequenceDiagram
    participant LLM as LLM Provider
    participant Engine as Agent Engine
    participant Pool as NodeClientPool
    participant Client as GRPCClient
    participant GW as OpenShell Gateway
    participant SV as Supervisor
    participant Node as astonish node<br/>(in sandbox)

    Note over Engine,Node: First tool call starts astonish node

    Engine->>Pool: Call tool (shell_command "git status")
    Pool->>Client: ExecSandbox(pod_uuid, ["astonish","node"], env)
    Client->>GW: gRPC ExecSandbox (streaming)
    GW->>SV: Relay to supervisor
    SV->>Node: Spawn "astonish node"<br/>(Landlock + seccomp enforced)
    Node-->>Pool: {"ready": true}

    Note over Engine,Node: Subsequent tool calls reuse the process

    Engine->>Pool: shell_command "git status"
    Pool->>Node: {"id":"1","tool":"shell_command","args":{"command":"git status"}}
    Node->>Node: Execute in /sandbox<br/>(Landlock: /usr/bin/git is read-only ✓)
    Node-->>Pool: {"id":"1","result":{"stdout":"On branch main...","exit_code":0}}
    Pool-->>Engine: ExecResult
    Engine->>LLM: Tool result

    LLM-->>Engine: Next action: write_file
    Engine->>Pool: write_file "/sandbox/src/main.go"
    Pool->>Node: {"id":"2","tool":"write_file","args":{"path":"/sandbox/src/main.go",...}}
    Node->>Node: Write file<br/>(Landlock: /sandbox is read-write ✓)
    Node-->>Pool: {"id":"2","result":{"success":true}}

    Engine->>Pool: write_file "/etc/passwd"
    Pool->>Node: {"id":"3","tool":"write_file","args":{"path":"/etc/passwd",...}}
    Node->>Node: DENIED by Landlock<br/>(/etc is read-only)
    Node-->>Pool: {"id":"3","error":"permission denied"}
    Pool-->>Engine: Error result
```

### How Landlock Enforcement Works

The supervisor applies Landlock rules **before** spawning `astonish node`.
Every child process inherits these restrictions at the kernel level — the
`astonish node` process cannot bypass them regardless of what the agent
tries to execute. This is defense-in-depth beyond the container boundary:

- Even if the agent finds a container escape (e.g., via a kernel bug),
  the Landlock policy limits what the escaped process can access.
- The agent cannot `chmod` or `chown` its way past Landlock — it's a
  kernel-level mandatory access control.
- `best_effort` mode means if the kernel lacks Landlock support, the
  supervisor still starts (graceful degradation) but logs a warning.

---

## 5. Landlock Filesystem Policy

The filesystem policy is defined in `pkg/sandbox/openshell/policy.go`
and passed to the gateway at sandbox creation time:

```go
func defaultSandboxPolicy(cfg config.SandboxOpenShellConfig) *SandboxPolicySpec {
    return &SandboxPolicySpec{
        Version: 1,
        Landlock: &LandlockSpec{
            Compatibility: cfg.LandlockCompatibility, // default: "best_effort"
        },
        Filesystem: &FilesystemSpec{
            IncludeWorkdir: true,
            ReadOnly: []string{
                "/usr", "/bin", "/sbin", "/lib", "/lib64",
                "/etc", "/opt",
                "/dev/urandom",
            },
            ReadWrite: []string{
                "/sandbox", "/tmp", "/var/tmp", "/home", "/run",
                "/dev/null",           // Must be read-write for shell redirections
                "/dev/pts",            // PTY devices directory (kernel 6.10+ ABI v5)
            },
        },
    }
}
```

### PTY Device Access (Kernel 6.10+ / Landlock ABI v5)

Linux kernel 6.10 introduced Landlock ABI v5 which adds the
`LANDLOCK_ACCESS_FS_IOCTL_DEV` restriction — blocking `ioctl` operations
on device files unless explicitly permitted. The `shell_command` tool
requires PTY allocation via `/dev/ptmx` for interactive terminal support
(password prompts, interactive CLIs, etc.).

The supervisor pre-opens `PathFd`s for all paths in the filesystem policy
**before** calling `landlock_restrict_self()`. Including `/dev/pts` in
`ReadWrite` ensures the agent can allocate PTYs on any kernel version
(the real PTY master device `/dev/pts/ptmx` is accessible via this directory).

> **Note:** `/dev/ptmx` is typically a symlink to `/dev/pts/ptmx`. It cannot
> be listed directly because the supervisor refuses to `chown` symlinks
> (potential privilege escalation). We list `/dev/pts` instead.

> **Supervisor version requirement:** OpenShell supervisor ≥ 0.0.70 is
> recommended. Earlier versions may have issues with device path handling
> in `prepare_filesystem()` (see OpenShell Issue #749).

### Path Access Matrix

| Path | Access | Purpose |
|------|--------|---------|
| `/sandbox` | Read-Write | Agent workspace (WORKDIR, `IncludeWorkdir: true`) |
| `/tmp`, `/var/tmp` | Read-Write | Temporary files for tool execution |
| `/home` | Read-Write | User home directories |
| `/run` | Read-Write | Runtime state (sockets, PIDs) |
| `/dev/pts` | Read-Write | PTY devices directory (includes ptmx master device) |
| `/usr`, `/bin`, `/sbin` | Read-Only | System executables (git, node, python) |
| `/lib`, `/lib64` | Read-Only | Shared libraries |
| `/etc` | Read-Only | System configuration (resolv.conf, etc.) |
| `/opt` | Read-Only | Optional packages |
| `/dev/null` | Read-Write | Null device (must be writable for shell redirections) |
| `/dev/urandom` | Read-Only | Random number generator |
| `/root` | **Denied** | Not in policy — agent runs as user `sandbox` |
| `/proc`, `/sys` | **Not in Landlock** | Handled by container + seccomp |

**`IncludeWorkdir: true`** tells the supervisor to automatically add the
container's working directory (`/sandbox`) to the read-write list,
ensuring the agent always has write access to its workspace.

**`best_effort` compatibility** means Landlock degrades gracefully:
- If the kernel supports Landlock v5 → full enforcement (including IOCTL_DEV)
- If the kernel supports Landlock v1-v4 → partial enforcement (missing
  newer restrictions silently skipped)
- If the kernel has no Landlock → no enforcement, supervisor logs warning

### Configurable Policy Extensions

Operators can extend the default filesystem policy without code changes:

```yaml
# values.yaml
sandbox:
  openshell:
    landlockCompatibility: "best_effort"  # or "hard_requirement"
    filesystemPolicy:
      extraReadOnly:
        - /data/shared-models
      extraReadWrite:
        - /mnt/scratch
```

---

## 6. Service Mesh Integration

### Overview

The OpenShell gateway is the bridge between the service mesh (Astonish
control plane) and non-mesh workloads (sandbox pods). It must accept
connections from both:

- **Mesh pods** (Astonish API/Worker) — arrive with mesh-provided mTLS
- **Non-mesh pods** (sandbox supervisors) — arrive as plaintext

The chart supports pluggable mesh configuration via `mesh.provider`:

```yaml
# values.yaml
mesh:
  provider: ""       # "" | "istio" (more meshes can be added)
```

| `mesh.provider` | What the chart renders |
|-----------------|----------------------|
| `""` (default) | No mesh resources. Operator manages mesh config manually or operates without a mesh. |
| `"istio"` | PeerAuthentication (PERMISSIVE on gateway) + AuthorizationPolicies + namespace labels (`istio-injection: disabled` on sandbox ns) |

For meshes not directly supported (e.g., Linkerd, Cilium), set
`mesh.provider: ""` and configure mesh resources manually. The key
requirement: **the gateway pod must accept both mTLS and plaintext on
port 8080**.

### Istio Configuration (mesh.provider: "istio")

When `mesh.provider: "istio"`, the gateway listens plaintext
(`openshell.server.disableTls: true`) and Istio provides all encryption:

```mermaid
graph LR
    subgraph "Istio Mesh (mTLS everywhere)"
        A["Astonish Pod<br/>+ Envoy Sidecar"] -->|"mTLS<br/>(auto SPIFFE)"| E["Envoy Sidecar<br/>(gateway pod)"]
        E --> G["OpenShell Gateway<br/>(:8080 plaintext)"]
    end

    subgraph "Outside Mesh"
        S["Sandbox Pod<br/>(no sidecar)"] -->|"plaintext<br/>(NetworkPolicy restricted)"| E
    end
```

**Rendered resources** (`templates/openshell/mesh-istio.yaml`):

**PeerAuthentication:**
- Mode: `PERMISSIVE` on the gateway pod only
- Allows the gateway's Envoy sidecar to accept both:
  - Istio mTLS from mesh pods (Astonish API/Worker)
  - Plaintext from non-mesh pods (sandbox supervisors)
- Rest of the namespace inherits mesh-wide STRICT mode

**AuthorizationPolicy — mesh peers** (`openshell-mesh-allow`):
- Selector: OpenShell gateway pods
- Rule: `principals: ["*"]` (any valid SPIFFE identity)
- Effect: Mesh pods get full unrestricted gRPC access

**AuthorizationPolicy — sandbox pods** (`openshell-sandbox-allow`):
- Selector: OpenShell gateway pods
- Rule: No `from` section (matches all traffic including unauthenticated)
- Restricted `to.operation.paths`:
  - `ConnectSupervisor`, `RelayStream`, `GetSandboxConfig`
  - `IssueSandboxToken`, `RefreshSandboxToken`, `RotateCredential`
  - `ReportPolicyStatus`, `GetSandboxProviderEnvironment`, `PushSandboxLogs`
  - `AcquireLease`, `RenewLease`, `Health`
- Effect: Non-mesh pods can only call supervisor callback methods

**Union model:** Istio ALLOW policies are additive. Mesh pods match both
policies → full access. Non-mesh pods match only the sandbox policy →
restricted to supervisor methods. A sandbox pod cannot call
`CreateSandbox` or `DeleteSandbox` on the gateway.

### Without a Mesh (mesh.provider: "")

When no mesh is configured:
- No mesh CRDs are rendered (no Istio/Linkerd dependency)
- Set `openshell.server.disableTls: false` to enable the gateway's
  built-in mTLS (the pkiInitJob generates certificates automatically)
- Configure Astonish's Go client with `GatewayTLS: true` and cert paths
- Or set `openshell.server.disableTls: true` for plaintext (dev only —
  no encryption, no authentication between Astonish and gateway)

### NetworkPolicy (mesh-agnostic)

The NetworkPolicy (`templates/openshell/networkpolicy.yaml`) is **always
rendered** when `sandbox.openshell.enabled=true`, regardless of mesh
provider. It restricts all egress from the sandbox namespace to:

1. OpenShell gateway in the control-plane namespace (port 8080/TCP)
2. Cluster DNS in kube-system (port 53/UDP+TCP)

All other egress is denied. This is a standard Kubernetes NetworkPolicy —
it requires only a CNI that supports NetworkPolicy enforcement (Calico,
Cilium, K3s kube-router, etc.), not a service mesh.

---

## 7. Configuration

### Helm Values

```yaml
# Service mesh (controls mesh-specific resources)
mesh:
  provider: istio  # "" | "istio"

# Sandbox backend
sandbox:
  enabled: true
  backend: openshell
  podSecurity: privileged  # Auto-promoted for openshell

  openshell:
    enabled: true
    image:
      repository: ghcr.io/sap/astonish-sandbox-openshell
      tag: dev

# OpenShell subchart pass-through
openshell:
  server:
    disableTls: true  # Mesh handles encryption
    sandboxNamespace: "astonishdev-sandbox"
    sandboxImage: "ghcr.io/sap/astonish-sandbox-openshell:dev"
```

### Go Config Struct

```go
type SandboxOpenShellConfig struct {
    GatewayAddr    string             `yaml:"gateway_addr" json:"gateway_addr"`
    GatewayTLS     bool               `yaml:"gateway_tls" json:"gateway_tls"`
    ClientCertPath string             `yaml:"client_cert_path,omitempty"`
    ClientKeyPath  string             `yaml:"client_key_path,omitempty"`
    CACertPath     string             `yaml:"ca_cert_path,omitempty"`
    AuthToken      string             `yaml:"auth_token,omitempty"`
    SandboxImage   string             `yaml:"sandbox_image,omitempty"`
    CertBundles    []CertBundleConfig `yaml:"cert_bundles,omitempty"` // PVC or ConfigMap (+ Kyverno) CA mounts
}
```

The TLS fields are optional — when using a service mesh with
`disableTls: true`, `GatewayTLS` is `false` and the cert paths are
empty. They exist for non-mesh environments where the client connects
directly with gateway-native mTLS.

---

## 8. Custom Sandbox Image

The Dockerfile at `docker/sandbox-openshell/Dockerfile` layers the
Astonish agent binary on top of the NVIDIA community sandbox base:

```dockerfile
FROM golang:1.24-alpine AS builder
COPY . /src
WORKDIR /src
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /tmp/astonish .

FROM ghcr.io/nvidia/openshell-community/sandboxes/base:latest
COPY --from=builder /tmp/astonish /usr/local/bin/astonish
WORKDIR /sandbox
```

The base image includes: Python 3.14, Node.js 22, git, gh CLI, vim,
nano, and common developer tools. The `astonish` binary provides the
`astonish node` NDJSON tool server.

The sandbox container runs as UID 0 in its securityContext (required by
the supervisor for Landlock/seccomp setup), but the supervisor demotes
the agent process to user `sandbox` before spawning it.

---

## 9. Deployment Prerequisites

The following are **cluster-scoped prerequisites** not managed by the
Astonish Helm chart:

### 1. Istio

```bash
istioctl install --set profile=minimal
kubectl label namespace astonishdev istio-injection=enabled
```

### 2. Agent Sandbox CRD + Controller

```bash
kubectl apply -f https://github.com/kubernetes-sigs/agent-sandbox/releases/latest/download/manifest.yaml
```

### 3. Deploy Astonish (includes OpenShell subchart)

```bash
helm upgrade --install astonish deploy/helm/astonish \
  -n astonishdev --create-namespace \
  -f deploy/helm/astonish/values-myenv.yaml
```

This single command deploys:
- Astonish API + Worker deployments
- OpenShell gateway StatefulSet (subchart)
- Sandbox namespace with PSA labels + Istio injection disabled
- Istio PeerAuthentication + AuthorizationPolicies
- NetworkPolicy for sandbox egress restriction

---

## 10. gRPC Client

### GatewayClient Interface

```go
type GatewayClient interface {
    CreateSandbox(ctx context.Context, req CreateSandboxRequest) (*CreateSandboxResponse, error)
    GetSandbox(ctx context.Context, sandboxID string) (*GetSandboxResponse, error)
    DeleteSandbox(ctx context.Context, sandboxID string) error
    ExecSandbox(ctx context.Context, sandboxID string, cmd []string, env map[string]string) (*ExecResult, error)
    WaitForSessionReady(ctx context.Context, sandboxID string) error
    Close() error
}
```

### Domain Types

```go
type SandboxPolicySpec struct {
    Version    int             `json:"version"`
    Landlock   *LandlockSpec   `json:"landlock,omitempty"`
    Filesystem *FilesystemSpec `json:"filesystem,omitempty"`
    Process    *ProcessSpec    `json:"process,omitempty"`
    Network    *NetworkPolicySpec `json:"network,omitempty"`
}

type LandlockSpec struct {
    Compatibility string `json:"compatibility"` // "best_effort" | "strict"
}

type FilesystemSpec struct {
    IncludeWorkdir bool     `json:"include_workdir"`
    ReadOnly       []string `json:"read_only,omitempty"`
    ReadWrite      []string `json:"read_write,omitempty"`
}

type CreateSandboxRequest struct {
    Name         string
    Image        string
    Env          map[string]string
    Labels       map[string]string
    Policy       *SandboxPolicySpec
    DriverConfig map[string]any // OpenShell driver-keyed envelope
}
```

The `mapPolicyToProto()` helper in `client_grpc.go` converts these
domain types to the protobuf `SandboxPolicy` message before sending
to the gateway. When `DriverConfig` is set, it is encoded as
`SandboxTemplate.driver_config` (`google.protobuf.Struct`). Platform
`cert_bundles` config renders into trust env vars and (for `source: pvc`)
the Kubernetes driver's PVC mount schema. Prefer `source: configMap` on
Cinder/EBS: Astonish omits PVC from `driver_config` and Kyverno injects
ConfigMap mounts (OpenShell's schema is PVC-only). By default the bundle
is also installed over `/etc/ssl/certs/ca-certificates.crt` so the
supervisor’s MITM upstream TLS trusts corporate roots — see
`pkg/sandbox/openshell/driver_config.go`.
Agent `SSL_CERT_FILE` alone is insufficient: OpenShell overwrites it to
`/etc/openshell-tls/ca-bundle.pem`.

---

## 11. File Map

| File | Purpose |
|------|---------|
| `pkg/sandbox/openshell/backend.go` | Backend registration, Config struct, constructor |
| `pkg/sandbox/openshell/session.go` | CreateSession (with policy + AlreadyExists), StopSession, DestroySession |
| `pkg/sandbox/openshell/exec.go` | Exec with `wrapCommand()` for WorkDir/Env |
| `pkg/sandbox/openshell/fleet.go` | Fleet/pool management, CreateSandbox with policy |
| `pkg/sandbox/openshell/policy.go` | `defaultSandboxPolicy()` — Landlock filesystem rules |
| `pkg/sandbox/openshell/driver_config.go` | CertBundles → trust env + PVC `driver_config` (or ConfigMap via Kyverno) |
| `pkg/sandbox/openshell/gateway_client.go` | GatewayClient interface, domain types (SandboxPolicySpec, etc.) |
| `pkg/sandbox/openshell/client_grpc.go` | Real gRPC implementation, `mapPolicyToProto()` |
| `pkg/sandbox/openshell/client_grpc_test.go` | Policy mapping tests |
| `pkg/sandbox/openshell/template.go` | Template stubs (not supported — use custom images) |
| `pkg/sandbox/openshell/network.go` | Network stubs (supervisor handles) |
| `pkg/sandbox/openshell/gen/openshellv1/` | Generated gRPC stubs from vendored protos |
| `proto/openshell/v1/` | Vendored NVIDIA proto definitions |
| `docker/sandbox-openshell/Dockerfile` | Custom sandbox image (base + astonish binary) |
| `deploy/helm/astonish/templates/openshell/mesh-istio.yaml` | Istio PeerAuth + AuthorizationPolicies (rendered when `mesh.provider: istio`) |
| `deploy/helm/astonish/templates/openshell/networkpolicy.yaml` | Sandbox egress NetworkPolicy |
| `deploy/helm/astonish/templates/sandbox/namespace.yaml` | Namespace with PSA + istio-injection labels |
| `deploy/helm/astonish/values-myenv.yaml` | Dev environment Helm values |

---

## 12. Next Steps

### Phase 1: L7 Network Egress Policy

**What:** OpenShell's network namespace + policy proxy intercepts all
outbound connections from the sandbox. Each connection is checked against
a per-sandbox network policy that specifies allowed destinations at the
URL/path/method level.

**Value:** The agent can only reach explicitly allowed endpoints (e.g.,
`github.com`, `registry.npmjs.org`, `api.openai.com`). All other egress
is denied. This prevents data exfiltration, SSRF attacks, and
unauthorized API calls from agent-generated code.

**What we need to do:**
1. Populate the `Network` field in `SandboxPolicySpec` with allowed
   egress rules (the `NetworkPolicySpec` type already exists in
   `gateway_client.go`)
2. Map the network policy to protobuf in `mapPolicyToProto()`
3. Define a default egress allowlist (e.g., GitHub, npm, PyPI, apt
   mirrors) and make it configurable per deployment
4. The gateway's supervisor will enforce this via its network namespace
   proxy — no code changes needed on the gateway side

**Example policy structure:**
```go
Network: &NetworkPolicySpec{
    Egress: []EgressRule{
        {Host: "github.com", Ports: []int{443}},
        {Host: "*.github.com", Ports: []int{443}},
        {Host: "registry.npmjs.org", Ports: []int{443}},
        {Host: "pypi.org", Ports: []int{443}},
        {Host: "archive.ubuntu.com", Ports: []int{80, 443}},
    },
    DefaultAction: "deny",
},
```

### Phase 2: Provider Credential Injection

**What:** OpenShell's provider system injects API keys into sandboxes as
environment variables managed by the supervisor. Credentials are never
written to disk, never visible in `/proc/*/environ` to other processes,
and are automatically rotated/revoked when the sandbox is destroyed.

**Value:** Agent tool calls that need credentials (e.g., `gh` CLI,
`git push`, API calls) get them securely injected without Astonish having
to pass them as exec env vars (which are visible to all processes in the
container).

**What we need to do:**
1. Call `CreateProvider` on the gateway at startup to register credential
   bundles (GH_TOKEN, API keys)
2. Call `AttachSandboxProvider` at sandbox creation to bind providers to
   the sandbox
3. The supervisor injects credentials into the agent process environment
   at spawn time — no changes to the `astonish node` protocol needed
4. Configure credential rotation policies (optional)

**Implemented (Fleet v1):** `pkg/fleet/openshell_providers.go` registers
providers from `plan.credential_injection.env` at fleet session start;
`pkg/api/fleet_sandbox_wiring.go` attaches them after sandbox creation and
detaches on cleanup. Plan-bound credentials (`plan.credentials` allowlist)
are resolved via `PlanBoundCredentialStore` in `pkg/fleet/plan_bound_store.go`.
File credentials use `Backend.PushFile` / backend exec materialization in
`pkg/fleet/credential_injection.go`. Regular Studio Chat is unchanged.

**gRPC methods involved:**
- `CreateProvider` — register a named credential bundle
- `ListProviders` / `GetProvider` — introspect registered providers
- `AttachSandboxProvider` — bind a provider to a specific sandbox
- `DetachSandboxProvider` — revoke access
- `GetSandboxProviderEnvironment` — supervisor calls this to get env vars

### Phase 3: Inference Routing (Privacy Proxy)

**What:** OpenShell's inference router intercepts LLM API calls from
inside the sandbox and routes them through a privacy-aware proxy at the
gateway. The proxy strips caller credentials, injects backend
credentials, and can enforce model/token budgets.

**Value:** If an agent spawns a sub-process that tries to call an LLM
API directly (e.g., a tool that uses the OpenAI SDK), the gateway:
- Strips any credentials the process might have found
- Injects the correct backend credentials
- Logs the inference call for audit
- Enforces rate limits and model restrictions

**What we need to do:**
1. Configure inference provider profiles in the gateway config
2. Set up the `inference.local` endpoint that sandbox processes route to
3. Configure the network policy to redirect LLM API traffic through the
   proxy (deny direct egress to `api.openai.com`, allow only via
   `inference.local`)
4. This is the most complex phase — requires gateway config changes
   (not just Astonish code changes)

**gRPC methods involved:**
- `CreateProvider` (with inference profile type)
- `ImportProviderProfiles` — bulk import from config file
- `ConfigureProviderRefresh` — auto-rotate credentials
