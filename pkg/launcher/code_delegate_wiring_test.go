package launcher

import (
	"context"
	"testing"

	"github.com/SAP/astonish/pkg/config"
)

func TestChatFactoryCodeDelegateModeWiring(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		codeMode, projectContext bool
	}{
		{name: "code-without-project-context", codeMode: true},
		{name: "project-context-is-not-code-mode", projectContext: true},
		{name: "noncode"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			t.Setenv("HOME", t.TempDir())
			cfg := &ChatFactoryConfig{
				AppConfig:            &config.AppConfig{},
				CodeMode:             tc.codeMode,
				LoadProjectContext:   tc.projectContext,
				AllowMissingProvider: true,
				WorkspaceDir:         t.TempDir(),
			}
			result, err := NewWiredChatAgent(context.Background(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer result.Cleanup()
			if result.ChatAgent.SubAgentManager == nil {
				t.Fatal("missing delegate manager")
			}
			if got := result.ChatAgent.SubAgentManager.Config.CodeMode; got != tc.codeMode {
				t.Fatalf("delegate CodeMode=%v want %v (project context=%v)", got, tc.codeMode, tc.projectContext)
			}
		})
	}
}
