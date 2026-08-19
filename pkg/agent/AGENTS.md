# pkg/agent — AGENTS.md

Core `ChatAgent` runtime: prompt assembly, model/tool loop, streaming, compaction, approvals, and sub-agent delegation.

## Interactions
- `pkg/launcher/chat_factory.go` wires LLMs, tools, sandbox, memory, MCP, prompts, and stores. The agent receives configuration; it must not load host config itself.
- Studio invokes the agent through `pkg/api` SSE handlers. Remote chat maps those events into `pkg/tui`; local code mode runs the same agent in-process.
- Tool execution follows `pkg/tools/AGENTS.md`; sandbox-scoped operations follow `pkg/sandbox/AGENTS.md`.
- `LoadProjectContext` discovers and merges `AGENTS.md`/fallback `CLAUDE.md` nearest-last for code mode only. Platform instructions remain store-backed.

## Invariants
1. **Streaming is incremental.** Partial text remains partial; tool calls/results and approval boundaries are discrete. Do not batch away ordering required by Studio/TUI reducers.
2. **Tool safety is callback-enforced.** Preserve credential/secret callbacks, code-mode folder/tool authorization, plan/ask gates, and sandbox wrapping order.
3. **Folder and command path checks share `pkg/pathscope`.** Do not create divergent containment logic. Interactive grants belong to `SessionAuthPolicy`; non-interactive shell guards remain grant-blind.
4. **Authorization scope matters.** One-shot grants are consumed, session grants survive turn resets, and `--auto-approve` deliberately bypasses interactive prompts.
5. **Sub-agent fan-out is bounded at 10.** Delegates use isolated sessions and filtered tools; do not raise the limit or merge their state implicitly.
6. Prompt/report/app protocols are contracts with backend marker generation and frontend consumers, not presentation-only prose.

## When editing
- Tool-loop or event changes require matching `pkg/api/chat_runner.go`, remote TUI mapping, Studio handling, docs, and scenarios.
- Prompt changes require system-prompt contract tests and must preserve hidden runtime guidance versus visible user text.
- Model, config, memory, or MCP changes follow [`pkg/provider/AGENTS.md`](../provider/AGENTS.md), [`pkg/config/AGENTS.md`](../config/AGENTS.md), [`pkg/memory/AGENTS.md`](../memory/AGENTS.md), and [`pkg/mcp/AGENTS.md`](../mcp/AGENTS.md).

## Verification

Run `go test ./pkg/agent/...`. For stream-visible changes also test `./pkg/api/...`, `./pkg/launcher/...`, and affected Studio scenarios.
