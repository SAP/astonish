<div align="center">
<img src="https://raw.githubusercontent.com/SAP/astonish/main/images/astonish-logo-only.svg" width="200" height="200" alt="Astonish Logo">

# Astonish

### AI agent platform that makes your whole team smarter

_When one person solves a problem, everyone benefits. Multi-tenant. Three-tier memory. Enterprise-ready. Built in Go._

[![Documentation](https://img.shields.io/badge/Documentation-Astonish-purple.svg)](https://sap.github.io/astonish/)
[![Lint](https://github.com/SAP/astonish/actions/workflows/lint.yml/badge.svg)](https://github.com/SAP/astonish/actions/workflows/lint.yml)
[![Build Status](https://github.com/SAP/astonish/actions/workflows/build.yml/badge.svg)](https://github.com/SAP/astonish/actions/workflows/build.yml)
[![License: Apache-2.0](https://img.shields.io/badge/License-Apache--2.0-blue.svg)](LICENSE)
[![REUSE status](https://api.reuse.software/badge/github.com/SAP/astonish)](https://api.reuse.software/info/github.com/SAP/astonish)

<br>

_Learn how Astonish works through the fable of "The Village of a Thousand Notebooks":_

<a href="https://www.youtube.com/watch?v=0BSrnA5mUUk" title="Watch: Astonish — The Village of a Thousand Notebooks">
  <img src="./images/astonish-presentation-video.webp" alt="Astonish — The Village of a Thousand Notebooks" width="720">
</a>

</div>

---

Astonish is a multi-tenant AI agent platform for teams and organizations. It solves problems dynamically using LLM-driven tool-use loops, distills successful interactions into reusable flows, and builds live interactive apps from plain English descriptions. Knowledge compounds across every conversation, every team member, every day — when someone solves a tricky problem, the solution flows into team memory and everyone benefits.

Run it as a shared platform backed by PostgreSQL for your organization, or standalone with SQLite for personal use. Same binary, same features, your choice.

---

## Quick Start

**Personal mode** — zero config, single user, SQLite:

```bash
brew install SAP/astonish/astonish   # or: curl -fsSL https://raw.githubusercontent.com/SAP/astonish/refs/heads/main/install.sh | sh
astonish setup                              # Configure your AI provider
astonish chat                               # Start chatting
```

**Platform mode** — multi-tenant, PostgreSQL, teams:

```bash
export ASTONISH_PLATFORM_DSN="postgres://user:pass@host:5432/astonish"
astonish platform init                      # Initialize database, create first org
astonish platform org invite admin@co.com   # Invite team members

# Team members connect remotely:
astonish login https://your-astonish-server.com
astonish chat
```

---

## Platform Mode

Astonish turns from a personal AI assistant into your team's collective intelligence. Everyone keeps their private workspace, but hard-won knowledge flows to everyone who needs it.

Imagine someone on your team spends an hour debugging a tricky Kubernetes networking issue. The solution goes into team memory. Next week, when another teammate hits the same problem, Astonish already knows the answer — no wasted time, no repeated work. Knowledge compounds across every conversation, every team member, every day.

**Teams & Organizations.** Create orgs, invite members, assign roles (owner, admin, member). Each person gets a private workspace while sharing team-level resources. Multiple teams within one organization, each with their own scope and configuration.

```bash
astonish platform org create engineering
astonish platform org invite --role admin alice@company.com
astonish platform org invite --team backend bob@company.com
```

**Private-First Data Ownership.** Sessions, flows, apps, and credentials are personal by default. Share to the team with one-click Publish. Fork team resources into your personal space to customize. You control what's shared and what stays private.

**Three-Tier Memory.** Personal, team, and org-level knowledge searched together with intelligent weighting. Hybrid search powered by pgvector + keyword matching. When someone on your team documents a solution, it surfaces automatically for anyone who hits the same problem — no manual knowledge-base maintenance needed.

| Tier         | Scope              | Weight | Examples                                      |
| ------------ | ------------------ | ------ | --------------------------------------------- |
| Personal     | Your sessions only | 1.2x   | Your debugging notes, personal preferences    |
| Team         | Shared within team | 1.0x   | Team runbooks, shared solutions, conventions  |
| Organization | All teams in org   | 0.8x   | Company-wide policies, architecture decisions |

**Cascading Defaults.** AI providers, MCP server configurations, skills, and sandbox templates cascade from platform to org to team to personal. Teams get sensible defaults from day one while individuals can override anything. Admins set guardrails without limiting flexibility.

**Team-Scoped MCP Servers & Skills.** Admins configure integrations at the org or team level. Six enforcement points guarantee full tenant isolation — no data leaks between teams. New team members get instant access to the tools they need.

---

## Enterprise Security

Built for organizations that take security seriously. Every layer is designed for multi-tenant isolation.

**Authentication.** JWT-based auth with secure HTTP-only cookies, bcrypt passwords, OIDC/SSO federation (any OpenID Connect provider), refresh token revocation, and CSRF protection. OIDC group claims auto-map to Astonish team memberships — connect your identity provider and team structure syncs automatically.

**Envelope Encryption.** A master KEK protects per-organization data encryption keys (DEKs). Credentials are encrypted with AES-256-GCM using the org's DEK. Secrets resolve personal-first with team fallback — isolation is structural, not just policy.

```
Master Key (KEK)  →  Per-Org DEK  →  Credential Data (AES-256-GCM)
                     (stored encrypted)
```

**Per-Team Sandboxes.** Each organization gets network-isolated execution environments. Two backends supported:

| Backend        | Isolation                               | Use Case                |
| -------------- | --------------------------------------- | ----------------------- |
| **Incus**      | Per-org bridge networks, LXC containers | Self-hosted, bare metal |
| **Kubernetes** | NetworkPolicies, org/team pod labels    | Cloud-native, scalable  |

Team admins customize container templates and get terminal access for configuration. Sandbox templates cascade: personal > team > org > global.

**Audit Logging.** Append-only, team-scoped audit trail. UPDATE and DELETE are revoked at the database level on audit tables — immutable by design. Admins see what's happening across their workspace with filtering by action, resource, and time range.

**PostgreSQL Backend.** Full database backbone with org-level databases and team schemas. Database-per-org isolation ensures one organization can never access another's data, even at the SQL level. Also runs on SQLite for development and personal use with the same multi-tenant semantics.

---

## Autonomous Chat Agent

The core of Astonish is a dynamic agent that uses LLM-driven tool-use loops to solve problems. It decides which tools to call, chains them together, and works through multi-step tasks autonomously.

**58+ built-in tools** — shell execution (PTY-backed), file operations, web fetching, PDF reading, semantic memory, browser automation, email, credential management, sub-agent delegation, and skill lookup.

**MCP native.** Any MCP server works out of the box — GitHub, Slack, databases, or any other MCP-compatible tool. In platform mode, admins configure MCP servers at the team level and everyone gets access automatically.

**15+ AI providers.** OpenAI, Anthropic, Google Gemini, Groq, OpenRouter, xAI, Ollama, LM Studio, SAP AI Core, LiteLLM, and more. Cascading provider defaults mean teams get the right model without individual configuration.

**Sub-agent delegation.** For complex tasks, the agent delegates work to up to 10 parallel sub-agents with isolated sessions and filtered tool access.

```bash
astonish chat                          # New session
astonish chat -p anthropic -m claude-4  # Specific provider/model
astonish chat --resume                 # Resume last session
```

### Code Mode

Your AI pair programmer that understands your entire codebase before writing a single line. `astonish code` runs locally — no daemon, no server, no login — executing the full agent loop in-process with direct access to your machine. Think Claude Code, Codex, or Cursor, but with a pre-computed knowledge graph that makes it **genuinely faster and smarter**.

```bash
astonish code                          # Start coding in the current directory
astonish code -m openai:gpt-4o         # Pin a provider/model
astonish code -C ./my-project          # Operate in a specific directory
astonish code --auto-approve           # Bypass tool & folder authorization prompts (a.k.a. --yolo)
```

**Why it's different: codegraph-powered navigation.** While other coding agents grep and read files hoping to find the right context, Astonish queries a [pre-computed knowledge graph](https://github.com/colbymchenry/codegraph) of your repository — symbols, call edges, dependencies, blast radius — all resolved in 1–4 calls. The agent knows exactly what to read, what to change, and what else will break. No wasted tokens exploring. No missed callers. No half-baked plans.

**Three operating modes** — cycle with `shift+tab`:

| Mode | Border | Behavior |
|------|--------|----------|
| **Normal** | gray | Full tool access — the agent reads, writes, builds, and tests |
| **Plan** | amber | Graph-optimized planning — investigates the codebase read-only, then produces a structured, dependency-first execution plan |
| **Ask** | green | Research-only Q&A — explore architecture, ask questions, discuss approaches without changing anything |

#### Plan Mode — Think Before You Act

The killer feature. Instead of jumping straight into edits, Plan mode uses the codegraph to map out the full blast radius of a change before touching a single file:

1. **GRAPH** — Query the knowledge graph for symbols, callers, and dependencies.
2. **READ** — Read only the files the graph identified as relevant.
3. **GAP** — Fill genuine gaps with grep/find/references (only if the graph couldn't answer).
4. **PLAN** — Produce a finalized, dependency-first execution plan with per-phase verification.

The result: plans that are **faster** (fewer tool calls), **cheaper** (lower token usage), and **more complete** (nothing left orphaned or unwired). Plans persist to a session `PLAN.md` that survives context compaction — pick up exactly where you left off.

#### Ask Mode — Understand Before You Decide

Sometimes you need answers, not changes. Ask mode gives the agent full read-only access to your codebase — files, search, codegraph, memory — but disables all mutating tools. Use it to understand architecture, trace data flows, discuss tradeoffs, or ask "how does X work?" before committing to an approach.

---

**Safety by default.** Tools run with your own permissions (no sandbox). File-modifying and command-running tools ask before executing; access to paths outside the project directory asks first. Each prompt offers **Allow**, **Always Allow**, or **Deny**. Use `--auto-approve` / `--yolo` to bypass both prompts when you trust the agent fully.

**Zero-config startup.** Code mode opens even without a configured model. Type `/model` to choose a provider and model (saved to `~/.config/astonish/config.yaml`), or `/provider` to add one from scratch — no database needed.

**Project-aware context.** On startup, Astonish loads `AGENTS.md` (or `CLAUDE.md` as fallback) from your project — walking up from the working directory to the repo root — plus a global `~/.config/astonish/AGENTS.md`. Your conventions, build commands, and project rules are always in context.

**Per-project history.** Conversations are saved to disk and kept separate per project directory. Type `/sessions` to browse and resume any previous session. `/rollback` reverts both the conversation and file changes to any earlier point.

**Dual-backend switching.** When logged in to a platform, press `Ctrl+\` to switch between local code (orange accent) and platform chat (blue accent) within the same TUI — your local coding agent and your team's collective intelligence, one keystroke apart.

---

## Generative UI (Apps)

Describe any dashboard, tool, or interactive app in plain English and Astonish builds it live in the chat. Powered by React 19 and Tailwind CSS v4.

```
You:    "Build me a project tracker with task priorities and due dates"
Agent:  [Live interactive app appears in chat]

You:    "Add a chart showing tasks completed per week"
Agent:  [App updates with a Recharts bar chart]

You:    "Save it"
Agent:  Saved as "project_tracker" — open anytime from the Apps tab.
```

- **`useAppData`** — connect to MCP tools, REST APIs (with OAuth credentials), or static config
- **`useAppAI`** — embed one-shot LLM calls for summarization, classification, or analysis
- **`useAppState`** — persistent database per app that survives page reloads; build CRUD apps, not just dashboards
- **Iterative refinement** — ask for changes and the app updates in place
- **Security sandboxed** — isolated iframe, opaque origin, SSRF-protected server-side proxy

Save once, share with your team. No coding required, no infrastructure to manage.

---

## Flow Distillation

The agent doesn't just solve problems — it learns from how it solved them, and the team benefits.

**Solve freely.** The agent works through your request dynamically, calling whatever tools it needs. The entire execution is traced.

**Distill.** After a successful multi-step task, run `/distill`. The agent analyzes its execution trace and generates a reusable YAML flow — parameterized, validated, ready to share.

**Share & Reuse.** Distilled flows become team knowledge. Version control them, review in PRs, schedule as recurring jobs, or edit visually in Studio. Next time anyone on the team hits a similar problem, the agent uses the saved flow as context.

```
You:    "Check how much memory my proxmox server has"
Agent:  [shell_command] ssh root@192.168.1.100 free -h
        Your server has 64GB total, 42GB used, 22GB free.

You:    /distill
Agent:  Saved: check_server_health.yaml
        Run again: astonish flows run check_server_health -p host="192.168.1.100"
```

```bash
# Schedule it, share it, reuse it
astonish flows run check_server_health -p host="192.168.1.100"
astonish scheduler add --cron "0 9 * * *" --flow check_server_health
astonish tap add yourteam/shared-flows   # Community flow store
```

---

## Multi-Tenant Channels & Remote CLI

**Channels.** Telegram, Email, and Slack adapters work in platform mode with database-backed allowlists and dynamic per-message routing. Email supports plus-addressing for per-org routing (`bot+orgname@domain.com`). In-channel `/org` and `/team` commands switch context on the fly.

**Remote CLI.** A rich terminal client to interact with your platform remotely. Login with password or SSO, select your org and team, then use all Astonish commands as if local — list sessions, run flows, browse traces, chat with full lipgloss styling.

```bash
astonish login https://platform.yourcompany.com   # SSO or password
astonish status                                    # Show connection info
astonish chat                                      # Chat through the platform
astonish flows list                                # Browse team flows
```

**Always-On Daemon.** Run as a system service (launchd/systemd) with a cron scheduler. Two execution modes: routine (headless flow replay) and adaptive (natural language instruction interpreted each run). Results broadcast to connected channels.

---

## Skills, Browser & Studio

**Skills System.** Markdown files that teach the agent CLI tools, APIs, and workflows. 9 bundled skills, community skills via [ClawHub](https://clawhub.com), and in platform mode skills are scoped to teams with live-reload. Drop a `SKILL.md` and the agent knows how to use it.

**Browser Automation.** 32 browser tools in pure Go via Chrome DevTools Protocol. Navigation, interaction, screenshots, device emulation, stealth mode. No Node.js, no Playwright, zero external dependencies.

**Astonish Studio.** Visual flow designer with AI assistant, drag-and-drop editing, real-time execution, and the Apps tab for managing generative UI creations. Plan auto-progression shows agent execution in real-time. Token usage tracking with per-call visibility.

```bash
astonish studio    # Opens at http://localhost:9393
```

---

## Supported AI Providers

| Provider          | Type        | Notes                                |
| ----------------- | ----------- | ------------------------------------ |
| **OpenRouter**    | Cloud       | 100+ models with one API key         |
| **OpenAI**        | Cloud       | GPT-4o, o1, o3                       |
| **Anthropic**     | Cloud       | Claude Opus, Sonnet, Haiku           |
| **Google Gemini** | Cloud       | Gemini Pro, Flash                    |
| **Groq**          | Cloud       | Ultra-fast inference                 |
| **xAI**           | Cloud       | Grok models                          |
| **LiteLLM**       | Cloud/Local | Unified interface for 100+ providers |
| **Ollama**        | Local       | Self-hosted open models              |
| **LM Studio**     | Local       | Self-hosted with GUI                 |
| **SAP AI Core**   | Cloud       | SAP enterprise AI                    |

Configure via `astonish setup` or Studio Settings. In platform mode, provider defaults cascade from org to team to individual.

---

## Architecture

```mermaid
flowchart TB
    subgraph Platform["Platform Layer"]
        direction TB
        P1["PostgreSQL (prod) / SQLite (dev)"]
        P2["JWT + OIDC Authentication"]
        P3["Envelope Encryption"]
        P4["Audit Logging"]
    end

    subgraph Org["Organization"]
        direction TB
        O1["Org-level memory & settings"]
        O2["Org encryption keys (DEKs)"]
        O3["Network-isolated sandboxes"]
    end

    subgraph Team["Team"]
        direction TB
        T1["Team memory & flows"]
        T2["Scoped MCP servers & skills"]
        T3["Cascading provider defaults"]
        T4["Channels & routing"]
    end

    subgraph Personal["Personal Workspace"]
        direction TB
        U1["Private sessions & credentials"]
        U2["Personal memory & apps"]
        U3["Publish / Fork"]
    end

    subgraph Agent["Agent Engine"]
        direction TB
        A1["LLM tool-use loops (58+ tools)"]
        A2["Sub-agent delegation"]
        A3["Flow distillation"]
        A4["Generative UI (React apps)"]
    end

    Platform --> Org
    Org --> Team
    Team --> Personal
    Personal --> Agent
    Team --> Agent
    Org --> Agent
```

Built on [Google's Agent Development Kit (ADK)](https://github.com/google/adk-go). The platform supports database-per-org isolation with schema-per-team. Resources cascade downward; data ownership flows upward only when explicitly published.

---

## Use Cases

- **Team Runbooks & SOPs.** One engineer solves a complex deployment issue; the distilled flow becomes a team resource anyone can run or reference.
- **Onboarding Acceleration.** New hires inherit months of team memory on day one. The agent already knows your infrastructure, conventions, and past solutions.
- **Internal Tools & Dashboards.** Describe what you need, get a live React app with charts and data connections. Share with the team — no frontend setup required.
- **Enterprise DevOps.** Shared knowledge across Kubernetes clusters, CI/CD pipelines, and infrastructure. Team-scoped credentials and sandboxes keep access controlled.
- **Scheduled Automation.** Cron jobs through conversation, results delivered to Telegram, Email, or Slack. Team-wide visibility into what's running.
- **Multi-Channel Support.** Interact from terminal, Studio, Telegram, Email, or Slack — all connected to the same platform with consistent context.

---

## Contributing & Support

- [Full Documentation](https://sap.github.io/astonish/)
- [Testing Guide](docs/TESTING.md) — 1,650+ tests across 4 layers
- [Submit a Pull Request](https://github.com/SAP/astonish/pulls)
- **License**: Apache-2.0

<div align="center">
<b><a href="https://github.com/SAP/astonish">Star us on GitHub</a></b>
</div>

## Licensing

Copyright 2026 SAP SE or an SAP affiliate company and astonish contributors. Please see our [LICENSE](LICENSE) for copyright and license information. Detailed information including third-party components and their licensing/copyright information is available [via the REUSE tool](https://api.reuse.software/info/github.com/SAP/astonish).

This project was originally developed and maintained by [schardosin](https://github.com/schardosin). Since 2026-07-15, Astonish has been maintained by SAP.
