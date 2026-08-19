package launcher

import (
	"testing"

	"github.com/SAP/astonish/pkg/skills"
)

func TestStudioChatComponentsFromFactoryResult_CopiesFilesystemSkills(t *testing.T) {
	configured := []skills.Skill{{Name: "initialized", Description: "factory value"}}
	result := &ChatFactoryResult{FilesystemSkills: configured}

	components := studioChatComponentsFromFactoryResult(result, true)
	configured[0].Description = "mutated"
	result.FilesystemSkills[0].Name = "changed"

	if !components.SandboxEnabled {
		t.Fatal("expected sandbox flag to be forwarded")
	}
	if got := components.FilesystemSkills[0]; got.Name != "initialized" || got.Description != "factory value" {
		t.Fatalf("filesystem skill = %+v, want copied initialization-time value", got)
	}
}
