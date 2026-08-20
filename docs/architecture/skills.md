# Skills System

## Overview

Skills are markdown-based guides that teach the AI agent how to use specific CLI tools, APIs, platforms, and workflows. Each skill has a required `SKILL.md` and may include auxiliary files such as scripts, references, and templates. The system exposes a lightweight list of available skills and lets the agent load full instructions and supporting files on demand with `skill_lookup`.

## Key Design Decisions

### Why Markdown with YAML Frontmatter

Skills use plain markdown files with YAML frontmatter for metadata:

```markdown
---
name: docker
description: Container management with Docker
requires:
  binaries: [docker]
  os: [linux, darwin]
---

# Docker

## Building Images
...
```

This format was chosen because:

- **Human-readable and editable**: Anyone can write or modify a skill.
- **LLM-friendly**: Markdown is a natural format for model instructions.
- **Structured metadata**: YAML frontmatter provides machine-parseable eligibility criteria without polluting the content.
- **Version-controllable**: Plain text files work naturally with git.
- **Compatible with multi-file skills**: A skill directory can keep focused details in auxiliary files and reveal them only when needed.

### Why Eligibility Checking

Not all skills are relevant to every environment. Eligibility checking validates:

- **OS**: The skill applies to the current operating system.
- **Binaries**: Required CLI tools are installed (checked via `exec.LookPath`).
- **Environment variables**: Required environment variables are set.

Missing requirements are reported with the skill so the agent can avoid blindly attempting operations that cannot succeed.

### Why Filesystem Skills Plus Platform Scopes

Local and Code sessions load filesystem skills from the configured user skills directory first, followed by each configured extra skills root in order. Each root contains one immediate subdirectory per skill, with `SKILL.md` at the skill root.

An optional allowlist filters the merged result. Allowlist matching and skill-name collision matching are case-insensitive. Later filesystem roots replace earlier definitions with the same name, so the precedence is:

```text
configured user skills directory < first extra root < ... < last extra root
```

Built-in skills are also available. Filesystem skills override built-ins on a case-insensitive name collision.

Code mode deliberately uses only this filesystem-plus-built-ins view. It does not read platform, organization, or team skill databases.

Platform sessions add database-backed scopes. Their case-insensitive collision precedence is:

```text
filesystem and built-ins < platform < organization < team
```

Thus the most specific available definition wins while unrelated skills from every scope remain visible.

### Why Progressive Disclosure

The system prompt includes only skill names and one-line descriptions. Calling `skill_lookup` with a skill name returns the main instructions plus a manifest of auxiliary files. The agent can then request one file by relative path. This keeps the prompt small while making complete multi-file skills available when needed.

## Architecture

### Skill Discovery and Retrieval

```text
User message: "Deploy the app to our Kubernetes cluster"
    |
    v
Available Skills index identifies the kubernetes skill
    |
    v
Agent calls skill_lookup(name: "kubernetes")
    |
    v
SKILL.md and an auxiliary-file manifest are returned
    |
    v
Agent loads only the referenced scripts or documentation it needs
```

Filesystem discovery loads the configured user root and then configured extra roots. The merged, allowlisted result is used by Local and Code sessions. Platform sessions overlay database skills using platform, organization, and team precedence.

### Skill Index in the System Prompt

The system prompt includes a lightweight skill listing rather than every skill body:

```text
Available Skills: git (version control), docker (containers), kubernetes (orchestration), ...
```

This tells the agent what exists without consuming tokens for full content. `skill_lookup` provides explicit, on-demand access to the selected skill.

### Multi-File Lookup and Security

For a filesystem skill, `skill_lookup(name)` recursively lists regular files beneath the skill directory and returns both a sorted file list and a directory-grouped manifest. A request can use either `file`, or `path` plus `filename`, to read a specific auxiliary file.

Filesystem access is constrained as follows:

- Paths must be clean, relative paths; absolute paths, backslashes, and `..` traversal are rejected.
- Recursive manifests skip symlinks and non-regular files.
- A requested file is resolved through symlinks and must still be contained within the resolved skill root.
- Only regular files are returned.
- Reads are capped at **256 KiB per file**; larger files are rejected.

Database-backed platform auxiliary files use the same lookup interface and 256 KiB read limit.

See [Multi-File ClawHub Skill Support](./skills-multi-file-clawhub-support.md) for the detailed multi-file design.

### Built-in Skills

Astonish includes built-in skills for common tools and workflows. They form the lowest static layer: a same-named filesystem skill replaces a built-in before any platform database overlays are applied.

### Agent-Created Skills

The `create_skill` tool creates a new scaffold only in the configured local user skills directory. It validates the requested name and creates a new skill directory containing `SKILL.md`. It never writes to extra roots or platform databases, and it refuses to overwrite an existing skill directory or `SKILL.md`.

### Terminal Command

The terminal UI exposes `/skills` when the active backend supports local skill listing. It renders the available skill summaries directly and does not send an agent turn. Platform-only or otherwise unsupported backends report that the command is unavailable.

## Key Files

| File | Purpose |
|---|---|
| `pkg/skills/loader.go` | Filesystem loading, allowlist filtering, precedence, and eligibility metadata |
| `pkg/skills/bundled.go` | Embedded built-in skill files |
| `pkg/tools/skill_lookup.go` | Mode-aware main and auxiliary-file lookup |
| `pkg/tools/create_skill.go` | Safe local skill scaffold creation |
| `pkg/channels/manager.go` | Platform/org/team skill-index overlay |
| `pkg/tui/skills.go` | Terminal `/skills` rendering |

## Interactions

- **Agent Engine**: The system prompt lists available skills; the agent calls `skill_lookup` for full content.
- **Code mode**: Uses filesystem skills and built-ins only and never consults database skill stores.
- **Platform**: Adds database-backed skills with filesystem `<` platform `<` organization `<` team precedence.
- **Configuration**: Defines the user skills directory, ordered extra roots, and optional allowlist.
- **ClawHub**: Installed multi-file skills retain their auxiliary files for progressive lookup.
- **Terminal UI**: `/skills` lists available local skills without invoking the model.
