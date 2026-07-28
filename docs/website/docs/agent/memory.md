# Memory

Astonish provides persistent memory that survives across sessions. The agent stores, searches, and retrieves knowledge automatically, building a growing context base over time.

## How Memory Works

Memory is curated knowledge the agent accumulates — distinct from session history (which is a conversation log). The agent can:

- **Save** facts, patterns, and solutions during conversations
- **Search** across all accessible memory tiers before responding
- **Retrieve** specific entries for detailed context

Before responding, the agent automatically retrieves relevant memories based on the current conversation. This happens transparently via the knowledge retrieval system.

## Memory Tools

The agent interacts with memory through three built-in tools:

| Tool | Description |
|------|-------------|
| `memory_save` | Store a new memory with content and category |
| `memory_search` | Semantic search across stored memories |
| `memory_get` | Retrieve full context around a specific memory entry |

### Saving Memory

The agent saves memories when it learns something worth retaining:

```
User: "Our API uses camelCase for JSON fields and snake_case for database columns"
Agent: [memory_save category="conventions" content="API uses camelCase for JSON, snake_case for DB columns"]
```

### Searching Memory

```
Agent: [memory_search query="API naming conventions"]
→ Returns: "API uses camelCase for JSON, snake_case for DB columns" (score: 0.92)
```

## Hybrid Search

Memory search combines two methods for best results:

- **Vector similarity** — Semantic search via embeddings (finds conceptually related content)
- **Full-text search** — Keyword matching (finds exact terms and phrases)

Results from both methods are merged using Reciprocal Rank Fusion (RRF). In platform mode, scenario-card results are also filtered against the actual query so a card that only shares generic terms such as “API” is not returned for an unrelated operational scenario. The direct `memory_search` tool, automatic knowledge retrieval, and Studio memory search use the same filtering.

### SQLite Backend

- Embeddings stored as BLOBs with cosine similarity computed in Go
- FTS5 virtual tables for BM25-ranked keyword search
- Zero configuration required — works out of the box

### PostgreSQL Backend

- pgvector for vector similarity search with IVFFlat indexes
- tsvector for full-text search
- Three-tier search (personal + team + org) with weighted RRF fusion

See [Three-Tier Memory](../platform/three-tier-memory.md) for details on how memory spans the org hierarchy.

## Scenario Cards

For repeatable operational tasks, Astonish can consolidate memory into **scenario cards**. A scenario card is still stored as normal embedded memory and still participates in semantic search, but it has a structured shape:

- the shortest recommended successful path
- the conditions where that path applies
- verification notes
- conditional cautions for temporary failures or outages
- lineage back to the source memory IDs and sessions that produced the card

This helps the agent reuse the efficient path it learned instead of replaying trial-and-error steps. Temporary failures should be treated as cautions to re-check, not permanent “never use this” rules. Raw memory rows are temporary staging inputs: once they are incorporated into a card, or if they cannot form a useful card, they are deleted or discarded instead of staying as long-term scattered memory.

Scenario cards are matched by scenario identity, not only by title or canonical key. Astonish extracts stable anchors such as system, service family, resource type, operation, environment, credential name, endpoint host family, API family, HTTP method, and URL path. This lets related labels such as “LBaaS” and “Octavia” merge into one OpenStack load-balancer card when the anchors agree, while still keeping different resources or environments separate.

## Managing Memory in Studio

Studio provides a visual interface for memory management. The main organization surface is **Memory Health**: when opened, Astonish checks whether the visible memories need consolidation, deduplication, or review. The check is lazy and on demand — there is no scheduled background job. If a recent evaluation is still fresh, Studio reuses it; after five days, the next visit runs a new evaluation.

- Browse all memory entries with search and filtering
- View memory content, tags, and metadata
- Use **Memory Health** to review suggested organization improvements
- Reanalyze memory on demand; otherwise fresh evaluations are reused for five days
- Draft and save scenario cards from actionable recommendations
- Merge duplicate scenario cards when Memory Health identifies two cards as the same scenario; Studio shows the resolver signals and deletes only the explicit duplicate card rows after the merged card is saved
- Open the advanced **Memory Map** only when you need low-level diagnostics for transitional raw memories
- Publish personal memories to your team by merging them into scenario cards when possible
- Promote team memories to org level (admin) by merging them into org scenario cards when possible
- Delete or edit memory entries

## Memory Configuration

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

## Best Practices

- Let the agent save memories organically during conversations
- Use categories for organization (the agent does this automatically)
- In team deployments, publish useful memories to your team via Studio so colleagues benefit
- The agent automatically searches memory before responding — no manual retrieval needed
- In platform mode, durable operational knowledge is organized into scenario cards. Successful trace-backed recipes can be marked verified; unsupported notes remain draft until a later successful run confirms them.

See [Sessions](./sessions.md) for how session history differs from memory, and [Three-Tier Memory](../platform/three-tier-memory.md) for the full multi-tier system.
