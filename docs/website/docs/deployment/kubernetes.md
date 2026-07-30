# Kubernetes Deployment

This guide covers deploying Astonish as a multi-tenant platform on Kubernetes using the official Helm chart.

## Prerequisites

- Kubernetes 1.28+ cluster
- Helm 3.12+
- `kubectl` configured for your cluster
- PostgreSQL 15+ with the `pgvector` extension enabled
- A container registry accessible from the cluster

## Architecture

```
┌─────────────────────────────────────────────────────┐
│  Kubernetes Cluster                                 │
│                                                     │
│  ┌──────────┐  ┌──────────┐  ┌─────────────────┐  │
│  │ Astonish │  │ Astonish │  │ Sandbox Pods     │  │
│  │  API     │  │  Worker  │  │ (per-org netpol) │  │
│  └────┬─────┘  └────┬─────┘  └─────────────────┘  │
│       │              │                              │
│       └──────┬───────┘                              │
│              │                                      │
│       ┌──────▼──────┐                               │
│       │ PostgreSQL  │                               │
│       │ + pgvector  │                               │
│       └─────────────┘                               │
└─────────────────────────────────────────────────────┘
```

## Step 1: Initialize the Database

Run from a machine with network access to your PostgreSQL instance:

```bash
astonish platform init \
  --host <postgres-host> \
  --port 5432 \
  --user postgres \
  --password <postgres-admin-password> \
  --sslmode require
```

This creates the platform database and runs migrations. It prints the connection DSN — save this for the Helm values.

Available flags:

| Flag | Default | Env Fallback | Description |
|------|---------|--------------|-------------|
| `--host` | (required) | `PGHOST` | PostgreSQL hostname |
| `--port` | `5432` | `PGPORT` | PostgreSQL port |
| `--user` | `postgres` | `PGUSER` | PostgreSQL admin user |
| `--password` | (required) | `PGPASSWORD` | PostgreSQL admin password |
| `--sslmode` | `prefer` | `PGSSLMODE` | SSL mode |
| `--suffix` | auto-generated | — | Fixed instance suffix |

## Step 2: Generate Secrets

Generate the master encryption key and JWT signing secret:

```bash
astonish platform gen-secret   # → use as masterKey
astonish platform gen-secret   # → use as jwtSecret
```

## Step 3: Create the Kubernetes Secret

```bash
kubectl create namespace astonish

kubectl create secret generic astonish-secrets \
  --namespace astonish \
  --from-literal=master-key="<master-key-from-step-2>" \
  --from-literal=jwt-secret="<jwt-secret-from-step-2>" \
  --from-literal=platform-dsn="<dsn-from-step-1>"
```

## Step 4: PostgreSQL Setup

Astonish requires a PostgreSQL instance with `pgvector`. You can use a managed service (RDS, Cloud SQL, Azure Database) or deploy in-cluster.

Enable the extension:

```sql
CREATE EXTENSION IF NOT EXISTS vector;
```

Astonish uses **separate databases per organization** for tenant isolation. The application role needs `CREATEDB` privilege:

```sql
CREATE ROLE astonish WITH LOGIN PASSWORD 'secure-password' CREATEDB;
```

## Step 5: Helm Values

Create a `values.yaml` file:

```yaml
image:
  repository: ghcr.io/sap/astonish
  tag: "latest"
  pullPolicy: IfNotPresent

namespaces:
  prefix: astonish

secrets:
  existingSecret: "astonish-secrets"

config:
  storage:
    backend: postgres
    postgres:
      instanceSuffix: ""  # Match the suffix from platform init
  auth:
    mode: local
    registration: invite

api:
  replicaCount: 2

ingress:
  enabled: true
  className: nginx
  hosts:
    - host: astonish.example.com
      paths:
        - path: /
          pathType: Prefix
  tls:
    - secretName: astonish-tls
      hosts:
        - astonish.example.com

sandbox:
  enabled: true
  backend: k8s
  image:
    repository: ghcr.io/sap/astonish-sandbox-base
    tag: "latest"
  storage:
    storageClassName: "<your-rwx-storage-class>"
  limits:
    cpu: 2
    memory: "2Gi"
    processes: 500
  requests:
    cpuMillis: 100
    memoryMiB: 256

autoscaling:
  enabled: true
  minReplicas: 2
  maxReplicas: 10
  targetCPUUtilizationPercentage: 70
```

## Step 6: Install the Chart

```bash
# From a local clone of the repository
helm install astonish deploy/helm/astonish \
  --namespace astonish \
  --values values.yaml
```

## Step 7: Verify the Deployment

```bash
# Check pod status
kubectl get pods -n astonish

# Check API logs. Kubernetes deployments run with ASTONISH_MODE=api/worker,
# so Astonish writes application logs to stdout for kubectl and log collectors.
kubectl logs -n astonish -l app.kubernetes.io/component=api --tail=50

# Check worker logs
kubectl logs -n astonish -l app.kubernetes.io/component=worker --tail=50

# Liveness check
curl https://astonish.example.com/api/healthz

# Readiness check
curl https://astonish.example.com/api/readyz
```

## Access Studio

### Via Ingress

If you configured ingress, open `https://astonish.example.com` in your browser.

### Via Port-Forward

For local access without ingress:

```bash
kubectl -n astonish port-forward svc/astonish-api 9393:9393 &
open http://localhost:9393
```

The first user to register will become the organization owner with full admin access. A default organization and team are created automatically on first signup.

## Upgrades

Upgrade to a new version:

```bash
helm upgrade astonish deploy/helm/astonish \
  --namespace astonish \
  --values values.yaml \
  --set image.tag="new-version"
```

Astonish runs database migrations automatically on startup. For major version upgrades, consult the release notes for any required manual migration steps.

## Scaling

The API server is stateless and can be horizontally scaled:

```yaml
api:
  replicaCount: 4

autoscaling:
  enabled: true
  minReplicas: 2
  maxReplicas: 10
  targetCPUUtilizationPercentage: 70
```

## Troubleshooting

| Symptom | Cause | Fix |
|---------|-------|-----|
| Pod in CrashLoopBackOff | Missing secret or bad DB URL | Check `kubectl describe pod` and secret values |
| `/api/readyz` returns 503 | Database unreachable | Verify PostgreSQL connectivity and credentials |
| OIDC login fails | Incorrect issuer URL | Confirm the issuer matches your provider's discovery endpoint |
| Sandbox pods not starting | Missing NetworkPolicy controller | Install a CNI that supports NetworkPolicy (Calico, Cilium) |

## See Also

- [Deployment Overview](./index.md) — comparison of deployment models
- [OpenShell](./openshell.md) — kernel-level agent sandboxing with NVIDIA OpenShell
- [Authentication](../security/authentication.md) — configuring OIDC providers
- [Envelope Encryption](../security/envelope-encryption.md) — master key management
