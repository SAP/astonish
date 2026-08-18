# A2A Agents — Connecting to External Agents

Astonish can connect to external agents that implement the **A2A (Agent-to-Agent) protocol**. Once configured, the remote agent's capabilities appear as tools your Astonish agent can call — just like MCP server tools, but backed by a full autonomous agent on the other end.

## What Is A2A?

The [Agent-to-Agent (A2A) protocol](https://github.com/google/A2A) is an open standard for AI agents to communicate with each other. It defines:

- **Agent Cards** — a discovery mechanism where agents advertise their capabilities at `/.well-known/agent-card.json`
- **Skills** — specific capabilities an agent offers (similar to tools)
- **Tasks** — units of work with lifecycle states (submitted → working → completed)
- **Streaming** — real-time updates via Server-Sent Events (SSE)

When you connect Astonish to an external A2A agent, Astonish acts as a **client** — it discovers the remote agent's skills and makes them available as tools.

## Why Connect to External Agents?

- **Specialized expertise** — connect to agents trained for specific domains (code review, translation, data analysis)
- **Organizational agents** — integrate with internal agents your team has built
- **Distributed workflows** — let your Astonish agent delegate sub-tasks to specialized agents
- **Vendor integrations** — use third-party AI services that expose an A2A endpoint

## Configuration

### Personal Mode (a2a_agents.json)

Create or edit `~/.config/astonish/a2a_agents.json`:

```json
{
  "a2aAgents": {
    "code-review": {
      "url": "https://code-review-agent.example.com",
      "credential_name": "code-review-token",
      "auth_type": "bearer",
      "timeout": "60s"
    },
    "translator": {
      "url": "https://translate-agent.internal:8080",
      "auth_type": "api_key",
      "credential_name": "translate-api-key"
    }
  }
}
```

| Field | Required | Description |
|-------|----------|-------------|
| `url` | ✅ | Base URL of the remote A2A agent |
| `credential_name` | | Name of credential in your credential store |
| `auth_type` | | Authentication type: `bearer` (default), `api_key`, or `oauth` |
| `enabled` | | Set to `false` to temporarily disable (defaults to `true`) |
| `headers` | | Additional HTTP headers as key-value pairs |
| `timeout` | | Request timeout (e.g. `"30s"`, `"2m"`) — default 30s |

### Platform Mode (UI / API)

In platform mode, A2A agents are managed through the Studio UI (Settings → A2A Agents) or the REST API. They follow the same cascade as MCP servers:

```
Config file  →  Platform  →  Organization  →  Team
 (defaults)    (overrides)    (overrides)    (wins)
```

Each tier can define agents. When names collide, the closest tier to the user wins. A team admin can override an org-level agent by creating a team-level entry with the same name.

## Authentication Setup

A2A agents typically require authentication. Astonish uses the **credential store** to manage secrets securely.

### Step 1: Add a Credential

Using the CLI:

```bash
# Bearer token
astonish credentials set code-review-token --type bearer_token --value "your-api-token"

# API key
astonish credentials set translate-api-key --type api_key --value "sk-your-key"
```

Or via Studio Settings → Credentials.

### Step 2: Reference in Agent Config

Use the credential name (not the value!) in your A2A agent configuration:

```json
{
  "a2aAgents": {
    "code-review": {
      "url": "https://code-review-agent.example.com",
      "credential_name": "code-review-token",
      "auth_type": "bearer"
    }
  }
}
```

The actual token is resolved at call time from your credential store — it never appears in the config file or API responses.

### Supported Auth Types

| Type | Header Sent | Use When |
|------|-------------|----------|
| `bearer` | `Authorization: Bearer <token>` | Most common; API tokens, JWTs |
| `api_key` | `X-API-Key: <key>` | Services using API key auth |
| `oauth` | `Authorization: Bearer <access_token>` | OAuth2 with auto-refresh |

## Example: Connecting to an External Agent

### 1. Verify the Agent Is Reachable

Check that the agent's card endpoint responds:

```bash
curl https://code-review-agent.example.com/.well-known/agent-card.json
```

You should see a JSON response with the agent's name, description, and skills.

### 2. Configure the Connection

Add to `~/.config/astonish/a2a_agents.json`:

```json
{
  "a2aAgents": {
    "code-review": {
      "url": "https://code-review-agent.example.com",
      "credential_name": "code-review-token",
      "auth_type": "bearer",
      "timeout": "60s"
    }
  }
}
```

### 3. Add the Credential

```bash
astonish credentials set code-review-token --type bearer_token --value "your-token-here"
```

### 4. Test the Connection

Via API:

```bash
curl -X POST http://localhost:52532/api/a2a-agents/code-review/test
```

Expected response:

```json
{
  "status": "ok",
  "agent_name": "Code Review Agent",
  "description": "Reviews pull requests for code quality",
  "version": "1.0.0",
  "skill_count": 3
}
```

### 5. Use in a Chat Session

Once configured, the agent's skills appear as tools. In a chat:

> "Use the code review agent to review the changes in PR #42"

Astonish will call the appropriate `a2a_code_review_*` tool automatically.

## API Reference

All endpoints are under `/api/a2a-agents`. Use `?scope=platform|org|team` to target a specific tier.

| Method | Endpoint | Description |
|--------|----------|-------------|
| `GET` | `/api/a2a-agents` | List all agents (merged view) |
| `POST` | `/api/a2a-agents` | Create a new agent connection |
| `GET` | `/api/a2a-agents/{name}` | Get agent details |
| `PUT` | `/api/a2a-agents/{name}` | Update agent configuration |
| `DELETE` | `/api/a2a-agents/{name}` | Remove agent connection |
| `PATCH` | `/api/a2a-agents/{name}` | Toggle enabled/disabled |
| `POST` | `/api/a2a-agents/{name}/refresh` | Re-fetch the agent card |
| `POST` | `/api/a2a-agents/{name}/test` | Test connectivity |

### Create/Update Request Body

```json
{
  "name": "code-review",
  "url": "https://code-review-agent.example.com",
  "credential_name": "code-review-token",
  "auth_type": "bearer",
  "enabled": true,
  "headers": {},
  "timeout": "60s"
}
```

### List Response

```json
{
  "agents": [
    {
      "name": "code-review",
      "url": "https://code-review-agent.example.com",
      "credential_name": "code-review-token",
      "auth_type": "bearer",
      "enabled": true,
      "scope": "team",
      "has_card": true,
      "skill_count": 3
    }
  ],
  "is_team_admin": true,
  "is_org_admin": false
}
```

## Troubleshooting

### Agent not appearing as tools

1. **Check enabled status** — ensure `"enabled": true` (or omit the field, which defaults to true)
2. **Refresh the card** — POST to `/api/a2a-agents/{name}/refresh` to re-fetch the agent card
3. **Verify URL** — the URL must be reachable from the Astonish server
4. **Check logs** — look for `a2aclient: warning: failed to fetch agent card` messages

### Authentication failures

1. **Verify credential exists** — `astonish credentials list` should show the credential
2. **Check credential name matches** — the `credential_name` in agent config must exactly match the credential store entry
3. **Test connectivity** — POST to `/api/a2a-agents/{name}/test` to check if the agent responds
4. **Check auth_type** — ensure it matches what the remote agent expects

### Timeout errors

- Increase the `timeout` field (e.g. `"120s"` for slow agents)
- Default is 30 seconds
- Streaming requests have no timeout

### Connection refused / DNS errors

- Verify the agent URL is correct and reachable from the Astonish server
- Check network policies / firewalls
- Ensure the remote agent is running

### "No skills" / generic tool only

- The remote agent may not advertise skills in its agent card
- A single generic `a2a_<agentName>` tool is generated in this case
- Ask the agent operator to add skills to their agent card

## Related Documentation

- [MCP Servers](../configuration/mcp-servers.md) — similar pattern for connecting to MCP tool servers
- [Credential Security](../security/credential-security.md) — how credentials are stored and protected
- [A2A Server (Channels)](../channels/a2a.md) — exposing Astonish as an A2A agent for others to call
