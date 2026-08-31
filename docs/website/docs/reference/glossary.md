# Glossary

Alphabetical definitions of key terms used throughout the Astonish documentation.

| Term | Definition |
|------|------------|
| **Agent** | An AI entity configured with a system prompt, model, and tools that processes user requests and takes actions. |
| **App** | A generated application (HTML/JS/CSS) produced by the agent and previewable in Studio. |
| **Cascading Defaults** | Configuration inheritance: org defaults → team defaults → user settings. Lower levels override higher. |
| **Channel** | A communication adapter connecting an external platform (Telegram, Email, Slack) to the agent engine. See [Channels](../channels/). |
| **Credential Store** | Encrypted storage for sensitive values (API keys, tokens) used by tools and integrations. |
| **DEK** | Data Encryption Key. Per-record symmetric key used to encrypt sensitive data. Itself encrypted by the KEK. |
| **Distillation** | The process of compressing long conversation histories into concise summaries for memory storage. |
| **Envelope Encryption** | Two-layer encryption scheme: data is encrypted with a DEK, and the DEK is encrypted with a KEK. |
| **Fleet** | Multi-agent coordination system where teams of specialized agents collaborate on complex missions. See [Fleet](../fleet/). |
| **Flow** | A defined sequence of agent actions, tool calls, and logic that executes as a pipeline. See [Flows](../flows/). |
| **Fork** | Creating a new session that branches from a specific point in an existing conversation. |
| **Hub Agent** | The coordinating agent in a fleet that assigns tasks to spokes and synthesizes results. |
| **KEK** | Key Encryption Key. Master key used to encrypt DEKs. Stored separately from encrypted data. |
| **MCP** | Model Context Protocol. A standard for connecting external tool servers to AI agents. |
| **Memory (Personal)** | Private memory tier storing facts and preferences for a single user. |
| **Memory (Team)** | Shared memory tier accessible to all members of a team. |
| **Memory (Org)** | Shared memory tier accessible across an entire organization. |
| **Node** | A single step in a flow: an agent call, tool invocation, condition, or transformation. |
| **Organization** | Top-level tenant in cloud deployments. Contains teams, users, and shared resources. |
| **Personal Workspace** | A user's private space within the platform containing their sessions, memory, credentials, and config. |
| **Plan** | A mission instance in fleet — created from a template with a specific objective and tracked through completion. |
| **Publish** | Making a flow or app available to other team/org members. |
| **Remote CLI** | Using the Astonish CLI against a remote platform instance rather than a local setup. |
| **RLS** | Row-Level Security. PostgreSQL feature ensuring tenants only access their own data. |
| **Sandbox** | Isolated execution environment for running agent-generated code safely. |
| **Session** | A conversation between a user and an agent (or between two fleet agents) with persistent message history. |
| **Skill** | A reusable capability package that can be attached to an agent (tools + prompt instructions). |
| **Slide deck** | A presentation generated and refined in Chat. It begins as a session draft and becomes a permanent personal or team deck after an explicit save. See [Slides](../slides/). |
| **Spoke Agent** | A specialist agent in a fleet that receives tasks from the hub and reports results back. |
| **Sub-agent** | An agent invoked by another agent to handle a delegated subtask within a single session. |
| **Tap** | A passive listener that observes agent activity for logging, analytics, or compliance without altering behavior. |
| **Team** | A group of users within an organization who share memory, flows, and agent configurations. |
| **Three-Tier Memory** | The memory architecture with personal, team, and organization levels. |
