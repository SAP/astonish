package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/skills"
	"github.com/SAP/astonish/pkg/store"
)

func TestSkillLookupFound(t *testing.T) {
	fn := SkillLookup([]skills.Skill{{Name: "test-skill", Description: "A test skill", Content: "# Test\n\nHello world.", RequireBins: []string{"echo"}}}, SkillLookupModeLocal)
	result, err := fn(nil, SkillLookupArgs{Name: "test-skill"})
	if err != nil || result.Error != "" {
		t.Fatalf("lookup failed: result=%#v err=%v", result, err)
	}
	if result.Name != "test-skill" || result.Content != "# Test\n\nHello world." {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestSkillLookupNotFound(t *testing.T) {
	fn := SkillLookup([]skills.Skill{{Name: "existing", Description: "Exists", Content: "content"}}, SkillLookupModeLocal)
	result, err := fn(nil, SkillLookupArgs{Name: "nonexistent"})
	if err != nil || !strings.Contains(result.Error, "existing") {
		t.Fatalf("unexpected result: %#v err=%v", result, err)
	}
}

func TestSkillLookupEmptyName(t *testing.T) {
	result, err := SkillLookup(nil, SkillLookupModeLocal)(nil, SkillLookupArgs{})
	if err != nil || result.Error == "" {
		t.Fatalf("expected validation error: %#v err=%v", result, err)
	}
}

func TestSkillLookupIneligibleReturnsWithMissingReqs(t *testing.T) {
	fn := SkillLookup([]skills.Skill{{Name: "missing-bin", Description: "Missing", Content: "content", RequireBins: []string{"nonexistent_xyz123"}}}, SkillLookupModeLocal)
	result, err := fn(nil, SkillLookupArgs{Name: "missing-bin"})
	if err != nil || result.Error != "" || len(result.MissingRequirements) == 0 || !strings.Contains(result.MissingRequirements[0], "nonexistent_xyz123") {
		t.Fatalf("unexpected result: %#v err=%v", result, err)
	}
}

func TestSkillLookupFilesystemNestedReadAndRecursiveManifest(t *testing.T) {
	root := t.TempDir()
	writeLookupFile(t, root, "SKILL.md", "main")
	writeLookupFile(t, root, "scripts/deploy.sh", "deploy")
	writeLookupFile(t, root, "references/nested/api.md", "api")
	skill := skills.Skill{Name: "disk", Description: "disk", Content: "body", Directory: root}
	fn := SkillLookup([]skills.Skill{skill}, SkillLookupModeLocal)

	manifest, err := fn(nil, SkillLookupArgs{Name: "disk"})
	if err != nil || manifest.Error != "" {
		t.Fatalf("manifest failed: %#v err=%v", manifest, err)
	}
	want := []string{"SKILL.md", "references/nested/api.md", "scripts/deploy.sh"}
	if strings.Join(manifest.Files, ",") != strings.Join(want, ",") {
		t.Fatalf("files=%v want=%v", manifest.Files, want)
	}
	if got := manifest.FilesManifest["references/nested"]; len(got) != 1 || got[0] != "api.md" {
		t.Fatalf("manifest=%#v", manifest.FilesManifest)
	}

	file, err := fn(nil, SkillLookupArgs{Name: "disk", Path: "references/nested", Filename: "api.md"})
	if err != nil || file.Error != "" || file.File != "references/nested/api.md" || file.Content != "api" {
		t.Fatalf("nested read failed: %#v err=%v", file, err)
	}
}

func TestSkillLookupFilesystemRejectsTraversalSymlinksAndOversize(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	writeLookupFile(t, root, "large.txt", strings.Repeat("x", maxSkillFileBytes+1))
	fn := SkillLookup([]skills.Skill{{Name: "disk", Description: "disk", Directory: root}}, SkillLookupModeCode)

	for _, file := range []string{"../secret.txt", "escape", "large.txt"} {
		result, err := fn(nil, SkillLookupArgs{Name: "disk", File: file})
		if err != nil || result.Error == "" || result.Content != "" {
			t.Errorf("file %q should be rejected: %#v err=%v", file, result, err)
		}
	}
	manifest, _ := fn(nil, SkillLookupArgs{Name: "disk"})
	for _, file := range manifest.Files {
		if file == "escape" {
			t.Fatalf("symlink leaked into manifest: %v", manifest.Files)
		}
	}
}

func TestSkillLookupModesAndPlatformPrecedence(t *testing.T) {
	platform := &lookupSkillStore{skills: []store.Skill{{Name: "shared", Description: "platform", Content: skillDocument("shared", "platform"), ValidationStatus: skills.ValidationStatusClean}}}
	org := &lookupSkillStore{skills: []store.Skill{{Name: "shared", Description: "org", Content: skillDocument("shared", "org"), ValidationStatus: skills.ValidationStatusClean}}}
	team := &lookupSkillStore{skills: []store.Skill{{Name: "shared", Description: "team", Content: skillDocument("shared", "team"), ValidationStatus: skills.ValidationStatusClean}}}
	base := store.WithSkillStores(context.Background(), &store.SkillStores{Platform: platform, Org: org, Team: team})
	ctx := &mockToolCtx{Context: base}
	filesystem := []skills.Skill{{Name: "shared", Description: "filesystem", Content: "filesystem"}}

	for _, mode := range []SkillLookupMode{SkillLookupModeLocal, SkillLookupModeCode} {
		result, err := SkillLookup(filesystem, mode)(ctx, SkillLookupArgs{Name: "shared"})
		if err != nil || result.Content != "filesystem" {
			t.Fatalf("mode %s touched stores: %#v err=%v", mode, result, err)
		}
	}
	if platform.getCalls != 0 || org.getCalls != 0 || team.getCalls != 0 {
		t.Fatalf("local modes touched DB stores: platform=%d org=%d team=%d", platform.getCalls, org.getCalls, team.getCalls)
	}

	result, err := SkillLookup(filesystem, SkillLookupModePlatform)(ctx, SkillLookupArgs{Name: "shared"})
	if err != nil || result.Content != "team" {
		t.Fatalf("team did not win: %#v err=%v", result, err)
	}
	if team.getCalls != 1 || org.getCalls != 0 || platform.getCalls != 0 {
		t.Fatalf("unexpected precedence calls: platform=%d org=%d team=%d", platform.getCalls, org.getCalls, team.getCalls)
	}
}

func TestNewSkillLookupTool(t *testing.T) {
	toolInst, err := NewSkillLookupTool(nil, SkillLookupModeLocal)
	if err != nil || toolInst.Name() != "skill_lookup" {
		t.Fatalf("tool=%v err=%v", toolInst, err)
	}
}

// TestSkillLookupCodeMode_GenerativeUINotResolvable verifies that in
// SkillLookupModeCode the generative-ui builtin cannot be resolved by name.
// It must return an error result, not the skill content, so the coding agent
// cannot load Studio-only instructions and generate astonish-app fences.
func TestSkillLookupCodeMode_GenerativeUINotResolvable(t *testing.T) {
	fn := SkillLookup(nil, SkillLookupModeCode)
	result, err := fn(nil, SkillLookupArgs{Name: "generative-ui"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Error == "" {
		t.Fatalf("expected error result for generative-ui in code mode, got content: %q", result.Content)
	}
	if result.Content != "" {
		t.Fatalf("expected empty content for generative-ui in code mode, got: %q", result.Content)
	}
}

// TestSkillLookupPlatformMode_GenerativeUIResolvable verifies that in
// SkillLookupModePlatform the generative-ui builtin IS resolvable — it must
// remain fully available for Studio/chat mode.
func TestSkillLookupPlatformMode_GenerativeUIResolvable(t *testing.T) {
	fn := SkillLookup(nil, SkillLookupModePlatform)
	result, err := fn(nil, SkillLookupArgs{Name: "generative-ui"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Error != "" {
		t.Fatalf("expected generative-ui to resolve in platform mode, got error: %q", result.Error)
	}
	if result.Content == "" {
		t.Fatal("expected non-empty content for generative-ui in platform mode")
	}
}

func writeLookupFile(t *testing.T, root, relative, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func skillDocument(name, body string) string {
	return "---\nname: " + name + "\ndescription: test\n---\n\n" + body
}

type lookupSkillStore struct {
	skills   []store.Skill
	getCalls int
}

func (m *lookupSkillStore) LoadAll(context.Context) ([]store.Skill, error) { return m.skills, nil }
func (m *lookupSkillStore) Get(_ context.Context, name string) (*store.Skill, error) {
	m.getCalls++
	for i := range m.skills {
		if m.skills[i].Name == name {
			return &m.skills[i], nil
		}
	}
	return nil, nil
}
func (m *lookupSkillStore) Save(context.Context, *store.Skill) error { return nil }
func (m *lookupSkillStore) Delete(context.Context, string) error     { return nil }
func (m *lookupSkillStore) List(context.Context) ([]store.Skill, error) {
	return m.skills, nil
}
func (m *lookupSkillStore) UpdateValidationStatus(context.Context, string, string, string) error {
	return nil
}
func (m *lookupSkillStore) ListFiles(context.Context, string) ([]store.SkillFile, error) {
	return nil, nil
}
func (m *lookupSkillStore) GetFile(context.Context, string, string, string) (*store.SkillFile, error) {
	return nil, nil
}
func (m *lookupSkillStore) SaveFile(context.Context, string, *store.SkillFile) error { return nil }
func (m *lookupSkillStore) DeleteFile(context.Context, string, string, string) error { return nil }
