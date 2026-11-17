package api

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
	"google.golang.org/genai"
)

// Controlled runner, production HTTP handlers and scoped repositories. No provider or sandbox.
func TestBotTitleConfirmationHTTPAcceptance(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			_, db, err := entstore.NewPlatformServices(ctx, entstore.Config{DSN: filepath.Join(dir, "platform.db"), DataDir: dir})
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			org := &store.Organization{ID: uuid.NewString(), Slug: "confirmation", Name: "Confirmation", Status: "active"}
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
			team := &store.Team{ID: uuid.NewString(), Slug: "team", Name: "Team", SchemaName: "team_team"}
			if err := ods.Teams().CreateTeam(ctx, team); err != nil {
				t.Fatal(err)
			}
			owner := uuid.NewString()
			if err := db.Users().Create(ctx, &store.User{ID: owner, Email: owner + "@example.test", DisplayName: "Owner", Status: "active"}); err != nil {
				t.Fatal(err)
			}
			if err := db.Organizations().AddMember(ctx, owner, org.ID, "member"); err != nil {
				t.Fatal(err)
			}
			if err := ods.Teams().AddMember(ctx, &store.TeamMembership{UserID: owner, TeamID: team.ID, Role: "member"}); err != nil {
				t.Fatal(err)
			}
			p := execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: owner, OrgSlug: org.Slug, TeamSlug: team.Slug, Authenticated: true, Scopes: []string{"chat", "tool:execute"}}
			ctx, err = execution.WithPrincipal(ctx, p)
			if err != nil {
				t.Fatal(err)
			}
			ctx, err = execution.WithAuthenticationEvidence(ctx, execution.AuthenticationEvidence{Method: p.Authentication, ExpiresAt: time.Now().Add(time.Hour)})
			if err != nil {
				t.Fatal(err)
			}
			ctx = store.WithTenantContext(ctx, &store.TenantContext{UserID: owner, OrgSlug: org.Slug, TeamSlug: team.Slug})
			personal := ods.ForUser(owner)
			repo := ods.ForTeam(team.Slug).Bots()
			if scope == store.BotScopePersonal {
				repo = personal.Bots()
			}
			registry := execution.NewBotTurnAuthorizationRegistry(20)
			defer registry.Close()
			ctx = store.WithBotMessageAdmission(ctx, registry)
			ctx = store.WithServices(ctx, &store.Services{Bots: repo, PersonalBots: personal.Bots(), BotTurnRegistry: registry, BotInteractiveReady: true})
			router := mux.NewRouter()
			registerBotRoutes(router)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { router.ServeHTTP(w, r.WithContext(ctx)) }))
			defer server.Close()
			request := func(method, path, body string, status int) []byte {
				t.Helper()
				r, _ := http.NewRequest(method, server.URL+path+"?scope="+string(scope), strings.NewReader(body))
				r.Header.Set("Content-Type", "application/json")
				resp, e := server.Client().Do(r)
				if e != nil {
					t.Fatal(e)
				}
				defer resp.Body.Close()
				raw, _ := io.ReadAll(resp.Body)
				if resp.StatusCode != status {
					t.Fatalf("%s: %d want %d: %s", path, resp.StatusCode, status, raw)
				}
				return raw
			}
			for _, answer := range []string{"brief", "Custom private assignment"} {
				var receipt store.BotTitleStartReceipt
				raw := request("POST", "/api/studio/bots", fmt.Sprintf(`{"name":"AI News","start_from_title":true,"idempotency_key":%q}`, answer), 201)
				if err := json.Unmarshal(raw, &receipt); err != nil {
					t.Fatal(err)
				}
				runs, err := repo.ListRuns(ctx, receipt.ID, store.BotPage{})
				if err != nil || len(runs) != 1 {
					t.Fatalf("runs: %v %v", runs, err)
				}
				run := runs[0]
				input, err := repo.(store.BotRuntimeStore).LoadRunInput(ctx, receipt.ID, run.ID)
				if err != nil || input.Run.TitleConfirmation == nil || input.Run.TitleConfirmation.State != store.BotTitleConfirmationNeedsQuestion || input.Task != nil {
					t.Fatalf("admission: %+v %v", input, err)
				}
				worker := execution.WithBotRunIdentity(ctx, execution.BotRunIdentity{BotID: run.BotID, RunID: run.ID, Version: run.Version, ActorID: owner, OrgSlug: org.Slug, TeamSlug: team.Slug})
				lease, err := repo.ClaimRun(worker, store.BotClaimRequest{BotID: run.BotID, RunID: run.ID, WorkerID: "controlled", ExpiresAt: time.Now().Add(time.Minute)})
				if err != nil {
					t.Fatal(err)
				}
				if err := repo.CommitRun(worker, store.BotRunCommit{Lease: lease, Status: store.RunSucceeded, Report: store.BotReport{Summary: "Premature"}}); err == nil {
					t.Fatal("unconfirmed success accepted")
				}
				payload := json.RawMessage(`{"questionId":"title-q","kind":"select","prompt":"Which AI news assignment?","allow_custom_answer":true,"options":[{"id":"brief","label":"Prepare an AI news briefing"},{"id":"compare","label":"Compare AI research announcements"},{"id":"sources","label":"Find original AI news sources"}]}`)
				action := execution.ActionDescriptor{Tool: "ask_user", Effect: execution.EffectRead, Arguments: payload, ConstraintsVerified: true}
				digest, _ := action.Digest()
				a, err := repo.RequestApproval(worker, lease, store.BotApproval{BotID: run.BotID, RunID: run.ID, Version: run.Version, Action: action, ActionDigest: digest, Interaction: &store.BotInteraction{Kind: store.BotInteractionQuestion, QuestionID: "title-q", Payload: payload}, ExpiresAt: time.Now().Add(time.Hour)})
				if err != nil {
					t.Fatal(err)
				}
				checkpoint, _ := json.Marshal(bot.ApprovalCheckpoint{Format: bot.InteractionCheckpointFormat, ApprovalID: a.ID, BotID: run.BotID, RunID: run.ID, SessionID: run.SessionID, Version: run.Version, ActionDigest: digest, Fence: lease.Fence})
				if err := repo.CommitRun(worker, store.BotRunCommit{Lease: lease, Status: store.RunWaitingApproval, Checkpoint: checkpoint, Report: store.BotReport{Summary: "Waiting", NeedsApproval: []uuid.UUID{a.ID}}}); err != nil {
					t.Fatal(err)
				}
				persisted, err := repo.GetRun(ctx, run.BotID, run.ID)
				if err != nil || !persisted.RuntimeAccountingRecorded || persisted.Usage.RuntimeNanos != 0 {
					t.Fatalf("zero accounting: %+v %v", persisted, err)
				}
				path := "/api/studio/bots/" + run.BotID.String() + "/approvals/" + a.ID.String() + "/approve-and-continue"
				body := fmt.Sprintf(`{"action_digest":%q,"response":{"question_id":"title-q","answer":%q}}`, digest, answer)
				request("POST", path, fmt.Sprintf(`{"action_digest":%q,"response":{"question_id":"foreign","answer":%q}}`, digest, answer), 400)
				request("POST", path, fmt.Sprintf(`{"action_digest":"stale","response":{"question_id":"title-q","answer":%q}}`, answer), 409)
				stillWaiting, err := repo.GetRun(ctx, run.BotID, run.ID)
				if err != nil || stillWaiting.Status != store.RunWaitingApproval || stillWaiting.TitleConfirmation.State != store.BotTitleConfirmationWaiting {
					t.Fatal("rejected answer unlocked execution", err)
				}
				if answer != "brief" {
					// Simulate losing process-local authority while the owner waits.
					registry.Cancel(execution.BotRunIdentity{BotID: run.BotID, RunID: run.ID, Version: run.Version, ActorID: owner, OrgSlug: org.Slug, TeamSlug: team.Slug})
					continuePath := "/api/studio/bots/" + run.BotID.String() + "/runs/" + run.ID.String() + "/continue-interactive"
					continueBody := fmt.Sprintf(`{"expected_revision":%d,"idempotency_key":"confirm-custom","response":{"question_id":"title-q","answer":%q}}`, receipt.Revision, answer)
					request("POST", continuePath, continueBody, 202)
					request("POST", continuePath, continueBody, 202)
				} else {
					request("POST", path, body, 202)
				}
				input, err = repo.(store.BotRuntimeStore).LoadRunInput(ctx, run.BotID, run.ID)
				want := answer
				if answer == "brief" {
					want = "Prepare an AI news briefing"
				}
				if err != nil || input.Instructions != want || input.Run.TitleConfirmation.OriginalTitle != "AI News" {
					t.Fatalf("confirmed: %+v %v", input, err)
				}
				request("POST", path, body, 202)
				if _, err := repo.ClaimRun(worker, store.BotClaimRequest{BotID: run.BotID, RunID: run.ID, WorkerID: "resume", ExpiresAt: time.Now().Add(time.Minute)}); err != nil {
					t.Fatal(err)
				}
				request("POST", path, body, 202)
				request("POST", path, fmt.Sprintf(`{"action_digest":%q,"response":{"question_id":"title-q","answer":"different"}}`, digest), 409)
			}
		})
	}
}

// A real loopback HTTP invocation of the production producer and event sink.
// The model is controlled, not paid: this proves transport ordering, not that a
// provider follows the prompt or that title admission supplies production tools.
// The reviewed final publication is intentionally outside this producer test.
func TestBotEmployeeTitleAcknowledgementHTTPToolBoundary(t *testing.T) {
	for _, guarded := range []bool{false, true} {
		for _, toolBoundary := range []bool{false, true} {
			name := "historical"
			if guarded {
				name = "evidence-bound"
			}
			if toolBoundary {
				name += "/work-tool"
			} else {
				name += "/toolless"
			}
			t.Run(name, func(t *testing.T) {
				ctx, req, ss := botChatTestRequest(t)
				req.Bot.Name, req.Instructions, req.Interactive = "AI News", "AI News", true
				req.Run.Intent = store.BotIntentRegular
				if guarded {
					req.Work = store.NewBotAgentWorkState(store.BotWorkContract{BotID: req.Bot.ID, RunID: req.Run.ID, Version: 1, Instructions: req.Instructions})
					req.Work.EmployeeReviewPolicy = store.BotEmployeeReviewEvidenceBoundV1
				}
				ctx = execution.WithBotRunIdentity(ctx, req.Policy.Identity())
				ctx = execution.WithRunPolicy(ctx, req.Policy)
				ctx = context.WithValue(ctx, botChatBudgetKey{}, req.Budget)
				ctx = agent.WithBotPolicyRuntime(ctx, agent.BotPolicyRuntime{Classifier: botChatTestClassifier{}, Guard: botChatTestGuard{}})
				const acknowledgement = "I'll put together an AI news briefing and check the original reporting."
				const candidate = "PRIVATE_FINAL_CANDIDATE"
				var cleaned, executed atomic.Int64
				read, err := functiontool.New(functiontool.Config{Name: "read_file", Description: "Inspect original controlled reporting"}, func(ctx tool.Context, args map[string]any) (map[string]any, error) {
					if err := agent.AuthorizeBotTool(ctx, "read_file", args); err != nil {
						return nil, err
					}
					executed.Add(1)
					return map[string]any{"content": "Controlled original reporting: a new model was announced."}, nil
				})
				if err != nil {
					t.Fatal(err)
				}
				turns := []*MockTurn{TextTurn(candidate)}
				if toolBoundary {
					turns = []*MockTurn{{Parts: []*genai.Part{{Text: acknowledgement}, {FunctionCall: &genai.FunctionCall{Name: "read_file", Args: map[string]any{"path": "original-reporting"}}}}, TurnComplete: true}, TextTurn(candidate)}
				}
				llm := NewMockLLM(turns...)
				resources, err := botChatTestFactory(t, ss, llm, []tool.Tool{read}, &cleaned)(ctx, req)
				if err != nil {
					t.Fatal(err)
				}
				defer resources.Cleanup()
				var events []bot.RuntimeEvent
				server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, _ *http.Request) {
					var sequence int64
					var completion ChatCompletion
					result, runErr := runBotChatAttempt(ctx, req, resources, func(_ context.Context, event bot.RuntimeEvent) error {
						events = append(events, event)
						return nil
					}, nil, nil, nil, false, &sequence, &completion)
					if runErr != nil {
						t.Errorf("producer: %v", runErr)
						out.WriteHeader(http.StatusInternalServerError)
					}
					if !completion.Final || completion.FinalText != candidate || result.Report.Summary != candidate {
						t.Errorf("producer lost its final candidate: %+v", completion)
					}
					_ = json.NewEncoder(out).Encode(result)
				}))
				defer server.Close()
				response, err := server.Client().Post(server.URL, "application/json", nil)
				if err != nil {
					t.Fatal(err)
				}
				defer response.Body.Close()
				if response.StatusCode != http.StatusOK {
					t.Fatalf("HTTP %d", response.StatusCode)
				}
				ackIndex, toolIndex, resultIndex := -1, -1, -1
				var live strings.Builder
				for i, event := range events {
					if event.Sequence != int64(i+1) || event.RunID != req.Run.ID || event.SessionID != req.Run.SessionID || event.TurnID != events[0].TurnID {
						t.Fatalf("wrong event identity/order: %+v", event)
					}
					if event.Type == "text" {
						var text struct {
							Text string `json:"text"`
						}
						if err := json.Unmarshal(event.Payload, &text); err != nil {
							t.Fatal(err)
						}
						live.WriteString(text.Text)
						if text.Text == acknowledgement {
							ackIndex = i
						}
					}
					if event.Type == "tool_call" {
						toolIndex = i
					}
					if event.Type == "tool_result" {
						resultIndex = i
					}
					if guarded && event.Type == "done" {
						t.Fatal("unreviewed producer closed the public stream")
					}
				}
				if toolBoundary && (ackIndex < 0 || toolIndex <= ackIndex || resultIndex <= toolIndex || executed.Load() != 1) {
					t.Fatalf("acknowledgement did not precede useful work: ack=%d call=%d result=%d executed=%d events=%+v", ackIndex, toolIndex, resultIndex, executed.Load(), events)
				}
				if guarded && toolBoundary && live.String() != acknowledgement {
					t.Fatalf("guarded active text must be exactly the model acknowledgement, got %q", live.String())
				}
				if guarded && !toolBoundary && live.Len() != 0 {
					t.Fatalf("toolless guarded candidate escaped: %q", live.String())
				}
				if guarded && strings.Contains(live.String(), candidate) {
					t.Fatal("unreviewed final candidate leaked")
				}
				if !guarded && !strings.Contains(live.String(), candidate) {
					t.Fatal("historical producer publication changed")
				}
				if !toolBoundary && (ackIndex >= 0 || executed.Load() != 0) {
					t.Fatal("server fabricated acknowledgement or work")
				}
				if len(llm.Calls) != len(turns) {
					t.Fatal("acknowledgement added a model call")
				}
				prompt := llm.Calls[0].Config.SystemInstruction.Parts[0].Text
				for _, guidance := range []string{
					"Once the assignment is sufficiently specified, give a brief friendly AI-authored acknowledgement of the actual next work",
					"not a canned ready message or a claim that work is complete",
					"continue immediately into actual useful work under the current scoped authority",
					"Put the acknowledgement before the first real work tool call",
					"Do not invoke a dummy tool merely to release text",
				} {
					if !strings.Contains(prompt, guidance) {
						t.Fatalf("producer prompt lost semantic acknowledgement guidance: %q", guidance)
					}
				}
			})
		}
	}
}
