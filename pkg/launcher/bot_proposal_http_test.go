package launcher

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
)

type proposalModel struct {
	calls        int
	after        func()
	sawReference bool
}

func (*proposalModel) Name() string { return "controlled-proposal" }
func (m *proposalModel) GenerateContent(ctx context.Context, q *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		m.calls++
		if m.calls == 1 {
			parts := make([]*genai.Part, 0, 3)
			for i := 0; i < 3; i++ {
				parts = append(parts, &genai.Part{FunctionCall: &genai.FunctionCall{ID: fmt.Sprintf("proposal-%d", i), Name: botProposalToolName, Args: map[string]any{"payload": map[string]any{"operation": "responsibility", "reason": "Owner requested a revised purpose", "responsibility": "Track reviewed notices"}}}})
			}
			yield(&model.LLMResponse{Content: &genai.Content{Role: "model", Parts: parts}}, nil)
			return
		}
		for _, c := range q.Contents {
			for _, p := range c.Parts {
				if p.FunctionResponse != nil && p.FunctionResponse.Name == botProposalToolName {
					_, m.sawReference = p.FunctionResponse.Response["proposal_id"]
				}
			}
		}
		if m.after != nil {
			m.after()
			m.after = nil
		}
		yield(&model.LLMResponse{Content: genai.NewContentFromText("Proposed, not applied. Please review and confirm after this conversation.", "model"), TurnComplete: true}, nil)
	}
}

type proposalResolver struct{ llm model.LLM }

func (r proposalResolver) Resolve(context.Context, uuid.UUID) (bot.ResolvedModel, error) {
	return bot.ResolvedModel{Provider: "fixture", Model: r.llm.Name(), LLM: r.llm}, nil
}

// Real socket + SQLite + production factory/ChatRunner + leased worker commit.
// Authentication and model are controlled; external capabilities remain absent.
func TestBotConversationalControlHTTPAcceptance(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			_, db, err := entstore.NewPlatformServices(ctx, entstore.Config{DSN: filepath.Join(dir, "platform.db"), DataDir: dir})
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			db.SetBotActivationReadiness(func(context.Context, store.Bot, store.BotVersion) error { return nil })
			org := &store.Organization{ID: uuid.NewString(), Slug: "proposal", Name: "Proposal", Status: "active"}
			if err = db.Organizations().Create(ctx, org); err != nil {
				t.Fatal(err)
			}
			if err = db.ProvisionOrg(ctx, org.ID, org.Slug); err != nil {
				t.Fatal(err)
			}
			ods, err := db.ForOrg(org.Slug)
			if err != nil {
				t.Fatal(err)
			}
			team := &store.Team{ID: uuid.NewString(), Slug: "team", Name: "Team", SchemaName: "team_team"}
			if err = ods.Teams().CreateTeam(ctx, team); err != nil {
				t.Fatal(err)
			}
			owner, reader := uuid.NewString(), uuid.NewString()
			for _, id := range []string{owner, reader} {
				if err = db.Users().Create(ctx, &store.User{ID: id, Email: id + "@example.test", DisplayName: "Tester", Status: "active"}); err != nil {
					t.Fatal(err)
				}
				if err = db.Organizations().AddMember(ctx, id, org.ID, "member"); err != nil {
					t.Fatal(err)
				}
				if err = ods.Teams().AddMember(ctx, &store.TeamMembership{UserID: id, TeamID: team.ID, Role: "member"}); err != nil {
					t.Fatal(err)
				}
			}
			principal := execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: owner, OrgSlug: org.Slug, TeamSlug: team.Slug, Authenticated: true}
			ctx, err = execution.WithPrincipal(ctx, principal)
			if err != nil {
				t.Fatal(err)
			}
			services, err := BotChatServicesForOwner(ods, principal, scope)
			if err != nil {
				t.Fatal(err)
			}
			repo := services.Bots
			if scope == store.BotScopePersonal {
				repo = services.PersonalBots
			}
			v := store.DefaultBotVersion()
			v.Charter = "Observe"
			b, err := repo.CreateDraft(ctx, store.BotCreateRequest{Name: "Proposal", OwnerID: uuid.MustParse(owner), Version: v, IdempotencyKey: "draft"})
			if err != nil {
				t.Fatal(err)
			}
			router := mux.NewRouter()
			router.HandleFunc("/bots/{botID}/sessions/{sessionID}/stream", api.BotHistoryStreamHandler).Methods("GET")
			router.HandleFunc("/bots/{botID}/proposals", api.BotControlProposalsHandler).Methods("GET")
			router.HandleFunc("/bots/{botID}/proposals/{proposalID}/decision", api.BotControlProposalDecisionHandler).Methods("POST")
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				c := r.Context()
				id := r.Header.Get("X-Test-Actor")
				if id != "" {
					p := principal
					p.Subject = id
					c, _ = execution.WithPrincipal(c, p)
					c = store.WithTenantContext(c, &store.TenantContext{UserID: id, OrgSlug: p.OrgSlug, TeamSlug: p.TeamSlug})
					scoped, _ := BotChatServicesForOwner(ods, p, scope)
					c = store.WithServices(c, scoped)
				}
				router.ServeHTTP(w, r.WithContext(c))
			}))
			defer server.Close()
			request := func(method, path, actor, body string, status int) []byte {
				t.Helper()
				r, _ := http.NewRequest(method, server.URL+path, strings.NewReader(body))
				r.Header.Set("X-Test-Actor", actor)
				r.Header.Set("Content-Type", "application/json")
				res, e := server.Client().Do(r)
				if e != nil {
					t.Fatal(e)
				}
				defer res.Body.Close()
				raw, _ := io.ReadAll(res.Body)
				if res.StatusCode != status {
					t.Fatalf("%s %s: %d want %d: %s", method, path, res.StatusCode, status, raw)
				}
				return raw
			}
			suffix := "?scope=" + string(scope)
			path := "/bots/" + b.ID.String() + "/proposals"
			request("GET", path+suffix, "", "", 401)
			request("GET", path+suffix, owner, "", 200)
			run, err := repo.RequestRun(ctx, store.BotRunRequest{BotID: b.ID, Version: b.Version, Intent: store.BotIntentDraftConversation, Trigger: store.TriggerManual, IdempotencyKey: "conversation", Instructions: "Change your responsibility to track reviewed notices"})
			if err != nil {
				t.Fatal(err)
			}
			identity := execution.BotRunIdentity{BotID: b.ID, RunID: run.ID, Version: b.Version, ActorID: owner, OrgSlug: principal.OrgSlug, TeamSlug: principal.TeamSlug}
			worker := execution.WithBotRunIdentity(ctx, identity)
			lease, err := repo.ClaimRun(worker, store.BotClaimRequest{BotID: b.ID, RunID: run.ID, WorkerID: "controlled-worker", ExpiresAt: time.Now().Add(time.Minute)})
			if err != nil {
				t.Fatal(err)
			}
			run, err = repo.GetRun(worker, b.ID, run.ID)
			if err != nil {
				t.Fatal(err)
			}
			version, _ := repo.GetVersion(ctx, b.ID, b.Version)
			policy, err := execution.NewRunPolicy(execution.RunPolicySpec{Identity: identity, Level: execution.AutonomySupervised, AllowedTools: []string{}})
			if err != nil {
				t.Fatal(err)
			}
			ss := session.InMemoryService()
			_, err = ss.Create(ctx, &session.CreateRequest{AppName: "astonish", UserID: owner, SessionID: run.SessionID.String(), State: map[string]any{"execution_owner": "bot"}})
			if err != nil {
				t.Fatal(err)
			}
			llm := &proposalModel{}
			var proposal store.BotControlProposal
			var remaining []store.BotControlProposal
			llm.after = func() {
				var rows struct {
					Proposals []store.BotControlProposal `json:"proposals"`
				}
				if err := json.Unmarshal(request("GET", path+suffix, owner, "", 200), &rows); err != nil {
					t.Fatal(err)
				}
				if len(rows.Proposals) != 3 {
					t.Fatalf("missing proposal: %+v", rows)
				}
				proposal = rows.Proposals[0]
				remaining = rows.Proposals[1:]
				decision := path + "/" + proposal.ID.String() + "/decision" + suffix
				request("POST", decision, owner, fmt.Sprintf(`{"digest":%q,"decision":"confirm"}`, proposal.Digest), 409)
				unchanged, _ := repo.Get(ctx, b.ID)
				if unchanged.Version != b.Version || unchanged.Revision != b.Revision {
					t.Fatal("proposal mutated configuration")
				}
			}
			factory, err := NewBotChatAgentFactory(BotChatFactoryConfig{ContextWindow: 8192, LoadScope: func(context.Context, bot.RunRequest) (*BotChatScope, error) {
				return &BotChatScope{Services: services, Sessions: ss, Models: proposalResolver{llm}, Cleanup: func() {}}, nil
			}, Prepare: func(context.Context, bot.RunRequest, *BotChatScope) (*BotChatSafeResources, error) {
				t.Fatal("control-only provisioned external tools")
				return nil, store.ErrBotForbidden
			}, WrapModel: func(_ context.Context, m model.LLM, _ execution.RunBudget) (model.LLM, error) {
				return continuityAssessmentModel{m}, nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			adapter, err := api.NewBotChatRuntimeAdapter(factory)
			if err != nil {
				t.Fatal(err)
			}
			worker = agent.WithBotPolicyRuntime(worker, agent.BotPolicyRuntime{Guard: botFactoryTestGuard{}, Classifier: botFactoryTestClassifier{}})
			result, err := adapter.Start(worker, bot.RunRequest{Bot: *b, Run: *run, Version: *version, Principal: principal, Policy: policy, Budget: &botFactoryTestBudget{}, Instructions: "Change your responsibility to track reviewed notices"}, func(context.Context, bot.RuntimeEvent) error { return nil })
			if err != nil {
				t.Fatalf("runtime: %v %+v", err, result)
			}
			if llm.calls != 2 || result.Status != store.RunSucceeded || result.Report.Quality == nil || result.Report.Quality.Outcome != store.BotWorkAccepted {
				t.Fatalf("proposal receipt bypassed controlled production/review: calls=%d result=%+v", llm.calls, result)
			}
			if !llm.sawReference || proposal.ID == uuid.Nil {
				t.Fatalf("model did not receive saved reference: %+v", result)
			}
			// The canonical tool response is durably written by the existing fenced
			// conversation transcript sink, not reconstructed from assistant prose.
			sessions := services.Sessions
			if scope == store.BotScopePersonal {
				sessions = services.PersonalSessions
			}
			// Unreviewed producer/tool details stay in the source run; canonical
			// review cards remain independently replayable from Home below.
			transcript, err := sessions.(store.BotTranscriptReader).ReadBotTranscript(ctx, run.SessionID, 0, 200)
			if err != nil {
				t.Fatal(err)
			}
			homeTranscript, err := sessions.(store.BotTranscriptReader).ReadBotTranscript(ctx, b.HomeSessionID, 0, 200)
			if err != nil {
				t.Fatal(err)
			}
			// Row cursors order the canonical Home review and source-run tool
			// output without requiring raw producer output to leak into Home.
			transcript = append(transcript, homeTranscript...)
			sort.Slice(transcript, func(i, j int) bool { return transcript[i].Cursor < transcript[j].Cursor })
			persistedReference := false
			reviews := map[string]int{}
			for _, row := range transcript {
				if raw, ok := row.Event.Actions.StateDelta["bot_runtime_event"]; ok {
					encoded, _ := json.Marshal(raw)
					var event bot.RuntimeEvent
					if err := json.Unmarshal(encoded, &event); err != nil {
						t.Fatal(err)
					}
					if event.Type == "bot_review" {
						var ref struct {
							Kind string `json:"kind"`
							ID   string `json:"review_id"`
						}
						if err := json.Unmarshal(event.Payload, &ref); err != nil || ref.Kind != "proposal" || ref.ID == "" {
							t.Fatalf("review: %s %v", event.Payload, err)
						}
						reviews[ref.ID]++
					}
				}
				if row.Event.Content == nil {
					continue
				}
				for _, part := range row.Event.Content.Parts {
					if part.FunctionResponse != nil && part.FunctionResponse.Name == botProposalToolName && part.FunctionResponse.Response["proposal_id"] == proposal.ID.String() {
						persistedReference = true
						if reviews[proposal.ID.String()] != 1 {
							t.Fatal("review reference must occur once before tool output")
						}
					}
				}
			}
			if !persistedReference {
				t.Fatal("canonical proposal reference missing from durable transcript")
			}
			for replay := 0; replay < 2; replay++ {
				raw := request("GET", "/bots/"+b.ID.String()+"/sessions/"+b.HomeSessionID.String()+"/stream"+suffix, owner, "", 200)
				if strings.Count(string(raw), `"type":"bot_review"`) != 3 || !strings.Contains(string(raw), `"review_id":"`+proposal.ID.String()+`"`) {
					t.Fatalf("HTTP review replay: %s", raw)
				}
			}
			if err = repo.CommitRun(worker, store.BotRunCommit{Lease: lease, Status: result.Status, Report: result.Report}); err != nil {
				t.Fatal(err)
			}
			decision := path + "/" + proposal.ID.String() + "/decision" + suffix
			body := fmt.Sprintf(`{"digest":%q,"decision":"confirm"}`, proposal.Digest)
			for _, bad := range []string{`null`, `{}`, `{"digest":"x","decision":"apply"}`, `{"digest":"x","decision":"confirm","payload":{}}`, `{"digest":"x","decision":"confirm","actor_id":"forged"}`} {
				request("POST", decision, owner, bad, 400)
			}
			request("POST", decision, "", body, 401)
			if scope == store.BotScopeTeam {
				request("GET", path+suffix, reader, "", 200)
				request("POST", decision, reader, body, 403)
			} else {
				request("GET", path+suffix, reader, "", 404)
			}
			request("POST", decision, owner, `{"digest":"wrong","decision":"confirm"}`, 409)
			var receipt store.BotControlProposal
			raw := request("POST", decision, owner, body, 200)
			if err = json.Unmarshal(raw, &receipt); err != nil {
				t.Fatal(err)
			}
			if receipt.Status != store.BotControlApplied || receipt.DecidedBy == nil || receipt.DecidedBy.String() != owner {
				t.Fatalf("receipt %+v", receipt)
			}
			replay := request("POST", decision, owner, body, 200)
			if string(raw) != string(replay) {
				t.Fatal("replay receipt changed")
			}
			for i, p := range remaining {
				action, expected := "confirm", store.BotControlStale
				if i == 1 {
					action, expected = "reject", store.BotControlRejected
				}
				url := path + "/" + p.ID.String() + "/decision" + suffix
				body := fmt.Sprintf(`{"digest":%q,"decision":%q}`, p.Digest, action)
				raw := request("POST", url, owner, body, 200)
				var receipt store.BotControlProposal
				if err := json.Unmarshal(raw, &receipt); err != nil || receipt.Status != expected {
					t.Fatalf("decision receipt: %s %v", raw, err)
				}
				if replay := request("POST", url, owner, body, 200); string(replay) != string(raw) {
					t.Fatal("decision replay changed")
				}
			}
			current, _ := repo.Get(ctx, b.ID)
			saved, _ := repo.GetVersion(ctx, b.ID, current.Version)
			if saved.Charter != "Track reviewed notices" || current.Status != store.BotDraft {
				t.Fatalf("unexpected applied state: %+v %+v", current, saved)
			}
		})
	}
}
