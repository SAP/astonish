# Future CLI Commands (Internal Reference)

::: warning Internal Document
This page is an internal reference for planned CLI commands. These commands do not exist yet and should not be documented as available features until implemented.
:::

This document tracks CLI commands that are planned for future implementation. Currently, these operations are available only through Studio (web UI) or the agent's built-in tools during chat.

## Team Management

| Planned Command | Purpose | Current Alternative |
|----------------|---------|--------------------|
| `astonish team create --name <name> --slug <slug>` | Create a new team | Studio → Settings → Teams |
| `astonish team show <slug>` | Show team details | Studio → Settings → Teams |
| `astonish team add-member <team> --user <email> --role <role>` | Add user to team | Studio → Team Settings |
| `astonish team remove-member <team> --user <email>` | Remove user from team | Studio → Team Settings |
| `astonish team delete <slug>` | Delete a team | Studio → Settings → Teams |

## Context Switching

| Planned Command | Purpose | Current Alternative |
|----------------|---------|--------------------|
| `astonish use team <slug>` | Switch active team without re-login | `astonish logout` + `astonish login --team <slug>` |
| `astonish use org <slug>` | Switch active org without re-login | `astonish logout` + `astonish login --org <slug>` |

## Configuration Management

| Planned Command | Purpose | Current Alternative |
|----------------|---------|--------------------|
| `astonish config set <key> <value>` | Set a config value | `astonish config edit` (opens editor) |
| `astonish config show --resolved` | Show merged config from all tiers | Studio → Settings |
| `astonish config show --resolved --show-origin` | Show merged config with source tier | Studio → Settings |
| `astonish platform config set <key> <value>` | Set platform-level config | Edit config.yaml directly |
| `astonish org config set <key> <value>` | Set org-level config | Studio → Org Settings |
| `astonish team config set <key> <value>` | Set team-level config | Studio → Team Settings |

## Memory Management

| Planned Command | Purpose | Current Alternative |
|----------------|---------|--------------------|
| `astonish memory search <query>` | Search across all memory tiers | Agent's `memory_search` tool during chat |
| `astonish memory publish <id> --to team` | Publish personal memory to team | Studio → Memory → Publish |
| `astonish memory promote <id> --from team <slug> --to org` | Promote team memory to org | Studio → Memory → Promote |

## Resource Publishing & Forking

| Planned Command | Purpose | Current Alternative |
|----------------|---------|--------------------|
| `astonish session publish <id> --to <team>` | Publish session to team | Studio → Session → Publish |
| `astonish session unpublish <id> --from <team>` | Remove from team | Studio → Session → Unpublish |
| `astonish flow publish <name> --to <team>` | Publish flow to team | Studio → Flow → Publish |
| `astonish flow fork <name>` | Fork team flow to personal | Studio → Flow → Fork |
| `astonish flow promote <name> --from team <slug> --to org` | Promote flow to org | Studio → Flow → Promote |
| `astonish app publish <name> --to <team>` | Publish app to team | Studio → App → Publish |

## Organization Management

| Planned Command | Purpose | Current Alternative |
|----------------|---------|--------------------|
| `astonish platform org show <slug>` | Show org details | Studio → Platform Admin |
| `astonish platform org delete <slug>` | Delete organization | Direct database operation |
| `astonish org invite <email>` | Invite user (bare command) | `astonish platform org invite --org <slug> --email <email>` |

## Authentication

| Planned Command | Purpose | Current Alternative |
|----------------|---------|--------------------|
| `astonish login --token <token>` | Login with API token (CI/CD) | `astonish platform issue-token` + manual config |
| `astonish login --device-code` | Explicit device-code flow | `astonish login --sso` (uses OAuth Authorization Code + PKCE with browser loopback) |

## Data Management

| Planned Command | Purpose | Current Alternative |
|----------------|---------|--------------------|
| `astonish platform backup --org <slug>` | Backup org database | Manual `pg_dump` |
| `astonish platform restore --org <slug> --from <file>` | Restore from backup | Manual `pg_restore` |
| `astonish export --format archive` | Export local data | Manual file copy |
| `astonish platform migrate --from <file>` | Import local data to platform | Not available |

## Priority Notes

### High Priority (Quality of Life)
- `astonish use team/org` — context switching without re-login
- `astonish config set` — quick config changes from terminal
- `astonish memory search` — search memory without starting a chat

### Medium Priority (Team Management)
- Team CRUD commands — currently requires Studio
- Resource publish/fork — currently requires Studio
- `astonish login --token` — needed for CI/CD pipelines

### Lower Priority (Admin Operations)
- Backup/restore — can use standard PostgreSQL tools
- Data migration — edge case for new deployments
- Org delete — rare, high-risk operation
