# pkg/sandbox — AGENTS.md

Backend abstraction for sandboxed execution. Every tool that runs a shell command, touches the filesystem, or opens a network connection **must** go through this layer.

## Scope
- `Backend` interface + factory (`backend_factory.go`, `backend_contract.go`).
- Backend implementations: `docker/` (local default), `k8s/`, `openshell/`, `mock/`.
- Image build (`imagebuilder/`) — Kaniko-driven, content-addressed tags.
- Template metadata (`tmplmeta/`).
- Flow-level wiring (`flow.go`).
- Session persistence (`session_store_local.go` and the platform stores upstream).

## Backend selection
- Config `BackendKind` → factory in `backend_factory.go`.
- Kinds: `BackendKindDocker` (default; empty and legacy `"incus"` alias to docker), `BackendKindK8s`, `BackendKindOpenShell`, `BackendKindMock`.
- Backends are registered via `RegisterBackendFactory`. Blank imports in `cmd/astonish/sandbox_backends.go` guarantee the docker/k8s/openshell/mock packages link into the binary.
- **Never call a backend implementation directly** from outside `pkg/sandbox` — always go through the `Backend` interface obtained from the factory. Otherwise you break the mock-based test story.

## Backend contract
Every backend implementation must satisfy the tests in `backend_contract.go` (`RunBackendContract`):

- `CreateSession` / `StartSession` / `WaitForSessionReady` / `StopSession` / `DestroySession` — the full lifecycle. Idempotent where the interface says so.
- `Exec` (buffered), `ExecInteractive` (PTY, bidi), `ExecStreaming` (line/byte stream).
- `PushFile` / `PullFile` — direction and error semantics must match the mock.
- `ExposePort`, template operations.
- Context cancellation propagates and aborts the underlying operation.

If you add a backend, run the contract suite against it in CI. If you change the contract, update **all** backends and the mock together.

## OpenShell gRPC contract
- Proto lives in `proto/openshell/v1/*.proto`. Generated Go under `pkg/sandbox/openshell/gen/openshellv1/`. Do **not** edit generated `.pb.go` — regenerate.
- Key RPCs:
  - `CreateSandbox` — provision a pod from a `SandboxTemplate` + `SandboxPolicy`.
  - `ExecSandbox` (server-streaming) — non-interactive exec, streams stdout/stderr/exit.
  - `ExecSandboxInteractive` (bidi) — PTY. First client message **must** be the `start` variant; subsequent messages carry stdin/resize.
  - `ConnectSupervisor` (bidi) — persistent stream opened by the in-pod supervisor to receive relay open/close requests.
  - `RelayStream` (bidi) — raw byte relay opened per `RelayOpen`.
  - `WatchSandbox` — status/logs/events.
  - `IssueSandboxToken` / `RefreshSandboxToken` — projected K8s SA token → gateway JWT bound to the sandbox UUID.
- Policy proto (`sandbox.proto`): `SandboxPolicy` with `Filesystem` / `Network` / `Process` rules is enforced **inside the sandbox by the supervisor** (Landlock + seccomp + L7 inspection). Do not assume host-side checks alone are sufficient.

## Image build flow
1. `pkg/api/image_build_handlers.go` receives a Dockerfile body, acquires a per-template build lock, and calls `imagebuilder.Builder`.
2. Builder computes `ImageRef` = base image name + `sha256(dockerfile-content)[:12]` — deterministic and content-addressed.
3. Builder creates a ConfigMap (Dockerfile) + Kubernetes Job (Kaniko) in the control-plane namespace, streams logs, waits for job completion (30-min timeout).
4. On success, the API handler records `LastBuiltImage`, `SandboxImage`, and `BuildStatus=succeeded` on the template.
5. On failure it records `BuildStatus=failed` with `BuildError`.

**Do not** add non-deterministic inputs (timestamps, build machine, current user) to the content hash — reproducibility across build machines relies on this.

## Session provisioning (per backend)
- **Docker OverlayFS**: `pkg/sandbox/docker` creates `astonish-session-*` containers from `ghcr.io/sap/astonish-sandbox-base`. Layers live on the host (`LayersDir`); the live upper is a Docker volume; `fuse-overlayfs` (default) composes `/sandbox/rootfs`. Same overlay contract as K8s. Used for local Studio on macOS and Linux. Session `docker run` / seed `docker create` MUST be invoked as `docker container run|create` with `--flag=value` (never split `--name value`) so LXC AppArmor wrappers that rewrite top-level `run`/`create` cannot report `invalid reference format` or start the seed image.
- **OpenShell**: `Gateway.CreateSandbox` provisions a pod; the in-pod supervisor opens `ConnectSupervisor`; exec/push/pull go through `ExecSandbox` / `ExecSandboxInteractive`. Evicted sandboxes are auto-resumed by `ensureSessionRunning`. Platform `cert_bundles` set trust env and (for `source: pvc`) `SandboxTemplate.driver_config` PVC mounts; `source: configMap` omits PVC and relies on Kyverno inject — see `pkg/sandbox/openshell/driver_config.go`.
- **K8s (direct, without OpenShell)**: `pkg/sandbox/k8s` — image pull policy is `Always` for mutable tags (`latest`, `dev`) and `IfNotPresent` for pinned digests. Enforces per-org/team labels and `NetworkPolicy`.
- **Mock**: in-memory, used by unit tests; supports injection hooks.

## Entrypoint contract
The Docker/K8s sandbox-base image and the OpenShell sandbox image (`docker/sandbox-base/Dockerfile`, `docker/sandbox-openshell/Dockerfile`) ship:
- `/usr/local/bin/astonish-host` — the real binary copied into the base image.
- `/usr/local/bin/astonish` — a wrapper that chroots into the composed overlay before exec'ing the real binary. **All Exec calls (from Astonish and kubectl exec both) rely on this wrapper.**
- `/usr/local/bin/astonish-shell` — interactive wrapper for team-admin interactive shells.
- The pod entrypoint (generated on the host via `make sandbox-entrypoint` / `cmd/astonish-sandbox-entrypoint-script`, then `COPY`ed into the image) composes the overlay (overlayfs / fuse-overlayfs / tar-resume depending on node capabilities) at `/sandbox/rootfs` and keeps PID 1 running.

**sandbox-base image build (cache contract):** Go is **not** compiled inside Docker. Prerequisites: `make build-linux` (and `build-linux-arm64` for multi-arch) + `make sandbox-entrypoint`. The Dockerfile keeps apt + treesitter + shell wrappers above the volatile `COPY` of entrypoint/binary so routine agent code changes only invalidate the last layers. Local rebuilds use BuildKit local cache under `.buildx-cache/sandbox-base`. Prefer `make push-sandbox-base-dev-fast` / `make docker-sandbox-base` for iteration.

If you change the entrypoint, update the generator in `cmd/astonish-sandbox-entrypoint-script/` and the wrapper section of both Dockerfiles together.

## Isolation model (summary)
- **Kernel**: Landlock + seccomp inside the sandbox (OpenShell supervisor). Optional user-namespace mapping via `SandboxTemplate.user_namespaces`.
- **Network**:
  - K8s: `NetworkPolicy` per org/team labels; OpenShell adds L7 inspection driven by `SandboxPolicy.network_policies`.
  - Docker OverlayFS: per-org Docker bridge networks (`astonish-org-<slug>`).
- **Filesystem**: per-session overlay; template layers are content-addressed directories (Docker/K8s) or image-tagged (OpenShell).
- **Bootstrap files**: template `bootstrap_files` (e.g. `.astonish/start-services.sh`) are injected at session start and never auto-executed — drills/fleet/chat call them after credentials.
- **Template overwrite**: `CreateTemplateFromContainer(..., overwrite=true)` when saving the **same** name the session is based on **flattens** (template upper ∪ session upper) onto the parent `BasedOn` (usually `base`). Never delete the source template before that flatten finishes — delete-first self-overwrite leaves the registry empty and breaks `ResolveLowerLayers`. Overwriting a *different* name may still delete-first.
- **Recovery if a template was deleted mid-overwrite**: Keep the live session (do not `use_sandbox_template` / reboot). Restart Studio with a build that has the flatten fix, then `save_sandbox_template(name, overwrite: true, bootstrap_files: …)` again. If the source is already gone from the registry, create materializes the session rootfs onto `@base`. If the session is already dead: recreate from `@base` (clone, deps, build, scripts) and save without overwrite.
- **Identity**: `IssueSandboxToken` binds the in-pod supervisor's identity to the sandbox UUID via a gateway-issued JWT.
- **Ops**: `tplStore.AcquireTemplateBuildLock` prevents concurrent Kaniko builds for the same template.

## When editing
1. Changing the `Backend` interface? Update **all** backends + `RunBackendContract` + the mock together; the OpenShell client must also carry any new semantics through gRPC.
2. Changing the OpenShell proto? Bump `proto/openshell/v1/*.proto`, regenerate, update `pkg/sandbox/openshell/client_grpc.go`, and coordinate with the OpenShell gateway version.
3. Changing image build tagging or Kaniko orchestration? Update `imagebuilder/imagebuilder.go` and `pkg/api/image_build_handlers.go` in the same commit — the handler owns the lock + template write-back.
4. Changing the entrypoint or wrappers? Rebuild `docker/sandbox-base` and `docker/sandbox-openshell`; run `make test-e2e-openshell` and `make test-e2e` (which exercises the k8s path).

## References
- `docs/architecture/sandbox-backends.md` — deep dive on all backends.
- `docs/architecture/openshell-sandbox-backend.md` — OpenShell + Landlock/seccomp specifics.
