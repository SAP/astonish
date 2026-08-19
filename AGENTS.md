# AGENTS.md

## Project overview

Astonish is an AI-agent platform distributed as one Go binary. It supports a local coding TUI, a remote multi-tenant chat/platform, HTTP/SSE APIs, reusable flows, tools/MCP, memory, browser automation, fleet orchestration, and isolated execution through Incus, Kubernetes, or OpenShell backends. The Studio UI is a React SPA embedded into the Go binary.

The repository is primarily:

- **Go 1.26** (`go.mod`, toolchain `go1.26.1`) for the CLI, agent runtime, API, storage, TUI, and sandbox backends.
- **React 19 + TypeScript/legacy JSX + Vite + Tailwind CSS 4** under `web/`.
- **Ent** for scoped persistence, with SQLite for personal/development use and PostgreSQL for the multi-tenant platform.

Before changing a subsystem, read the nearest nested `AGENTS.md`; those files contain stricter subsystem-specific contracts and override this root guide.

## Repository map

- `main.go`, `cmd/astonish/` — binary entry point and CLI dispatch. Commands should parse arguments and delegate; keep business logic in `pkg/`. Preserve local/remote command gating. Bare `astonish` launches local code mode in a TTY.
- `pkg/agent/` — LLM-driven agent loop, prompt construction, tool execution, compaction, delegation, and approvals.
- `pkg/api/` — platform/Studio HTTP handlers, authentication, SSE chat/fleet streams, and orchestration wiring.
- `pkg/launcher/` — composition roots for local code TUI, remote chat TUI, and server/daemon launch paths.
- `pkg/tui/`, `pkg/ui/` — Bubble Tea terminal clients and reusable terminal rendering.
- `pkg/tools/`, `pkg/browser/`, `pkg/codeintel/` — built-in tools, CDP browser automation, and source-code intelligence.
- [`pkg/provider/`](pkg/provider/AGENTS.md), [`pkg/mcp/`](pkg/mcp/AGENTS.md), [`pkg/skills/`](pkg/skills/AGENTS.md) — model providers, MCP integration, and skill loading.
- [`pkg/config/`](pkg/config/AGENTS.md) — configuration loading, defaults, and environment overrides.
- [`pkg/store/`](pkg/store/AGENTS.md) — storage interfaces and implementations/routing; tenant scope is a core invariant.
- [`pkg/memory/`](pkg/memory/AGENTS.md), `pkg/session/`, `pkg/fleet/`, `pkg/drill/`, `pkg/channels/` — major domain subsystems.
- [`pkg/scheduler/`](pkg/scheduler/AGENTS.md) — scheduled-job registration and execution.
- [`web/src/api/`](web/src/api/AGENTS.md), [`web/src/components/chat/`](web/src/components/chat/AGENTS.md) — Studio transport contracts and chat rendering/interaction guidance.
- `pkg/sandbox/` — backend-neutral sandbox contracts plus Incus/Kubernetes/OpenShell implementations. `Backend` implementations must be concurrency-safe and lifecycle methods are intentionally idempotent.
- `ent/{platform,org,team,personal}/` — four persistence scopes. Only `schema/*.go` and `generate.go` are normally hand-edited; the remaining Ent files are generated.
- `web/src/` — Studio SPA. Entry points are `main.tsx` and `App.tsx`; `api/` contains REST/SSE clients and `components/` contains UI.
- `tests/e2e/`, `tests/e2eboot/`, `tests/scenarios/` — tagged E2E suites, shared bootstrap harness, and scenario catalogs/reporters.
- `docs/architecture/` — authoritative design documents. Update the relevant document when changing a documented protocol or invariant.
- `proto/`, `deploy/`, `docker/` — protocol definitions, deployment assets, and container images.

## Setup, build, and run

Prerequisites: Go 1.26.x, Node.js 24+ with npm, Make, and golangci-lint matching `.golangci-version` (currently the 2.12 minor line).

```bash
make build-all       # Ent generation + npm/UI build + Go binary; also configures git hooks
make build           # Go binary only: ./astonish
make build-ui        # npm install and production UI/sandbox bundles in web/dist
make run             # go run .
go run .              # run CLI directly
make studio           # build UI, then start Studio backend
make studio-dev       # backend on :9393; run `cd web && npm run dev` separately for :5173
```

`Makefile` auto-loads gitignored `.env` and `.env.local`; existing shell variables win. Start from `.env.example` or `.env.integration.example` and never commit credentials.

Useful generators:

```bash
make ent-generate       # regenerate all four Ent scopes
make proto-gen          # regenerate gRPC stubs from vendored proto files
make sandbox-entrypoint # regenerate the canonical sandbox entrypoint script
make build-treesitter-lib
```

Do not hand-edit generated Ent clients, protobuf stubs, `web/dist`, or generated sandbox artifacts. Change their source and rerun the corresponding target.

## Test and lint commands

Run the narrowest relevant checks while iterating, then broaden before finishing.

```bash
# All dependency-free unit suites
make test                         # go test ./... + frontend Vitest

# Go
go test ./pkg/agent/...           # focused package subtree
go test ./pkg/tools -run TestName
go test -race ./pkg/tools
go test ./...                     # all normal Go tests
make lint                         # version check + golangci config verify/run

# Frontend
cd web && npm test                # Vitest, one run
cd web && npm run typecheck
cd web && npm run lint            # installs isolated lint-tool dependencies
cd web && npm run build           # typecheck + both Vite bundles
```

External test tiers:

```bash
make test-integration  # requires admin-capable ASTONISH_TEST_DSN; integration build tag
make test-e2e          # PostgreSQL + live LLM key + provisioned Kubernetes sandbox infra
make test-e2e-sqlite   # SQLite, but still needs live LLM key and Kubernetes for sandbox tests
make e2e-k8s-up        # provision E2E namespaces/PVCs
make e2e-k8s-down
```

E2E tests use the `e2e` build tag and run serially through the scenario reporter. Put shared DB/auth/provider/sandbox setup in `tests/e2eboot/`, not individual tests. Never point `ASTONISH_TEST_DSN` at production; the harness creates and drops databases.

## Code style

### Go

- Run `gofmt`; use tabs. Import groups are standard library, external dependencies, then this module.
- Use lowercase package names, `PascalCase` exports, and `camelCase` internals.
- Return errors last, check them promptly, and wrap with context using `fmt.Errorf("operation: %w", err)`.
- Prefer interfaces at subsystem boundaries and small dependency-injection methods over global state.
- Keep command handlers thin. Add tests beside code as `*_test.go`; table-driven tests are preferred where cases share structure.
- Use `t.TempDir()` or a temporary directory with `t.Cleanup`; do not leave test state on disk.
- The configured Go linters are `govet`, `ineffassign`, `unused`, `staticcheck`, and `gosec`. Do not weaken lint configuration to hide a new finding.

### Frontend

- New UI files should be `.tsx`; legacy `.jsx` remains but should not be copied without reason.
- Use functional components and hooks. Keep one main component per file and default-export it; extract components before files become unwieldy (roughly 300 lines).
- Use React state/context only—do not introduce a separate state-management framework.
- Use Tailwind v4 semantic tokens and local primitives in `web/src/components/ui/`; do not hard-code brand purple/indigo colors. Read `docs/architecture/studio-ui-system.md` for styling work.
- Use two-space indentation and external imports before local imports. Prefer the `@/*` alias for shared `web/src` imports.
- Add/update Vitest coverage for behavior changes.

## Critical invariants and cross-cutting changes

- **Tenant boundaries:** persistence is split into `platform → org → team → personal`. Defaults cascade downward; sharing/publication upward is explicit. Route through scoped stores and treat accidental cross-team or cross-org visibility as a security bug.
- **Ent edits:** modify `ent/<scope>/schema/*.go`, regenerate, and commit generated output. Schema changes and their required migrations belong in the same change. Read `ent/AGENTS.md` and `docs/architecture/migrations.md` first.
- **SSE contracts:** every event emitted by Go chat/fleet code must have a matching Studio consumer. Add or rename backend and frontend handlers in the same change and include a scenario fixture where applicable.
- **Report rendering:** Studio promotes markdown to the report harness only when it is from the last turn, has `fileType === "Markdown"`, and backend-provided `isReport === true`. Fix marker generation in `pkg/api/chat_runner.go`; never weaken the client gate.
- **Terminal parity:** chat-stream behavior changes may affect both Studio and the terminal TUI. Check `pkg/tui/` whenever changing events, approvals, artifacts, or rendering semantics.
- **Generative UI security:** Apps execute in a sandboxed opaque-origin iframe and use `postMessage` plus a server-side SSRF-protected proxy. Do not bypass the iframe/origin boundary.
- **Sandbox lifecycle:** preserve backend idempotency, readiness-before-exec, stream ownership/closure, and capability-based behavior. Read `pkg/sandbox/AGENTS.md` and `docs/architecture/sandbox-backends.md` before backend work.
- **CLI modes:** local code mode is deliberately ungated and host-filesystem based; remote chat requires login. Preserve `mustBeRemote`/`mustNotBeRemote` decisions when adding commands. New sandbox backends also require registration/linkage described in `cmd/astonish/AGENTS.md`.
- **Architecture docs:** when a change alters a protocol, security boundary, storage scope, generated artifact, or runtime flow, update the matching file in `docs/architecture/` in the same change.

## Working practices

1. Read the closest nested `AGENTS.md` and relevant architecture document before editing.
2. Use code intelligence to trace definitions, callers, tests, and frontend/backend consumers before changing shared symbols.
3. Keep changes scoped; do not mix visual refreshes with SSE/report behavior or unrelated generated output.
4. Add focused tests for modified behavior. Verify the affected package/UI first, then run `make test`, relevant build checks, and linters as practical.
5. Do not commit local binaries, `.env*` secrets, API keys, test databases, or transient E2E output.
