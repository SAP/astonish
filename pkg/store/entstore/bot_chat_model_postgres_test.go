//go:build integration

package entstore

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	botdomain "github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// Real PostgreSQL, both tenant scopes, fail closed rather than silently skip.
func TestIntegrationPostgresBotChatModel(t *testing.T) {
	requireDraftActivationPostgres(t)
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			f := newPostgresBotServicesFixture(t, scope, 100, 100)
			p, _ := execution.PrincipalFromContext(f.owner)
			ctx, err := execution.WithAuthenticationEvidence(f.owner, execution.AuthenticationEvidence{Method: p.Authentication, ExpiresAt: time.Now().Add(time.Hour)})
			if err != nil {
				t.Fatal(err)
			}
			registry := execution.NewBotTurnAuthorizationRegistry(10)
			defer registry.Close()
			ctx = store.WithBotMessageAdmission(ctx, registry)
			v := store.DefaultBotVersion()
			v.Charter = "Research an arbitrary title"
			q := store.BotCreateRequest{Name: v.Charter, OwnerID: uuid.MustParse(p.Subject), Version: v, IdempotencyKey: "pg-chat-model"}
			first, err := f.repo.CreateDraftAndStart(ctx, q)
			if err != nil {
				t.Fatal(err)
			}
			run := first.Run
			if first.Status != store.BotDraft || !run.IsInteractiveChat() || run.CallerCeiling == nil {
				t.Fatal("title did not start bounded interactive draft")
			}
			f.reopenReviewed(t)
			again, err := f.repo.CreateDraftAndStart(ctx, q)
			if err != nil || again.ID != first.ID || again.Run.ID != run.ID || again.UserEventID != first.UserEventID {
				t.Fatalf("reopen replay: %+v %v", again, err)
			}
			q.Name += " changed"
			if _, err := f.repo.CreateDraftAndStart(ctx, q); err != store.ErrBotConflict {
				t.Fatalf("changed replay: %v", err)
			}
			saved, err := f.repo.db.ReadRun(ctx, first.ID, run.ID)
			if err != nil || !reflect.DeepEqual(saved.Run.CallerCeiling.Principal, run.CallerCeiling.Principal) || !saved.Run.IsInteractiveChat() {
				t.Fatalf("durable authority: %v", err)
			}
			public, err := json.Marshal(saved.Run)
			if err != nil {
				t.Fatal(err)
			}
			for _, forbidden := range []string{"caller_ceiling", "CallerCeiling", "authentication", "principal"} {
				if strings.Contains(string(public), forbidden) {
					t.Fatalf("public run leaks %s", forbidden)
				}
			}
			snap, err := f.bots.(store.BotSnapshotReader).Snapshot(ctx, first.ID)
			if err != nil || len(snap.RunStates) != 1 || snap.RunStates[0].Status != store.RunQueued {
				t.Fatalf("queued snapshot: %+v %v", snap, err)
			}
			id := execution.BotRunIdentity{BotID: first.ID, RunID: run.ID, Version: run.Version, ActorID: p.Subject, OrgSlug: p.OrgSlug, TeamSlug: p.TeamSlug}
			ctx = execution.WithBotRunIdentity(ctx, id)
			lease, err := f.repo.ClaimRun(ctx, store.BotClaimRequest{BotID: first.ID, RunID: run.ID, WorkerID: "pg-chat", ExpiresAt: time.Now().Add(time.Minute)})
			if err != nil {
				t.Fatal(err)
			}
			payload := json.RawMessage(`{"questionId":"pg-custom","kind":"select","prompt":"Where?","allow_custom_answer":true,"options":[{"id":"local","label":"Local"}]}`)
			action := execution.ActionDescriptor{Tool: "ask_user", Effect: execution.EffectRead, Arguments: payload, ConstraintsVerified: true}
			digest, _ := action.Digest()
			approval, err := f.repo.RequestApproval(ctx, lease, store.BotApproval{BotID: first.ID, RunID: run.ID, Version: run.Version, Action: action, ActionDigest: digest, Interaction: &store.BotInteraction{Kind: store.BotInteractionQuestion, QuestionID: "pg-custom", Payload: payload}, ExpiresAt: time.Now().Add(time.Hour)})
			if err != nil {
				t.Fatal(err)
			}
			checkpoint, _ := json.Marshal(botdomain.ApprovalCheckpoint{Format: botdomain.InteractionCheckpointFormat, ApprovalID: approval.ID, BotID: first.ID, RunID: run.ID, SessionID: run.SessionID, Version: run.Version, ActionDigest: digest, Fence: lease.Fence})
			if err := f.repo.CommitRun(ctx, store.BotRunCommit{Lease: lease, Status: store.RunWaitingApproval, Checkpoint: checkpoint, Report: store.BotReport{Summary: "Waiting for location", NeedsApproval: []uuid.UUID{approval.ID}}}); err != nil {
				t.Fatal(err)
			}
			f.reopenReviewed(t)
			registry.Cancel(id)
			response := &store.BotInteractionResponse{QuestionID: "pg-custom", Answer: "A custom location"}
			if _, err := f.repo.DecideAndRequeueApprovedRun(ctx, store.BotApproveContinueRequest{BotID: first.ID, ApprovalID: approval.ID, ActorID: first.OwnerID, ActionDigest: digest, Response: response}); err == nil {
				t.Fatal("revoked registry implicitly renewed")
			}
			pending, err := f.repo.db.ReadApproval(ctx, first.ID, approval.ID)
			if err != nil || pending.Status != store.ApprovalPending {
				t.Fatalf("denied answer mutated approval: %+v %v", pending, err)
			}
			blocked, err := f.repo.RequireInteractiveContinuation(ctx, first.ID, run.ID)
			if err != nil || blocked.Status != store.RunWaitingApproval || blocked.ContinuationReason != store.BotContinuationRequired {
				t.Fatalf("continuation state: %+v %v", blocked, err)
			}
			continuation := store.BotContinueInteractiveRequest{BotID: first.ID, RunID: run.ID, ExpectedRevision: first.Revision, IdempotencyKey: "explicit-pg", Response: response}
			continued, err := f.repo.ContinueInteractive(ctx, continuation)
			if err != nil || continued.ID != run.ID || continued.Status != store.RunQueued {
				t.Fatalf("explicit continuation: %+v %v", continued, err)
			}
			f.reopenReviewed(t)
			if _, err := f.repo.ContinueInteractive(ctx, continuation); err != nil {
				t.Fatal(err)
			}
			fresh, err := f.repo.ClaimRun(ctx, store.BotClaimRequest{BotID: first.ID, RunID: run.ID, WorkerID: "pg-resume", ExpiresAt: time.Now().Add(time.Minute)})
			if err != nil || fresh.Fence <= lease.Fence {
				t.Fatalf("fresh fence: %+v %v", fresh, err)
			}
			if _, err := f.repo.ClaimRun(ctx, store.BotClaimRequest{BotID: first.ID, RunID: run.ID, WorkerID: "duplicate", ExpiresAt: time.Now().Add(time.Minute)}); err == nil {
				t.Fatal("duplicate claimant")
			}
			if _, err := f.repo.LoadResumeApproval(ctx, fresh); err != nil {
				t.Fatal(err)
			}
			if err := f.repo.CommitRun(ctx, store.BotRunCommit{Lease: fresh, Status: store.RunSucceeded, Report: acceptedWorkReport(t, f.repo, ctx, fresh, "Custom answer resumed once")}); err != nil {
				t.Fatal(err)
			}
			f.reopenReviewed(t)
			if _, err := f.repo.ContinueInteractive(ctx, continuation); err != nil {
				t.Fatal(err)
			}
			runs, err := f.repo.ListRuns(ctx, first.ID, store.BotPage{})
			if err != nil || len(runs) != 1 || runs[0].Status != store.RunSucceeded {
				t.Fatalf("exactly one durable turn: %+v %v", runs, err)
			}
			current, err := f.repo.Get(ctx, first.ID)
			if err != nil || current.Status != store.BotDraft {
				t.Fatal("interactive continuation activated autonomous work")
			}
		})
	}
}
