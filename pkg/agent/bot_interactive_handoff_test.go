package agent

import (
	"context"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
)

func TestBotInteractiveHandoffsRecheckAuthority(t *testing.T) {
	for _, boundary := range []string{"lazy", "bound"} {
		t.Run(boundary, func(t *testing.T) {
			ctx, registry, id := interactivePolicyContext(t, []string{"chat", "tool:execute"})
			defer registry.Close()
			tc := &contextToolContext{minimalReadonlyContext{Context: ctx}}
			args := map[string]any{"path": "notes"}
			invocation, err := authorizeBotInvocation(tc, "read_file", args)
			if err != nil {
				t.Fatal(err)
			}
			registry.Cancel(id)
			if boundary == "lazy" {
				err = authorizeBotLazyInvocation(invocation, "read_file", args)
			} else {
				err = AuthorizeBotBoundTool(ctx, invocation, "read_file", args)
			}
			if err == nil {
				t.Fatal("revoked handoff executed")
			}
		})
	}
}

func TestBotInteractiveAuxiliaryRejectsExpiredContextAndWrongRun(t *testing.T) {
	for _, mode := range []string{"deadline", "different-run"} {
		t.Run(mode, func(t *testing.T) {
			ctx, registry, id := interactivePolicyContext(t, []string{"chat"})
			defer registry.Close()
			probe := &botAuxProbe{}
			ctx = WithBotAuxiliaryLLM(ctx, probe)
			llm := auxiliaryLLM(ctx, nil)
			if mode == "deadline" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithDeadline(ctx, time.Now().Add(-time.Second))
				defer cancel()
			} else {
				id.RunID = uuid.New()
				ctx = execution.WithBotRunIdentity(ctx, id)
			}
			denied := false
			for _, err := range llm.GenerateContent(ctx, &model.LLMRequest{}, false) {
				denied = err != nil
			}
			if !denied || probe.calls != 0 {
				t.Fatalf("denied=%v provider calls=%d", denied, probe.calls)
			}
		})
	}
}
