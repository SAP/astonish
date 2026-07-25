# Credential Security

## Overview

Astonish manages authentication credentials (API keys, bearer tokens, passwords, OAuth flows, OpenStack Keystone) with a core design principle: **the LLM provider must never see raw secret values**. Credentials are stored encrypted at rest, referenced by placeholder tokens in conversation history, and substituted with real values only at the moment of tool execution.

This architecture has three layers of defense:

1. **Placeholder substitution**: The LLM sees `{{CREDENTIAL:name:field}}` tokens instead of real values. Real values are injected just before tool execution and restored to placeholders immediately after.
2. **Secret tagging**: Users wrap secrets in `<<<value>>>` when providing them in chat. The system extracts raw values before the LLM sees the message, replacing them with `<<<SECRET_N>>>` tokens.
3. **Redaction**: A multi-encoding scanner catches any credential values that leak through other pathways, replacing them with `[REDACTED:name]` markers.

## Key Design Decisions

### Why Placeholder Substitution Instead of Tool-Level Resolution

The initial approach had `resolve_credential` return the actual secret values, which the LLM would then pass to tool args. This leaked secrets into the conversation history (session events stored the tool args containing real values), and on subsequent turns the LLM would replay the full history -- sending secrets to the LLM provider.

The current design has `resolve_credential` return placeholders like `{{CREDENTIAL:my-api:password}}`. The LLM passes these tokens to tool args naturally. The `BeforeToolCallback` replaces placeholders with real values in-place on the shared args map, the tool executes with real values, and the `AfterToolCallback` restores the original placeholders. The session event (which holds the same args map by reference) always contains only placeholders.

### Why The ADK Shared Args Map Matters

This is a critical implementation detail. ADK's `base_flow.go` creates a session event with a `Content` pointer, then extracts the `Args` map from the same `Content` for the tool callback. The event is stored by pointer, with no deep copy. This means:

- Mutating the args map in `BeforeToolCallback` **also mutates the stored session event**.
- If real values aren't restored in `AfterToolCallback`, they persist in the session transcript.
- On the next LLM turn, ADK rebuilds conversation history from these events, sending real credentials to the provider.

The `SubstituteAndRestore()` function is specifically designed for this: it snapshots original placeholder values before substitution and returns a closure that puts them back.

### Why Triple Angle Brackets for Secret Tagging

When users need to provide a new password or API key in chat, they can't use `{{CREDENTIAL:...}}` placeholders (those reference existing credentials). The `<<<value>>>` syntax was chosen for several reasons:

- **Very low collision risk**: Triple angle brackets almost never appear in code, config files, or natural language. Unlike single or double angle brackets (common in HTML/XML/templates), triple brackets have no established meaning.
- **User-memorable**: Simple to type and visually distinct.
- **Regex-friendly**: Easy to match with `<<<(.+?)>>>` without ambiguity.

### Why AES-256-GCM Encryption

Credentials are encrypted at rest using AES-256-GCM (authenticated encryption). The encryption key is auto-generated on first use and stored in `~/.config/astonish/.store_key` (hex-encoded). This provides:

- Confidentiality: Even if the credential file is copied, it can't be read without the key.
- Integrity: GCM's authentication tag detects tampering.
- Simplicity: No external key management service needed for a local tool.

A master key (argon2id-derived) can optionally gate credential reveals to humans, adding a second layer for shared environments.

### Why Multi-Encoding Redaction

The Redactor tracks each secret in three forms: raw, base64-encoded, and URL-encoded. This catches common transformations:

- **Raw**: Direct string match (e.g., `my-secret-key`).
- **Base64**: Catches `Authorization: Basic <base64(user:pass)>` and similar encodings.
- **URL-encoded**: Catches secrets embedded in URLs (e.g., `password=my%2Dsecret%2Dkey`).

Minimum signature length is 8 characters to avoid false positives with short values.

## Architecture

### Credential Lifecycle

```
1. Creation:
   User: "Save my API key <<<sk-abc123>>>"
     |
     v
   PendingVault.Extract(): "Save my API key <<<SECRET_1>>>"
     (raw value "sk-abc123" registered with Redactor as safety net)
     |
     v
   LLM calls save_credential(name="my-api", type="api_key", value="<<<SECRET_1>>>")
     |
     v
   BeforeToolCallback: resolves <<<SECRET_1>>> -> "sk-abc123"
   Tool stores encrypted credential
   AfterToolCallback: restores <<<SECRET_1>>> in args
     |
     v
   RedactSessionFunc: retroactively scans and redacts the entire session transcript

2. Usage:
   LLM calls resolve_credential(name="my-api")
     |
     v
   Returns: { header: "X-API-Key", value: "{{CREDENTIAL:my-api:value}}" }
     (non-secret fields like header name returned as plaintext)
     |
     v
   LLM calls http_request(url="...", headers={"X-API-Key": "{{CREDENTIAL:my-api:value}}"})
     |
     v
   BeforeToolCallback: {{CREDENTIAL:my-api:value}} -> "sk-abc123"
   Tool executes HTTP request with real value
   AfterToolCallback: restores {{CREDENTIAL:my-api:value}}
```

### Credential Types

| Type | Use Case | Secret Fields | Non-Secret Fields |
|---|---|---|---|
| `api_key` | Custom header auth | `value` | `header` |
| `bearer` | Authorization: Bearer | `token` | -- |
| `basic` | Authorization: Basic | `password` | `username` |
| `password` | SSH, FTP, DB, SMTP | `password` | `username` |
| `oauth_client_credentials` | Machine-to-machine OAuth2 | `client_secret` | `client_id`, `auth_url`, `scope` |
| `oauth_authorization_code` | User-consent OAuth2 (Google, GitHub) | `client_secret`, `access_token`, `refresh_token` | `client_id`, `token_url`, `scope` |
| `openstack_keystone` | OpenStack Keystone v3 | `password` or `application_credential_secret` | `auth_url`, `username` / `application_credential_id`, project/domain fields |
| `raw_content` | Arbitrary JSON/YAML/dotenv/text payloads for sandbox file materialization | `content` | `content_type` |

Non-secret fields are returned as plaintext because the LLM needs them for decision-making (e.g., knowing which header name to use, which auth URL to call). `raw_content` is intentionally not an HTTP credential: `Resolve()` rejects it, and fleet/drill file injection should reference `field: content`. The `content` value is not normalized or trimmed because exact file bytes, whitespace, and trailing punctuation can be meaningful.

### OAuth Token Management

For OAuth credentials, the store handles automatic token refresh:

1. `Resolve()` checks if the access token has expired (with 30-second buffer).
2. If expired and a refresh token exists, it performs the refresh flow.
3. New access token (and potentially rotated refresh token) are persisted to disk.
4. The token cache (`tokenCache`) prevents concurrent refresh requests for the same credential.
5. Refreshed tokens are registered with the Redactor for ongoing redaction.

### OpenStack Keystone Token Management

`openstack_keystone` mirrors OAuth client_credentials for Keystone v3:

1. Method is inferred from fields: `application_credential_id`+`application_credential_secret`, else `username`+`password` with project scope.
2. `Resolve()` POSTs JSON to `auth_url`, reads the token from the `X-Subject-Token` response header, and parses `token.expires_at`.
3. Tokens are cached in-memory (same cache as OAuth) with a 30-second expiry buffer.
4. Injection uses `X-Auth-Token: <token>` (not Bearer).
5. `InvalidateToken` on 401 forces a fresh fetch on the next resolve.

### Redaction Pipeline

```
Tool Output -> Redactor.RedactMap()
  |
  v
For each string value in the map:
  - Scan for all known secret signatures (raw, base64, URL-encoded)
  - Replace matches with [REDACTED:credential-name]
  |
  v
SSE Stream -> Redactor.Redact() on text events
  |
  v
Session Persist -> FileStore redacts before writing to disk
```

The Redactor is thread-safe (RWMutex-protected) and operates as a longest-match-first scanner to prevent partial replacements.

### Retroactive Session Redaction

After `save_credential` succeeds, the system retroactively redacts the session transcript:

1. `FileStore.RedactSession()` re-reads the entire `.jsonl` transcript.
2. All occurrences of the new secret values (in all encodings) are replaced.
3. The redacted transcript is written back to disk.
4. In-memory session events are also redacted.

This catches the case where the user provided a secret in plaintext earlier in the conversation (before `<<<>>>` tagging was available or before the credential was formally saved).

### Migration From Plaintext Config

Legacy installations may have provider API keys in `config.yaml`. The credential store provides an idempotent migration:

1. On first open, checks for known plaintext keys in the config (ANTHROPIC_API_KEY, OPENAI_API_KEY, etc.).
2. Moves them into the encrypted store as flat secrets.
3. Sets `migrated: true` in the store data to prevent re-migration.
4. The original config values can then be removed.

### Personal vs Team Credentials (Platform Mode)

Interactive Studio chat injects `MergedCredentialStore(personal, team)` — personal-first reads, personal writes, team fallback.

Headless paths diverge by design:

| Path | Credential store |
|------|------------------|
| Studio chat | Merged (personal → team) |
| **Personal scheduled job** | Merged (personal → team), same as chat |
| **Team scheduled job** | Team only |
| Fleet / channel enrichment | Team only |

Do **not** publish a personal OAuth/API credential to the team just to schedule automation — create a personal-scope job instead. Publish to team only for shared service credentials used by team jobs, fleet, or other members. See `docs/architecture/daemon-scheduler.md` ("Why Dual-Scope Jobs").

## Key Files

| File | Purpose |
|---|---|
| `pkg/credentials/store.go` | Encrypted credential store: Open, Get, Set, Resolve, token cache |
| `pkg/credentials/substitute.go` | Placeholder substitution: SubstituteAndRestore, FormatPlaceholder |
| `pkg/credentials/redact.go` | Multi-encoding redaction: AddSecret, Redact, RedactMap |
| `pkg/credentials/pending_secrets.go` | PendingVault: Extract, Resolve, SubstituteAndRestore for <<<SECRET_N>>> |
| `pkg/store/merged_credential_store.go` | Personal-first / team-fallback CredentialStore |
| `pkg/tools/credential_tool.go` | resolve_credential tool (returns placeholders), save_credential tool |
| `pkg/agent/chat_agent_run.go` | BeforeToolCallback/AfterToolCallback wiring for credential flow |
| `pkg/session/file_store.go` | RedactSession() for retroactive transcript redaction |

## Interactions

- **Agent Engine**: BeforeToolCallback/AfterToolCallback in ChatAgent and AstonishAgent handle placeholder substitution and restoration.
- **Sessions**: Session persistence redacts credential values. RedactSession() handles retroactive cleanup.
- **Tools**: `resolve_credential` returns placeholders. `http_request` accepts credential names and resolves internally. `save_credential` triggers retroactive redaction.
- **API/SSE**: The SSE streaming handler applies Redactor to all text events before sending to the browser.
- **Channels**: Telegram and email adapters receive already-redacted text.
- **Sandbox**: Credential resolution happens on the host side (in callbacks), before tool args reach the container. Raw secrets are never stored in container state. Fleet/drill file injection uses the same store for `raw_content` credentials and materializes the `content` field into the sandbox only when the run starts.
- **Scheduler**: Personal jobs use merged credentials; team jobs and fleet use team-only. See daemon-scheduler.md.
