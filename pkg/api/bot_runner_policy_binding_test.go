package api

import (
	"context"
	"errors"
	"testing"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
)

func TestBotPolicyFactoryBindingPreservesApprovalContinuation(t *testing.T) {
	live := agent.BotPolicyRuntime{Classifier: resumeTestClassifier{}, Guard: resumeTestGuard{}}
	capture := &botApprovalCapture{}
	ctx := agent.WithBotPolicyRuntime(context.Background(), agent.BotPolicyRuntime{Approvals: capture})
	bound, err := BindBotChatPolicyRuntime(ctx, live)
	if err != nil || bound.Approvals != capture || bound.Guard != live.Guard || bound.Classifier != live.Classifier {
		t.Fatalf("capture detached from prepared runtime: %+v %v", bound, err)
	}
	resume := &botResumeGuard{}
	ctx = agent.WithBotPolicyRuntime(context.Background(), agent.BotPolicyRuntime{Guard: resume, Approvals: capture})
	bound, err = BindBotChatPolicyRuntime(ctx, live)
	if err != nil || bound.Guard != resume || resume.live != live.Guard || bound.Approvals != nil {
		t.Fatalf("exact resume detached from prepared guard: %+v %v", bound, err)
	}
	resume.attempted = true
	if _, err = BindBotChatPolicyRuntime(ctx, live); !errors.Is(err, store.ErrBotConflict) {
		t.Fatalf("consumed continuation rebound: %v", err)
	}
}

func TestBotPolicyFactoryBindingRequiresPreparedGuardAndClassifier(t *testing.T) {
	for _, live := range []agent.BotPolicyRuntime{
		{},
		{Classifier: resumeTestClassifier{}},
		{Guard: resumeTestGuard{}},
	} {
		if _, err := BindBotChatPolicyRuntime(context.Background(), live); !errors.Is(err, store.ErrBotUnsupported) {
			t.Fatalf("unprepared runtime accepted: %v", err)
		}
	}
	// Deferred guard construction is permitted, but never tool authorization
	// before the factory has installed the live isolation/fencing guard.
	a := execution.ActionDescriptor{Tool: "read_file", Arguments: []byte(`{"body":"exact"}`), Effect: execution.EffectSend}
	digest, err := a.Digest()
	if err != nil {
		t.Fatal(err)
	}
	g := &botResumeGuard{approval: store.BotApproval{ActionDigest: digest}}
	if _, err := g.Authorize(context.Background(), a); !errors.Is(err, store.ErrBotUnsupported) {
		t.Fatalf("unbound resume guard authorized an effect: %v", err)
	}
}
