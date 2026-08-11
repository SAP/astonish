package agent

import (
	"os"
	"path/filepath"
	"strings"
)

// Project-context loading implements the AGENTS.md convention used by coding
// agents (see https://agents.md). Agents load AGENTS.md into context near the
// system prompt to pick up project-specific conventions, build/test commands,
// and gotchas.
//
// Discovery walks upward from the working directory to the project root,
// collecting the nearest instruction file at each level (AGENTS.md, falling
// back to CLAUDE.md). Files are concatenated root-first so the nearest file
// appears last and therefore takes precedence. A global file at
// ~/.config/astonish/AGENTS.md is prepended (lowest precedence).

const (
	// projectContextFileName is the primary per-directory instruction file.
	projectContextFileName = "AGENTS.md"
	// projectContextFallbackName is the Claude Code fallback, used only when a
	// directory has no AGENTS.md.
	projectContextFallbackName = "CLAUDE.md"
	// maxProjectContextBytes caps the total concatenated size to keep the
	// system prompt bounded (mirrors Codex's ~32 KiB default).
	maxProjectContextBytes = 32 * 1024
	// maxProjectContextDepth bounds the upward walk so a deep path cannot cause
	// excessive stat calls; the project root is normally found well within this.
	maxProjectContextDepth = 64
)

// LoadProjectContext discovers and concatenates AGENTS.md (fallback CLAUDE.md)
// files for the given working directory, following the agents.md convention:
// walk upward to the project root, nearest file wins (appears last), and
// prepend a global ~/.config/astonish/AGENTS.md when present.
//
// It returns the merged Markdown (with per-file provenance headers) or an empty
// string when nothing is found. It never returns an error: missing/unreadable
// files are skipped so a coding session is never blocked by project docs.
func LoadProjectContext(workingDir string) string {
	return loadProjectContext(workingDir, globalProjectContextPath())
}

// loadProjectContext is the testable core: globalPath may be empty to skip the
// global file (used in tests for isolation).
func loadProjectContext(workingDir, globalPath string) string {
	dir := strings.TrimSpace(workingDir)
	if dir == "" {
		if wd, err := os.Getwd(); err == nil {
			dir = wd
		}
	}
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}

	// Collect directories from the working dir up to the project root.
	dirs := ancestorDirs(dir)

	// Build (path -> content) from root down to the working dir so the nearest
	// file is emitted last (highest precedence).
	type entry struct {
		path    string
		content string
	}
	var entries []entry

	// Global file first (lowest precedence).
	if globalPath != "" {
		if content, ok := readProjectContextFile(globalPath); ok {
			entries = append(entries, entry{path: globalPath, content: content})
		}
	}

	// Walk from the top-most ancestor down to the working directory.
	for i := len(dirs) - 1; i >= 0; i-- {
		if path, content, ok := readNearestInstructionFile(dirs[i]); ok {
			entries = append(entries, entry{path: path, content: content})
		}
	}

	if len(entries) == 0 {
		return ""
	}

	var sb strings.Builder
	total := 0
	for _, e := range entries {
		block := "<!-- " + e.path + " -->\n" + e.content
		if total+len(block) > maxProjectContextBytes {
			remaining := maxProjectContextBytes - total
			if remaining > 0 {
				sb.WriteString(block[:remaining])
			}
			sb.WriteString("\n\n<!-- project guidance truncated: size limit reached -->\n")
			break
		}
		if sb.Len() > 0 {
			sb.WriteString("\n\n")
		}
		sb.WriteString(block)
		total += len(block) + 2
	}
	return strings.TrimSpace(sb.String())
}

// ancestorDirs returns dir and each parent up to (and including) the project
// root. The project root is the first ancestor containing a .git directory; if
// none is found, the walk stops at the filesystem root or the depth cap.
func ancestorDirs(dir string) []string {
	var out []string
	seen := map[string]bool{}
	cur := dir
	for i := 0; i < maxProjectContextDepth; i++ {
		if cur == "" || seen[cur] {
			break
		}
		seen[cur] = true
		out = append(out, cur)

		// Stop once we've included the git project root.
		if isGitRoot(cur) {
			break
		}
		parent := filepath.Dir(cur)
		if parent == cur { // filesystem root
			break
		}
		cur = parent
	}
	return out
}

func isGitRoot(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil && (info.IsDir() || info.Mode().IsRegular())
}

// readNearestInstructionFile returns the AGENTS.md in dir, or CLAUDE.md as a
// fallback when AGENTS.md is absent. Empty files are skipped.
func readNearestInstructionFile(dir string) (path, content string, ok bool) {
	primary := filepath.Join(dir, projectContextFileName)
	if c, found := readProjectContextFile(primary); found {
		return primary, c, true
	}
	fallback := filepath.Join(dir, projectContextFallbackName)
	if c, found := readProjectContextFile(fallback); found {
		return fallback, c, true
	}
	return "", "", false
}

// readProjectContextFile reads a file and reports whether it exists and has
// non-whitespace content.
func readProjectContextFile(path string) (string, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	content := strings.TrimRight(string(data), "\n")
	if strings.TrimSpace(content) == "" {
		return "", false
	}
	return content, true
}

// globalProjectContextPath returns ~/.config/astonish/AGENTS.md when a home
// directory is resolvable, honoring XDG_CONFIG_HOME.
func globalProjectContextPath() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "astonish", projectContextFileName)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "astonish", projectContextFileName)
}
