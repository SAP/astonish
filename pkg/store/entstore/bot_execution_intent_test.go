package entstore

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
	"testing"
	"time"
)

func TestBotExecutionIntentsDurableLease(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, intent := range []store.BotExecutionIntent{store.BotIntentDraftTest, store.BotIntentConversation} {
			t.Run(string(scope)+"/"+string(intent), func(t *testing.T) {
				repo, _, ctx, initial, _ := runtimeFixture(t, scope)
				p, _ := execution.PrincipalFromContext(ctx)
				v := store.DefaultBotVersion()
				v.Charter = "Explicit single-run consent"
				b, err := repo.CreateDraft(ctx, store.BotCreateRequest{Name: "Intent", OwnerID: uuid.MustParse(p.Subject), Version: v, IdempotencyKey: initial.ID.String() + "intent"})
				if err != nil {
					t.Fatal(err)
				}
				repo.activationCheck = func(context.Context, store.Bot, store.BotVersion) error { return nil }
				if intent == store.BotIntentConversation {
					// Establish paused state via the same fixture's already activated Bot.
					b, err = repo.Get(ctx, initial.BotID)
					if err != nil {
						t.Fatal(err)
					}
					b, err = repo.Pause(ctx, store.BotLifecycleRequest{BotID: b.ID, ActorID: b.OwnerID, ExpectedRevision: b.Revision, Reason: "Paused conversation"})
					if err != nil {
						t.Fatal(err)
					}
				}
				q := store.BotRunRequest{Intent: intent, BotID: b.ID, Version: b.Version, Trigger: store.TriggerManual, IdempotencyKey: "consent", Instructions: "Reply once"}
				stale := q
				stale.Version++
				if _, err := repo.RequestRun(ctx, stale); !errors.Is(err, store.ErrBotConflict) {
					t.Fatalf("unreviewed version admitted: %v", err)
				}
				other := p
				other.Subject = uuid.NewString()
				foreign, err := execution.WithPrincipal(context.Background(), other)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := repo.RequestRun(foreign, q); err == nil {
					t.Fatal("non-owner admitted")
				}
				run, err := repo.RequestRun(ctx, q)
				if err != nil {
					t.Fatalf("explicit %s admission: %v", intent, err)
				}
				if run.Intent != intent {
					t.Fatal("lost intent")
				}
				again, err := repo.RequestRun(ctx, q)
				if err != nil || again.ID != run.ID {
					t.Fatal("non-idempotent", err)
				}
				q.Intent = store.BotIntentRegular
				if _, err := repo.RequestRun(ctx, q); !errors.Is(err, store.ErrBotConflict) {
					t.Fatalf("intent collision: %v", err)
				}
				c := execution.WithBotRunIdentity(ctx, execution.BotRunIdentity{BotID: b.ID, RunID: run.ID, Version: b.Version, ActorID: p.Subject, OrgSlug: p.OrgSlug, TeamSlug: p.TeamSlug})
				l, err := repo.ClaimRun(c, store.BotClaimRequest{BotID: b.ID, RunID: run.ID, WorkerID: "intent-worker", ExpiresAt: time.Now().Add(time.Minute)})
				if err != nil {
					t.Fatal("claim", err)
				}
				secondRequest := q
				secondRequest.Intent = intent
				secondRequest.IdempotencyKey = "second-consent"
				second, err := repo.RequestRun(ctx, secondRequest)
				if err != nil {
					t.Fatal("second admission", err)
				}
				secondCtx := execution.WithBotRunIdentity(ctx, execution.BotRunIdentity{BotID: b.ID, RunID: second.ID, Version: b.Version, ActorID: p.Subject, OrgSlug: p.OrgSlug, TeamSlug: p.TeamSlug})
				if _, err := repo.ClaimRun(secondCtx, store.BotClaimRequest{BotID: b.ID, RunID: second.ID, WorkerID: "other-worker", ExpiresAt: time.Now().Add(time.Minute)}); !errors.Is(err, store.ErrBotConflict) {
					t.Fatalf("parallel home writer claimed: %v", err)
				}
				l, err = repo.RenewLease(c, l, time.Now().Add(2*time.Minute))
				if err != nil {
					t.Fatal("renew", err)
				}
				if intent == store.BotIntentConversation {
					action := execution.ActionDescriptor{Tool: "send", Effect: execution.EffectSend, Arguments: json.RawMessage(`{}`)}
					digest, _ := action.Digest()
					if _, err := repo.RequestApproval(c, l, store.BotApproval{BotID: b.ID, RunID: run.ID, Version: b.Version, Action: action, ActionDigest: digest, ExpiresAt: time.Now().UTC().Add(time.Hour)}); !errors.Is(err, store.ErrBotForbidden) {
						t.Fatalf("conversation requested tool approval: %v", err)
					}
				}
				if intent == store.BotIntentDraftTest {
					action := execution.ActionDescriptor{Tool: "send", Effect: execution.EffectSend, Arguments: json.RawMessage(`{"body":"exact"}`)}
					digest, _ := action.Digest()
					a, err := repo.RequestApproval(c, l, store.BotApproval{BotID: b.ID, RunID: run.ID, Version: b.Version, Action: action, ActionDigest: digest, ExpiresAt: time.Now().In(time.FixedZone("approval-owner", -5*60*60)).Add(time.Hour)})
					if err != nil {
						t.Fatal("draft approval", err)
					}
					checkpoint, _ := json.Marshal(bot.ApprovalCheckpoint{Format: bot.ApprovalCheckpointFormat, ApprovalID: a.ID, BotID: b.ID, RunID: run.ID, SessionID: run.SessionID, Version: b.Version, ActionDigest: digest, Fence: l.Fence})
					if err := repo.CommitRun(c, store.BotRunCommit{Lease: l, Status: store.RunWaitingApproval, Checkpoint: checkpoint, Report: store.BotReport{Summary: "Waiting", NeedsApproval: []uuid.UUID{a.ID}}}); err != nil {
						t.Fatal(err)
					}
					if err := repo.RequeueApprovedRun(ctx, b.ID, run.ID, a.ID); !errors.Is(err, store.ErrBotConflict) {
						t.Fatal("unapproved test resumed", err)
					}
					if _, err := repo.DecideApproval(ctx, store.BotApprovalDecision{BotID: b.ID, ApprovalID: a.ID, ActorID: b.OwnerID, ActionDigest: digest, Status: store.ApprovalApproved}); err != nil {
						current, _ := repo.Get(ctx, b.ID)
						approvals, _ := repo.ListApprovals(ctx, b.ID, store.BotPage{Limit: 20})
						t.Fatalf("decide: %v current=%+v approval=%+v", err, current, approvals)
					}
					if err := repo.RequeueApprovedRun(ctx, b.ID, run.ID, a.ID); err != nil {
						t.Fatal("draft resume", err)
					}
					old := l
					l, err = repo.ClaimRun(c, store.BotClaimRequest{BotID: b.ID, RunID: run.ID, WorkerID: "resumed-worker", ExpiresAt: time.Now().Add(time.Minute)})
					if err != nil {
						t.Fatal("draft resumed claim", err)
					}
					if _, err := repo.LoadResumeApproval(c, old); err == nil {
						t.Fatal("stale approval lease")
					}
					if _, err := repo.LoadResumeApproval(c, l); err != nil {
						t.Fatal("draft load approval", err)
					}
					if err := repo.ConsumeApproval(c, store.BotApprovalConsumption{Lease: l, ApprovalID: a.ID, Version: b.Version, ActionDigest: digest}); err != nil {
						t.Fatal("draft consume", err)
					}
				}
				if err := repo.CommitRun(c, store.BotRunCommit{Lease: l, Status: store.RunSucceeded, Report: store.BotReport{Summary: "Reply"}}); err != nil {
					t.Fatal("commit", err)
				}
				after, err := repo.Get(ctx, b.ID)
				if err != nil || after.Status != b.Status || after.Version != b.Version {
					t.Fatal("lifecycle changed", err)
				}
				if _, err := repo.RenewLease(c, l, time.Now().Add(3*time.Minute)); err == nil {
					t.Fatal("stale lease renewed")
				}
				secondLease, err := repo.ClaimRun(secondCtx, store.BotClaimRequest{BotID: b.ID, RunID: second.ID, WorkerID: "other-worker", ExpiresAt: time.Now().Add(time.Minute)})
				if err != nil {
					t.Fatal("serialized next writer", err)
				}
				event := &session.Event{ID: uuid.NewString(), Author: "user", LLMResponse: model.LLMResponse{Content: genai.NewContentFromText("fenced owner turn", "user")}}
				if err := repo.appendBotRunEvent(secondCtx, secondLease, second.SessionID, event); err != nil {
					t.Fatal("intent transcript", err)
				}
				if intent == store.BotIntentDraftTest {
					_, err = repo.CancelRun(ctx, b.ID, second.ID)
				} else {
					_, err = repo.Stop(ctx, store.BotLifecycleRequest{BotID: b.ID, ActorID: b.OwnerID, ExpectedRevision: after.Revision, Reason: "revoke single-run consent"})
				}
				if err != nil {
					t.Fatal("stop intent", err)
				}
				if err := repo.appendBotRunEvent(secondCtx, secondLease, second.SessionID, event); err == nil {
					t.Fatal("revoked intent wrote home transcript")
				}
				if _, err := repo.RenewLease(secondCtx, secondLease, time.Now().Add(2*time.Minute)); err == nil {
					t.Fatal("stopped intent renewed")
				}
				if err := repo.CommitRun(secondCtx, store.BotRunCommit{Lease: secondLease, Status: store.RunSucceeded, Report: store.BotReport{Summary: "stale"}}); err == nil {
					t.Fatal("stopped intent committed")
				}
			})
		}
	}
}
