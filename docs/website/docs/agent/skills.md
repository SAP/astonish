# Skills

Skills are markdown-based instruction packages that teach the agent specific tools, APIs, workflows, or domain knowledge. Every skill has a `SKILL.md`; it may also include scripts, references, templates, and other auxiliary text files that the agent loads on demand.

## How Skills Work

The agent sees a lightweight list of available skill names and descriptions. When a skill is relevant, it uses `skill_lookup` to load the full instructions. For a multi-file skill, that first lookup also returns a manifest so the agent can request only the supporting files it needs.

Skills differ from memory:

- **Memory** = facts the agent has learned (declarative knowledge)
- **Skills** = instructions for how to do things (procedural knowledge)

## Skill Structure

A filesystem skill is a directory with a required `SKILL.md`:

```text
my-skill/
├── SKILL.md
├── scripts/
│   └── run.sh
├── references/
│   └── api.md
└── templates/
    └── config.yaml
```

`SKILL.md` uses YAML frontmatter for its name, description, and optional requirements:

```markdown
---
name: my-skill
description: When and why the agent should use this skill
requires:
  binaries: [example-cli]
---

# My Skill

Detailed steps, commands, examples, and best practices.
```

The description is critical because it tells the agent when the skill applies. Requirements can describe supported operating systems, required binaries, and required environment variables.

## Filesystem Discovery

Astonish loads filesystem skills from the configured user skills directory first and then from each configured extra skills root in order.

If two skills have the same name ignoring case, the later root wins:

```text
configured user directory < first extra root < ... < last extra root
```

An optional skill allowlist is applied case-insensitively after the roots are merged. Built-in skills remain available unless a same-named filesystem skill overrides them.

## Availability by Mode

### Code

Code sessions use only built-in and filesystem skills. They do not load platform, organization, or team skills from the database.

### Platform

Platform-connected sessions combine filesystem/built-in skills with managed scopes. Same-named skills are resolved case-insensitively with this precedence:

```text
filesystem and built-ins < platform < organization < team
```

A team skill therefore overrides an organization, platform, or filesystem skill with the same name. Skills with different names are combined and remain available.

## Managing Skills

Managed skills are available at three platform scopes:

| Scope | Managed By | Available To |
|-------|------------|-------------|
| Platform | Platform admin | All users |
| Organization | Organization admin | All organization members |
| Team | Team admin | Team members |

### In Studio

Depending on your permissions and deployment, Studio lets you:

- Browse available skills
- Create and edit managed skills
- Install skills from ClawHub
- Configure platform, organization, or team scope

### CLI

The CLI provides skill management commands:

```bash
astonish skills list              # List available skills
astonish skills show <name>       # Show skill content
astonish skills install <source>  # Install from ClawHub
astonish skills create <name>     # Create a new skill template
```

::: tip Plural Command
The CLI command is `astonish skills` (plural), not `astonish skill`.
:::

### Agent-Created Local Skills

The agent's `create_skill` tool creates a scaffold only in the configured local user skills directory. It never creates a platform, organization, or team database skill and never writes to an extra skills root.

Creation does not overwrite anything. The tool rejects an existing skill directory and creates `SKILL.md` exclusively.

### Terminal `/skills`

In a terminal session with local skill-listing support, enter:

```text
/skills
```

The command displays available skill summaries directly; it does not send a request to the model. If the active backend does not expose local skill summaries, the terminal reports that `/skills` is unsupported.

## ClawHub Community Skills

The [ClawHub](https://github.com/astonish-clawhub) organization hosts community-contributed skills covering common tools and workflows. Multi-file packages retain their auxiliary files so the agent can discover and load scripts, references, and templates as needed.

## Agent Tool: `skill_lookup`

Load a skill's main instructions by name:

```yaml
skill_lookup:
  name: docker
```

The response includes `SKILL.md` content and, for multi-file skills, a recursive manifest. Load a specific supporting file with a relative path:

```yaml
skill_lookup:
  name: docker
  file: references/best-practices.md
```

You can equivalently supply `path` and `filename` separately.

### Auxiliary-File Safety

Filesystem auxiliary-file access is constrained for safety:

- Manifests recurse through nested directories but include regular files only and do not follow symlinks.
- File paths must be clean and relative; absolute paths and `..` traversal are rejected.
- Symlinks are resolved before a file is read, and the target must remain inside the skill directory.
- Each auxiliary-file read is limited to **256 KiB**; larger files are rejected.

These protections prevent a skill from using a path or symlink to read unrelated host files.

## Cascading Access

Skills cascade through the platform hierarchy (see [Cascading Defaults](../platform/cascading-defaults.md)):

- Platform skills are available to everyone.
- Organization skills are available to all organization members.
- Team skills are available to team members.
- Lower, more specific scopes override same-named higher-scope skills; they supplement skills with different names.

See [Sub-agents](./sub-agents.md) for how delegated tasks inherit skill access, and [Configuration](../configuration/mcp-servers.md) for MCP servers (another way to extend agent capabilities).
