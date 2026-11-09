package bot

import (
	"context"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"testing"
	"time"
)

type interactiveSupervisorStore struct {
	store.BotStore
	input  store.BotRunInput
	claims int
}

func (s *interactiveSupervisorStore) LoadRunInput(context.Context, store.BotID, store.RunID) (*store.BotRunInput, error) {
	return &s.input, nil
}
func (s *interactiveSupervisorStore) ClaimRun(context.Context, store.BotClaimRequest) (store.BotLease, error) {
	s.claims++
	return store.BotLease{}, store.ErrBotUnsupported
}

type interactiveSupervisorAuth struct{}

func (interactiveSupervisorAuth) Authorize(context.Context, execution.Capability) error { return nil }
func (interactiveSupervisorAuth) AuthorizeTool(context.Context, string) error           { return nil }
func TestSupervisorInteractiveRequiresPhysicalHome(t *testing.T) {
	owner := uuid.New()
	id := uuid.New()
	p := execution.Principal{Kind: execution.PrincipalKindUser, Subject: owner.String(), Authentication: execution.AuthMethodPlatformJWT, Surface: execution.Surface("api"), OrgSlug: "org", TeamSlug: "team", Authenticated: true}
	ctx, err := execution.WithPrincipal(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	repo := &interactiveSupervisorStore{input: store.BotRunInput{Bot: store.Bot{ID: id, OwnerID: owner, Version: 1, Status: store.BotDraft}, Version: store.BotVersion{BotID: id, Version: 1, SchemaVersion: store.BotSchemaVersion, Charter: "help", Persona: store.BotPersona{Timezone: "UTC"}, Autonomy: store.BotAutonomy{Level: execution.AutonomyAsk}, Budget: store.BudgetSpec{MaxRuntimeSeconds: 60}, Delivery: store.DeliveryPolicy{Notify: store.NotifyNever}}, Run: store.BotRun{ID: uuid.New(), BotID: id, Version: 1, SessionID: uuid.New(), Status: store.RunQueued, Trigger: store.TriggerManual, Intent: store.BotIntentDraftConversation, ExecutionMode: execution.BotExecutionModeChatInteractive, CallerCeiling: &execution.BotCallerCeiling{Principal: p, ExpiresAt: time.Now().Add(time.Hour)}}, Instructions: "help now"}}
	budget, err := NewRunBudget(repo.input.Version.Budget, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	supervisor := &Supervisor{inputs: repo, config: SupervisorConfig{Store: repo, Ready: func(context.Context, store.BotRunInput) (RunPreparation, error) {
		return RunPreparation{Authorization: interactiveSupervisorAuth{}, Budget: budget}, nil
	}}}
	if _, err := supervisor.Execute(ctx, id, repo.input.Run.ID); err == nil {
		t.Fatal("chat admitted without home")
	}
	if repo.claims != 0 {
		t.Fatal("claimed before verifying physical home")
	}
}
