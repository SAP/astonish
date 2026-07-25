# AGENTS.md - Agent Coding Guidelines

This document is the **root** guidance file for agentic coding systems working on the Astonish codebase. Deeper AGENTS.md files exist under significant subsystems (see [Hierarchical AGENTS.md Index](#hierarchical-agentsmd-index)) — always consult the deepest one whose scope covers the files you are editing.

## Project Overview

Astonish is a **multi-tenant AI agent platform** written in Go with a React/TypeScript UI. It uses Google's Agent Development Kit (ADK) and provides three top-level modes:

- **Personal / CLI**: `astonish chat` — single-user, SQLite, in-process agent runtime.
- **Studio (HTTP + SPA)**: `astonish daemon run` → serves REST/SSE API under `/api/*` and the embedded React SPA. This is what people usually call "Astonish Studio".
- **Platform**: multi-tenant PostgreSQL, envelope encryption, org/team/personal scoping, sandboxed execution over Kubernetes/OpenShell/Incus, channel adapters (Slack/Telegram/Email), scheduler, remote CLI.

**Stack:**
- **Backend**: Go 1.26 (see `go.mod`; toolchain go1.26.x), ADK (`github.com/google/adk-go`), Ent ORM (`entgo.io/ent`), bubbletea (TUI), pgx (Postgres), `atlas` for migrations, `mcp-go-sdk`, starlark, Kubernetes/Incus client libraries.
- **Frontend**: React 19.2 with **mixed TypeScript (`.ts`/`.tsx`) and JSX (`.jsx`)** (the app entry is `web/src/main.tsx`), Vite 7.2, Tailwind CSS 4.1, Vitest. `npm run build` runs `tsc --noEmit` before Vite.
- **Build System**: Make (`Makefile`, `Makefile.integration`). Pre-commit hook (`.githooks/pre-commit`) enforces Atlas migration integrity.
- **Sandbox / Isolation**: Incus (default, LXC), Kubernetes, OpenShell (gRPC gateway with Landlock/seccomp), plus an in-memory mock for tests.

## Build / Lint / Test Commands

### Go Backend
```bash
# Build everything (UI + Go binary)
make build-all

# Build Go binary only
make build

# Build React UI only
make build-ui

# Run Go application
go run .

# Run Astonish Studio (HTTP + SPA on :9393 by default)
make studio              # Production mode (serves embedded UI)
make studio-dev          # Dev mode (live UI reload on http://localhost:5173)

# Tests (tiered)
make test-unit           # Go + frontend unit tests (fast, no external deps)
make test-integration    # Integration tests (needs ASTONISH_TEST_DSN)
make test-e2e            # Full E2E (needs ASTONISH_TEST_DSN + provider API key + kubectl + helm)
make test-e2e-sqlite     # E2E in SQLite mode (no ASTONISH_TEST_DSN, still needs provider key + k8s sandbox)
make test-e2e-openshell  # E2E against the OpenShell sandbox backend
make test-e2e-inspect    # Leave a long-lived inspector server on :9394 after the run
make test-e2e-inspect-stop # Stop the inspector

# Run single test
go test ./pkg/tools -run TestFileTree
go test -v ./pkg/tools -run TestFileTree  # Verbose

# Lint
golangci-lint run        # Full lint check (bug-finders only; see .golangci.yml)
# Prefer: make lint      # config verify + run; version must match .golangci-version (CI)

# Atlas migrations (schema/*.sql, pkg/store/*/migrations)
make migrate-diff        # Generate a new migration by diffing ent schemas
```

Full e2e infra (docker-compose, k8s namespace bring-up, PVCs, inspector state) is documented in `tests/AGENTS.md`.

### React Frontend (in `web/` directory)
```bash
cd web

# Development server with hot reload
npm run dev              # http://localhost:5173

# Build for production (runs tsc --noEmit first)
npm run build

# Lint
npm run lint

# Unit tests (Vitest)
npm test
```

### Quick Reference
- Single test: `go test ./pkg/path -run TestFunctionName`
- Verbose test: `go test -v ./pkg/path -run TestFunctionName`
- Run specific package: `go test ./pkg/path`
- Run with race detector: `go test -race ./pkg/path`
- Rebuild the e2e-inspector when backend code changed: `make test-e2e-inspect` handles this automatically.

## Go Code Style

- **Imports**: stdlib → external → internal, with blank lines between groups.
- **Naming**: `PascalCase` for exports, `camelCase` for private, lowercase packages.
- **Tags**: `yaml` and `json` with `omitempty` for optional fields.
- **Errors**: return as last value, check immediately, wrap with `fmt.Errorf` when needed. Never suppress with `_`.
- **Interfaces**: minimal, defined **near use** (e.g., `RunnableTool`, `ToolWithDeclaration` in `pkg/tools`, `Backend` in `pkg/sandbox`, `Channel` in `pkg/channels`).
- **Testing**: `*_test.go` same package, table-driven tests, `os.MkdirTemp` with cleanup. Integration/e2e tests use build tags (`//go:build integration`, `//go:build e2e`).
- **Linting**: pre-commit runs `golangci-lint` (required; version must match `.golangci-version` / CI). Policy is in `.golangci.yml` and is **intentionally narrow**: only `govet`, `ineffassign`, `unused`, `staticcheck` (with ST*/S*/QF*/SA9003/SA1019 disabled), and `gosec` (with G101/G104/G117/G124/G301/G302 and related excludes). Do **not** re-enable style linters without discussion — the project deliberately prioritizes bug-finders. Always run `golangci-lint config verify` (or `make lint`) after editing excludes — unknown rule IDs break CI.
- **Comments**: avoid unless the code is complex or non-obvious.
- **Generated code**: never hand-edit `ent/*/*.go` except files under `ent/*/schema/`. See `ent/AGENTS.md`. The same applies to `pkg/sandbox/openshell/gen/openshellv1/*.pb.go` — regenerate from `proto/openshell/v1/*.proto`.

## React / TypeScript / JSX Code Style

- **Language**: The web app is **mixed TS and JSX**. New UI files should be **`.tsx`** unless they mirror an existing `.jsx` neighbor. The Vite/Vitest configs handle both; ESLint has separate blocks for `{js,jsx}` and `{ts,tsx}` in `web/eslint.config.js`.
- **Components**: Functional with hooks, single per file, `export default` for the main component (named exports for helpers).
- **Imports**: External first, local second. Named exports preferred for utilities. The web app supports the `@/*` alias for imports from `web/src`.
- **Styling**: Tailwind CSS v4 with semantic tokens in `web/src/index.css`. Standard application UI should prefer source-owned shadcn/ui primitives in `web/src/components/ui/*`; custom product surfaces such as Flow Canvas, Studio Chat rendering, embedded viewers, terminal, and the Apps iframe sandbox should be tokenized and restyled rather than blindly rewritten as generic shadcn UI.
- **State**: React hooks only — **no Redux/Zustand/Jotai/etc.** Props drilling is acceptable. Cross-cutting state uses React Context sparingly.
- **Handlers**: CamelCase, prevent default on forms, cleanup in `useEffect`.
- **Linting**: ESLint config in `web/eslint.config.js`; `varsIgnorePattern: '^[A-Z_]'` allows unused component-name imports.
- **Type-check gate**: `npm run build` runs `tsc --noEmit` — do not commit code that fails it.

## File Structure

```
astonish/
├── cmd/
│   ├── astonish/                       # Cobra CLI dispatch (root.go, chat.go, daemon.go, …)
│   └── astonish-sandbox-entrypoint-script/  # Generator for sandbox pod entrypoint
├── pkg/
│   ├── agent/            # Core ChatAgent runtime, tool-loop orchestration
│   ├── api/              # HTTP handlers, SSE chat runner, tenant middleware, image build endpoints
│   ├── apps/             # Generative UI (VisualApp, DataSource, versioning)
│   ├── browser/          # Chromium-based browser automation, handoff, CAPTCHA
│   ├── cache/            # Small in-memory caches (tools cache, etc.)
│   ├── channels/         # Slack / Telegram / Email adapters + routing + commands
│   ├── config/           # YAML config loading (LoadAgent, LoadAppConfig)
│   ├── credentials/      # Encrypted credential store, secret scanner, pending vault, OAuth
│   ├── daemon/           # Platform bootstrap: wires stores, scheduler, channels, fleet, Studio
│   ├── drill/            # Test/drill suite runner (SuiteRunner, TriageAgent, ArtifactManager)
│   ├── fleet/            # Multi-agent orchestration (FleetConfig, PlanRegistry, PlanActivator)
│   ├── launcher/         # Console + Studio entrypoints (RunChatConsole, NewStudioServer)
│   ├── mcp/              # MCP client and server management
│   ├── mcpstore/         # Tapped MCP server catalog
│   ├── memory/           # Three-tier memory (personal / team / org) + embeddings
│   ├── pdfgen/           # Markdown → PDF via goldmark-pdf and Chrome
│   ├── planner/          # ReAct planning loop for providers without native tool-calling
│   ├── provider/         # LLM provider factory (OpenAI, Anthropic, Gemini, …)
│   ├── sandbox/          # Backend interface + incus/k8s/openshell/mock + imagebuilder
│   ├── scheduler/        # Cron-like job scheduling and delivery
│   ├── session/          # SessionIndex, Transcript, FileStore, Compactor
│   ├── skills/           # SKILL.md loader, validator, ClawHub integration
│   ├── store/entstore/   # Multi-tenant DB router on top of Ent
│   ├── tools/            # Built-in tools implementing RunnableTool
│   └── ui/               # TUI components (bubbletea)
├── ent/
│   ├── platform/ …/schema/*.go        # Hand-edited schema
│   ├── org/     …/schema/*.go
│   ├── team/    …/schema/*.go
│   ├── personal/…/schema/*.go
│   └── */generate.go                   # `go generate ./…` entrypoints (do NOT edit generated output)
├── proto/openshell/v1/*.proto          # OpenShell gRPC contract (regen via `make …`, see pkg/sandbox/AGENTS.md)
├── web/
│   ├── src/
│   │   ├── main.tsx                    # React entry (NOT main.jsx)
│   │   ├── App.tsx
│   │   ├── components/                 # React components (mix of .tsx and .jsx)
│   │   └── api/                        # SPA API client
│   ├── package.json
│   ├── vite.config.ts
│   └── eslint.config.js
├── tests/
│   ├── e2e/                            # Grouped e2e packages (chat_core, chat_auth, drill, fleet, sandbox_layerchain, …)
│   ├── e2eboot/                        # Custom test harness (DB, seed, SSE, sandbox helpers, inspector)
│   └── scenarios/*.yaml, *.mjs         # Scenario catalog + Node reporters
├── tools/e2e-inspector/                # Long-lived StudioServer for post-run inspection (port 9394)
├── docs/architecture/                  # Authoritative architecture references (see below)
├── docker/sandbox-base/                # K8s sandbox base image
├── docker/sandbox-openshell/           # OpenShell sandbox image
├── deploy/{helm,k8s}/                  # Deployment manifests
├── schema/*.sql, pkg/store/*/migrations/*.sql   # Atlas migrations (integrity-checked by pre-commit)
├── .githooks/pre-commit                # Enforces migration integrity
├── .golangci.yml                       # Lint policy (bug-finders only)
├── Makefile / Makefile.integration
├── go.mod
└── main.go                             # Calls cmd/astonish.Execute()
```

## Key Patterns

### Config Loading (Go)
- User configs live in `~/.config/astonish/`.
- YAML with `gopkg.in/yaml.v3`.
- Use `config.LoadAgent()` for flow/agent configs, `config.LoadAppConfig()` for app settings.
- Provider env-var mapping is in `pkg/config/provider_env.go`; the provider factory is `pkg/provider/factory.go`.

### Tool Implementation (Go)
- Tools implement `RunnableTool.Run(ctx tool.Context, args any) (map[string]any, error)`.
- Declare the JSON schema with `Declaration() *genai.FunctionDeclaration`.
- Return `map[string]any` with string keys.
- Register in the appropriate group in `pkg/tools/` (see `pkg/tools/AGENTS.md`). Sandbox-aware tools are wrapped by `pkg/sandbox` so a single tool implementation works across backends.

### API Handlers (Go)
- Pattern: `func HandlerName(w http.ResponseWriter, r *http.Request)`.
- Set `Content-Type: application/json`.
- Use `json.NewEncoder(w).Encode(response)`.
- Error responses: `http.Error(w, "message", statusCode)`.
- Streaming chat / build logs use **SSE**. Chat SSE has strict invariants (see [Inline Report Rendering Contract](#inline-report-rendering-contract-do-not-loosen)).
- Tenant scoping is enforced by `TenantMiddleware` — see `pkg/api/AGENTS.md`. Every handler that touches per-tenant data must consume the tenant context, not bypass it.

### MCP Integration
- MCP servers are defined in config (personal, team, org — cascading).
- Tools are cached in `pkg/cache/tools_cache.go`.
- Use `GetCachedTools()` to retrieve available tools.
- Team-scoped MCP servers must remain isolated: **six enforcement points** — do not add code paths that read/write MCP configuration outside the tenant router.

### Sandbox Execution
- Every tool that touches the filesystem, network, or shell runs inside a **sandbox** via the `Backend` interface in `pkg/sandbox`.
- Backend selection: config `BackendKind` → factory in `pkg/sandbox/backend_factory.go`. Blank imports in `cmd/astonish/sandbox_backends.go` ensure implementations are linked.
- The OpenShell backend talks to the OpenShell Gateway via gRPC (`proto/openshell/v1/openshell.proto`). Landlock/seccomp/L7 network policy is enforced **inside the sandbox by the supervisor**, not by Go code — do not assume host-side checks are the whole story.
- Deeper contract, image build flow, and provisioning are in `pkg/sandbox/AGENTS.md`.

### Multi-Tenant Data Boundary
- Ent schemas are split into four scopes: `platform`, `org`, `team`, `personal` — each in `ent/<scope>/schema/*.go`.
- The router that picks the correct DB/schema is `pkg/store/entstore` (see `pkg/store/entstore/AGENTS.md`).
- **Never bypass `entstore`** by opening a raw connection or using an ent client from another scope. Isolation is structural (database-per-org, schema-per-team) — code paths that circumvent the router break audit and encryption guarantees.
- Migrations live under `schema/*.sql` and `pkg/store/*/migrations/*.sql`. The pre-commit hook validates Atlas integrity for envs `platform_{pg,lite}`, `org_{pg,lite}`, `team_{pg,lite}`, `personal_{pg,lite}` whenever those files change.

### Inline Report Rendering Contract (do NOT loosen)

A markdown artifact is promoted to inline `EmbeddedFileViewer` rendering iff **all three** signals are present:

1. The artifact event was emitted in the **last turn** (after the most recent user message).
2. The artifact's `fileType === 'Markdown'`.
3. The artifact's `isReport === true`, set only when the agent emitted an `` ```astonish-report `` fence whose `path:` matches the artifact's path. The backend's `detectAndEmitReportMarkers` (`pkg/api/chat_runner.go`) validates the path match and emits a `report_marker` SSE event; `joinReportMarkers` (`pkg/api/chat_utils.go`) projects the persisted marker onto `ArtifactInfo` at session-detail load time.

**Anything failing any one of these conditions falls back to the compact `ArtifactCard` download tile.** This is intentional. Do not "fix" code that produces an `ArtifactCard` for a non-report `write_file` by widening the gate. If you find yourself wanting to widen the gate, the agent prompt is the correct place to teach the LLM the two-step contract — not the gate.

The two prior regressions to avoid:
- `b5310ae`: widened the gate to "any last-turn artifact embeds" → incidental edits during a multi-step task were promoted to reports. Defended by `TestE2E_Chat_PlainWriteFileNotReport` (CHAT-066) and the system prompt contract test.
- `ee2d47d`: tried to make the fence carry inline content (no `write_file`) → broke Files panel, artifact API, PDF/DOCX export. Defended by keeping the fence as a *signal*; the file is always real.

Authoritative docs: `docs/architecture/chat-rendering-pipeline.md` ("The Report Pipeline" section).

## Testing Guidelines

- Write tests for non-trivial functions.
- Focus on core business logic (agent execution, tools, config, tenant routing, sandbox contracts).
- Mock external dependencies (API calls, file system). The `pkg/sandbox/mock` backend is the canonical way to exercise sandbox-consuming code in unit tests.
- Test file names: `*_test.go`; e2e/integration use build tags.
- Pre-commit lints but does **not** run tests. Run them yourself before pushing.
- See `docs/architecture/testing-chat-scenarios.md` for the Studio Chat scenario test infrastructure.
- E2E specifics (env vars, kubectl/helm prerequisites, provider keys, inspector mode) are in `tests/AGENTS.md`.

## Architecture Documentation

The `docs/architecture/` directory is the authoritative reference for cross-cutting design. Read the relevant file **before** modifying the corresponding subsystem:

- `docs/architecture/chat-rendering-pipeline.md` — Studio Chat SSE transport, event types, message-to-component mapping, report/app/artifact pipelines, export pipeline. **Read this before modifying `web/src/components/StudioChat.tsx` or adding new SSE event types.**
- `docs/architecture/testing-chat-scenarios.md` — Scenario test infrastructure, fixture authoring.
- `docs/architecture/generative-ui.md` — App preview (Generative UI) pipeline.
- `docs/architecture/api-studio.md` — REST API and SSE streaming surface.
- `docs/architecture/multi-tenant-platform.md` — Org/team/personal isolation, envelope encryption, six enforcement points.
- `docs/architecture/sandbox-backends.md` — In-depth backend comparison, resource lifecycle.
- `docs/architecture/openshell-sandbox-backend.md` — OpenShell-specific gRPC + Landlock/seccomp details.
- `docs/architecture/sqlite-backend.md` — Personal-mode SQLite topology.
- `docs/architecture/smart-compaction.md` — Session compaction algorithm.

An index of every architecture doc plus the invariants it defends is in `docs/architecture/AGENTS.md`.

## Hierarchical AGENTS.md Index

When you edit files under one of these subtrees, read the local AGENTS.md first — it names the invariants and gotchas the root file cannot cover:

- `cmd/astonish/AGENTS.md` — CLI dispatch (Cobra), local vs. remote gating, `mustBeRemote` / `mustNotBeRemote`.
- `pkg/api/AGENTS.md` — HTTP/SSE, chat runner, tenant middleware, image build handlers, report marker plumbing.
- `pkg/agent/AGENTS.md` + `pkg/launcher/AGENTS.md` — ChatAgent, `RunChatConsole`, `NewWiredChatAgent`, `NewStudioServer`.
- `pkg/sandbox/AGENTS.md` — Backend interface, backend selection, gRPC contract, image build, entrypoint, isolation model.
- `pkg/store/entstore/AGENTS.md` — Multi-tenant DB router, migration policy.
- `ent/AGENTS.md` — Schema hand-edit rules, four tenant scopes, regeneration flow.
- `pkg/tools/AGENTS.md` — `RunnableTool`, `ToolWithDeclaration`, sandbox wrapping.
- `pkg/fleet/AGENTS.md` — Fleet plans, activator, session manager, GitHub channel adapter.
- `pkg/daemon/AGENTS.md` — Platform bootstrap, wiring boundary, `MultiTenantScheduler`.
- `pkg/drill/AGENTS.md` — Drill suite runner, triage, LLM assertions.
- `pkg/channels/AGENTS.md` — Channel interface, Slack/Telegram/Email adapters, routing.
- `pkg/credentials/AGENTS.md` — Encrypted store, secret scanner, pending vault, OAuth cache.
- `pkg/session/AGENTS.md` — Session index, transcripts, compaction.
- `pkg/skills/AGENTS.md` — Skill loader, validator, ClawHub.
- `pkg/browser/AGENTS.md` — Browser manager, handoff, CAPTCHA, accounts.
- `web/src/AGENTS.md` — React/TS+JSX conventions, StudioChat invariants, App preview pipeline.
- `tests/AGENTS.md` — e2eboot harness, `ASTONISH_TEST_DSN`, provider keys, inspector, scenarios.
- `docs/architecture/AGENTS.md` — Architecture doc index + invariant registry.

If you land in a directory without a local AGENTS.md, walk **upward** to the nearest one.

## Before Committing

Always run linting:
```bash
# Go linting (automatic via pre-commit hook)
make build-all

# Manual Go lint
golangci-lint run

# Web linting
cd web && npm run lint
```

If you modified schema or migrations, the pre-commit hook will run `atlas migrate hash --env <env>` for each affected env — do not skip it.

Skip the lint check only when absolutely necessary:
```bash
git commit --no-verify
```

# OpenCode Directives

CRITICAL: Whenever you process a request that involves writing or modifying code, you must execute the following steps before considering the task complete:

1. **Unit Testing:** Always analyze the changes you made and create or modify the corresponding unit tests to ensure the new code is fully covered.
2. **Documentation Review:** Always check the `docs/` folder. You **MUST update the architecture documentation and user documentation** if they are relevant to the changes you just implemented.
