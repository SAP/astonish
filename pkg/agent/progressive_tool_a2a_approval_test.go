package agent

import (
	"context"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
	"testing"
)

func TestProgressiveToolBridgeExactQualifiedA2AIndex(t *testing.T) {
	target, err := functiontool.New(functiontool.Config{Name: "a2a_configured_service", Description: "fixture"}, func(tool.Context, map[string]any) (map[string]any, error) { return nil, nil })
	if err != nil {
		t.Fatal(err)
	}
	idx := syncTestToolIndex(t, &ToolGroup{Name: "a2a", Tools: []tool.Tool{target}})
	resolver := deferredToolResolver{index: idx}
	for _, name := range []string{"a2a/a2a_configured_service", "a2a_configured_service"} {
		t.Run(name, func(t *testing.T) {
			got, group, err := resolver.resolve(&minimalReadonlyContext{Context: context.Background()}, name)
			if err != nil || got != target || group != "a2a" {
				t.Fatalf("resolution identity %v %s %v", got, group, err)
			}
		})
	}
	for _, name := range []string{"other/a2a_configured_service", "a2a/foreign", "a2a/a2a_configured_service/extra"} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := resolver.resolve(&minimalReadonlyContext{Context: context.Background()}, name); err == nil {
				t.Fatal("foreign qualified identity resolved")
			}
		})
	}
	ctx := store.WithDisabledTools(context.Background(), []string{"a2a/a2a_configured_service"})
	if _, _, err := resolver.resolve(&minimalReadonlyContext{Context: ctx}, "a2a/a2a_configured_service"); err == nil {
		t.Fatal("disabled exact identity resolved")
	}
}
