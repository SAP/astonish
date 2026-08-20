package tools

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestCreateSkillCreatesFreshTemplate(t *testing.T) {
	root := t.TempDir()
	result, err := CreateSkill(root)(nil, CreateSkillArgs{Name: "deploy-k8s"})
	if err != nil || result.Error != "" {
		t.Fatalf("create failed: %#v err=%v", result, err)
	}
	wantFile := filepath.Join(root, "deploy-k8s", "SKILL.md")
	if result.File != wantFile || result.Directory != filepath.Dir(wantFile) {
		t.Fatalf("unexpected paths: %#v", result)
	}
	data, err := os.ReadFile(wantFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != result.Content || !strings.Contains(string(data), "name: deploy-k8s") {
		t.Fatalf("unexpected template: %q", data)
	}
	info, err := os.Stat(wantFile)
	if err != nil || info.Mode().Perm() != 0644 {
		t.Fatalf("mode=%v err=%v", info.Mode(), err)
	}
}

func TestCreateSkillAcceptsApprovedNamesAfterTrimming(t *testing.T) {
	for _, input := range []string{"Upper", "a_b", "-start", "end-", "two--hyphens", "_", "A0-z_9", "  Trim_Me--  "} {
		t.Run(input, func(t *testing.T) {
			root := t.TempDir()
			want := strings.TrimSpace(input)
			result, err := CreateSkill(root)(nil, CreateSkillArgs{Name: input})
			if err != nil || result.Error != "" {
				t.Fatalf("name %q was rejected: %#v err=%v", input, result, err)
			}
			if result.Name != want || result.Directory != filepath.Join(root, want) {
				t.Fatalf("name was not trimmed/preserved: %#v", result)
			}
			if !strings.Contains(result.Content, "name: "+want) {
				t.Fatalf("template does not preserve name %q: %q", want, result.Content)
			}
		})
	}
}

func TestCreateSkillRejectsInvalidName(t *testing.T) {
	root := t.TempDir()
	invalid := []string{
		"", "   ", "with space", "with\tspace", "line\nbreak",
		"../escape", ".", "..", "a..b", "a/b", `a\b`,
		"é", "技能", "name!", "name.", "name@host", "a:b", "a,b",
	}
	for _, name := range invalid {
		result, err := CreateSkill(root)(nil, CreateSkillArgs{Name: name})
		if err != nil || result.Error == "" {
			t.Errorf("name %q was not rejected: %#v err=%v", name, result, err)
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("invalid names created entries: %v err=%v", entries, err)
	}
}

func TestCreateSkillNeverOverwrites(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "existing")
	if err := os.Mkdir(dir, 0755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(file, []byte("keep me"), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := CreateSkill(root)(nil, CreateSkillArgs{Name: "existing"})
	if err != nil || result.Error == "" {
		t.Fatalf("expected conflict: %#v err=%v", result, err)
	}
	data, err := os.ReadFile(file)
	if err != nil || string(data) != "keep me" {
		t.Fatalf("existing file changed: %q err=%v", data, err)
	}
}

func TestCreateSkillRejectsCaseInsensitiveDuplicate(t *testing.T) {
	root := t.TempDir()
	first, err := CreateSkill(root)(nil, CreateSkillArgs{Name: "Foo"})
	if err != nil || first.Error != "" {
		t.Fatalf("first create failed: %#v err=%v", first, err)
	}

	duplicate, err := CreateSkill(root)(nil, CreateSkillArgs{Name: "foo"})
	if err != nil || duplicate.Error == "" {
		t.Fatalf("expected case-insensitive conflict: %#v err=%v", duplicate, err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "Foo" {
		t.Fatalf("unexpected root entries after conflict: %v", entries)
	}
	data, err := os.ReadFile(filepath.Join(root, "Foo", "SKILL.md"))
	if err != nil || !strings.Contains(string(data), "name: Foo") {
		t.Fatalf("original skill changed: %q err=%v", data, err)
	}
}

func TestCreateSkillConcurrentCallsCreateExactlyOnce(t *testing.T) {
	root := t.TempDir()
	const callers = 16
	results := make(chan CreateSkillResult, callers)
	errs := make(chan error, callers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			result, err := CreateSkill(root)(nil, CreateSkillArgs{Name: "Concurrent_Skill"})
			results <- result
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("unexpected Go error: %v", err)
		}
	}
	successes := 0
	for result := range results {
		if result.Error == "" {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("successful creations=%d, want 1", successes)
	}
	data, err := os.ReadFile(filepath.Join(root, "Concurrent_Skill", "SKILL.md"))
	if err != nil || !strings.Contains(string(data), "name: Concurrent_Skill") {
		t.Fatalf("invalid winning skill file: %q err=%v", data, err)
	}
}

func TestCreateSkillCleansPartialDirectory(t *testing.T) {
	parent := t.TempDir()
	rootFile := filepath.Join(parent, "not-a-directory")
	if err := os.WriteFile(rootFile, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	result, err := CreateSkill(rootFile)(nil, CreateSkillArgs{Name: "new-skill"})
	if err != nil || result.Error == "" {
		t.Fatalf("expected root error: %#v err=%v", result, err)
	}
	if _, err := os.Stat(filepath.Join(rootFile, "new-skill")); err == nil {
		t.Fatal("partial directory remains")
	}
}

func TestNewCreateSkillTool(t *testing.T) {
	toolInst, err := NewCreateSkillTool(t.TempDir())
	if err != nil || toolInst.Name() != "create_skill" {
		t.Fatalf("tool=%v err=%v", toolInst, err)
	}
}

func TestCreateSkillWritesProvidedContent(t *testing.T) {
	root := t.TempDir()
	custom := `---
name: my-custom
description: "A fully custom skill"
require_bins: []
---

# My Custom Skill

Custom body.
`
	result, err := CreateSkill(root)(nil, CreateSkillArgs{Name: "my-custom", Content: custom})
	if err != nil || result.Error != "" {
		t.Fatalf("create failed: %#v err=%v", result, err)
	}
	data, err := os.ReadFile(result.File)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != custom {
		t.Fatalf("file content mismatch:\ngot:  %q\nwant: %q", data, custom)
	}
	if result.Content != custom {
		t.Fatalf("result.Content mismatch:\ngot:  %q\nwant: %q", result.Content, custom)
	}
}

func TestCreateSkillUsesTemplateWhenContentEmpty(t *testing.T) {
	root := t.TempDir()
	result, err := CreateSkill(root)(nil, CreateSkillArgs{Name: "scaffold-only", Content: ""})
	if err != nil || result.Error != "" {
		t.Fatalf("create failed: %#v err=%v", result, err)
	}
	data, err := os.ReadFile(result.File)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "name: scaffold-only") {
		t.Fatalf("expected template content, got: %q", data)
	}
}

func TestCreateSkillUsesTemplateWhenContentWhitespace(t *testing.T) {
	root := t.TempDir()
	result, err := CreateSkill(root)(nil, CreateSkillArgs{Name: "ws-only", Content: "   \n\t  "})
	if err != nil || result.Error != "" {
		t.Fatalf("create failed: %#v err=%v", result, err)
	}
	data, err := os.ReadFile(result.File)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "name: ws-only") {
		t.Fatalf("expected template content, got: %q", data)
	}
}

func TestEnsureSkillFrontmatterPreservesValidFrontmatter(t *testing.T) {
	content := "---\nname: my-skill\ndescription: \"good\"\nrequire_bins: []\n---\n\n# Body\n"
	got := ensureSkillFrontmatter(content, "my-skill")
	if got != content {
		t.Fatalf("expected content unchanged, got:\n%s", got)
	}
}

func TestEnsureSkillFrontmatterInjectsMissingFrontmatter(t *testing.T) {
	content := "# My Skill\n\nSome body text.\n"
	got := ensureSkillFrontmatter(content, "my-skill")
	if !strings.HasPrefix(got, "---\n") {
		t.Fatalf("expected injected frontmatter, got:\n%s", got)
	}
	if !strings.Contains(got, "name: my-skill") {
		t.Fatalf("expected name in injected frontmatter, got:\n%s", got)
	}
	if !strings.Contains(got, "description:") {
		t.Fatalf("expected description in injected frontmatter, got:\n%s", got)
	}
	if !strings.Contains(got, "# My Skill") {
		t.Fatalf("expected original body preserved, got:\n%s", got)
	}
}

func TestCreateSkillAutoInjectsFrontmatterWhenMissing(t *testing.T) {
	root := t.TempDir()
	// Content without frontmatter — simulates what the agent wrote for openstack
	body := "# OpenStack API\n\nInteract with OpenStack services via REST APIs.\n"
	result, err := CreateSkill(root)(nil, CreateSkillArgs{Name: "openstack", Content: body})
	if err != nil || result.Error != "" {
		t.Fatalf("create failed: %#v err=%v", result, err)
	}
	data, err := os.ReadFile(result.File)
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	if !strings.HasPrefix(s, "---\n") {
		t.Fatalf("expected SKILL.md to start with frontmatter, got:\n%s", s)
	}
	if !strings.Contains(s, "name: openstack") {
		t.Fatalf("expected name in frontmatter, got:\n%s", s)
	}
	if !strings.Contains(s, "# OpenStack API") {
		t.Fatalf("expected original body preserved, got:\n%s", s)
	}
}
