package api_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/daemon"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/launcher"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/scheduler"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"google.golang.org/adk/model"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
	"google.golang.org/genai"
)

type journeyNotifier struct {
	url    string
	client *http.Client
}

func (n journeyNotifier) Send(ctx context.Context, q bot.NotificationRequest) (bot.NotificationReceipt, error) {
	raw, err := json.Marshal(q)
	if err != nil {
		return bot.NotificationReceipt{}, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", n.url, strings.NewReader(string(raw)))
	if err != nil {
		return bot.NotificationReceipt{}, err
	}
	resp, err := n.client.Do(req)
	if err != nil {
		return bot.NotificationReceipt{}, err
	}
	defer resp.Body.Close()
	var receipt bot.NotificationReceipt
	err = json.NewDecoder(resp.Body).Decode(&receipt)
	return receipt, err
}

type journeyHomeBackend struct{ sandbox.Backend }

func (journeyHomeBackend) EnsureBotHome(_ context.Context, s sandbox.BotHomeScope) (*sandbox.BotHomeBinding, error) {
	return &sandbox.BotHomeBinding{Scope: s, Backend: "controlled", Ref: "journey-home"}, nil
}

type journeyPool struct{ sandbox.ToolNodePool }

func (journeyPool) SetBotHome(string, *sandbox.BotHomeBinding) error { return nil }

type journeyModel struct {
	report bool
	calls  *atomic.Int64
}

func (*journeyModel) Name() string { return "journey-model" }
func (m *journeyModel) GenerateContent(_ context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		m.calls.Add(1)
		if response, ok := journeyWorkAssessment(req); ok {
			yield(response, nil)
			return
		}
		part := &genai.Part{Text: "```astonish-report\npath: reports/receipt.md\n```"}
		if !m.report {
			m.report = true
			part = &genai.Part{FunctionCall: &genai.FunctionCall{ID: uuid.NewString(), Name: "read_file", Args: map[string]any{"body": "exact"}}}
		}
		yield(&model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{part}}, UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 5, CandidatesTokenCount: 3, TotalTokenCount: 8}}, nil)
	}
}

// Controlled assessor validates the actual receipt body; it never bypasses the
// production controller or writes a verdict directly to the store.
func journeyWorkAssessment(req *model.LLMRequest) (*model.LLMResponse, bool) {
	if req.Config == nil || req.Config.SystemInstruction == nil || len(req.Config.SystemInstruction.Parts) == 0 || len(req.Contents) != 1 {
		return nil, false
	}
	prompt := req.Config.SystemInstruction.Parts[0].Text
	var output any
	if strings.HasPrefix(prompt, "BOT_WORK_PREPARE_V1") {
		var contract store.BotWorkContract
		_ = json.Unmarshal([]byte(req.Contents[0].Parts[0].Text), &contract)
		sources := []string{"assignment", "charter"}
		for _, a := range contract.Attachments {
			sources = append(sources, a.SourceID)
		}
		for _, g := range contract.Goals {
			sources = append(sources, "goal:"+g.ID.String())
		}
		output = map[string]any{"criteria": []store.BotWorkCriterion{{ID: "receipt", SourceIDs: sources, Text: "Execute the exact approved action and report the observed receipt."}}}
	} else if strings.HasPrefix(prompt, "BOT_WORK_REVIEW_V1") {
		var work store.BotWorkState
		_ = json.Unmarshal([]byte(req.Contents[0].Parts[0].Text), &work)
		verdict := store.BotCriterionUnverified
		if work.Candidate != nil && work.Candidate.Text == "Exact approved action receipt observed." && len(work.Candidate.Observations) > 0 && len(work.Candidate.Artifacts) == 1 && work.Candidate.Bodies[work.Candidate.Artifacts[0].ID.String()] == work.Candidate.Text {
			verdict = store.BotCriterionSatisfied
		}
		output = store.BotWorkReview{CandidateDigest: work.Candidate.Digest, Criteria: []store.BotCriterionAssessment{{CriterionID: "receipt", Verdict: verdict, Method: store.BotAssessmentModel, EvidenceRefs: []string{"candidate:" + work.Candidate.Digest}, Explanation: "Inspected captured receipt and actual observed effect."}}}
	} else {
		return nil, false
	}
	raw, _ := json.Marshal(output)
	return &model.LLMResponse{Content: genai.NewContentFromText(string(raw), "model"), UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 5, CandidatesTokenCount: 3, TotalTokenCount: 8}}, true
}

type journeyClassifier struct{}

func (journeyClassifier) DescribeAction(_ context.Context, n string, raw json.RawMessage) (execution.ActionDescriptor, error) {
	return execution.ActionDescriptor{Tool: n, Arguments: raw, Effect: execution.EffectSend, ConstraintsVerified: true}, nil
}

type journeyGuard struct {
	repo store.BotStore
	req  bot.RunRequest
}

func (g journeyGuard) Authorize(ctx context.Context, a execution.ActionDescriptor) (execution.PolicyDecision, error) {
	if err := g.req.Budget.Check(ctx); err != nil {
		return execution.PolicyDecision{}, err
	}
	r, err := g.repo.GetRun(ctx, g.req.Bot.ID, g.req.Run.ID)
	if err != nil {
		return execution.PolicyDecision{}, err
	}
	b, err := g.repo.Get(ctx, g.req.Bot.ID)
	if err != nil {
		return execution.PolicyDecision{}, err
	}
	if b.Status != store.BotActive || b.Version != g.req.Version.Version || r.Lease.Fence != g.req.Run.Lease.Fence || r.Status != store.RunRunning {
		return execution.PolicyDecision{}, store.ErrBotConflict
	}
	d := execution.EvaluateRunPolicy(ctx, a, time.Now())
	if d.Kind == execution.PolicyConsumeGrant {
		d.Kind = execution.PolicyAllow
	}
	return d, nil
}

// journeyJobs is only a serialization adapter for the real SQLite scheduler
// store. Dropping one acknowledgement simulates a scheduler crash after durable
// admission, proving the next real 30-second tick cannot admit a second run.
type journeyJobs struct {
	ctx   context.Context
	store store.SchedulerStore
	drop  atomic.Bool
}

func journeyConvert[T any](v any) *T {
	if v == nil {
		return nil
	}
	raw, _ := json.Marshal(v)
	var out T
	_ = json.Unmarshal(raw, &out)
	return &out
}
func (j *journeyJobs) List() []*scheduler.Job {
	var out []*scheduler.Job
	for _, s := range j.store.List(j.ctx) {
		out = append(out, journeyConvert[scheduler.Job](s))
	}
	return out
}
func (j *journeyJobs) Get(id string) *scheduler.Job {
	return journeyConvert[scheduler.Job](j.store.Get(j.ctx, id))
}
func (j *journeyJobs) GetByName(n string) *scheduler.Job {
	return journeyConvert[scheduler.Job](j.store.GetByName(j.ctx, n))
}
func (j *journeyJobs) Add(s *scheduler.Job) error {
	return j.store.Add(j.ctx, journeyConvert[store.ScheduledJob](s))
}
func (j *journeyJobs) Update(s *scheduler.Job) error {
	if s.LastRun != nil && j.drop.CompareAndSwap(false, true) {
		return nil
	}
	return j.store.Update(j.ctx, journeyConvert[store.ScheduledJob](s))
}
func (j *journeyJobs) Remove(id string) error { return j.store.Remove(j.ctx, id) }

// TestBotRuntimeHTTPAcceptance runs real HTTP handlers, SQLite stores, two distinct
// scheduler occurrences plus duplicate replay, the production worker/ChatRunner,
// and reconstructs database handles and worker before exact approval resume.
// Authentication, model, isolated tool and registered home are controlled. Public
// HTTP activation uses the installed production worker readiness checks; no
// activation, home, version or successful run outcomes are seeded with SQL.
func TestBotRuntimeHTTPAcceptance(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	dir := t.TempDir()
	cfg := entstore.Config{DSN: filepath.Join(dir, "platform.db"), DataDir: dir}
	_, db, err := entstore.NewPlatformServices(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	org := &store.Organization{ID: uuid.NewString(), Slug: "runtime-journey", Name: "Journey", Status: "active"}
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
	team := &store.Team{ID: uuid.NewString(), Slug: "journey", Name: "Journey", SchemaName: "team_journey"}
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
	auth := func(c context.Context) context.Context {
		p, e := execution.WithPrincipal(c, execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: owner, OrgSlug: org.Slug, TeamSlug: team.Slug, Authenticated: true})
		if e != nil {
			t.Fatal(e)
		}
		return store.WithTenantContext(p, &store.TenantContext{UserID: owner, OrgSlug: org.Slug, TeamSlug: team.Slug})
	}
	ownerCtx := auth(ctx)
	// Only public handlers are mounted. Repository selection is the same scoped
	// service container supplied by production tenant middleware.
	router := mux.NewRouter()
	router.HandleFunc("/api/studio/sessions/{id}/model", api.PatchSessionModelHandler).Methods("PATCH")
	router.HandleFunc("/api/studio/sessions/{id}/model-status", api.GetSessionModelStatusHandler).Methods("GET")
	root := "/api/studio/bots"
	router.HandleFunc(root, api.BotCreateHandler).Methods("POST")
	router.HandleFunc(root+"/{botID}", api.BotGetHandler).Methods("GET")
	router.HandleFunc(root+"/{botID}/activate", api.BotActivateHandler).Methods("POST")
	router.HandleFunc(root+"/{botID}/configuration", api.BotConfigurationHandler).Methods("PUT")
	router.HandleFunc(root+"/{botID}/runs", api.BotRequestRunHandler).Methods("POST")
	router.HandleFunc(root+"/{botID}/runs", api.BotRunsHandler).Methods("GET")
	router.HandleFunc(root+"/{botID}/runs/{runID}", api.BotRunHandler).Methods("GET")
	router.HandleFunc(root+"/{botID}/approvals", api.BotApprovalsHandler).Methods("GET")
	router.HandleFunc(root+"/{botID}/approvals/{approvalID}/decision", api.BotApprovalDecisionHandler).Methods("POST")
	router.HandleFunc(root+"/{botID}/runs/{runID}/resume", api.BotResumeRunHandler).Methods("POST")
	router.HandleFunc(root+"/{botID}/pause", api.BotPauseHandler).Methods("POST")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		o, e := db.ForOrg(org.Slug)
		if e != nil {
			http.Error(w, e.Error(), 500)
			return
		}
		svc := &store.Services{TenantRouter: db, PersonalBots: o.ForUser(owner).Bots(), PersonalSessions: o.ForUser(owner).Sessions()}
		router.ServeHTTP(w, r.WithContext(store.WithServices(auth(r.Context()), svc)))
	}))
	defer server.Close()
	request := func(method, path, body string, status int) []byte {
		t.Helper()
		r, e := http.NewRequestWithContext(ctx, method, server.URL+path, strings.NewReader(body))
		if e != nil {
			t.Fatal(e)
		}
		r.Header.Set("Content-Type", "application/json")
		resp, e := server.Client().Do(r)
		if e != nil {
			t.Fatal(e)
		}
		defer resp.Body.Close()
		raw, e := io.ReadAll(resp.Body)
		if e != nil {
			t.Fatal(e)
		}
		if resp.StatusCode != status {
			t.Fatalf("%s %s: %d want %d: %s", method, path, resp.StatusCode, status, raw)
		}
		return raw
	}
	var b store.Bot
	if err := json.Unmarshal(request("POST", root, `{"name":"Runtime journey","idempotency_key":"journey","definition":{"charter":"Execute the exact approved action once and report."}}`, 201), &b); err != nil {
		t.Fatal(err)
	}
	path := root + "/" + b.ID.String()
	// Without a configured worker, activation remains explicitly unavailable.
	if raw := request("POST", path+"/activate", fmt.Sprintf(`{"expected_revision":%d,"reason":"Reviewed runtime journey"}`, b.Revision), http.StatusServiceUnavailable); strings.TrimSpace(string(raw)) != "Bot operation unavailable" {
		t.Fatalf("activation readiness contract: %s", raw)
	}
	var draft store.Bot
	if err := json.Unmarshal(request("GET", path, "", http.StatusOK), &draft); err != nil {
		t.Fatal(err)
	}
	if draft.Status != store.BotDraft || draft.Revision != b.Revision {
		t.Fatalf("failed activation mutated draft: %+v", draft)
	}
	personal := ods.ForUser(owner)
	repo := personal.Bots()
	v, err := repo.GetVersion(ownerCtx, b.ID, b.Version)
	if err != nil {
		t.Fatal(err)
	}
	triggerID := uuid.New()
	v.Autonomy.AllowedTools = []string{"read_file"}
	link := &store.UserChannel{ID: uuid.NewString(), UserID: owner, ChannelType: "telegram", ExternalID: "1234567", Enabled: true}
	if err := db.UserChannels().Link(ownerCtx, link); err != nil {
		t.Fatal(err)
	}
	if err := db.UserChannels().Verify(ownerCtx, link.ID); err != nil {
		t.Fatal(err)
	}
	target := store.BotChannelBinding{ID: uuid.MustParse(link.ID), Channel: "telegram", ChatID: link.ExternalID}
	v.Delivery = store.DeliveryPolicy{Notify: store.NotifyOnChange, Preferences: `{"min_changed":2}`, Bindings: []store.BotChannelBinding{target}}
	configuration, err := json.Marshal(store.BotConfigurationRequest{ExpectedRevision: b.Revision, ExpectedVersion: b.Version, Reviewed: true, Reason: "Review isolated tool capability", Autonomy: v.Autonomy, Budget: v.Budget, Delivery: v.Delivery})
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(request("PUT", path+"/configuration", string(configuration), http.StatusOK), &b); err != nil {
		t.Fatal(err)
	}
	if b.Status != store.BotDraft {
		t.Fatal("reviewed configuration activated the draft")
	}
	home := store.BotHomeBinding{ID: uuid.New(), BotID: b.ID, Scope: store.BotScopePersonal, Backend: "controlled", StorageKey: "journey-home", Revision: 1}
	registryCtx, err := execution.WithPrincipal(ctx, execution.Principal{Kind: execution.PrincipalKindService, ClientID: "journey-registry", Authentication: execution.AuthMethod("internal"), Surface: execution.SurfaceCode, Actor: "journey-registry", OrgSlug: org.Slug, TeamSlug: team.Slug, Authenticated: true, Scopes: []string{entstore.BotHomeRegistrationScope}})
	if err != nil {
		t.Fatal(err)
	}
	if err := entstore.RegisterBotHomeBinding(registryCtx, repo, entstore.BotHomeRegistration{BotID: b.ID, ExpectedRevision: b.Revision, Home: home}, func(c context.Context, q entstore.BotHomeRegistration) error {
		actual, e := (journeyHomeBackend{}).EnsureBotHome(c, sandbox.BotHomeScope{OrgID: org.Slug, TeamID: team.Slug, UserID: owner, BotID: b.ID.String()})
		if e != nil {
			return e
		}
		if actual.Ref != q.Home.StorageKey || string(actual.Backend) != q.Home.Backend {
			return store.ErrBotConflict
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	updated, err := repo.Get(ownerCtx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	b = *updated
	updated, err = repo.(store.BotTriggerStore).ChangeTrigger(ownerCtx, store.BotTriggerChange{BotID: b.ID, TriggerID: triggerID, ExpectedRevision: b.Revision, Action: store.BotTriggerSave, Reason: "Review recurring task", Definition: store.TriggerSpec{Kind: store.TriggerCron, Cron: "* * * * *", Timezone: "UTC", Instructions: "Execute saved exact action then report."}})
	if err != nil {
		t.Fatal(err)
	}
	b = *updated
	request("PATCH", "/api/studio/sessions/"+b.HomeSessionID.String()+"/model", `{"provider":"controlled","model":"journey-model"}`, http.StatusOK)
	request("GET", "/api/studio/sessions/"+b.HomeSessionID.String()+"/model-status", "", http.StatusOK)
	jobs := personal.ScheduledJobs()
	var modelCalls, effects atomic.Int64
	physicalIDs := map[uuid.UUID][]string{}
	makeWorker := func(report bool) *daemon.BotWorker {
		w, e := daemon.NewBotWorker(daemon.BotWorkerConfig{Backend: db, Sandbox: journeyHomeBackend{}, Pool: journeyPool{}, WorkerID: uuid.NewString(), ContextWindow: 8192, ModelOptions: provider.BudgetLLMOptions{MaxOutputTokens: 100, SupportsOutputLimit: true}, ResolveModel: func(_ context.Context, p execution.Principal, s store.BotScope, providerName, modelName string) (model.LLM, error) {
			if p.Subject != owner || s != store.BotScopePersonal || providerName != "controlled" || modelName != "journey-model" {
				return nil, store.ErrBotForbidden
			}
			return &journeyModel{report: report, calls: &modelCalls}, nil
		}, Prepare: func(_ context.Context, q bot.RunRequest, s *launcher.BotChatScope) (*launcher.BotChatSafeResources, error) {
			physical, err := os.MkdirTemp(dir, "physical-run-")
			if err != nil {
				return nil, err
			}
			physicalIDs[q.Run.ID] = append(physicalIDs[q.Run.ID], physical)
			action, err := functiontool.New(functiontool.Config{Name: "read_file", Description: "Controlled isolated exact-action fixture"}, func(c tool.Context, args map[string]any) (map[string]any, error) {
				if args["body"] != "exact" {
					return nil, store.ErrBotForbidden
				}
				// A real controlled effect and readback, not a manufactured CommitRun.
				receiptPath := filepath.Join(physical, "receipt")
				if err := os.WriteFile(receiptPath, []byte("exact"), 0600); err != nil {
					return nil, err
				}
				receipt, err := os.ReadFile(receiptPath)
				if err != nil || string(receipt) != "exact" {
					return nil, fmt.Errorf("receipt readback failed: %v", err)
				}
				n := effects.Add(1)
				body := "Exact approved action receipt observed."
				digest := sha256.Sum256([]byte(body))
				evidence := store.BotEvidence{Kind: store.BotEvidenceObservation, Summary: "Exact receipt read back", Source: receiptPath, ObservedAt: time.Now().UTC(), ActorID: q.Bot.OwnerID}
				outcome := bot.ToolOutcome{Verified: true, Artifacts: []bot.ToolReportArtifact{{Artifact: store.BotArtifact{ID: uuid.New(), HomeID: q.Bot.Home.ID, RelativePath: "reports/receipt.md", SHA256: hex.EncodeToString(digest[:]), Title: "Receipt", MediaType: "text/markdown"}, Body: body}}}
				if n == 1 {
					outcome.Changed = []store.BotEvidence{evidence}
					// Independently observe the durable report artifact as a second change.
					reportDir := filepath.Join(dir, "home", "reports")
					if err := os.MkdirAll(reportDir, 0700); err != nil {
						return nil, err
					}
					reportPath := filepath.Join(reportDir, "receipt.md")
					if err := os.WriteFile(reportPath, []byte(body), 0600); err != nil {
						return nil, err
					}
					got, err := os.ReadFile(reportPath)
					if err != nil || string(got) != body {
						return nil, fmt.Errorf("report readback failed: %v", err)
					}
					evidence.Source, evidence.Summary = reportPath, "Report artifact read back"
					outcome.Changed = append(outcome.Changed, evidence)
				} else {
					previous, err := os.ReadFile(filepath.Join(dir, "home", "reports", "receipt.md"))
					if err != nil || string(previous) != body {
						return nil, fmt.Errorf("previous report changed: %v", err)
					}
					evidence.Source = filepath.Join(dir, "home", "reports", "receipt.md")
					evidence.Summary = "Report remains identical to previous occurrence"
					outcome.Unchanged = []store.BotEvidence{evidence}
				}
				bot.RecordToolOutcome(c, c.FunctionCallID(), outcome)
				return map[string]any{"receipt": string(receipt), "report_path": "reports/receipt.md"}, nil
			})
			if err != nil {
				return nil, err
			}
			return &launcher.BotChatSafeResources{Tools: []tool.Tool{action}, WorkspaceDir: physical, PolicyRuntime: agent.BotPolicyRuntime{Classifier: journeyClassifier{}, Guard: journeyGuard{repo: s.Services.PersonalBots, req: q}}, VerifyIsolation: func(c context.Context) error { return c.Err() }, Cleanup: func() {}}, nil
		}})
		if e != nil {
			t.Fatal(e)
		}
		return w
	}
	worker := makeWorker(false)
	if err := json.Unmarshal(request("GET", path, "", http.StatusOK), &draft); err != nil {
		t.Fatal(err)
	}
	if draft.Status != store.BotDraft {
		t.Fatal("runtime construction activated a draft")
	}
	request("POST", path+"/runs", fmt.Sprintf(`{"version":%d,"instructions":"must remain draft","idempotency_key":"draft"}`, b.Version), http.StatusForbidden)
	if err := json.Unmarshal(request("POST", path+"/activate", fmt.Sprintf(`{"expected_revision":%d,"reason":"Reviewed runtime journey"}`, b.Revision), http.StatusOK), &b); err != nil {
		t.Fatal(err)
	}
	if b.Status != store.BotActive {
		t.Fatalf("HTTP activation did not activate: %+v", b)
	}
	request("PATCH", "/api/studio/sessions/"+b.HomeSessionID.String()+"/model", `{"provider":"other","model":"changed"}`, http.StatusConflict)
	if modelCalls.Load() != 0 || effects.Load() != 0 {
		t.Fatal("activation executed model or tool work")
	}
	if runs, err := repo.ListRuns(ownerCtx, b.ID, store.BotPage{}); err != nil || len(runs) != 0 {
		t.Fatalf("activation queued work: %+v %v", runs, err)
	}
	// Recurrence remains a separate reviewed action after activation.
	updated, err = repo.(store.BotTriggerStore).ChangeTrigger(ownerCtx, store.BotTriggerChange{BotID: b.ID, TriggerID: triggerID, ExpectedRevision: b.Revision, Action: store.BotTriggerEnable, Reason: "Enable reviewed recurring task"})
	if err != nil {
		t.Fatal(err)
	}
	b = *updated
	v, err = repo.GetVersion(ownerCtx, b.ID, b.Version)
	if err != nil {
		t.Fatal(err)
	}
	job := jobs.Get(ownerCtx, v.Triggers[0].ScheduleID.String())
	if job == nil || !job.Enabled {
		t.Fatal("reviewed schedule unavailable")
	}
	due := time.Now().Add(-time.Minute).UTC()
	job.NextRun = &due
	if err := jobs.Update(ownerCtx, job); err != nil {
		t.Fatal(err)
	}
	ticks := make(chan scheduler.BotAdmission, 2)
	executor := &scheduler.Executor{BotTrigger: func(c context.Context, q scheduler.BotTriggerRequest) (scheduler.BotAdmission, error) {
		a, e := repo.(store.BotScheduledRunStore).AdmitScheduledRun(auth(c), store.BotScheduledOccurrence{JobID: q.JobID, BotID: q.BotID, Version: q.Version, TriggerID: q.TriggerID, OccurrenceAt: q.OccurrenceAt, IdempotencyKey: q.IdempotencyKey})
		if e != nil {
			return scheduler.BotAdmission{}, e
		}
		status := scheduler.BotAdmissionAdmitted
		if a.Duplicate {
			status = scheduler.BotAdmissionDuplicate
		}
		receipt := scheduler.BotAdmission{Status: status, RunID: a.Run.ID, Acknowledgement: scheduler.BotScheduleAcknowledged}
		ticks <- receipt
		return receipt, nil
	}}
	engine := scheduler.New(&journeyJobs{ctx: ownerCtx, store: jobs}, executor.Execute, scheduler.WithoutBotTriggerDelivery(func(context.Context, *scheduler.Job, string, error) error { t.Error("legacy Bot delivery"); return nil }), nil)
	if _, err := engine.RunNow(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	first := <-ticks
	runID := first.RunID
	advanced := jobs.Get(ownerCtx, job.ID)
	if advanced.NextRun == nil || !advanced.NextRun.After(due) {
		t.Fatal("admission did not atomically acknowledge occurrence")
	}
	// Replay a stale fetched occurrence: durable dedup must not advance twice.
	if _, err := executor.Execute(ctx, journeyConvert[scheduler.Job](job)); err != nil {
		t.Fatal(err)
	}
	replay := <-ticks
	if replay.RunID != runID || replay.Status != scheduler.BotAdmissionDuplicate {
		t.Fatalf("replay: %+v", replay)
	}
	if !jobs.Get(ownerCtx, job.ID).NextRun.Equal(*advanced.NextRun) {
		t.Fatal("duplicate advanced schedule")
	}
	request("POST", path+"/runs", `{"version":1,"instructions":"forged","idempotency_key":"forged","trigger":"cron"}`, 400)
	if err := worker.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	runPath := path + "/runs/" + runID.String()
	var run store.BotRun
	_ = json.Unmarshal(request("GET", runPath, "", 200), &run)
	request("GET", "/api/studio/sessions/"+run.SessionID.String()+"/model-status", "", http.StatusNotFound)
	request("PATCH", "/api/studio/sessions/"+run.SessionID.String()+"/model", `{"provider":"other","model":"changed"}`, http.StatusNotFound)
	if run.Status != store.RunWaitingApproval || effects.Load() != 0 || len(run.Checkpoint) == 0 {
		t.Fatalf("not waiting safely: %+v effects=%d", run, effects.Load())
	}
	assertHomeReports := func(want int) {
		t.Helper()
		o, err := db.ForOrg(org.Slug)
		if err != nil {
			t.Fatal(err)
		}
		events, err := o.ForUser(owner).Sessions().ReadTranscriptEvents(ownerCtx, "astonish", owner, b.HomeSessionID.String())
		if err != nil {
			t.Fatal(err)
		}
		count := 0
		seen := map[string]bool{}
		for _, event := range events {
			if event.InvocationID != "bot-report:"+runID.String() {
				continue
			}
			if seen[event.ID] {
				t.Fatalf("duplicate home report %s", event.ID)
			}
			seen[event.ID] = true
			count++
		}
		if count != want {
			t.Fatalf("home reports=%d want=%d", count, want)
		}
	}
	assertHomeReports(1)
	firstSession := run.SessionID
	firstFence := run.Lease.Fence
	worker.Stop()
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	_, db, err = entstore.NewPlatformServices(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	worker = makeWorker(true)
	defer worker.Stop()
	var pending struct {
		Approvals []store.BotApproval `json:"approvals"`
	}
	if err := json.Unmarshal(request("GET", path+"/approvals", "", 200), &pending); err != nil {
		t.Fatal(err)
	}
	if len(pending.Approvals) != 1 {
		t.Fatalf("approvals=%+v", pending)
	}
	approval := pending.Approvals[0]
	request("POST", path+"/approvals/"+approval.ID.String()+"/decision", fmt.Sprintf(`{"status":"approved","action_digest":%q}`, strings.Repeat("0", 64)), 409)
	request("POST", path+"/approvals/"+approval.ID.String()+"/decision", fmt.Sprintf(`{"status":"approved","action_digest":%q}`, approval.ActionDigest), 200)
	request("POST", runPath+"/resume", fmt.Sprintf(`{"approval_id":%q}`, approval.ID.String()), 202)
	if err := worker.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(request("GET", runPath, "", 200), &run)
	if run.Status != store.RunSucceeded || len(run.Report.Changed) != 2 || len(run.Report.Artifacts) != 1 || !strings.Contains(run.Report.Summary, "receipt observed") || effects.Load() != 1 || run.SessionID != firstSession || run.Lease.Fence <= firstFence {
		t.Fatalf("resume failed: %+v effects=%d", run, effects.Load())
	}
	assertHomeReports(2)
	request("POST", runPath+"/resume", fmt.Sprintf(`{"approval_id":%q}`, approval.ID.String()), 409)
	// Reconstruct publication around the actual committed ChatRunner report.
	o, err := db.ForOrg(org.Slug)
	if err != nil {
		t.Fatal(err)
	}
	personal = o.ForUser(owner)
	repo, jobs = personal.Bots(), personal.ScheduledJobs()
	var sends atomic.Int64
	notifyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var q bot.NotificationRequest
		if err := json.NewDecoder(r.Body).Decode(&q); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		if q.ID == uuid.Nil || q.BotID != b.ID || q.RunID != runID || q.Decision != store.NotificationSend || q.Target != target || q.Body != "Exact approved action receipt observed.\n" {
			t.Errorf("wrong publication: %+v", q)
		}
		sends.Add(1)
		_ = json.NewEncoder(w).Encode(bot.NotificationReceipt{ProviderID: "journey-receipt"})
	}))
	defer notifyServer.Close()
	publish := func(id uuid.UUID) {
		t.Helper()
		publisher := bot.Publisher{Store: repo.(store.BotPublicationStore), Notifier: journeyNotifier{notifyServer.URL, notifyServer.Client()}}
		if _, err := publisher.Publish(ownerCtx, b.ID, id); err != nil {
			t.Fatal(err)
		}
	}
	publish(runID)
	publish(runID)
	if sends.Load() != 1 {
		t.Fatalf("threshold sends=%d", sends.Load())
	}
	// A second distinct persisted occurrence, not a duplicate admission, executes.
	job = jobs.Get(ownerCtx, job.ID)
	secondDue := due.Add(time.Second)
	job.NextRun = &secondDue
	if err := jobs.Update(ownerCtx, job); err != nil {
		t.Fatal(err)
	}
	secondJobs := &journeyJobs{ctx: ownerCtx, store: jobs}
	secondJobs.drop.Store(true)
	engine2 := scheduler.New(secondJobs, executor.Execute, scheduler.WithoutBotTriggerDelivery(nil), nil)
	engine2.Start(ctx)
	var secondID uuid.UUID
	select {
	case receipt := <-ticks:
		if receipt.Status != scheduler.BotAdmissionAdmitted || receipt.RunID == runID {
			t.Fatalf("not a distinct occurrence: %+v", receipt)
		}
		secondID = receipt.RunID
	case <-ctx.Done():
		t.Fatal("second occurrence missing", ctx.Err())
	}
	engine2.Stop()
	worker.Stop()
	worker = makeWorker(false)
	if err := worker.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	secondPath := path + "/runs/" + secondID.String()
	var secondRun store.BotRun
	if err := json.Unmarshal(request("GET", secondPath, "", 200), &secondRun); err != nil {
		t.Fatal(err)
	}
	if secondRun.Status != store.RunWaitingApproval || secondRun.SessionID == firstSession {
		t.Fatalf("second run did not independently execute: %+v", secondRun)
	}
	if err := json.Unmarshal(request("GET", path+"/approvals", "", 200), &pending); err != nil {
		t.Fatal(err)
	}
	var secondApproval store.BotApproval
	for _, a := range pending.Approvals {
		if a.RunID == secondID {
			secondApproval = a
		}
	}
	if secondApproval.ID == uuid.Nil {
		t.Fatal("missing second approval")
	}
	request("POST", path+"/approvals/"+secondApproval.ID.String()+"/decision", fmt.Sprintf(`{"status":"approved","action_digest":%q}`, secondApproval.ActionDigest), 200)
	request("POST", secondPath+"/resume", fmt.Sprintf(`{"approval_id":%q}`, secondApproval.ID.String()), 202)
	worker.Stop()
	worker = makeWorker(true)
	defer worker.Stop()
	if err := worker.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(request("GET", secondPath, "", 200), &secondRun); err != nil {
		t.Fatal(err)
	}
	if secondRun.Status != store.RunSucceeded || len(secondRun.Report.Unchanged) != 1 || len(secondRun.Report.Changed) != 0 || effects.Load() != 2 {
		t.Fatalf("second execution: %+v effects=%d", secondRun, effects.Load())
	}
	if len(physicalIDs[runID]) != 2 || len(physicalIDs[secondID]) != 2 {
		t.Fatalf("physical allocations: %+v", physicalIDs)
	}
	seenPhysical := map[string]bool{}
	for _, ids := range physicalIDs {
		for _, id := range ids {
			if seenPhysical[id] {
				t.Fatal("reused physical execution", id)
			}
			seenPhysical[id] = true
		}
	}
	t.Logf("distinct runs=%s,%s sessions=%s,%s controlled physical IDs=%v", runID, secondID, firstSession, secondRun.SessionID, physicalIDs)
	publish(secondID)
	publish(secondID)
	if sends.Load() != 1 {
		t.Fatalf("unchanged/replay notified: %d", sends.Load())
	}
	before := modelCalls.Load()
	_ = request("GET", runPath, "", 200)
	if modelCalls.Load() != before {
		t.Fatal("history invoked model")
	}
	var current store.Bot
	if err := json.Unmarshal(request("GET", path, "", 200), &current); err != nil {
		t.Fatal(err)
	}
	request("POST", path+"/pause", fmt.Sprintf(`{"expected_revision":%d,"reason":"Journey complete"}`, current.Revision), 200)
	if err := worker.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	if effects.Load() != 2 || modelCalls.Load() != before {
		t.Fatal("pause invoked model or replayed action")
	}
	if err := json.Unmarshal(request("GET", path, "", 200), &current); err != nil {
		t.Fatal(err)
	}
	if current.Status != store.BotPaused {
		t.Fatalf("pause not persisted: %s", current.Status)
	}
	if raw := request("POST", path+"/runs", fmt.Sprintf(`{"version":%d,"instructions":"must not run","idempotency_key":"paused"}`, current.Version), http.StatusForbidden); strings.TrimSpace(string(raw)) != "Bot access forbidden" {
		t.Fatalf("paused admission denial contract: %s", raw)
	}
	if err := worker.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	if effects.Load() != 2 || modelCalls.Load() != before {
		t.Fatal("denied paused admission executed work")
	}
	assertHomeReports(2)
}

type chatFirstModel struct{ requests []string }

func (*chatFirstModel) Name() string { return "chat-first" }
func (m *chatFirstModel) GenerateContent(_ context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	if response, ok := workerWorkAssessment(req, func(w *store.BotWorkState) bool { return w.Candidate.Text == "Acknowledged safely" }); ok {
		return response
	}
	return func(yield func(*model.LLMResponse, error) bool) {
		raw, _ := json.Marshal(req.Contents)
		m.requests = append(m.requests, string(raw))
		yield(&model.LLMResponse{Content: genai.NewContentFromText("Acknowledged safely", "model"), UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 2, CandidatesTokenCount: 2, TotalTokenCount: 4}}, nil)
	}
}

// Real HTTP admission, durable SQLite reopen and the production worker/factory.
// No physical sandbox or external preparation callback is permitted for drafting.
func TestBotChatFirstConversationHTTPAcceptance(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	cfg := entstore.Config{DSN: filepath.Join(dir, "platform.db"), DataDir: dir}
	_, db, err := entstore.NewPlatformServices(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	org := &store.Organization{ID: uuid.NewString(), Slug: "chat-first", Name: "Chat first", Status: "active"}
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
	team := &store.Team{ID: uuid.NewString(), Slug: "chat-first", Name: "Chat first", SchemaName: "team_chat_first"}
	if err := ods.Teams().CreateTeam(ctx, team); err != nil {
		t.Fatal(err)
	}
	owner := uuid.NewString()
	if err := db.Users().Create(ctx, &store.User{ID: owner, Email: owner + "@example.test", DisplayName: "Chat owner", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	if err := db.Organizations().AddMember(ctx, owner, org.ID, "member"); err != nil {
		t.Fatal(err)
	}
	if err := ods.Teams().AddMember(ctx, &store.TeamMembership{UserID: owner, TeamID: team.ID, Role: "member"}); err != nil {
		t.Fatal(err)
	}
	auth := func(c context.Context) context.Context {
		c, err := execution.WithPrincipal(c, execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: owner, OrgSlug: org.Slug, TeamSlug: team.Slug, Authenticated: true})
		if err != nil {
			t.Fatal(err)
		}
		return store.WithTenantContext(c, &store.TenantContext{UserID: owner, OrgSlug: org.Slug, TeamSlug: team.Slug})
	}
	if err := ods.ForUser(owner).PersonalSettings().Save(auth(ctx), &store.PersonalSettings{DefaultProvider: "controlled", DefaultModel: "chat-first"}); err != nil {
		t.Fatal(err)
	}
	router := mux.NewRouter()
	root := "/api/studio/bots"
	router.HandleFunc(root, api.BotCreateHandler).Methods("POST")
	router.HandleFunc(root+"/{botID}/messages", api.BotMessageHandler).Methods("POST")
	router.HandleFunc(root+"/{botID}", api.BotGetHandler).Methods("GET")
	router.HandleFunc("/api/studio/sessions/{id}/model-status", api.GetSessionModelStatusHandler).Methods("GET")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		o, e := db.ForOrg(org.Slug)
		if e != nil {
			http.Error(w, e.Error(), 500)
			return
		}
		svc := &store.Services{TenantRouter: db, PersonalBots: o.ForUser(owner).Bots(), PersonalSessions: o.ForUser(owner).Sessions()}
		router.ServeHTTP(w, r.WithContext(store.WithServices(auth(r.Context()), svc)))
	}))
	defer server.Close()
	request := func(path, body string, status int, out any) {
		t.Helper()
		resp, err := server.Client().Post(server.URL+path, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != status {
			t.Fatalf("%s: %d want %d: %s", path, resp.StatusCode, status, raw)
		}
		if out != nil {
			if err := json.Unmarshal(raw, out); err != nil {
				t.Fatal(err)
			}
		}
	}
	var b store.Bot
	request(root, `{"name":"Chat first","idempotency_key":"create"}`, 201, &b)
	path := root + "/" + b.ID.String() + "/messages"
	statusResponse, err := server.Client().Get(server.URL + "/api/studio/sessions/" + b.HomeSessionID.String() + "/model-status")
	if err != nil {
		t.Fatal(err)
	}
	var modelStatus api.SessionModelStatusResponse
	err = json.NewDecoder(statusResponse.Body).Decode(&modelStatus)
	statusResponse.Body.Close()
	if err != nil || statusResponse.StatusCode != http.StatusOK || modelStatus.EffectiveProvider != "controlled" || modelStatus.EffectiveModel != "chat-first" || modelStatus.PinnedProvider != "" || modelStatus.PinnedModel != "" {
		t.Fatalf("inherited HTTP model status: %d %+v %v", statusResponse.StatusCode, modelStatus, err)
	}
	body := fmt.Sprintf(`{"version":%d,"instructions":"FIRST_UNIQUE","idempotency_key":"first"}`, b.Version)
	var first, replay, second store.BotMessageReceipt
	request(path, body, 202, &first)
	request(path, body, 202, &replay)
	if first.Run.ID != replay.Run.ID || first.UserEventID != replay.UserEventID || first.HistoryCursor != replay.HistoryCursor {
		t.Fatal("unstable durable receipt")
	}
	request(path, fmt.Sprintf(`{"version":%d,"instructions":"SECOND_UNIQUE","idempotency_key":"second"}`, b.Version), 202, &second)
	request(path, strings.Replace(body, "FIRST_UNIQUE", "changed", 1), 409, nil)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	_, db, err = entstore.NewPlatformServices(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	request(path, body, 202, &replay)
	if first.Run.ID != replay.Run.ID {
		t.Fatal("reopen lost receipt")
	}
	llm := &chatFirstModel{}
	worker, err := daemon.NewBotWorker(daemon.BotWorkerConfig{Backend: db, Sandbox: journeyHomeBackend{}, Pool: journeyPool{}, WorkerID: "chat-first", ContextWindow: 8192, ModelOptions: provider.BudgetLLMOptions{MaxOutputTokens: 100, SupportsOutputLimit: true}, ResolveModel: func(_ context.Context, p execution.Principal, scope store.BotScope, providerName, modelName string) (model.LLM, error) {
		if p.Subject != owner || scope != store.BotScopePersonal || providerName != "controlled" || modelName != "chat-first" {
			return nil, store.ErrBotForbidden
		}
		return llm, nil
	}, Prepare: func(context.Context, bot.RunRequest, *launcher.BotChatScope) (*launcher.BotChatSafeResources, error) {
		t.Error("control turn invoked external preparation")
		return nil, store.ErrBotForbidden
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	ods, err = db.ForOrg(org.Slug)
	if err != nil {
		t.Fatal(err)
	}
	repo := ods.ForUser(owner).Bots()
	for _, receipt := range []store.BotMessageReceipt{first, second} {
		run, err := repo.GetRun(auth(ctx), b.ID, receipt.Run.ID)
		if err != nil || run.Status != store.RunSucceeded {
			t.Fatalf("run: %+v %v", run, err)
		}
	}
	if len(llm.requests) != 2 {
		t.Fatalf("model calls=%d", len(llm.requests))
	}
	for _, raw := range llm.requests {
		if strings.Contains(raw, "FIRST_UNIQUE") && !strings.Contains(raw, "SECOND_UNIQUE") {
			continue
		}
		if strings.Count(raw, "SECOND_UNIQUE") != 1 {
			t.Fatalf("duplicate or future turn in context: %s", raw)
		}
	}
	rows, err := ods.ForUser(owner).Sessions().(store.BotTranscriptReader).ReadBotTranscript(auth(ctx), b.HomeSessionID, 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, row := range rows {
		if row.Event != nil {
			counts[row.Event.ID]++
		}
	}
	if counts[first.UserEventID] != 1 || counts[second.UserEventID] != 1 {
		t.Fatalf("canonical user events: %v", counts)
	}
	current, err := repo.Get(auth(ctx), b.ID)
	if err != nil || current.Status != store.BotDraft || current.Home.ID != uuid.Nil {
		t.Fatalf("conversation changed lifecycle/home: %+v %v", current, err)
	}
}
