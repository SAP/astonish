// Package pathscope provides the pure, domain-agnostic path primitives that
// back Astonish's code-mode folder-access boundary. It is the single source of
// truth for:
//
//   - normalizing/expanding filesystem paths (ExpandHome, NormalizePath),
//   - deciding containment within a project root (PathWithin), and
//   - best-effort extraction of the filesystem paths embedded in a free-form
//     shell command string (ExtractCommandPaths).
//
// Both the agent authorization gate (pkg/agent) and the shell tool guard
// (pkg/tools) import these helpers so the two enforcement points can never
// drift apart. Everything here is pure (no shared mutable state) and safe for
// concurrent use.
package pathscope

import (
	"os"
	"path/filepath"
	"strings"
)

// ExpandHome resolves a leading ~ / ~/ to the user's home directory. Go's os
// and filepath packages do not expand ~ (it is a shell feature), so
// LLM-provided paths like "~/snake/main.py" would otherwise be treated as
// relative. Only the leading "~" or "~/" is expanded; ~user syntax is not.
func ExpandHome(path string) string {
	if path == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
		return path
	}
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}

// NormalizePath expands ~, makes the path absolute, and resolves symlinks as
// far as they exist. If the path (or a prefix) does not exist yet — common for
// write_file creating a new file — it resolves the deepest existing ancestor
// and re-appends the remainder, so containment checks work for not-yet-created
// files. Returns "" only when the path cannot be made absolute.
func NormalizePath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	path = ExpandHome(path)
	abs, err := filepath.Abs(path)
	if err != nil {
		return ""
	}
	abs = filepath.Clean(abs)

	// Resolve symlinks on the deepest existing ancestor to defeat symlink
	// escapes, then re-attach the non-existent tail.
	existing := abs
	var tail []string
	for {
		if _, statErr := os.Lstat(existing); statErr == nil {
			break
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			break
		}
		tail = append([]string{filepath.Base(existing)}, tail...)
		existing = parent
	}
	if resolved, rerr := filepath.EvalSymlinks(existing); rerr == nil {
		existing = resolved
	}
	if len(tail) > 0 {
		return filepath.Join(append([]string{existing}, tail...)...)
	}
	return existing
}

// NormalizeDir returns the absolute, symlink-resolved form of a directory path.
// Returns "" if the input is empty.
func NormalizeDir(dir string) string {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return ""
	}
	return NormalizePath(dir)
}

// NormalizePathInRoot resolves a path relative to root (instead of the process
// CWD). If path is already absolute, it behaves like NormalizePath. If path is
// relative and root is non-empty, resolves as filepath.Join(root, path) then
// proceeds with symlink resolution. Falls back to NormalizePath (CWD-based) if
// root is empty. This ensures tool arguments like "pkg/tools/internal.go" are
// resolved against the project directory, not the Go process CWD.
func NormalizePathInRoot(path, root string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	path = ExpandHome(path)
	if !filepath.IsAbs(path) && root != "" {
		path = filepath.Join(root, path)
	}
	// From here, same logic as NormalizePath: make absolute, resolve symlinks.
	abs, err := filepath.Abs(path)
	if err != nil {
		return ""
	}
	abs = filepath.Clean(abs)

	// Resolve symlinks on the deepest existing ancestor to defeat symlink
	// escapes, then re-attach the non-existent tail.
	existing := abs
	var tail []string
	for {
		if _, statErr := os.Lstat(existing); statErr == nil {
			break
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			break
		}
		tail = append([]string{filepath.Base(existing)}, tail...)
		existing = parent
	}
	if resolved, rerr := filepath.EvalSymlinks(existing); rerr == nil {
		existing = resolved
	}
	if len(tail) > 0 {
		return filepath.Join(append([]string{existing}, tail...)...)
	}
	return existing
}

// DirOf returns the directory containing path (the path itself if it is a
// directory), normalized. Used when granting a path "for session".
func DirOf(path string) string {
	abs := NormalizePath(path)
	if abs == "" {
		return ""
	}
	if info, err := os.Stat(abs); err == nil && info.IsDir() {
		return abs
	}
	return filepath.Dir(abs)
}

// PathWithin reports whether candidate is root or lives inside root's subtree.
// Both are expected to be absolute + cleaned. Rejects ".." escapes.
func PathWithin(root, candidate string) bool {
	if root == "" || candidate == "" {
		return false
	}
	if root == candidate {
		return true
	}
	rel, err := filepath.Rel(root, candidate)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	// filepath.Rel can return an absolute path on Windows across volumes; guard.
	if filepath.IsAbs(rel) {
		return false
	}
	return true
}

// filesystemCommands is the set of command names that are known to touch the
// filesystem. Only arguments of these commands are inspected for out-of-scope
// paths. Commands not in this set (git, curl, npm, docker, go, python, etc.)
// are not inspected — their path-shaped arguments typically represent remote
// refs, URLs, or other non-filesystem operands that should not trigger
// folder-access prompts. This mirrors the approach used by OpenCode.
var filesystemCommands = map[string]bool{
	// Directory navigation
	"cd": true, "pushd": true, "popd": true,
	// File reading
	"cat": true, "less": true, "more": true, "head": true, "tail": true, "tac": true,
	// File manipulation
	"cp": true, "mv": true, "rm": true, "mkdir": true, "rmdir": true,
	"touch": true, "chmod": true, "chown": true, "chgrp": true,
	"ln": true, "readlink": true, "realpath": true, "install": true,
	// File inspection
	"ls": true, "dir": true, "find": true, "locate": true,
	"stat": true, "file": true, "du": true, "df": true, "wc": true,
	// Archives
	"tar": true, "zip": true, "unzip": true, "gzip": true, "gunzip": true,
	"bzip2": true, "bunzip2": true, "xz": true, "unxz": true,
	// Source/execute
	"source": true, ".": true,
	// Open
	"open": true, "xdg-open": true,
	// Editors (when used non-interactively)
	"tee": true, "dd": true, "truncate": true, "shred": true,
}

// commandPrefixes are tokens that wrap the real command (e.g. "sudo cat" → the
// real command is "cat"). When a segment starts with one of these, we skip it
// and look at the next token as the command name.
var commandPrefixes = map[string]bool{
	"sudo": true, "env": true, "nice": true, "nohup": true,
	"time": true, "command": true, "builtin": true, "exec": true,
	"doas": true, "strace": true, "ltrace": true,
}

// ExtractCommandPaths extracts filesystem path tokens from shell commands that
// are known to touch the filesystem. Commands like git, curl, npm, docker, etc.
// are NOT inspected because their path-shaped arguments typically do not
// represent filesystem access outside the project.
//
// The function splits the command into segments (separated by shell operators
// |, &, ;, etc.), identifies the command name in each segment (skipping
// prefixes like sudo/env and env-assignment tokens), and only extracts
// path-shaped arguments from commands in the filesystemCommands set.
//
// Redirect targets (>, >>, <) are always treated as filesystem paths regardless
// of the command name, because redirects always touch the filesystem.
//
// The tokenizer is QUOTE-AWARE: a single- or double-quoted span is part of one
// atomic token, so word/operator boundaries INSIDE quotes do not split it. This
// prevents false positives from quoted literal data (e.g. git commit messages
// containing "/").
//
// Returns the raw (un-normalized) tokens; the caller normalizes + tests
// containment so this stays a pure string function.
func ExtractCommandPaths(command string) []string {
	if strings.TrimSpace(command) == "" {
		return nil
	}

	segments, redirectTargets := splitCommandSegments(command)

	var out []string
	seen := make(map[string]bool)
	add := func(tok string) {
		tok = strings.TrimSpace(tok)
		if tok == "" || seen[tok] {
			return
		}
		if looksLikePathToken(tok) {
			seen[tok] = true
			out = append(out, tok)
		}
	}

	// Redirect targets are always filesystem paths regardless of command.
	for _, rt := range redirectTargets {
		add(rt)
	}

	// Process each command segment independently.
	for _, seg := range segments {
		cmdName, args := identifyCommand(seg)
		if cmdName == "" || !filesystemCommands[cmdName] {
			continue
		}
		// Extract path-shaped arguments from this filesystem command.
		for _, arg := range args {
			arg = strings.TrimSpace(arg)
			if arg == "" {
				continue
			}
			// Handle --opt=value flags: inspect the value.
			if eq := strings.IndexByte(arg, '='); eq >= 0 && eq < len(arg)-1 && !strings.ContainsAny(arg, " \t") {
				add(arg[eq+1:])
				continue
			}
			// Bare flags are never paths.
			if strings.HasPrefix(arg, "-") {
				continue
			}
			add(arg)
		}
	}
	return out
}

// identifyCommand finds the command name in a segment, skipping command
// prefixes (sudo, env, etc.) and env-assignment tokens (KEY=VALUE). Returns
// the base name of the command and the remaining argument tokens.
func identifyCommand(tokens []string) (string, []string) {
	i := 0
	for i < len(tokens) {
		tok := tokens[i]
		// Skip command prefixes (sudo, env, nice, etc.)
		base := filepath.Base(tok)
		if commandPrefixes[base] {
			i++
			// For "env", also skip KEY=VALUE tokens that follow it.
			if base == "env" {
				for i < len(tokens) && isEnvAssignment(tokens[i]) {
					i++
				}
			}
			continue
		}
		// Skip env-assignment tokens at the start (e.g. "FOO=bar cmd ...")
		if isEnvAssignment(tok) {
			i++
			continue
		}
		// This is the command name.
		if i+1 < len(tokens) {
			return filepath.Base(tok), tokens[i+1:]
		}
		return filepath.Base(tok), nil
	}
	return "", nil
}

// isEnvAssignment reports whether a token looks like a shell env assignment
// (KEY=VALUE where KEY is a valid identifier). Does not match tokens with
// whitespace (those come from quoted literals).
func isEnvAssignment(tok string) bool {
	if strings.ContainsAny(tok, " \t") {
		return false
	}
	eq := strings.IndexByte(tok, '=')
	if eq <= 0 {
		return false
	}
	key := tok[:eq]
	for i, r := range key {
		if r == '_' || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') {
			continue
		}
		if i > 0 && r >= '0' && r <= '9' {
			continue
		}
		return false
	}
	return true
}

// splitCommandSegments splits a command line into segments (separated by shell
// operators |, &, ;, &&, ||) and also extracts redirect targets. Each segment
// is a slice of tokens representing one simple command. Redirect targets are
// returned separately because they always represent filesystem access regardless
// of the command.
//
// The tokenizer is quote-aware: quoted spans are treated as single tokens.
func splitCommandSegments(command string) (segments [][]string, redirectTargets []string) {
	var (
		cur           strings.Builder
		hasTok        bool
		inQuote       rune
		currentSeg    []string
		afterRedirect bool // next token is a redirect target
	)

	flush := func() {
		if hasTok {
			tok := cur.String()
			cur.Reset()
			hasTok = false

			if afterRedirect {
				redirectTargets = append(redirectTargets, tok)
				afterRedirect = false
				return
			}
			currentSeg = append(currentSeg, tok)
		}
		// Note: we do NOT reset afterRedirect when hasTok is false.
		// Whitespace between '>' and the target should not cancel the redirect.
	}

	finishSegment := func() {
		flush()
		if len(currentSeg) > 0 {
			segments = append(segments, currentSeg)
			currentSeg = nil
		}
		afterRedirect = false
	}

	for _, r := range command {
		if inQuote != 0 {
			if r == inQuote {
				inQuote = 0
				continue
			}
			cur.WriteRune(r)
			hasTok = true
			continue
		}
		switch r {
		case '\'', '"':
			inQuote = r
			hasTok = true
		case '|', '&', ';', '(', ')':
			// Segment separator.
			finishSegment()
		case '<', '>':
			// Redirect operator: flush current token, next token is a redirect target.
			flush()
			afterRedirect = true
		case ' ', '\t', '\n', '\r':
			flush()
		default:
			cur.WriteRune(r)
			hasTok = true
		}
	}
	finishSegment()

	return segments, redirectTargets
}

// looksLikePathToken reports whether a bare command token references a location
// that could resolve OUTSIDE the project root: an absolute path, a home path,
// or an explicit relative escape/reference. Plain in-tree names (e.g. "main.go"
// or "pkg/agent") are deliberately NOT flagged here — they resolve inside the
// working directory and the caller's containment check handles them; flagging
// every bare word would make the tokenizer useless. The point of this function
// is to surface the shapes that can point out of scope.
func looksLikePathToken(tok string) bool {
	switch {
	case tok == "~" || strings.HasPrefix(tok, "~/"):
		return true
	case strings.HasPrefix(tok, "/"):
		return true
	case tok == ".." || strings.HasPrefix(tok, "../"):
		return true
	}
	// A leading "./" or a bare relative name (e.g. "./pkg" or "main.go") resolves
	// inside the current working directory and therefore inside the project root
	// in code mode — it is NOT an escape shape and must not be flagged, or every
	// in-tree operand would prompt. Only "../"-style escapes matter for relative
	// tokens; catch them even when buried mid-token (e.g. "foo/../../etc").
	if strings.Contains(tok, "..") {
		cleaned := filepath.Clean(tok)
		if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}


