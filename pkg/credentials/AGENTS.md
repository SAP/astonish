# pkg/credentials — AGENTS.md

Encrypted credential store, secret scanner, pending vault, OAuth token cache. Any secret material — API keys, OAuth tokens, passwords — must flow through here.

## Scope
- `store.go` — `Store`, `Credential`, `CredentialType`, `storeData`, in-memory token cache.
- `secret_scanner.go` — `SecretScanner`, `Detection`: scans tool arguments and outputs for leaked secrets.
- `pending_secrets.go` — `PendingVault`: holds user-entered secrets awaiting confirmation.
- `oauth.go` — `cachedToken`, `tokenCache`, OAuth response structs, refresh logic.

## Non-negotiable rules
1. **Envelope encryption**: credential ciphertext is encrypted with the org's DEK (AES-256-GCM). The DEK itself is wrapped with the master KEK. This layering must be preserved — never store plaintext, never re-use another org's DEK.
2. **Personal-first resolution with team fallback**: reads look up personal creds first, then team. This is the invariant that lets an individual override team defaults without exposing personal secrets to the team.
3. **Secret scanning is opt-in** (disabled by default). Enable it via `security.secret_scanner.enabled: true` in config.yaml. When enabled, any code path that composes a tool argument or shell command should let `SecretScanner` see it.
4. **OAuth refresh** must happen in a single goroutine per token (guarded by `tokenCache`). Never call refresh from a hot path without going through the cache.

## When editing
1. Adding a new credential type? Extend `CredentialType`, ent schema (org or personal scope), and the scanner's redaction rules together.
2. Changing encryption? Update key rotation policy in `docs/architecture/multi-tenant-platform.md` at the same time.

## Verification

Run `go test ./pkg/credentials/...`; include the affected store/MCP/tool package when changing resolution or redaction.

## References
- [`pkg/store/AGENTS.md`](../store/AGENTS.md) — personal/team store composition and tenant scope.
- [`pkg/mcp/AGENTS.md`](../mcp/AGENTS.md) — runtime placeholder resolution and secret-safe diagnostics.
- [`docs/architecture/multi-tenant-platform.md`](../../docs/architecture/multi-tenant-platform.md) — platform encryption and tenant enforcement.
