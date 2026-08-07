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
- `tool_authorization.go:SessionAuthPolicy` holds the **code-mode** authorization state (one per session, on the `ChatAgent`). Two `BeforeToolCallbacks` in `chat_agent_run.go` enforce it when `ChatAgent.EnforceAuthorization` is set: a **tool-execution gate** (Normal mode reuses `agent.SafeTools` as an auto-allow baseline; non-whitelisted tools prompt for authorization) and a **folder-access gate** (paths outside `ChatAgent.WorkingDir` prompt). The folder gate's `OutOfScopePaths` inspects structured path args **and** free-form command strings (`shell_command`'s `command`) via `pathscope.ExtractCommandPaths` (quote-aware — quoted literal data like a commit message is not mis-flagged as a path operand), so a shell command touching `/`, `~/…`, or `../…` outside the root is caught. All path primitives (`NormalizePath`, `PathWithin`, `ExtractCommandPaths`, …) live in **`pkg/pathscope`** — the single source of truth shared with the `pkg/tools` shell guard (`tools.SetScopeRoot` + `ShellCommand`'s runtime reject), so the interactive gate and the non-interactive guard use identical containment logic. The `tools.SetScopeRoot` guard is **grant-blind** and is NOT engaged in the interactive code-mode launcher — the folder-access gate here is the authoritative *grant-aware* enforcer (approving an out-of-scope path records a grant so the retry succeeds); the shell guard is reserved for genuinely non-interactive callers (planner/scheduler). To avoid a double prompt when a not-whitelisted tool ALSO touches an out-of-scope path, granting the folder for that call also records a one-shot tool grant (`ApplyAuthorizationDecision`), so a single approval covers both gates for that one retry. Both callbacks run after the credential/secret callbacks and before the Plan-mode gate, and are bypassed by `--auto-approve`. Grants: tool once (consumed) / all-tools **session-scoped** (`GrantAllToolsSession`, what the tool gate's "Always Allow" records — survives `ResetForNewTurn` so the user isn't re-asked) plus an iteration-only `GrantAllToolsThisIteration`; folder once / for-session. The policy is also seeded with Astonish's own state dir (`config.GetConfigDir()`) as an implicit `allowedRoots` entry, so writing session transcripts / `PLAN.md` outside the project never prompts. See `docs/architecture/terminal-app.md#tool--folder-authorization-code-mode`.

## Key rules
1. **The agent must not read config directly** — it receives its configuration from the factory. Keeps testing tractable.
2. **Streaming semantics**: partial model output is streamed as text events; tool calls are emitted as discrete events. Do not batch — Studio Chat relies on incremental delivery.
3. **Tool-call safety**: never execute a tool call without going through the `Backend`-wrapped path when the tool is sandbox-scoped.
4. **Sub-agent budget**: max 10 concurrent sub-agents by design (see the README and `docs/architecture/`). Do not raise this without discussion — it bounds fan-out cost.

## When editing
- Changing the tool-call loop? Update the Studio SSE runner (`pkg/api/chat_runner.go`). CLI chat consumes the same SSE events via `pkg/launcher/tui_chat.go` → `pkg/tui`.
- Changing prompt construction? Coordinate with the system-prompt contract tests (they enforce the two-step artifact/report protocol).
