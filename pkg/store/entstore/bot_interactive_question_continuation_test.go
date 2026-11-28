package entstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"errors"
	personalent "github.com/SAP/astonish/ent/personal"
	teament "github.com/SAP/astonish/ent/team"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	botdomain "github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
)

func TestDurableQuestionAnswerContinuesSameRequest(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, chatResource := range []string{"legacy-chat", "bot-id"} {
			t.Run(string(scope)+"/"+chatResource, func(t *testing.T) {
				owner := uuid.New()
				ctx, err := execution.WithPrincipal(context.Background(), execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: owner.String(), OrgSlug: "org", TeamSlug: "team", Authenticated: true})
				if err != nil {
					t.Fatal(err)
				}
				ctx, err = execution.WithAuthenticationEvidence(ctx, execution.AuthenticationEvidence{Method: execution.AuthMethodPlatformJWT, ExpiresAt: time.Now().Add(time.Hour)})
				if err != nil {
					t.Fatal(err)
				}
				registry := execution.NewBotTurnAuthorizationRegistry(10)
				ctx = store.WithBotMessageAdmission(ctx, registry)
				ctx = store.WithBotMandateAdmission(ctx, questionContinuationChatPolicy(chatResource))
				auth := botRepositoryAuth{orgSlug: "org", teamSlug: "team", userID: owner.String(), scope: scope, check: func(context.Context, string) (string, error) { return "member", nil }}
				if scope == store.BotScopePersonal {
					auth.teamSlug = ""
				}
				path := filepath.Join(t.TempDir(), "question.db")
				var repo *botRepository
				var closeDB func() error
				open := func() {
					db, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(1)")
					if err != nil {
						t.Fatal(err)
					}
					drv := entsql.OpenDB(dialect.SQLite, db)
					content := newReopenBotContentCodec(t, filepath.Join(filepath.Dir(path), "content"), store.BotContentScope{OrgID: "org", TeamID: "team", OwnerID: owner.String()})
					if scope == store.BotScopeTeam {
						c := teament.NewClient(teament.Driver(drv))
						if err := c.Schema.Create(ctx); err != nil {
							t.Fatal(err)
						}
						repo = (&teamBotStore{client: c, content: content, auth: auth}).repository()
						closeDB = c.Close
					} else {
						c := personalent.NewClient(personalent.Driver(drv))
						if err := c.Schema.Create(ctx); err != nil {
							t.Fatal(err)
						}
						repo = (&personalBotStore{client: c, content: content, auth: auth}).repository()
						closeDB = c.Close
					}
				}
				open()
				defer func() { _ = closeDB() }()
				v := store.DefaultBotVersion()
				v.Charter = "Briefing"
				b, err := repo.Create(ctx, store.BotCreateRequest{Name: "Briefing", OwnerID: owner, Version: v, IdempotencyKey: "create"})
				if err != nil {
					t.Fatal(err)
				}
				receipt, err := repo.AdmitMessage(ctx, store.BotMessageRequest{BotID: b.ID, Version: b.Version, IdempotencyKey: "schedule-request", Instructions: "Schedule this briefing every day at 9am"})
				if err != nil {
					t.Fatal(err)
				}
				run := receipt.Run
				id := execution.BotRunIdentity{BotID: b.ID, RunID: run.ID, Version: run.Version, ActorID: owner.String(), OrgSlug: "org", TeamSlug: "team"}
				ctx = execution.WithBotRunIdentity(ctx, id)
				a := savedContinuationQuestion(t, repo, ctx, run)
				before, err := readMandate(ctx, repo.db, *run.MandateRef)
				if err != nil {
					t.Fatal(err)
				}
				if err := closeDB(); err != nil {
					t.Fatal(err)
				}
				open()
				registry.Close()
				ctx = store.WithBotMessageAdmission(ctx, execution.NewBotTurnAuthorizationRegistry(10))
				q := store.BotApproveContinueRequest{BotID: b.ID, ApprovalID: a.ID, ActorID: owner, ActionDigest: a.ActionDigest, Response: &store.BotInteractionResponse{QuestionID: "timezone", Answer: "America/New_York"}}
				result, err := repo.DecideAndRequeueApprovedRun(ctx, q)
				if err != nil {
					t.Fatalf("authenticated saved question required original registry: %v", err)
				}
				if result.RunID != run.ID || result.Continuation != store.BotContinuationQueued {
					t.Fatalf("wrong continuation: %+v", result)
				}
				saved, err := repo.db.ReadRun(ctx, b.ID, run.ID)
				if err != nil {
					t.Fatal(err)
				}
				if saved.Run.Status != store.RunQueued || saved.Run.SessionID != run.SessionID || saved.Run.UserEventID != run.UserEventID || !reflect.DeepEqual(saved.Run.MandateRef, run.MandateRef) || saved.Instructions != "Schedule this briefing every day at 9am" {
					t.Fatalf("request changed: %+v", saved.Run)
				}
				after, err := readMandate(ctx, repo.db, *run.MandateRef)
				if err != nil || !reflect.DeepEqual(before, after) {
					t.Fatalf("mandate changed: %v", err)
				}
				decision, err := repo.db.ReadApproval(ctx, b.ID, a.ID)
				if err != nil || !reflect.DeepEqual(decision.Response, q.Response) {
					t.Fatalf("answer not stored: %v", err)
				}
				rec, _ := repo.db.GetBot(ctx, b.ID)
				if _, err := repo.DecideAndRequeueApprovedRun(ctx, q); err != nil {
					t.Fatal(err)
				}
				replayed, _ := repo.db.GetBot(ctx, b.ID)
				if rec.LastSequence != replayed.LastSequence {
					t.Fatal("duplicate queued twice")
				}
				q.Response = &store.BotInteractionResponse{QuestionID: "timezone", Answer: "Europe/Berlin"}
				if _, err := repo.DecideAndRequeueApprovedRun(ctx, q); !errors.Is(err, store.ErrBotConflict) {
					t.Fatalf("changed replay: %v", err)
				}
			})
		}
	}
}

// Production chat grants use the exact Bot UUID; retain legacy fixture coverage
// without treating other Bots (or arbitrary resources) as question authority.
func questionContinuationChatPolicy(resource string) store.BotMandatePolicyResolver {
	return func(ctx context.Context, ref store.BotMandateRef) (store.BotMandatePolicy, error) {
		policy, err := mandateTestPolicy(ctx, ref)
		if resource == "legacy-chat" {
			return policy, err
		}
		chatResource := resource
		if chatResource == "bot-id" {
			chatResource = ref.BotID.String()
		}
		for i := range policy.Constraints {
			if policy.Constraints[i].Action == execution.CapabilityChat {
				policy.Constraints[i].Resources = []string{chatResource}
			}
		}
		return policy, err
	}
}

func savedContinuationQuestion(t *testing.T, repo *botRepository, ctx context.Context, run store.BotRun) *store.BotApproval {
	t.Helper()
	lease, err := repo.ClaimRun(ctx, store.BotClaimRequest{BotID: run.BotID, RunID: run.ID, WorkerID: "question", ExpiresAt: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	payload := json.RawMessage(`{"questionId":"timezone","kind":"text","prompt":"Which timezone?"}`)
	action := execution.ActionDescriptor{Tool: "ask_user", Effect: execution.EffectRead, Arguments: payload, ConstraintsVerified: true}
	digest, _ := action.Digest()
	a, err := repo.RequestApproval(ctx, lease, store.BotApproval{BotID: run.BotID, RunID: run.ID, Version: run.Version, Action: action, ActionDigest: digest, Interaction: &store.BotInteraction{Kind: store.BotInteractionQuestion, QuestionID: "timezone", Payload: payload}, ExpiresAt: time.Now().Add(24 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	var args map[string]any
	_ = json.Unmarshal(payload, &args)
	for _, event := range []*session.Event{
		{ID: "timezone-call", Author: "agent", InvocationID: "question", Timestamp: time.Now(), LLMResponse: model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: "timezone-tool", Name: "ask_user", Args: args}}}}}},
		{ID: "timezone-response", Author: "agent", InvocationID: "question", Timestamp: time.Now(), LLMResponse: model.LLMResponse{Content: &genai.Content{Role: "user", Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{ID: "timezone-tool", Name: "ask_user", Response: map[string]any{"questionId": "timezone", "kind": "text", "prompt": "Which timezone?", "status": "ok"}}}}}}},
	} {
		if err := repo.appendBotRunEvent(ctx, lease, run.SessionID, event); err != nil {
			t.Fatal(err)
		}
	}
	checkpoint, _ := json.Marshal(botdomain.ApprovalCheckpoint{Format: botdomain.InteractionCheckpointFormat, ApprovalID: a.ID, BotID: run.BotID, RunID: run.ID, SessionID: run.SessionID, Version: run.Version, ActionDigest: digest, Fence: lease.Fence})
	work, err := repo.LoadWork(ctx, lease)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CommitRun(ctx, store.BotRunCommit{Lease: lease, Status: store.RunWaitingApproval, Checkpoint: checkpoint, Report: store.BotReport{Work: store.BuildBotWorkProjection(work), Summary: "Waiting", NeedsApproval: []uuid.UUID{a.ID}}}); err != nil {
		t.Fatal(err)
	}
	return a
}

func TestInteractiveContinuationQuestionSafety(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, kind := range []string{"durable", "registry"} {
			for _, mode := range []string{"expired-question", "revoked-question", "wrong-owner", "wrong-tenant", "digest", "question-id", "checkpoint", "active-run", "rollback", "removed-membership", "removed-policy", "removed-chat", "wrong-bot-chat", "arbitrary-chat-resource", "source-digest", "expired-mandate", "revoked-mandate"} {
				if kind == "registry" && (mode == "removed-policy" || mode == "removed-chat" || mode == "wrong-bot-chat" || mode == "arbitrary-chat-resource" || mode == "source-digest" || mode == "expired-mandate" || mode == "revoked-mandate") {
					continue
				}
				t.Run(string(scope)+"/"+kind+"/"+mode, func(t *testing.T) {
					repo, _, ctx, _, _ := runtimeFixture(t, scope)
					p, _ := execution.PrincipalFromContext(ctx)
					ctx, err := execution.WithAuthenticationEvidence(ctx, execution.AuthenticationEvidence{Method: p.Authentication, ExpiresAt: time.Now().Add(time.Hour)})
					if err != nil {
						t.Fatal(err)
					}
					registry := execution.NewBotTurnAuthorizationRegistry(10)
					admission := &questionContinuationAdmission{BotTurnAuthorizationRegistry: registry}
					ctx = store.WithBotMessageAdmission(ctx, admission)
					if kind == "durable" {
						policy := store.BotMandatePolicyResolver(mandateTestPolicy)
						if mode == "wrong-bot-chat" {
							policy = questionContinuationChatPolicy(uuid.NewString())
						} else if mode == "arbitrary-chat-resource" {
							policy = questionContinuationChatPolicy("unrelated-resource")
						}
						ctx = store.WithBotMandateAdmission(ctx, policy)
					}
					v := store.DefaultBotVersion()
					v.Charter = "Briefing"
					b, err := repo.Create(ctx, store.BotCreateRequest{Name: "Question safety", OwnerID: uuid.MustParse(p.Subject), Version: v, IdempotencyKey: "create-question"})
					if err != nil {
						t.Fatal(err)
					}
					receipt, err := repo.AdmitMessage(ctx, store.BotMessageRequest{BotID: b.ID, Version: b.Version, IdempotencyKey: "question", Instructions: "Schedule this briefing every day at 9am"})
					if err != nil {
						t.Fatal(err)
					}
					run := receipt.Run
					id := execution.BotRunIdentity{BotID: b.ID, RunID: run.ID, Version: run.Version, ActorID: p.Subject, OrgSlug: p.OrgSlug, TeamSlug: p.TeamSlug}
					ctx = execution.WithBotRunIdentity(ctx, id)
					a := savedContinuationQuestion(t, repo, ctx, run)
					registry.Cancel(id)
					q := store.BotApproveContinueRequest{BotID: b.ID, ApprovalID: a.ID, ActorID: b.OwnerID, ActionDigest: a.ActionDigest, Response: &store.BotInteractionResponse{QuestionID: "timezone", Answer: "America/New_York"}}
					original := repo.db
					switch mode {
					case "wrong-bot-chat", "arbitrary-chat-resource":
						// The persisted mandate and current policy agree; only the
						// question's exact Bot resource check should reject them.
						policy, _ := store.BotMandateAdmissionFromContext(ctx)
						m, err := readMandate(ctx, repo.db, *run.MandateRef)
						if err != nil {
							t.Fatal(err)
						}
						if _, _, err := (&botMandateRuntime{repo: repo, policy: policy}).verify(ctx, repo.db, *run.MandateRef, m.Mandate.Purpose); err != nil {
							t.Fatalf("resource fixture failed mandate verification: %v", err)
						}
					case "expired-question":
						repo.now = func() time.Time { return a.ExpiresAt.Add(time.Second) }
					case "wrong-owner":
						q.ActorID = uuid.New()
					case "wrong-tenant":
						p.OrgSlug = "other"
						ctx, err = execution.WithPrincipal(ctx, p)
						if err != nil {
							t.Fatal(err)
						}
					case "digest":
						q.ActionDigest = "wrong"
					case "question-id":
						q.Response.QuestionID = "wrong"
					case "removed-membership":
						repo.auth.check = func(context.Context, string) (string, error) { return "", store.ErrBotForbidden }
					case "removed-policy":
						ctx = store.WithBotMandateAdmission(ctx, func(context.Context, store.BotMandateRef) (store.BotMandatePolicy, error) {
							return store.BotMandatePolicy{}, store.ErrBotForbidden
						})
					case "removed-chat":
						ctx = store.WithBotMandateAdmission(ctx, func(c context.Context, ref store.BotMandateRef) (store.BotMandatePolicy, error) {
							policy, err := mandateTestPolicy(c, ref)
							policy.Scopes = []string{string(execution.CapabilityToolExecute)}
							policy.Constraints = policy.Constraints[:1]
							return policy, err
						})
					case "expired-mandate":
						m, e := readMandate(ctx, repo.db, *run.MandateRef)
						if e != nil {
							t.Fatal(e)
						}
						repo.now = func() time.Time { return m.Mandate.ExpiresAt.Add(time.Second) }
					case "revoked-mandate":
						runtime := &botMandateRuntime{repo: repo, policy: mandateTestPolicy}
						if err := runtime.RevokeBotMandate(ctx, *run.MandateRef); err != nil {
							t.Fatal(err)
						}
					default:
						repo.db = questionContinuationSafetyDB{botDB: original, mode: mode}
					}
					before, err := original.ReadApproval(ctx, b.ID, a.ID)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := repo.DecideAndRequeueApprovedRun(ctx, q); err == nil {
						t.Fatal("unsafe answer accepted")
					}
					after, err := original.ReadApproval(ctx, b.ID, a.ID)
					if err != nil || !reflect.DeepEqual(before, after) {
						t.Fatalf("failed decision persisted: %v", err)
					}
					saved, err := original.ReadRun(ctx, b.ID, run.ID)
					if err != nil || saved.Run.Status != store.RunWaitingApproval {
						t.Fatalf("failed answer queued: %v", err)
					}
					if kind == "registry" && mode == "rollback" && (admission.registered != 1 || admission.cancelled != 1) {
						t.Fatalf("rollback did not cancel registration: %+v", admission)
					}
					if _, err := registry.Resolve(id); err == nil {
						t.Fatal("failed transaction retained refreshed authority")
					}
				})
			}
		}
	}
}

type questionContinuationAdmission struct {
	*execution.BotTurnAuthorizationRegistry
	registered, cancelled int
}

func (a *questionContinuationAdmission) RegisterContinuation(ctx context.Context, id execution.BotRunIdentity, ceiling execution.BotCallerCeiling) (execution.BotTurnAuthorization, error) {
	auth, err := a.BotTurnAuthorizationRegistry.RegisterContinuation(ctx, id, ceiling)
	if err == nil {
		a.registered++
	}
	return auth, err
}
func (a *questionContinuationAdmission) Cancel(id execution.BotRunIdentity) {
	a.cancelled++
	a.BotTurnAuthorizationRegistry.Cancel(id)
}

// Faults enter through transaction reads; all writes still use disposable scoped
// SQLite. The rollback case fails after the decision at the queue's DB type gate.
type questionContinuationSafetyDB struct {
	botDB
	mode string
}

func (d questionContinuationSafetyDB) Transaction(ctx context.Context, fn func(botDB) error) error {
	return d.botDB.Transaction(ctx, func(tx botDB) error { return fn(questionContinuationSafetyDB{botDB: tx, mode: d.mode}) })
}
func (d questionContinuationSafetyDB) ReadApproval(ctx context.Context, botID, approvalID uuid.UUID) (*store.BotApproval, error) {
	a, err := d.botDB.ReadApproval(ctx, botID, approvalID)
	if err == nil && d.mode == "revoked-question" {
		a.Status = store.ApprovalRevoked
	}
	return a, err
}
func (d questionContinuationSafetyDB) ReadRun(ctx context.Context, botID, runID uuid.UUID) (*botStoredRun, error) {
	run, err := d.botDB.ReadRun(ctx, botID, runID)
	if err == nil && d.mode == "source-digest" {
		run.Instructions += " tampered"
	}
	if err == nil && d.mode == "checkpoint" {
		run.Run.Checkpoint = []byte(`{"format":"invalid"}`)
	}
	return run, err
}
func (d questionContinuationSafetyDB) GetBot(ctx context.Context, botID uuid.UUID) (*botStoredRecord, error) {
	rec, err := d.botDB.GetBot(ctx, botID)
	if err == nil && d.mode == "active-run" {
		id := uuid.New()
		rec.ActiveRunID = &id
	}
	return rec, err
}

func TestInteractiveContinuationExpiredQuestion(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, status := range []store.BotStatus{store.BotActive, store.BotPaused} {
			t.Run(string(scope)+"/"+string(status), func(t *testing.T) {
				repo, _, ctx, _, _ := runtimeFixture(t, scope)
				p, _ := execution.PrincipalFromContext(ctx)
				v := store.DefaultBotVersion()
				v.Charter = "Answer safely"
				b, err := repo.Create(ctx, store.BotCreateRequest{Name: "Question", OwnerID: uuid.MustParse(p.Subject), Version: v, IdempotencyKey: "question"})
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
				// The accepted-answer gate binds consent to the actual durable
				// ask_user call/response, not only an approval checkpoint.
				var questionArgs map[string]any
				if err := json.Unmarshal(payload, &questionArgs); err != nil {
					t.Fatal(err)
				}
				questionResponse := map[string]any{"questionId": "q", "kind": "yesno", "prompt": "Continue?", "status": "ok"}
				for _, event := range []*session.Event{
					{ID: "question-call", Author: "agent", InvocationID: "question-turn", Timestamp: time.Now(), LLMResponse: model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: "question-tool", Name: "ask_user", Args: questionArgs}}}}}},
					{ID: "question-response", Author: "agent", InvocationID: "question-turn", Timestamp: time.Now(), LLMResponse: model.LLMResponse{Content: &genai.Content{Role: "user", Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{ID: "question-tool", Name: "ask_user", Response: questionResponse}}}}}},
				} {
					if err := repo.appendBotRunEvent(ctx, lease, run.SessionID, event); err != nil {
						t.Fatal(err)
					}
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
				// Fresh authentication recovers only the still-valid saved question.
				id := execution.BotRunIdentity{BotID: b.ID, RunID: run.ID, Version: run.Version, ActorID: p.Subject, OrgSlug: p.OrgSlug, TeamSlug: p.TeamSlug}
				time.Sleep(time.Until(expiry) + time.Millisecond)
				ctx, err = execution.WithAuthenticationEvidence(ctx, execution.AuthenticationEvidence{Method: p.Authentication, ExpiresAt: time.Now().Add(time.Hour)})
				if err != nil {
					t.Fatal(err)
				}
				response := &store.BotInteractionResponse{QuestionID: "q", Answer: "yes"}
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
				q := store.BotApproveContinueRequest{BotID: b.ID, ApprovalID: a.ID, ActorID: b.OwnerID, ActionDigest: digest, Response: response}
				result, err := repo.DecideAndRequeueApprovedRun(ctx, q)
				if err != nil {
					t.Fatal(err)
				}
				continued, err := repo.GetRun(ctx, b.ID, result.RunID)
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
				if _, err := repo.DecideAndRequeueApprovedRun(ctx, q); err == nil {
					t.Fatal("terminal question replay accepted")
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
