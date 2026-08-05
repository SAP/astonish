# pkg/tools — AGENTS.md

Built-in tool implementations. Every tool the LLM can call is either here or supplied dynamically by an MCP server (see `pkg/mcp`).

## The contract
A tool implements:

```go
type RunnableTool interface {
    Run(ctx tool.Context, args any) (map[string]any, error)
}

type ToolWithDeclaration interface {
    RunnableTool
    Declaration() *genai.FunctionDeclaration
}
```

- `Run` returns `map[string]any` (string keys) — Studio Chat and the CLI both consume this shape.
- `Declaration` is the JSON schema exposed to the LLM.

## Sandbox wrapping
Tools that touch the filesystem, network, or shell **must** be executable via `pkg/sandbox.Backend`. Do not spawn processes with `os/exec` directly — the sandbox wrapper adapts the same tool implementation to Incus / K8s / OpenShell / Mock.

## Categories
- File / grep / tree (used heavily by the agent for repo understanding).
- Shell (PTY-backed via the sandbox).
- Web fetch / PDF read.
- Memory (semantic search across personal / team / org tiers).
- Browser (delegates to `pkg/browser`).
- Credentials / secrets (delegates to `pkg/credentials`).
- Sub-agent delegation.
- Skill lookup.

Full list: 58+ built-in tools — see the README.

## Context-efficiency invariants
- **`read_file` soft-caps unbounded reads.** When called with no `Limit`, a large file (> ~`readFileSoftCapLines` lines) returns only the first window, and `Range` carries a paging notice. Reading whole large files repeatedly is the dominant cause of context bloat (→ slow inference) in agentic loops; an explicit `Limit`/`Offset` always wins. Do not remove this cap; if you raise it, keep the paging notice.
- **Prefer structural navigation.** `code_definition` / `code_references` / `repo_map` (tree-sitter, `pkg/codeintel`) resolve a symbol in one call instead of broad `grep_search` + repeated `read_file`. They are on the main-thread allowlist (`pkg/launcher/chat_factory.go`). See `docs/architecture/code-intelligence.md`.
- **Must-read-before-edit guard counts read/edit/write as "seen."** `edit_file` blocks edits to a file the agent has never observed (anti-hallucination), via `FileReadCache.HasSeenEntry`. A prior `read_file`, `edit_file`, OR `write_file` all satisfy it — a successful edit/write leaves the agent knowing the current content, so **consecutive edits to the same file must not be blocked**. Do not narrow this back to read-only (that regression forced needless re-reads). Staleness (real external changes) is caught separately: `edit_file` always re-reads current disk content and exact-matches `old_string`.

## When editing
1. Adding a new tool? Implement `ToolWithDeclaration`, register it in the appropriate group, and — if it hits shell/network/fs — verify it runs through the sandbox path.
2. Changing a tool's schema? Coordinate with prompt tests and the tools cache (`pkg/cache/tools_cache.go`).
3. Never bypass credentials/secret scanning — sensitive-looking arguments must flow through `pkg/credentials` scanning where applicable.
