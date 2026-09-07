# Sandboxes

Astonish executes agent tool calls inside isolated sandbox environments. Each organization gets its own network-isolated sandbox infrastructure, preventing lateral movement between tenants even if code execution is compromised.

## Per-Org Network Isolation

Every organization's sandboxes run on a dedicated network segment. Containers from different orgs cannot communicate at the network layer — isolation is enforced by the infrastructure, not application logic.

## Backends

Astonish supports three sandbox backends depending on your deployment model.

### OpenShell (NVIDIA)

The most secure backend, using NVIDIA's OpenShell Gateway with kernel-level isolation:

- **Landlock LSM** — Linux Security Module enforcing per-sandbox filesystem policies with `best_effort` compatibility mode
- **Seccomp** — system call filtering to restrict available kernel interfaces
- **Network namespaces** — each sandbox gets its own network namespace; a supervisor proxy denies all egress by default
- **Non-root execution** — commands run as the `sandbox` user with read-only system paths

#### Filesystem Policy

| Access | Paths |
|--------|-------|
| Read-only | `/usr`, `/bin`, `/sbin`, `/lib`, `/lib64`, `/etc`, `/opt` |
| Read-write | `/sandbox` (workspace), `/tmp`, `/var/tmp`, `/home`, `/run` |

Packages must be baked into the sandbox image at build time — `apt install` is blocked at runtime by design (requires root and writable system paths).

#### Network Policy

Egress is denied by default. Allow specific traffic using presets:

| Preset | Allows |
|--------|--------|
| `code_hosting` | GitHub, GitLab, Bitbucket |
| `package_registries` | npm, PyPI, apt mirrors |
| `llm_apis` | OpenAI, Anthropic, etc. |
| `tools` | Common developer tool APIs |
| `search` | Search engines |
| `cdn` | CDN providers |

Custom endpoints can be added via configuration:

```yaml
sandbox:
  openshell:
    network_policy:
      presets: ["code_hosting", "package_registries"]
      extra_endpoints:
        - host: "internal.corp.com"
          port: 443
```

For full details on managing network access rules — including multi-tier admin policies (platform/org/team), deny-wins-from-above merge semantics, and in-chat interactive approval — see [Network Policy](./network-policy.md).

#### Authentication

The OpenShell backend authenticates to the gateway via mTLS (client certificate + key + CA) or a static bearer token for development environments.

### Kubernetes

For platform deployments on Kubernetes, sandboxes run as pods with NetworkPolicy-based isolation and portable overlay filesystems.

#### Pod Security

Sandbox pods are hardened with:
- SecurityContext restrictions (non-root, read-only root filesystem where applicable)
- RuntimeClassName support for additional isolation (gVisor, Kata)
- User namespace isolation via `hostUsers: false`
- Overlay filesystem via fuse-overlayfs (default) with kernel overlayfs fallback

#### Pod Labels

Every sandbox pod is labeled for policy targeting:

```yaml
metadata:
  labels:
    astonish.dev/org: acme
    astonish.dev/team: backend
    astonish.dev/sandbox: "true"
    astonish.dev/session: a1b2c3d4
```

#### NetworkPolicies

A NetworkPolicy is applied per org to restrict traffic:

```yaml
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: astonish-sandbox-acme
spec:
  podSelector:
    matchLabels:
      astonish.dev/org: acme
      astonish.dev/sandbox: "true"
  policyTypes:
    - Ingress
    - Egress
  ingress:
    - from:
        - podSelector:
            matchLabels:
              astonish.dev/org: acme
  egress:
    - to:
        - podSelector:
            matchLabels:
              astonish.dev/org: acme
```

Pods from org `acme` can only communicate with other `acme` pods. Cross-org traffic is denied by default.

### Docker OverlayFS (local)

For non-Kubernetes deployments, Astonish runs session containers with Docker OverlayFS — the same overlay contract as the Kubernetes sandbox-base image. This is the local Studio default on both macOS and Linux.

- Session containers are named `astonish-session-*` from `ghcr.io/sap/astonish-sandbox-base`.
- Template layers live on the host; the live upper is a Docker volume.
- `fuse-overlayfs` composes `/sandbox/rootfs`. Tools run via `astonish-shell`.

#### Network Architecture

Each organization gets a dedicated Docker bridge network:

```
astonish-org-acme     →  org acme containers only
astonish-org-other    →  org other containers only
```

Containers on different org networks have no route to each other.

#### Container Naming

```
astonish-session-<session-id-prefix>-<short>
```

## Team-Scoped Sandbox Templates

Organizations can define sandbox templates per team, controlling the base image, resource limits, and pre-installed packages:

```yaml
sandbox:
  templates:
    backend:
      image: astonish-sandbox:python-3.12
      cpu: "2"
      memory: 4Gi
      packages: [git, python3, python3-pip, curl]
    frontend:
      image: astonish-sandbox:node-22
      cpu: "1"
      memory: 2Gi
      packages: [git, nodejs, npm]
```

Templates are managed through Studio or the platform API. When packages are added, Astonish builds a new sandbox image automatically using Kaniko (no Docker daemon required).

## Sandbox Auditing

For Kubernetes deployments, audit sandbox storage for orphaned data:

```bash
astonish platform sandbox-audit
```

This spawns a remote audit pod that diffs on-disk PVC contents against database records, identifying orphaned sandbox data that can be reclaimed.

Options:
- `--reclaim` — automatically reclaim orphaned data
- `--grace 24h` — grace period before reclaiming
- `--namespace` — target namespace
- `--kubeconfig` — path to kubeconfig

## See Also

- [Deployment Overview](../deployment/index.md) — choosing between sandbox backends
- [OpenShell](../deployment/openshell.md) — detailed OpenShell deployment guide
- [Credential Security](./credential-security.md) — how secrets are injected into sandbox environments
