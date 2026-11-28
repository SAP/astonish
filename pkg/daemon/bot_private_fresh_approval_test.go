package daemon

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// The real resume repository rejects an empty checkpoint with ErrBotInvalid.
// Fresh actions must not call that resume-only contract.
func TestPrivateInteractiveFreshApprovalAdmission(t *testing.T) {
	for _, variant := range []string{"fresh", "invalid_checkpoint", "missing_saved_approval", "expired_caller", "revoked_caller"} {
		t.Run(variant, func(t *testing.T) {
			owner := uuid.New()
			p := execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodOAuth, Surface: execution.SurfaceStudio, Subject: owner.String(), OrgSlug: "org", TeamSlug: "team", Authenticated: true, Scopes: []string{"chat", "tool:execute"}}
			ctx, err := execution.WithPrincipal(context.Background(), p)
			if err != nil {
				t.Fatal(err)
			}
			expiry := time.Now().Add(time.Minute)
			if variant == "expired_caller" {
				expiry = time.Now().Add(10 * time.Millisecond)
			}
			ctx, err = execution.WithAuthenticationEvidence(ctx, execution.AuthenticationEvidence{Method: p.Authentication, ExpiresAt: expiry})
			if err != nil {
				t.Fatal(err)
			}
			id := execution.BotRunIdentity{BotID: uuid.New(), RunID: uuid.New(), Version: 1, ActorID: p.Subject, OrgSlug: p.OrgSlug, TeamSlug: p.TeamSlug}
			registry := execution.NewBotTurnAuthorizationRegistry(1)
			defer registry.Close()
			authority, err := registry.Register(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			auth, err := registry.Authorizer(id, authority.CallerCeiling(), func(context.Context, execution.Principal) (execution.Principal, error) { return p, nil })
			if err != nil {
				t.Fatal(err)
			}
			budget, err := bot.NewRunBudget(store.BudgetSpec{MaxRuntimeSeconds: 60}, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			run := store.BotRun{ID: id.RunID, BotID: id.BotID, Version: 1, SessionID: uuid.New(), Status: store.RunRunning, Intent: store.BotIntentConversation, ExecutionMode: execution.BotExecutionModeChatInteractive, Lease: store.BotLease{BotID: id.BotID, RunID: id.RunID, Fence: 2, WorkerID: "worker", ExpiresAt: time.Now().Add(time.Minute)}}
			req := bot.RunRequest{Principal: p, Bot: store.Bot{ID: id.BotID, OwnerID: owner, Status: store.BotPaused, Version: 1}, Version: store.BotVersion{Version: 1}, Run: run, Budget: budget, Authorization: auth}
			repo := &employeeLiveGuardRepo{workerEventRepo: &workerEventRepo{b: req.Bot, r: run}, loadErr: store.ErrBotInvalid}
			action := execution.ActionDescriptor{Tool: "fixture_send", Arguments: json.RawMessage(`{"body":"exact"}`), Effect: execution.EffectUnknown}
			switch variant {
			case "invalid_checkpoint":
				req.Run.Checkpoint = json.RawMessage(`{}`)
			case "missing_saved_approval":
				digest, _ := action.Digest()
				req.Run.Checkpoint, _ = json.Marshal(bot.ApprovalCheckpoint{Format: bot.ApprovalCheckpointFormat, ApprovalID: uuid.New(), BotID: id.BotID, RunID: id.RunID, SessionID: run.SessionID, Version: 1, ActionDigest: digest, Fence: 1})
				repo.loadErr = store.ErrBotNotFound
			case "expired_caller":
				time.Sleep(time.Until(expiry) + time.Millisecond)
			case "revoked_caller":
				registry.Cancel(id)
			}
			ctx = agent.WithBotPrivateWorkerExecution(execution.WithBotRunIdentity(ctx, id))
			decision, err := (botLiveGuard{repo: repo, request: req}).Authorize(ctx, action)
			if variant == "fresh" {
				digest, _ := action.Digest()
				if err != nil || decision.Kind != execution.PolicyRequestApproval || decision.ActionDigest != digest {
					t.Fatalf("decision=%+v err=%v", decision, err)
				}
				if repo.loadedLease.WorkerID != "" {
					t.Fatal("fresh action called resume-only repository")
				}
			} else if err == nil || decision.Kind == execution.PolicyAllow || decision.Kind == execution.PolicyRequestApproval {
				t.Fatalf("invalid saved authority admitted: %+v %v", decision, err)
			}
		})
	}
}
