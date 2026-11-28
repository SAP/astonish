package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/launcher"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

// This model has no store access or knowledge of the test's current phase. Its
// answer is derived exclusively from an explicit scoped memory_get response.
type correctionFollowupModel struct {
	prompts []string
	answers []string
	reviews []string
	// Responses from the shared dispatcher are named execute_tool; match them
	// to the exact invocation rather than guessing a target from that name.
	invocations map[string]string
}

func (*correctionFollowupModel) Name() string { return "controlled" }
func (m *correctionFollowupModel) GenerateContent(_ context.Context, r *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	if response, ok := workerWorkAssessment(r, func(w *store.BotWorkState) bool {
		if w.Candidate.Text == "No delivery fact in active context." {
			return false
		}
		// The review must see current captured evidence, not the producer's
		// answer, its previous prompt, or an obsolete delivery notice.
		var input workerContentInput
		if json.Unmarshal([]byte(r.Contents[0].Parts[0].Text), &input) != nil {
			return false
		}
		for _, body := range input.Sources {
			if strings.Contains(body, w.Candidate.Text) {
				return true
			}
		}
		return false
	}); ok {
		m.reviews = append(m.reviews, r.Contents[0].Parts[0].Text)
		return response
	}
	return func(yield func(*model.LLMResponse, error) bool) {
		callMemory := func(name string, args map[string]any) {
			call := &genai.FunctionCall{ID: uuid.NewString(), Name: name, Args: args}
			if r.Tools[name] == nil {
				if r.Tools["execute_tool"] == nil {
					yield(nil, fmt.Errorf("scoped memory tool %s and shared dispatcher are not advertised", name))
					return
				}
				call.Name, call.Args = "execute_tool", map[string]any{"name": name, "arguments": args}
			}
			if m.invocations == nil {
				m.invocations = make(map[string]string)
			}
			m.invocations[call.ID] = name
			m.answers = append(m.answers, "")
			yield(&model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: call}}}, TurnComplete: true, UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 7, CandidatesTokenCount: 3, TotalTokenCount: 10}}, nil)
		}
		var prompt string
		if r.Config != nil && r.Config.SystemInstruction != nil {
			for _, p := range r.Config.SystemInstruction.Parts {
				prompt += p.Text
			}
		}
		m.prompts = append(m.prompts, prompt)
		foundMemory := false
		var memoryBody string
		for _, content := range r.Contents {
			for _, part := range content.Parts {
				if part.FunctionResponse != nil && m.invocations[part.FunctionResponse.ID] == "memory_get" {
					foundMemory = true
					raw, _ := json.Marshal(part.FunctionResponse.Response)
					memoryBody = string(raw)
				}
			}
		}
		if !foundMemory {
			for _, content := range r.Contents {
				for _, part := range content.Parts {
					if part.FunctionResponse == nil || m.invocations[part.FunctionResponse.ID] != "memory_search" {
						continue
					}
					raw, _ := json.Marshal(part.FunctionResponse.Response["result"])
					var result struct {
						Results []struct {
							Path string `json:"path"`
						} `json:"results"`
					}
					if err := json.Unmarshal(raw, &result); err != nil {
						yield(nil, fmt.Errorf("decode scoped memory search: %w: %s", err, raw))
						return
					}
					if len(result.Results) > 0 {
						callMemory("memory_get", map[string]any{"path": result.Results[0].Path})
						return
					}
					yield(nil, fmt.Errorf("scoped memory search returned no delivery evidence: %v", part.FunctionResponse.Response))
					return
				}
			}
			callMemory("memory_search", map[string]any{"query": "Delivery", "max_results": 6})
			return
		}
		answer := "No delivery fact in active context."
		if strings.Contains(memoryBody, "Delivery is Thursday") {
			answer = "Delivery is Thursday"
		}
		if strings.Contains(memoryBody, "Delivery is Friday") {
			answer = "Delivery is Friday"
		}
		m.answers = append(m.answers, answer)
		yield(&model.LLMResponse{Content: genai.NewContentFromText(answer, "model"), TurnComplete: true, UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 7, CandidatesTokenCount: 3, TotalTokenCount: 10}}, nil)
	}
}

// Real HTTP handlers, scoped SQLite, durable admission, BotWorker and wired Chat.
// Only the model and sandbox isolation are controlled. No fabricated run result
// or manually constructed memory/goal prompt is injected by this fixture.
func TestBotCorrectionFollowupHTTPWorker(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			db, owner := setupChannelResolverFixture(t, "correction", "team", "telegram", "linked")
			ctx := context.Background()
			org, err := db.ForOrg("correction")
			if err != nil {
				t.Fatal(err)
			}
			llm := &correctionFollowupModel{}
			backend := newRegressionHomeBackend(t)
			if err := db.(*entstore.Store).InstallBotContentHomeBackend(backend); err != nil {
				t.Fatal(err)
			}
			worker, err := NewBotWorker(BotWorkerConfig{Backend: db, Sandbox: backend, Pool: workerTestPool{}, WorkerID: "correction-worker", ContextWindow: 32768, ModelOptions: provider.BudgetLLMOptions{MaxOutputTokens: 100, SupportsOutputLimit: true}, ResolveModel: func(context.Context, execution.Principal, store.BotScope, string, string) (model.LLM, error) {
				return mediateDaemonModel(llm), nil
			}, Prepare: func(_ context.Context, q bot.RunRequest, s *launcher.BotChatScope) (*launcher.BotChatSafeResources, error) {
				s.Services.BotFilesFactory = func(c context.Context, svc *store.Services, scope store.BotScope) (store.BotFiles, error) {
					return productionBotFiles(c, backend, svc, scope)
				}
				repo := s.Services.Bots
				if q.Bot.Scope == store.BotScopePersonal {
					repo = s.Services.PersonalBots
				}
				// The shared Chat composition installs scoped Bot memory tools;
				// the catalog pool is controlled and the live memory-only policy stays intact.
				return &launcher.BotChatSafeResources{ChatConfig: &config.AppConfig{}, Catalog: &launcher.ChatCatalogResources{Pool: &titleSkillPool{}}, WorkspaceDir: "/workspace", PolicyRuntime: agent.BotPolicyRuntime{Guard: botLiveGuard{repo: repo, request: q}, Classifier: launcher.BotCatalogueClassifier{}}, VerifyIsolation: func(context.Context) error { return nil }, Cleanup: func() {}}, nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			defer worker.Stop()
			auth, _, err := worker.authenticateOwner(ctx, BotSchedulerScope{"correction", "team", owner})
			if err != nil {
				t.Fatal(err)
			}
			repo := org.ForTeam("team").Bots()
			memories := org.ForTeam("team").Memories()
			sessions := org.ForTeam("team").Sessions()
			jobs := org.ForTeam("team").ScheduledJobs()
			pin := org.ForTeam("team").SetSessionPin
			svc := &store.Services{Bots: repo, Sessions: sessions, TenantRouter: db}
			if scope == store.BotScopePersonal {
				repo = org.ForUser(owner).Bots()
				memories = org.ForUser(owner).Memories()
				sessions = org.ForUser(owner).Sessions()
				jobs = org.ForUser(owner).ScheduledJobs()
				pin = org.ForUser(owner).SetSessionPin
				svc.PersonalBots = repo
				svc.PersonalSessions = sessions
			}
			version := store.DefaultBotVersion()
			version.Charter = "Report delivery facts and track receipt."
			version.Autonomy.AllowedTools = []string{"memory_search", "memory_get"}
			b, err := repo.Create(auth, store.BotCreateRequest{Name: "Delivery", OwnerID: uuid.MustParse(owner), Version: version, IdempotencyKey: "correction"})
			if err != nil {
				t.Fatal(err)
			}
			b, err = worker.provisionHome(auth, repo, b.ID, b.Revision)
			if err != nil {
				t.Fatal(err)
			}
			if err = pin(auth, b.HomeSessionID.String(), "controlled", "model"); err != nil {
				t.Fatal(err)
			}
			b, err = repo.Get(auth, b.ID)
			if err != nil {
				t.Fatal(err)
			}
			httpCtx := store.WithServices(store.WithTenantContext(auth, &store.TenantContext{UserID: owner, OrgSlug: "correction", TeamSlug: "team"}), svc)
			router := mux.NewRouter()
			router.HandleFunc("/bots/{botID}/runs", api.BotRequestRunHandler)
			router.HandleFunc("/bots/{botID}/goals", api.BotSaveGoalHandler)
			router.HandleFunc("/bots/{botID}/goals/{goalID}/complete", api.BotCompleteGoalHandler)
			router.HandleFunc("/bots/{botID}/corrections", api.BotCorrectionHandler)
			router.HandleFunc("/bots/{botID}/corrections/{correctionID}/reconcile", api.BotReconcileCorrectionHandler)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { router.ServeHTTP(w, r.WithContext(httpCtx)) }))
			defer server.Close()
			post := func(path, body string, status int) []byte {
				t.Helper()
				res, e := server.Client().Post(server.URL+"/bots/"+b.ID.String()+path+"?scope="+string(scope), "application/json", strings.NewReader(body))
				if e != nil {
					t.Fatal(e)
				}
				defer res.Body.Close()
				raw, e := io.ReadAll(res.Body)
				if e != nil {
					t.Fatal(e)
				}
				if res.StatusCode != status {
					t.Fatalf("POST %s: %d want %d: %s", path, res.StatusCode, status, raw)
				}
				return raw
			}
			due := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
			var goal store.BotGoal
			if err = json.Unmarshal(post("/goals", fmt.Sprintf(`{"title":"Receive delivery","completion_criteria":"Owner confirms parcel received","due_at":%q}`, due.Format(time.RFC3339)), 201), &goal); err != nil {
				t.Fatal(err)
			}
			botMemory, err := store.NewBotMemoryService(repo, memories, b.ID, scope)
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC().Truncate(time.Second)
			if err = botMemory.Add(auth, store.MemoryEntry{Content: "Delivery is Thursday", Category: "delivery", SessionID: b.HomeSessionID.String(), CreatedBy: owner, Attribution: &store.MemoryAttribution{BotID: b.ID, Source: "delivery-receipt", ObservedAt: now, Status: store.MemoryKnowledgeVerified, Evidence: &store.BotEvidence{Kind: store.BotEvidenceObservation, Source: "delivery-receipt", Summary: "Original delivery notice", ObservedAt: now, ActorID: b.OwnerID}}}); err != nil {
				t.Fatal(err)
			}
			rows, err := botMemory.ListBySession(auth, b.HomeSessionID.String())
			if err != nil || len(rows) != 1 {
				t.Fatalf("seed memory: %+v %v", rows, err)
			}
			memoryID := rows[0].ID
			tid := uuid.New()
			change := func(action store.BotTriggerAction, definition store.TriggerSpec) {
				t.Helper()
				b, err = repo.Get(auth, b.ID)
				if err != nil {
					t.Fatal(err)
				}
				b, err = repo.(store.BotTriggerStore).ChangeTrigger(auth, store.BotTriggerChange{BotID: b.ID, TriggerID: tid, ExpectedRevision: b.Revision, Action: action, Reason: "delivery followup", Definition: definition})
				if err != nil {
					t.Fatal(err)
				}
			}
			change(store.BotTriggerSave, store.TriggerSpec{Kind: store.TriggerGoalDeadline, GoalID: &goal.ID, Instructions: "Check delivery receipt"})
			change(store.BotTriggerEnable, store.TriggerSpec{})
			savedVersion, err := repo.GetVersion(auth, b.ID, b.Version)
			if err != nil {
				t.Fatal(err)
			}
			var scheduleID string
			for _, tr := range savedVersion.Triggers {
				if tr.ID == tid && tr.ScheduleID != nil {
					scheduleID = tr.ScheduleID.String()
				}
			}
			job := jobs.Get(auth, scheduleID)
			if job == nil || !job.Enabled || job.NextRun == nil {
				t.Fatalf("followup not enabled: %+v", job)
			}
			run := func(key, want string) {
				t.Helper()
				var admitted store.BotRun
				if e := json.Unmarshal(post("/runs", fmt.Sprintf(`{"version":%d,"idempotency_key":%q,"instructions":"What is the delivery date and outstanding goal?"}`, b.Version, key), 202), &admitted); e != nil {
					t.Fatal(e)
				}
				before := len(llm.prompts)
				if e := worker.Poll(ctx); e != nil {
					t.Fatal(e)
				}
				saved, e := repo.GetRun(auth, b.ID, admitted.ID)
				if e != nil || saved.Status != store.RunSucceeded {
					t.Fatalf("worker run: %+v %v reviews=%s", saved, e, llm.reviews)
				}
				if len(llm.prompts) != before+3 {
					t.Fatalf("correction followup should need one scoped memory search, one exact memory read and one final producer call: before=%d after=%d", before, len(llm.prompts))
				}
				if saved.Report.Work == nil || saved.Report.Work.Protocol != store.BotWorkProtocolAgentLedV1 {
					t.Fatalf("new followup lost agent work protocol: %+v", saved.Report.Work)
				}
				prompt := llm.prompts[before]
				if !strings.Contains(prompt, "MEMORY.md") {
					t.Errorf("%s: actual Chat system instruction missing compact MEMORY.md", key)
				}
				if strings.Contains(prompt, "Delivery is Thursday") || strings.Contains(prompt, "Delivery is Friday") {
					t.Errorf("%s: full delivery fact bootstrapped instead of explicitly retrieved", key)
				}
				if !strings.Contains(prompt, goal.Title) {
					t.Errorf("%s: actual Chat system instruction missing durable goal %q", key, goal.Title)
				}
				if llm.answers[before+2] != want {
					t.Errorf("%s: model derived %q, want %q", key, llm.answers[before+2], want)
				}
				events, e := sessions.ReadTranscriptEvents(auth, "astonish", owner, admitted.SessionID.String())
				if e != nil {
					t.Fatal(e)
				}
				persisted := false
				for _, event := range events {
					if event.Content == nil || event.Content.Role != "model" {
						continue
					}
					for _, part := range event.Content.Parts {
						persisted = persisted || part.Text == llm.answers[before+2]
					}
				}
				if !persisted {
					t.Fatal("model's context-derived answer absent from durable transcript")
				}
			}
			run("before-correction", "Delivery is Thursday")
			b, err = repo.Get(auth, b.ID)
			if err != nil {
				t.Fatal(err)
			}
			correctionID := uuid.NewString()
			evidence := fmt.Sprintf(`{"source":"owner-review","summary":"Owner reviewed updated delivery notice","observed_at":%q}`, now.Format(time.RFC3339))
			post("/corrections", fmt.Sprintf(`{"id":%q,"expected_revision":%d,"superseded_source":%q,"replacement":"Delivery is Friday","goal_ids":[%q],"evidence":%s}`, correctionID, b.Revision, "memory:"+memoryID, goal.ID, evidence), 202)
			pending, err := botMemory.Get(auth, memoryID)
			if err != nil || pending.Snippet != "Delivery is Thursday" {
				t.Fatalf("record intent applied prematurely: %+v %v", pending, err)
			}
			post("/corrections/"+correctionID+"/reconcile", "", 200)
			post("/corrections/"+correctionID+"/reconcile", "", 200) // exact retry is idempotent
			// Disk corrections retain the source, unlike the legacy in-place
			// protocol whose correction: marker was still considered current.
			// Only the supersession link may change: original text, creator,
			// observation, confidence and evidence must all survive intact.
			expectedHistory := rows[0]
			expectedAttribution := *rows[0].Attribution
			expectedAttribution.SupersededBy = "superseded:correction:" + correctionID
			expectedHistory.Attribution = &expectedAttribution
			historical, err := botMemory.Get(auth, memoryID)
			if err != nil || historical == nil || !reflect.DeepEqual(*historical, expectedHistory) {
				t.Fatalf("original correction evidence not retained: got %+v want %+v: %v", historical, expectedHistory, err)
			}
			replacementID := uuid.NewSHA1(uuid.MustParse(correctionID), []byte("memory")).String()
			corrected, err := botMemory.Get(auth, replacementID)
			if err != nil || corrected == nil || corrected.Snippet != "Delivery is Friday" || corrected.Attribution == nil || corrected.Attribution.Source != "correction:"+correctionID || corrected.Attribution.Evidence == nil || corrected.Attribution.Evidence.ActorID != b.OwnerID {
				t.Fatalf("correction not reconciled with owner evidence: %+v %v", corrected, err)
			}
			active, err := botMemory.Search(auth, "Delivery", 10, 0)
			if err != nil || len(active) != 1 || active[0].ID != replacementID {
				t.Fatalf("active retrieval must select only the reviewed replacement: %+v %v", active, err)
			}
			b, err = repo.Get(auth, b.ID)
			if err != nil {
				t.Fatal(err)
			}
			run("after-correction", "Delivery is Friday")
			// A correction intentionally suspends explicitly affected follow-ups.
			// The owner must review and re-enable the deadline before completion can
			// independently prove that it disables an otherwise live follow-up.
			job = jobs.Get(auth, scheduleID)
			if job.Enabled || job.NextRun != nil {
				t.Fatalf("correction did not suspend affected followup: %+v", job)
			}
			change(store.BotTriggerDisable, store.TriggerSpec{})
			change(store.BotTriggerEnable, store.TriggerSpec{})
			job = jobs.Get(auth, scheduleID)
			if !job.Enabled || job.NextRun == nil || !job.NextRun.Equal(due) {
				t.Fatalf("reviewed followup not restored: %+v", job)
			}
			goals, err := repo.ListGoals(auth, b.ID)
			if err != nil || len(goals) != 1 {
				t.Fatalf("goals: %+v %v", goals, err)
			}
			goal = goals[0]
			post("/goals/"+goal.ID.String()+"/complete", fmt.Sprintf(`{"expected_revision":%d}`, goal.Revision), 400)
			job = jobs.Get(auth, scheduleID)
			if !job.Enabled {
				t.Fatal("unevidenced completion disabled schedule")
			}
			completionEvidence := fmt.Sprintf(`{"source":"owner-receipt","summary":"Owner confirms parcel received","observed_at":%q}`, now.Format(time.RFC3339))
			var completed store.BotGoal
			if err = json.Unmarshal(post("/goals/"+goal.ID.String()+"/complete", fmt.Sprintf(`{"expected_revision":%d,"evidence":%s}`, goal.Revision, completionEvidence), 200), &completed); err != nil {
				t.Fatal(err)
			}
			if completed.Status != store.BotGoalCompleted || len(completed.Evidence) != 1 || completed.Evidence[0].ActorID.String() != owner || completed.Evidence[0].Kind != store.BotEvidenceOwnerConfirmation {
				t.Fatalf("missing owner completion evidence: %+v", completed)
			}
			job = jobs.Get(auth, scheduleID)
			if job.Enabled || job.NextRun != nil {
				t.Fatalf("completed goal still scheduled: %+v", job)
			}
			identity := fmt.Sprintf("%s/%d/%s/%s", b.ID, b.Version, tid, due.Format(time.RFC3339Nano))
			q := store.BotScheduledOccurrence{JobID: scheduleID, BotID: b.ID, Version: b.Version, TriggerID: tid, OccurrenceAt: due, IdempotencyKey: fmt.Sprintf("bot-schedule-v1:%x", sha256.Sum256([]byte(identity)))}
			if admitted, e := repo.(store.BotScheduledRunStore).AdmitScheduledRun(auth, q); !errors.Is(e, store.ErrBotForbidden) || admitted != nil {
				t.Fatalf("completed deadline admitted: %+v %v", admitted, e)
			}
			calls := len(llm.prompts)
			if err = worker.Poll(ctx); err != nil {
				t.Fatal(err)
			}
			if len(llm.prompts) != calls {
				t.Fatal("completed goal caused another model run")
			}
		})
	}
}
