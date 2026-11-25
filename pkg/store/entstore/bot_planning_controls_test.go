package entstore

import (
	"errors"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestBotPlanningDurableOwnerMode(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			repo, _, ctx, _, _ := runtimeFixture(t, scope)
			p, _ := execution.PrincipalFromContext(ctx)
			v := store.DefaultBotVersion()
			v.Charter = "Plan changes safely"
			b, err := repo.CreateDraft(ctx, store.BotCreateRequest{Name: "Planning", OwnerID: uuid.MustParse(p.Subject), Version: v, IdempotencyKey: "planning"})
			if err != nil {
				t.Fatal(err)
			}
			ctx, err = execution.WithAuthenticationEvidence(ctx, execution.AuthenticationEvidence{Method: p.Authentication, ExpiresAt: time.Now().Add(time.Hour)})
			if err != nil {
				t.Fatal(err)
			}
			registry := execution.NewBotTurnAuthorizationRegistry(10)
			defer registry.Close()
			ctx = store.WithBotMessageAdmission(ctx, registry)
			mode := true
			request := store.BotMessageRequest{BotID: b.ID, Version: b.Version, IdempotencyKey: "plan", Instructions: "Plan the change", PlanMode: &mode}
			receipt, err := repo.AdmitMessage(ctx, request)
			if err != nil {
				t.Fatal(err)
			}
			input, err := repo.LoadRunInput(ctx, b.ID, receipt.Run.ID)
			if err != nil || !input.PlanMode {
				t.Fatalf("lost durable planning mode: %+v %v", input, err)
			}
			saved, err := repo.db.ReadRun(ctx, b.ID, receipt.Run.ID)
			if err != nil || !saved.PlanMode {
				t.Fatalf("private codec lost mode: %+v %v", saved, err)
			}
			// A new owner turn does not inherit planning from Home conversation history.
			fresh, err := repo.AdmitMessage(ctx, store.BotMessageRequest{BotID: b.ID, Version: b.Version, IdempotencyKey: "normal", Instructions: "An unrelated question"})
			if err != nil {
				t.Fatal(err)
			}
			freshInput, err := repo.LoadRunInput(ctx, b.ID, fresh.Run.ID)
			if err != nil || freshInput.PlanMode {
				t.Fatalf("new turn inherited mode: %+v %v", freshInput, err)
			}
			// The original run keeps its mode until an explicit, safe continuation changes it.
			identity := execution.BotRunIdentity{BotID: b.ID, RunID: receipt.Run.ID, Version: b.Version, ActorID: p.Subject, OrgSlug: p.OrgSlug, TeamSlug: p.TeamSlug}
			registry.Cancel(identity)
			if _, err := repo.RequireInteractiveContinuation(ctx, b.ID, receipt.Run.ID); err != nil {
				t.Fatal(err)
			}
			mode = false
			q := store.BotContinueInteractiveRequest{BotID: b.ID, RunID: receipt.Run.ID, ExpectedRevision: b.Revision, IdempotencyKey: "normal-continuation", PlanMode: &mode}
			continued, err := repo.ContinueInteractive(ctx, q)
			if err != nil || continued.ID != receipt.Run.ID || continued.SessionID != receipt.Run.SessionID {
				t.Fatalf("continuation changed identity: %+v %v", continued, err)
			}
			input, err = repo.LoadRunInput(ctx, b.ID, receipt.Run.ID)
			if err != nil || input.PlanMode {
				t.Fatalf("explicit false lost: %+v %v", input, err)
			}
			// Idempotency binds the optional mode, including explicit false.
			mode = true
			if _, err := repo.ContinueInteractive(ctx, q); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("mode-changing replay: %v", err)
			}
			forged := p
			forged.Subject = uuid.NewString()
			forgedCtx, err := execution.WithPrincipal(ctx, forged)
			if err != nil {
				t.Fatal(err)
			}
			request.IdempotencyKey = "forged"
			if _, err := repo.AdmitMessage(forgedCtx, request); !errors.Is(err, store.ErrBotForbidden) {
				t.Fatalf("nonowner mode: %v", err)
			}
			q.IdempotencyKey = "definition-consent"
			q.ApprovedPlanExecution = true
			q.PlanMode = nil
			if _, err := repo.ContinueInteractive(ctx, q); err == nil {
				t.Fatal("missing plan consent accepted")
			}
			q.PlanExecution = &store.BotPlanExecutionConsent{RunID: receipt.Run.ID, SessionID: receipt.Run.SessionID.String(), PlanID: "stale", Revision: 1, Sequence: 1}
			if _, err := repo.ContinueInteractive(ctx, q); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("unsupported originating continuation: %v", err)
			}
			q.PlanExecution.SessionID = uuid.NewString()
			if _, err := repo.ContinueInteractive(ctx, q); !errors.Is(err, store.ErrBotInvalid) {
				t.Fatalf("different session consent: %v", err)
			}
			q.PlanExecution.SessionID = receipt.Run.SessionID.String()
			request.PlanExecution = q.PlanExecution
			request.ApprovedPlanExecution = true
			request.IdempotencyKey = "imported-plan"
			if _, err := repo.AdmitMessage(ctx, request); !errors.Is(err, store.ErrBotInvalid) {
				t.Fatalf("new run imported consent: %v", err)
			}
		})
	}
}
