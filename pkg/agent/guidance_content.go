package agent

// Guidance documents are written to memory/guidance/*.md at startup and indexed
// into the vector store. They replace the hardcoded guidance sections that were
// previously embedded in the system prompt, delivering the same instructional
// content on-demand via semantic search.

const guidanceBrowserAutomation = `# Guidance: Browser Automation

You have a built-in browser with tools for navigating, interacting, and observing web pages.
The browser uses a persistent profile — login sessions, cookies, and site data survive across restarts.

**Core workflow:**
1. ` + "`browser_navigate`" + ` to load a page
2. ` + "`browser_snapshot`" + ` to see the page structure (accessibility tree with ref IDs)
3. Use refs from the snapshot to interact: ` + "`browser_click ref=ref5`" + `, ` + "`browser_type ref=ref7 text=\"hello\"`" + `
4. ` + "`browser_snapshot`" + ` again to see the result

**Sandbox mode:**
Browser tools work automatically in sandboxes — Chromium + KasmVNC run inside the session container (same network as shell tools). Use ` + "`localhost`" + ` in ` + "`browser_navigate`" + ` URLs to reach services running inside the container. Never rewrite drill or chat URLs to the container bridge IP — that is obsolete host-browser guidance.

**Authorization and login screens:**
When a page requires login, check the credential store (` + "`list_credentials`" + ` / ` + "`resolve_credential`" + `) and fill the form. If the page shows a device authorization flow, OAuth consent, SSO redirect, MFA/TOTP, or any auth that CANNOT be solved by filling credentials — STOP immediately. Do NOT try to reverse-engineer auth APIs, run CLI commands, or programmatically bypass it. Instead: take a ` + "`browser_snapshot`" + `, relay the exact instructions to the user (code, steps, URL), and wait for them to confirm before continuing.

**Browser is an isolated client:**
The browser does NOT share cookies or sessions with ` + "`shell_command`" + ` (curl), ` + "`http_request`" + `, or ` + "`web_fetch`" + `. To check browser state, use ` + "`browser_snapshot`" + `. Never use curl to verify what the browser sees.

**When to use the browser:**
- After ` + "`web_fetch`" + ` fails for content extraction (JS-heavy pages)
- When you need to interact with a page (click buttons, fill forms, log in)
- When you need a visual screenshot of a page
- When you need to navigate through multi-page workflows

**Tips:**
- Prefer ` + "`web_fetch`" + ` for simple content extraction — it's faster
- Use ` + "`browser_snapshot`" + ` over ` + "`browser_take_screenshot`" + ` for decision-making — text is cheaper than images
- Use ` + "`mode=\"efficient\"`" + ` on snapshots for large pages — shows only interactive elements
- Refs are valid until the next snapshot. If a ref fails, take a new snapshot.
- Use ` + "`browser_tabs`" + ` with ` + "`action=\"new\"`" + ` and ` + "`incognito=true`" + ` for isolated sessions
- Before clearing cookies (` + "`browser_cookies action=clear`" + `), take a ` + "`browser_snapshot`" + ` first — clearing destroys login sessions.
- If the browser is in an unexpected state, navigate directly to the target URL. Do not use repeated ` + "`browser_navigate_back`" + ` calls — go forward, not backward.

**Human-in-the-loop (browser handoff):**
Use ` + "`browser_request_human`" + ` for CAPTCHAs, complex MFA, payment forms, or any visual challenge you cannot solve.
1. Call ` + "`browser_request_human`" + ` — returns immediately and opens visual browser access for the user.
2. **Relay the reason to the user.** The browser panel appears automatically in Studio.
3. The chat stays **fully interactive** — you can continue receiving instructions and controlling the browser while the user watches and interacts via the visual panel.
4. When the user clicks "Done", the visual session ends but the browser remains available.
5. Take a fresh ` + "`browser_snapshot`" + ` if you need to see the current state.
`

const guidanceCredentialManagement = `# Guidance: Credential Management

You have access to an encrypted credential store. Use it to securely manage API keys, tokens, and passwords.

**CRITICAL: When the user shares a secret (API key, token, password), IMMEDIATELY save it using ` + "`save_credential`" + ` before doing anything else.** Once saved, the secret value is automatically redacted from all your outputs — you will never accidentally leak it.

**Secure secret input with <<<...>>> tags:**
When asking users to provide secrets (passwords, API keys, tokens), instruct them to wrap the secret value in triple angle brackets: ` + "`<<<secret_value>>>`" + `. For example: "Please provide your API key wrapped in triple angle brackets, like: ` + "`<<<sk-abc123...>>>`" + `".
The system extracts the raw value BEFORE you see the message, replacing it with a safe token like ` + "`<<<SECRET_1>>>`" + `. You should pass these tokens as-is to tool arguments (e.g., ` + "`save_credential(password=\"<<<SECRET_1>>>\")`" + `). The real value is substituted automatically at execution time.
This ensures the actual secret never appears in your context or in any LLM API calls.

**When the user provides credentials without <<<>>> tags:**
If the user pastes raw credentials (passwords, tokens, keys) in plain text without using the ` + "`<<<>>>`" + ` wrapper, do NOT ask them to re-enter with tags. The credential is already in the conversation. Instead: immediately save it using ` + "`save_credential`" + `, then inform the user that for future credential sharing they should use the ` + "`<<<>>>`" + ` tags for better security. The Redactor will retroactively scrub the raw value from the session transcript after ` + "`save_credential`" + ` succeeds.

**Available credential types:**
- ` + "`api_key`" + ` — Custom header + value (e.g., ` + "`X-API-Key: sk-abc123`" + `)
- ` + "`bearer`" + ` — Authorization: Bearer token
- ` + "`basic`" + ` — HTTP Basic Auth header (Authorization: Basic base64(user:pass))
- ` + "`password`" + ` — Plain username + password for non-HTTP use (SSH, FTP, SMTP, databases)
- ` + "`oauth_client_credentials`" + ` — Auto-refreshing OAuth2 client credentials flow (server-to-server)
- ` + "`oauth_authorization_code`" + ` — User-authorized OAuth2 with refresh token (Google, GitHub, Spotify, etc.)
- ` + "`openstack_keystone`" + ` — OpenStack Keystone v3 token auth (password or application credential); injects X-Auth-Token automatically

**OpenStack Keystone:**
1. Save with ` + "`save_credential`" + ` type=openstack_keystone, auth_url (e.g. https://identity.example.com/v3/auth/tokens), and either application_credential_id+application_credential_secret, or username+password with project_id or project_name
2. Use ` + "`http_request`" + ` with credential parameter — X-Auth-Token is fetched, cached, and injected automatically

**OAuth Authorization Code flow (for Google Calendar, GitHub, etc.):**
1. User provides client_id and client_secret (from their Google Cloud Console, GitHub OAuth app, etc.)
2. Save these immediately with ` + "`save_credential`" + ` (type=password, name like 'google-oauth-setup') as a temporary hold
3. Build the authorization URL with: client_id, redirect_uri (usually http://localhost:8080), response_type=code, scope, access_type=offline
4. Give the URL to the user — they open it in their browser, authorize, and paste back the redirect URL containing the code
5. Exchange the code for tokens: POST to the token endpoint with grant_type=authorization_code, code, client_id, client_secret, redirect_uri
6. Save the final credential with ` + "`save_credential`" + ` type=oauth_authorization_code including: token_url, client_id, client_secret, access_token, refresh_token, scope
7. Remove the temporary credential from step 2
8. Use ` + "`http_request`" + ` with credential parameter for all subsequent API calls — tokens auto-refresh

**CRITICAL: After setting up ANY new API integration or OAuth credential**, immediately save the integration context to MEMORY.md using ` + "`memory_save`" + `. Include: credential name, service name, API base URL, scopes granted, redirect URI (if OAuth), and any discovered resources (e.g., calendar names, repositories, endpoints). Without this, future sessions will not know the integration exists or how to use it.

**Rules:**
- NEVER echo back, repeat, or include credential secret values in your responses. The redaction system will catch it, but don't rely on that — avoid outputting secrets entirely.
- Reference credentials by name (e.g., "I saved it as 'my-server-ssh'") rather than showing the value.
- Use ` + "`list_credentials`" + ` to show what's stored (it only shows metadata, never secret values).
- Use ` + "`test_credential`" + ` to verify a credential works before using it.
- Use ` + "`resolve_credential`" + ` to retrieve credential fields for non-HTTP use. Secret fields (password, token, value) are returned as ` + "`{{CREDENTIAL:name:field}}`" + ` placeholders — NOT raw values. Non-secret fields (username, header, client_id) are returned as plaintext.
- Pass placeholders directly to ` + "`process_write`" + `, ` + "`shell_command`" + `, ` + "`browser_type`" + `, or ` + "`browser_fill_form`" + ` — the system substitutes real values at execution time. The actual secrets never appear in your context.
- For HTTP requests, prefer ` + "`http_request`" + ` with the ` + "`credential`" + ` parameter — it handles auth headers automatically without exposing secrets.

**SSH/FTP/database workflow:**
1. Save credentials as ` + "`password`" + ` type: ` + "`save_credential(name=\"my-server-ssh\", type=\"password\", username=\"admin\", password=\"...\")`" + `
2. Start the connection: ` + "`shell_command(command=\"ssh admin@myserver.example.com\")`" + `
3. When prompted for password: ` + "`resolve_credential(name=\"my-server-ssh\")`" + ` — this returns ` + "`{{CREDENTIAL:my-server-ssh:password}}`" + `
4. Send the placeholder: ` + "`process_write(session_id=\"...\", input=\"{{CREDENTIAL:my-server-ssh:password}}\\n\")`" + ` — the real password is injected automatically

**CLI commands** (only if the user specifically asks about the command line):
- ` + "`astonish credential add <name>`" + ` — Interactive TUI form (no flags; prompts for type and fields)
- ` + "`astonish credential list`" + ` — List credentials (metadata only)
- ` + "`astonish credential remove <name>`" + ` — Remove a credential
- ` + "`astonish credential test <name>`" + ` — Test a credential
Prefer using your tools (` + "`save_credential`" + `, etc.) directly over suggesting CLI commands.
`

const guidanceJobScheduling = `# Guidance: Job Scheduling

You can create scheduled jobs that run automatically using the ` + "`schedule_job`" + ` tool.

**CRITICAL: This is a multi-step conversational process. Send ONE message per step and WAIT for the user's response before proceeding. Do NOT batch multiple steps into a single message.**

**When the user asks to schedule something, follow these steps IN ORDER:**

**Step 1 — Acknowledge.** Tell the user you can set that up. Keep it short: "Sure, I can schedule that for you." Then move to step 2 in the SAME message.

**Step 2 — Ask which mode (MANDATORY).** You MUST ask the user which mode they prefer. NEVER choose a mode on their behalf or recommend one over the other. Present both options objectively and let the user decide:
- **Routine** — Replays a saved flow with the exact same steps and parameters every time. Predictable and consistent.
- **Adaptive** — The AI receives written instructions and executes them fresh each time using tools. Can reason, adapt, and handle unexpected situations.
Do NOT add commentary suggesting which mode is better, more practical, or more appropriate. Simply present both and wait for the user to choose.

**Step 3 — Gather the schedule.** Ask when and how often. Convert natural language to cron (e.g., 'every morning at 9' → ` + "`0 9 * * *`" + `). Confirm the cron with the user. Wait for confirmation.

**Step 4 — Gather details (use conversation context).**
- For **routine**: identify the flow name and ALL required parameters. If you just ran this task in the conversation, you ALREADY KNOW the parameters — extract them from the conversation context. Never leave required parameters empty if you have them available. **If no saved flow exists for this task**, call the ` + "`distill_flow`" + ` tool to create one from the conversation traces. This will analyze the tool calls you just made and generate a reusable flow YAML. Then use the resulting flow name for scheduling.
- For **adaptive**: write detailed instructions for your future self. CRITICAL — The instructions MUST include the EXACT output format that was last shown to the user. Copy the format/template from the most recent output the user saw and approved. Include a concrete example of what the output should look like, taken from the actual output you produced. The scheduled execution must reproduce the same presentation — if the user wants a different format, they will ask.

**Step 5 — Summarize the plan and ask permission to test (MANDATORY).** Before running anything, present a clear summary of what will happen:
- Mode, schedule, flow/instructions
- Note that results will be delivered to all active channels (e.g., Telegram) automatically
- Explain: "I'll run a test execution now to verify it works. The result will be shown to you before I enable the schedule."
- Ask: "Does this look right? Can I go ahead and run the test?"
NEVER execute the test without the user's explicit approval. Some tasks may be sensitive or have side effects. Wait for a clear yes.

**Step 6 — Run the test.** After the user approves, call ` + "`schedule_job`" + ` with ` + "`test_first=true`" + `. You do NOT need to set channel or target — delivery is automatic to all active channels. Show the test result to the user.

**Step 7 — Ask to enable.** Ask the user if the test result looks good. If confirmed, call ` + "`update_scheduled_job`" + ` to enable the job. If not, discuss what to fix.

**Cron syntax** (5-field: minute hour day-of-month month day-of-week):
- ` + "`0 9 * * *`" + ` = daily at 9 AM
- ` + "`0 9 * * 1-5`" + ` = weekdays at 9 AM
- ` + "`0 */2 * * *`" + ` = every 2 hours
- ` + "`*/30 * * * *`" + ` = every 30 minutes
`

const guidanceTaskDelegation = `# Guidance: Task Delegation

Use ` + "`delegate_tasks`" + ` to run tasks via sub-agents. Sub-agent execution is **transparent** — the user sees every tool call, result, and image in real-time, exactly as if you were doing the work yourself. Only a compact summary enters your context, keeping it lean.

## When to delegate

Delegation gives you **parallelism** and **context isolation**. Sub-agents run independently and only their concise summaries enter your context — raw search results, HTML pages, and API responses stay out. This keeps your context lean and focused.

**Prefer delegation when:**
- The request involves 2+ independent information-gathering tasks (e.g., "research X and Y", "compare A vs B") — each topic becomes a parallel sub-task
- A task will produce large raw output (web research, multi-page fetches, API exploration) — delegate so only concise findings enter your context
- A task involves many sequential tool calls (file analysis, API testing, container setup) **and** can run in parallel with other work

**Call tools directly (no delegation) when:**
- It's a single tool call or one-off action (send one email, one HTTP request, one MCP tool, one screenshot)
- You need the result immediately to decide your next step
- The user asked to use a specific tool or MCP server — use ` + "`search_tools`" + ` and ` + "`describe_tools`" + ` if needed, then call it through ` + "`execute_tool`" + ` on the main thread

**Never use ` + "`delegate_tasks`" + ` as a fallback:**
- Do **not** delegate because a tool "wasn't found", failed once, or "isn't on the main thread"
- Main thread and sub-agents share the same tool catalog; delegation does **not** unlock extra tools
- If a tool is missing: call ` + "`search_tools`" + `, inspect it with ` + "`describe_tools`" + `, then invoke it through ` + "`execute_tool`" + `. Do not wrap a single failed call in a sub-agent
- ` + "`delegate_tasks`" + ` is for **parallelism and context isolation**, not error recovery

## Task Decomposition Strategy

When facing a complex goal, think in terms of independent deliverables:

1. **Identify independent units** — Each sub-task should have a clear, self-contained deliverable (a file, a data set, an analysis). If two tasks don't depend on each other's output, they can run in parallel.
2. **Keep sub-tasks focused** — A sub-task should do ONE thing well. "Research competitor pricing" is good. "Research competitors and write the final report" is too broad.
3. **Handle dependencies with phased delegation** — If task B needs the output of task A, run them in separate ` + "`delegate_tasks`" + ` calls. The first call completes entirely before the second starts. Use ` + "`read_task_result`" + ` to retrieve full outputs from earlier phases if needed.
4. **Choose the right data retrieval strategy** — For web research, delegate targeted fetches: "Fetch the pricing page at URL", "Get the API documentation for endpoint Y". Targeted tasks produce cleaner, more usable results. However, **for source code analysis or repository exploration, clone the repository locally** using ` + "`git clone`" + ` or ` + "`gh repo clone`" + ` (via ` + "`shell_command`" + `) — this gives sub-agents full access to ` + "`grep_search`" + `, ` + "`file_tree`" + `, ` + "`read_file`" + `, and the complete codebase. Reserve ` + "`web_fetch`" + ` on raw GitHub URLs only for quick single-file lookups where cloning would be overkill. If a clone fails, retry before switching strategies — transient network failures (especially in sandboxed environments) are common and do not mean the network is permanently unavailable.
5. **Limit sub-task scope to avoid context explosion** — Sub-agents have a 5-minute timeout and limited context. A sub-task that tries to do too much will produce worse results than two focused sub-tasks.

## How to delegate

` + "```" + `
delegate_tasks(tasks: [{
  name: "descriptive-name",
  task: "Clear description of what to accomplish",
  instructions: "Additional context, file paths, constraints",
  tools: ["group_name"]
}])
` + "```" + `

**Tool groups** are listed in your system prompt under "Task Delegation". Common groups:
- ` + "`core`" + ` — shell, file I/O, grep, find (for sub-agent file work)
- ` + "`browser`" + ` — browser automation (navigate, click, type, screenshot)
- ` + "`web`" + ` — web fetching and HTTP requests
- ` + "`credentials`" + ` — credential store access (list, resolve, test)
- ` + "`sandbox_templates`" + ` — save, list, and use sandbox container templates
- ` + "`fleet_plans`" + ` — create and validate fleet plans
- ` + "`process`" + ` — background processes, interactive commands

You can combine groups: ` + "`tools: [\"core\", \"web\"]`" + ` or request individual tools by name.

## Synthesizing Results

After all sub-tasks complete, you are the synthesizer. **Do not just concatenate sub-agent output.** Instead:
- Cross-reference findings from multiple sub-agents to identify patterns, contradictions, or gaps.
- Structure the final output around the user's original question, not around how the work was divided.
- If a sub-task produced a large result that was summarized, use ` + "`read_task_result`" + ` to retrieve the full text before synthesizing — the summary may omit critical details.
- Cite which sub-task produced which finding when it adds clarity.

## Structured Output & Reports

When the user's request involves research, analysis, or comparison work — or when they explicitly ask for a report — your final deliverable should be a **well-structured document saved as a file** using ` + "`write_file`" + `. This applies to:
- Deep comparisons or competitive analyses
- Architecture or code reviews
- Research summaries with multiple sections
- Any output the user is likely to share, reference later, or export

**Reports are produced via a TWO-STEP contract. Both steps are required.**

**Step 1 — Write the file with ` + "`write_file`" + `:**
- Use ` + "`write_file`" + ` directly — do NOT delegate file writing to sub-agents.
- Use a descriptive filename in the working directory (e.g., ` + "`comparison-report.md`" + `, ` + "`pricing-analysis.md`" + `, ` + "`architecture-review.md`" + `).
- The file extension must be ` + "`.md`" + ` — only markdown reports are eligible for inline rendering.

**Step 2 — Signal that the file is a report with an ` + "`astonish-report`" + ` fence:**

After the ` + "`write_file`" + ` call completes, include a fenced block in your reply text:

` + "```" + `astonish-report
path: <absolute path you passed to write_file>
title: <human-readable title — optional but strongly preferred>
` + "```" + `

The ` + "`path`" + ` field MUST match exactly the absolute path you used in ` + "`write_file`" + `. The fence is a *signal*, not content — it tells the chat UI to embed the file inline as a full-width report viewer instead of as a small download card. Without the fence, your file will still be saved and downloadable, but it will appear as a compact artifact card, not a report. Without ` + "`write_file`" + `, the fence is ignored — there is no file to embed.

**Both steps are mandatory. The fence alone does not create a file. The file alone does not promote to a report viewer.**

**When to use this two-step contract:**
- Any time the user asks for a report, analysis, review, comparison, or research summary.
- Any time you produce a long-form markdown document the user is likely to read end-to-end, share, or export to PDF/DOCX.

**When NOT to use the fence:**
- Quick answers, status updates, conversational replies — respond directly in the chat, no file needed.
- Code files, configs, scripts, data files — these are working artifacts, not reports. Use ` + "`write_file`" + ` without the fence; they appear as a download card, which is the correct affordance.
- Incidental edits during a larger task (e.g., fixing a typo in an existing file). Use ` + "`edit_file`" + ` without the fence unless the edit *is itself* the report being delivered.

After Step 2, present a concise summary inline in the chat with key findings. The user gets all three: an inline-rendered report, a downloadable document, and an at-a-glance overview.

### Diagrams in Reports (Mermaid)

Markdown reports support **mermaid diagrams** — flowcharts, sequence diagrams, pie charts, Gantt charts, ER diagrams, class diagrams, and more. These render as visual SVGs in the chat viewer and in exported PDFs and DOCX files. Use mermaid whenever a report benefits from visual representation of flows, architectures, timelines, or data breakdowns.

Syntax — use a fenced code block with language ` + "`mermaid`" + `:

` + "```" + `mermaid
graph TD
    A[Start] --> B{Decision}
    B -->|Yes| C[Action]
    B -->|No| D[End]
` + "```" + `

Common diagram types for reports:
- ` + "`graph TD`" + ` / ` + "`graph LR`" + ` — flowcharts and architecture diagrams
- ` + "`sequenceDiagram`" + ` — API flows, user interactions, process sequences
- ` + "`pie`" + ` — data breakdowns, market share, budget allocation
- ` + "`gantt`" + ` — timelines, project plans, roadmaps
- ` + "`erDiagram`" + ` — database schemas, data models
- ` + "`classDiagram`" + ` — system design, class hierarchies
- ` + "`stateDiagram-v2`" + ` — state machines, workflow states

When the user asks for a report with charts or diagrams, prefer mermaid in a markdown file over generating a visual app. Visual apps (` + "`astonish-app`" + `) are for interactive dashboards and tools, not static reports.

## Announcing Your Plan

**` + "`announce_plan`" + ` exists only in Plan mode.** The runtime will refuse it in Normal mode. In Plan mode it shows the user a visible checklist and persists the plan to a session ` + "`PLAN.md`" + ` that is inlined after approval and after compaction. Put a self-contained implementation spec in each step's ` + "`details`" + ` field (exact symbols, signatures, call sites, tests) and list every affected file so execution does not re-investigate.

**Tracking progress — two mechanisms:**
- **Work you do yourself (main thread):** call ` + "`update_plan`" + ` to mark a phase ` + "`running`" + ` when you start it and ` + "`complete`" + ` (or ` + "`failed`" + `) when you finish. Do this as you go — it keeps the checklist and ` + "`PLAN.md`" + ` accurate and lets you resume exactly where you left off after a context summary. There is no automatic completion for inline work; you own it.
- **Delegated work:** set the ` + "`plan_step`" + ` field on each ` + "`delegate_tasks`" + ` task. Those phases progress automatically from sub-task lifecycle events — do not also call ` + "`update_plan`" + ` for them.

**After a context summary:** the approved PLAN.md is inlined in the execution context. Follow it; do not re-announce or re-investigate confirmed files.

**How to use (Plan mode only):**
1. Call ` + "`announce_plan`" + ` with a concise ` + "`goal`" + ` (the plan title) and 3-7 ` + "`steps`" + ` (each with ` + "`name`" + `, ` + "`description`" + `, required ` + "`details`" + ` and ` + "`files`" + `).
2. Each step is a distinct phase of work (a ` + "`delegate_tasks`" + ` call or a chunk of main-thread work).
3. Drive progress via ` + "`update_plan`" + ` (main-thread) or ` + "`plan_step`" + ` (delegated).

**Plan step naming tips:**
- Keep step names short and stable: "explore-repos", not "Clone repo and run find"
- Each step should map to a distinct phase of work
- Include the final synthesis/output step (e.g., "Produce comparison report")
- Step names should be the ` + "`name`" + ` field; use ` + "`description`" + ` for the user-visible label and ` + "`details`" + ` for the concrete plan

**Example:**
` + "```" + `
announce_plan(
  goal: "Source-Level GitHub Comparison: astonish vs openclaw",
  steps: [
    {name: "explore-repos", description: "Explore both repository structures and dependencies", details: "file_tree both repos; read go.mod / package.json; note entry points"},
    {name: "analyze-core", description: "Read and analyze core source files from both projects"},
    {name: "compare-features", description: "Compare feature implementations side by side"},
    {name: "write-report", description: "Produce structured comparison report"}
  ]
)
# ... start phase 1 ...
update_plan(step: "explore-repos", status: "running")
# ... finish phase 1 ...
update_plan(step: "explore-repos", status: "complete")
` + "```" + `

## Guidelines

- ALWAYS send a brief acknowledgment message BEFORE calling delegate_tasks
- Be specific in task descriptions — sub-agents work autonomously without your conversation context
- **Link delegate tasks to plan steps** — when a plan is active, set the ` + "`plan_step`" + ` field on each delegate task to the name of the plan step it belongs to. Multiple tasks can share the same ` + "`plan_step`" + ` — the step completes only when ALL tasks with that plan_step finish. This drives accurate progress tracking in the UI.
- Sub-agents can read memory but cannot write to it
- Max 10 tasks per delegation call, each with a 5-minute timeout
- For multi-step workflows, delegate each phase as a separate task with clear inputs/outputs
`

const guidanceProcessManagement = `# Guidance: Process Management & Interactive Commands

## Interactive Commands

` + "`shell_command`" + ` runs commands in a full PTY (pseudo-terminal). If a command requires interactive input, it will detect this and return ` + "`waiting_for_input=true`" + ` with a ` + "`session_id`" + `.

**When shell_command returns ` + "`waiting_for_input=true`" + `:**
1. Read the output — it shows the prompt the process is displaying
2. Use ` + "`process_write(session_id, input)`" + ` to send your response (always include ` + "`\\n`" + ` to press Enter)
3. Use ` + "`process_read(session_id)`" + ` to check for more output
4. Repeat until the task is complete

**For long-running commands** (servers, watchers), use ` + "`shell_command(command, background=true)`" + ` to start them without waiting. Then use ` + "`process_read`" + ` to check output and ` + "`process_kill`" + ` to stop them.

**Common interactive scenarios:**
- SSH host key verification: respond with ` + "`yes\\n`" + `
- Password prompts: first check the credential store (` + "`resolve_credential`" + `) — the password may already be saved. If not, ask the user to provide it wrapped in triple angle brackets (e.g., ` + "`<<<my_password>>>`" + `) so the value stays protected. Then save it with ` + "`save_credential`" + ` for future use, and send it via ` + "`resolve_credential`" + ` + ` + "`process_write`" + ` with the ` + "`{{CREDENTIAL:name:password}}`" + ` placeholder. Never send a raw password directly.
- Package install confirmations: respond with ` + "`y\\n`" + `
- Use ` + "`process_list`" + ` to see all active sessions and ` + "`process_kill`" + ` to clean up when done

## Commands That Open Text Editors

Some CLI tools open a text editor (vi, nano, etc.) for user input. You cannot operate a text editor through ` + "`process_write`" + ` — this will cause the command to hang indefinitely.

Before running any command, consider whether it might open an editor. Common triggers include commit messages, interactive modes, config editing, and squash/rebase operations.

**Always prevent editors from opening by using one of these strategies:**
- Use flags that skip the editor (e.g., ` + "`--no-edit`" + `, ` + "`--message \"...\"`" + `, ` + "`-m \"...\"`" + `)
- Use non-interactive alternatives instead of interactive modes
- Pipe input via stdin where the tool supports it
- As a last resort, prefix with ` + "`EDITOR=true`" + ` to auto-accept defaults: ` + "`EDITOR=true <command>`" + `

Note: The shell environment already sets ` + "`EDITOR=true`" + ` and ` + "`VISUAL=true`" + ` as a safety net, but you should still prefer explicit flags that avoid the editor entirely.

If a command unexpectedly opens an editor (returns ` + "`waiting_for_input`" + ` with editor-like output), kill the session and re-run with editor prevention applied.
`

const guidanceWebAccess = `# Guidance: Web Access & HTTP Requests

## URL Fetching Priority Chain

You have a built-in ` + "`web_fetch`" + ` tool that can fetch and extract content from any URL.

**MANDATORY tool selection rules for web tasks:**

1. **For any specific URL**, you MUST use ` + "`web_fetch`" + ` first. Do NOT skip it in favor of other tools.
2. If ` + "`web_fetch`" + ` returns empty, navigation-only, or broken content (common with JS-heavy pages), THEN try the same URL using browser tools (e.g., ` + "`browser_navigate`" + ` + ` + "`browser_snapshot`" + `). This runs locally and is free.
3. ONLY if ` + "`web_fetch`" + ` and the browser fail to produce usable content, THEN retry the same URL with the configured web extract tool.
4. To **search** for information (when you don't have a specific URL), use the configured web search tool.

**Never** use a search tool to extract content from a known URL. **Never** skip ` + "`web_fetch`" + ` and go directly to an MCP extraction tool. When a browser is available, prefer it before paid extraction to avoid unnecessary costs.
Use web capabilities when you need up-to-date information not available in your training data.

## HTTP Requests

Use the ` + "`http_request`" + ` tool for API calls instead of curl via shell_command.
- Set ` + "`credential`" + ` to a stored credential name for authenticated requests
- Use ` + "`save_credential`" + ` first if you need to store new API credentials
- The credential's auth header is injected automatically (supports API key, bearer, basic, OAuth)
- For JSON APIs, the Content-Type header is set automatically when the body is JSON
- Credential values are never exposed in tool args — only the credential name is passed
`

const guidanceMemoryUsage = `# Guidance: Memory Usage

## Saving to Memory

When you discover NEW durable facts during an interaction, save them using **memory_save**.

**Auto-save workarounds:** After completing a task where your initial approach FAILED and you found a working alternative, ALWAYS save the solution using ` + "`memory_save`" + ` with kind="workarounds". Include: what failed, why, and the working command/approach. This prevents repeating the same trial-and-error in future sessions.

**What to save to MEMORY.md (omit kind):** connection details (IPs, hostnames, users, auth methods, ports), server roles, network topology, user preferences, project conventions, **and API/service integrations** (credential name, service name, API base URL, OAuth scopes, redirect URI, discovered resources like calendar names, repo lists, or account details). Keep it concise — only durable facts.

**What to save to knowledge files (via kind param):** procedural knowledge discovered during problem-solving. Use the appropriate kind:
- ` + "`kind=\"tools\"`" + ` — tool quirks, CLI syntax, API access patterns, credential usage
- ` + "`kind=\"workarounds\"`" + ` — problems encountered and their solutions, error workarounds
- ` + "`kind=\"infrastructure\"`" + ` — server configuration, networking, deployment, service architecture
- ` + "`kind=\"projects\"`" + ` — project-specific knowledge (build commands, API endpoints, fleet plans)
- ` + "`kind=\"others\"`" + ` — anything that doesn't fit the above categories

**Behavior preferences:** If the user tells you to change how you behave (e.g., always use a certain tool, skip confirmations for certain operations), save that to INSTRUCTIONS.md rather than MEMORY.md.

**Correcting facts:** When the user corrects information or you discover that existing memory is wrong, use ` + "`overwrite: true`" + ` and provide the **complete corrected section content**. This replaces the entire section, preventing contradictory duplicate entries.

## Deleting from Memory

When the user asks you to forget, remove, delete, or clean up stored memory, use **memory_delete**. First call ` + "`memory_search`" + ` to identify the exact entry, then call ` + "`memory_delete`" + ` with the result's ` + "`id`" + ` and ` + "`scope`" + `. Only delete memories the user explicitly asks to remove.

**NEVER save:** command outputs, lists of resources (VMs, containers, pods), current status, resource usage, or ANY results/data that changes over time. Those MUST always be fetched live. Saving stale results risks returning outdated information instead of checking the actual current state.

## Recalling from Memory

You have a searchable knowledge base with procedural knowledge, workarounds, and past solutions.

**ALWAYS call ` + "`memory_search`" + ` before starting any multi-step task.** Search for the tool name, command name, or task description. Past workarounds, working commands, and procedures are stored here.
Use ` + "`memory_get`" + ` to read full context around a search result.

Examples of when to search:
- Before running yt-dlp → search "yt-dlp"
- Before deploying an app → search "deploy" or the app name
- Before configuring a server → search the server name or technology
`

const guidanceSandboxTemplates = `# Guidance: Sandbox Templates

Sandbox templates are frozen container snapshots with a project's code, toolchains, and dependencies pre-installed. Fleet sessions clone from templates so each agent starts with a ready-to-use development environment instead of building from scratch.

**These tools run on the HOST, not inside the container.** You cannot manage templates from inside the sandbox — CLI commands like ` + "`astonish sandbox init`" + ` or API calls to ` + "`localhost`" + ` from within the container will not work. Always use ` + "`delegate_tasks`" + ` with ` + "`tools: [\"sandbox_templates\"]`" + `.

## Available tools (via delegate_tasks)

Access these tools by delegating with ` + "`tools: [\"sandbox_templates\"]`" + `:

- ` + "`save_sandbox_template`" + ` — Freeze the current sandbox container as a reusable template. Call this after cloning the repo, installing dependencies, and verifying the build works inside the container. The tool stops the container, snapshots it, and restarts. Returns a ` + "`template_name`" + ` to pass to ` + "`save_fleet_plan`" + `'s template field.
- ` + "`list_sandbox_templates`" + ` — List all saved templates with name, description, creation date, and associated fleet plans. Use this to check what templates exist before creating new ones or to verify a template was saved.
- ` + "`use_sandbox_template`" + ` — Switch the current sandbox session to a different template. Tears down the current container and creates a new one cloned from the specified template. All file and shell tools then operate inside the new container.

## Creating a template (typical workflow)

1. Set up the container: clone the repo, install toolchains (Go, Node, etc.), install dependencies, verify the build passes
2. Stop any background services (servers, watchers) — the template must be saved in a quiescent state
3. Delegate the save:
` + "```" + `
delegate_tasks(tasks: [{
  name: "save-template",
  task: "Save the current sandbox container as a template named '<repo-name>' with description '<stack summary>'",
  tools: ["sandbox_templates"]
}])
` + "```" + `
4. Use the returned ` + "`template_name`" + ` when calling ` + "`save_fleet_plan`" + `

## Key rules

- **NEVER try to save templates from inside the container.** The ` + "`save_sandbox_template`" + ` tool operates on the host's Incus runtime — it cannot be called via shell commands or API requests from within the sandbox.
- **Stop background processes first.** Running services (dev servers, file watchers) can cause snapshot corruption. Use ` + "`process_kill`" + ` to stop them before saving.
- **Use the repo name as the template name.** For ` + "`acme/billing-api`" + `, use ` + "`billing-api`" + `. This keeps naming consistent and predictable.
- **Verify before saving.** Run the build, run tests (or a drill suite), and confirm everything works before freezing the template. A broken template means every fleet session starts broken.
`

const guidanceWebResearch = `# Guidance: Web Research & Information Gathering

When you need to research information from the web, choose the right tool based on the nature of the information you need.

## Search tools vs browser navigation

Search tools (tavily_search, brave_web_search, web_fetch) query search engine indexes — they return summarized results, snippets, and links. This is ideal for **discovering** information: finding articles, understanding topics, locating relevant URLs, or getting general knowledge that doesn't change minute-to-minute.

For tasks requiring **live, current data from specific websites** — real-time prices, stock availability, product listings, account dashboards, dynamically-rendered content, or any information that changes frequently and must reflect the present state — delegate with browser tools to navigate the site directly. Search indexes are often hours or days stale and incomplete for this kind of data.

**Rule of thumb:** If the answer depends on what a website shows *right now* (not what was indexed days ago), use the browser. If you need to discover *which* websites or resources exist, use search.

## When to use search (delegate with web tools)

- General knowledge research: "What is X?", "How does Y work?"
- News and current events (search indexes update frequently for news)
- Finding documentation, articles, guides
- Discovering which sites/URLs to visit for deeper investigation
- Getting aggregated information from multiple sources simultaneously

## When to use browser (delegate with browser tools)

- Current prices, availability, or inventory on a specific store or site
- Content that is dynamically rendered (SPAs, JS-heavy pages)
- Tasks that require interaction (login, form fill, configuration, checkout)
- Data that must be verified as current (financial, inventory, scheduling)
- When search results seem stale or don't match what the site actually shows

## Research strategy

1. **Assess the task** — Does the user need live site data, or general knowledge?
2. **For general research**: Delegate with web tools (search + extract). A single search query returns aggregated results from many sites simultaneously.
3. **For live/current data**: Delegate with browser tools to navigate the target site directly and extract what is currently displayed.
4. **Combine when appropriate**: Use search to discover which sites to check, then use browser to get the live data from those sites.
5. **Synthesize**: After sub-tasks complete, structure the final output around the user's original question. Save as a markdown file for substantial research output.
`

const guidanceCodeIntelligence = `# Guidance: Code Intelligence

Use code intelligence tools for structural symbol navigation in supported source languages: Go, TypeScript/TSX, JavaScript/JSX, and Python.

## When to use these tools

- Use ` + "`repo_map`" + ` when entering an unfamiliar repository, package, or module. It gives a ranked structural overview of important files and definitions before broad edits.
- Use ` + "`code_definition`" + ` to find where a function, method, type, class, or variable is declared. Prefer it over ` + "`grep_search`" + ` when you need the declaration of a specific symbol.
- Use ` + "`code_references`" + ` before refactoring, changing a signature, renaming, or deleting a symbol. It finds structural usages and call sites so you do not miss capped or noisy grep results.

## When to use grep_search instead

- Text patterns, literals, comments, config files, docs, generated files, or unsupported languages.
- Regex searches that are not tied to a specific symbol.
- Any fallback case where a code intelligence tool reports that the tree-sitter runtime library is unavailable.

## Recommended workflow

1. For unfamiliar code, call ` + "`repo_map`" + ` scoped to the relevant root.
2. Before editing a symbol, call ` + "`code_definition`" + ` to inspect the declaration.
3. Before a refactor, call ` + "`code_references`" + ` to enumerate affected sites.
4. Use ` + "`read_file`" + ` on the returned locations before editing.
5. After editing, run the project's tests or build with ` + "`shell_command`" + `.

## Planning a change (investigate before you touch code)

Whether you are in Plan mode or just scoping a multi-step change, understand the full blast radius before you start editing — a plan that misses callers or tests produces partial, broken implementations.

- Batch independent read-only lookups in the same turn so they run in parallel (e.g. ` + "`code_definition`" + ` for several symbols at once) instead of one-at-a-time round trips.
- Trace ALL callers with ` + "`code_references`" + ` before you change any signature, type, or interface — then plan to update every call site, plus the tests and docs that exercise it. Do not leave orphaned or unwired code.
- Spend effort proportional to blast radius: a one-file tweak needs a quick look; a cross-cutting change needs full tracing. Stop once you can name every file you would change and why — do not read the whole repo.
- Prefer structural tools (` + "`code_definition`" + `/` + "`code_references`" + `) over broad ` + "`grep_search`" + ` for symbols, and never re-read a file already in your context.
`

// guidanceGenerativeUI has been moved to pkg/skills/builtin_content.go
// and is now delivered as a built-in skill via skill_lookup("generative-ui")
// instead of through the vector store.
