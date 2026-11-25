package launcher

import (
	"context"
	"testing"

	"github.com/SAP/astonish/pkg/chatruntime"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/session"
)

func TestChatRuntimeScopedMemoryAuthority(t *testing.T) {
	ambient := &runtimeMemoryStore{}
	ambientCalls := 0
	for _, user := range []string{"alice", "bob"} {
		for _, configured := range []bool{false, true} {
			f := newBotFactoryFixture(t)
			p := f.req.Principal
			p.Subject = user
			mem := &runtimeMemoryStore{}
			f.services.Memory = nil
			if configured {
				f.services.Memory = mem
			}
			cfg := &config.AppConfig{}
			in, err := chatruntime.Build(t.Context(), chatruntime.BuildOptions{Principal: p, Services: f.services, ResourceScope: chatruntime.ResourceScopeTeam, AppConfig: cfg, MemorySaveOrMerge: func(ctx context.Context, selected store.MemoryStore, e store.MemoryEntry) error {
				principal, _ := execution.PrincipalFromContext(ctx)
				if selected != mem || principal.Subject != user || e.CreatedBy != user || e.SessionID != user+"-session" || ctx.Value(schedulerRequestMarker{}) != "current" {
					t.Fatal("memory callback escaped scoped store or current request")
				}
				return selected.Add(ctx, e)
			}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = in.Close() })
			runtime, err := NewScopedChatRuntime(t.Context(), ChatRuntimeDependencies{Inputs: in, Config: &ChatFactoryConfig{AppConfig: cfg}, LLM: provider.NewPlaceholderLLM(), Sessions: session.InMemoryService(), Resources: &ChatCatalogResources{Pool: &catalogTestPool{}}}, ChatRuntimeHooks{})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(runtime.Cleanup)
			parent := store.WithMemoryStore(t.Context(), ambient)
			parent = store.WithMemorySaveOrMerge(parent, func(context.Context, store.MemoryStore, store.MemoryEntry) error { ambientCalls++; return nil })
			ctx := scopedSchedulerInvocation{runtimeTestToolContext: runtimeTestToolContext{lifetime: context.WithValue(parent, schedulerRequestMarker{}, "current")}, session: user + "-session"}
			save := runtime.ChatAgent.ToolIndex.GetToolEntry("memory_save").Tool.(chatRuntimeRunnable)
			result, err := save.Run(ctx, map[string]any{"category": "preferences", "content": "fixture fact"})
			if err != nil || result["saved"] != configured {
				t.Fatalf("save=%v err=%v configured=%v", result, err, configured)
			}
			search := runtime.ChatAgent.ToolIndex.GetToolEntry("memory_search").Tool.(chatRuntimeRunnable)
			if _, err = search.Run(ctx, map[string]any{"query": "fixture"}); err != nil {
				t.Fatal(err)
			}
			if configured && (len(mem.entries) != 1 || mem.searches != 1) {
				t.Fatal("configured memory not executed")
			}
		}
	}
	if ambientCalls != 0 || ambient.searches != 0 {
		t.Fatal("ambient memory used")
	}
}
