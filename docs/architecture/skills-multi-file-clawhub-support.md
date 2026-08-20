# Multi-File ClawHub Skill Support

**Status:** Implemented

**Originally drafted:** 2026-05-29

**Author:** Astonish Team

## Overview

Astonish supports the standard multi-file skill layout used by ClawHub and OpenClaw:

```text
my-skill/
├── SKILL.md
├── scripts/
│   └── deploy.sh
├── references/
│   └── api.md
└── assets/
    └── template.yaml
```

`SKILL.md` is required. Auxiliary files may be nested recursively under arbitrary directories. The agent first loads the main instructions and a file manifest, then reads individual supporting files on demand.

## Goals and Constraints

The implementation provides:

1. **Multi-file compatibility** — Preserve and expose scripts, references, templates, and other auxiliary text files.
2. **Progressive disclosure** — Keep the main prompt small and load supporting files through `skill_lookup` only when needed.
3. **Mode isolation** — Code sessions use filesystem skills and built-ins only; they never query database-backed skill stores.
4. **Deterministic precedence** — Resolve same-named skills case-insensitively according to the active mode.
5. **Safe filesystem access** — Reject traversal and symlink escapes and enforce a 256 KiB per-file read limit.
6. **Safe local creation** — Let the agent scaffold a local skill without overwriting existing content.

Pre-compiled skill binaries and automatic dependency installation remain unsupported. A skill may provide a script as text, but executing it is a separate, explicit agent action subject to the normal tool and sandbox controls.

## Discovery and Precedence

### Filesystem Sources

Filesystem loading begins with the configured user skills directory and then processes configured extra skills roots in order. Each root contains immediate child directories with a `SKILL.md` file.

Names are normalized case-insensitively for both collision handling and allowlist filtering. A later root replaces an earlier same-named skill:

```text
configured user directory < first extra root < ... < last extra root
```

The optional allowlist is applied to the merged result and also matches case-insensitively. Built-in skills are the lowest static source; a same-named filesystem skill wins over a built-in.

### Code Mode

Code mode receives only:

- built-in skills; and
- the merged, allowlisted filesystem skills described above.

It does **not** read platform, organization, or team database skills. This remains true even if database stores are present in the surrounding application context.

### Platform Mode

Platform mode overlays database-backed scopes on the static filesystem/built-in base. Same-name comparisons are case-insensitive, with this precedence:

```text
filesystem and built-ins < platform < organization < team
```

Lookup checks the most specific database scope first (team, then organization, then platform) and falls back to the filesystem/built-in definition. The rendered skill index uses the equivalent low-to-high overlay order, yielding one entry per case-insensitive name.

## Tool Interface

### `skill_lookup`

Loading a skill root uses the existing signature:

```json
{ "name": "docker" }
```

For a filesystem skill, the response contains the parsed `SKILL.md` instructions and a recursive manifest of regular files:

```json
{
  "name": "docker",
  "description": "Container management with Docker",
  "content": "# Docker\n\n## Building Images\n...",
  "files": [
    "SKILL.md",
    "references/best-practices.md",
    "scripts/deploy.sh"
  ],
  "files_manifest": {
    "": ["SKILL.md"],
    "references": ["best-practices.md"],
    "scripts": ["deploy.sh"]
  }
}
```

The agent can load one auxiliary file using either form:

```json
{ "name": "docker", "file": "scripts/deploy.sh" }
```

```json
{
  "name": "docker",
  "path": "scripts",
  "filename": "deploy.sh"
}
```

Database-backed platform skills expose the same main-content, manifest, and individual-file interface. The selected platform scope supplies both the main skill and its auxiliary files.

### `create_skill`

`create_skill` scaffolds a new skill only beneath the configured local user skills directory. The requested name is trimmed and must contain only ASCII letters, digits, hyphens, and underscores.

Creation is intentionally non-destructive:

- It never targets an extra root or a platform database.
- It uses exclusive filesystem creation and never overwrites an existing directory or `SKILL.md`.

The generated scaffold can then be expanded with auxiliary directories and files through ordinary filesystem tools.

## Secure Auxiliary-File Access

Filesystem manifests walk the complete skill directory recursively, allowing nested paths such as `references/api/v2/auth.md`. The walk includes regular files only and does not follow symlinks.

Individual reads enforce all of the following:

- The requested path must be a clean relative path.
- Absolute paths, backslashes, empty paths, and `..` traversal are rejected.
- The candidate path must be contained within the skill root before resolution.
- Symlinks are resolved before reading, and the resolved target must still be within the resolved skill root.
- The target must be a regular file.
- At most **256 KiB** is read; larger files return an error and no partial content.

These checks prevent a skill from exposing arbitrary host files through crafted paths or symlinks. Database auxiliary-file reads enforce the same 256 KiB limit.

**Threat-model boundary:** The traversal and resolved-root/symlink containment checks protect a filesystem skill tree that remains stable during each lookup. The tree is expected not to be concurrently and adversarially mutated while validation and reading are in progress. Because lookup uses pathname-based filesystem APIs rather than descriptor-relative `openat`-style operations, it does not provide race hardening against an attacker who can rename or replace path components between checks and use. This concurrency assumption does not relax any of the traversal, symlink-resolution, containment, regular-file, or size checks above.

## Runtime Execution Model

### Sandbox Enabled

`skill_lookup` returns file content to the agent rather than exposing or mounting a host skill path into the sandbox. If a script is needed, the agent can explicitly materialize the returned text inside the session using `write_file`, inspect it, and then invoke `shell_command` under the sandbox's normal policy.

Benefits:

- No skill-directory bind mount is required.
- Host paths need not be usable from the container.
- The agent materializes only files needed for the current task.
- Existing sandbox command and network controls continue to apply.

### Sandbox Disabled

The lookup contract is unchanged. The agent may use returned content directly or write it to an appropriate temporary/work directory before execution.

## Terminal Experience

The terminal UI provides `/skills` when the backend supports local skill summaries. The command lists available skills and eligibility/source information directly without starting an LLM turn. It reports an unsupported message when that capability is unavailable.

## Storage and UI Notes

Filesystem multi-file skills remain directories on disk. Platform, organization, and team skills may store main and auxiliary content in their scoped stores. Code mode does not consult those stores.

Studio can continue to present `SKILL.md` as the primary editor while progressively adding file-tree editing for auxiliary files. Regardless of UI capabilities, the runtime `skill_lookup` interface already supports manifests and individual auxiliary-file reads.

## Implementation References

- Filesystem loading and precedence: `pkg/skills/loader.go`
- Built-in skills: `pkg/skills/bundled.go`
- Main and auxiliary-file lookup: `pkg/tools/skill_lookup.go`
- Safe local scaffolding: `pkg/tools/create_skill.go`
- Platform/org/team overlay: `pkg/channels/manager.go`
- Terminal `/skills`: `pkg/tui/skills.go`
- General skills architecture: [skills.md](./skills.md)
