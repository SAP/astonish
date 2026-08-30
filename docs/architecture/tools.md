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
read_file, write_file, edit_file, file_tree, grep_search, find_files,
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
