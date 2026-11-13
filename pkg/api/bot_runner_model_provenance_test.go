package api

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
)

func TestBotChatRuntimeResolvedModelProvenance(t *testing.T) {
	for _, mode := range []string{"success", "model-error", "cleanup-panic", "factory-error", "legacy"} {
		t.Run(mode, func(t *testing.T) {
			ctx, req, sessions := botChatTestRequest(t)
			selected := store.SessionPin{Provider: "owner-scoped-instance", Model: "resolved-model"}
			var cleaned atomic.Int64
			llm := NewMockLLM(TextTurn("Done."))
			adapter, err := NewBotChatRuntimeAdapter(func(c context.Context, q bot.RunRequest) (*BotChatResources, error) {
				resources, err := botChatTestFactory(t, sessions, llm, nil, &cleaned)(c, q)
				if err != nil {
					return nil, err
				}
				if mode != "legacy" {
					resources.ModelSelection = &selected
				}
				if mode == "factory-error" {
					return resources, errors.New("factory unavailable")
				}
				if mode == "model-error" {
					resources.Agent.LLM = nil
				}
				if mode == "cleanup-panic" {
					resources.Cleanup = func() { panic("cleanup failed") }
				}
				return resources, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			result, runErr := adapter.Start(ctx, req, func(context.Context, bot.RuntimeEvent) error { return nil })
			if mode == "factory-error" || mode == "legacy" {
				if result.ModelSelection != nil {
					t.Fatalf("fabricated identity: %+v", result.ModelSelection)
				}
				return
			}
			if result.ModelSelection == nil || *result.ModelSelection != selected {
				t.Fatalf("lost identity: %+v err=%v", result.ModelSelection, runErr)
			}
			if mode == "success" && runErr != nil {
				t.Fatal(runErr)
			}
			if mode != "success" && runErr == nil {
				t.Fatal("expected execution failure")
			}
			selected.Model = "changed-after-return"
			if result.ModelSelection.Model != "resolved-model" {
				t.Fatal("identity aliases factory memory")
			}
		})
	}
}
