# Publish & Fork

Resources in Astonish are **private by default**. You own your sessions, flows, apps, slide decks, and memory entries. Sharing is always an explicit action — publish to your team, fork from your team.

## The Model

```
┌──────────┐   publish   ┌──────────┐   promote   ┌──────────┐
│ Personal │ ──────────▶ │   Team   │ ──────────▶ │   Org    │
│  (yours) │             │ (shared) │             │  (all)   │
└──────────┘             └──────────┘             └──────────┘
                              │
                         fork │
                              ▼
                        ┌──────────┐
                        │ Personal │
                        │  (copy)  │
                        └──────────┘
```

- **Publish** — share your resource with your team (read access for team members)
- **Fork** — copy a team resource into your personal workspace for modification
- **Promote** — elevate team knowledge to org level (admin action)

## How to Publish, Fork, and Promote

All publish, fork, and promote operations are performed through **Studio**:

- **Publish**: In Studio, open a personal resource (session, flow, app, slide deck, or memory entry) and use the "Publish to Team" action in the resource menu.
- **Fork**: Browse team resources in Studio and use the "Fork to Personal" action to create your own copy.
- **Promote**: Team admins and org admins can promote team resources to org level via the "Promote to Org" action.

### What Gets Published

| Resource | What's Shared | What Stays Private |
|----------|--------------|-------------------|
| Session | Messages, artifacts, memory extracts | Draft messages, personal notes |
| Flow | Flow definition, steps, tool config | Execution history |
| App | Source code, manifest | Local environment variables |
| Slide deck | Slides, theme, template data, and assets | Source session draft |
| Memory | Content, embeddings, tags | Source session reference |

## Publishing Details

Published resources appear in the team's shared space. The original remains in your personal schema — publishing creates a linked copy in the team schema.

Any team member can publish their own resources to the team. You cannot publish another user's resources.

## Forking Details

Forking creates an independent copy in your personal workspace. Changes to your fork do not affect the team resource, and vice versa. Provenance metadata tracks the fork origin.

## Promotion Details

Org admins and team admins can promote resources from team level to org level, making them available to every team. Promotion is useful for:

- Standards and best practices that apply org-wide
- Flows that every team should have access to
- Memory entries containing institutional knowledge

## Unpublishing

Authors and team admins can unpublish resources via Studio. Unpublishing removes the team copy but does not affect forks that other users have already created.

## Permissions Summary

| Action | Who Can Do It |
|--------|--------------|
| Publish to team | Any team member (own resources only) |
| Fork from team | Any team member |
| Unpublish from team | Author or team admin |
| Promote to org | Team admin or org admin |
| Demote from org | Org admin only |

Slide deck publishing and forking create independent personal and team copies; they do not synchronize later edits. See [Sharing Slides](../slides/sharing.md) for the Slides workflow.

## Next Steps

- [Sharing Slides](../slides/sharing.md) — publish personal decks and fork team decks
- [Three-Tier Memory](./three-tier-memory) — how published memory is searched
- [Organizations & Teams](./organizations-and-teams) — roles that govern publish/promote
