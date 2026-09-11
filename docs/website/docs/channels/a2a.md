# Inbound A2A Server

Astonish can expose an Agent-to-Agent (A2A) JSON-RPC endpoint for other agents to call:

```text
POST https://astonish.example.com/api/a2a
```

Inbound A2A is a dedicated OAuth-protected execution endpoint. It is **not** a Telegram/Email/Slack-style channel and does not appear in Channels administration or under the `channels` configuration key.

> This page covers agents calling Astonish. To configure Astonish to call remote A2A agents, see [A2A Agents](../configuration/a2a-agents.md).

## Discovery

The A2A Agent Card is public at:

```text
GET https://astonish.example.com/.well-known/agent-card.json
```

The card describes Astonish's A2A capabilities and bearer authentication requirement. Public discovery does not grant permission to call the endpoint.

## Grant access with OAuth

1. Open **Settings → OAuth**.
2. Create a client or edit an existing client.
3. Select **A2A access** in its allowed permissions.
4. Complete the applicable OAuth grant flow to obtain a new Astonish access token.
5. Send that token as `Authorization: Bearer <token>` to `/api/a2a`.

The OAuth settings view shows the canonical A2A endpoint for the installation.

A token must be issued by this Astonish installation, target its configured protected-resource audience, and include the exact `a2a` scope. Adding the scope to a client does not alter previously issued tokens; authorize or issue a new token.

`a2a` is independent from `chat` and `tool:execute`. A token carrying either of those scopes alone cannot call A2A, and an A2A token does not gain chat or MCP tool access unless those scopes are separately granted.

## Calling the endpoint

Send JSON-RPC 2.0 requests. For example:

```bash
curl -X POST https://astonish.example.com/api/a2a \
  -H 'Authorization: Bearer <astonish-a2a-token>' \
  -H 'Content-Type: application/json' \
  --data '{
    "jsonrpc": "2.0",
    "id": "example-1",
    "method": "message/send",
    "params": {
      "message": {
        "role": "user",
        "parts": [{"kind": "text", "text": "Hello from another agent"}]
      }
    }
  }'
```

The endpoint supports A2A task operations including synchronous message completion, return-immediately processing with task retrieval, streaming, cancellation, and push-notification configuration. Task access is bound to the authenticated OAuth principal.

## Security behavior

The endpoint rejects a request before protocol execution when its bearer token is missing, expired, invalid, issued by a different issuer, addressed to the wrong audience, lacks `a2a`, or cannot resolve a tenant principal. Astonish does not accept raw upstream IdP/JWKS tokens directly for inbound A2A.

There is no inbound `channels.a2a` configuration, trusted-issuer list, allowed-agent list, or A2A user-channel link to configure. Legacy `channels.a2a` JSON is ignored and is removed when Channels settings are next saved.

## Related documentation

- [OAuth authorization](../configuration/oauth.md)
- [A2A Agents](../configuration/a2a-agents.md) — outbound connections to remote agents
- [Telegram](./telegram.md), [Email](./email.md), and [Slack](./slack.md) — messaging channels
