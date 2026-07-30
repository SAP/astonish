# File & Search Tools

Six tools for reading, writing, editing, and searching the filesystem.

## Tools

| Tool | Description | Confirmation |
|------|-------------|-------------|
| `read_file` | Read file contents | auto-approve |
| `write_file` | Create or overwrite a file | always-confirm |
| `edit_file` | Apply targeted find-and-replace edits | always-confirm |
| `file_tree` | Display directory structure | auto-approve |
| `grep_search` | Search for text patterns in files | auto-approve |
| `find_files` | Find files by name pattern (glob) | auto-approve |

## read_file

Reads file content:

```
read_file:
  path: "src/main.go"
```

Supports binary detection (returns metadata instead of content for binary files).

## write_file

Creates or overwrites a file. In Studio, renders a diff preview before confirmation:

```
write_file:
  file_path: "config.yaml"
  content: |
    name: my-app
    version: 1.0.0
```

On success, the result includes a compact **unified-style hunk** in `verification_context`:
- **Create** — header `@@ file:1 (created)` with `+` lines for the new content
- **Overwrite** — header `@@ file:1 (overwritten, N→M lines)` with `-` old lines and `+` new lines

Large files are truncated with `…` so the tool result stays bounded. Prefer `edit_file` for surgical changes; use `write_file` for new files or full rewrites.

## edit_file

Applies surgical find-and-replace edits without rewriting the entire file:

```
edit_file:
  path: "src/handler.go"
  old_string: "func handler(w http.ResponseWriter)"
  new_string: "func handler(w http.ResponseWriter, r *http.Request)"
```

Supports:
- Exact string matching (default)
- Regex patterns (`regex: true`)
- Replace all occurrences (`replace_all: true`)

On success, the result includes a compact **unified-style hunk** in `verification_context` (with surrounding context, `-` removed lines, and `+` added lines) so you can confirm the edit without a follow-up `read_file`. Deletions (`new_string` empty) still locate the edit from the pre-edit file and show the removed region, not the top of the file. For `replace_all`, the hunk covers the first occurrence; the message reports the full replacement count.

## grep_search

Searches for text patterns across files (uses ripgrep when available):

```
grep_search:
  pattern: "TODO|FIXME"
  search_path: "src/"
  include_globs: ["*.go"]
  max_results: 50
```

## find_files

Finds files by name pattern using glob matching:

```
find_files:
  pattern: "*.test.ts"
  search_path: "src/"
  max_results: 50
```

## file_tree

Displays directory structure:

```
file_tree:
  path: "."
  depth: 3
```

See [Shell & Process Tools](./shell-process.md) for command execution and [Tools Overview](./index.md) for the full tool catalog.
