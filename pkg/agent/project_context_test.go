package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFile is a small helper for the project-context tests.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestLoadProjectContext_MergesRootToNearest(t *testing.T) {
	root := t.TempDir()
	// Make root a git project root so the upward walk stops here.
	writeFile(t, filepath.Join(root, ".git", "HEAD"), "ref: refs/heads/main\n")
	writeFile(t, filepath.Join(root, "AGENTS.md"), "ROOT RULES")
	sub := filepath.Join(root, "pkg", "svc")
	writeFile(t, filepath.Join(sub, "AGENTS.md"), "SUBDIR RULES")

	out := loadProjectContext(sub, "")
	if !strings.Contains(out, "ROOT RULES") || !strings.Contains(out, "SUBDIR RULES") {
		t.Fatalf("expected both files merged, got:\n%s", out)
	}
	// Nearest (subdir) must appear after root so it takes precedence.
	if strings.Index(out, "ROOT RULES") > strings.Index(out, "SUBDIR RULES") {
		t.Fatalf("expected root before subdir (nearest last):\n%s", out)
	}
	// Provenance headers reference the source paths.
	if !strings.Contains(out, filepath.Join(sub, "AGENTS.md")) {
		t.Errorf("expected provenance header for subdir file:\n%s", out)
	}
}

func TestLoadProjectContext_ClaudeFallback(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".git", "HEAD"), "ref: refs/heads/main\n")
	// Only a CLAUDE.md exists (no AGENTS.md) — it should be used as fallback.
	writeFile(t, filepath.Join(root, "CLAUDE.md"), "CLAUDE FALLBACK")

	out := loadProjectContext(root, "")
	if !strings.Contains(out, "CLAUDE FALLBACK") {
		t.Fatalf("expected CLAUDE.md fallback, got:\n%s", out)
	}
}

func TestLoadProjectContext_AgentsPreferredOverClaude(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".git", "HEAD"), "ref: refs/heads/main\n")
	writeFile(t, filepath.Join(root, "AGENTS.md"), "AGENTS WINS")
	writeFile(t, filepath.Join(root, "CLAUDE.md"), "CLAUDE LOSES")

	out := loadProjectContext(root, "")
	if !strings.Contains(out, "AGENTS WINS") {
		t.Fatalf("expected AGENTS.md, got:\n%s", out)
	}
	if strings.Contains(out, "CLAUDE LOSES") {
		t.Fatalf("CLAUDE.md must be ignored when AGENTS.md exists:\n%s", out)
	}
}

func TestLoadProjectContext_SkipsEmptyFiles(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".git", "HEAD"), "ref: refs/heads/main\n")
	writeFile(t, filepath.Join(root, "AGENTS.md"), "   \n\n  ")

	out := loadProjectContext(root, "")
	if out != "" {
		t.Fatalf("expected empty result for whitespace-only file, got:\n%q", out)
	}
}

func TestLoadProjectContext_GlobalPrepended(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".git", "HEAD"), "ref: refs/heads/main\n")
	writeFile(t, filepath.Join(root, "AGENTS.md"), "PROJECT RULES")
	global := filepath.Join(t.TempDir(), "AGENTS.md")
	writeFile(t, global, "GLOBAL RULES")

	out := loadProjectContext(root, global)
	if !strings.Contains(out, "GLOBAL RULES") || !strings.Contains(out, "PROJECT RULES") {
		t.Fatalf("expected global + project, got:\n%s", out)
	}
	// Global is lowest precedence → appears first.
	if strings.Index(out, "GLOBAL RULES") > strings.Index(out, "PROJECT RULES") {
		t.Fatalf("expected global before project:\n%s", out)
	}
}

func TestLoadProjectContext_StopsAtGitRoot(t *testing.T) {
	outer := t.TempDir()
	// An AGENTS.md above the git root must NOT be included.
	writeFile(t, filepath.Join(outer, "AGENTS.md"), "OUTSIDE REPO")
	repo := filepath.Join(outer, "repo")
	writeFile(t, filepath.Join(repo, ".git", "HEAD"), "ref: refs/heads/main\n")
	writeFile(t, filepath.Join(repo, "AGENTS.md"), "INSIDE REPO")

	out := loadProjectContext(repo, "")
	if !strings.Contains(out, "INSIDE REPO") {
		t.Fatalf("expected repo file, got:\n%s", out)
	}
	if strings.Contains(out, "OUTSIDE REPO") {
		t.Fatalf("must not walk above the git root:\n%s", out)
	}
}

func TestLoadProjectContext_SizeCap(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".git", "HEAD"), "ref: refs/heads/main\n")
	big := strings.Repeat("A", maxProjectContextBytes*2)
	writeFile(t, filepath.Join(root, "AGENTS.md"), big)

	out := loadProjectContext(root, "")
	if len(out) > maxProjectContextBytes+256 { // allow for headers/truncation notice
		t.Fatalf("expected output capped near %d bytes, got %d", maxProjectContextBytes, len(out))
	}
	if !strings.Contains(out, "truncated") {
		t.Errorf("expected a truncation notice when over the size cap")
	}
}

func TestLoadProjectContext_NoneFound(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".git", "HEAD"), "ref: refs/heads/main\n")
	if out := loadProjectContext(root, ""); out != "" {
		t.Fatalf("expected empty result when no AGENTS.md exists, got:\n%s", out)
	}
}

// TestCodeSystemPromptBuilder_ProjectContextInjected verifies the loaded content
// is rendered into the code-mode system prompt under a Project Guidance section.
func TestCodeSystemPromptBuilder_ProjectContextInjected(t *testing.T) {
	base := &SystemPromptBuilder{}
	cb := NewCodeSystemPromptBuilder(base)
	cb.ProjectContext = "USE TABS NOT SPACES"
	prompt := base.Build()
	if !strings.Contains(prompt, "## Project Guidance") {
		t.Fatalf("expected Project Guidance section, got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "USE TABS NOT SPACES") {
		t.Fatalf("expected project content in prompt, got:\n%s", prompt)
	}
}

func TestSystemPromptBuilder_NoProjectContextSection(t *testing.T) {
	b := &SystemPromptBuilder{}
	if strings.Contains(b.Build(), "## Project Guidance") {
		t.Fatal("Project Guidance section must be absent for base builder (chat mode)")
	}
}
