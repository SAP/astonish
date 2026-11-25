package api_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"iter"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/daemon"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/launcher"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/sandbox/docker"
	"github.com/SAP/astonish/pkg/scheduler"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

const newRunStage = "ASTONISH_NEW_RUN_PROCESS_STAGE"
const newRunEvidenceEnv = "ASTONISH_NEW_RUN_PROCESS_EVIDENCE"
const newRunReady = "NEW_RUN_MODEL_PENDING_READY"
const newRunAnswer = "The previous scheduled check was interrupted before it produced an answer. This is a new check, not a replay; no external action was repeated."

type newRunProcessEvidence struct {
	progressProcessEvidence
	ContainerID   string
	ContainerName string
	JobID         string
	TriggerID     uuid.UUID
}

// The local scheduled fixture executes a genuine scheduler occurrence and shared
// worker against SQL. It does not claim physical Docker teardown acceptance.
func TestBotDatabaseScheduledOccurrenceHTTP(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			f := newInteractiveHTTPFixture(t, "", nil, []string{"chat", "tool:execute"}, scope)
			defer f.worker.Stop()
			before := databaseRuntimeFileSnapshot(t, f.dataDir)
			f.db.SetBotActivationReadiness(func(context.Context, store.Bot, store.BotVersion) error { return nil })
			if err := json.Unmarshal(f.request("POST", f.path+"/activate", fmt.Sprintf(`{"expected_revision":%d,"reason":"owned SQL scheduled acceptance"}`, f.b.Revision), 200), &f.b); err != nil {
				t.Fatal(err)
			}
			triggers := f.repo.(store.BotTriggerStore)
			tid := uuid.New()
			b, err := triggers.ChangeTrigger(f.ctx, store.BotTriggerChange{BotID: f.b.ID, TriggerID: tid, ExpectedRevision: f.b.Revision, Action: store.BotTriggerSave, Reason: "owned occurrence", Definition: store.TriggerSpec{Kind: store.TriggerCron, Instructions: "Report major indexes once.", Cron: "* * * * *", Timezone: "UTC"}})
			if err != nil {
				t.Fatal(err)
			}
			b, err = triggers.ChangeTrigger(f.ctx, store.BotTriggerChange{BotID: b.ID, TriggerID: tid, ExpectedRevision: b.Revision, Action: store.BotTriggerEnable, Reason: "owned occurrence"})
			if err != nil {
				t.Fatal(err)
			}
			org, err := f.db.ForOrg("interactive")
			if err != nil {
				t.Fatal(err)
			}
			jobs := org.ForUser(b.OwnerID.String()).ScheduledJobs()
			if scope == store.BotScopeTeam {
				jobs = org.ForTeam("team").ScheduledJobs()
			}
			listed := jobs.List(f.ctx)
			if len(listed) != 1 {
				t.Fatalf("scheduled jobs=%d", len(listed))
			}
			job := listed[0]
			due := time.Now().Add(-3 * time.Minute).UTC()
			job.NextRun = &due
			if err = jobs.Update(f.ctx, job); err != nil {
				t.Fatal(err)
			}
			admitted := newRunTick(t, f.ctx, f.repo, job)
			if admitted.Duplicate || admitted.Coalesced {
				t.Fatal("first occurrence was not fresh")
			}
			duplicate := newRunTick(t, f.ctx, f.repo, job)
			if !duplicate.Duplicate || duplicate.Run.ID != admitted.Run.ID {
				t.Fatal("scheduled occurrence duplicate created another run")
			}
			calls := 0
			f.model.assessment = func(req *model.LLMRequest) (*model.LLMResponse, bool) {
				if seq, ok := workerWorkAssessment(req, func(w *store.BotWorkState) bool {
					return w.Candidate != nil && w.Candidate.Text == "Observed scoped result."
				}); ok {
					for response, err := range seq {
						if err != nil {
							t.Fatal(err)
						}
						return response, true
					}
				}
				calls++
				return &model.LLMResponse{Content: genai.NewContentFromText("Observed scoped result.", "model")}, true
			}
			if err = f.worker.Poll(f.ctx); err != nil {
				t.Fatal(err)
			}
			run, err := f.repo.GetRun(f.ctx, b.ID, admitted.Run.ID)
			if err != nil || run.Status != store.RunSucceeded || calls != 1 {
				t.Fatalf("scheduled run=%+v calls=%d err=%v", run, calls, err)
			}
			home := f.path + "/sessions/" + b.HomeSessionID.String() + "/stream"
			history := f.request("GET", home, "", 200)
			assertProgressReportText(t, string(history), "bot-report:"+run.ID.String(), "Observed scoped result.")
			if !reflect.DeepEqual(history, f.request("GET", home, "", 200)) {
				t.Fatal("scheduled reload changed event IDs/order")
			}
			if err = f.worker.Poll(f.ctx); err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatal("scheduled reload/maintenance re-executed occurrence")
			}
			if after := databaseRuntimeFileSnapshot(t, f.dataDir); !reflect.DeepEqual(before, after) {
				t.Fatal("scheduled runtime wrote files")
			}
		})
	}
}

// This opt-in live harness SIGKILLs an owned OS process inside the production
// worker's model dispatch, then starts a distinct process over the same SQLite.
// Docker is real: a running, owned session container must block cleanup, and the
// production Docker verifier must observe its physical removal before admission.
// Model, Home binding and tool-node provisioning are controlled; no daemon on
// :9393, paid provider, production DB, or pre-existing container is touched.
func TestBotScheduledNewRunProcessDockerAcceptance(t *testing.T) {
	if os.Getenv("ASTONISH_TEST_DOCKER") != "1" {
		t.Skip("set ASTONISH_TEST_DOCKER=1 for actual process/Docker acceptance")
	}
	if stage := os.Getenv(newRunStage); stage != "" {
		newRunProcessChild(t, stage)
		return
	}
	image := os.Getenv("ASTONISH_SANDBOX_IMAGE")
	if image == "" {
		t.Fatal("ASTONISH_SANDBOX_IMAGE must name an existing local image")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	newRunDocker(t, ctx, "image", "inspect", image)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	evidenceFile := filepath.Join(t.TempDir(), "evidence.json")
	cmd := exec.CommandContext(ctx, exe, "-test.run=^TestBotScheduledNewRunProcessDockerAcceptance$", "-test.count=1", "-test.v")
	cmd.Env = append(os.Environ(), newRunStage+"=crash", newRunEvidenceEnv+"="+evidenceFile)
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	ready := make(chan bool, 1)
	var childOutput bytes.Buffer
	go func() {
		scanner := bufio.NewScanner(pipe)
		for scanner.Scan() {
			childOutput.WriteString(scanner.Text() + "\n")
			if scanner.Text() == newRunReady {
				ready <- true
				return
			}
		}
		ready <- false
	}()
	select {
	case ok := <-ready:
		if !ok {
			_ = cmd.Wait()
			t.Fatalf("child failed before model barrier: %s\n%s", stderr.String(), childOutput.String())
		}
	case <-ctx.Done():
		t.Fatal("model barrier timeout")
	}
	evidence := newRunReadEvidence(t, evidenceFile)
	if evidence.PID != cmd.Process.Pid || !strings.Contains(filepath.Base(filepath.Dir(evidence.Dir)), "TestBotScheduledNewRunProcessDockerAcceptance") {
		t.Fatal("unowned child evidence")
	}
	t.Cleanup(func() {
		c, done := context.WithTimeout(context.Background(), 20*time.Second)
		defer done()
		out, e := exec.CommandContext(c, "docker", "ps", "-aq", "--filter", "id="+evidence.ContainerID).CombinedOutput()
		if e != nil {
			t.Errorf("cleanup lookup: %v %s", e, out)
			return
		}
		if strings.TrimSpace(string(out)) != "" {
			newRunDocker(t, c, "rm", "-f", evidence.ContainerID)
		}
		_ = os.RemoveAll(filepath.Dir(evidence.Dir))
	})
	if got := newRunDocker(t, ctx, "inspect", "--format", `{{.State.Running}} {{index .Config.Labels "astonish.new-run-owned"}}`, evidence.ContainerID); got != "true "+evidence.Run.ID.String() {
		t.Fatal("owned physical process missing: " + got)
	}
	if err = cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err = cmd.Wait(); err == nil {
		t.Fatal("expected SIGKILL")
	}
	restart := exec.CommandContext(ctx, exe, "-test.run=^TestBotScheduledNewRunProcessDockerAcceptance$", "-test.count=1", "-test.v")
	restart.Env = append(os.Environ(), newRunStage+"=restart", newRunEvidenceEnv+"="+evidenceFile)
	output, err := restart.CombinedOutput()
	if err != nil {
		t.Fatalf("restart: %v\n%s", err, output)
	}
	t.Logf("SIGKILL old_pid=%d replacement_pid=%d physical_container=%s\n%s", evidence.PID, restart.ProcessState.Pid(), evidence.ContainerID, output)
}

func newRunDocker(t *testing.T, ctx context.Context, args ...string) string {
	t.Helper()
	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}
func newRunReadEvidence(t *testing.T, path string) newRunProcessEvidence {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var e newRunProcessEvidence
	if err = json.Unmarshal(raw, &e); err != nil {
		t.Fatal(err)
	}
	return e
}

// The Home is the existing scoped fixture. Teardown is NOT attested by a fake:
// the concrete Docker backend issues inspect against the canonical session name.
type newRunPhysicalBackend struct {
	composedBrowserBackend
	physical *docker.DockerBackend
}

func (b newRunPhysicalBackend) VerifySessionDestroyed(ctx context.Context, id string) error {
	return b.physical.VerifySessionDestroyed(ctx, id)
}
func newRunBackend(t *testing.T, dir string) newRunPhysicalBackend {
	t.Helper()
	physical, err := docker.New(docker.Config{Sessions: sandbox.NewSessionRegistryFromStore(nil), LayersDir: filepath.Join(dir, "layers"), UppersDir: filepath.Join(dir, "uppers")})
	if err != nil {
		t.Fatal(err)
	}
	return newRunPhysicalBackend{composedBrowserBackend: composedBrowserBackend{synthesisHTTPBackend{files: &synthesisHTTPFiles{root: dir}}}, physical: physical}
}

type newRunModel struct {
	generate func(context.Context, *model.LLMRequest) *model.LLMResponse
}

func (*newRunModel) Name() string { return "new-run-owned" }
func (m *newRunModel) GenerateContent(ctx context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) { yield(m.generate(ctx, req), nil) }
}
func newRunWorker(t *testing.T, db store.PlatformBackend, dir string, mdl model.LLM) *daemon.BotWorker {
	t.Helper()
	pool := &interactiveHTTPPool{dir: dir}
	w, err := daemon.NewBotWorker(daemon.BotWorkerConfig{Backend: db, Sandbox: newRunBackend(t, dir), Pool: pool, WorkerID: uuid.NewString(), ContextWindow: 32768, ModelOptions: provider.BudgetLLMOptions{MaxOutputTokens: 512, SupportsOutputLimit: true}, ResolveModel: func(context.Context, execution.Principal, store.BotScope, string, string) (model.LLM, error) {
		return mdl, nil
	}, Prepare: func(c context.Context, q bot.RunRequest, s *launcher.BotChatScope) (*launcher.BotChatSafeResources, error) {
		files := newComposedHTTPFiles(t, dir, s.Services.PersonalBots, q.Bot, nil)
		files.install(c, q, s)
		resources := &launcher.BotChatSafeResources{WorkspaceDir: "/isolated", ChatConfig: &config.AppConfig{}, Catalog: &launcher.ChatCatalogResources{Pool: pool}, VerifyIsolation: func(c context.Context) error { return c.Err() }, Cleanup: func() {}}
		resources.PolicyRuntime = agent.BotPolicyRuntime{Classifier: composedWeatherClassifier{}, Guard: journeyGuard{repo: s.Services.PersonalBots, req: q}}
		return resources, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	return w
}

// Executor constructs the production occurrence identity and the store performs
// durable admission/acknowledgement. We never rewrite future ticks after restart.
func newRunTick(t *testing.T, ctx context.Context, repo store.BotStore, job *store.ScheduledJob) *store.BotScheduledAdmission {
	t.Helper()
	var result *store.BotScheduledAdmission
	executor := &scheduler.Executor{BotTrigger: func(_ context.Context, q scheduler.BotTriggerRequest) (scheduler.BotAdmission, error) {
		var err error
		result, err = repo.(store.BotScheduledRunStore).AdmitScheduledRun(ctx, store.BotScheduledOccurrence{JobID: q.JobID, BotID: q.BotID, Version: q.Version, TriggerID: q.TriggerID, OccurrenceAt: q.OccurrenceAt, IdempotencyKey: q.IdempotencyKey})
		if err != nil {
			return scheduler.BotAdmission{}, err
		}
		status := scheduler.BotAdmissionAdmitted
		if result.Duplicate {
			status = scheduler.BotAdmissionDuplicate
		}
		if result.Coalesced {
			status = scheduler.BotAdmissionCoalesced
		}
		return scheduler.BotAdmission{Status: status, RunID: result.Run.ID, Acknowledgement: scheduler.BotScheduleAcknowledged}, nil
	}}
	if _, err := executor.Execute(ctx, journeyConvert[scheduler.Job](job)); err != nil {
		t.Fatal(err)
	}
	if result == nil {
		t.Fatal("scheduler did not admit occurrence")
	}
	return result
}
func newRunWaitTick(t *testing.T, ctx context.Context, jobs store.SchedulerStore, id string) *store.ScheduledJob {
	t.Helper()
	job := jobs.Get(ctx, id)
	if job == nil || job.NextRun == nil {
		t.Fatal("future schedule absent")
	}
	wait := time.Until(*job.NextRun) + 20*time.Millisecond
	t.Logf("waiting real future cron occurrence=%s delay=%s", job.NextRun.Format(time.RFC3339Nano), wait)
	if wait > 0 {
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	return job
}

func newRunDispatchRecords(t *testing.T, ctx context.Context, repo store.BotStore, botID, runID uuid.UUID) []store.BotActivity {
	t.Helper()
	activities, err := repo.ListActivity(ctx, botID, 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	var records []store.BotActivity
	for _, a := range activities {
		var dispatch struct {
			Request store.BotProgressBeginDispatchRequest
		}
		if a.RunID != nil && *a.RunID == runID && json.Unmarshal(a.Payload, &dispatch) == nil && dispatch.Request.DispatchID != uuid.Nil {
			records = append(records, a)
		}
	}
	return records
}

func newRunProcessChild(t *testing.T, stage string) {
	if stage == "crash" {
		f := newInteractiveHTTPFixture(t, "", nil, []string{"chat", "tool:execute"}, store.BotScopePersonal)
		v, err := f.repo.GetVersion(f.ctx, f.b.ID, f.b.Version)
		if err != nil {
			t.Fatal(err)
		}
		v.Budget.RuntimePolicy = execution.NewBotProgressRuntimePolicy()
		*f.policy = func(context.Context, store.BotMandateRef) (store.BotMandatePolicy, error) {
			return store.BotMandatePolicy{RuntimePolicy: execution.NewBotProgressRuntimePolicy()}, nil
		}
		raw, _ := json.Marshal(store.BotConfigurationRequest{ExpectedRevision: f.b.Revision, ExpectedVersion: f.b.Version, Reviewed: true, Reason: "Owned process acceptance", Autonomy: v.Autonomy, Budget: v.Budget, Delivery: v.Delivery})
		if err = json.Unmarshal(f.request("PUT", f.path+"/configuration", string(raw), 200), &f.b); err != nil {
			t.Fatal(err)
		}
		*f.policy = nil
		f.db.SetBotActivationReadiness(func(context.Context, store.Bot, store.BotVersion) error { return nil })
		if err = json.Unmarshal(f.request("POST", f.path+"/activate", fmt.Sprintf(`{"expected_revision":%d,"reason":"owned acceptance"}`, f.b.Revision), 200), &f.b); err != nil {
			t.Fatal(err)
		}
		triggers := f.repo.(store.BotTriggerStore)
		tid := uuid.New()
		b, err := triggers.ChangeTrigger(f.ctx, store.BotTriggerChange{BotID: f.b.ID, TriggerID: tid, ExpectedRevision: f.b.Revision, Action: store.BotTriggerSave, Reason: "owned check", Definition: store.TriggerSpec{Kind: store.TriggerCron, Instructions: "Report whether the previous scheduled check completed; do not repeat external actions.", Cron: "* * * * *", Timezone: "UTC"}})
		if err != nil {
			t.Fatal(err)
		}
		b, err = triggers.ChangeTrigger(f.ctx, store.BotTriggerChange{BotID: b.ID, TriggerID: tid, ExpectedRevision: b.Revision, Action: store.BotTriggerEnable, Reason: "owned check"})
		if err != nil {
			t.Fatal(err)
		}
		org, err := f.db.ForOrg("interactive")
		if err != nil {
			t.Fatal(err)
		}
		jobs := org.ForUser(b.OwnerID.String()).ScheduledJobs()
		job := jobs.List(f.ctx)[0]
		due := time.Now().Add(-3 * time.Minute).UTC()
		job.NextRun = &due
		if err = jobs.Update(f.ctx, job); err != nil {
			t.Fatal(err)
		}
		admitted := newRunTick(t, f.ctx, f.repo, job)
		if admitted.Coalesced || admitted.Duplicate {
			t.Fatal("initial occurrence not fresh")
		}
		session := admitted.Run.SessionID.String()
		sum := sha256.Sum256([]byte(session))
		name := fmt.Sprintf("astonish-session-%s-%x", strings.Trim(session[:20], "-"), sum[:6])
		id := newRunDocker(t, f.ctx, "run", "-d", "--pull=never", "--name", name, "--label", "astonish.new-run-owned="+admitted.Run.ID.String(), "--network=none", "--entrypoint", "/bin/sh", os.Getenv("ASTONISH_SANDBOX_IMAGE"), "-c", "exec sleep 600")
		t.Cleanup(func() { newRunDocker(t, context.Background(), "rm", "-f", id) })
		mdl := &newRunModel{generate: func(c context.Context, req *model.LLMRequest) *model.LLMResponse {
			run, e := f.repo.GetRun(f.ctx, b.ID, admitted.Run.ID)
			if e != nil {
				t.Fatal(e)
			}
			if run.Status != store.RunRunning || run.Progress != nil {
				t.Fatalf("not pending model before first checkpoint: %+v", run)
			}
			ledger := newRunDispatchRecords(t, f.ctx, f.repo, b.ID, run.ID)
			if len(ledger) != 1 {
				t.Fatalf("expected exactly one pending model dispatch, got %d", len(ledger))
			}
			var dispatch struct {
				Request    store.BotProgressBeginDispatchRequest
				Settlement *store.BotProgressSettleDispatchRequest
			}
			if e := json.Unmarshal(ledger[0].Payload, &dispatch); e != nil || dispatch.Request.Kind != store.BotProgressDispatchModel || dispatch.Settlement != nil {
				t.Fatalf("not a durable pending model dispatch: %+v %v", dispatch, e)
			}
			evidence := newRunProcessEvidence{progressProcessEvidence: progressProcessEvidence{Dir: f.dataDir, Bot: *b, Run: *run, PID: os.Getpid(), Scope: store.BotScopePersonal}, ContainerID: id, ContainerName: name, JobID: job.ID, TriggerID: tid}
			raw, e := json.Marshal(evidence)
			if e != nil {
				t.Fatal(e)
			}
			if e = os.WriteFile(os.Getenv(newRunEvidenceEnv), raw, 0600); e != nil {
				t.Fatal(e)
			}
			fmt.Println(newRunReady)
			<-c.Done()
			return nil
		}}
		worker := newRunWorker(t, f.db, f.dataDir, mdl)
		if err = worker.Poll(f.ctx); err != nil {
			t.Fatal(err)
		}
		stopped, readErr := f.repo.GetRun(f.ctx, b.ID, admitted.Run.ID)
		t.Fatalf("worker returned without SIGKILL: run=%+v failure=%+v read_error=%v", stopped, stopped.Report.Failure, readErr)
	}
	if stage != "restart" {
		t.Fatal("unknown stage")
	}
	e := newRunReadEvidence(t, os.Getenv(newRunEvidenceEnv))
	if e.PID == os.Getpid() {
		t.Fatal("same OS process")
	}
	_, db, err := entstore.NewPlatformServices(context.Background(), entstore.Config{DSN: filepath.Join(e.Dir, "platform.db"), DataDir: e.Dir})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	principal := execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodOAuth, Surface: execution.SurfaceStudio, Subject: e.Bot.OwnerID.String(), OrgSlug: "interactive", TeamSlug: "team", Authenticated: true, Scopes: []string{"chat", "tool:execute"}}
	ctx, err := execution.WithPrincipal(context.Background(), principal)
	if err != nil {
		t.Fatal(err)
	}
	ctx = store.WithTenantContext(ctx, &store.TenantContext{UserID: principal.Subject, OrgSlug: principal.OrgSlug, TeamSlug: principal.TeamSlug})
	org, err := db.ForOrg("interactive")
	if err != nil {
		t.Fatal(err)
	}
	personal := org.ForUser(principal.Subject)
	repo := personal.Bots()
	jobs := personal.ScheduledJobs()
	calls := 0
	mdl := &newRunModel{generate: func(_ context.Context, req *model.LLMRequest) *model.LLMResponse {
		if req.Config != nil && req.Config.SystemInstruction != nil && len(req.Config.SystemInstruction.Parts) > 0 && strings.HasPrefix(req.Config.SystemInstruction.Parts[0].Text, "BOT_WORK_REVIEW_V1") {
			var in struct {
				store.BotWorkState
				Items []struct {
					ID   string `json:"id"`
					Text string `json:"text"`
				} `json:"content_items"`
			}
			if err := json.Unmarshal([]byte(req.Contents[0].Parts[0].Text), &in); err != nil || in.Candidate == nil || in.Candidate.Text != newRunAnswer {
				t.Fatalf("unexpected reviewed candidate: %+v %v", in, err)
			}
			criteria := []store.BotCriterionAssessment{}
			for _, c := range in.Criteria {
				criteria = append(criteria, store.BotCriterionAssessment{CriterionID: c.ID, Verdict: store.BotCriterionSatisfied, Method: store.BotAssessmentModel, EvidenceRefs: []string{"candidate:" + in.Candidate.Digest}, Explanation: "Exact response communicates the independently asserted interruption and fresh check boundary."})
			}
			items := []any{}
			for _, item := range in.Items {
				if item.Text != newRunAnswer {
					t.Fatal("unexpected review content")
				}
				items = append(items, map[string]any{"item_id": item.ID, "claim_type": "nonfactual", "execution_sources": []any{}, "relevance": "satisfied", "support": "satisfied", "explanation": "Run-status limitation, not an external factual or tool-success claim.", "sources": []any{}})
			}
			raw, _ := json.Marshal(map[string]any{"candidate_digest": in.Candidate.Digest, "criteria": criteria, "content_assessments": items})
			return &model.LLMResponse{Content: genai.NewContentFromText(string(raw), "model"), UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 5, CandidatesTokenCount: 3, TotalTokenCount: 8}}
		}
		calls++
		raw, _ := json.Marshal(req.Config.SystemInstruction)
		for _, want := range []string{e.Run.ID.String(), e.TriggerID.String(), `\"prior_run\"`, `\"status\":\"interrupted\"`, "distinct newly admitted run", "not a retry or replay"} {
			if !strings.Contains(string(raw), want) {
				t.Errorf("provider-bound prompt missing %q", want)
			}
		}
		return &model.LLMResponse{Content: genai.NewContentFromText(newRunAnswer, "model"), TurnComplete: true, UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 31, CandidatesTokenCount: 29, TotalTokenCount: 60}}
	}}
	worker := newRunWorker(t, db, e.Dir, mdl)
	defer worker.Stop()
	if wait := time.Until(e.Run.Lease.ExpiresAt) + 50*time.Millisecond; wait > 0 {
		t.Logf("honoring actual lease expiry %s", wait)
		time.Sleep(wait)
	}
	if err = worker.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	interrupted, err := repo.GetRun(ctx, e.Bot.ID, e.Run.ID)
	if err != nil || interrupted.Status != store.RunInterrupted || interrupted.Progress != nil {
		t.Fatalf("recovery: %+v %v", interrupted, err)
	}
	oldLedger := newRunDispatchRecords(t, ctx, repo, e.Bot.ID, e.Run.ID)
	if err = worker.Poll(ctx); err == nil {
		t.Fatal("live physical container falsely accepted as torn down")
	}
	held := newRunTick(t, ctx, repo, jobs.Get(ctx, e.JobID))
	if !held.Coalesced || held.Run.ID != e.Run.ID {
		t.Fatal("live old container did not hold next occurrence")
	}
	if calls != 0 {
		t.Fatal("recovery replayed old model")
	}
	// Teardown is explicit here, matching the independent sandbox lifecycle. The
	// worker only verifies destruction, and must not delete arbitrary foreign work.
	if got := newRunDocker(t, ctx, "inspect", "--format", `{{index .Config.Labels "astonish.new-run-owned"}}`, e.ContainerID); got != e.Run.ID.String() {
		t.Fatal("foreign container")
	}
	newRunDocker(t, ctx, "rm", "-f", e.ContainerID)
	if err = worker.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	if err = newRunBackend(t, e.Dir).VerifySessionDestroyed(ctx, e.Run.SessionID.String()); err != nil {
		t.Fatal(err)
	}
	future := jobs.Get(ctx, e.JobID)
	if future.NextRun == nil || !future.NextRun.After(time.Now()) {
		t.Fatal("catchup backlog or missing future tick")
	}
	runs, err := repo.ListRuns(ctx, e.Bot.ID, store.BotPage{Limit: 100})
	if err != nil || len(runs) != 1 {
		t.Fatalf("recovery queued catchup runs: %d %v", len(runs), err)
	}
	job := newRunWaitTick(t, ctx, jobs, e.JobID)
	next := newRunTick(t, ctx, repo, job)
	if next.Coalesced || next.Duplicate || next.Run.ID == e.Run.ID || next.Run.SessionID == e.Run.SessionID || next.Run.TriggerID != e.TriggerID {
		t.Fatalf("not a distinct same-trigger future run: %+v", next)
	}
	replay := newRunTick(t, ctx, repo, job)
	if !replay.Duplicate || replay.Run.ID != next.Run.ID {
		t.Fatal("duplicate cron replay admitted a backlog")
	}
	if err = worker.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	after, err := repo.GetRun(ctx, e.Bot.ID, next.Run.ID)
	if err != nil || after.Status != store.RunSucceeded || after.Report.Summary != newRunAnswer || calls != 1 {
		t.Fatalf("new useful answer: calls=%d run=%+v err=%v", calls, after, err)
	}
	for range 2 {
		if err = worker.Poll(ctx); err != nil {
			t.Fatal(err)
		}
	}
	old, err := repo.GetRun(ctx, e.Bot.ID, e.Run.ID)
	if err != nil || old.Status != store.RunInterrupted || !reflect.DeepEqual(old.Usage, interrupted.Usage) || !reflect.DeepEqual(old.Report, interrupted.Report) {
		t.Fatal("old accounting/report/ledger rewritten", err)
	}
	if !reflect.DeepEqual(oldLedger, newRunDispatchRecords(t, ctx, repo, e.Bot.ID, e.Run.ID)) {
		t.Fatal("old immutable dispatch records rewritten")
	}
	events, err := personal.Sessions().ReadTranscriptEvents(ctx, "astonish", principal.Subject, e.Bot.HomeSessionID.String())
	if err != nil {
		t.Fatal(err)
	}
	publications := 0
	for _, event := range events {
		if event.InvocationID == "bot-report:"+after.ID.String() {
			publications++
		}
	}
	if publications != 1 {
		t.Fatalf("Home final publications=%d", publications)
	}
	runs, err = repo.ListRuns(ctx, e.Bot.ID, store.BotPage{Limit: 100})
	if err != nil || len(runs) != 2 {
		t.Fatal("unexpected catchup/replay runs", err)
	}
	t.Logf("old_run=%s old_status=%s checkpoint_absent=true retained_usage=%+v new_run=%s new_session=%s trigger=%s provider_saw_prior_interrupted=true final_home_publications=%d no_catchup=true actual_docker_teardown=true", old.ID, old.Status, old.Usage, after.ID, after.SessionID, after.TriggerID, publications)
}
