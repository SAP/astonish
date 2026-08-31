# Introduction

Astonish is an AI agent platform that makes your whole team smarter. When one person solves a problem, the solution flows to everyone who needs it. Knowledge compounds across every conversation, every team member, every day.

Built in Go on Google's Agent Development Kit, Astonish combines autonomous tool-use agents with three-tier memory, flow distillation, generative UI, and enterprise-grade multi-tenancy. It runs as a single binary with all platform capabilities regardless of deployment size:

- **Local (SQLite)** — Runs entirely on your machine. SQLite handles storage and vector search out of the box. Full platform features with minimal setup.
- **Cloud (PostgreSQL)** — Multi-tenant with pgvector. Organizations, teams, shared memory, cascading configuration, and enterprise security for your whole team.

Same binary, same 90+ tools, same platform. Your choice of database backend.

## Core Capabilities

**Autonomous Agent Engine.** LLM-driven tool-use loops that solve problems dynamically. 90+ built-in tools spanning shell execution, file operations, web fetching, browser automation, memory, and more. Sub-agent delegation for complex multi-step tasks.

**12+ AI Providers.** OpenAI, Anthropic, Google Gemini, Groq, OpenRouter, xAI, Ollama, LM Studio, SAP AI Core, LiteLLM, and others. Switch providers per conversation or set team-wide defaults.

**MCP Native.** Any MCP-compatible server works out of the box. Admins can configure MCP servers at the team level and everyone gets instant access.

**Three-Tier Memory.** Personal, team, and organization-level knowledge stores searched together with intelligent weighting. Powered by hybrid search (vector similarity + keyword matching via FTS5 or pgvector). Solutions persist and surface automatically when relevant.

**Flow Distillation.** After solving a multi-step problem, distill the execution trace into a reusable YAML workflow. Parameterized, validated, shareable. Schedule flows, version-control them, or edit visually in Studio.

**Generative UI.** Describe a dashboard, tool, or interactive app in plain English. Astonish builds it live in the chat using React 19 and Tailwind CSS. Save and share with your team.

**Slides.** Generate complete presentations from prompts, documents, research, and connected systems. Apply built-in or imported PowerPoint templates, refine the deck with AI, present it in the browser, and export it as PPTX, PDF, or HTML. See [Slides](./slides/).

**Enterprise Security.** Envelope encryption (AES-256-GCM), OIDC/SSO federation, per-organization sandboxes (Incus or Kubernetes), immutable audit logs, and database-per-org isolation.

**Multi-Channel Access.** Studio (web UI), CLI, Remote CLI, Telegram, Email, and Slack. All channels connect to the same platform with consistent context.

**Fleet.** Multi-agent collaboration for complex missions. Delegate work across parallel sub-agents with isolated sessions and filtered tool access.

## The Platform Differentiator

Most AI tools are isolated to individual users. Astonish is built for teams from the ground up.

When someone on your team spends an hour debugging a tricky Kubernetes networking issue, the solution goes into team memory. Next week, when another teammate hits the same problem, the agent already knows the answer. No repeated work, no tribal knowledge lost in chat logs.

Resources cascade downward — provider configs, MCP servers, skills, and sandbox templates flow from platform to org to team to individual. Data ownership flows upward only when explicitly published. You control what stays private and what benefits the team.

## How It Works

```bash
# 1. Setup — configure backend and AI provider
astonish setup                    # Interactive wizard (SQLite or PostgreSQL)

# 2. Start the platform
astonish daemon install           # Register as system service
astonish daemon start             # Start the daemon

# 3. Use Astonish
# Open Studio at http://localhost:9393 and log in with your admin credentials
# Or connect via CLI:
astonish login http://localhost:9393
astonish chat                     # Start solving problems
```

The agent solves problems using autonomous tool-use loops. It selects tools, chains them together, and works through multi-step tasks without manual intervention. After a successful interaction, distill it into a reusable flow that anyone on the team can run.

```
You:    "Deploy the staging environment and run the smoke tests"
Agent:  [executes 12 tool calls across shell, git, and kubectl]
        Deployment complete. All 47 smoke tests passing.

You:    /distill
Agent:  Saved: deploy_staging.yaml
        Run again: astonish flows run deploy_staging -p env="staging"
```

## What's Next

- [Installation](./getting-started/installation.md) — Get Astonish on your machine
- [Quick Start: Local](./getting-started/quick-start-local.md) — Get up and running with SQLite
- [Quick Start: Cloud](./getting-started/quick-start-cloud.md) — Deploy for your team with PostgreSQL
- [Choose Your Interface](./getting-started/choose-your-interface.md) — Studio, CLI, Telegram, and more
- [Slides](./slides/) — Create, edit, present, share, and export presentations
- [Architecture](./getting-started/architecture.md) — Understand the layer model

## At a Glance

| Dimension | Local (SQLite) | Cloud (PostgreSQL) |
|-----------|---------------|-------------------|
| Database | SQLite (with built-in vector search) | PostgreSQL 15+ with pgvector |
| Users | Single user / small team | Multi-tenant (orgs, teams, members) |
| Memory | Personal + team tiers | Personal + Team + Organization tiers |
| Security | Envelope encryption, audit logs | Envelope encryption, OIDC/SSO, audit logs |
| Channels | Studio, CLI, Telegram, Email, Slack | Studio, CLI, Remote CLI, Telegram, Email, Slack |
| Sandboxes | Local (Incus) | Per-org network-isolated (Incus/Kubernetes) |
| Config | Platform config | Cascading (platform → org → team → personal) |

Both deployments ship in the same binary and share the same agent engine, tools, and capabilities.
