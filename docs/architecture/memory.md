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

Astonish distinguishes ordinary Chat persistence from Bot-owned content:

- **Personal / local mode** uses the file-backed markdown knowledge base described below.
- **Platform ordinary Chat** stores personal, team, and org memories in tenant-scoped database stores behind `entstore`, while still using the same agent-facing `MemoryStore` and `ThreeTierSearcher` interfaces. Platform code must resolve stores through request context and the tenant router; it must not open raw tenant connections.
- **Bots (personal and team)** use retained files and a scoped disk-memory repository, not generic platform-memory SQL content. See [File-first Bot memory](#file-first-bot-memory-separate-from-ordinary-chat). The generic retrieval, indexing and scenario-card descriptions below are not Bot bootstrap rules.

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
  5. Format as "Relevant Knowledge" context
  6. Persist the exact model-facing context as a marked user-role event
    |
    v
  7. Emit a content-less `_knowledge_injection` session diagnostic event
     containing query metadata plus result id/scope/session provenance
```

The marked per-turn context is replayed to the model byte-for-byte but filtered from Studio, terminal, channel, scheduler, memory/reflection, distillation, and compaction user-text views. This keeps retrieved knowledge out of the frozen system prompt without misrepresenting it as user-authored content.

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
- Consolidation apply resolves source/discard rows and explicit duplicate cards in their scoped stores and checks `canManageMemory` for every resolved participant before any write or deletion. The unchanged identity resolver runs against a write-recording adapter so its actual destination can also be authorized before an underlying Add/Update invokes embedding or persistence. The adapter records destination-list failures even when the preferred-card selector treats an error as no match; apply returns a bounded error immediately, without retrying into a write or deleting sources. Deletes use the authorized store/ID pairs, not a second unscoped search. This does not change memory-sharing or team visibility policy; missing historical source IDs remain tolerated. Source, duplicate, and discard cleanup stop at the first deletion failure and return a bounded HTTP 500 error, never `applied: true`; preview and write-planning storage errors also omit raw backend details. A cleanup error after upsert explicitly reports that the scenario card was saved. The saved destination is preserved by its request-resolved scope and ID, not ID alone: an authorized source or duplicate with the same ID in another scope is still cleaned up, and its deletion failure is reported. `store.MemoryStore` has no transaction/rollback contract: earlier successful deletions remain persisted, and even a failed mutation may have partially persisted. Apply invalidates Memory Health after attempted persistence/cleanup, including failures. Refresh/reanalyze the current rows before retrying; this is not atomic cross-store consolidation.
- Promotion is upsert-based. Personal → team and team → org promotion draft a scenario card in the target scope and merge it with any existing card that resolves to the same scenario; after a successful upsert, the source raw memory is deleted.
- Retrieval asks the underlying search for extra candidates, runs query-aware scenario-card filtering, prefers scenario cards, suppresses any transitional raw memories that are explicitly listed as `source_memory_ids`, de-duplicates equivalent scenario cards as a safety net, and drops scenario cards that share only generic words with the query. This same filtering is used by automatic knowledge injection, the direct `memory_search` tool, and the Studio memory search endpoint so manual and automatic searches are consistent.

The key invariant is that Astonish should save **how to do the thing efficiently**, not a transcript of exploratory dead ends. Temporary outages, timeouts, rate limits, and “X did not work” observations may appear only as conditional cautions that require re-verification before they change behavior. If a raw memory cannot produce a usable recommended path, it is not durable memory; the system can learn it again later and create a proper card. The card frontmatter `scope` must match the store tier where the row is written (`personal`, `team`, or `org`); this prevents confusing diagnostics where a personal row contains team metadata.

### File-first Bot memory (separate from ordinary Chat)

Bots do not use the generic platform-memory SQL rows described above as their content authority. Ordinary Chat/code memory, its reflector, scenario cards and three-tier search keep their existing behavior. Retained Home owns working documents; protected immutable content storage owns attributed memory history. SQL holds scoped control state and typed content references, not Bot memory bodies, embeddings, nested `source_records` or replacement snapshots. Generic SQL memory `Add`/`Update` exclude Bot-linked sessions; there is no legacy-body fallback or automatic migration.

#### Real documents and bounded bootstrap

`bot.Documents.Load` reads `SOUL.md`, `IDENTITY.md`, `USER.md`, `CHARTER.md` and actual `MEMORY.md` through `store.BotFiles`. Critical documents are bounded to 16 KiB each; `BotMemoryBootstrapBytes` is **12 KiB**, including the explicit `read_file` retrieval hint. Oversized external files remain intact: `BoundDocumentText` trims only the UTF-8 prompt excerpt. `SourceVersion` identifies the full file SHA-256, not a memory-row revision.

`LoadMapped` retains its compatibility signature but ignores the old memory-service argument. It reads optional `GOALS.md`, `people/INDEX.md`, `groups/INDEX.md`, and `goals/INDEX.md`, `skills/INDEX.md`, `tasks/INDEX.md`, `knowledge/INDEX.md` when present. It does not synthesize notes, record mappings or historical reports from SQL. Missing critical files fail; missing optional indexes are skipped. Missing file capability fails explicitly. A GET never initializes Home, starts a run or repairs files. Historical configuration versions cannot project historical working files.

The compact index is for current goals, useful knowledge, completed/unfinished work, concise outcomes and paths. Full earlier deliverables require explicit reads, not a latest-20 memory query or automatic historical-report injection. Active-resume transcript handling is a separate contract and must not be replaced by a lossy memory summary. Index/persona text and paths never grant permission or prove external effects. Ordinary `write_file`/`edit_file` are restricted to `outputs/`; they cannot rewrite instructions, policy or `MEMORY.md`.

#### Scoped disk memory and attribution

`store.NewBotMemoryService` resolves the Bot-specific disk view through the same authenticated scoped Bot repository. `botDiskMemoryStore` retains memory IDs and the agent-facing result shape, but reads an immutable disk catalogue. `bot_memory_head_v1` is a typed `BotContentRef` in control state. Content is staged before the head becomes visible; scoped revision/sequence CAS and fresh authority guard the head commit. Interrupted or rejected commits can leave unreferenced immutable objects, not partly edited current claims. This is not a filesystem/SQL distributed transaction or permission to delete orphans during reads.

Each operation authorizes the Bot and constrains candidates by linked home/run sessions before ranking or pagination. Generic owner-wide search/merger context is shadowed. Unsupported capabilities never fall back to unrestricted memory. Mutations require dedicated `BotMemoryMutationStore`, reviewed-edit or fenced runtime paths; the generic disk `MemoryStore` view is read-only. `NewBotRunMemoryService` requires a current lease and confines autonomous mutation to the producing run's session. Autonomous notes cannot silently replace owner-reviewed or attributed claims. Credential values are placeholderized before tool saves.

`MemoryAttribution` binds Bot/goal/run provenance, source, evidence, observation time, status (`inferred`/`verified`) and optional `superseded_by`. Valid owner-confirmation or observation evidence is required for verified knowledge; model text cannot upgrade an inference. Source content and provenance remain in protected disk history. `IsCurrentBotMemory` excludes retired records from ordinary Bot recall and `memory_get`; list/get and Memory Health retain history for review. Attributed search excludes superseded observations unless explicitly requested. These are Bot rules, not a generic Chat retention change.

Approved routine `memory_forget` is logical retirement with `superseded_by: routine-operation:<operation UUID>`, not physical deletion. Denial and exception expiry do not retire memory. Validate the exact target marker, absence from current recall and preservation of original content/provenance and neighboring records, including after reopen; an empty UI list is insufficient evidence.

#### Reviewed batches, corrections and health

`BotMemoryService.ApplyReviewed` uses `BotMemoryReviewStore.ApplyReviewedBotMemory`, not generic consolidation. A confirmed request binds an idempotency UUID, expected Bot revision, typed kind (`document`/`consolidate`), reason and 1–100 edits. Each edit has an authorized target session and fingerprint-bound source mappings; consolidation needs at least two sources. `MemoryContentFingerprint` includes content, category, path, session, creator/time and attribution. Stale fingerprints, foreign sources, superseded claims and changed retries fail closed.

Personal/team implementations require their exact `botDiskMemoryStore` and content backend. They freshly authorize, CAS the Bot revision, preserve original sources, insert inferred replacements and mark sources `memory-review:<UUID>`. Source snapshots and `source_records` are disk content, not SQL JSON. The durable receipt binds request and actor; exact replay returns the receipt without repeating the change. Transaction rollback preserves the old visible head even if immutable staged bytes exist. There is no claim of globally atomic membership revocation across databases.

`GET /api/studio/bots/{id}/memory-health` evaluates a bounded Bot-scoped snapshot with fingerprints, truncation information and review-only recommendations. `POST /memory-health/consolidate` requires a separate confirmed review. `/documents/{document}/ingest` remains a structured reviewed-memory operation, not arbitrary Markdown replacement and not proof that an independently read Home file was rewritten. A file-only document may have no record mappings. Persona/configuration edits remain separately authorized and must not import file prose as tool policy or resume paused work.

`Correct` records intent; bodyless `/corrections/{correctionID}/reconcile` separately applies it. Only exact `memory:<UUID>` sources are executable. The same scoped disk view, immutable session linkage, current editor, source state and Bot CAS are rechecked. Reconciliation retains the original source and inserts a deterministic replacement with reviewed correction evidence, then records a deduplicated receipt. It no longer relies on old in-place SQL replacement/vector-cache invalidation. HTTP 202 means recorded intent; only successful reconciliation with `memory_applied: true` means applied. Corrections grant no rights, reopen no goals and enable no schedules.

#### File-first validation still required

The production worker now reconciles committed report publications into `MEMORY.md` through `PublicationMemoryResolver` and `MemoryIndex`. The source is the exact Bot/run/report-sequence receipt and settled report status, never the mutable run's latest status or model-authored index JSON. Cancelled/interrupted reports create an `interrupted` unfinished-work entry even with zero artifacts; they never claim delivery. Partial-file paths and immutable artifact IDs are included only after trusted snapshot readback reaches the bounded stream's verified EOF. Absent snapshots are omitted for interruptions; invalid scope, digest, transport and cancellation errors fail closed. A `publish_file` snapshot alone is not a committed publication and cannot become a memory outcome.

Index writes preserve authored notes, deduplicate Bot/run/report-sequence receipts (including archived entries), and use MEMORY.md digest CAS under the existing post-publication commit fence. Conflicts retry only index metadata, with later authenticated reconciliation retaining the same durable receipt after failure; neither model execution nor file generation is replayed. Older index detail is linked before the 12 KiB bootstrap bound. Controlled resolver/index and worker tests cover cancellation, partial retained files, missing snapshots and idempotent retry. Source inspection and controlled tests are not physical backend/remount or full end-to-end acceptance; those evidence tiers remain separately required.

Run `TestBotFileFirstWorkerHTTP` through the production worker in personal/team scopes, then scoped `TestBot.*(Memory|Document|FileFirst|TaskWorkspace|Mandate)` regression tests. Capture the actual provider request after reopen: critical files and bounded `MEMORY.md` must appear, full old deliverables must not, and a later explicit read must retrieve earlier bytes. Include manual/cron/confirmed-title, paused, stale-lease, cancelled, foreign-scope and ordinary-Chat regression cases. Reviewed-memory tests must exercise source/evidence CAS, receipt replay, failure rollback and reopen against disk content, not old SQL-only fixtures.

Physical Docker, dedicated Kubernetes/PostgreSQL, built Studio and native terminal evidence remain separate gates. Inspect raw Bot-owned SQL payloads/indexes for sentinel bodies; exercise file/DB interruption, unsupported capability, backup/restore and cleanup. Missing infrastructure blocks that tier; skipped or controlled package tests are not physical acceptance. No live acceptance was performed for this documentation update.

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
- **Tools**: `memory_save`, `memory_get`, `memory_search`, and `memory_delete` tools provide direct agent access to the memory system. Deletes require an exact memory ID, target the result's tenant-scoped memory tier, and use the same creator/admin authorization path as Studio memory management.
- **Configuration**: Embedding provider and memory directory are configured in app config.
- **Daemon**: Memory indexer is initialized during daemon startup with fsnotify watcher for live updates.
