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

// ExtractCommandPaths performs a best-effort extraction of the filesystem path
// tokens embedded in a free-form shell command string. It is intentionally
// CONSERVATIVE (default-deny biased): it recognizes tokens that reference a
// location by absolute path (/...), home (~ or ~/...), or an explicit relative
// escape/reference (../...). These are exactly the shapes that can point
// OUTSIDE the project root, which is what the folder-access gate must catch.
//
// Shell syntax is not fully parseable in the general case (command
// substitution, variable expansion, eval, here-docs, nested shells like
// `sh -c "..."`, etc. can all hide paths), so callers MUST treat a positive
// extraction as "these tokens need a scope check" and MUST NOT treat an empty
// result as proof the command is in-scope. The caller's policy decides what to
// do when the command is opaque.
//
// The tokenizer is QUOTE-AWARE: a single- or double-quoted span is part of one
// atomic token, so word/operator boundaries INSIDE quotes do not split it. This
// is the key to not mis-flagging quoted LITERAL DATA — e.g. a commit message
// `git commit -m "fixes A / B"` yields the whole message as one token, which is
// not path-shaped (it does not start with /, ~, or ../), instead of a spurious
// bare "/" operand. A genuinely path-shaped quoted argument such as
// `cat "/etc/passwd"` still surfaces, because the token content ("/etc/passwd")
// begins with an absolute-path shape. Just because a command *contains* a "/"
// (or ~ or ..) inside quoted prose does NOT mean it accesses that location.
//
// The tokenizer:
//   - splits on shell word boundaries and the common operators | & ; < > ( )
//     but ONLY outside quotes,
//   - treats '...' and "..." spans as literal and consumes the quote marks,
//     joining adjacent quoted/unquoted runs into a single token (shell word-join),
//   - drops flag tokens (those starting with '-'),
//   - drops "key=value" env-assignment prefixes but inspects the value,
//   - returns the raw (un-normalized) tokens; the caller normalizes + tests
//     containment so this stays a pure string function.
func ExtractCommandPaths(command string) []string {
	if strings.TrimSpace(command) == "" {
		return nil
	}

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

	for _, field := range splitCommand(command) {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		// Handle key=value env assignments and --opt=value flags: inspect the
		// value, which may itself be a path (e.g. OUT=/etc/x, --file=../y).
		// Checked BEFORE the flag-drop below so "--file=../y" still surfaces
		// its value. Skip this for tokens containing whitespace: those can only
		// come from a quoted literal (env assignments and --opt=value are always
		// single unquoted words), and a literal message may legitimately contain
		// '=' — we must not re-split it into a fake "value".
		if eq := strings.IndexByte(field, '='); eq >= 0 && eq < len(field)-1 && !strings.ContainsAny(field, " \t") {
			add(field[eq+1:])
			continue
		}
		// Bare flags (e.g. -la, --color) with no attached value are never paths.
		if strings.HasPrefix(field, "-") {
			continue
		}
		add(field)
	}
	return out
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

// splitCommand splits a command line into candidate word tokens, treating shell
// operators as separators. It is QUOTE-AWARE: characters inside single- or
// double-quoted spans are literal and never treated as word/operator
// boundaries, and the quote marks themselves are consumed. Adjacent
// quoted/unquoted runs join into a single token (e.g. cat"/etc"/passwd →
// "cat/etc/passwd"), mirroring how a real shell forms a word. This is a
// heuristic (not a full shell lexer — it does not handle backslash escapes,
// command substitution, or here-docs) but it is sufficient to isolate
// path-shaped operands from pipelines/redirections WITHOUT shredding quoted
// literal data (messages, prose, patterns) into spurious path tokens.
func splitCommand(command string) []string {
	var (
		tokens  []string
		cur     strings.Builder
		hasTok  bool // whether cur holds an in-progress token
		inQuote rune // 0, '\'' or '"'
	)
	flush := func() {
		if hasTok {
			tokens = append(tokens, cur.String())
			cur.Reset()
			hasTok = false
		}
	}
	for _, r := range command {
		if inQuote != 0 {
			if r == inQuote {
				inQuote = 0 // closing quote; token continues (may join more)
				continue
			}
			cur.WriteRune(r)
			hasTok = true
			continue
		}
		switch r {
		case '\'', '"':
			// Opening quote: begin a literal span, and mark that a token exists
			// even if the quoted content is empty ("" is still a word).
			inQuote = r
			hasTok = true
		case ' ', '\t', '\n', '\r', '|', '&', ';', '<', '>', '(', ')':
			flush()
		default:
			cur.WriteRune(r)
			hasTok = true
		}
	}
	flush()
	return tokens
}
