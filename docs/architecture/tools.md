# Tool System

## Overview

Astonish provides 60+ built-in tools that give the AI agent the ability to interact with the filesystem, execute commands, manage processes, make HTTP requests, control a browser, send emails, and more. Tools follow a consistent architecture based on Google ADK's tool interface, with a key innovation: most tools execute inside sandbox containers via a transparent proxy layer.

## Key Design Decisions

### Why the ADK FunctionTool Pattern

Every tool follows the same structure:

1. Define an `Args` struct with `jsonschema` tags (these generate the JSON Schema that tells the LLM what parameters the tool accepts).
2. Define a `Result` struct.
3. Implement a handler function: `func(tool.Context, Args) (Result, error)`.
4. Wrap with `functiontool.New(functiontool.Config{Name, Description}, handler)`.

This provides strong typing, automatic schema generation, and a uniform interface that the sandbox proxy and callback systems can work with generically.

### Why Transparent Sandbox Proxying

Tools that modify the filesystem or execute commands run inside sandbox containers, but the tool implementation doesn't know this. The `NodeTool` wrapper intercepts `Run()` calls and forwards them to the `astonish node` process inside the container via NDJSON. From the tool's perspective, it runs locally -- the proxying is invisible.

A whitelist (`containerTools`) determines which tools are proxied:

```
read_file, load_file, write_file, edit_file, file_tree, grep_search, find_files,
shell_command, process_read, process_write, process_list, process_kill,
http_request, web_fetch, read_pdf, filter_json, git_diff_add_line_numbers
```

Host-side tools (memory, credentials, scheduler, email) are NOT proxied -- they need access to host resources. Browser tools also stay on the host process, but when sandbox is enabled they drive **in-container** Chromium via CDP (not host Chrome).

### Why Dependency Injection via Package-Level Variables

Tools need access to shared services (credential store, sub-agent manager, flow registry, fleet registry, scheduler) but importing those packages from `pkg/tools` would create circular dependencies. Instead, package-level variables with setter functions break the cycle:

```go
var credentialStoreVar *credentials.Store
func SetCredentialStore(s *credentials.Store) { credentialStoreVar = s }
```

The launcher wires everything together at startup.

### Why Protected Files

Certain files are blocked from tool access to prevent the agent from reading or modifying security-critical data:

- `.store_key` -- the encryption key for the credential store
- `credentials.enc` -- the encrypted credentials

Both `read_file`, `write_file`, and `shell_command` resolve paths (including symlinks) and reject access to these files.

### Why SSRF Prevention

Both `http_request` and `web_fetch` check resolved DNS addresses against private IP ranges (RFC1918, loopback, link-local). The `http_request` tool runs inside the container (where it CAN reach the container's bridge network), but the SSRF check prevents it from reaching the host's private network. Browser navigation has a separate SSRF guard that is disabled in sandbox mode (services on private bridge IPs need to be reachable).

## Architecture

### Tool Categories

| Category | Tools | Location |
|---|---|---|
| **File Operations** | `read_file`, `write_file`, `edit_file`, `file_tree`, `grep_search`, `find_files`, `read_pdf`, `filter_json` | `pkg/tools/` |
| **Shell & Process** | `shell_command`, `process_read`, `process_write`, `process_list`, `process_kill` | `pkg/tools/` |
| **Git** | `git_diff_add_line_numbers` | `pkg/tools/` |
| **HTTP** | `http_request`, `web_fetch` | `pkg/tools/` |
| **Credentials** | `save_credential`, `list_credentials`, `remove_credential`, `test_credential`, `resolve_credential` | `pkg/tools/credential_tool.go` |
| **Memory** | `memory_save`, `memory_search`, `memory_get`, `memory_delete` | `pkg/tools/memory_*.go` |
| **Delegation** | `delegate_tasks` | `pkg/tools/delegate_tool.go` |
| **Flows** | `search_flows`, `run_flow`, `distill_flow` | `pkg/tools/` |
| **Scheduler** | `schedule_job`, `list_scheduled_jobs`, `remove_scheduled_job`, `update_scheduled_job` | `pkg/tools/scheduler_tool.go` |
| **Browser** (35+ tools) | `browser_navigate`, `browser_click`, `browser_type`, `browser_snapshot`, `browser_take_screenshot`, etc. | `pkg/tools/browser_*.go` |
| **Email** | `email_list`, `email_read`, `email_search`, `email_send`, `email_reply`, `email_mark_read`, `email_delete`, `email_wait` | `pkg/tools/email_*.go` |
| **Drills** | `save_drill`, `validate_drill`, `delete_drill`, `list_drills`, `read_drill`, `edit_drill`, `inject_drill_credentials`, `run_drill` | `pkg/tools/drill_tool.go`, `inject_drill_credentials_tool.go`, `run_drill_tool.go` |
| **Fleet** | Fleet plan and task tools | `pkg/tools/fleet_*.go` |
| **Templates** | `save_sandbox_template`, `list_sandbox_templates`, `use_sandbox_template` | `pkg/tools/` |
| **Discovery** | `search_tools`, `skill_lookup` | `pkg/tools/` |

### Sandbox Proxy Flow

```
LLM requests tool call: read_file(path="/etc/hosts")
    |
    v
NodeTool.Run():
  1. Serialize args as NDJSON: {"id":"1", "tool":"read_file", "args":{"path":"/etc/hosts"}}
  2. Send to astonish node inside container via stdin
  3. Read response from stdout: {"id":"1", "result":{"content":"..."}}
  4. Deserialize and return result
```

The `NodeTool` also implements `ProcessRequest()` which eagerly triggers container creation before the LLM call completes, so the container is ready by the time the first tool call arrives.

### Reading Versus File Publication

Reading a file into the model and publishing a file to the user are two distinct jobs, served by two distinct tools:

- `read_file` returns line-numbered content to the model, with range and byte caps. A large file read without an explicit `limit` is auto-capped to a bounded window so whole-file reads do not bloat the model context; callers page with `offset`/`limit` or jump with `grep_search`/`code_definition`. Use this when the model must inspect or reason about the bytes. `read_file` never publishes to the Files section.
- `load_file` validates the complete regular file but returns only its path, optional title, byte size, and line count. The agent publishes that path through the existing artifact event, so Studio adds it to the Files section and fetches bytes directly through the tenant-authorized artifact-content endpoint only when the user opens it. File content is not included in the tool result, subsequent model context, chat-event payload, or restored tool summary. Use this to surface a report, log, or generated file to the user.

`load_file` does not accept `offset` or `limit`, because it publishes the complete file rather than a model-readable slice. Direct and progressively discovered execution share the same artifact capture, and restored session projection recognizes the persisted `load_file` call and reconstructs the identical Files-section artifact. An omitted `path` fails with explicit guidance; this prevents an unknown JSON field from silently decoding into an empty filesystem path.

### Canonical File Arguments and Direct Schemas

`read_file`, `write_file`, `edit_file`, and `load_file` advertise the same required `path` argument. Their full schemas are directly visible in both Studio/remote chat and Code mode, so these four tools do not need `describe_tools` before use. Other deferred tools retain the describe/execute discovery flow. Direct visibility does not bypass sandbox routing, tenant checks, approvals, or disabled-tool filtering.

For compatibility with existing calls and saved flows, the file tools also accept `file_path` at runtime, but never advertise it as a second parameter. `common.NormalizeFileToolArguments` copies the input and translates it to `path` before decoding; matching aliases are accepted, conflicting aliases or non-string paths are rejected without choosing a target. The original argument map remains available to authorization and credential callbacks. File-tool wrappers retain their own native request registration so native, deferred, and node dispatch all use compatibility decoding. `common.FileToolPath` applies the same read-only interpretation to live artifacts, restored artifacts, and content recovery without rewriting historical events.

### Durable Growing-File Generation

Large or uncertain-size Markdown documents use the destination file itself as the durable checkpoint. There is no separate artifact job:

1. Choose a unique HTML comment marker, for example `<!-- astonish-continue:7f3a9c -->`. The marker must not occur in the document content.
2. Make one bounded `write_file` call containing the document opening, the first semantic section, and the marker on its own line.
3. For every subsequent bounded step, call `edit_file` and replace **exactly that marker** with one complete semantic section followed by the identical marker. Do not rewrite the whole document and do not remove or alter the marker while more content remains.
4. To resume after compaction, interruption, or handoff, read or inspect the destination file, locate the marker, and inspect the surrounding tail. The material before it is the committed checkpoint; continue with the next not-yet-written semantic section.
5. After the last section is present, make one final `edit_file` call that removes the marker. Only then emit the existing `astonish-report` fence that names the completed Markdown file; the established report publication path remains unchanged.

The growing destination is intentionally visible throughout generation. A partial file is valid, readable, downloadable through the normal authorized file path, and useful even if generation stops. Completion is represented only by absence of the continuation marker and the subsequent existing report fence.

Retries are **not automatically idempotent**. If a `write_file` or `edit_file` result is missing, interrupted, or otherwise uncertain, inspect the file's marker and tail before retrying; a blind retry can duplicate a section. Use one semantic section per edit so this inspection remains straightforward.

This architecture has no expected section count, chunk ledger, generation ID, content hash, manifest, status service, or final assembly step. Durability and recovery are exactly the bytes already present in the destination Markdown file. Existing path authorization and confinement for `write_file`, `edit_file`, and file reads continue to apply; this protocol does not introduce another storage or retrieval boundary.

### Shell Command Architecture

The `shell_command` tool is one of the most complex:

- Uses PTY (pseudo-terminal) via `creack/pty` for realistic shell behavior.
- Output is captured in a 64KB `RingBuffer` with ANSI escape code stripping.
- **Idle detection**: If the command produces no output for 3 seconds and hasn't exited, it's considered idle.
- **Prompt detection**: Heuristics detect shell prompts to determine when interactive commands are waiting for input (`waiting_for_input=true` + `session_id`; the agent responds with `process_write`).
- **Auto-pager disabled**: because a PTY looks like a terminal, git/less/most CLIs would launch a pager and block waiting for keypresses. The child env sets `PAGER=cat`, `GIT_PAGER=cat`, `GIT_TERMINAL_PROMPT=0` (plus `EDITOR/VISUAL=true`) to suppress that without disabling genuine interactivity.
- **Cancellable**: the wait loop (`waitForShellSession`) selects on `ctx.Done()`, so a cancelled turn (Esc in the TUI) kills the child and returns promptly rather than waiting for the timeout.
- **Timeout**: Configurable per-command timeout (default varies by context).

### HTTP Request Credential Injection

The `http_request` tool accepts an optional `credential` parameter (credential name, not value). When provided:

1. The request-scoped credential store resolves the exact credential name. In platform mode this is a merged store: personal first, then team fallback. In console/personal mode it falls back to `credentialStoreVar`.
2. `Resolve(name)` returns `(headerKey, headerValue)` -- e.g., `("Authorization", "Bearer sk-abc123")`.
3. The resolved header is set on the outgoing HTTP request.
4. For OAuth credentials, `Resolve()` handles automatic token refresh.
5. The credential value never appears in the tool args or LLM-visible output.

### Semantic Tool Discovery

The `search_tools` tool and `ToolIndex` provide catalog discovery with a fixed model-visible bridge:

1. User messages and explicit `search_tools` calls search tool names and descriptions without adding declarations. When semantic retrieval is configured, initialization and query failures are returned; the runtime does not silently degrade to lexical-only results.
2. `describe_tools(names)` returns the selected tools' descriptions and input schemas.
3. `execute_tool(name, arguments)` resolves and runs the deferred tool with first-party precedence and request-scoped MCP access/disabled checks.

`execute_tool` is dispatched by ADK as a normal fixed tool. ChatAgent's existing BeforeTool/AfterTool callbacks unwrap the selected tool identity and arguments for mode/authorization gates, credential substitution, redaction, tracing, and artifact capture before the bridge invokes it. Request-scoped groups are merged; ambiguous bare names are rejected and must use a qualified `group/tool` reference. MCP authorization applies effective team → org → platform precedence, including explicit disabled overrides. MCP tools use this discovery model in every mode, including Astonish Code.

### Execution Envelope and Validation Safety

`common.NormalizeExecuteToolArgs` defines the same bounded interpretation for execution, authorization callbacks, and API live/restored tool projection. It accepts explicit argument objects (including JSON-encoded objects) and at most one accidental nameless wrapper or `execute_tool` self-wrapper. Missing/null/malformed arguments and repeated wrappers fail with guidance rather than becoming empty mutations. `describe_tools` and `search_tools` must be called directly and can never be unwrapped into an executable target. Native invocation decoding canonicalizes accepted envelopes once so callbacks and execution retain the same argument object for credential substitution; persisted-event projection does not mutate stored events.

`common.ValidateToolArguments` checks top-level required keys and explicit nullability from the advertised declaration before a resolved runner executes. `NodeTool` also checks before client acquisition/readiness and injects `_caller` into a copy, not the callback's argument map. This is not a general JSON-schema validator: optional fields, unknown-field policy, and nested validation retain their existing tool behavior. `write_file` separately rejects blank `path` before any filesystem/cache operation; explicit empty content remains valid. Required-key validation recognizes the compatibility alias only for the four canonical file schemas, without mutating the supplied arguments.

Native and progressive callbacks share artifact capture and `common.ToolResultSucceeded` with API restoration. Nil/error results, explicit failures, and pending approvals/authorization do not publish artifacts. `load_file` is sandbox-routed for chat, fleet, and drills; local code mode remains host-based. The `read_file` schema has no `delivery` field or alternate publication mode.

Opt-in live acceptance commands (require an existing Docker sandbox-base image) build the current node and use disposable containers without replacing the active daemon:

```sh
ASTONISH_FILE_TOOL_LIVE=1 go test ./pkg/agent -run '^TestFileToolLiveNativeDispatch$' -count=1 -v -timeout=20m
ASTONISH_FILE_TOOL_API_LIVE=1 go test ./pkg/api -run '^TestFileToolAPILiveDeliveryRestore$' -count=1 -v -timeout=15m
```

These exercise real file handlers and authenticated artifact download/restoration, with test-owned container transport. They do not establish production OverlayFS lifecycle, browser interaction, or natural-language provider reliability.

### Scoped Child Discovery

Delegated children use `NewScopedSearchToolsTool(*agent.ChildToolScope)`, wired by the launcher even without a global semantic index. Search reports only the child's frozen allowed instances; describe and execute use the same scope. Explicit filters intersect override tools/toolsets rather than widening them. Override toolsets retain their group identity without initializing ambient/global toolsets; only the supplied wrapped instances can execute. A selected `mcp:server/tool` exposes only that tool, and a web group includes only its configured request search provider. Helpers are installed once and never add declarations after discovery. Child callbacks unwrap the logical execute target for folder/tool authorization, credential substitution/restoration, redaction and artifact capture; denied or failed writes do not create artifacts. The public delegate_tasks argument shape is unchanged; the internal `SearchToolsFactory` signature now accepts the child scope.

## Key Files

| File | Purpose |
|---|---|
| `pkg/tools/internal_tools.go` | Core tools: read_file, write_file, edit_file, shell_command, etc. |
| `pkg/tools/process_tool.go` | Background process management (start, read, write, list, kill) |
| `pkg/tools/credential_tool.go` | Credential CRUD tools + resolve_credential |
| `pkg/tools/http_request.go` | HTTP requests with credential injection and SSRF prevention |
| `pkg/tools/delegate_tool.go` | delegate_tasks: sub-agent delegation |
| `pkg/tools/browser_*.go` | 8 files covering browser navigation, interaction, observation, management, state, handoff |
| `pkg/tools/email_*.go` | 5 files for email operations |
| `pkg/tools/drill_tool.go` | Drill suite management tools |
| `pkg/tools/run_drill_tool.go` | Drill execution with composite executor |
| `pkg/tools/scheduler_tool.go` | Cron job management |
| `pkg/tools/fleet_tool.go` | Fleet management tools |
| `pkg/sandbox/node_tool.go` | NodeTool: sandbox proxy wrapper |

## Interactions

- **Sandbox**: `WrapToolsWithNode()` transparently proxies container tools. `ProcessRequest()` eagerly warms containers.
- **Credentials**: `resolve_credential` returns placeholders. `http_request` uses `Resolve()` for header injection. All tool outputs pass through the Redactor.
- **Agent Engine**: Direct core tools and the progressive bridge are registered via `llmagent.Config.Tools`. Model-visible declarations stay fixed. BeforeToolCallback/AfterToolCallback wrap direct and bridged calls.
- **Memory**: `memory_save/search/get/delete` tools provide direct memory access. `memory_delete` deletes by exact ID and scope through tenant-scoped stores, using the same ownership/admin authorization as Studio memory management. Tool descriptions are indexed in the ToolIndex for semantic discovery.
- **Browser**: Browser tools run in the host process and manage a shared go-rod session. With sandbox enabled they launch Chromium+KasmVNC inside the session container and connect over CDP; sandboxed drills refuse host Chrome.
- **Drills**: The drill runner uses a composite executor that routes different tool categories to different backends.
