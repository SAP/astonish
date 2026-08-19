package channels

import (
	"context"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/skills"
	"github.com/SAP/astonish/pkg/store"
)

// mockSkillStore implements store.SkillStore for testing.
type mockSkillStore struct {
	skills []store.Skill
}

func (m *mockSkillStore) LoadAll(_ context.Context) ([]store.Skill, error) {
	return m.skills, nil
}
func (m *mockSkillStore) Get(_ context.Context, name string) (*store.Skill, error) {
	for i, s := range m.skills {
		if s.Name == name {
			return &m.skills[i], nil
		}
	}
	return nil, nil
}
func (m *mockSkillStore) Save(_ context.Context, _ *store.Skill) error  { return nil }
func (m *mockSkillStore) Delete(_ context.Context, _ string) error      { return nil }
func (m *mockSkillStore) List(_ context.Context) ([]store.Skill, error) { return m.skills, nil }
func (m *mockSkillStore) UpdateValidationStatus(_ context.Context, _, _, _ string) error {
	return nil
}
func (m *mockSkillStore) ListFiles(_ context.Context, _ string) ([]store.SkillFile, error) {
	return nil, nil
}
func (m *mockSkillStore) GetFile(_ context.Context, _, _, _ string) (*store.SkillFile, error) {
	return nil, nil
}
func (m *mockSkillStore) SaveFile(_ context.Context, _ string, _ *store.SkillFile) error { return nil }
func (m *mockSkillStore) DeleteFile(_ context.Context, _, _, _ string) error             { return nil }

// TestBuildChannelSkillIndex_MergesAllTiers verifies that the skill index
// includes skills from platform, org, and team stores.
func TestBuildChannelSkillIndex_MergesAllTiers(t *testing.T) {
	ss := &store.SkillStores{
		Platform: &mockSkillStore{skills: []store.Skill{
			{Name: "deploy-k8s", Description: "Deploy to Kubernetes"},
		}},
		Org: &mockSkillStore{skills: []store.Skill{
			{Name: "org-review", Description: "Code review process"},
		}},
		Team: &mockSkillStore{skills: []store.Skill{
			{Name: "team-debug", Description: "Debug production issues"},
		}},
	}

	result := buildChannelSkillIndex(context.Background(), nil, ss)

	if result == "" {
		t.Fatal("expected non-empty skill index")
	}
	if !strings.Contains(result, "deploy-k8s") {
		t.Error("platform skill 'deploy-k8s' missing from index")
	}
	if !strings.Contains(result, "org-review") {
		t.Error("org skill 'org-review' missing from index")
	}
	if !strings.Contains(result, "team-debug") {
		t.Error("team skill 'team-debug' missing from index")
	}
}

// TestBuildChannelSkillIndex_DeduplicatesTeamWins verifies that when platform
// and team have a skill with the same name, only the team version appears.
func TestBuildChannelSkillIndex_DeduplicatesTeamWins(t *testing.T) {
	ss := &store.SkillStores{
		Platform: &mockSkillStore{skills: []store.Skill{
			{Name: "deploy", Description: "Platform deploy (should be overridden)"},
		}},
		Org: &mockSkillStore{skills: []store.Skill{
			{Name: "deploy", Description: "Org deploy (should be overridden)"},
		}},
		Team: &mockSkillStore{skills: []store.Skill{
			{Name: "deploy", Description: "Team-specific deploy process"},
		}},
	}

	result := buildChannelSkillIndex(context.Background(), nil, ss)

	if result == "" {
		t.Fatal("expected non-empty skill index")
	}

	// Team description should win
	if !strings.Contains(result, "Team-specific deploy process") {
		t.Error("team skill description should override platform/org")
	}
	// Platform/org descriptions should NOT appear
	if strings.Contains(result, "Platform deploy") {
		t.Error("platform skill should be overridden by team")
	}
	if strings.Contains(result, "Org deploy") {
		t.Error("org skill should be overridden by team")
	}

	// Only one "deploy" entry
	count := strings.Count(result, "**deploy**")
	if count != 1 {
		t.Errorf("expected exactly 1 'deploy' entry, got %d", count)
	}
}

// TestBuildChannelSkillIndex_EmptyStores verifies that empty stores return "".
func TestBuildChannelSkillIndex_EmptyStores(t *testing.T) {
	ss := &store.SkillStores{
		Platform: &mockSkillStore{skills: nil},
		Org:      &mockSkillStore{skills: nil},
		Team:     &mockSkillStore{skills: nil},
	}

	result := buildChannelSkillIndex(context.Background(), nil, ss)

	// Even with no user/platform skills, built-in skills are always included
	if !strings.Contains(result, "generative-ui") {
		t.Error("expected built-in generative-ui skill in index even with empty stores")
	}
}

// TestBuildChannelSkillIndex_NilStores verifies graceful handling of nil stores.
func TestBuildChannelSkillIndex_NilStores(t *testing.T) {
	// All nil
	ss := &store.SkillStores{}
	result := buildChannelSkillIndex(context.Background(), nil, ss)
	// Built-in skills are always present
	if !strings.Contains(result, "generative-ui") {
		t.Error("expected built-in generative-ui skill even with nil stores")
	}

	// Only team set
	ss = &store.SkillStores{
		Team: &mockSkillStore{skills: []store.Skill{
			{Name: "team-only", Description: "Only team skill"},
		}},
	}
	result = buildChannelSkillIndex(context.Background(), nil, ss)
	if !strings.Contains(result, "team-only") {
		t.Error("team-only skill missing when Platform and Org are nil")
	}
}

// TestBuildChannelSkillIndex_OrgOverridesPlatform verifies the priority chain:
// team > org > platform.
func TestBuildChannelSkillIndex_OrgOverridesPlatform(t *testing.T) {
	ss := &store.SkillStores{
		Platform: &mockSkillStore{skills: []store.Skill{
			{Name: "shared-skill", Description: "Platform version"},
		}},
		Org: &mockSkillStore{skills: []store.Skill{
			{Name: "shared-skill", Description: "Org version wins over platform"},
		}},
		Team: &mockSkillStore{skills: nil},
	}

	result := buildChannelSkillIndex(context.Background(), nil, ss)

	if !strings.Contains(result, "Org version wins over platform") {
		t.Error("org should override platform when team has no override")
	}
	if strings.Contains(result, "Platform version") {
		t.Error("platform version should be overridden by org")
	}
}

func TestBuildChannelSkillIndex_FilesystemBaseAndCaseInsensitiveLaterWins(t *testing.T) {
	filesystem := []skills.Skill{
		{Name: "filesystem-only", Description: "filesystem survives"},
		{Name: "Shared", Description: "filesystem overridden"},
	}
	ss := &store.SkillStores{
		Platform: &mockSkillStore{skills: []store.Skill{{Name: "SHARED", Description: "platform overridden"}}},
		Org:      &mockSkillStore{skills: []store.Skill{{Name: "shared", Description: "org overridden"}}},
		Team:     &mockSkillStore{skills: []store.Skill{{Name: "sHaReD", Description: "team wins"}}},
	}

	result := buildChannelSkillIndex(context.Background(), filesystem, ss)
	if !strings.Contains(result, "filesystem survives") {
		t.Error("filesystem-only skill missing from cascade")
	}
	if !strings.Contains(result, "team wins") {
		t.Error("team skill should win case-insensitively")
	}
	for _, overridden := range []string{"filesystem overridden", "platform overridden", "org overridden"} {
		if strings.Contains(result, overridden) {
			t.Errorf("overridden entry %q should not be rendered", overridden)
		}
	}
	if count := strings.Count(strings.ToLower(result), "**shared**"); count != 1 {
		t.Errorf("expected one rendered shared entry, got %d", count)
	}
}

func TestNewChannelManager_CopiesFilesystemSkills(t *testing.T) {
	configured := []skills.Skill{{Name: "cached", Description: "loaded once"}}
	manager := NewChannelManager(nil, nil, nil, &ChannelManagerConfig{FilesystemSkills: configured})
	configured[0].Description = "mutated"

	if got := manager.filesystemSkills[0].Description; got != "loaded once" {
		t.Fatalf("manager filesystem skills = %q, want an initialization-time copy", got)
	}
}
