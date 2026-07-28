# Three-Tier Memory

Astonish memory operates across three tiers — personal, team, and org — searched in parallel and merged with weighted Reciprocal Rank Fusion (RRF). This gives agents access to the full depth of organizational knowledge while prioritizing context that is most relevant to the user.

## Tiers and Weights

| Tier | Schema | Weight | Contains |
|------|--------|--------|----------|
| **Personal** | `personal_<user_id>` | 1.2× | Your sessions, notes, corrections, personal patterns |
| **Team** | `team_<slug>` | 1.0× | Published team knowledge, shared sessions, team patterns |
| **Org** | `public` | 0.8× | Promoted org-wide knowledge, standards, institutional memory |

The weight multiplier is applied during RRF score calculation. Personal memories rank slightly higher because they represent your specific context and preferences. Org memories rank slightly lower because they are more general.

## How Search Works

When an agent searches memory, the platform executes queries against all three tiers simultaneously:

```
User query: "How do we handle database migrations?"

┌──────────────┐     ┌──────────────┐     ┌──────────────┐
│   Personal   │     │     Team     │     │     Org      │
│  (1.2× wt)  │     │  (1.0× wt)  │     │  (0.8× wt)  │
└──────┬───────┘     └──────┬───────┘     └──────┬───────┘
       │                    │                    │
       ▼                    ▼                    ▼
  Vector search        Vector search        Vector search
  + Full-text          + Full-text          + Full-text
       │                    │                    │
       └────────────────────┼────────────────────┘
                            ▼
                   Reciprocal Rank Fusion
                   (weighted by tier)
                            │
                            ▼
                    Merged result set
```

### Hybrid Search

Each tier uses hybrid search combining two capabilities:

- **Vector similarity** — semantic search via embedding cosine distance (pgvector for PostgreSQL, built-in for SQLite)
- **Full-text search** — keyword matching via PostgreSQL tsvector or SQLite FTS5

Results from both methods are fused per-tier, then cross-tier RRF produces the final ranked list.

## Knowledge Promotion Chain

Knowledge flows upward through the tiers via explicit actions in **Studio**:

```
Personal  ──publish──▶  Team  ──promote──▶  Org
```

1. **Personal → Team**: any team member can publish a memory entry to their team via Studio. This makes it searchable by all team members.
2. **Team → Org**: a team admin or org admin promotes team knowledge to org level via Studio, making it available to every team in the organization.

The agent can also save knowledge directly during conversations using the `memory_save` tool. The tier is determined by the current context.

Promotion now performs a scenario-card upsert. When possible, the selected memory is distilled into an efficient successful-path card in the target tier and merged with an existing card for the same scenario. Scenario matching uses deterministic identity anchors such as system, service family, resource type, operation, environment, credential, endpoint host family, API family, HTTP method, and URL path; the canonical key is treated as an alias, not the only identity. After the card is saved, the original raw source memory is deleted; if it cannot form a useful card, it is discarded rather than kept as durable memory.

## The Learning Loop

Here is how knowledge compounds in practice:

1. **Alice** debugs a tricky Kubernetes networking issue. The agent saves the resolution to her personal memory via `memory_save`.
2. Alice publishes the resolution to the **Backend team** via Studio. Now when any backend engineer hits a similar issue, the agent surfaces Alice's solution.
3. The team admin notices this resolution is relevant org-wide and **promotes it to org level** via Studio. If an org scenario card already exists for the same scenario identity, the new evidence is merged into that card instead of creating another duplicate memory.
4. If two cards were already created with different labels for the same scenario, **Memory Health** can recommend a duplicate-card merge, show the resolver signals, save the merged card, and delete only the explicit duplicate card rows.
5. **Dave** on the Frontend team later encounters the same networking issue. The agent finds the org-level scenario card and guides him through the efficient path — even though Dave never interacted with Alice.

Each step is explicit. Knowledge does not leak upward automatically.

## Memory During Chat

The agent interacts with memory through built-in tools:

- **`memory_save`** — saves facts to the user's personal memory tier
- **`memory_search`** — searches across all three tiers with RRF fusion
- **`memory_get`** — retrieves full context around a specific memory entry

These tools are always available during chat sessions and sub-agent delegation.

## Configuring Memory

Memory behavior can be tuned via the platform configuration:

```yaml
memory:
  embedding_model: text-embedding-3-small
  search_limit: 20              # results per tier before fusion
  weights:
    personal: 1.2
    team: 1.0
    org: 0.8
  auto_memorize: true           # extract key facts from sessions
```

## Privacy Guarantees

- Personal memory is never searched by other users, even org admins.
- Publishing is always an explicit user action — never automatic.
- Promotion requires admin privileges.
- Deleted memories are hard-deleted (not soft-deleted) from all tiers.

## Next Steps

- [Publish & Fork](./publish-and-fork) — the full resource sharing model
- [Organizations & Teams](./organizations-and-teams) — how tiers map to the org hierarchy
