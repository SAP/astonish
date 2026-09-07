# Deployment Overview

Astonish supports three deployment models, scaling from a single developer workstation to enterprise Kubernetes clusters with kernel-level agent isolation.

## Deployment Models

| | Local | Kubernetes (Standard) | Kubernetes (OpenShell) |
|---|---|---|---|
| **Use case** | Individual developer | Team/enterprise platform | Secure autonomous agents |
| **Database** | SQLite | PostgreSQL + pgvector | PostgreSQL + pgvector |
| **Auth** | Local (setup wizard) | JWT + OIDC federation | JWT + OIDC federation |
| **Sandboxes** | Docker OverlayFS sessions | K8s pods + NetworkPolicy | OpenShell (kernel-level isolation) |
| **Encryption** | Local keychain | Master KEK + per-org DEK | Master KEK + per-org DEK |
| **Multi-tenant** | No | Yes | Yes |
| **Install method** | Single binary | Helm chart | Helm chart + OpenShell |

## When to Use Which

**Local** — You are a single developer who wants an AI agent platform on your workstation. No server infrastructure required. Install the binary, start the daemon, and begin working. All platform features run locally with SQLite.

**Kubernetes (Standard)** — Your organization needs a shared platform with team management, credential sharing, audit logging, and network-isolated sandboxes. Deploy via Helm to any Kubernetes cluster with PostgreSQL.

**Kubernetes (OpenShell)** — Your agents need to execute autonomously with access to files, credentials, and networks — but you require kernel-level isolation, granular policy enforcement, and full audit trails. NVIDIA OpenShell provides security-hardened sandbox environments where agents can operate freely without risking system compromise.

## Prerequisites

### Local

- macOS or Linux
- Astonish binary ([install guide](../getting-started/index.md))
- Docker (Colima or Docker Desktop on macOS) for OverlayFS sandboxes

### Kubernetes (Standard)

- Kubernetes 1.28+
- Helm 3.x
- PostgreSQL 15+ with pgvector extension
- Container registry access for sandbox images

### Kubernetes (OpenShell)

- Everything in Standard, plus:
- OpenShell deployed ([GitHub](https://github.com/NVIDIA/OpenShell))
- Policy profiles configured for your agent workloads

## Quick Start

### Local

```bash
astonish setup
astonish daemon install
astonish daemon start
```

Studio is available at `http://localhost:9393`. See [Running as a Service](./running-as-service.md) for details on the daemon lifecycle.

### Cloud (Kubernetes)

```bash
# Initialize the database
astonish platform init \
  --host <postgres-host> \
  --password <postgres-admin-password>

# Install with Helm
helm install astonish deploy/helm/astonish \
  --namespace astonish --create-namespace \
  --values values.yaml
```

See [Kubernetes Deployment](./kubernetes.md) for the full guide.

## See Also

- [Kubernetes Deployment](./kubernetes.md) — full Helm chart reference
- [OpenShell](./openshell.md) — kernel-level agent sandboxing with NVIDIA OpenShell
- [Running as a Service](./running-as-service.md) — daemon management for local deployments
