package agent

import (
	"context"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/execution"
)

func TestPreserveBotInvocationHandoff(t *testing.T) {
	for _, mode := range []string{"exact", "arguments", "policy", "identity", "replay", "direct"} {
		t.Run(mode, func(t *testing.T) {
			base := botPolicyContext(t, botBoundaryContext(t), []string{"read_file"}, nil)
			calls := 0
			base = WithBotPolicyRuntime(base, BotPolicyRuntime{Classifier: botClassifierFunc(botVerifiedRead), Guard: botGuardFunc(func(ctx context.Context, a execution.ActionDescriptor) (execution.PolicyDecision, error) {
				calls++
				return execution.EvaluateRunPolicy(ctx, a, time.Now()), nil
			})})
			type callerKey struct{}
			original := &contextToolContext{minimalReadonlyContext{Context: context.WithValue(base, callerKey{}, "untrusted")}}
			args := map[string]any{"path": "notes"}
			dispatched, err := authorizeBotInvocation(original, "read_file", args)
			if err != nil {
				t.Fatal(err)
			}
			bound := base
			switch mode {
			case "arguments":
				args["path"] = "other"
			case "policy":
				child, _ := execution.RunPolicyFromContext(base).Narrow([]string{"read_file"})
				bound = execution.WithRunPolicy(base, child)
			case "identity":
				id, _ := execution.BotRunIdentityFromContext(base)
				id.Version++
				bound = execution.WithBotRunIdentity(base, id)
			case "replay":
				if err := AuthorizeBotBoundTool(base, dispatched, "read_file", args); err != nil {
					t.Fatal(err)
				}
			case "direct":
				dispatched = original
			}
			preserved := PreserveBotInvocationHandoff(bound, dispatched)
			if preserved.Value(callerKey{}) != nil {
				t.Fatal("copied ambient caller values")
			}
			if preserved.Value(botLazyHandoffKey{}) != dispatched.Value(botLazyHandoffKey{}) {
				t.Fatal("copied or minted a handoff instead of retaining one-use state")
			}
			invocation := &contextToolContext{minimalReadonlyContext{Context: preserved}}
			err = AuthorizeBotBoundTool(preserved, invocation, "read_file", args)
			wantAllowed := mode == "exact" || mode == "direct"
			if (err == nil) != wantAllowed {
				t.Fatalf("allowed=%v err=%v", wantAllowed, err)
			}
			wantCalls := 1
			if mode == "direct" {
				wantCalls = 2
			}
			if calls != wantCalls {
				t.Fatalf("guard calls=%d want=%d", calls, wantCalls)
			}
			if mode == "exact" {
				// Even separately rebound aliases share consumption of the same token.
				again := &contextToolContext{minimalReadonlyContext{Context: PreserveBotInvocationHandoff(base, dispatched)}}
				if err := AuthorizeBotBoundTool(base, again, "read_file", args); err == nil {
					t.Fatal("rebound alias replayed handoff")
				}
			}
		})
	}
}
