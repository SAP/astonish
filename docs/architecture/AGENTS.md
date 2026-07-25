# docs/architecture — AGENTS.md

This directory is the **authoritative reference** for cross-cutting design decisions. Whenever the code and an architecture doc disagree, either the code is a bug **or** the doc needs an update — never silently diverge. Doc changes accompany the code change in the same commit.

## Index

### Chat rendering
- `chat-rendering-pipeline.md` — SSE transport, event types, message-to-component mapping, report/app/artifact pipelines, export pipeline. **Owns the three-signal report gate invariant** defended by `pkg/api/chat_runner.go`, `pkg/api/chat_utils.go`, and `web/src/components/StudioChat.tsx`.
- `testing-chat-scenarios.md` — scenario test infrastructure, fixture authoring, mapping between backend SSE events and expected UI outcomes.

### Multi-tenant platform
- `multi-tenant-platform.md` — org/team/personal isolation model, envelope encryption, six enforcement points, cascading defaults.
- `sqlite-backend.md` — personal-mode SQLite topology.

### Sandbox
- `sandbox-backends.md` — Incus vs. K8s vs. OpenShell vs. Mock: capabilities, lifecycle, template model.
- `openshell-sandbox-backend.md` — OpenShell gRPC gateway, supervisor, Landlock/seccomp, L7 network policy.

### API + Generative UI
- `api-studio.md` — REST + SSE surface reference.
- `generative-ui.md` — App preview pipeline, iframe sandbox, `useAppData` / `useAppAI` / `useAppState`, SSRF-protected proxy.
- `studio-ui-system.md` — Studio design system, shadcn/ui usage, theme tokens, and custom-surface boundaries for Flow Canvas, Studio Chat, embedded viewers, terminal, and app sandbox.

### Code Intelligence
- `code-intelligence.md` - Tree-sitter-first structural code intelligence. Scope graphs, reference graph, PageRank, sandbox-native execution. LSP is deferred pending observed need. **Status: implemented** (`pkg/codeintel`, sandbox packaging per backend).

### Session behavior
- `smart-compaction.md` — session compaction algorithm.

## Rules for this directory
1. **These docs are versioned invariants, not tutorials.** Keep them precise, terse, and code-adjacent (reference file paths, function names, and PR/commit hashes when useful).
2. **A code change that alters a documented invariant must update the doc in the same commit.**
3. **New cross-cutting design?** Add a new file here rather than burying it in a package README.
4. **Do not delete a doc when its subject is removed** — mark it as historical with a header note. The regression story in the root `AGENTS.md` (commits `b5310ae`, `ee2d47d`) is the pattern.
