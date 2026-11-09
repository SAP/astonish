package launcher

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/fleet"
	"github.com/SAP/astonish/pkg/sandbox/sessioncreds"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

type drillPayloadApprovals struct{ actions []execution.ActionDescriptor }

func (a *drillPayloadApprovals) RequestApproval(_ context.Context, _ execution.BotRunIdentity, action execution.ActionDescriptor, _ string) (uuid.UUID, error) {
	a.actions = append(a.actions, action)
	return uuid.New(), nil
}

func TestBotDrillPendingPayloadApproval(t *testing.T) {
	f := newBotFactoryFixture(t)
	data := drillData("test")
	data["suite"] = "name: suite\ntype: drill_suite\nsuite_config:\n  credentials: {app: owner}\n  credential_injection:\n    files:\n      - {credential: app, path: /workspace/token, field: token, mode: '0600'}\n"
	f.services.Flows = drillFlowStore{data: data}
	const secret = "low-entropy-pin-1234"
	cs := sessioncreds.NewStore(map[string]*store.Credential{"owner": {Type: store.CredBearer, Token: secret}})
	f.services.PersonalCredentials = cs
	drillGrants(t, f, map[string][]map[string]any{"inject_drill_credentials": {{"suite_name": "suite"}}})
	b, c := &drillBackend{}, &drillClient{}
	ts, ctx, _ := drillFactory(t, f, b, c, BotCatalogueClassifier{})
	bound := ts["inject_drill_credentials"].(*botBoundTool)
	runtime := agent.BotPolicyRuntimeFromContext(bound.binding.owner)
	approvals := &drillPayloadApprovals{}
	runtime.Approvals = approvals
	bound.binding.owner = agent.WithBotPolicyRuntime(bound.binding.owner, runtime)
	_, err := bound.Run(ctx, map[string]any{"suite_name": "suite"})
	if err == nil || len(b.ids) != 0 || len(approvals.actions) != 1 {
		t.Fatalf("expected pending payload approval without writes: err=%v writes=%d approvals=%d", err, len(b.ids), len(approvals.actions))
	}
	wire, err := json.Marshal(approvals.actions[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(wire), secret) {
		t.Fatal("pending approval persisted resolved credential")
	}
	var action map[string]any
	if err := json.Unmarshal(approvals.actions[0].Arguments, &action); err != nil {
		t.Fatal(err)
	}
	ref, ok := action["payload_ref"].(string)
	if !ok || !strings.HasPrefix(ref, "drill-payload-v1:") {
		t.Fatalf("missing opaque payload reference: %v", action)
	}
	resolved, err := fleet.ResolveCredentialsPlatformMap(f.ctx, map[string]string{"app": "owner"}, cs)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := botDrillPayloadReference(resolved)
	if err != nil || ref != expected {
		t.Fatal("pending approval does not bind resolved snapshot")
	}
}

func TestBotDrillCredentialPayloadBinding(t *testing.T) {
	for _, mode := range []string{"unchanged", "rotated-before", "rotated-after-authorization"} {
		t.Run(mode, func(t *testing.T) {
			f := newBotFactoryFixture(t)
			data := drillData("test")
			data["suite"] = "name: suite\ntype: drill_suite\nsuite_config:\n  credentials: {app: owner}\n  credential_injection:\n    files:\n      - {credential: app, path: /workspace/token, field: token, mode: '0600'}\n"
			f.services.Flows = drillFlowStore{data: data}
			oldSecret, newSecret := "1234", "5678"
			cs := sessioncreds.NewStore(map[string]*store.Credential{"owner": {Type: store.CredBearer, Token: oldSecret}})
			f.services.PersonalCredentials = cs
			mapping := map[string]any{"suite_name": "suite", "credentials": map[string]string{"app": "owner"}, "credential_injection": map[string]any{"files": []any{map[string]any{"credential": "app", "path": "/workspace/token", "field": "token", "mode": "0600"}}}, "sandbox_id": "physical-run"}
			resolved, err := fleet.ResolveCredentialsPlatformMap(f.ctx, map[string]string{"app": "owner"}, cs)
			if err != nil {
				t.Fatal(err)
			}
			ref, err := botDrillPayloadReference(resolved)
			if err != nil {
				t.Fatal(err)
			}
			mapping["payload_ref"] = ref
			drillGrants(t, f, map[string][]map[string]any{"inject_drill_credentials": {{"suite_name": "suite"}, mapping}})
			b, c := &drillBackend{}, &drillClient{}
			ts, ctx, _ := drillFactory(t, f, b, c, BotCatalogueClassifier{})
			bound := ts["inject_drill_credentials"].(*botBoundTool)
			runtime := agent.BotPolicyRuntimeFromContext(bound.binding.owner)
			guard := &emailInterleaveGuard{PolicyGuard: runtime.Guard}
			rotate := func() {
				if err := cs.Set(f.ctx, "owner", &store.Credential{Type: store.CredBearer, Token: newSecret}); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "rotated-before" {
				rotate()
			}
			if mode == "rotated-after-authorization" {
				guard.after = func() {
					if guard.calls == 2 {
						rotate()
					}
				}
			}
			runtime.Guard = guard
			bound.binding.owner = agent.WithBotPolicyRuntime(bound.binding.owner, runtime)
			out, err := ts["inject_drill_credentials"].Run(ctx, map[string]any{"suite_name": "suite"})
			if mode == "rotated-before" {
				if err == nil || len(b.ids) != 0 {
					t.Fatalf("rotated payload injected: %v", err)
				}
				return
			}
			if err != nil || len(b.contents) != 1 || b.contents[0] != oldSecret {
				t.Fatalf("snapshot mismatch err=%v contents=%v", err, b.contents)
			}
			wire, _ := json.Marshal(out)
			approval, _ := json.Marshal(mapping)
			if strings.Contains(string(wire), oldSecret) || strings.Contains(string(approval), oldSecret) {
				t.Fatal("resolved secret exposed")
			}
			if guard.calls != 2 {
				t.Fatalf("expected outer + payload authorizations, got %d", guard.calls)
			}
		})
	}
}
