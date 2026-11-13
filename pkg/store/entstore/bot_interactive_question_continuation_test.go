package entstore

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	botdomain "github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestInteractiveContinuationExpiredQuestion(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, status := range []store.BotStatus{store.BotDraft, store.BotPaused} {
			t.Run(string(scope)+"/"+string(status), func(t *testing.T) {
				repo, _, ctx, _, _ := runtimeFixture(t, scope)
				p, _ := execution.PrincipalFromContext(ctx)
				v := store.DefaultBotVersion()
				v.Charter = "Answer safely"
				b, err := repo.CreateDraft(ctx, store.BotCreateRequest{Name: "Question", OwnerID: uuid.MustParse(p.Subject), Version: v, IdempotencyKey: "question"})
				if err != nil {
					t.Fatal(err)
				}
				expiry := time.Now().Add(200 * time.Millisecond)
				ctx, err = execution.WithAuthenticationEvidence(ctx, execution.AuthenticationEvidence{Method: p.Authentication, ExpiresAt: expiry})
				if err != nil {
					t.Fatal(err)
				}
				registry := execution.NewBotTurnAuthorizationRegistry(10)
				ctx = store.WithBotMessageAdmission(ctx, registry)
				receipt, err := repo.AdmitMessage(ctx, store.BotMessageRequest{BotID: b.ID, Version: b.Version, IdempotencyKey: "question-turn", Instructions: "Ask"})
				if err != nil {
					t.Fatal(err)
				}
				run := receipt.Run
				ctx = execution.WithBotRunIdentity(ctx, execution.BotRunIdentity{BotID: b.ID, RunID: run.ID, Version: run.Version, ActorID: p.Subject, OrgSlug: p.OrgSlug, TeamSlug: p.TeamSlug})
				lease, err := repo.ClaimRun(ctx, store.BotClaimRequest{BotID: b.ID, RunID: run.ID, WorkerID: "question-worker", ExpiresAt: time.Now().Add(time.Minute)})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := repo.RequireInteractiveContinuation(ctx, b.ID, run.ID); err == nil {
					t.Fatal("interrupted active lease")
				}
				payload := json.RawMessage(`{"questionId":"q","kind":"yesno","prompt":"Continue?"}`)
				action := execution.ActionDescriptor{Tool: "ask_user", Effect: execution.EffectRead, Arguments: payload, ConstraintsVerified: true}
				digest, _ := action.Digest()
				a, err := repo.RequestApproval(ctx, lease, store.BotApproval{BotID: b.ID, RunID: run.ID, Version: run.Version, Action: action, ActionDigest: digest, Interaction: &store.BotInteraction{Kind: store.BotInteractionQuestion, QuestionID: "q", Payload: payload}, ExpiresAt: time.Now().Add(24 * time.Hour)})
				if err != nil {
					t.Fatal(err)
				}
				checkpoint, _ := json.Marshal(botdomain.ApprovalCheckpoint{Format: botdomain.InteractionCheckpointFormat, ApprovalID: a.ID, BotID: b.ID, RunID: run.ID, SessionID: run.SessionID, Version: run.Version, ActionDigest: digest, Fence: lease.Fence})
				work, err := repo.LoadWork(ctx, lease)
				if err != nil {
					t.Fatal(err)
				}
				if err := repo.CommitRun(ctx, store.BotRunCommit{Lease: lease, Status: store.RunWaitingApproval, Checkpoint: checkpoint, Report: store.BotReport{Work: store.BuildBotWorkProjection(work), Summary: "Waiting", NeedsApproval: []uuid.UUID{a.ID}}}); err != nil {
					t.Fatal(err)
				}
				// Advance beyond the original authentication lifetime, not the question TTL.
				// A newly authenticated request cannot implicitly renew the old turn.
				id := execution.BotRunIdentity{BotID: b.ID, RunID: run.ID, Version: run.Version, ActorID: p.Subject, OrgSlug: p.OrgSlug, TeamSlug: p.TeamSlug}
				time.Sleep(time.Until(expiry) + time.Millisecond)
				ctx, err = execution.WithAuthenticationEvidence(ctx, execution.AuthenticationEvidence{Method: p.Authentication, ExpiresAt: time.Now().Add(time.Hour)})
				if err != nil {
					t.Fatal(err)
				}
				response := &store.BotInteractionResponse{QuestionID: "q", Answer: "yes"}
				if _, err := repo.DecideAndRequeueApprovedRun(ctx, store.BotApproveContinueRequest{BotID: b.ID, ApprovalID: a.ID, ActorID: b.OwnerID, ActionDigest: digest, Response: response}); err == nil {
					t.Fatal("answer implicitly renewed lost authority")
				}
				pending, err := repo.db.ReadApproval(ctx, b.ID, a.ID)
				if err != nil || pending.Status != store.ApprovalPending {
					t.Fatalf("decision escaped denial: %+v %v", pending, err)
				}
				if status == store.BotPaused {
					switch db := repo.db.(type) {
					case personalBotDB:
						err = db.client.Bot.UpdateOneID(b.ID).SetStatus("paused").Exec(ctx)
					case teamBotDB:
						err = db.client.Bot.UpdateOneID(b.ID).SetStatus("paused").Exec(ctx)
					}
					if err != nil {
						t.Fatal(err)
					}
				}
				blocked, err := repo.RequireInteractiveContinuation(ctx, b.ID, run.ID)
				if err != nil || blocked.Status != store.RunWaitingApproval || blocked.ContinuationReason != store.BotContinuationRequired || blocked.Lease.Fence != lease.Fence || string(blocked.Checkpoint) != string(checkpoint) {
					t.Fatalf("question or fence lost: %+v %v", blocked, err)
				}
				durable, err := repo.db.ReadRun(ctx, b.ID, run.ID)
				if err != nil || durable.Run.ContinuationReason != store.BotContinuationRequired || durable.Run.Status != store.RunWaitingApproval || durable.Run.Lease.Fence != lease.Fence {
					t.Fatalf("question continuation reason not durable: %+v %v", durable, err)
				}
				q := store.BotContinueInteractiveRequest{BotID: b.ID, RunID: run.ID, ExpectedRevision: b.Revision, IdempotencyKey: "explicit", Response: response}
				continued, err := repo.ContinueInteractive(ctx, q)
				if err != nil {
					t.Fatal(err)
				}
				if continued.Status != store.RunQueued || continued.ContinuationReason != "" || !reflect.DeepEqual(run.CallerCeiling.Principal, continued.CallerCeiling.Principal) {
					t.Fatalf("ceiling/status changed: %+v", continued)
				}
				fresh, err := repo.ClaimRun(ctx, store.BotClaimRequest{BotID: b.ID, RunID: run.ID, WorkerID: "resume", ExpiresAt: time.Now().Add(time.Minute)})
				if err != nil {
					t.Fatal(err)
				}
				if fresh.Fence <= lease.Fence {
					t.Fatal("fence reused")
				}
				if _, err := repo.ClaimRun(ctx, store.BotClaimRequest{BotID: b.ID, RunID: run.ID, WorkerID: "duplicate", ExpiresAt: time.Now().Add(time.Minute)}); err == nil {
					t.Fatal("resumed twice")
				}
				if _, err := repo.RenewLease(ctx, fresh, time.Now().Add(2*time.Minute)); err != nil {
					t.Fatal(err)
				}
				if _, err := repo.LoadResumeApproval(ctx, fresh); err != nil {
					t.Fatal(err)
				}
				if err := repo.CommitRun(ctx, store.BotRunCommit{Lease: fresh, Status: store.RunSucceeded, Report: acceptedWorkReport(t, repo, ctx, fresh, "Answered")}); err != nil {
					t.Fatal(err)
				}
				registry.Cancel(id)
				if _, err := repo.ContinueInteractive(ctx, q); err != nil {
					t.Fatal(err)
				}
				if _, err := registry.Resolve(id); err == nil {
					t.Fatal("duplicate renewed authority")
				}
				current, _ := repo.Get(ctx, b.ID)
				if current.Status != status {
					t.Fatal("activated bot")
				}
			})
		}
	}
}
