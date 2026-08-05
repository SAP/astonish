# pkg/agent — AGENTS.md

Core `ChatAgent` runtime — the tool-use loop that drives Astonish's autonomous chat. Everything that runs "one turn" of the agent goes through here.

## Scope
- `ChatAgent` construction and step execution.
- Prompt building (system prompt, tool descriptions, session context).
- Tool-call orchestration: model → tool call → tool result → model, with streaming.
- Sub-agent delegation (a `ChatAgent` may spawn up to 10 sub-agents with filtered tool access and isolated sessions).

## Interactions
- Wired by `pkg/launcher/chat_factory.go:NewWiredChatAgent` — this is where the full agent (LLM, tools, sandbox, memory, tool index, prompt builder) is assembled.
- Invoked by Studio via `pkg/api` chat handlers (SSE). `astonish chat` streams Studio SSE through `pkg/launcher.RunChatTUI`. **`astonish code` runs this same agent in-process** via `pkg/launcher.RunCodeTUI` (no platform).
- Runs tools via `RunnableTool.Run` (see `pkg/tools/AGENTS.md`); tools that hit the shell/network/filesystem are wrapped by `pkg/sandbox` (see `pkg/sandbox/AGENTS.md`).
- `project_context.go:LoadProjectContext` implements the [agents.md](https://agents.md) convention: it discovers `AGENTS.md` (fallback `CLAUDE.md`) by walking upward from the working dir to the git root, merges them nearest-last, and the factory injects the result into `SystemPromptBuilder.ProjectContext` (rendered as `## Project Guidance`). Only code mode enables this (via `ChatFactoryConfig.LoadProjectContext`); platform uses per-team DB instructions.

## Key rules
1. **The agent must not read config directly** — it receives its configuration from the factory. Keeps testing tractable.
2. **Streaming semantics**: partial model output is streamed as text events; tool calls are emitted as discrete events. Do not batch — Studio Chat relies on incremental delivery.
3. **Tool-call safety**: never execute a tool call without going through the `Backend`-wrapped path when the tool is sandbox-scoped.
4. **Sub-agent budget**: max 10 concurrent sub-agents by design (see the README and `docs/architecture/`). Do not raise this without discussion — it bounds fan-out cost.

## When editing
- Changing the tool-call loop? Update the Studio SSE runner (`pkg/api/chat_runner.go`). CLI chat consumes the same SSE events via `pkg/launcher/tui_chat.go` → `pkg/tui`.
- Changing prompt construction? Coordinate with the system-prompt contract tests (they enforce the two-step artifact/report protocol).
