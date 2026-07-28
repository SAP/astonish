# Memory & Knowledge

## Overview

Astonish provides persistent, searchable memory for the AI agent. The memory system stores knowledge as markdown files, indexes them with a hybrid vector + keyword search engine, and automatically retrieves relevant knowledge before each LLM turn. This gives the agent the ability to learn from past interactions, remember project-specific details, and improve over time.

The system runs entirely locally by default -- embeddings are computed in-process using a pure Go implementation, with no external API calls required.

## Key Design Decisions

### Why Hybrid Search (Vector + BM25)

Pure vector search excels at semantic similarity ("find docs about authentication") but misses exact keyword matches ("find docs mentioning `BIFROST_API_KEY`"). Pure keyword search handles exact terms but misses semantic equivalence. The hybrid approach combines both:

- **Vector search**: Finds semantically similar documents using cosine similarity on embedding vectors.
- **BM25 keyword search**: Finds documents containing specific terms using TF-IDF scoring.
- **Reciprocal Rank Fusion (RRF)**: Combines results from both methods using the formula `1 / (k + rank)` where `k=60`. This gives high-ranked results from either method a boost while avoiding domination by either approach.

Additionally, a **topic relevance penalty** (0.5x multiplier) is applied to results with zero keyword overlap with the query, preventing the retrieval of semantically-similar-but-topically-irrelevant documents.

### Why Local Embeddings

The default embedding model is **all-MiniLM-L6-v2** (384 dimensions) running in-process via Hugot + GoMLX. This was chosen because:

- **No API dependency**: Works offline, no API key needed, no rate limits, no cost.
- **Privacy**: Document content never leaves the machine.
- **Speed**: In-process computation avoids network round-trips. Good enough for the index sizes Astonish handles (hundreds of documents, not millions).
- **Pure Go**: GoMLX provides a Go-native ML runtime, avoiding CGo and Python dependencies.

A patch (`patchGoMLXBackendForLowCPU`) fixes a deadlock in GoMLX on systems with 1-2 CPUs by reducing the internal worker pool size.

Cloud fallback options (OpenAI, Ollama, OpenAI-compatible) are available for users who prefer them.

### Why Heading-Aware Chunking

Markdown documents are chunked at `##` heading boundaries rather than fixed token counts. This preserves semantic coherence -- a section about "OAuth Setup" stays together instead of being split arbitrarily. Small sections (under 100 characters) are merged with the next section to avoid fragments. A size-based fallback with overlap handles very large sections.

### Why File-Based Knowledge Storage

Knowledge is stored as markdown files in a watched directory rather than a database:

- **Human-readable**: Users can read, edit, and version-control their knowledge base with standard tools.
- **Composable**: Different knowledge categories live in different directories (guidance, skills, flows, general knowledge).
- **Incremental indexing**: SHA-256 hashing detects changed files, re-indexing only what's modified. File watchers (fsnotify) trigger re-indexing on changes.
- **Schema versioning**: A schema version (currently v6) forces full re-indexing when the chunking or indexing strategy changes.

### Why Three Special Documents

Three auto-managed markdown files serve specific purposes:

- **MEMORY.md**: Section-based knowledge store where the agent saves durable knowledge (workarounds, patterns, API quirks). Sections are indexed by `##` heading. Supports deduplication and overwrite modes.
- **INSTRUCTIONS.md**: User-editable behavior directives. Loaded into the system prompt's Tier 1 (always visible). Default content covers communication style, permissions, and task approach.
- **SELF.md**: Auto-generated system awareness document containing the current configuration -- providers, MCP servers, tools, memory settings, channels, agent identity. Regenerated when config changes. Indexed for retrieval when the agent needs to know its own capabilities.

## Architecture

Astonish currently has two persistence shapes:

- **Personal / local mode** uses the file-backed markdown knowledge base described below.
- **Platform mode** stores personal, team, and org memories in tenant-scoped database stores behind `entstore`, while still using the same agent-facing `MemoryStore` and `ThreeTierSearcher` interfaces. Platform code must resolve stores through request context and the tenant router; it must not open raw tenant connections.

### Knowledge Retrieval Per Turn

```
User sends message
    |
    v
ChatAgent.Run():
  1. Build search query from user message
  2. For short messages: augment with last LLM response context
    |
    v
  3. Partitioned search:
     a. Guidance docs (KnowledgeSearchByCategory, max 3, min score 0.3)
        - How-to instructions for capabilities
     b. General knowledge (KnowledgeSearch, max 5, min score 0.3)
        - Memory, skills, flows, user knowledge
    |
    v
  4. Deduplicate results
  5. Format as "Relevant Knowledge" section
  6. Set SystemPromptBuilder.RelevantKnowledge
    |
    v
  7. Emit a content-less `_knowledge_injection` session diagnostic event
     containing query metadata plus result id/scope/session provenance
    |
    v
  8. System prompt appends knowledge at the end (Tier 3)
     (static prefix remains cacheable for KV-cache)
```

The `_knowledge_injection` event is persisted but not sent to the LLM or rendered as a chat message. It records the cleaned semantic query, BM25 context length, injection type, result count, estimated injected tokens, and each result's path, score, category, scope, id, creator, creation time, and source session when available. This is the first diagnostic surface to inspect when memory appears unstable: it shows whether retrieval was disabled, which tier answered, and which exact memory chunk was injected.

### Indexing Pipeline

```
Memory directory (e.g., ~/.config/astonish/memory/)
  ├── guidance/          # Capability how-to docs (Tier 2)
  ├── skills/            # Skill documentation
  ├── flows/             # Flow knowledge docs
  ├── MEMORY.md          # Agent's saved knowledge
  ├── INSTRUCTIONS.md    # User behavior directives
  └── SELF.md            # Auto-generated system awareness
    |
    v
Indexer.FullSync():
  1. Walk directory tree, find all .md files
  2. Compute SHA-256 hash for each file
  3. Compare with stored hashes -- skip unchanged files
  4. For changed files:
     a. Read content
     b. Chunk at ## headings (merge small sections, split large ones)
     c. Assign category from path (guidance, skill, flow, knowledge, etc.)
     d. Compute embeddings (local or cloud)
     e. Store in chromem-go vector DB
     f. Update BM25 inverted index
  5. Remove entries for deleted files
```

### Search Pipeline

```
SearchHybrid(query, maxResults, minScore):
  |
  v
1. Vector search:
   - Embed query using same embedding function
   - Cosine similarity against all indexed documents
   - Top maxResults*2 results with category filter
  |
  v
2. BM25 search:
   - Tokenize query
   - Score documents using TF-IDF (sublinear TF: 1+log(tf))
   - Cosine similarity on TF-IDF vectors
   - Top maxResults*2 results with category filter
  |
  v
3. Reciprocal Rank Fusion:
   - Score = sum(1 / (k + rank)) for each method where result appears
   - k = 60 (standard RRF constant)
   - Sort by combined score
  |
  v
4. Topic relevance penalty:
   - For each result: check keyword overlap with query
   - Zero overlap → multiply score by 0.5
  |
  v
5. Apply minScore threshold, return top maxResults
```

### Memory Save Flow

```
Agent decides to save knowledge (prompted by system instructions or memory reflector):
  |
  v
memory_save tool:
  - category: "OAuth/Google Calendar"
  - content: "- Must use offline access_type for refresh tokens\n- ..."
  - file: "integrations/google-calendar.md" (optional)
  |
  v
1. If file specified: append/write to that path under memory directory
2. Otherwise: append section to MEMORY.md under "## category" heading
3. Deduplication: compare new content against existing section
4. Trigger re-indexing of the modified file
```

In platform mode, there is also a post-turn `PlatformReflector`. It runs asynchronously after the chat response has completed, examines the conversation and tool execution trace, and saves durable knowledge that the main model did not save in-band. The reflector should save reusable access recipes and workarounds, such as the credential name, API base URL, catalog service type, and HTTP method/path that worked for a system integration. It must not save live resource inventories, current statuses, node counts, or command output snapshots.

Example: after discovering how to list Kubernetes clusters in SAP Converged Cloud QA-DE-1, memory should capture that the reusable route is Kubernikus via the `openstack-keystone` Keystone token credential and `GET $KUBERNIKUS_URL/api/v1/clusters` with `X-Auth-Token`. It should not capture that a particular cluster currently has 13 healthy nodes, because that is live inventory that must be fetched again.

The Studio memory icon appears immediately for in-band `memory_save` tool calls. For post-turn reflector saves, the SPA polls the session-memory endpoint for a bounded period after `done`, because the reflector may finish after the SSE response text has completed. For newly-created chats, this polling uses the session ID emitted by the chat stream's `session` event rather than relying only on React's active-session state, which may still be stale when `done` arrives.

### Scenario Cards: Efficient Successful Paths

Platform mode now treats durable operational memory as a **scenario card** when possible. A scenario card is still a normal memory row: it is stored in the existing personal/team/org memory store, embedded, and returned by the same semantic + BM25 search pipeline. The difference is the content contract. The row category is `scenario_card/efficient_successful_path`, and the markdown body contains frontmatter plus sections for:

- `canonical_key`: a human-readable alias/label for the scenario, not the durable identity boundary
- `scenario_id`, `aliases`, `related_scenario_ids`: optional entity metadata for card evolution
- `identity_json`: deterministic scenario anchors such as domain, system, service family, resource type, operation, environment, credentials, endpoint host family, API family, HTTP method, and URL path
- `superseded_json`: explicit temporal/supersedes notes when an old value or path has been replaced
- `Recommended path`: the shortest known successful recipe
- `Conditions`: when the recipe applies
- `Verification`: how the path was or should be checked
- `Cautions or conditional failures`: conditional notes only, not broad permanent failure rules
- `source_memory_ids` / `source_session_ids`: lineage back to the raw extraction inputs and turns that produced the card

This makes scenario cards behave like self-managed operational skills: they are generated from verified experience, searched semantically like any other memory, and improved over time by merging new evidence into the same card. They are not human-authored skills from `memory/skills`, and they do not replace explicit skills or flows. Raw memory rows are staging inputs only; after they are incorporated into a scenario card, or if they cannot form a useful card, they are deleted or discarded rather than kept as durable memory.

Scenario-card upsert uses **scenario identity resolution**, not only canonical-key equality. `pkg/memory/scenario_identity.go` extracts deterministic anchors and scores candidate pairs with rarity-aware weights. A card is auto-merged only when the score crosses `DefaultScenarioAutoMergeThreshold`, there are no negative signals, and the best candidate is not ambiguous. Scores below the auto-merge threshold are not sent to an LLM on the write path; ambiguous duplicate work belongs in Memory Health, where a user can review the proposed merge. The resolver deliberately separates alias resolution from deduplication: aliases like “LBaaS” and “Octavia” can map to the same service family, but conflicting resource types, environments, or write/read operations prevent silent merges.

Implementation details:

- `pkg/memory/scenario_card.go` owns rendering, parsing, draft generation, merge, upsert, and retrieval filtering.
- `pkg/memory/scenario_identity.go` owns deterministic extraction, corpus statistics, pair scoring, and conservative candidate choice.
- `MemoryMerger.SaveOrMerge` drafts/upserts a scenario card for platform memory saves and fails closed if the card cannot be saved. It must not fall back to raw-memory insertion because that would reintroduce scattered notes. Post-turn reflector saves created from a trace with successful non-memory/non-knowledge tool calls are marked `verified`; saves without execution evidence remain `draft` until a later successful run confirms them.
- Promotion is upsert-based. Personal → team and team → org promotion draft a scenario card in the target scope and merge it with any existing card that resolves to the same scenario; after a successful upsert, the source raw memory is deleted.
- Retrieval asks the underlying search for extra candidates, runs query-aware scenario-card filtering, prefers scenario cards, suppresses any transitional raw memories that are explicitly listed as `source_memory_ids`, de-duplicates equivalent scenario cards as a safety net, and drops scenario cards that share only generic words with the query. This same filtering is used by automatic knowledge injection, the direct `memory_search` tool, and the Studio memory search endpoint so manual and automatic searches are consistent.

The key invariant is that Astonish should save **how to do the thing efficiently**, not a transcript of exploratory dead ends. Temporary outages, timeouts, rate limits, and “X did not work” observations may appear only as conditional cautions that require re-verification before they change behavior. If a raw memory cannot produce a usable recommended path, it is not durable memory; the system can learn it again later and create a proper card. The card frontmatter `scope` must match the store tier where the row is written (`personal`, `team`, or `org`); this prevents confusing diagnostics where a personal row contains team metadata.

### Memory Health Recommendations and Advanced Map

Platform mode exposes `GET /api/memories/health` as the product-facing memory organization surface. It evaluates the current user's visible personal, team, and org memories lazily when the Knowledge Browser's **Memory Health** tab is opened. There is no background schedule: a cached evaluation is reused for five days only when the visible memory snapshot has not changed. Successful memory mutations explicitly clear the health cache, and users can also force a refresh with **Reanalyze**.

Memory Health returns reviewable, actionable recommendations, not automatic writes:

- create a scenario card from any remaining raw memory when no card exists yet
- update an existing scenario card when new raw source memories are not yet incorporated
- clean up raw source memories that are already represented by an existing scenario card
- merge duplicate scenario cards that deterministic identity resolution considers the same operational scenario

Each recommendation contains the proposed card, target scope, source memory IDs, and the diagnostic flags that explain why it was suggested. Duplicate-card recommendations also include `duplicate_card_ids`, `resolver_signals`, and a `match_score`; applying one saves the merged content in the target scope, preserves any existing non-duplicate card for the same resolved scenario, and then deletes only the explicit duplicate scenario-card rows. Applying any recommendation uses the same scenario-card upsert endpoint as manual consolidation; after the card is saved or merged, incorporated raw source memories are deleted. Cleanup recommendations re-save the existing card metadata and delete the still-visible raw source rows. If the proposed card contains only the placeholder recipe, the raw inputs are discarded and no placeholder card is saved.

`GET /api/memories/map` remains available as the advanced diagnostic report behind the Memory Health UI. It groups likely related memory chunks by a canonical topic key and flags conditions that make memory feel scattered or unsafe:

- duplicate risk: multiple memories appear to cover the same topic
- scattered topic: related memories are spread across scopes or categories
- transient failure risk: a memory uses outage/timeout/flaky language that should not become a permanent avoidance rule
- trial/error risk: a memory appears to preserve exploratory failed attempts instead of the shortest successful path
- scenario card: the group already contains a structured card; any raw rows in the group are transitional evidence that should be incorporated or discarded

The consolidation endpoints remain the write boundary:

- `POST /api/memories/consolidate/preview` drafts a scenario card from a selected group without saving.
- `POST /api/memories/consolidate/apply` saves or merges the edited card into the selected personal, team, or org scope.

Both endpoints resolve stores through the tenant router and current authenticated context. They do not connect directly to tenant databases. Memory Health recommendations are operational metadata, not agent knowledge, and are not stored as retrievable memories.

### BM25 Implementation

The BM25 index is a pure Go implementation:

- **Document preprocessing**: Lowercase, split on non-alphanumeric, filter stopwords, collect term frequencies.
- **IDF**: `log(N / df)` where N is total docs, df is document frequency for the term.
- **TF**: Sublinear `1 + log(tf)` to dampen high-frequency terms.
- **Scoring**: TF-IDF vector cosine similarity (not the classic BM25 formula with document length normalization).
- **Category filtering**: Optional filter restricts search to specific document categories.

## Key Files

| File | Purpose |
|---|---|
| `pkg/memory/store.go` | Store: chromem-go wrapper, Search, SearchHybrid, RRF fusion |
| `pkg/memory/indexer.go` | Indexer: file discovery, incremental sync, SHA-256 hashing, fsnotify watcher |
| `pkg/memory/chunker.go` | Heading-aware markdown chunking, category assignment |
| `pkg/memory/bm25.go` | Pure Go BM25 inverted index with TF-IDF cosine scoring |
| `pkg/memory/memory.go` | Manager: MEMORY.md section CRUD, deduplication |
| `pkg/memory/embedder.go` | Embedding function resolver (local vs cloud) |
| `pkg/memory/hugot_embedder.go` | Local in-process embeddings via Hugot + GoMLX |
| `pkg/memory/instructions.go` | INSTRUCTIONS.md management with defaults |
| `pkg/memory/self_awareness.go` | SELF.md auto-generation from system configuration |

## Interactions

- **Agent Engine**: Auto-knowledge retrieval queries the store before each LLM turn. Memory reflector saves knowledge after turns. ToolIndex shares the same chromem-go DB for tool discovery.
- **Skills**: Skill documents are indexed alongside memory documents for retrieval.
- **Flows**: Flow knowledge documents (generated during distillation) are indexed for discovery.
- **Tools**: `memory_save`, `memory_get`, `memory_search` tools provide direct agent access to the memory system.
- **Configuration**: Embedding provider and memory directory are configured in app config.
- **Daemon**: Memory indexer is initialized during daemon startup with fsnotify watcher for live updates.
