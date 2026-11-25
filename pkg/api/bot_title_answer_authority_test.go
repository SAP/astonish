package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
)

// Exercise the actual answer handler with a personal repository while the
// authenticated Studio caller retains its selected org/team, as in local mode.
// Mandate admission must not depend on the ephemeral interactive registry.
func TestBotTitleAnswerAuthorityHTTPRegression(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, durable := range []bool{false, true} {
			for _, decision := range []string{"owner", "foreign", "revoked", "mandate-revoked", "policy-unavailable"} {
				if !durable && (decision == "mandate-revoked" || decision == "policy-unavailable") {
					continue
				}
				t.Run(fmt.Sprintf("%s/durable=%t/%s", scope, durable, decision), func(t *testing.T) {
					ctx := context.Background()
					dir := t.TempDir()
					_, db, err := entstore.NewPlatformServices(ctx, entstore.Config{DSN: filepath.Join(dir, "platform.db"), DataDir: dir})
					if err != nil {
						t.Fatal(err)
					}
					defer db.Close()
					org := &store.Organization{ID: uuid.NewString(), Slug: "my-org", Name: "My Org", Status: "active"}
					if err := db.Organizations().Create(ctx, org); err != nil {
						t.Fatal(err)
					}
					if err := db.ProvisionOrg(ctx, org.ID, org.Slug); err != nil {
						t.Fatal(err)
					}
					ods, err := db.ForOrg(org.Slug)
					if err != nil {
						t.Fatal(err)
					}
					team := &store.Team{ID: uuid.NewString(), Slug: "general", Name: "General", SchemaName: "team_general"}
					if err := ods.Teams().CreateTeam(ctx, team); err != nil {
						t.Fatal(err)
					}
					owner, foreign := uuid.NewString(), uuid.NewString()
					for _, user := range []string{owner, foreign} {
						if err := db.Users().Create(ctx, &store.User{ID: user, Email: user + "@example.test", DisplayName: "User", Status: "active"}); err != nil {
							t.Fatal(err)
						}
						if err := db.Organizations().AddMember(ctx, user, org.ID, "member"); err != nil {
							t.Fatal(err)
						}
						if err := ods.Teams().AddMember(ctx, &store.TeamMembership{UserID: user, TeamID: team.ID, Role: "member"}); err != nil {
							t.Fatal(err)
						}
					}
					auth := func(subject string) context.Context {
						t.Helper()
						p := execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: subject, OrgSlug: org.Slug, TeamSlug: team.Slug, Authenticated: true, Roles: []string{"member"}}
						c, err := execution.WithPrincipal(context.Background(), p)
						if err != nil {
							t.Fatal(err)
						}
						c, err = execution.WithAuthenticationEvidence(c, execution.AuthenticationEvidence{Method: execution.AuthMethodLocalOS, LocalLoopback: true, ExpiresAt: time.Now().Add(15 * time.Minute)})
						if err != nil {
							t.Fatal(err)
						}
						return store.WithTenantContext(c, &store.TenantContext{UserID: subject, OrgSlug: org.Slug, TeamSlug: team.Slug})
					}
					ctx = auth(owner)
					repo := ods.ForUser(owner).Bots()
					if scope == store.BotScopeTeam {
						repo = ods.ForTeam(team.Slug).Bots()
					}
					registry := execution.NewBotTurnAuthorizationRegistry(20)
					defer registry.Close()
					allowed := true
					policy := func(context.Context, store.BotMandateRef) (store.BotMandatePolicy, error) {
						if !allowed {
							return store.BotMandatePolicy{}, store.ErrBotForbidden
						}
						return store.BotMandatePolicy{TTL: time.Hour, Scopes: []string{"chat", "tool:execute"}, Constraints: []execution.BotActionResourceConstraint{{Action: execution.CapabilityChat, Resources: []string{"task"}}, {Action: execution.CapabilityToolExecute, Resources: []string{"ask_user", "read_file"}}}, Limits: store.BotMandateLimits(config.DefaultBotMandateConfig())}, nil
					}
					svc := &store.Services{Bots: repo, PersonalBots: repo, BotTurnRegistry: registry, BotInteractiveReady: true}
					if durable {
						svc.BotMandatePolicy = policy
					}
					router := mux.NewRouter()
					registerBotRoutes(router)
					// Headers select fixture identities only; production receives already verified
					// middleware context, never an actor field from the answer JSON.
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						subject := owner
						if r.Header.Get("X-Test-Foreign") == "true" {
							subject = foreign
						}
						router.ServeHTTP(w, r.WithContext(store.WithServices(auth(subject), svc)))
					}))
					defer server.Close()
					request := func(method, path, body string, foreign bool, want int) []byte {
						t.Helper()
						req, err := http.NewRequest(method, server.URL+path+"?scope="+string(scope), strings.NewReader(body))
						if err != nil {
							t.Fatal(err)
						}
						if foreign {
							req.Header.Set("X-Test-Foreign", "true")
						}
						req.Header.Set("Content-Type", "application/json")
						resp, err := server.Client().Do(req)
						if err != nil {
							t.Fatal(err)
						}
						defer resp.Body.Close()
						raw, err := io.ReadAll(resp.Body)
						if err != nil {
							t.Fatal(err)
						}
						if resp.StatusCode != want {
							t.Fatalf("%s %s: status=%d want=%d body=%s", method, path, resp.StatusCode, want, raw)
						}
						return raw
					}
					var created store.Bot
					if err := json.Unmarshal(request("POST", "/api/studio/bots", `{"name":"AI News","start_from_title":true,"idempotency_key":"auth-regression"}`, false, 201), &created); err != nil {
						t.Fatal(err)
					}
					runs, err := repo.ListRuns(ctx, created.ID, store.BotPage{})
					if err != nil || len(runs) != 1 {
						t.Fatalf("runs %v %v", runs, err)
					}
					run := runs[0]
					if (run.MandateRef != nil) != durable || run.CallerCeiling == nil || run.CallerCeiling.Principal.TeamSlug != "general" {
						t.Fatalf("wrong authority fixture: %+v", run)
					}
					identity := execution.BotRunIdentity{BotID: run.BotID, RunID: run.ID, Version: run.Version, ActorID: owner, OrgSlug: org.Slug, TeamSlug: team.Slug}
					if _, err := registry.Resolve(identity); (err != nil) != durable {
						t.Fatalf("unexpected process-local authority for durable=%t: %v", durable, err)
					}
					worker := execution.WithBotRunIdentity(ctx, identity)
					lease, err := repo.ClaimRun(worker, store.BotClaimRequest{BotID: run.BotID, RunID: run.ID, WorkerID: "question-fixture", ExpiresAt: time.Now().Add(time.Minute)})
					if err != nil {
						t.Fatal(err)
					}
					payload := json.RawMessage(`{"questionId":"title-q","kind":"select","prompt":"Which AI news assignment?","allow_custom_answer":true,"options":[{"id":"brief","label":"Prepare an AI news briefing"},{"id":"compare","label":"Compare AI announcements"},{"id":"sources","label":"Find original AI news sources"}]}`)
					action := execution.ActionDescriptor{Tool: "ask_user", Effect: execution.EffectRead, Arguments: payload, ConstraintsVerified: true}
					digest, err := action.Digest()
					if err != nil {
						t.Fatal(err)
					}
					approval, err := repo.RequestApproval(worker, lease, store.BotApproval{BotID: run.BotID, RunID: run.ID, Version: run.Version, Action: action, ActionDigest: digest, Interaction: &store.BotInteraction{Kind: store.BotInteractionQuestion, QuestionID: "title-q", Payload: payload}, ExpiresAt: time.Now().Add(time.Hour)})
					if err != nil {
						t.Fatal(err)
					}
					// Persist the actual question invocation/response before the owner can
					// answer it. A bare approval is deliberately not transcript identity.
					var questionArgs map[string]any
					if err := json.Unmarshal(payload, &questionArgs); err != nil {
						t.Fatal(err)
					}
					questionResponse := map[string]any{"status": "ok"}
					for key, value := range questionArgs {
						questionResponse[key] = value
					}
					for _, event := range []*session.Event{
						{ID: "title-question-call", Author: "model", InvocationID: "title-question", Timestamp: time.Now(), LLMResponse: model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: "title-question-call", Name: "ask_user", Args: questionArgs}}}}}},
						{ID: "title-question-response", Author: "user", InvocationID: "title-question", Timestamp: time.Now(), LLMResponse: model.LLMResponse{Content: &genai.Content{Role: "user", Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{ID: "title-question-call", Name: "ask_user", Response: questionResponse}}}}}},
					} {
						if err := repo.(store.BotTranscriptStore).AppendBotRunEvent(worker, lease, run.SessionID, event); err != nil {
							t.Fatal(err)
						}
					}
					checkpoint, err := json.Marshal(bot.ApprovalCheckpoint{Format: bot.InteractionCheckpointFormat, ApprovalID: approval.ID, BotID: run.BotID, RunID: run.ID, SessionID: run.SessionID, Version: run.Version, ActionDigest: digest, Fence: lease.Fence})
					if err != nil {
						t.Fatal(err)
					}
					if err := repo.CommitRun(worker, store.BotRunCommit{Lease: lease, Status: store.RunWaitingApproval, Checkpoint: checkpoint, Report: store.BotReport{Summary: "Waiting", NeedsApproval: []uuid.UUID{approval.ID}}}); err != nil {
						t.Fatal(err)
					}
					if decision == "mandate-revoked" {
						mandates, err := entstore.NewBotMandateRuntime(repo, policy)
						if err != nil {
							t.Fatal(err)
						}
						if err := mandates.RevokeBotMandate(ctx, *run.MandateRef); err != nil {
							t.Fatal(err)
						}
					}
					if decision == "policy-unavailable" {
						svc.BotMandatePolicy = nil
					}
					if decision == "revoked" {
						if durable {
							allowed = false
						} else {
							if err := db.Organizations().RemoveMember(ctx, owner, org.ID); err != nil {
								t.Fatal(err)
							}
						}
					}
					want := http.StatusAccepted
					if decision != "owner" {
						want = http.StatusForbidden
					}
					path := "/api/studio/bots/" + run.BotID.String() + "/approvals/" + approval.ID.String() + "/approve-and-continue"
					body := fmt.Sprintf(`{"action_digest":%q,"response":{"question_id":"title-q","answer":"brief"}}`, digest)
					request("POST", path, body, decision == "foreign", want)
					if decision == "revoked" && !durable {
						if err := db.Organizations().AddMember(ctx, owner, org.ID, "member"); err != nil {
							t.Fatal(err)
						}
					}
					saved, err := repo.GetRun(ctx, run.BotID, run.ID)
					if err != nil {
						t.Fatal(err)
					}
					approvals, err := repo.ListApprovals(ctx, run.BotID, store.BotPage{})
					if err != nil || len(approvals) != 1 {
						t.Fatalf("approvals: %v %v", approvals, err)
					}
					if decision != "owner" {
						if saved.Status != store.RunWaitingApproval || saved.TitleConfirmation.State != store.BotTitleConfirmationWaiting || approvals[0].Status != store.ApprovalPending {
							t.Fatalf("rejected answer mutated state: %+v %+v", saved, approvals)
						}
						return
					}
					if saved.Status != store.RunQueued || saved.TitleConfirmation.State != store.BotTitleConfirmationConfirmed || approvals[0].Status != store.ApprovalApproved {
						t.Fatalf("owner answer did not queue: %+v %+v", saved, approvals)
					}
					input, err := repo.(store.BotRuntimeStore).LoadRunInput(ctx, run.BotID, run.ID)
					if err != nil || input.Instructions != "Prepare an AI news briefing" {
						t.Fatalf("confirmed input: %+v %v", input, err)
					}
					if durable && *saved.MandateRef == *run.MandateRef {
						t.Fatal("answer did not refresh question-only mandate")
					}
					request("POST", path, body, false, http.StatusAccepted)
					runs, err = repo.ListRuns(ctx, run.BotID, store.BotPage{})
					if err != nil || len(runs) != 1 {
						t.Fatalf("duplicate execution: %v %v", runs, err)
					}
				})
			}
		}
	}
}
