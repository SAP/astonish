package api

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/drill"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tools"
)

func TestCapturedDrillInteractiveOriginalCallerExactReadOnly(t *testing.T) {
	for _, mode := range []string{"exact", "shell", "revoked", "uncaptured", "arguments"} {
		t.Run(mode, func(t *testing.T) {
			ctx, req, _ := botChatTestRequest(t)
			id := req.Policy.Identity()
			ctx, req = employeeInteractiveTestRequest(t, ctx, req)
			ctx = execution.WithBotRunIdentity(ctx, id)
			classifier := capturedReadClassifier{}
			name := "filter_json"
			if mode == "shell" {
				name = "shell_command"
			}
			args := map[string]any{"value": "exact"}
			raw, _ := json.Marshal(args)
			child, _ := classifier.DescribeAction(ctx, name, raw)
			snapshot := tools.CapturedDrill{Format: tools.CapturedDrillFormat, Suite: &drill.LoadedSuite{Name: "pinned", Config: &config.AgentConfig{}}, Children: []execution.ActionDescriptor{child}}
			evidence, _ := json.Marshal(snapshot)
			live := &capturedLiveGuard{deny: mode == "revoked"}
			guard := &botResumeGuard{req: req, attempted: true, consumptionStarted: true, live: live, approval: store.BotApproval{Action: execution.ActionDescriptor{Tool: "run_drill", Effect: execution.EffectUnknown, Arguments: []byte(`{"suite_name":"pinned"}`), TargetFingerprint: snapshot.Fingerprint(), TargetSnapshot: evidence}}}
			ctx = agent.WithBotPolicyRuntime(ctx, agent.BotPolicyRuntime{Guard: guard, Classifier: classifier})
			if mode == "uncaptured" {
				if _, err := guard.Authorize(ctx, child); err == nil {
					t.Fatal("uncaptured action admitted")
				}
				return
			}
			if mode == "arguments" {
				args["value"] = "other"
			}
			bound, err := CapturedDrillChildContext(ctx, name, args)
			if mode == "shell" || mode == "arguments" {
				if err == nil {
					t.Fatal("unsafe child admitted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if execution.RunPolicyFromContext(bound) != nil {
				t.Fatal("interactive autonomy policy minted")
			}
			_, err = guard.Authorize(PreserveCapturedDrillOccurrence(ctx, bound), child)
			if mode == "revoked" {
				if err == nil {
					t.Fatal("live guard bypassed")
				}
				return
			}
			if err != nil || live.checks != 1 {
				t.Fatalf("exact child %v checks=%d", err, live.checks)
			}
			if _, err := guard.Authorize(bound, child); err == nil {
				t.Fatal("child replayed")
			}
		})
	}
}

type capturedReadClassifier struct{}

func (capturedReadClassifier) DescribeAction(_ context.Context, n string, a json.RawMessage) (execution.ActionDescriptor, error) {
	effect := execution.EffectRead
	if n == "shell_command" {
		effect = execution.EffectUnknown
	}
	return execution.ActionDescriptor{Tool: n, Arguments: a, Effect: effect, ConstraintsVerified: effect == execution.EffectRead}, nil
}

type capturedLiveGuard struct {
	checks int
	deny   bool
}

func (g *capturedLiveGuard) Authorize(_ context.Context, a execution.ActionDescriptor) (execution.PolicyDecision, error) {
	g.checks++
	if g.deny {
		return execution.PolicyDecision{}, store.ErrBotConflict
	}
	digest, _ := a.Digest()
	return execution.PolicyDecision{Kind: execution.PolicyAllow, ActionDigest: digest}, nil
}

func TestCapturedDrillOccurrenceExactAndNoReplay(t *testing.T) {
	for _, mode := range []string{"exact", "arguments", "tool", "owner", "revoked", "uncaptured", "shell", "shell-grant", "cancelled", "missing-classifier", "late-owner", "late-fence", "late-revoked", "rebound-policy"} {
		t.Run(mode, func(t *testing.T) {
			ctx, req, _ := botChatTestRequest(t)
			id := req.Policy.Identity()
			policy, err := execution.NewRunPolicy(execution.RunPolicySpec{Identity: id, Level: execution.AutonomyScoped, AllowedTools: []string{"filter_json", "shell_command"}})
			if err != nil {
				t.Fatal(err)
			}
			ctx = execution.WithRunPolicy(execution.WithBotRunIdentity(ctx, id), policy)
			classifier := capturedReadClassifier{}
			name := "filter_json"
			if mode == "shell" || mode == "shell-grant" {
				name = "shell_command"
			}
			args := map[string]any{"value": "exact"}
			raw, _ := json.Marshal(args)
			child, _ := classifier.DescribeAction(ctx, name, raw)
			if mode == "shell-grant" {
				digest, _ := child.Digest()
				policy, err = execution.NewRunPolicy(execution.RunPolicySpec{Identity: id, Level: execution.AutonomyScoped, AllowedTools: []string{name}, Grants: []execution.ScopedGrant{{ID: uuid.New(), Identity: id, ActionDigest: digest, ExpiresAt: time.Now().Add(time.Hour)}}})
				if err != nil {
					t.Fatal(err)
				}
				ctx = execution.WithRunPolicy(ctx, policy)
			}
			snapshot := tools.CapturedDrill{Format: tools.CapturedDrillFormat, Suite: &drill.LoadedSuite{Name: "pinned", Config: &config.AgentConfig{}}, Children: []execution.ActionDescriptor{child}}
			evidence, _ := json.Marshal(snapshot)
			live := &capturedLiveGuard{deny: mode == "revoked"}
			guard := &botResumeGuard{attempted: true, consumptionStarted: true, live: live, approval: store.BotApproval{Action: execution.ActionDescriptor{Tool: "run_drill", Effect: execution.EffectUnknown, Arguments: []byte(`{"suite_name":"pinned"}`), TargetFingerprint: snapshot.Fingerprint(), TargetSnapshot: evidence}}}
			ctx = agent.WithBotPolicyRuntime(ctx, agent.BotPolicyRuntime{Guard: guard, Classifier: classifier})
			if mode == "arguments" {
				args["value"] = "changed"
			}
			if mode == "tool" {
				name = "other"
			}
			if mode == "owner" {
				other := id
				other.ActorID = "foreign"
				ctx = execution.WithBotRunIdentity(ctx, other)
			}
			if mode == "uncaptured" {
				if _, err := guard.Authorize(ctx, child); err == nil {
					t.Fatal("generic resume widened")
				}
				return
			}
			if mode == "cancelled" {
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
			}
			if mode == "missing-classifier" {
				ctx = agent.WithBotPolicyRuntime(ctx, agent.BotPolicyRuntime{Guard: guard})
			}
			bound, err := CapturedDrillChildContext(ctx, name, args)
			admitted := mode == "exact" || mode == "revoked" || mode == "late-owner" || mode == "late-fence" || mode == "late-revoked" || mode == "rebound-policy"
			if !admitted {
				if err == nil {
					t.Fatal("altered or consequential child admitted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if mode == "late-owner" {
				other := id
				other.ActorID = "foreign"
				bound = execution.WithBotRunIdentity(bound, other)
			}
			if mode == "late-fence" || mode == "late-revoked" {
				live.deny = true // current guard changed after occurrence capture
			}
			if mode == "rebound-policy" {
				bound = execution.WithRunPolicy(bound, policy)
			}
			_, err = guard.Authorize(bound, child)
			if mode == "revoked" || mode == "late-owner" || mode == "late-fence" || mode == "late-revoked" {
				if err == nil {
					t.Fatal("revocation ignored")
				}
				return
			}
			if err != nil || live.checks != 1 {
				t.Fatal(err, live.checks)
			}
			if _, err = guard.Authorize(bound, child); err == nil {
				t.Fatal("occurrence replayed")
			}
			if _, err = CapturedDrillChildContext(ctx, name, args); err == nil {
				t.Fatal("captured occurrence reused")
			}
		})
	}
}

func TestCapturedDrillRepeatedOccurrenceConcurrentNoReplay(t *testing.T) {
	ctx, req, _ := botChatTestRequest(t)
	id := req.Policy.Identity()
	policy, err := execution.NewRunPolicy(execution.RunPolicySpec{Identity: id, Level: execution.AutonomyScoped, AllowedTools: []string{"filter_json"}})
	if err != nil {
		t.Fatal(err)
	}
	ctx = execution.WithRunPolicy(execution.WithBotRunIdentity(ctx, id), policy)
	classifier := capturedReadClassifier{}
	args := map[string]any{"value": "exact"}
	raw, _ := json.Marshal(args)
	child, _ := classifier.DescribeAction(ctx, "filter_json", raw)
	// Identical action digests are two occurrences, not interchangeable authority.
	snapshot := tools.CapturedDrill{Format: tools.CapturedDrillFormat, Suite: &drill.LoadedSuite{Name: "pinned", Config: &config.AgentConfig{}}, Children: []execution.ActionDescriptor{child, child}}
	evidence, _ := json.Marshal(snapshot)
	parent := execution.ActionDescriptor{Tool: "run_drill", Effect: execution.EffectUnknown, Arguments: []byte(`{"suite_name":"pinned"}`), TargetFingerprint: snapshot.Fingerprint(), TargetSnapshot: evidence}
	digest, _ := parent.Digest()
	live := &capturedLiveGuard{}
	guard := &botResumeGuard{attempted: true, consumptionStarted: true, live: live, approval: store.BotApproval{Action: parent, ActionDigest: digest}}
	ctx = agent.WithBotPolicyRuntime(ctx, agent.BotPolicyRuntime{Guard: guard, Classifier: classifier})
	first, err := CapturedDrillChildContext(ctx, "filter_json", args)
	if err != nil {
		t.Fatal(err)
	}
	// Every dispatcher rebind shares the same one-shot marker.
	var successes atomic.Int32
	var unexpected atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			bound := PreserveCapturedDrillOccurrence(ctx, first)
			decision, err := guard.Authorize(bound, child)
			if err == nil && decision.Kind == execution.PolicyAllow {
				successes.Add(1)
			} else if !errors.Is(err, store.ErrBotForbidden) {
				unexpected.Add(1)
			}
		}()
	}
	wg.Wait()
	if successes.Load() != 1 || unexpected.Load() != 0 || live.checks != 1 {
		t.Fatalf("concurrent occurrence authority: successes=%d unexpected=%d live=%d", successes.Load(), unexpected.Load(), live.checks)
	}
	second, err := CapturedDrillChildContext(ctx, "filter_json", args)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := guard.Authorize(first, child); !errors.Is(err, store.ErrBotForbidden) {
		t.Fatalf("stale identical occurrence reused: %v", err)
	}
	if _, err := guard.Authorize(second, child); err != nil || live.checks != 2 {
		t.Fatalf("second exact occurrence rejected: %v checks=%d", err, live.checks)
	}
	if _, err := CapturedDrillChildContext(ctx, "filter_json", args); !errors.Is(err, store.ErrBotForbidden) {
		t.Fatalf("completed suite minted another occurrence: %v", err)
	}
	for _, stale := range []context.Context{first, second, ctx} {
		if _, err := guard.Authorize(stale, child); !errors.Is(err, store.ErrBotForbidden) {
			t.Fatalf("completed suite child replayed: %v", err)
		}
	}
	if _, err := guard.Authorize(ctx, parent); !errors.Is(err, store.ErrBotForbidden) || live.checks != 2 {
		t.Fatalf("completed parent replayed: %v checks=%d", err, live.checks)
	}
}
