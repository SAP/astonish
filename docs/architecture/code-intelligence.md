# Code Intelligence (Tree-sitter)

> **Status: Implemented.**
> This document specifies the code intelligence system for Astonish
> Fleet agents. It is based on analysis of Grok (xAI), OpenClaude, and
> OpenClaw's production implementations. The primary system is
> **tree-sitter** - zero-config, low-memory, always-on
> structural code understanding (`pkg/codeintel`). LSP is documented as a
> future evaluation target, gated on observed failure patterns.

## 1. Motivation

Fleet mode agents perform full software development: implementing features,
fixing bugs, refactoring, and running tests. The current text-based tools
(`grep_search`, `find_files`, `file_tree`) are fast (~2-30ms on 500K-line
repos) and cover most navigation needs, but they have two specific problems:

### Problem 1: Speed (unnecessary round-trips)

Without semantic tools, the agent does:
1. `grep_search` for a function name -> gets 15+ results (definitions,
   references, comments, test mocks, string literals)
2. Agent reasons about which are real call sites vs. noise
3. Another `grep_search` with context to disambiguate
4. `read_file` on 2-3 locations to confirm

With tree-sitter `code_references`:
1. One call -> gets structural references (not definitions, not comments,
   not strings)
2. Done.

That's 3-4 tool calls reduced to 1. Across a Fleet session doing 50+
navigation queries, this saves significant tokens and iteration time.

### Problem 2: Coverage (silent misses)

When an agent greps for a symbol to find all callers before refactoring:
- Grep finds the string everywhere: 200+ matches
- Agent hits the `max_results` cap at 50
- Agent sees enough results to feel confident
- Misses call sites in files that didn't make the top 50
- "Fix" compiles but breaks those missed sites at runtime

Tree-sitter `code_references` improves this because:
- It returns structural references without a string-search cap
- It excludes most noise (comments, strings, type assertions)
- The result set is small enough (only real usages) that the agent processes
  all of them without hitting caps

### What tree-sitter does NOT solve

- **Go interface implementations:** Tree-sitter cannot determine "this struct
  satisfies this interface" because Go uses structural typing (requires type
  checking). This is the one case where only LSP/gopls can answer correctly.
- **Indirect references:** If code does `fn := pkg.DoThing` and later calls
  `fn()`, tree-sitter sees the assignment but cannot trace the indirect call.
- **Cross-package type resolution:** Complex re-exports or interface casts
  may be missed.

In practice, these gaps are covered by compilation: `go build` or `tsc`
catches breakage from missed interface implementations. The agent already
runs builds as part of its workflow.

## 2. Design Principles

1. **Text tools remain the primary discovery layer.** Code intelligence
   *complements* grep/find/tree; it does not replace them. An agent
   greps to find candidates, then uses code intelligence to confirm.

2. **Zero-config, zero cost.** Tree-sitter works with no external
   dependencies, no server processes, no memory overhead beyond the small
   in-process index. Adding it to sandboxes costs <5MB disk.

3. **Runs inside the sandbox.** Tree-sitter indexing runs within the
   `astonish node` process inside the sandbox, alongside the code it
   analyzes. No cross-boundary protocol proxying needed.

4. **Lazy initialization.** Indexing happens on first tool invocation,
   not at sandbox startup. Sessions that never need code intelligence
   pay zero cost.

5. **Graceful degradation.** If tree-sitter grammars aren't available for
   a file's language, the tool returns an informative error directing the
   agent to use `grep_search`. Never fails silently.

6. **Language-agnostic engine, per-language queries.** The Go
   implementation is 100% shared across languages. Adding a language means
   dropping in a grammar `.so` (~500KB) and a query `.scm` file (50-200
   lines). No code changes.

## 3. Supported Languages

Initial release targets the four languages that cover essentially all Fleet
development scenarios:

| Language | Grammar | Query Source | Covers |
|----------|---------|--------------|--------|
| **Go** | `tree-sitter-go` | Custom + Aider-derived | Backend services (primary workload) |
| **TypeScript** | `tree-sitter-typescript` | Aider-derived | React frontends, Node services |
| **JavaScript** | `tree-sitter-javascript` | Aider-derived | Node services, scripts, configs |
| **Python** | `tree-sitter-python` | Aider-derived | ML/data services, automation |

**Comparison with prior art:**
- Grok covers: Rust, TypeScript, JavaScript, Go, Python (5 languages)
- OpenClaude covers: TypeScript, TSX, JavaScript, Python (4 languages)
- OpenClaw covers: TypeScript (via LSP only)
- **Astonish:** Go, TypeScript, JavaScript, Python (4 languages)

TSX is handled by the TypeScript grammar (tree-sitter-typescript includes
TSX support). Rust can be added later if demand appears.

## 4. Architecture Overview

```
+-----------------------------------------------------------------+
|                        Agent Tool Loop                          |
+-----------------------------------------------------------------+
|                                                                 |
|  +------------+   +-----------------------------------------+    |
|  | Text Tools |   | Code Intelligence Tools                 |    |
|  | (baseline) |   | (tree-sitter powered)                   |    |
|  |            |   |                                         |    |
|  | grep_search|   | repo_map        - structural map        |    |
|  | find_files |   | code_definition - find defs             |    |
|  | file_tree  |   | code_references - find usages           |    |
|  | read_file  |   |                                         |    |
|  +------------+   +-------------------+---------------------+    |
|                                       |                         |
|  -------------------------------------+---------------------    |
|            Sandbox Boundary            |                         |
|  -------------------------------------+---------------------    |
|                                       |                         |
|                                       v                         |
|              +---------------------------------------------+    |
|              | Tree-sitter Index (in-process)              |    |
|              |                                             |    |
|              | +-----------+  +----------------------+     |    |
|              | | Per-file  |  | Cross-file           |     |    |
|              | | Scope     |  | Reference Graph      |     |    |
|              | | Graphs    |  |                      |     |    |
|              | |           |  | - Directed edges     |     |    |
|              | | - Defs    |  | - IDF-weighted       |     |    |
|              | | - Refs    |  | - PageRank-ranked    |     |    |
|              | | - Imports |  |                      |     |    |
|              | | - Scopes  |  |                      |     |    |
|              | +-----------+  +----------------------+     |    |
|              |                                             |    |
|              | Grammars: go.so, typescript.so, js.so, py.so|    |
|              | Queries:  go.scm, typescript.scm, ...       |    |
|              +---------------------------------------------+    |
|                                                                 |
|                     [ Workspace / Sandbox ]                     |
+-----------------------------------------------------------------+
```

## 5. Implementation

### 5.1. Package Structure

**Location:** `pkg/codeintel/` (new package)

**Dependencies:**
- `github.com/ebitengine/purego` — runtime `dlopen` of a bundled shared library
  (keeps `CGO_ENABLED=0` for the astonish binary)
- Language grammars compiled into one shared object at
  `/usr/lib/astonish/libastonish-treesitter.so`, packaged per sandbox backend

```
pkg/codeintel/
|-- codeintel.go          # Public API: Index, Query functions
|-- parser.go             # Parse files -> extract def/ref tags via .scm queries
|-- scope_graph.go        # Per-file scope graph (defs, refs, imports, scopes)
|-- ref_graph.go          # Cross-file directed graph (A->B if A references def in B)
|-- pagerank.go           # Rank files by structural importance (IDF-weighted)
|-- index.go              # Persistent index: build, update, serialize, cache
|-- repo_map.go           # Token-budgeted rendering of ranked file signatures
|-- tools.go              # Agent-facing tool implementations (repo_map, code_definition, code_references)
|-- languages.go          # Language registry (extension -> grammar + query mapping)
|-- queries/
|   |-- go.scm            # Go: functions, methods, interfaces, structs, vars
|   |-- typescript.scm    # TS/TSX: functions, classes, interfaces, types, exports
|   |-- javascript.scm    # JS: functions, classes, vars, exports
|   `-- python.scm        # Python: functions, classes, methods, module-level vars
`-- codeintel_test.go     # Tests with fixture repos
```

### 5.2. Query File Design

Each `.scm` file defines tree-sitter patterns that tag nodes as either
**definitions** or **references**. Example for Go:

```scheme
;; === DEFINITIONS ===

;; Package declaration
(package_clause (package_identifier) @definition.package)

;; Function declarations
(function_declaration name: (identifier) @definition.function)

;; Method declarations
(method_declaration name: (field_identifier) @definition.method)

;; Type declarations (struct, interface, alias)
(type_declaration (type_spec name: (type_identifier) @definition.type))

;; Const/var declarations
(const_spec name: (identifier) @definition.constant)
(var_spec name: (identifier) @definition.variable)

;; === REFERENCES ===

;; Qualified identifiers (pkg.Symbol)
(selector_expression
  operand: (identifier) @ref.qualifier
  field: (field_identifier) @reference)

;; Function calls
(call_expression function: (identifier) @reference)
(call_expression function: (selector_expression field: (field_identifier) @reference))

;; Type references in signatures
(type_identifier) @reference

;; Identifiers in expressions (excluding definitions captured above)
(identifier) @reference
```

The query patterns are ranked by specificity: more specific patterns
(function_declaration) capture first, preventing the generic (identifier)
pattern from double-tagging definitions.

### 5.3. Scope Graph

Each parsed file produces a scope graph with:

```go
type ScopeGraph struct {
    File    string
    Defs    []Definition  // {Name, Kind, Line, Col, Signature, Scope}
    Refs    []Reference   // {Name, Line, Col, Scope}
    Imports []Import      // {Path, Alias, Line}
    Scopes  []Scope       // {Start, End, Parent} - nested lexical scopes
}

type Definition struct {
    Name      string   // e.g., "GrepSearch"
    Kind      string   // "function", "method", "type", "interface", "variable", "constant"
    Line      int
    Col       int
    Signature string   // Full signature line (e.g., "func GrepSearch(ctx tool.Context, args GrepSearchArgs) (GrepSearchResult, error)")
    Scope     int      // Index into Scopes (which scope this def belongs to)
}
```

### 5.4. Reference Graph & PageRank

The cross-file reference graph is built by resolving references to
definitions:

1. For each reference in file A, check if a definition with that name exists
   in the imported files (resolved via import paths).
2. Create a directed edge A -> B (weighted by reference count).
3. Apply **IDF weighting**: common identifiers (`err`, `ctx`, `Get`, `New`)
   contribute less weight than domain-specific names. IDF = log(N / df) where
   N = total files, df = files containing that identifier as a definition.
4. Run PageRank (damping factor 0.85, ~20 iterations) to rank files.

This surfaces architecturally important files (heavily referenced by many
others) over utility files.

### 5.5. Tool Interfaces

#### `repo_map` Tool

```go
type RepoMapArgs struct {
    Path         string   `json:"path,omitempty" jsonschema:"Root directory to map (default: workspace root)"`
    MaxTokens    int      `json:"max_tokens,omitempty" jsonschema:"Token budget for output (default: 4096, max: 16384)"`
    FocusFiles   []string `json:"focus_files,omitempty" jsonschema:"Files to boost in ranking"`
    FocusSymbols []string `json:"focus_symbols,omitempty" jsonschema:"Symbols to boost in ranking"`
}

type RepoMapResult struct {
    Map          string `json:"map"`           // Rendered tree of file signatures
    FilesRanked  int    `json:"files_ranked"`  // Total files in the graph
    FilesShown   int    `json:"files_shown"`   // Files included in output (budget-limited)
    TokensUsed   int    `json:"tokens_used"`   // Approximate tokens in output
    IndexTimeMs  int64  `json:"index_time_ms"` // Time to build/refresh index
}
```

**Output format** (token-budgeted, PageRank-ordered):
```
pkg/sandbox/backend.go
| func NewBackend(config Config) (Backend, error)
| type Backend interface { ... }
| func (b *backend) Exec(ctx context.Context, cmd string) (string, error)

pkg/agent/chat_agent.go
| func NewChatAgent(opts ...Option) *ChatAgent
| func (a *ChatAgent) Run(ctx context.Context, input string) error
| type ChatAgent struct { ... }

pkg/tools/grep_search.go
| func GrepSearch(ctx tool.Context, args GrepSearchArgs) (GrepSearchResult, error)
| func tryRipgrep(args GrepSearchArgs, ...) ([]GrepMatch, string, error)
| func goGrep(pattern, searchPath string, ...) ([]GrepMatch, error)
...
```

#### `code_definition` Tool

```go
type CodeDefinitionArgs struct {
    Symbol string `json:"symbol" jsonschema:"Name of the symbol to find the definition of"`
    File   string `json:"file,omitempty" jsonschema:"Limit search to definitions in this file"`
    Path   string `json:"path,omitempty" jsonschema:"Limit search to this directory subtree"`
}

type CodeDefinitionResult struct {
    Definitions []DefinitionLocation `json:"definitions"`
    Total       int                  `json:"total"`
    IndexTimeMs int64                `json:"index_time_ms,omitempty"` // Only on first call (index build)
}

type DefinitionLocation struct {
    File      string `json:"file"`
    Line      int    `json:"line"`
    Column    int    `json:"column"`
    Kind      string `json:"kind"`      // "function", "method", "type", "interface", etc.
    Signature string `json:"signature"` // Full signature line
}
```

#### `code_references` Tool

```go
type CodeReferencesArgs struct {
    Symbol string `json:"symbol" jsonschema:"Name of the symbol to find references for"`
    File   string `json:"file,omitempty" jsonschema:"Only references in this file"`
    Path   string `json:"path,omitempty" jsonschema:"Limit search to this directory subtree"`
}

type CodeReferencesResult struct {
    References  []ReferenceLocation `json:"references"`
    Total       int                 `json:"total"`
    IndexTimeMs int64               `json:"index_time_ms,omitempty"`
}

type ReferenceLocation struct {
    File        string `json:"file"`
    Line        int    `json:"line"`
    Column      int    `json:"column"`
    ContextLine string `json:"context_line"` // The full source line containing the reference
}
```

### 5.6. Indexing Strategy

- **Trigger:** First invocation of any code intelligence tool.
- **File enumeration:** `git ls-files` output (respects .gitignore). Falls
  back to `filepath.Walk` with default exclusions if not a git repo.
- **Parallelism:** Worker pool (GOMAXPROCS workers). Each worker parses one
  file, produces a scope graph, sends it to the index builder.
- **Incremental updates:** When `write_file` or `edit_file` modifies a file,
  the codeintel index is notified. Only the modified file is re-parsed;
  graph edges are updated incrementally (remove old edges from that file,
  add new ones).
- **Disk cache:** Serialized to `$WORKSPACE/.codeintel/` keyed by
  `sha256(path + mtime + size)`. On warm start, only files with changed
  mtime are re-parsed.
- **Memory budget:** Target <100MB for a 500K-line repo. Scope graphs are
  compact (string-interned names, int positions).

### 5.7. Sandbox Integration

- **Grammar library:** Single shared object
  `/usr/lib/astonish/libastonish-treesitter.so` (Go/TS/TSX/JS/Python
  grammars). Path must be visible **inside the filesystem where
  `astonish node` runs** (after overlay chroot on K8s).
- **Query files:** Embedded in the `astonish` Go binary via `//go:embed`
  (`pkg/codeintel/queries/*.scm`).
- **Tool registration:** `repo_map`, `code_definition`, `code_references`
  are registered in `pkg/tools/internal.go` and added to the
  `containerTools` whitelist (proxied to sandbox like `grep_search`).
- **Main-thread availability:** These three tools are in the top-level agent's
  allowlist (`mainThreadToolAllowlist` in `pkg/launcher/chat_factory.go`), not
  only in sub-agent groups. This is deliberate: when they were reachable only
  via lazy `search_tools`/ToolIndex surfacing, agents fell back to broad
  `grep_search` plus repeated full-file `read_file` — a pattern observed to
  re-read the same large files 10-12× in one session, inflating the prompt and
  making each inference progressively slower (10s → 170s+). Keeping structural
  navigation on the main thread lets the model resolve a symbol in one call.
- **Prompt enforcement:** The system prompt carries a conditional "Code
  navigation rule (MUST)" (in `SystemPromptBuilder.Build`, gated on
  `hasCodeIntelTools`) that instructs the model to use
  `code_definition`/`code_references` for symbol definitions/uses and NOT
  `grep_search`. The rule is rendered **only** when the tree-sitter tools are
  present, so non-code agents (channels, general chat) never see it and
  `grep_search` stays domain-agnostic — its own tool description must not carry
  code-navigation advice.
- **No new processes:** Everything runs in-process within `astonish node`.
  No servers to manage, no lifecycle complexity.

#### Packaging by backend

| Backend | Tree-sitter `.so` | Ripgrep (`rg`) |
|---------|-------------------|----------------|
| **Incus (Linux native)** | `InitBaseTemplate` / `RefreshTemplate` / `BuildTemplate` build in a temp container and push to `/usr/lib/astonish/...` inside the template | `CoreToolInstallCommands()` apt-installs `ripgrep` into `@base` |
| **Incus (macOS Docker+Incus)** | Copied from `astonish-incus` image (`docker/incus/Dockerfile`) via `pushTreeSitterLibraryFromDocker` | Same `CoreToolInstallCommands()` into Incus templates (not the Docker host image) |
| **K8s** | Baked into `sandbox-base` **and** bind-mounted into the overlay by the entrypoint (`HostTreeSitterLibPath`). Dockerfile `COPY` alone is insufficient after chroot. | Installed into `@base` overlay layers via `baseconfig.Render()` → `CoreToolInstallCommands()` when `Core: true`. Do **not** rely on packaging `rg` only in the pod image. |
| **OpenShell** | Baked into `docker/sandbox-openshell` image rootfs (no Astonish overlay chroot) | apt `ripgrep` in `docker/sandbox-openshell/Dockerfile` |

Ripgrep and the tree-sitter library must live where tools execute (template/overlay/image rootfs), not only on a helper host image.

## 6. Performance Targets

| Metric | Target | Notes |
|--------|--------|-------|
| Cold index (500K lines, 4 languages) | <5s | Parallel parsing, disk cache |
| Warm index (no changes) | <100ms | mtime-based cache hit |
| Incremental update (1 file changed) | <50ms | Re-parse single file, update edges |
| `repo_map` render | <200ms | PageRank O(V+E), linear render |
| `code_definition` query | <10ms | Hash lookup on interned name |
| `code_references` query | <10ms | Hash lookup on interned name |
| Memory (index, 500K lines) | <100MB | String-interned, compact structs |
| Disk (grammar .so files) | <2MB | 4 languages |
| Disk (query .scm files) | <10KB | Embedded in binary |
| Disk (cached index) | <50MB | Serialized scope graphs + ref graph |

## 7. Feature Flags

| Flag | Default | Effect |
|------|---------|--------|
| `codeintel.enabled` | `true` | When false, `GetInternalTools` omits `repo_map`, `code_definition`, and `code_references` |
| `codeintel.library_path` | _(empty)_ | If set, exported as `ASTONISH_TREESITTER_LIB` before tool registration (overrides the default `/usr/lib/astonish/libastonish-treesitter.so`) |

Auto-inject of a repo map into the system prompt at session start is not implemented yet; those knobs are intentionally omitted until that feature lands.

## 8. Rollout Plan

### Step 1: Core Package

1. Add `pkg/codeintel/` package.
2. Implement parser with tree-sitter Go bindings.
3. Write `.scm` query files for Go, TypeScript, JavaScript, Python.
4. Implement scope graph builder.
5. Implement reference graph + IDF-weighted PageRank.
6. Implement disk-cached persistent index.
7. Unit tests with fixture repos (small Go project, small TS project).

### Step 2: Tools

1. Implement `repo_map` tool with token-budgeted rendering.
2. Implement `code_definition` tool.
3. Implement `code_references` tool.
4. Register all three in `pkg/tools/internal.go` (container-proxied).
5. Add to system prompt guidance: "Use `code_definition` and
   `code_references` for precise symbol navigation; prefer these over
   grep when you need structural usages of a specific symbol."

### Step 3: Sandbox Image (implemented)

1. Build `libastonish-treesitter.so` via `pkg/codeintel/native/Makefile`
   (used by Docker multi-stage builds).
2. **K8s:** `COPY` into `docker/sandbox-base/Dockerfile` at
   `/usr/lib/astonish/libastonish-treesitter.so`, **and** bind-mount that
   path into the composed overlay in `pkg/sandbox/k8s/overlay_entrypoint.go`
   (`HostTreeSitterLibPath`). Without the bind-mount, `astonish node`
   cannot `dlopen` the library after chroot.
3. **OpenShell:** `COPY` into `docker/sandbox-openshell/Dockerfile` image
   rootfs (sufficient; no overlay chroot).
4. **Docker+Incus (macOS):** bake into `docker/incus/Dockerfile`; template
   setup copies from the helper image during `InitBaseTemplate` /
   `RefreshTemplate`.
5. **Native-Linux Incus:** `InitBaseTemplate` / `RefreshTemplate` /
   `BuildTemplate` create a temporary `ubuntu/24.04` builder container,
   compile the library, pull out only the `.so`, and push it into the
   template. The final sandbox template does not retain compilers.
6. **Ripgrep:** install via `CoreToolInstallCommands()` for Incus + K8s
   `@base`, and via apt in the OpenShell Dockerfile. See §5.7 table.

### Step 4: Fleet Integration

1. Update Fleet software-dev agent prompts to recommend code intelligence
   tools for refactoring and cross-cutting changes.
2. Validate: Fleet agents use `code_references` before refactoring to find
   structural call sites and reduce capped grep misses.
3. Validate: Fleet agents use `repo_map` for orientation in unfamiliar
   repos instead of multiple `file_tree` + `grep_search` round-trips.

### Step 5: Incremental Index Updates

1. Wire `write_file` and `edit_file` tools to notify the codeintel index
   when files change inside the sandbox.
2. Implement incremental re-parse (single file, update graph edges).
3. Validate: after agent edits a file, subsequent `code_references` calls
   reflect the edit immediately.

## 9. Future: LSP Evaluation

> **Status: Deferred.** LSP will be evaluated after tree-sitter ships,
> based on observed failure patterns in Fleet runs. The primary concern
> is memory cost (~500MB per language server) in a multi-tenant cloud
> platform where many sessions run simultaneously.

### 9.1. When to reconsider LSP

LSP becomes justified if Fleet agents are regularly:
- Breaking code by missing Go interface implementations during refactoring
  (tree-sitter cannot detect structural typing satisfaction)
- Failing to understand complex type relationships that require full
  type checking
- Spending excessive tokens on type disambiguation that `hover` would
  resolve instantly

### 9.2. On-demand activation model

If LSP is implemented, it would use an **on-demand activation** pattern
rather than always-on servers:

**For Fleet:** The fleet setup phase detects workspace languages (scans for
`go.mod`, `tsconfig.json`, etc.) and offers to enable LSP for the session.
This is part of the plan's `capabilities` declaration:

```yaml
# In fleet plan
capabilities:
  lsp:
    auto_detect: true
    # OR explicit:
    # languages: [go, typescript]
```

**For Studio Chat:** An `enable_lsp` tool allows the agent to activate
language servers on-demand when it recognizes sustained development work:

```json
{
  "name": "enable_lsp",
  "description": "Start language servers for type-aware code intelligence. Call this when you need precise type information, interface implementation discovery, or post-edit diagnostics for sustained development work.",
  "parameters": {
    "languages": "Optional list: ['go', 'typescript', 'python']. If omitted, auto-detects from workspace markers.",
    "wait_ready": "Wait for servers to initialize before returning (default: true)"
  }
}
```

**Lifecycle:** Servers auto-shutdown after 5 minutes of inactivity to
reclaim memory. Re-activated on next `lsp` tool call.

### 9.3. Resource concern

In a multi-tenant cloud platform:
- 50 concurrent Fleet sessions x 500MB gopls = 25GB just for Go LSP
- Plus tsserver, pyright, etc. for polyglot workloads
- Versus tree-sitter: 50 sessions x 100MB index = 5GB total

Options to mitigate (if LSP proves necessary):
1. **Ephemeral servers** - start on demand, kill after idle timeout
2. **Shared LSP pool** - language server as a microservice, multiplexed
   across sessions (complex, would require protocol proxying)
3. **Per-template opt-in** - only templates marked "heavy development"
   get LSP; lightweight templates use tree-sitter only

### 9.4. What other cloud platforms do

| Platform | LSP? | Notes |
|----------|------|-------|
| Grok (xAI) | Yes | Runs locally, not multi-tenant cloud |
| OpenClaude | Yes | Runs locally, not multi-tenant cloud |
| OpenClaw | Yes | Runs on host (not in sandbox), single-user |
| **Astonish** | **Deferred** | Multi-tenant cloud; memory cost is prohibitive without mitigation |

All three platforms that use LSP run **locally on user hardware** where
memory is abundant and not shared. Astonish is the only one operating as
a multi-tenant cloud service where per-session memory directly impacts
cost and density.

## 10. Comparison with Prior Art

| Aspect | Grok (xAI) | OpenClaude | OpenClaw | Astonish (planned) |
|--------|------------|------------|----------|-------------------|
| Tree-sitter | Native Rust | WASM | Bash only | **Native Go** |
| Languages | 5 (Rust,TS,JS,Go,Py) | 4 (TS,TSX,JS,Py) | 0 | **4 (Go,TS,JS,Py)** |
| Code graph | Scope graph | PageRank | None | **Both** |
| Auto-inject context | No | Yes (RepoMap) | No | **Yes, opt-in** |
| LSP | Yes (6 ops) | Yes (9 ops) | Yes (3 ops) | **Deferred** |
| LSP runs where | Host | Host | Host | N/A (sandbox if added) |
| Execution env | Local | Local | Local | **Multi-tenant cloud** |
| Memory model | Unlimited | Unlimited | Unlimited | **Constrained per-session** |

## 11. Open Questions

1. **Grammar compilation strategy:** Should grammar `.so` files be
   cross-compiled at build time and embedded in the Go binary via
   `//go:embed`, or compiled separately and placed in the sandbox image?
   Embedding simplifies distribution; separate files allow grammar updates
   without rebuilding the binary.

2. **Index sharing across sandbox restarts:** If a Fleet session's sandbox
   is recycled (e.g., pod preemption in K8s), should the codeintel cache
   persist on a PVC? Or is re-indexing on restart fast enough (<5s) to not
   bother?

3. **Symbol disambiguation:** If multiple files define a symbol with the
   same name (e.g., `Backend` in 3 packages), how should `code_definition`
   present results? Options: rank by PageRank importance, group by package,
   or require the caller to specify a path filter.

4. **Token estimation:** The `repo_map` tool needs to estimate token count
   for budget enforcement. Use a simple heuristic (chars/4) or integrate
   a proper tokenizer (tiktoken)? Heuristic is faster but less accurate.

## 12. References

- Grok tree-sitter implementation: `/root/grok-build/crates/codegen/xai-codebase-graph/`
- OpenClaude RepoMap: `/root/openclaude/src/context/repoMap/`
- OpenClaw LSP integration: `/root/openclaw/src/agents/agent-bundle-lsp-runtime.ts`
- Aider tree-sitter queries: MIT licensed, basis for `.scm` query files
- Runtime FFI: `github.com/ebitengine/purego` (`pkg/codeintel/internal/treesitter`)
- Tree-sitter grammars: `github.com/tree-sitter/tree-sitter-{go,typescript,javascript,python}`
- PageRank algorithm: Brin & Page, 1998 (damping factor 0.85)
