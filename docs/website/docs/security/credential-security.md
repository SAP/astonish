# Credential Security

Astonish provides a secure credential store that keeps API keys, tokens, and other secrets encrypted at rest and invisible to the LLM at runtime. A multi-layer redaction pipeline ensures that secrets never leak into model context, logs, or responses.

## Credential Store

Credentials are stored encrypted using [envelope encryption](./envelope-encryption.md) and scoped to either a user (personal) or a team:

| Scope | Visibility | Use Case |
|-------|-----------|----------|
| Personal | Only the owning user | Dev/testing API keys |
| Team | All team members | Shared production credentials |

### Credential Types

| Type | Description |
|------|-------------|
| `api_key` | Custom header + value for API authentication |
| `bearer` | Authorization: Bearer token |
| `basic` | HTTP Basic Auth (username + password) |
| `password` | Username + password for SSH, FTP, SMTP, databases |
| `oauth_client_credentials` | OAuth2 client credentials with automatic token refresh |
| `oauth_authorization_code` | User-authorized OAuth2 with refresh token |
| `openstack_keystone` | OpenStack Keystone v3 token authentication |
| `raw_content` | Encrypted JSON, YAML, dotenv, or text payload for sandbox file injection; not HTTP auth |

Use `raw_content` when a fleet or drill needs to materialize a configuration file in the sandbox at run time. The content stays encrypted in the credential store and is injected as `field: content` only when needed.

## Output Redaction

Astonish applies multiple independent redaction layers to guarantee secrets never reach the LLM context:

| Layer | Stage | Mechanism |
|-------|-------|-----------|
| 1 | Injection | Secrets are injected into tool call environments via placeholders, never into the prompt |
| 2 | Tool output filtering | Tool stdout/stderr is scanned; matching values are replaced with `[REDACTED]` |
| 3 | Context assembly | The context builder verifies no known secret values appear in assembled messages |
| 4 | Channel output | Outbound messages to channels (Telegram, Slack, etc.) are scanned against active credentials |
| 5 | Audit and logs | All logging pipelines strip secret values before writing |

Each layer operates independently. If one layer fails, the remaining layers still prevent exposure.

## How Secrets Reach Tools

When an agent invokes a tool that requires credentials:

1. The credential is resolved (personal-first, team fallback).
2. The encrypted ciphertext is decrypted in memory using the org's DEK.
3. The plaintext is injected as an **environment variable** or, for `raw_content`, materialized as a sandbox file requested by the fleet/drill configuration.
4. The tool reads the credential from its environment or file path.
5. After execution, the environment/container state is destroyed.

At no point does the LLM see the credential value. The model only knows that a credential named (e.g.) `GITHUB_TOKEN` is available — never its contents.

## Managing Credentials

### In Studio

Studio provides a credential manager under **Settings → Credentials** where you can add, view metadata, test, and remove credentials through a visual interface.

### Via CLI

```bash
# Add a credential
astonish credential add

# List credentials (values are never displayed)
astonish credential list

# Show credential metadata
astonish credential show <name>

# Test a credential (validates it works)
astonish credential test <name>

# Remove a credential
astonish credential remove <name>

# Set or manage the master key
astonish credential master-key
```

Credential values are never echoed back in any command output. The `list` and `show` commands display names, types, scopes, and metadata only.

## See Also

- [Envelope Encryption](./envelope-encryption.md) — how credentials are encrypted at rest
- [Sandboxes](./sandboxes.md) — the isolated execution environment where credentials are consumed
- [Audit Logging](./audit-logging.md) — all credential access is recorded
