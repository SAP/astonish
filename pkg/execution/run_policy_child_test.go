package execution

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestBotChildDropsGrantsAndCannotWidenAgain(t *testing.T) {
	id := BotRunIdentity{BotID: uuid.New(), RunID: uuid.New(), Version: 1, ActorID: "owner", OrgSlug: "org", TeamSlug: "team"}
	now := time.Now()
	action := ActionDescriptor{Tool: "read_file", Effect: EffectRead, Arguments: json.RawMessage(`{}`)}
	digest, _ := action.Digest()
	p, err := NewRunPolicy(RunPolicySpec{Identity: id, Level: AutonomyScoped, AllowedTools: []string{"read_file", "send_email"}, Grants: []ScopedGrant{{ID: uuid.New(), Identity: id, ActionDigest: digest, ExpiresAt: now.Add(time.Hour)}}})
	if err != nil {
		t.Fatal(err)
	}
	base := WithBotRunIdentity(botTestPrincipal(t), id)
	child, err := p.Narrow([]string{"read_file"})
	if err != nil {
		t.Fatal(err)
	}
	grandchild, err := child.Narrow([]string{"read_file", "send_email"})
	if err != nil {
		t.Fatal(err)
	}
	if got := EvaluateRunPolicy(WithRunPolicy(base, p), action, now); got.Kind != PolicyConsumeGrant {
		t.Fatalf("parent changed: %+v", got)
	}
	for _, narrowed := range []*RunPolicy{child, grandchild} {
		ctx := WithRunPolicy(base, narrowed)
		if got := EvaluateRunPolicy(ctx, action, now); got.Kind != PolicyRequestApproval {
			t.Fatalf("child inherited grant: %+v", got)
		}
		for _, effect := range []ActionEffect{EffectUnknown, EffectSend, EffectSpend, EffectDelete, EffectPublish, EffectSecurity, EffectSchedule} {
			candidate := action
			candidate.Effect = effect
			if got := EvaluateRunPolicy(ctx, candidate, now); got.Kind != PolicyDeny {
				t.Fatalf("child effect %s: %+v", effect, got)
			}
		}
		candidate := action
		candidate.Tool = "send_email"
		candidate.ConstraintsVerified = true
		if got := EvaluateRunPolicy(ctx, candidate, now); got.Kind != PolicyDeny {
			t.Fatalf("child widened: %+v", got)
		}
	}
}
