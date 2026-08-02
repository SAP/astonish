# Tools Overview

Astonish provides 90+ built-in tools across multiple categories, plus unlimited extension via MCP servers. Tools are the agent's interface to the world — reading files, running commands, browsing the web, managing credentials, and more.

## Tool Categories

| Category | Tools | Description |
|----------|-------|-------------|
| [Shell & Process](./shell-process.md) | 5 | Command execution, background processes |
| [File & Search](./file-search.md) | 6 | Read, write, edit, search filesystem |
| [Web & HTTP](./web-http.md) | 3 | Fetch pages, read PDFs, make API requests |
| [Browser Automation](./browser.md) | 34 | Full browser automation via CDP |
| [Email](./email.md) | 8 | Inbox management, send, search, wait |
| [Credentials](./credentials.md) | 5 | Secure secret storage and retrieval |
| [Scheduler & Agent](./scheduler-agent.md) | 10+ | Scheduling, delegation, flows, planning |
| Memory | 4 | Save, search, retrieve, and delete memories |
| Drill & Testing | 7 | Create, validate, run test suites |
| Fleet | 2 | Multi-agent collaboration plans |
| Sandbox | 3 | Sandbox template management |

## Confirmation System

Every tool has a confirmation level that determines whether user approval is needed before execution:

### auto-approve

Safe, read-only tools that execute immediately:

- `read_file`, `file_tree`, `grep_search`, `find_files`
- `memory_search`, `memory_get`
- `skill_lookup`, `list_drills`
- `web_fetch`, `read_pdf`

### always-confirm

Tools that modify state or have side effects:

- `write_file`, `edit_file`, `shell_command`
- `memory_save`, `memory_delete`
- `http_request` (POST/PUT/DELETE)
- `email_send`, `email_reply`
- `schedule_job`, `distill_flow`

## MCP Integration

Tools from [MCP servers](../configuration/mcp-servers.md) appear alongside built-in tools. The agent sees a unified tool list and selects from both seamlessly.

MCP tools follow the same confirmation system — new MCP tools default to `always-confirm` until explicitly trusted.

## Tool Execution in Studio

In the Studio interface, tool executions render as expandable cards showing:

- Tool name and arguments
- Execution duration
- Output (truncated with expand option)
- Confirmation buttons for `always-confirm` tools
- File diffs for write/edit operations

## Tool Discovery

The agent can discover tools dynamically using `search_tools`. This is useful when a task requires capabilities not in the agent's default tool set — the agent searches for relevant tools and makes them available for the current session.

See individual tool pages for detailed documentation on each category.
