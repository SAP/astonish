# Multi-Agent Fleet

## Overview

Fleet is Astonish's multi-agent collaboration system. Instead of a single AI agent handling everything, a fleet consists of specialized agents (product owner, architect, developer, QA, UX, E2E tester) that communicate through a shared channel using @mentions. Each agent has its own role, personality, tool access, and memory scope.

Fleets are designed for complex, multi-phase projects where different expertise is needed at different stages -- like a software development team where the PO writes requirements, the architect designs the solution, developers implement it, and QA validates it.

## Key Design Decisions

### Why @Mention-Based Communication

Agents communicate through a shared channel using `@agent-name` mentions rather than direct API calls between agents. This was chosen because:

- **Transparency**: All communication is visible in the channel, making it easy to follow the collaboration, debug issues, and intervene when needed.
- **Controllable routing**: The communication graph defines who can talk to whom, preventing chaotic all-to-all messaging.
- **Human participation**: Users can jump into the channel and @mention any agent to redirect work, provide feedback, or answer questions.
- **Natural for LLMs**: Models are already trained on @mention-style communication from chat platforms.

### Why Explicit Communication Graphs

Each fleet defines a `communication` section specifying which agents can reach which other agents:

```yaml
communication:
  - from: po
    to: [architect, dev, qa]
  - from: architect
    to: [po, dev]
  - from: dev
    to: [architect, qa]
```

Without this, agents would freely message each other in unpredictable patterns. The graph enforces workflow discipline -- the PO can't bypass the architect to talk directly to QA, ensuring proper information flow.

When an agent needs to reach someone outside its direct connections, `rerouteViaIntermediary` finds a one-hop path through a shared connection.

### Why Memory-Scoped Messages

Each message carries `MemoryKeys` -- a list of agent names that can see it. When building an agent's context for a new turn, `BuildThreadContext` filters messages to only those the agent has access to. This prevents:

- Information overload: Agents don't see irrelevant technical discussions between other agents.
- Context pollution: A QA agent doesn't need to see the internal back-and-forth between PO and architect about requirements.
- Budget management: The 50K character budget per turn is spent on messages the agent actually needs.

### Why LLM-Based Routing

Message routing uses an LLM call to determine the recipient from the message content, with a regex fallback for when the LLM fails or times out (15 seconds):

- **LLM routing**: Understands context, resolves ambiguous @mentions, handles implicit addressing ("tell the developer to...").
- **Regex fallback**: Simple `@agent-name` pattern matching. Fast, deterministic, handles the 80% case.

This dual approach ensures routing works even when the LLM is slow or unavailable.

### Why Isolated Workspaces

Each fleet session gets its own workspace created via `git clone --local`:

- **Hardlinks**: Local clone uses hardlinks to the object store, making it nearly instant and space-efficient.
- **Isolation**: Each session works on its own branch without affecting the main repository.
- **Safety**: The cleanup guard verifies the workspace is actually a git repo before `rm -rf`.

### Why Stateless GitHub Integration

The `GitHubMonitor` uses a label-based model where GitHub is the source of truth:

- Issues labeled with the fleet's activation label trigger new fleet sessions.
- The monitor uses cursor-based polling (checking comments newer than the last seen) rather than webhooks.
- No local state needs to be maintained -- if the daemon restarts, it simply re-polls and catches up.
- Labels on issues control lifecycle: adding a label starts processing, removing it stops.

## Architecture

### Fleet Session Lifecycle

```
1. Plan Creation (user defines fleet plan):
   - Select fleet definition (agents, comms graph)
   - Configure channel (chat or GitHub Issues)
   - Set workspace, credentials, activation rules

2. Session Start (user request or GitHub issue trigger):
   - Create workspace (git clone --local)
   - Create sandbox containers (one per agent)
   - Initialize ChatChannel or GitHubIssueChannel
   - Register session in SessionRegistry

3. Orchestration Loop (FleetSession):
   State machine: idle -> processing -> waiting_for_customer -> idle
   |
   For each message in channel:
     a. Route to target agent (LLM + regex fallback)
     b. Build agent context (thread history, budget-limited)
     c. Execute agent turn (via SubAgentManager)
     d. Post response back to channel
     e. Track progress (milestones, completions)

4. Error Handling:
   - Max 3 consecutive errors before stopping
   - Idle watchdog: 5min without activity triggers check
   - Recovery: PlanActivator can restart stopped sessions

5. Session End:
   - Stop all agents
   - Cleanup sandbox containers
   - Post summary to GitHub (if GitHub channel)
```

### Agent Prompt Architecture

Each fleet agent gets a comprehensive system prompt built by `BuildAgentPrompt`:

```
Identity: Name, role description, personality traits
    +
Behaviors: Specific instructions for this agent type
    +
Communication Rules: Who to @mention, routing constraints
    +
Delegate Instructions: Optional — only when an agent declares a `delegate` tool (custom plans)
    +
Progress Tracker: Current milestones, completed items
    +
Environment: Workspace path, git branch, available tools
    +
Project Context: AGENTS.md content (loaded via project_context.load_file when present)
```

### Thread Context Building

`BuildThreadContext` assembles the conversation history for an agent's turn with a 50K character budget:

1. Filter messages by the agent's memory keys (only messages it can see).
2. Deduplicate messages (same content from same sender).
3. If total exceeds budget: progressively truncate older messages from the beginning.
4. Format as a conversation thread with sender names and timestamps.

### Progress Tracking

The `ProgressTracker` monitors fleet activity and injects status into agent prompts:

- Tracks milestones: PO approvals, architect handoffs, dev deliveries, QA sign-offs.
- Uses conservative pattern detection (explicit keywords in messages).
- Survives recovery -- progress state is persisted and restored when a session restarts.
- Prevents loops: agents can see what's already been accomplished.

### Bundled Software Development Fleet

Astonish ships with a pre-built 6-agent fleet for software development:

| Agent | Role | Communicates With |
|---|---|---|
| PO (Product Owner) | Requirements, acceptance criteria | architect, dev, qa |
| Architect | System design, technical decisions | po, dev |
| Dev (Developer) | Implementation, code changes | architect, qa |
| QA | Testing, validation | dev, po |
| UX | User experience, design review | po, dev |
| E2E | End-to-end testing, integration | qa, dev |

The fleet plan wizard (700+ line system prompt) guides users through configuring their project.

## Key Files

| File | Purpose |
|---|---|
| `pkg/fleet/config.go` | Fleet definition: agent configs, communication graph, validation |
| `pkg/fleet/plan_config.go` | Fleet plans: activation, workspace, channel, credentials |
| `pkg/fleet/session_manager.go` | FleetSession: orchestration loop, state machine, error tracking |
| `pkg/fleet/router.go` | LLM-based message routing with regex fallback |
| `pkg/fleet/thread.go` | Per-agent memory view with budget management |
| `pkg/fleet/prompt.go` | Agent system prompt assembly |
| `pkg/fleet/progress_tracker.go` | Milestone tracking injected into agent prompts |
| `pkg/fleet/channel.go` | Channel interface definition |
| `pkg/fleet/channel_chat.go` | In-memory chat channel with pub/sub |
| `pkg/fleet/channel_github.go` | GitHub Issues channel with polling |
| `pkg/fleet/plan_activator.go` | Lifecycle management, scheduler jobs, recovery |
| `pkg/fleet/github_monitor.go` | Stateless label-based GitHub issue polling |
| `pkg/fleet/workspace.go` | Isolated workspaces via git clone --local |
| `pkg/fleet/registry.go` | Fleet definition CRUD |
| `pkg/fleet/plan_registry.go` | Fleet plan CRUD |
| `pkg/fleet/session_registry.go` | Active session tracking and message routing |
| `pkg/fleet/credentials.go` | Credential resolution for fleet agents |

## Interactions

- **Agent Engine**: Fleet agents run as sub-agents via `SubAgentManager` with custom prompts and override tools.
- **Sandbox**: Each fleet agent gets its own sandbox container with a workspace. Tools are sandbox-wrapped per-agent.
- **Channels**: Chat channel for Studio UI; GitHub Issues channel for automated workflows.
- **Scheduler**: PlanActivator creates scheduler jobs for GitHub issue polling.
- **Credentials**: Fleet plans declare required credentials, resolved from the encrypted store. Environment injection uses type-specific fields such as `token` or `password`; file injection should use `raw_content` credentials with `field: content` for JSON/YAML/dotenv/text payloads. Older plans that stored file payloads as `api_key.value` continue to work for compatibility.
- **Memory**: Agent context uses memory-scoped messages, not the general memory store.
- **API/Studio**: Fleet view in Studio shows session status, message stream, and controls.
