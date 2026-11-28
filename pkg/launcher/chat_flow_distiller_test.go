package launcher

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/provider"
	"google.golang.org/adk/session"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
)

const sharedDistillerFlow = `name: shared_test
description: validation fixture
nodes:
  - name: inspect
    type: tool
    tools_selection: [scoped_read]
    args:
      path: "{missing_path}"
flow:
  - from: START
    to: inspect
  - from: inspect
    to: END
`

func TestChatRuntimeDirectToolsetDistillation(t *testing.T) {
	calls := 0
	fixture, err := functiontool.New(functiontool.Config{Name: "scoped_read", Description: "direct scoped read"}, func(tool.Context, struct {
		Path string `json:"path"`
	}) (map[string]any, error) {
		calls++
		return map[string]any{"read": true}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	policy := &runtimeTestPolicy{}
	runtime, err := NewScopedChatRuntime(t.Context(), ChatRuntimeDependencies{
		Config: &ChatFactoryConfig{AppConfig: &config.AppConfig{}},
		LLM:    provider.NewPlaceholderLLM(), Sessions: session.InMemoryService(),
		Catalog: &ChatCatalog{}, Toolsets: []tool.Toolset{&runtimeFixedToolset{tools: []tool.Tool{fixture}}},
	}, ChatRuntimeHooks{Authorization: policy})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtime.Cleanup)
	d := runtime.ChatAgent.FlowDistiller
	d.LLM = func(context.Context, string) (string, error) { return "```yaml\n" + sharedDistillerFlow + "```", nil }
	if _, err := d.Distill(t.Context(), agent.DistillRequest{UserRequest: "repeat the read", Trace: &agent.ExecutionTrace{}}); err != nil {
		t.Fatalf("direct capability missing from distillation: %v", err)
	}
	if calls != 0 {
		t.Fatal("distillation executed a tool")
	}
	if len(d.Toolsets) != 1 || d.Toolsets[0] != runtime.ChatAgent.Toolsets[0] {
		t.Fatal("distillation lost guarded toolset identity")
	}
	ctx := runtimeTestToolContext{lifetime: t.Context()}
	ts, err := d.Toolsets[0].Tools(ctx)
	if err != nil || len(ts) != 1 {
		t.Fatalf("discovery: %v %v", ts, err)
	}
	policy.deny = true
	if _, err := ts[0].(chatRuntimeRunnable).Run(ctx, map[string]any{"path": "proof"}); !errors.Is(err, errRuntimeTestDenied) || calls != 0 {
		t.Fatalf("distiller view bypassed policy: %v", err)
	}
	policy.deny = false
	if out, err := ts[0].(chatRuntimeRunnable).Run(ctx, map[string]any{"path": "proof"}); err != nil || calls != 1 || out["read"] != true {
		t.Fatalf("direct capability failed execution: %v %v", out, err)
	}
}

func TestBuildChatFlowDistillerNoFallback(t *testing.T) {
	// No app config, model, host resources, or catalog are needed for validation.
	d := buildChatFlowDistiller(nil, nil, nil)
	if d.Tools != nil || d.Toolsets != nil || d.MaxRetries != 3 {
		t.Fatalf("changed supplied dependencies or defaults: %+v", d)
	}
	if got := d.GetSchema(); got == "" || got != api.GetFlowSchema() {
		t.Fatal("flow schema not wired")
	}
	if got := d.ValidateYAML(sharedDistillerFlow, nil); got.Valid || !strings.Contains(strings.Join(got.Errors, " "), "unknown tool 'scoped_read'") {
		t.Fatalf("empty catalog acquired an unsupplied tool: %+v", got)
	}
}

func TestBuildChatFlowDistillerValidation(t *testing.T) {
	d := buildChatFlowDistiller(nil, nil, nil)
	catalog := []agent.DistillerToolInfo{{Name: "scoped_read", Description: "Scoped read", Source: "test"}}
	for _, tc := range []struct {
		name, yaml string
		valid      bool
	}{
		{"supplied tool", sharedDistillerFlow, true},
		{"unsupplied tool", strings.ReplaceAll(sharedDistillerFlow, "scoped_read", "unavailable_read"), false},
		{"invalid schema", "name: missing_nodes", false},
		{"malformed yaml", "nodes: [", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := d.ValidateYAML(tc.yaml, catalog)
			if got.Valid != tc.valid || (!tc.valid && len(got.Errors) == 0) {
				t.Fatalf("validation = %+v, want valid %v", got, tc.valid)
			}
		})
	}
}

func TestBuildChatFlowDistillerDryRun(t *testing.T) {
	d := buildChatFlowDistiller(nil, nil, nil)
	schemas := map[string]json.RawMessage{"scoped_read": json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`)}
	valid := d.DryRunYAML(sharedDistillerFlow, schemas)
	if !valid.Valid || len(valid.Warnings) == 0 {
		t.Fatalf("expected preserved unresolved-variable warning: %+v", valid)
	}
	invalid := d.DryRunYAML(strings.ReplaceAll(sharedDistillerFlow, "      path:", "      wrong:"), schemas)
	if invalid.Valid || len(invalid.Errors) == 0 || len(invalid.Warnings) == 0 {
		t.Fatalf("expected schema error and required-parameter warning: %+v", invalid)
	}
	if got := d.DryRunYAML(sharedDistillerFlow, nil); !got.Valid {
		t.Fatalf("nil schema behavior changed: %+v", got)
	}
}
