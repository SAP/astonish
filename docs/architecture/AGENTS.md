# docs/architecture — AGENTS.md

This directory is the **authoritative reference** for cross-cutting design decisions. Whenever the code and an architecture doc disagree, either the code is a bug **or** the doc needs an update — never silently diverge. Doc changes accompany the code change in the same commit.

## Index

### Chat rendering
- `chat-rendering-pipeline.md` — SSE transport, event types, message-to-component mapping, report/app/artifact pipelines, export pipeline. **Owns the three-signal report gate invariant** defended by `pkg/api/chat_runner.go`, `pkg/api/chat_utils.go`, and `web/src/components/StudioChat.tsx`.
- `testing-chat-scenarios.md` — scenario test infrastructure, fixture authoring, mapping between backend SSE events and expected UI outcomes.
- `terminal-app.md` — fullscreen terminal chat app (`pkg/tui`): event model, backends (platform `chat` and in-process `code` mode), transcript reducer, CLI entry. Companion to Studio pipeline for CLI UX.
- `remote-cli-client.md` — remote mode auth/SSE client; TUI dual-mode section points at `terminal-app.md`.

### Multi-tenant platform
- `multi-tenant-platform.md` — org/team/personal isolation model, envelope encryption, six enforcement points, cascading defaults.
- `sqlite-backend.md` — personal-mode SQLite topology.

### Sandbox
- `sandbox-backends.md` — Incus vs. K8s vs. OpenShell vs. Mock: capabilities, lifecycle, template model.
- `openshell-sandbox-backend.md` — OpenShell gRPC gateway, supervisor, Landlock/seccomp, L7 network policy.

### API + Generative UI
- `api-studio.md` — REST + SSE surface reference.
- `generative-ui.md` — App preview pipeline, iframe sandbox, `useAppData` / `useAppAI` / `useAppState`, SSRF-protected proxy.
- `studio-ui-system.md` — Studio design system: dual-axis brand packs (mode × `data-theme`), shadcn vs custom surfaces, token rules (no hard-coded brand colors), preference cascade, App Canvas, and Flow/Chat/terminal boundaries.

### Code Intelligence
- `code-intelligence.md` - Tree-sitter-first structural code intelligence. Scope graphs, reference graph, PageRank, sandbox-native execution. LSP is deferred pending observed need. **Status: implemented** (`pkg/codeintel`, sandbox packaging per backend).

### Channels & Protocols
- `channels.md` — External channel architecture: Telegram, Slack, Email adapters, routing, commands, fleet integration.
- `a2a-server.md` — A2A (Agent-to-Agent) protocol server implementation as a channel adapter. Discovery (Agent Card), task lifecycle, streaming, push notifications, multi-tenant mapping.
- `a2a-client.md` — A2A Client: Astonish calling external A2A agents. Configuration cascade, credential integration, skill-to-tool mapping, streaming, multi-tenant isolation.
- `a2a-server-research.md` — Research findings and architectural decision record for A2A integration.

### Session behavior
- `smart-compaction.md` — session compaction algorithm.
- `cache-diagnostics.md` — superadmin-only request stability and cache observability, including capture safety and persistence.

## Package implementation guides

Use the nearest package guide for implementation contracts: [`pkg/config`](../../pkg/config/AGENTS.md), [`pkg/mcp`](../../pkg/mcp/AGENTS.md), [`pkg/memory`](../../pkg/memory/AGENTS.md), [`pkg/provider`](../../pkg/provider/AGENTS.md), [`pkg/scheduler`](../../pkg/scheduler/AGENTS.md), [`pkg/store`](../../pkg/store/AGENTS.md), [`web/src/api`](../../web/src/api/AGENTS.md), and [`web/src/components/chat`](../../web/src/components/chat/AGENTS.md). Architecture documents remain authoritative for cross-cutting invariants.

## Rules for this directory
1. **These docs are versioned invariants, not tutorials.** Keep them precise, terse, and code-adjacent (reference file paths, function names, and PR/commit hashes when useful).
2. **A code change that alters a documented invariant must update the doc in the same commit.**
3. **New cross-cutting design?** Add a new file here rather than burying it in a package README.
4. **Do not delete a doc when its subject is removed** — mark it as historical with a header note. The regression story in the root `AGENTS.md` (commits `b5310ae`, `ee2d47d`) is the pattern.
