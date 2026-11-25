package api_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/daemon"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/launcher"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

type progressReceiptModel struct {
	model.LLM
	ctx context.Context
}

func (m *progressReceiptModel) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	m.ctx = ctx
	return m.LLM.GenerateContent(ctx, req, stream)
}

type progressProcessEvidence struct {
	Dir   string
	Bot   store.Bot
	Run   store.BotRun
	PID   int
	Scope store.BotScope
}

// The parent owns every child and listener. SIGKILL, not injected crash errors,
// destroys the worker and its registry. SQLite and its committed ledger survive.
func TestBotProgressProcessRestartAcceptance(t *testing.T) {
	if stage := os.Getenv("ASTONISH_PROGRESS_PROCESS_STAGE"); stage != "" {
		progressProcessChild(t, stage)
		return
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"settled", "settled-write", "in-flight"} {
		t.Run(mode, func(t *testing.T) {
			var effects atomic.Int64
			sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" {
					http.Error(w, "POST required", 405)
					return
				}
				effects.Add(1)
				fmt.Fprint(w, "owned external receipt")
			}))
			defer sink.Close()
			evidenceFile := filepath.Join(t.TempDir(), "evidence.json")
			// The production worker lease is five minutes. In-flight recovery must
			// observe real expiry rather than shorten it or edit persisted timestamps.
			ctx, cancel := context.WithTimeout(context.Background(), 7*time.Minute)
			defer cancel()
			cmd := exec.CommandContext(ctx, exe, "-test.run=^TestBotProgressProcessRestartAcceptance$", "-test.count=1")
			cmd.Env = append(os.Environ(), "ASTONISH_PROGRESS_PROCESS_STAGE="+mode, "ASTONISH_PROGRESS_PROCESS_EVIDENCE="+evidenceFile, "ASTONISH_PROGRESS_PROCESS_SINK="+sink.URL)
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
			var childOutput strings.Builder
			go func() {
				scanner := bufio.NewScanner(pipe)
				for scanner.Scan() {
					if scanner.Text() == "PROGRESS_PROCESS_READY" {
						ready <- true
						return
					}
					childOutput.WriteString(scanner.Text())
					childOutput.WriteByte('\n')
				}
				ready <- false
			}()
			select {
			case ok := <-ready:
				if !ok {
					_ = cmd.Wait()
					t.Fatalf("child failed before barrier: %s\n%s", stderr.String(), childOutput.String())
				}
			case <-ctx.Done():
				t.Fatal("child barrier timeout")
			}
			raw, err := os.ReadFile(evidenceFile)
			if err != nil {
				t.Fatal(err)
			}
			var evidence progressProcessEvidence
			if err = json.Unmarshal(raw, &evidence); err != nil {
				t.Fatal(err)
			}
			if evidence.PID != cmd.Process.Pid || !strings.Contains(filepath.Base(filepath.Dir(evidence.Dir)), "TestBotProgressProcessRestartAcceptance") {
				t.Fatalf("unowned process evidence: %+v", evidence)
			}
			t.Cleanup(func() { _ = os.RemoveAll(filepath.Dir(evidence.Dir)) })
			if err = cmd.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			if err = cmd.Wait(); err == nil {
				t.Fatal("child exited normally instead of killed")
			}
			if mode != "settled" && effects.Load() != 1 {
				t.Fatal("no real external effect before kill")
			}
			restart := exec.CommandContext(ctx, exe, "-test.run=^TestBotProgressProcessRestartAcceptance$", "-test.count=1", "-test.v")
			restart.Env = append(os.Environ(), "ASTONISH_PROGRESS_PROCESS_STAGE=restart-"+mode, "ASTONISH_PROGRESS_PROCESS_EVIDENCE="+evidenceFile)
			output, err := restart.CombinedOutput()
			if err != nil {
				t.Fatalf("restart failed: %v\n%s", err, output)
			}
			if mode != "settled" && effects.Load() != 1 {
				t.Fatal("uncertain external effect replayed")
			}
			t.Logf("killed_pid=%d restart_pid=%d mode=%s external_effects=%d\n%s", evidence.PID, restart.ProcessState.Pid(), mode, effects.Load(), output)
		})
	}
}

func progressProcessChild(t *testing.T, stage string) {
	evidenceFile := os.Getenv("ASTONISH_PROGRESS_PROCESS_EVIDENCE")
	if !strings.HasPrefix(stage, "restart-") {
		f, run := progressControlFixture(t, store.BotScopePersonal)
		evidence := progressProcessEvidence{Dir: f.pool.dir, Bot: f.b, Run: run, PID: os.Getpid(), Scope: store.BotScopePersonal}
		ready := func() {
			raw, _ := json.Marshal(evidence)
			if err := os.WriteFile(evidenceFile, raw, 0600); err != nil {
				t.Fatal(err)
			}
			fmt.Println("PROGRESS_PROCESS_READY")
			select {}
		}
		if stage == "settled" {
			ready()
			return
		}
		if stage != "in-flight" && stage != "settled-write" {
			t.Fatal("unknown process mode")
		}
		// Exercise an actual external write through the node transport. write_file
		// now denotes a confined local Home edit and must not stand in for HTTP.
		receipts := &progressReceiptModel{LLM: f.model}
		f.workerConfig.ResolveModel = func(context.Context, execution.Principal, store.BotScope, string, string) (model.LLM, error) {
			return receipts, nil
		}
		writer := progressFreshWorker(t, f)
		drained := make(chan error, 1)
		f.model.assessment = func(req *model.LLMRequest) (*model.LLMResponse, bool) {
			if stage == "settled-write" {
				go func() {
					ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					defer cancel()
					drained <- writer.Drain(ctx)
				}()
				time.Sleep(50 * time.Millisecond)
			}
			return &model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: "owned-write", Name: "http_request", Args: map[string]any{"url": os.Getenv("ASTONISH_PROGRESS_PROCESS_SINK"), "method": "POST", "body": "owned effect"}}}}}}, true
		}
		f.pool.callOverride = func(name string, args map[string]interface{}) (json.RawMessage, error) {
			if name != "http_request" || args["method"] != "POST" || args["url"] != os.Getenv("ASTONISH_PROGRESS_PROCESS_SINK") {
				return nil, fmt.Errorf("unexpected replay %s", name)
			}
			resp, err := http.Post(os.Getenv("ASTONISH_PROGRESS_PROCESS_SINK"), "text/plain", strings.NewReader("owned effect"))
			if err != nil {
				return nil, err
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				return nil, fmt.Errorf("external write failed: %d", resp.StatusCode)
			}
			if stage == "settled-write" {
				// This owned sink has acknowledged the exact call. Record its
				// trusted out-of-band receipt, never infer safety from JSON success.
				bot.RecordToolOutcome(receipts.ctx, "owned-write", bot.ToolOutcome{Verified: true, Changed: []store.BotEvidence{{Kind: store.BotEvidenceObservation, Summary: "Owned sink acknowledged owned-write", Source: os.Getenv("ASTONISH_PROGRESS_PROCESS_SINK"), ObservedAt: time.Now(), ActorID: f.b.OwnerID}}})
				return json.RawMessage(`{"success":true,"receipt":"owned external receipt"}`), nil
			}
			private, err := f.repo.GetRun(f.ctx, f.b.ID, run.ID)
			if err != nil {
				return nil, err
			}
			evidence.Run = *private
			ready()
			return nil, nil
		}
		if err := writer.Poll(f.ctx); err != nil {
			t.Fatal(err)
		}
		if stage == "settled-write" {
			if err := <-drained; err != nil {
				t.Fatal(err)
			}
			private, err := f.repo.GetRun(f.ctx, f.b.ID, run.ID)
			if err != nil || private.Status != store.RunQueued || private.Progress == nil || private.Usage.ToolCalls != 2 {
				t.Fatalf("write receipt not durably settled: %+v %v", private, err)
			}
			evidence.Run = *private
			ready()
		}
		t.Fatal("worker returned without reaching external effect")
	}
	raw, err := os.ReadFile(evidenceFile)
	if err != nil {
		t.Fatal(err)
	}
	var evidence progressProcessEvidence
	if err = json.Unmarshal(raw, &evidence); err != nil {
		t.Fatal(err)
	}
	if evidence.PID == os.Getpid() {
		t.Fatal("same process")
	}
	_, db, err := entstore.NewPlatformServices(context.Background(), entstore.Config{DSN: filepath.Join(evidence.Dir, "platform.db"), DataDir: evidence.Dir})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	org, err := db.ForOrg("interactive")
	if err != nil {
		t.Fatal(err)
	}
	repo := org.ForUser(evidence.Bot.OwnerID.String()).Bots()
	principal := execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodOAuth, Surface: execution.SurfaceStudio, Subject: evidence.Bot.OwnerID.String(), OrgSlug: "interactive", TeamSlug: "team", Authenticated: true, Scopes: []string{"chat", "tool:execute"}}
	ctx, err := execution.WithPrincipal(context.Background(), principal)
	if err != nil {
		t.Fatal(err)
	}
	ctx = store.WithTenantContext(ctx, &store.TenantContext{UserID: principal.Subject, OrgSlug: principal.OrgSlug, TeamSlug: principal.TeamSlug})
	registry := execution.NewBotTurnAuthorizationRegistry(4)
	defer registry.Close()
	pool := &interactiveHTTPPool{dir: evidence.Dir, callOverride: func(name string, _ map[string]interface{}) (json.RawMessage, error) {
		t.Errorf("restarted process repeated tool %s", name)
		return nil, fmt.Errorf("replay forbidden")
	}}
	mdl := &interactiveHTTPModel{principal: principal, t: t}
	mdl.assessment = func(req *model.LLMRequest) (*model.LLMResponse, bool) {
		if seq, ok := workerWorkAssessment(req, func(w *store.BotWorkState) bool {
			return w.Candidate != nil && w.Candidate.Text == "The source says isolated receipt."
		}); ok {
			for response, err := range seq {
				if err != nil {
					t.Fatal(err)
				}
				return response, true
			}
		}
		raw, _ := json.Marshal(req.Contents)
		if !strings.Contains(string(raw), "isolated receipt") {
			t.Error("restored context lost settled read")
		}
		if stage == "restart-settled-write" && !strings.Contains(string(raw), "owned external receipt") {
			t.Error("restored context lost the settled write receipt")
		}
		return &model.LLMResponse{Content: genai.NewContentFromText("The source says isolated receipt.", "model")}, true
	}
	files := &composedHTTPFiles{disk: &synthesisHTTPFiles{root: evidence.Dir}, repo: repo, id: evidence.Bot.ID, scope: evidence.Scope, fetch: func(name string, args map[string]interface{}) (json.RawMessage, error) {
		return pool.Call("", name, args)
	}}
	worker, err := daemon.NewBotWorker(daemon.BotWorkerConfig{Backend: db, Sandbox: synthesisHTTPBackend{files: files.disk}, Pool: pool, TurnRegistry: registry, WorkerID: uuid.NewString(), ContextWindow: 8192, ModelOptions: provider.BudgetLLMOptions{MaxOutputTokens: 100, SupportsOutputLimit: true}, ResolveModel: func(context.Context, execution.Principal, store.BotScope, string, string) (model.LLM, error) {
		return mdl, nil
	}, Prepare: func(c context.Context, q bot.RunRequest, s *launcher.BotChatScope) (*launcher.BotChatSafeResources, error) {
		files.install(c, q, s)
		return &launcher.BotChatSafeResources{WorkspaceDir: "/isolated", ChatConfig: &config.AppConfig{}, Catalog: &launcher.ChatCatalogResources{Pool: pool}, VerifyIsolation: func(c context.Context) error { return c.Err() }, Cleanup: func() {}}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Stop()
	// Honor real lease expiry. Do not edit database timestamps to simulate a kill.
	if stage == "restart-in-flight" {
		if wait := time.Until(evidence.Run.Lease.ExpiresAt) + 100*time.Millisecond; wait > 0 {
			time.Sleep(wait)
		}
	}
	if err = worker.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	held, err := repo.GetRun(ctx, evidence.Bot.ID, evidence.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if held.Status == store.RunSucceeded || pool.calls != 0 {
		t.Fatalf("restart falsely completed/replayed: %+v", held)
	}
	if stage == "restart-in-flight" {
		if held.Status != store.RunInterrupted || held.ContinuationReason != "" {
			t.Fatalf("uncertain write not safely held: %+v", held)
		}
		t.Log("in-flight effect held after real lease expiry; no automatic replay or final success")
		return
	}
	if held.Progress == nil || held.ContinuationReason != store.BotContinuationRequired {
		t.Fatalf("lost authority did not offer checkpoint continuation: %+v", held)
	}
	// Explicit fresh authentication, not restoration of the persisted ceiling.
	ctx, err = execution.WithAuthenticationEvidence(ctx, execution.AuthenticationEvidence{Method: principal.Authentication, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	ctx = store.WithBotMessageAdmission(ctx, registry)
	current, err := repo.Get(ctx, evidence.Bot.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = repo.(store.BotInteractiveContinuationStore).ContinueInteractive(ctx, store.BotContinueInteractiveRequest{BotID: evidence.Bot.ID, RunID: held.ID, Kind: store.BotContinuationProgress, CheckpointRevision: held.Progress.Revision, ExpectedRevision: current.Revision, IdempotencyKey: "fresh-process-auth"})
	if err != nil {
		t.Fatal(err)
	}
	if err = worker.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	after, err := repo.GetRun(ctx, evidence.Bot.ID, held.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != store.RunSucceeded || after.SessionID != evidence.Run.SessionID || after.Usage.ToolCalls != evidence.Run.Usage.ToolCalls || after.Usage.InputTokens < evidence.Run.Usage.InputTokens || after.Usage.RuntimeNanos < evidence.Run.Usage.RuntimeNanos || pool.calls != 0 {
		t.Fatalf("unsafe restart result: %+v failure=%+v", after, after.Report.Failure)
	}
	if stage == "restart-settled-write" && (mdl.reviews == 0 || after.Report.EffectState != store.BotReportEffectsConfirmed) {
		t.Fatalf("settled write lost effect evidence or bypassed employee assessment: reviews=%d effects=%s", mdl.reviews, after.Report.EffectState)
	}
	// Repeated recovery polling cannot republish the completed logical run.
	if err = worker.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	ss := org.ForUser(evidence.Bot.OwnerID.String()).Sessions()
	events, err := ss.ReadTranscriptEvents(ctx, "astonish", evidence.Bot.OwnerID.String(), evidence.Bot.HomeSessionID.String())
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
		t.Fatalf("final publications=%d want exactly one", publications)
	}
	t.Logf("same_run=%s cumulative_tool_calls=%d settled_read_replayed=false final_publications=%d", after.ID, after.Usage.ToolCalls, publications)
}
