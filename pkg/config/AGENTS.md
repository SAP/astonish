# Configuration package guide

## Scope

This package owns host/app configuration, YAML flow parsing, provider environment mapping, and MCP config discovery. Read the root [`AGENTS.md`](../../AGENTS.md) first; changes that feed provider creation or MCP startup must also follow [`../provider/AGENTS.md`](../provider/AGENTS.md) and [`../mcp/AGENTS.md`](../mcp/AGENTS.md).

## Invariants

- Preserve the established effective-config precedence. More specific runtime, personal, team, or explicit values must not be overwritten by broader org/platform defaults or host defaults. Treat “missing” differently from an explicitly configured zero/empty value where the current merge code does.
- Keep backward-compatible YAML aliases and normalization unless a migration deliberately removes them. Decode, normalize, validate, and reconcile in the same order as existing loaders.
- Configuration may contain API keys, tokens, credential placeholders, and endpoint credentials. Never log or return raw secret values. Diagnostics should name the field/provider and source, not its value.
- Keep credential placeholders unresolved in persisted config. Resolve secrets only at the runtime boundary that needs them; do not write resolved values back to YAML, JSON, logs, errors, or snapshots.
- Environment-derived provider settings remain an input layer, not a reason to mutate process environment or persisted configuration.
- When changing MCP config shape or defaults, trace through the MCP Manager lifecycle and diagnostics. When changing provider selection/defaults, trace through effective resolution and provider pool invalidation.

## Tests

Use the narrowest matching suite while iterating:

```bash
go test ./pkg/config
go test ./pkg/config -run 'Test.*(Load|Merge|MCP|Provider|YAML)'
```

For cross-package changes also run:

```bash
go test ./pkg/provider ./pkg/mcp
```
