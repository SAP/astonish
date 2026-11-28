package launcher

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/google/uuid"
)

func TestBotSkillReadClassificationPreservesPolicyCeilings(t *testing.T) {
	id := execution.BotRunIdentity{BotID: uuid.New(), RunID: uuid.New(), Version: 1, ActorID: uuid.NewString(), OrgSlug: "org", TeamSlug: "team"}
	ctx, err := execution.WithPrincipal(context.Background(), execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: id.ActorID, OrgSlug: id.OrgSlug, TeamSlug: id.TeamSlug, Authenticated: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx = execution.WithBotRunIdentity(ctx, id)
	for _, name := range []string{"bot_skill_list", "bot_skill_read"} {
		t.Run(name, func(t *testing.T) {
			raw := json.RawMessage(`{"skill_id":"` + uuid.NewString() + `"}`)
			a, err := (BotCatalogueClassifier{}).DescribeAction(ctx, name, raw)
			if err != nil || a.Effect != execution.EffectRead || !a.ConstraintsVerified || a.Tool != name || string(a.Arguments) != string(raw) {
				t.Fatalf("classification %+v %v", a, err)
			}
			for _, tc := range []struct {
				name            string
				allowed, denied []string
				want            execution.PolicyDecisionKind
			}{
				{"owned", []string{name}, nil, execution.PolicyAllow},
				{"not-allowed", nil, nil, execution.PolicyDeny},
				{"explicitly-denied", []string{name}, []string{name}, execution.PolicyDeny},
			} {
				p, err := execution.NewRunPolicy(execution.RunPolicySpec{Identity: id, Level: execution.AutonomyScoped, AllowedTools: tc.allowed, DeniedTools: tc.denied})
				if err != nil {
					t.Fatal(err)
				}
				got := execution.EvaluateRunPolicy(execution.WithRunPolicy(ctx, p), a, time.Now())
				if got.Kind != tc.want {
					t.Fatalf("%s: %+v", tc.name, got)
				}
			}
			for _, group := range []string{"mcp:untrusted", "request", "a2a"} {
				foreign, err := (BotCatalogueClassifier{}).DescribeAction(ctx, group+"/"+name, raw)
				if err != nil || foreign.ConstraintsVerified || foreign.Effect != execution.EffectUnknown {
					t.Fatalf("foreign tool borrowed read authority: %+v %v", foreign, err)
				}
			}
		})
	}
	for _, name := range []string{"bot_skill_use", "bot_skill_save"} {
		a, err := (BotCatalogueClassifier{}).DescribeAction(ctx, name, json.RawMessage(`{}`))
		if err != nil || a.Effect != execution.EffectReversibleWrite {
			t.Fatalf("mutation relabeled as read: %+v %v", a, err)
		}
	}
}
