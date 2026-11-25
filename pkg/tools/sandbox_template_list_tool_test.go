package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/SAP/astonish/pkg/sandbox"
	"google.golang.org/adk/tool"
)

func TestListSandboxTemplatesInstanceIsolation(t *testing.T) {
	type runnable interface {
		Run(tool.Context, any) (map[string]any, error)
	}
	var instances []runnable
	for _, name := range []string{"first", "second"} {
		registry := sandbox.NewInMemoryRegistry(t.TempDir())
		if err := registry.Add(&sandbox.TemplateMeta{Name: name}); err != nil {
			t.Fatal(err)
		}
		instance, err := NewListSandboxTemplatesTool(registry)
		if err != nil {
			t.Fatal(err)
		}
		instances = append(instances, instance.(runnable))
	}
	unavailable, err := NewListSandboxTemplatesTool(nil)
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"first", "second"} {
		result, err := instances[i].Run(templateScopeContext{base: context.Background()}, map[string]any{})
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(result)
		var decoded ListSandboxTemplatesResult
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatal(err)
		}
		if decoded.Status != "ok" || len(decoded.Templates) != 1 || decoded.Templates[0].Name != want {
			t.Fatalf("instance %d leaked registry: %s", i, raw)
		}
	}
	result, err := unavailable.(runnable).Run(templateScopeContext{base: context.Background()}, map[string]any{})
	if err != nil || result["status"] != "error" {
		t.Fatalf("nil dependency fell back: %v %v", result, err)
	}
}
