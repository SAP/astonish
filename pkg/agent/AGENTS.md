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
5. **Sub-agent fan-out is bounded at 10.** Delegates use isolated sessions and scoped tools; do not raise the limit or merge their state implicitly. Public tool preferences are not authorization boundaries (see the delegation contract below).
6. Prompt/report/app protocols are contracts with backend marker generation and frontend consumers, not presentation-only prose.

## When editing
- Tool-loop or event changes require matching `pkg/api/chat_runner.go`, remote TUI mapping, Studio handling, docs, and scenarios.
- Prompt changes require system-prompt contract tests and must preserve hidden runtime guidance versus visible user text.
- Code-mode investigation approach (Work Policy, Live Evidence, `debug-regression` / `inspect-live-surface` / `verify-live-with-drill` / `watch-long-running`, follow-up injector, `update_plan` step names) is documented in [`docs/architecture/code-agent-investigation.md`](../../docs/architecture/code-agent-investigation.md). Plan completion is structural: every phase complete (each gated by its own `verify`) plus `announce_completion` writing `## Results` from the recorded phase evidence. `announce_completion` does not execute plan-level commands; do not reintroduce that gate, and do not restore `CompleteAll` as a bulk completer. Integration coverage is enforced at announce time by requiring a `verify_kind=behavior` phase for running-surface plans. Do not weaken those tests to hide a prompt or tool-contract change. Studio must keep the short Live Evidence block; do not move it back to vector-only guidance.
- Model, config, memory, or MCP changes follow [`pkg/provider/AGENTS.md`](../provider/AGENTS.md), [`pkg/config/AGENTS.md`](../config/AGENTS.md), [`pkg/memory/AGENTS.md`](../memory/AGENTS.md), and [`pkg/mcp/AGENTS.md`](../mcp/AGENTS.md).

## Delegation tool contract

- Public `delegate_tasks.tasks[].tools` entries are initial tool/group preferences, not a strict allowlist or a permission grant. Omitted, `null`, and empty lists request auto-selection. `buildChildPrompt()` must frame preferred tools as the starting set and permit discovery of other authorized capabilities when needed; it must not describe preferences as the child's only available tools.
- Internal default filters and explicit override tools/toolsets remain strict boundaries. Preferences cannot widen them, and filters intersect override instances. A qualified `mcp:server/tool` preference selects only that tool initially, never its entire server; other discovery remains subject to the effective authorized catalog. The web group adds only the configured request-local search provider.
- `ChildToolScope` freezes authorized metadata and preserves exact executable instances, including sandbox and authorization wrappers. Lazy search/description must not initialize unrelated ambient toolsets or reconstruct executables by name. Search, describe, and execute share the same scope; helpers and initial declarations are deduplicated, and discovery never grows provider declarations.
- Disabled-tool rules, effective MCP access, mode gates, folder authorization, and approvals remain independent permission checks. Neither a named preference nor auto-selection bypasses them. Preserve logical-target callbacks for credential substitution/restoration, redaction, authorization, and artifact capture.
- Keep the public `delegate_tasks` JSON shape and the existing scope-based `SearchToolsFactory` signature unchanged. See [`agent-engine.md`](../../docs/architecture/agent-engine.md) and [`tools.md`](../../docs/architecture/tools.md) for the architecture contract.

## Verification

Run `go test ./pkg/agent/...`. Prompt changes require `go test ./pkg/agent -run 'TestSubAgentManager_BuildChildPrompt' -count=1`. For stream-visible changes also test `./pkg/api/...`, `./pkg/launcher/...`, and affected Studio scenarios.

Delegation acceptance must exercise one public preference followed by search, describe, and execution of another authorized tool; repeat with omitted, `null`, and empty preferences. Also prove strict internal/override exclusion, qualified MCP selection, retained wrapper identity, lazy metadata discovery, and fixed provider declarations. The local scripted commands are `go test ./pkg/agent ./pkg/tools ./pkg/launcher -count=1` and `go test ./pkg/api -run '^TestChildDiscoveryHTTPAcceptance$' -count=1 -timeout=3m -v`. These commands document intended acceptance, not evidence that it ran: record actual results separately. The loopback HTTP harness uses a scripted child and does not establish paid-provider judgment, real remote MCP access, or live sandbox/container behavior. Wrapper identity, strict override boundaries, and lazy initialization require focused local tests.
