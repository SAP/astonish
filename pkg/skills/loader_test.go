package skills

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseSkillFile(t *testing.T) {
	content := []byte(`---
name: test-skill
description: "A test skill for testing"
require_bins: ["echo"]
---

# Test Skill

## Commands
- echo hello
`)

	skill, err := ParseSkillFile("test/SKILL.md", content)
	if err != nil {
		t.Fatalf("ParseSkillFile failed: %v", err)
	}

	if skill.Name != "test-skill" {
		t.Errorf("Name = %q, want %q", skill.Name, "test-skill")
	}
	if skill.Description != "A test skill for testing" {
		t.Errorf("Description = %q, want %q", skill.Description, "A test skill for testing")
	}
	if len(skill.RequireBins) != 1 || skill.RequireBins[0] != "echo" {
		t.Errorf("RequireBins = %v, want [echo]", skill.RequireBins)
	}
	if skill.Content == "" {
		t.Error("Content is empty")
	}
	if skill.FilePath != "test/SKILL.md" {
		t.Errorf("FilePath = %q, want %q", skill.FilePath, "test/SKILL.md")
	}
}

func TestParseSkillFileNoFrontmatter(t *testing.T) {
	content := []byte("# Just a markdown file\n\nNo frontmatter here.\n")
	_, err := ParseSkillFile("test/SKILL.md", content)
	if err == nil {
		t.Fatal("Expected error for missing frontmatter, got nil")
	}
}

func TestParseSkillFileNoName(t *testing.T) {
	content := []byte("---\ndescription: \"test\"\n---\n\n# Content\n")
	_, err := ParseSkillFile("test/SKILL.md", content)
	if err == nil {
		t.Fatal("Expected error for missing name, got nil")
	}
}

func TestParseSkillFileNoDescription(t *testing.T) {
	content := []byte("---\nname: test\n---\n\n# Content\n")
	_, err := ParseSkillFile("test/SKILL.md", content)
	if err == nil {
		t.Fatal("Expected error for missing description, got nil")
	}
}

func TestParseSkillFileTooLarge(t *testing.T) {
	content := make([]byte, 257*1024)
	_, err := ParseSkillFile("test/SKILL.md", content)
	if err == nil {
		t.Fatal("Expected error for oversized file, got nil")
	}
}

func TestIsEligibleWithEcho(t *testing.T) {
	s := Skill{
		Name:        "test",
		Description: "test",
		RequireBins: []string{"echo"},
	}
	// echo should always exist on unix
	if !s.IsEligible() {
		t.Error("Expected echo to be eligible")
	}
}

func TestIsEligibleMissingBin(t *testing.T) {
	s := Skill{
		Name:        "test",
		Description: "test",
		RequireBins: []string{"nonexistent_binary_xyz123"},
	}
	if s.IsEligible() {
		t.Error("Expected missing binary to be ineligible")
	}
}

func TestIsEligibleOSRestriction(t *testing.T) {
	s := Skill{
		Name:        "test",
		Description: "test",
		OS:          []string{"nonexistent_os"},
	}
	if s.IsEligible() {
		t.Error("Expected wrong OS to be ineligible")
	}
}

func TestIsEligibleIgnoresEnvVars(t *testing.T) {
	s := Skill{
		Name:        "test",
		Description: "test",
		RequireEnv:  []string{"NONEXISTENT_ENV_VAR_XYZ123"},
	}
	// RequireEnv is no longer checked — env vars should be resolved
	// from the credential store at runtime, not treated as prerequisites.
	if !s.IsEligible() {
		t.Error("Expected skill with RequireEnv to be eligible (env vars are resolved at runtime)")
	}
}

func TestMissingRequirements(t *testing.T) {
	s := Skill{
		Name:        "test",
		Description: "test",
		RequireBins: []string{"nonexistent_binary_xyz123"},
		RequireEnv:  []string{"NONEXISTENT_ENV_VAR_XYZ123"},
	}
	missing := s.MissingRequirements()
	// Only the missing binary should be reported — env vars are no longer
	// checked (they are resolved from the credential store at runtime).
	if len(missing) != 1 {
		t.Errorf("Expected 1 missing (binary only), got %d: %v", len(missing), missing)
	}
}

func TestFilterEligible(t *testing.T) {
	allSkills := []Skill{
		{Name: "good", Description: "good", RequireBins: []string{"echo"}},
		{Name: "bad", Description: "bad", RequireBins: []string{"nonexistent_xyz123"}},
	}
	eligible := FilterEligible(allSkills)
	if len(eligible) != 1 {
		t.Errorf("Expected 1 eligible, got %d", len(eligible))
	}
	if eligible[0].Name != "good" {
		t.Errorf("Expected good, got %s", eligible[0].Name)
	}
}

func TestLoadSkillsFromDir(t *testing.T) {
	tmpDir := t.TempDir()

	writeTestSkill(t, tmpDir, "my-tool", "my-tool", "My custom tool", "# My Tool")

	byName := make(map[string]*Skill)
	loadSkillsFromDir(tmpDir, "user", byName)

	if len(byName) != 1 {
		t.Fatalf("Expected 1 skill, got %d", len(byName))
	}
	skill := byName["my-tool"]
	if skill == nil {
		t.Fatal("Skill 'my-tool' not found")
	}
	if skill.Source != "user" {
		t.Errorf("Source = %q, want %q", skill.Source, "user")
	}
	wantDir, err := filepath.Abs(filepath.Join(tmpDir, "my-tool"))
	if err != nil {
		t.Fatal(err)
	}
	if skill.Directory != wantDir {
		t.Errorf("Directory = %q, want %q", skill.Directory, wantDir)
	}
	if skill.FilePath != filepath.Join(tmpDir, "my-tool", "SKILL.md") {
		t.Errorf("FilePath = %q", skill.FilePath)
	}
	if skill.Content != "# My Tool" {
		t.Errorf("Content = %q", skill.Content)
	}
}

func TestLoadSkillsSourcesOverridesAllowlistAndSort(t *testing.T) {
	userDir := t.TempDir()
	extraOne := t.TempDir()
	extraTwo := t.TempDir()

	writeTestSkill(t, userDir, "zulu", "Zulu", "user zulu", "user zulu")
	writeTestSkill(t, userDir, "shared", "Shared", "user shared", "user shared")
	writeTestSkill(t, extraOne, "alpha", "alpha", "extra alpha", "extra alpha")
	writeTestSkill(t, extraOne, "shared", "sHaReD", "first override", "first override")
	writeTestSkill(t, extraTwo, "shared", "SHARED", "last override", "last override")
	writeTestSkill(t, extraTwo, "bravo", "Bravo", "extra bravo", "extra bravo")

	sk, err := LoadSkills(userDir, []string{extraOne, extraTwo}, []string{"ALPHA", "shared", "BRAVO"})
	if err != nil {
		t.Fatalf("LoadSkills failed: %v", err)
	}
	if len(sk) != 3 {
		t.Fatalf("got %d skills, want 3: %#v", len(sk), sk)
	}
	wantNames := []string{"alpha", "Bravo", "SHARED"}
	for i, want := range wantNames {
		if sk[i].Name != want {
			t.Errorf("skill[%d].Name = %q, want %q", i, sk[i].Name, want)
		}
	}
	if sk[2].Description != "last override" || sk[2].Content != "last override" {
		t.Errorf("override did not retain winning skill: %#v", sk[2])
	}
	if sk[2].Source != "extra" {
		t.Errorf("winning Source = %q, want extra", sk[2].Source)
	}
	wantDir, _ := filepath.Abs(filepath.Join(extraTwo, "shared"))
	if sk[2].Directory != wantDir || sk[2].FilePath != filepath.Join(extraTwo, "shared", "SKILL.md") {
		t.Errorf("winning filesystem metadata not retained: %#v", sk[2])
	}
}

func TestLoadSkillsEmptyAllowlistAndInvalidInputs(t *testing.T) {
	userDir := t.TempDir()
	writeTestSkill(t, userDir, "valid", "Valid", "valid skill", "valid body")

	if err := os.WriteFile(filepath.Join(userDir, "SKILL.md"), []byte("not a child skill"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(userDir, "missing-file"), 0755); err != nil {
		t.Fatal(err)
	}
	invalidDir := filepath.Join(userDir, "invalid")
	if err := os.Mkdir(invalidDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(invalidDir, "SKILL.md"), []byte("invalid"), 0644); err != nil {
		t.Fatal(err)
	}

	sk, err := LoadSkills(userDir, []string{"", filepath.Join(userDir, "does-not-exist")}, nil)
	if err != nil {
		t.Fatalf("LoadSkills failed: %v", err)
	}
	if len(sk) != 1 || sk[0].Name != "Valid" {
		t.Fatalf("got %#v, want only Valid", sk)
	}
	if sk[0].Source != "user" {
		t.Errorf("Source = %q, want user", sk[0].Source)
	}
}

func TestLoadSkillsCaseInsensitiveDedupeWithinRoot(t *testing.T) {
	root := t.TempDir()
	writeTestSkill(t, root, "a-first", "Duplicate", "first", "first")
	writeTestSkill(t, root, "z-last", "duplicate", "last", "last")

	sk, err := LoadSkills(root, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(sk) != 1 || sk[0].Description != "last" {
		t.Fatalf("got %#v, want deterministic last directory override", sk)
	}
}

func writeTestSkill(t *testing.T, root, dirname, name, description, body string) {
	t.Helper()
	dir := filepath.Join(root, dirname)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	content := []byte("---\nname: " + name + "\ndescription: \"" + description + "\"\nmetadata:\n  custom: retained\n---\n\n" + body + "\n")
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), content, 0644); err != nil {
		t.Fatal(err)
	}
}
