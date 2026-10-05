package launcher

import (
	"testing"

	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/skills"
)

func TestStudioListenAddress(t *testing.T) {
	noAuth := api.NewPlatformAuth(config.PlatformAuthConfig{Mode: config.AuthModeNone}, nil, config.StorageConfig{Backend: "sqlite"})
	if got := studioListenAddress(9393, noAuth); got != "127.0.0.1:9393" {
		t.Fatalf("no-auth listen address = %q, want loopback", got)
	}
	if got := studioListenAddress(9393, nil); got != ":9393" {
		t.Fatalf("authenticated listen address = %q, want all interfaces", got)
	}
}

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
