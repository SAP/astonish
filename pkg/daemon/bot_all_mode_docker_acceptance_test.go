package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/scheduler"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

// Extends the unchanged 165-byte/no-Python/24-open/15-second reproduction.
// The only injected component is the deterministic model. Scheduler admission,
// worker, child runner, node transport, retained store and Docker are real.
type allModeDockerFixture struct {
	t          *testing.T
	ctx        context.Context
	db         store.PlatformBackend
	repo       store.BotStore
	sessions   store.SessionStore
	worker     *BotWorker
	home       *sandbox.BotHomeBinding
	bot        store.Bot
	lastRun    store.BotRun
	scope      store.BotScope
	org        store.OrgDataStore
	owner      string
	server     *httptest.Server
	post       func(string, any, int) []byte
	evict      func()
	ownSession func(string)
	// Selects existing authenticated Chat-turn admission for the delegate only;
	// routine mandates remain the scheduler/continuation authority.
	interactiveAdmission func() func()
}

type allModeDockerModel struct {
	t                 *testing.T
	mu                sync.Mutex
	calls, exactReads int
	childID           string
}

func (*allModeDockerModel) Name() string { return "controlled-all-mode-docker" }
func (m *allModeDockerModel) GenerateContent(ctx context.Context, r *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		// Terminal child results also drive the production tool-free knowledge
		// extractor. Keep that genuine auxiliary call separate from the parent/
		// child execution script; it must never emit or consume a tool call.
		if r.Config != nil && r.Config.ResponseMIMEType == "application/json" {
			m.t.Log("ALL_MODE auxiliary_knowledge=no_change tools=0")
			yield(&model.LLMResponse{Content: genai.NewContentFromText(`{"schema_version":1,"kind":"no_change","reason":"retained bytes unchanged","proposals":[]}`, "model"), TurnComplete: true}, nil)
			return
		}
		m.mu.Lock()
		m.calls++
		call := m.calls
		m.mu.Unlock()
		m.t.Logf("ALL_MODE model_call=%d", call)
		if call == 2 || call == 4 || call == 7 {
			found := false
			for _, c := range r.Contents {
				if c == nil {
					continue
				}
				for _, p := range c.Parts {
					if p != nil && p.FunctionResponse != nil && p.FunctionResponse.ID == fmt.Sprintf("all-mode-read-%d", call-1) && betweenCallExactToolResult(p.FunctionResponse.Response) {
						found = true
					}
				}
			}
			if !found {
				m.t.Errorf("model call %d did not receive exact retained bytes", call)
				for _, content := range r.Contents {
					if content != nil {
						for _, part := range content.Parts {
							if part != nil && part.FunctionResponse != nil {
								raw, _ := json.Marshal(part.FunctionResponse)
								m.t.Logf("ALL_MODE_RESPONSE %s", raw)
							}
						}
					}
				}
				yield(nil, fmt.Errorf("exact retained read missing at generation %d", call))
				return
			} else {
				m.mu.Lock()
				m.exactReads++
				m.mu.Unlock()
			}
		}
		if call == 7 {
			yield(&model.LLMResponse{Content: genai.NewContentFromText(betweenCallFixtureText, "model"), TurnComplete: true}, nil)
			return
		}
		if call == 8 {
			found := false
			for _, c := range r.Contents {
				if c == nil {
					continue
				}
				for _, p := range c.Parts {
					if p == nil || p.FunctionResponse == nil || p.FunctionResponse.ID != "all-mode-delegate" {
						continue
					}
					raw, _ := json.Marshal(p.FunctionResponse.Response)
					var result struct {
						Results []struct {
							Status         string `json:"status"`
							ChildSessionID string `json:"child_session_id"`
							Result         string `json:"result"`
						} `json:"results"`
					}
					if json.Unmarshal(raw, &result) == nil && len(result.Results) == 1 && result.Results[0].Status == "success" && strings.Contains(result.Results[0].Result, betweenCallFixtureText) && result.Results[0].ChildSessionID != "" {
						found = true
						m.childID = result.Results[0].ChildSessionID
					}
					if !found {
						m.t.Errorf("actual delegated result missing: %s", raw)
					}
				}
			}
			if !found {
				m.t.Error("parent did not receive joined delegate")
			}
		}
		var fc *genai.FunctionCall
		switch call {
		case 1, 3, 6:
			fc = &genai.FunctionCall{ID: fmt.Sprintf("all-mode-read-%d", call), Name: "read_file", Args: map[string]any{"path": betweenCallFixturePath, "force": true}}
		case 2, 4, 8:
			fc = &genai.FunctionCall{ID: fmt.Sprintf("all-mode-question-%d", call), Name: "ask_user", Args: map[string]any{"prompt": fmt.Sprintf("Read the retained result again after step %d?", call), "kind": "text"}}
		case 5:
			fc = &genai.FunctionCall{ID: "all-mode-delegate", Name: "delegate_tasks", Args: map[string]any{"tasks": []any{map[string]any{"name": "retained-reader", "task": "Read outputs/local-search-results.txt using read_file and return its complete exact content.", "tools": []string{"read_file"}}}}}
		default:
			yield(nil, fmt.Errorf("unexpected all-mode generation %d", call))
			return
		}
		if r.Tools[fc.Name] == nil {
			fc = &genai.FunctionCall{ID: fc.ID, Name: "execute_tool", Args: map[string]any{"name": fc.Name, "arguments": fc.Args}}
		}
		yield(&model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: fc}}}}, nil)
	}
}

func TestBotAllModeRetainedSessionDocker(t *testing.T) {
	if os.Getenv("ASTONISH_TEST_DOCKER") != "1" {
		t.Skip("explicit real Docker opt-in required")
	}
	if os.Getenv("ASTONISH_BOT_HOME_TEST_TEMPLATE") != "" {
		t.Fatal("combined no-Python fixture requires unchanged default template")
	}
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) { runBotNewMessageDockerReproduction(t, true, scope, runAllModeDocker) })
	}
}

func TestBotDelegatedRetainedSessionDocker(t *testing.T) {
	if os.Getenv("ASTONISH_TEST_DOCKER") != "1" {
		t.Skip("explicit real Docker opt-in required")
	}
	if os.Getenv("ASTONISH_BOT_HOME_TEST_TEMPLATE") != "" {
		t.Fatal("combined no-Python fixture requires unchanged default template")
	}
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			runBotNewMessageDockerReproduction(t, true, scope, func(f *allModeDockerFixture) {
				mdl := &allModeDockerModel{t: t, calls: 4}
				f.worker.config.ResolveModel = func(context.Context, execution.Principal, store.BotScope, string, string) (model.LLM, error) {
					return mdl, nil
				}
				// The base reproduction already proves owner continuation and recovery.
				// Start at delegation without re-answering its mandate-bound question.
				runAllModeDelegate(f, mdl, "", 1)
			})
		})
	}
}

func TestAllModeDockerFixtureContract(t *testing.T) {
	if len(betweenCallFixtureText) != 165 {
		t.Fatalf("retained fixture changed: %d bytes", len(betweenCallFixtureText))
	}
	if betweenCallExactToolResult(map[string]any{"content": "1: LOCAL_SEARCH_RESULT_42"}) {
		t.Fatal("marker-only content accepted")
	}
	start := time.Now()
	if betweenCallLatencyError(start, start.Add(16*time.Second), 0) == nil {
		t.Fatal("15-second budget weakened")
	}
}

func TestAllModeDockerModelScript(t *testing.T) {
	m := &allModeDockerModel{t: t}
	for response, err := range m.GenerateContent(t.Context(), &model.LLMRequest{Config: &genai.GenerateContentConfig{ResponseMIMEType: "application/json"}}, false) {
		if err != nil || response == nil || response.Content == nil || len(response.Content.Parts) != 1 || response.Content.Parts[0].FunctionCall != nil || !strings.Contains(response.Content.Parts[0].Text, `"kind":"no_change"`) {
			t.Fatalf("tool-free auxiliary extraction: response=%+v err=%v", response, err)
		}
	}
	if m.calls != 0 {
		t.Fatal("auxiliary extraction consumed execution script")
	}
	prompts := make(map[string]bool)
	for call := 1; call <= 8; call++ {
		r := &model.LLMRequest{}
		if call == 2 || call == 4 || call == 7 {
			r.Contents = []*genai.Content{{Role: "user", Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{ID: fmt.Sprintf("all-mode-read-%d", call-1), Name: "read_file", Response: map[string]any{"content": "1: " + betweenCallFixtureText}}}}}}
		}
		if call == 8 {
			r.Contents = []*genai.Content{{Role: "user", Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{ID: "all-mode-delegate", Name: "delegate_tasks", Response: map[string]any{"results": []any{map[string]any{"status": "success", "child_session_id": "actual-reader", "result": betweenCallFixtureText}}}}}}}}
		}
		count := 0
		for response, err := range m.GenerateContent(t.Context(), r, false) {
			if err != nil || response == nil || response.Content == nil {
				t.Fatalf("generation %d: response=%+v err=%v", call, response, err)
			}
			count++
			part := response.Content.Parts[0]
			if call == 7 {
				if part.Text != betweenCallFixtureText || !response.TurnComplete {
					t.Fatal("child must return complete exact bytes")
				}
			} else if part.FunctionCall == nil || part.FunctionCall.Name != "execute_tool" {
				t.Fatalf("generation %d bypassed shared discovery dispatch", call)
			}
			if call == 2 || call == 4 || call == 8 {
				args := part.FunctionCall.Args["arguments"].(map[string]any)
				prompt, _ := args["prompt"].(string)
				if prompt == "" || prompts[prompt] {
					t.Fatalf("generation %d reused a question identity: %q", call, prompt)
				}
				prompts[prompt] = true
			}
		}
		if count != 1 {
			t.Fatalf("generation %d yielded %d responses", call, count)
		}
	}
	if m.calls != 8 || m.exactReads != 3 || m.childID != "actual-reader" {
		t.Fatalf("incomplete script: %+v", m)
	}
}

func runAllModeDocker(f *allModeDockerFixture) {
	runAllModeDockerWithDenial(f, "")
}

// Exercise lifecycle and current-owner authority denial against an actual scheduled Docker
// run after its complete owner answer has been accepted into the database.
func TestBotDatabaseContinuationDenialsDocker(t *testing.T) {
	if os.Getenv("ASTONISH_TEST_DOCKER") != "1" {
		t.Fatal("database denial acceptance requires real Docker; skips are not evidence")
	}
	if os.Getenv("ASTONISH_BOT_HOME_TEST_TEMPLATE") != "" {
		t.Fatal("denial acceptance requires unchanged default template")
	}
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, denial := range []string{"paused", "stopped", "revoked"} {
			t.Run(string(scope)+"/"+denial, func(t *testing.T) {
				runBotNewMessageDockerReproduction(t, true, scope, func(f *allModeDockerFixture) {
					runAllModeDockerWithDenial(f, denial)
				})
			})
		}
	}
}

func runAllModeDockerWithDenial(f *allModeDockerFixture, denial string) {
	t, ctx := f.t, f.ctx
	mdl := &allModeDockerModel{t: t}
	// The controlled model has a finite output bound, required by activation's
	// provider-budget preflight as well as actual scheduled generation.
	f.worker.config.ModelOptions.MaxOutputTokens = 4096
	f.worker.config.ResolveModel = func(context.Context, execution.Principal, store.BotScope, string, string) (model.LLM, error) {
		return mdl, nil
	}
	current, err := f.repo.Get(ctx, f.bot.ID)
	if err != nil {
		t.Fatal(err)
	}
	version, err := f.repo.GetVersion(ctx, f.bot.ID, current.Version)
	if err != nil {
		t.Fatal(err)
	}
	selectedTemplate := version.SandboxTemplate
	base := fmt.Sprintf("%s/bots/%s", f.server.URL, f.bot.ID)
	suffix := "?scope=" + string(f.scope)
	// Title-created Bots intentionally have no autonomous tool grants. Consent
	// must use the same reviewed HTTP configuration as the owner-facing UI.
	autonomy := version.Autonomy
	autonomy.AllowedTools = []string{"read_file", "ask_user"}
	autonomy.AllowedPaths = []string{"outputs"}
	body, err := json.Marshal(store.BotConfigurationRequest{ExpectedRevision: current.Revision, ExpectedVersion: current.Version, Reviewed: true, Reason: "Owner permits only retained output reads and questions for this disposable schedule", Autonomy: autonomy, Budget: version.Budget, Delivery: version.Delivery})
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, base+"/configuration"+suffix, strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := f.server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	configured, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("reviewed configuration: status=%d body=%s read=%v close=%v", response.StatusCode, configured, readErr, closeErr)
	}
	if err = json.Unmarshal(configured, &current); err != nil {
		t.Fatal(err)
	}
	version, err = f.repo.GetVersion(ctx, f.bot.ID, current.Version)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.worker.activationReady(ctx, *current, *version); err != nil {
		t.Fatalf("activation readiness: %v", err)
	}
	raw := f.post(base+"/activate"+suffix, map[string]any{"expected_revision": current.Revision, "reason": "Owner authorizes the disposable read-only scheduled lifecycle"}, 200)
	if err = json.Unmarshal(raw, &current); err != nil {
		t.Fatal(err)
	}
	// Trusted scheduler setup, explicitly not presented as an HTTP route. This
	// exercises normal trigger validation/versioning; never writes raw SQL/content.
	triggers := f.repo.(store.BotTriggerStore)
	tid := uuid.New()
	current, err = triggers.ChangeTrigger(ctx, store.BotTriggerChange{BotID: f.bot.ID, TriggerID: tid, ExpectedRevision: current.Revision, Action: store.BotTriggerSave, Reason: "Owned retained-file read", Definition: store.TriggerSpec{Kind: store.TriggerCron, Instructions: "Read outputs/local-search-results.txt and ask whether to read it again.", Cron: "0 9 * * *", Timezone: "UTC"}})
	if err != nil {
		t.Fatal(err)
	}
	current, err = triggers.ChangeTrigger(ctx, store.BotTriggerChange{BotID: f.bot.ID, TriggerID: tid, ExpectedRevision: current.Revision, Action: store.BotTriggerEnable, Reason: "Owner enables read-only schedule"})
	if err != nil {
		t.Fatal(err)
	}
	jobs := f.org.ForTeam("team").ScheduledJobs()
	if f.scope == store.BotScopePersonal {
		jobs = f.org.ForUser(f.owner).ScheduledJobs()
	}
	list := jobs.List(ctx)
	if len(list) != 1 {
		t.Fatalf("expected one test schedule: %+v", list)
	}
	job := list[0]
	due := time.Now().UTC().Add(-time.Second)
	job.NextRun = &due
	if err = jobs.Update(ctx, job); err != nil {
		t.Fatal(err)
	}
	mts := NewMultiTenantScheduler(f.db, &scheduler.Executor{}, nil, nil)
	mts.SetBotSchedulerCallbacks(f.worker.SchedulerCallbacks())
	if f.scope == store.BotScopePersonal {
		mts.executePersonalJob(ctx, job, f.home.Scope.OrgID, f.org, f.org.ForUser(f.owner), jobs, f.owner)
	} else {
		mts.executeTeamJob(ctx, job, f.home.Scope.OrgID, "team", f.org, f.org.ForTeam("team"), jobs)
	}
	runs, err := f.repo.ListRuns(ctx, f.bot.ID, store.BotPage{})
	if err != nil {
		t.Fatal(err)
	}
	var scheduled *store.BotRun
	for i := range runs {
		if runs[i].Trigger == store.TriggerCron {
			if scheduled != nil {
				t.Fatal("duplicate scheduled occurrence")
			}
			scheduled = &runs[i]
		}
	}
	if scheduled == nil || scheduled.IsInteractiveChat() {
		t.Fatalf("real autonomous occurrence missing: %+v", scheduled)
	}
	f.ownSession(scheduled.SessionID.String())
	if err = f.worker.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	saved, err := f.repo.GetRun(ctx, f.bot.ID, scheduled.ID)
	if err != nil || saved.Status != store.RunWaitingApproval || mdl.calls != 2 || mdl.exactReads != 1 {
		t.Fatalf("scheduled read failed: run=%+v err=%v calls=%d exact=%d", saved, err, mdl.calls, mdl.exactReads)
	}
	f.assertPhysical("scheduled", selectedTemplate)
	approvals, err := f.repo.ListApprovals(ctx, f.bot.ID, store.BotPage{})
	if err != nil {
		t.Fatal(err)
	}
	var question *store.BotApproval
	for i := range approvals {
		if approvals[i].RunID == scheduled.ID && approvals[i].Interaction != nil {
			question = &approvals[i]
		}
	}
	if question == nil {
		t.Fatal("scheduled question missing")
	}
	f.evict()
	answer := "Read the same exact bytes again."
	if denial != "" {
		answer = continuationDockerAnswer
	}
	f.post(fmt.Sprintf("%s/approvals/%s/approve-and-continue%s", base, question.ID, suffix), map[string]any{"action_digest": question.ActionDigest, "response": store.BotInteractionResponse{QuestionID: question.Interaction.QuestionID, Answer: answer}}, 202)
	if denial != "" {
		runAllModeDockerDenial(f, mdl, scheduled.ID, question.ID, question.Interaction.QuestionID, answer, selectedTemplate, denial)
		return
	}
	if err = f.worker.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	saved, err = f.repo.GetRun(ctx, f.bot.ID, scheduled.ID)
	if err != nil || saved.Status != store.RunWaitingApproval || mdl.calls != 4 || mdl.exactReads != 2 {
		t.Fatalf("same-run continuation failed: run=%+v err=%v calls=%d exact=%d", saved, err, mdl.calls, mdl.exactReads)
	}
	f.assertPhysical("continuation-recovered", selectedTemplate)
	runAllModeDelegate(f, mdl, selectedTemplate, 3)
	t.Logf("ALL_MODE_SUCCESS scope=%s home_session=%s scheduled=%s continuation_same_run=true child=%s exact_bytes=%d", f.scope, f.bot.HomeSessionID, scheduled.ID, mdl.childID, len(betweenCallFixtureText))
}

func runAllModeDockerDenial(f *allModeDockerFixture, mdl *allModeDockerModel, runID store.RunID, approvalID uuid.UUID, questionID, answer, template, denial string) {
	t, ctx := f.t, f.ctx
	queued, err := f.repo.GetRun(ctx, f.bot.ID, runID)
	if err != nil || queued.Status != store.RunQueued || queued.Lease.Fence == 0 || len(queued.Checkpoint) == 0 {
		t.Fatalf("denial requires actual queued checkpoint and prior worker fence: %+v %v", queued, err)
	}
	assertAnswer := func() {
		t.Helper()
		approvals, e := f.repo.ListApprovals(ctx, f.bot.ID, store.BotPage{})
		if e != nil {
			t.Fatal(e)
		}
		for _, a := range approvals {
			if a.ID == approvalID && a.RunID == runID && a.Response != nil && a.Response.QuestionID == questionID && a.Response.Answer == answer {
				return
			}
		}
		t.Fatal("denial lost or changed exact accepted owner answer")
	}
	assertAnswer()
	current, err := f.repo.Get(ctx, f.bot.ID)
	if err != nil || current.Status != store.BotActive {
		t.Fatalf("denial requires active scheduled Bot: %+v %v", current, err)
	}
	var revokedOrgID, revokedRole string
	switch denial {
	case "paused", "stopped":
		operation := "pause"
		want := store.BotPaused
		if denial == "stopped" {
			operation, want = "stop", store.BotStopped
		}
		var raw []byte
		if denial == "stopped" {
			// Stop is a repository lifecycle operation; the fixture router does
			// not expose a stop URL. Do not invent one.
			current, err = f.repo.Stop(ctx, store.BotLifecycleRequest{BotID: f.bot.ID, ActorID: current.OwnerID, ExpectedRevision: current.Revision, Reason: "Owner denies the pending scheduled continuation"})
			if err != nil || current.Status != want {
				t.Fatalf("owner stop denial failed: %+v %v", current, err)
			}
			break
		}
		raw = f.post(fmt.Sprintf("%s/bots/%s/%s?scope=%s", f.server.URL, f.bot.ID, operation, f.scope), map[string]any{"expected_revision": current.Revision, "reason": "Owner denies the pending scheduled continuation"}, http.StatusOK)
		if err = json.Unmarshal(raw, &current); err != nil || current.Status != want {
			t.Fatalf("owner lifecycle denial failed: %+v %v", current, err)
		}
	case "revoked":
		// This scheduled fixture uses owner/directory authority, not a private
		// mandate pin. Revoke the actual current membership it authenticates.
		p, ok := execution.PrincipalFromContext(ctx)
		if !ok || p.Subject != f.owner {
			t.Fatal("denial requires the real authenticated owner")
		}
		org, e := f.db.Organizations().GetBySlug(ctx, p.OrgSlug)
		if e != nil || org == nil {
			t.Fatalf("owner organization: %+v %v", org, e)
		}
		revokedOrgID = org.ID
		revokedRole, e = f.db.Organizations().GetMemberRole(ctx, f.owner, org.ID)
		if e != nil || revokedRole == "" {
			t.Fatalf("actual membership before revocation: role=%q err=%v", revokedRole, e)
		}
	default:
		t.Fatalf("unknown denial %q", denial)
	}
	fence, err := f.repo.GetRun(ctx, f.bot.ID, runID)
	if err != nil {
		t.Fatal(err)
	}
	meta, err := f.sessions.GetSessionMeta(ctx, queued.SessionID.String())
	if err != nil || meta == nil {
		t.Fatalf("denial transcript meta: %+v %v", meta, err)
	}
	transcript := func() string {
		t.Helper()
		events, e := f.sessions.ReadTranscriptEvents(ctx, meta.AppName, meta.UserID, meta.ID)
		if e != nil || len(events) == 0 {
			t.Fatalf("actual saved transcript: events=%d err=%v", len(events), e)
		}
		raw, e := json.Marshal(events)
		if e != nil {
			t.Fatal(e)
		}
		return string(raw)
	}
	before := transcript()
	if denial == "revoked" {
		if err = f.db.Organizations().RemoveMember(ctx, f.owner, revokedOrgID); err != nil {
			t.Fatal(err)
		}
	}
	for attempt := 0; attempt < 2; attempt++ {
		attemptContext := ctx
		if denial == "revoked" {
			p, _ := execution.PrincipalFromContext(ctx)
			if _, _, e := f.worker.authenticateOwner(ctx, BotSchedulerScope{p.OrgSlug, p.TeamSlug, f.owner}); e == nil {
				t.Fatal("actual worker authenticated the revoked owner")
			}
			// Poll takes this same live-directory gate before dispatching; it
			// must not replay the queued saved conversation either.
			_ = f.worker.Poll(ctx)
			continue
		}
		if e := f.worker.execute(attemptContext, f.repo, f.sessions, f.bot.ID, runID); e == nil {
			t.Fatal("denied worker continuation unexpectedly succeeded")
		}
	}
	if denial == "revoked" {
		// Restore only after both worker admissions have been denied, solely
		// to inspect the protected records. No execution follows restoration.
		if err = f.db.Organizations().AddMember(ctx, f.owner, revokedOrgID, revokedRole); err != nil {
			t.Fatal(err)
		}
	}
	after, err := f.repo.GetRun(ctx, f.bot.ID, runID)
	if err != nil || after.Lease != fence.Lease || string(after.Checkpoint) != string(fence.Checkpoint) || after.SessionID != queued.SessionID || after.Status != fence.Status {
		t.Fatalf("denied worker changed lease/checkpoint/session/status: %+v %v", after, err)
	}
	mdl.mu.Lock()
	calls, reads := mdl.calls, mdl.exactReads
	mdl.mu.Unlock()
	if calls != 2 || reads != 1 || transcript() != before {
		t.Fatalf("denied continuation generated or replayed transcript: calls=%d reads=%d", calls, reads)
	}
	assertAnswer()
	if denial == "revoked" {
		physical, e := exec.CommandContext(ctx, "docker", "ps", "-a", "--filter", "label=astonish.bot-home.scope="+f.home.Scope.Key(), "--format", "{{.ID}}").CombinedOutput()
		if e != nil || strings.TrimSpace(string(physical)) != "" {
			t.Fatalf("revoked worker restarted evicted compute: %s %v", physical, e)
		}
		if e := continuationRuntimeIOError(f.worker.config.Sandbox.(*betweenCallDockerTrace).metrics); e != nil {
			t.Fatal(e)
		}
	} else {
		f.assertPhysical("denied-"+denial, template)
	}
	t.Logf("DATABASE_DOCKER_DENIAL_SUCCESS scope=%s denial=%s bot=%s run=%s exact_answer_bytes=%d worker_attempts=2 no_generation=true lease_unchanged=true checkpoint_unchanged=true reload_identical=true", f.scope, denial, f.bot.ID, runID, len(answer))
}

func runAllModeDelegate(f *allModeDockerFixture, mdl *allModeDockerModel, selectedTemplate string, wantReads int) {
	t, ctx := f.t, f.ctx
	base := fmt.Sprintf("%s/bots/%s", f.server.URL, f.bot.ID)
	suffix := "?scope=" + string(f.scope)
	current, err := f.repo.Get(ctx, f.bot.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Delegation is a separate owner-reviewed capability, not implied by the
	// read-only scheduled task or by the interactive transport. Configuration
	// requires a paused/draft/stopped Bot; use the owner's actual pause route.
	if current.Status == store.BotActive {
		raw := f.post(base+"/pause"+suffix, map[string]any{"expected_revision": current.Revision, "reason": "Owner pauses the disposable schedule before reviewing delegated reads"}, http.StatusOK)
		if err := json.Unmarshal(raw, &current); err != nil {
			t.Fatal(err)
		}
	}
	version, err := f.repo.GetVersion(ctx, f.bot.ID, current.Version)
	if err != nil {
		t.Fatal(err)
	}
	autonomy := version.Autonomy
	autonomy.AllowedTools = []string{"read_file", "ask_user", "delegate_tasks"}
	autonomy.AllowedPaths = []string{"outputs"}
	body, err := json.Marshal(store.BotConfigurationRequest{ExpectedRevision: current.Revision, ExpectedVersion: current.Version, Reviewed: true, Reason: "Owner permits a restricted retained-file reader child for this disposable acceptance", Autonomy: autonomy, Budget: version.Budget, Delivery: version.Delivery})
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, base+"/configuration"+suffix, strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := f.server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	configured, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("reviewed delegate configuration: status=%d body=%s read=%v close=%v", response.StatusCode, configured, readErr, closeErr)
	}
	if err = json.Unmarshal(configured, &current); err != nil {
		t.Fatal(err)
	}
	// Reviewed capabilities still require explicit activation. Paused messages
	// are control-only and must not acquire file or delegate authority.
	f.worker.config.ModelOptions.MaxOutputTokens = 4096
	t.Logf("DELEGATE_ACTIVATION scope=%s status=%s revision=%d version=%d", f.scope, current.Status, current.Revision, current.Version)
	raw := f.post(base+"/activate"+suffix, map[string]any{"expected_revision": current.Revision, "reason": "Owner resumes active interactive delegated reads without enabling recurrence"}, http.StatusOK)
	if err = json.Unmarshal(raw, &current); err != nil {
		t.Fatal(err)
	}
	if current.Status != store.BotActive {
		t.Fatalf("delegation requires active lifecycle: %+v", current)
	}
	jobs := f.org.ForTeam("team").ScheduledJobs()
	if f.scope == store.BotScopePersonal {
		jobs = f.org.ForUser(f.owner).ScheduledJobs()
	}
	for _, job := range jobs.List(ctx) {
		if job.Enabled || job.NextRun != nil {
			t.Fatalf("activation unexpectedly resumed recurrence: %+v", job)
		}
	}
	if f.interactiveAdmission == nil {
		t.Fatal("authenticated interactive admission unavailable")
	}
	restoreAdmission := f.interactiveAdmission()
	defer restoreAdmission()
	raw = f.post(base+"/messages"+suffix, map[string]any{"version": current.Version, "instructions": "Delegate reading the retained result to a reader child.", "idempotency_key": "all-mode-delegated-read"}, 202)
	var receipt store.BotMessageReceipt
	if err = json.Unmarshal(raw, &receipt); err != nil {
		t.Fatal(err)
	}
	admitted, admissionErr := f.repo.GetRun(ctx, f.bot.ID, receipt.Run.ID)
	if admissionErr != nil || admitted.MandateRef != nil || admitted.CallerCeiling == nil || !admitted.IsInteractiveChat() {
		t.Fatalf("delegate must use authenticated Chat-turn authority, not a routine grant: %+v %v", admitted, admissionErr)
	}
	f.ownSession(receipt.Run.SessionID.String())
	if err = f.worker.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	saved, err := f.repo.GetRun(ctx, f.bot.ID, receipt.Run.ID)
	// The post-commit notifier can claim this run before Poll's discovery
	// generation. Wait for that already-owned execution to reach its durable
	// terminal/handoff state rather than treating a still-running run as done.
	deadline := time.Now().Add(2 * time.Minute)
	for err == nil && saved != nil && (saved.Status == store.RunQueued || saved.Status == store.RunRunning) && time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
		saved, err = f.repo.GetRun(ctx, f.bot.ID, receipt.Run.ID)
	}
	if err != nil || saved.Status != store.RunWaitingApproval || mdl.calls != 8 || mdl.exactReads != wantReads || mdl.childID == "" || mdl.childID == receipt.Run.SessionID.String() {
		if saved != nil {
			rawReport, marshalErr := json.Marshal(saved.Report)
			t.Logf("DELEGATE_FAILURE_REPORT scope=%s report=%s marshal=%v", f.scope, rawReport, marshalErr)
			approvals, approvalErr := f.repo.ListApprovals(ctx, f.bot.ID, store.BotPage{})
			for _, approval := range approvals {
				if approval.RunID == saved.ID {
					t.Logf("DELEGATE_FAILURE_APPROVAL scope=%s id=%s status=%s interaction=%t", f.scope, approval.ID, approval.Status, approval.Interaction != nil)
				}
			}
			t.Logf("DELEGATE_FAILURE_APPROVAL_LOOKUP scope=%s err=%v", f.scope, approvalErr)
			meta, metaErr := f.sessions.GetSessionMeta(ctx, saved.SessionID.String())
			if metaErr != nil || meta == nil {
				t.Logf("DELEGATE_FAILURE_TRANSCRIPT_META scope=%s err=%v", f.scope, metaErr)
			} else {
				events, transcriptErr := f.sessions.ReadTranscriptEvents(ctx, meta.AppName, meta.UserID, meta.ID)
				t.Logf("DELEGATE_FAILURE_TRANSCRIPT scope=%s events=%d err=%v", f.scope, len(events), transcriptErr)
				for _, event := range events {
					if event == nil {
						continue
					}
					if event.ErrorCode != "" || event.ErrorMessage != "" {
						t.Logf("DELEGATE_FAILURE_EVENT scope=%s code=%s message=%s", f.scope, event.ErrorCode, event.ErrorMessage)
					}
					if event.Content != nil {
						for _, part := range event.Content.Parts {
							if part != nil && part.FunctionResponse != nil {
								response := part.FunctionResponse
								t.Logf("DELEGATE_FAILURE_TOOL scope=%s id=%s name=%s response=%v", f.scope, response.ID, response.Name, response.Response)
							}
						}
					}
				}
			}
		}
		t.Fatalf("delegated read failed: run=%+v err=%v calls=%d exact=%d child=%s", saved, err, mdl.calls, mdl.exactReads, mdl.childID)
	}
	// A waiting run is not sufficient: the genuine final ask_user card must
	// remain pending after the authoritative progress checkpoint, not revoked
	// by an uncertain-effect veto after the child has already joined.
	approvals, err := f.repo.ListApprovals(ctx, f.bot.ID, store.BotPage{})
	if err != nil {
		t.Fatal(err)
	}
	pendingQuestion := false
	for _, approval := range approvals {
		if approval.RunID != saved.ID || approval.Interaction == nil {
			continue
		}
		var payload struct {
			Prompt string `json:"prompt"`
		}
		if approval.Status == store.ApprovalPending && approval.Interaction.Kind == store.BotInteractionQuestion && approval.Interaction.Validate() == nil && json.Unmarshal(approval.Interaction.Payload, &payload) == nil && payload.Prompt == "Read the retained result again after step 8?" {
			pendingQuestion = true
		}
	}
	if !pendingQuestion {
		t.Fatal("genuine final ask_user card did not survive the waiting progress checkpoint")
	}
	t.Logf("DELEGATE_FINAL_QUESTION_PENDING scope=%s run=%s kind=%s", f.scope, saved.ID, store.BotInteractionQuestion)
	f.ownSession(mdl.childID)
	child, err := f.sessions.GetSessionMeta(ctx, mdl.childID)
	if err != nil || child == nil || child.ParentID != receipt.Run.SessionID.String() || child.UserID != f.owner {
		t.Fatalf("delegated child not persisted under actual parent/owner: %+v %v", child, err)
	}
	events, err := f.sessions.ReadTranscriptEvents(ctx, child.AppName, child.UserID, child.ID)
	if err != nil {
		t.Fatal(err)
	}
	childRead := false
	for _, event := range events {
		if event == nil || event.Content == nil {
			continue
		}
		for _, part := range event.Content.Parts {
			if part != nil && part.FunctionResponse != nil && part.FunctionResponse.ID == "all-mode-read-6" && betweenCallExactToolResult(part.FunctionResponse.Response) {
				childRead = true
			}
		}
	}
	if !childRead {
		t.Fatal("actual child transcript lacks exact shared read_file result")
	}
	f.assertPhysical("delegated", selectedTemplate)
	after, err := f.repo.Get(ctx, f.bot.ID)
	if err != nil || after.Status != store.BotActive {
		t.Fatalf("interactive delegate changed active lifecycle: %+v %v", after, err)
	}
	t.Logf("DELEGATE_PHYSICAL_SUCCESS scope=%s original_session=%s parent=%s child=%s transcript_exact=true exact_bytes=%d", f.scope, f.bot.HomeSessionID, receipt.Run.SessionID, mdl.childID, len(betweenCallFixtureText))
}

func (f *allModeDockerFixture) assertPhysical(stage, template string) {
	f.t.Helper()
	raw, err := exec.CommandContext(f.ctx, "docker", "ps", "-a", "--filter", "label=astonish.bot-home.scope="+f.home.Scope.Key(), "--format", "{{.Label \"astonish.session_id\"}}").CombinedOutput()
	if err != nil || strings.TrimSpace(string(raw)) != f.bot.HomeSessionID.String() {
		f.t.Fatalf("%s: expected sole original session: %s %v", stage, raw, err)
	}
	// Also count the entire unique test organization: an unbound child container
	// would lack the Home label and must not escape the one-compute assertion.
	orgRaw, orgErr := exec.CommandContext(f.ctx, "docker", "ps", "-a", "--filter", "label=astonish.org="+f.home.Scope.OrgID, "--format", "{{.Label \"astonish.session_id\"}}").CombinedOutput()
	if orgErr != nil || strings.TrimSpace(string(orgRaw)) != f.bot.HomeSessionID.String() {
		f.t.Fatalf("%s: stray org compute: %s %v", stage, orgRaw, orgErr)
	}
	b, err := f.repo.Get(f.ctx, f.bot.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	v, err := f.repo.GetVersion(f.ctx, f.bot.ID, b.Version)
	if err != nil || v.SandboxTemplate != template || b.HomeSessionID != f.bot.HomeSessionID {
		f.t.Fatalf("%s changed template/session: %+v %v", stage, v, err)
	}
	stream, err := f.worker.config.Sandbox.(*betweenCallDockerTrace).DockerBackend.OpenBotHomeFile(f.ctx, f.home, betweenCallFixturePath, store.BotFileRange{})
	if err != nil {
		f.t.Fatal(err)
	}
	data, readErr := io.ReadAll(stream)
	closeErr := stream.Close()
	if readErr != nil || closeErr != nil || string(data) != betweenCallFixtureText {
		f.t.Fatalf("%s exact bytes: %q %v %v", stage, data, readErr, closeErr)
	}
	if !botNoPythonDockerEvidence(f.t, f.ctx, f.bot.HomeSessionID.String(), f.home.Scope.Key()) {
		f.t.Fatal("default template no-Python prerequisite changed")
	}
	if err := continuationRuntimeIOError(f.worker.config.Sandbox.(*betweenCallDockerTrace).metrics); err != nil {
		f.t.Fatalf("%s database runtime IO: %v", stage, err)
	}
	f.t.Logf("ALL_MODE_DATABASE_RUNTIME stage=%s scope=%s runtime_puts=0 runtime_opens=0", stage, f.scope)
	f.t.Logf("ALL_MODE_PHYSICAL stage=%s scope=%s original_session=%s exact_bytes=%d template=%q", stage, f.scope, f.bot.HomeSessionID, len(data), template)
}
