package agent

import (
	"context"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	"testing"
	"time"
)

func interactivePolicyContext(t *testing.T, scopes []string) (context.Context, *execution.BotTurnAuthorizationRegistry, execution.BotRunIdentity) {
	t.Helper()
	id := execution.BotRunIdentity{BotID: uuid.New(), RunID: uuid.New(), Version: 1, ActorID: uuid.NewString(), OrgSlug: "org", TeamSlug: "team"}
	p := execution.Principal{Kind: execution.PrincipalKindUser, Subject: id.ActorID, OrgSlug: id.OrgSlug, TeamSlug: id.TeamSlug, Authentication: execution.AuthMethodOAuth, Surface: execution.Surface("api"), Authenticated: true, Scopes: scopes}
	ctx, err := execution.WithPrincipal(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	ctx, err = execution.WithAuthenticationEvidence(ctx, execution.AuthenticationEvidence{Method: p.Authentication, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	registry := execution.NewBotTurnAuthorizationRegistry(2)
	admission, err := registry.Register(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	auth, err := registry.Authorizer(id, admission.CallerCeiling(), func(_ context.Context, original execution.Principal) (execution.Principal, error) {
		return original, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return execution.WithBotTurnAuthorizer(execution.WithBotRunIdentity(ctx, id), auth), registry, id
}
func TestBotInteractiveChatOnlyCannotExecute(t *testing.T) {
	ctx, _, _ := interactivePolicyContext(t, []string{"chat"})
	if err := AuthorizeBotTool(ctx, "shell", nil); err == nil {
		t.Fatal("chat-only caller executed tool")
	}
}
func TestBotInteractiveToolAndAuxiliaryRevocation(t *testing.T) {
	ctx, registry, id := interactivePolicyContext(t, []string{"chat", "tool:execute"})
	if err := AuthorizeBotTool(ctx, "ask_user", nil); err != nil {
		t.Fatal(err)
	}
	probe := &botAuxProbe{}
	ctx = WithBotAuxiliaryLLM(ctx, probe)
	llm := auxiliaryLLM(ctx, nil)
	registry.Cancel(id)
	if err := AuthorizeBotTool(ctx, "shell", nil); err == nil {
		t.Fatal("revoked tool authority")
	}
	for _, err := range llm.GenerateContent(ctx, &model.LLMRequest{}, false) {
		if err == nil {
			t.Fatal("revoked auxiliary authority")
		}
	}
	if probe.calls != 0 {
		t.Fatal("provider called after revocation")
	}
}
