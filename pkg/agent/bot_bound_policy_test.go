package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/execution"
)

func TestBotHandoffClassifiedTarget(t *testing.T) {
	for _, lazy := range []bool{false, true} {
		for _, change := range []string{"target", "recipient", "effect", "unchanged"} {
			t.Run(fmt.Sprintf("lazy=%v/%s", lazy, change), func(t *testing.T) {
				base := botPolicyContext(t, botBoundaryContext(t), []string{"read_file"}, nil)
				target, recipient := "account-A", "recipient-A"
				effect := execution.EffectRead
				calls := 0
				base = WithBotPolicyRuntime(base, BotPolicyRuntime{Classifier: botClassifierFunc(func(ctx context.Context, name string, raw json.RawMessage) (execution.ActionDescriptor, error) {
					a, err := botVerifiedRead(ctx, name, raw)
					a.TargetFingerprint, a.Recipient, a.Effect = target, recipient, effect
					return a, err
				}), Guard: botGuardFunc(func(ctx context.Context, a execution.ActionDescriptor) (execution.PolicyDecision, error) {
					calls++
					return execution.EvaluateRunPolicy(ctx, a, time.Now()), nil
				})})
				original := &contextToolContext{minimalReadonlyContext{Context: base}}
				args := map[string]any{"path": "notes"}
				dispatched, err := authorizeBotInvocation(original, "read_file", args)
				if err != nil {
					t.Fatal(err)
				}
				switch change {
				case "target":
					target = "account-B"
				case "recipient":
					recipient = "recipient-B"
				case "effect":
					effect = execution.EffectUnknown
				}
				if lazy {
					err = authorizeBotLazyInvocation(dispatched, "read_file", args)
				} else {
					err = AuthorizeBotBoundTool(base, dispatched, "read_file", args, "account-A")
				}
				if (err == nil) != (change == "unchanged") || calls != 1 {
					t.Fatalf("err=%v guard calls=%d", err, calls)
				}
				if lazy {
					err = authorizeBotLazyInvocation(dispatched, "read_file", args)
				} else {
					err = AuthorizeBotBoundTool(base, dispatched, "read_file", args)
				}
				if err == nil {
					t.Fatal("handoff reused")
				}
			})
		}
	}
}

func TestBotBoundPolicyHandoff(t *testing.T) {
	for _, mode := range []string{"exact", "replay", "arguments", "identity", "policy", "direct"} {
		t.Run(mode, func(t *testing.T) {
			base := botPolicyContext(t, botBoundaryContext(t), []string{"read_file"}, nil)
			calls := 0
			base = WithBotPolicyRuntime(base, BotPolicyRuntime{Classifier: botClassifierFunc(botVerifiedRead), Guard: botGuardFunc(func(ctx context.Context, a execution.ActionDescriptor) (execution.PolicyDecision, error) {
				calls++
				return execution.EvaluateRunPolicy(ctx, a, time.Now()), nil
			})})
			original := &contextToolContext{minimalReadonlyContext{Context: base}}
			args := map[string]any{"path": "notes"}
			dispatched, err := authorizeBotInvocation(original, "read_file", args)
			if err != nil {
				t.Fatal(err)
			}
			bound := base // deliberately has no handoff; owner rebinding strips values
			name := "read_file"
			switch mode {
			case "arguments":
				args["path"] = "other"
			case "identity":
				name = "write_file"
			case "policy":
				child, _ := execution.RunPolicyFromContext(base).Narrow([]string{})
				bound = execution.WithRunPolicy(base, child)
			case "replay":
				if err := AuthorizeBotBoundTool(bound, dispatched, name, args); err != nil {
					t.Fatal(err)
				}
			case "direct":
				dispatched = original
			}
			err = AuthorizeBotBoundTool(bound, dispatched, name, args)
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
		})
	}
}
