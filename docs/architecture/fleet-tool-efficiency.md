# Fleet Tool Efficiency: Reducing Redundant File Operations

**Date:** 2026-07-14  
**Status:** Accepted  
**Motivation:** Fleet session `8bf63fbf` (issue #77) showed the dev agent reading the same 58KB file 58 times during a 9-minute implementation pass. This document defines optimizations to eliminate redundant file I/O and reduce token consumption by ~90%.

---

## Problem Statement

When a fleet agent (or any sandboxed agent) implements a feature, the typical tool-call pattern is:

```
read_file(path) → think → edit_file(path, old, new) → read_file(path) → verify → edit_file(...) → read_file(path) → ...
```

Each `read_file` call on a large file (e.g., 1911-line Vue component = 58KB) triggers:
1. A sandbox round-trip (host → container via NDJSON pipe, gRPC, or per-call Exec)
2. Full file content returned to the host
3. Truncation to 50KB by `TruncateToolResponsesCallback` (losing the tail)
4. The full 50KB injected into the LLM context window

In session `8bf63fbf`, the dev agent called `read_file` on the same file **58 times** (35 times on another), consuming ~5.4MB of input tokens on a single file. This is the dominant source of slowness vs. interactive coding tools like Cursor.

**Root causes:**
1. `edit_file` returns only `{"success": true, "replacements": 1}` — no verification context, so the agent re-reads to confirm.
2. `read_file` has no line-range support — the agent must read the entire file even when it only needs 20 lines.
3. No caching layer — identical reads produce identical round-trips every time.

---

## Design Principles

1. **All filesystem state and logic lives inside the container.** The sandbox is the isolation boundary. The host never has direct knowledge of or access to the container's filesystem. No host-side caching of file content or metadata.
2. **Must work on K8s per-call Exec.** The K8s backend (`backendNodeClient`) spawns a fresh `astonish node` process per tool call. In-process state does not survive between calls. Any caching must be persisted to the container's filesystem.
3. **Backend-agnostic.** The same tool implementation runs identically on Incus (persistent pipe), OpenShell (gRPC), K8s (per-call Exec), and Mock.
4. **The real cost is tokens, not network I/O.** Even on K8s where process spawn is 200-500ms, the savings come from not injecting 50KB into the LLM context on every turn. A 100-byte stub response vs. 58KB payload is the win.

---

## Prior Art: OpenClaude (Claude Code)

Analysis of `/root/openclaude` reveals how Claude Code solves the same problem:

| Mechanism | OpenClaude Implementation | Relevance to Astonish |
|-----------|--------------------------|----------------------|
| Line-range reads | `offset` (1-indexed start) + `limit` (line count) params | Direct adoption |
| Line numbers in output | `{lineNum}→{content}` format via `addLineNumbers()` | Direct adoption |
| Mtime-based dedup | LRU cache (100 entries, 25MB) checked on each read; returns `"file_unchanged"` stub if mtime matches | Adopt with disk-persistence for K8s |
| "Must read first" guard | Edit fails if file hasn't been read in current session | Adopt — prevents blind edits |
| Edit response | Minimal: `"The file has been updated successfully."` — NO verification context | Diverge — we add context for non-Claude models |
| Post-compaction restore | Re-inject 5 most recent files as attachments after compaction | Future consideration |
| Cache key design | `offset: undefined` on Edit/Write entries distinguishes them from Read entries; dedup only fires on Read-sourced entries | Adopt this pattern |

Key insight from their telemetry: *"~18% of Read calls are same-file collisions (up to 2.64% of fleet cache_creation)"* — confirming the problem exists at scale.

---

## Solution: Three Layers (All Inside the Container)

### Layer 1: `edit_file` Auto-Verify Response

After a successful edit, `edit_file` returns a **compact unified-style hunk** around the edit site so the agent never needs a follow-up `read_file` to verify what changed.

#### Changes

**File:** `pkg/tools/edit_file.go`

```go
type EditFileResult struct {
    Success             bool   `json:"success"`
    Path                string `json:"path"`
    Replacements        int    `json:"replacements"`
    Message             string `json:"message"`
    VerificationContext string `json:"verification_context,omitempty"`
}
```

After `os.WriteFile` succeeds:
1. Locate the **first** match in the **pre-edit** content (`oldString` exact match, or first regex match). Never search for `newString` in the post-edit file — that fails for deletions and short strings that appear earlier.
2. Derive the removed text (matched span) and added text (replacement, with regex expansion when needed).
3. Build a mini hunk: header `@@ basename:startLine`, ~3 context lines before, `-` removed lines, `+` added lines, ~3 context lines after. Cap body at ~30 lines.
4. Message includes the 1-based start line: `"Replaced N occurrence(s) in PATH (line L)"`.

**Edge cases:**
- **`replace_all=true`**: Hunk shows the **first** replacement only. Message still reports full count `"Replaced N occurrence(s)..."`.
- **Deletion (empty `NewString`)**: Locate via `oldString` / regex in pre-edit content; emit `-` lines and surrounding context (not the top of the file).
- **Very long replacement**: Cap the hunk body at 30 lines; truncate large remove/add blocks with `…`.

#### Response example

```json
{
    "success": true,
    "path": "/root/project/src/handler.go",
    "replacements": 1,
    "message": "Replaced 1 occurrence(s) in /root/project/src/handler.go (line 42)",
    "verification_context": "@@ handler.go:42\n  40| // handle requests\n  41| \n- 42| func handler(w http.ResponseWriter) {\n+ 42| func handler(w http.ResponseWriter, r *http.Request) {\n  43|     ..."
}
```

#### Impact

Eliminates the "did my edit work?" re-read pattern and makes deletions/replacements obvious (same shape as OpenCode / Grok Build diffs). Estimated: **~50% reduction** in post-edit reads.

### Layer 1b: `write_file` Auto-Verify Response

`write_file` returns the same style of compact hunk so create/overwrite are verifiable without a follow-up `read_file`.

**File:** `pkg/tools/internal.go` (`WriteFileResult` + `buildWriteVerificationContext` in `edit_file.go`)

```go
type WriteFileResult struct {
    Message             string `json:"message"`
    Path                string `json:"path,omitempty"`
    Created             bool   `json:"created,omitempty"`
    VerificationContext string `json:"verification_context,omitempty"`
}
```

Before `os.WriteFile`, read existing content if present:

| Case | Header | Body |
|------|--------|------|
| **Create** (file did not exist) | `@@ basename:1 (created)` | `+` lines only (cap ~30, then `…`) |
| **Overwrite** | `@@ basename:1 (overwritten, N→M lines)` | `-` old lines then `+` new lines (budget split, capped) |

Message: `"Created PATH"` or `"Overwrote PATH"`.

---

### Layer 2: `read_file` Line-Range Support + Line Numbers

Allow the agent to request a specific line window instead of the entire file. Always return line-numbered content.

#### Changes

**File:** `pkg/tools/internal.go`

```go
type ReadFileArgs struct {
    Path      string `json:"path" jsonschema:"Absolute path to the file to read"`
    Offset    *int   `json:"offset,omitempty" jsonschema:"Line number to start reading from (1-indexed). Omit to start from line 1."`
    Limit     *int   `json:"limit,omitempty" jsonschema:"Maximum number of lines to read. Omit to read to end of file."`
}

type ReadFileResult struct {
    Content    string `json:"content"`
    TotalLines int    `json:"total_lines"`
    Range      string `json:"range,omitempty"`
    Unchanged  bool   `json:"unchanged,omitempty"`
}
```

Semantics (matching OpenClaude's proven interface):
- `offset`: 1-indexed start line (default: 1)
- `limit`: number of lines to return (default: all remaining)

Processing:
1. Read file content via `os.ReadFile`
2. Split by `\n`, count total lines
3. Clamp: `start = max(1, offset)`, `end = min(totalLines, start + limit - 1)`
4. Slice `lines[start-1 : end]`
5. Prefix each line with its 1-indexed line number: `"140: const foo = bar"`
6. Set `Range` (e.g., `"lines 140-240 of 1911"`)
7. Always set `TotalLines`

Full-file reads (no offset/limit) also get line numbers and `TotalLines`.

#### Tool description update

```
"Read file contents with line numbers. For large files, use offset and limit to read specific sections. Returns line-numbered content. Use grep_search to find relevant line numbers first."
```

#### Impact

A 1911-line file read with a 100-line window: **95% payload reduction** (58KB → ~3KB).

---

### Layer 3: In-Container Mtime-Based Read Cache

A file-persisted cache inside the container that deduplicates `read_file` calls when the file hasn't changed since the last read.

#### Design Constraint: K8s Per-Call Exec

On K8s, each tool call spawns a fresh `astonish node` process. The cache must survive between process invocations. Solution: **persist the cache to a JSON file inside the container's filesystem** (`/tmp/.astonish_read_cache.json`). The container's filesystem (overlay/PVC) is persistent for the session lifetime.

#### Cache File Format

```json
{
    "version": 1,
    "entries": {
        "/root/project/src/Component.vue:1:0": {
            "mtime_ns": 1752454679000000000,
            "total_lines": 1911,
            "offset": 1,
            "limit": 0,
            "source": "read"
        },
        "/root/project/src/Component.vue:140:100": {
            "mtime_ns": 1752454679000000000,
            "total_lines": 1911,
            "offset": 140,
            "limit": 100,
            "source": "read"
        }
    }
}
```

Cache key: `"{path}:{offset}:{limit}"` (where 0 means "not specified").

**Fields:**
- `mtime_ns`: file modification time at the moment of the read (nanosecond precision from `os.Stat`)
- `total_lines`: total line count at time of read
- `offset`, `limit`: the range that was read
- `source`: `"read"` for entries created by `read_file`, `"edit"` or `"write"` for entries created by mutations (following OpenClaude's `offset: undefined` pattern — dedup only fires on `source: "read"` entries)

#### Flow

On `read_file` call inside `ExecuteTool` / `ReadFile`:

```
1. Load cache from /tmp/.astonish_read_cache.json (if exists)
2. Build cache key from path + offset + limit
3. Look up entry:
   a. If entry exists AND entry.source == "read":
      - stat() the file, get current mtime
      - If mtime matches entry.mtime_ns:
        → Return stub: {"content": "...", "unchanged": true, "total_lines": N}
      - If mtime differs:
        → Cache miss, proceed to full read, update entry
   b. If no entry or entry.source != "read":
      → Cache miss, proceed to full read, store new entry
4. After read completes: update cache entry with current mtime
5. Write cache back to /tmp/.astonish_read_cache.json
```

On `edit_file` / `write_file` call:

```
1. Load cache
2. After successful write: update/create entry for that path with source="edit", new mtime
   (This invalidates future dedup checks because source != "read")
3. Also invalidate all range-specific entries for that path
4. Write cache back
```

On `shell_command` call:

```
1. Load cache
2. Mark ALL entries as verified=false (do NOT delete them)
3. Write cache back
```

On next `read_file` cache lookup with `verified=false` entry:

```
1. stat() the file, get current mtime
2. If mtime matches → promote to verified=true, return stub (cache hit)
3. If mtime differs → cache miss, full read, update entry
```

This is less aggressive than full deletion. Commands like `go test`, `npm run lint`,
`git status` don't modify source files — the next read costs one extra stat() (~0.1ms)
instead of a full 58KB re-read. Only mutations (detected by mtime change) force a full read.

#### "Must Read Before Edit" Guard

Before `edit_file` proceeds, it checks the cache for a `source: "read"` entry for the target path. If none exists, the edit is rejected:

```json
{
    "success": false,
    "message": "You must read this file before editing it. Use read_file first."
}
```

This prevents hallucinated edits where the LLM guesses file content without reading it. After a successful read, the cache entry exists and subsequent edits proceed normally. This matches OpenClaude's proven pattern.

#### The `force` Parameter

To handle post-compaction scenarios where the LLM has lost the prior read from its conversation history:

```go
type ReadFileArgs struct {
    Path   string `json:"path" jsonschema:"Absolute path to the file to read"`
    Offset *int   `json:"offset,omitempty" jsonschema:"Line number to start reading from (1-indexed). Omit to start from line 1."`
    Limit  *int   `json:"limit,omitempty" jsonschema:"Maximum number of lines to read. Omit to read to end of file."`
    Force  bool   `json:"force,omitempty" jsonschema:"Bypass the read cache and always return file content, even if unchanged. Use when you no longer have the file content in your conversation history."`
}
```

When `force=true`, skip the cache check and always return full content (still updates the cache for future calls).

#### Stub Response

When a cache hit occurs:

```json
{
    "unchanged": true,
    "total_lines": 1911,
    "range": "lines 1-1911 of 1911"
}
```

The `unchanged: true` flag signals to the LLM that this is a cache hit. No verbose text — the tool description and agent prompt teach the LLM to interpret this field.

#### Observability

Cache hits and misses are logged for post-session analysis:

```go
slog.Info("file_read_cache", "hit", true, "path", path, "offset", offset, "limit", limit)
slog.Info("file_read_cache", "hit", false, "path", path, "reason", "mtime_changed")
```

#### Cache Size Limits

- Maximum 100 entries (LRU eviction by access time)
- Cache entries store only metadata (~150 bytes each), not file content — max cache size ~15KB

#### Impact

For 58 reads of the same file with only ~5 intervening edits: **~45 reads return the stub** instead of 58KB. The LLM context stays clean. Token savings: ~5MB → ~500KB.

---

### Layer 4: Fleet Dev Agent Prompt Update

**File:** `pkg/fleet/bundled/software-dev.yaml` — dev agent behavior

```yaml
## File Reading Efficiency
- When reading large files (>200 lines), ALWAYS use offset/limit to read only the section you need
- After a successful edit_file, DO NOT re-read the file — the verification_context in the response confirms your edit landed correctly
- Only re-read a file section if: (a) tests fail and you need to debug, or (b) you need to see a DIFFERENT section than what you edited
- Use grep_search to find the right line numbers before reading, rather than reading the whole file to locate a function
- If read_file returns unchanged=true, trust it — your earlier read is still in context. Only use force=true if you've lost the content (e.g., after a very long conversation)
```

---

## Implementation Order

1. **Layer 2** (read_file line ranges + line numbers) — biggest token win, purely functional, stateless, zero risk
2. **Layer 1** (edit_file verification context) — purely functional, stateless, zero risk
3. **Layer 3** (mtime cache with disk persistence) — stateful, requires testing on K8s
4. **Layer 4** (prompt update) — depends on Layers 1-3 being deployed

Layers 1 and 2 are independent and can be implemented in parallel. Layer 3 depends on Layer 2's `ReadFileArgs` signature being finalized.

---

## Expected Impact

| Metric | Before | After (all layers) |
|--------|--------|-------------------|
| read_file calls per dev pass | ~58 | ~8-12 |
| Bytes sent to LLM per session | ~5.4 MB | ~400-600 KB |
| Dev agent wall-clock time | ~9 min | ~5-6 min |
| Sandbox round-trips (with content) | ~93 | ~30-40 |
| Token cost per feature | ~$2-4 (est.) | ~$0.50-1.00 (est.) |

---

## Testing Strategy

- **Unit tests** for edit_file verification context:
  - Edit at start, middle, end of file
  - Multi-match with `replace_all`
  - Regex mode with capture groups
  - Very long replacement strings (context window doesn't explode)
  - Empty file, single-line file

- **Unit tests** for read_file line ranges:
  - `offset=0` (treated as 1), `limit` > total lines, `offset` past EOF
  - Single-line file, empty file
  - Line number formatting (correct 1-indexing)
  - Full-file read still returns `total_lines`
  - Binary-like content (no panics on split)

- **Unit tests** for mtime cache:
  - Cache hit (unchanged file)
  - Cache miss (file modified between reads)
  - Invalidation by `edit_file`, `write_file`
  - Full invalidation by `shell_command`
  - `force=true` bypass
  - `source` field: dedup only on `"read"` entries
  - Cache file persistence (write, reload in fresh process)
  - LRU eviction at 100 entries
  - Corrupt/missing cache file (graceful degradation)

- **Integration validation**: re-run a fleet issue after deployment and compare:
  - `read_file` call counts
  - Truncation log frequency
  - Wall-clock time
  - Baseline: session `8bf63fbf`

---

## Interaction with Existing Systems

- **TruncateToolResponsesCallback** (`pkg/agent/tool_response_truncate.go`): With line ranges, responses will rarely exceed 50KB. Truncation becomes a safety net rather than a common path. No conflict — it operates at `BeforeModelCallback` level (after tool execution, before next LLM call).

- **Sandbox wrapping (NodeTool)** (`pkg/sandbox/node_tool.go`): Unchanged. All three layers are implemented inside the tool functions that run within the container via `ExecuteTool()`. The NodeTool proxy sees a smaller response payload on the wire — that's the only observable difference.

- **K8s per-call Exec** (`pkg/sandbox/backend_pool.go`): The cache file adds ~2-5ms of file I/O per tool call (read + write `/tmp/.astonish_read_cache.json`). Negligible vs. the 200-500ms process spawn. The real win is the smaller response payload.

- **Credential/secret callbacks**: No conflict. These operate at the ADK callback level on the host. The in-container tool functions are unaware of them.

- **Compaction** (`pkg/agent/`): Reduced token volume means compaction triggers less frequently. The `force=true` parameter handles the edge case where compaction removes a prior read from context and the LLM needs to re-read.

- **`ExecuteTool` dispatcher** (`pkg/tools/internal.go:574`): The cache logic integrates directly into the `ReadFile`, `EditFile`, `WriteFile`, and `ShellCommand` functions (or a shared helper they call). The dispatcher remains unchanged.

---

## File Change Summary

| File | Change |
|------|--------|
| `pkg/tools/internal.go` | `ReadFileArgs` gains `Offset`, `Limit`, `Force` fields; `ReadFileResult` gains `TotalLines`, `Range`, `Unchanged`; `ReadFile()` rewritten with line-range + line-number + cache logic |
| `pkg/tools/edit_file.go` | `EditFileResult` gains `VerificationContext`; `EditFile()` computes context window after write; calls cache invalidation |
| `pkg/tools/file_read_cache.go` (new) | `FileReadCache` struct: Load/Save from disk, Get/Set/Invalidate/InvalidateAll, LRU eviction |
| `pkg/tools/internal.go` (`GetInternalTools`) | Updated description for `read_file` |
| `pkg/fleet/bundled/software-dev.yaml` | Dev agent efficiency guidance in behavior section |
| `pkg/tools/edit_file_test.go` | Tests for verification context |
| `pkg/tools/internal_test.go` or `read_file_test.go` (new) | Tests for line ranges, line numbers, cache |

---

## References

- Fleet session `8bf63fbf` analysis (issue #77, "Show existing positions in Options Chain")
- OpenClaude (`/root/openclaude`) — `FileReadTool.ts`, `FileEditTool.ts`, `fileStateCache.ts`, `readFileInRange.ts`
- ADK tool execution: `google.golang.org/adk@v0.3.0/internal/llminternal/base_flow.go`
- In-container tool dispatch: `pkg/tools/internal.go:ExecuteTool()`
- Container node server: `cmd/astonish/node.go`
- Sandbox tool proxy: `pkg/sandbox/node_tool.go`
- K8s per-call backend: `pkg/sandbox/backend_pool.go`
- Truncation: `pkg/agent/tool_response_truncate.go`
