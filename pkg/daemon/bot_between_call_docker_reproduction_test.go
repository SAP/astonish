package daemon

import (
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
	"sync"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/launcher"
	"github.com/SAP/astonish/pkg/sandbox"
	dockersandbox "github.com/SAP/astonish/pkg/sandbox/docker"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
)

// This wrapper supplies only the trusted creation configuration; all admission,
// authorisation, transaction and content operations still use the production store.
type sessionTemplateTitleStore struct {
	store.BotStore
	store.BotTitleStartStore
	template string
}

func (s *sessionTemplateTitleStore) CreateDraftAndStart(ctx context.Context, q store.BotCreateRequest) (*store.BotTitleStartReceipt, error) {
	q.Version.SandboxTemplate = s.template
	return s.BotTitleStartStore.CreateDraftAndStart(ctx, q)
}

const betweenCallStage continuationStage = "read-return-to-next-producer"
const betweenCallFixturePath = "outputs/local-search-results.txt"

// Keep live history representative of the reported ~14-event conversation.
// Large-history bounded-read stress belongs in the independent store/API tests.
const betweenCallHistoryEvents = 16
const betweenCallFixtureText = "LOCAL_SEARCH_RESULT_42: OpenShell sidecars separate temporary compute from retained Home storage. This is a deterministic local fixture, not a fetched public source."

// Observe the close of the actual confined read, not its start. Everything after
// this point is framework overhead; tool IO and provider latency are excluded.
type betweenCallDockerTrace struct {
	*continuationDockerTrace
	model *betweenCallDockerModel
}

// Ordinary Chat read_file runs through the real per-call node, not the private
// retained-file adapter. Observe that transport return without substituting it.
func (d *betweenCallDockerTrace) Exec(c context.Context, sessionID string, spec sandbox.ExecSpec) (*sandbox.ExecResult, error) {
	var request struct {
		Tool string `json:"tool"`
		Args struct {
			Path string `json:"path"`
		} `json:"args"`
	}
	if spec.Stdin != nil && len(spec.Command) == 2 && spec.Command[0] == "astonish" && spec.Command[1] == "node" {
		raw, err := io.ReadAll(spec.Stdin)
		if err != nil {
			return nil, err
		}
		spec.Stdin = bytes.NewReader(raw)
		_ = json.Unmarshal(raw, &request)
	}
	result, err := d.DockerBackend.Exec(c, sessionID, spec)
	if err == nil && request.Tool == "read_file" && request.Args.Path == betweenCallFixturePath {
		d.model.mu.Lock()
		d.model.returned = time.Now()
		d.model.auxiliary = 0
		d.model.reads++
		d.model.softNow = d.model.softNow.Add(2*time.Hour + time.Second)
		d.model.mu.Unlock()
		d.metrics.setStage(betweenCallStage)
	}
	return result, err
}

func (d *betweenCallDockerTrace) OpenBotHomeFile(c context.Context, h *sandbox.BotHomeBinding, path string, r store.BotFileRange) (*store.BotFileStream, error) {
	s, err := d.DockerBackend.OpenBotHomeFile(c, h, path, r)
	if err == nil && path == betweenCallFixturePath {
		s.ReadCloser = &betweenCallClose{ReadCloser: s.ReadCloser, done: func() {
			d.model.mu.Lock()
			defer d.model.mu.Unlock()
			d.model.returned = time.Now()
			d.model.auxiliary = 0
			d.model.reads++
			// Only the progress soft clock advances; auth, lease and budget stay real.
			d.model.softNow = d.model.softNow.Add(2*time.Hour + time.Second)
			d.metrics.setStage(betweenCallStage)
		}}
	}
	return s, err
}

type betweenCallClose struct {
	io.ReadCloser
	done func()
}

func (r *betweenCallClose) Close() error {
	err := r.ReadCloser.Close()
	if err == nil {
		r.done()
	}
	return err
}

type betweenCallDockerModel struct {
	t                                          *testing.T
	metrics                                    *continuationMetrics
	mu                                         sync.Mutex
	softNow, returned, producer, finalReturned time.Time
	auxiliary                                  time.Duration
	calls, reads, evaluations                  int
	sawResult                                  bool
}

func (*betweenCallDockerModel) Name() string { return "controlled-between-call-docker" }
func (m *betweenCallDockerModel) GenerateContent(c context.Context, r *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	entered := time.Now()
	// The production factory consumes purpose before rebinding to owner context;
	// the underlying provider sees the request protocol, not that context value.
	if betweenCallProgressRequest(r) {
		m.evaluations++
		raw, _ := json.Marshal(r.Contents)
		if !strings.Contains(string(raw), "LOCAL_SEARCH_RESULT_42") {
			m.t.Errorf("progress evaluation lost current tool result: %s", raw)
		}
		m.t.Logf("BETWEEN_CALL progress_entry_after_read=%s", entered.Sub(m.returned))
		return func(yield func(*model.LLMResponse, error) bool) {
			defer func() { m.auxiliary += time.Since(entered) }()
			yield(&model.LLMResponse{Content: genai.NewContentFromText(`{"decision":"continue","summary":"Compare the retained result.","next_steps":[],"evidence_refs":[]}`, "model")}, nil)
		}
	}
	m.calls++
	m.t.Logf("%s PRODUCER generation=%d", entered.UTC().Format(time.RFC3339Nano), m.calls)
	if m.calls == 2 {
		ctrl := bot.ProgressControllerFromContext(c)
		if ctrl == nil {
			m.t.Error("missing production progress controller")
		} else {
			m.softNow = time.Now()
			ctrl.Now = func() time.Time { m.mu.Lock(); defer m.mu.Unlock(); return m.softNow }
		}
		raw, _ := json.Marshal(r.Contents)
		if !strings.Contains(string(raw), "The deliverable should be a detailed technical design") {
			m.t.Error("new message missing from producer request")
		}
	}
	if m.calls == 3 || m.calls == 5 || m.calls == 7 {
		m.producer = entered
		for _, content := range r.Contents {
			if content == nil {
				continue
			}
			for _, p := range content.Parts {
				if p != nil && p.FunctionResponse != nil && p.FunctionResponse.ID == "between-call-read" {
					if betweenCallExactToolResult(p.FunctionResponse.Response) {
						m.sawResult = true
					} else {
						m.t.Errorf("shared read_file did not return exact fixture: %+v", p.FunctionResponse.Response)
					}
				}
			}
		}
		elapsed := entered.Sub(m.returned) - m.auxiliary
		m.t.Logf("BETWEEN_CALL tool_return_to_producer=%s auxiliary_provider=%s framework=%s evaluations=%d", entered.Sub(m.returned), m.auxiliary, elapsed, m.evaluations)
		if err := betweenCallLatencyError(m.returned, entered, m.auxiliary); err != nil {
			m.t.Error(err)
		}
		m.metrics.report(m.t, "next-producer")
		m.metrics.setStage(continuationPersistence)
	}
	return func(yield func(*model.LLMResponse, error) bool) {
		var call *genai.FunctionCall
		switch m.calls {
		case 1:
			call = &genai.FunctionCall{ID: "between-call-initial-question", Name: "ask_user", Args: map[string]any{"prompt": "What should I design?", "kind": "select", "allow_custom_answer": true, "options": []any{map[string]any{"id": "design", "label": "Design"}, map[string]any{"id": "review", "label": "Review"}, map[string]any{"id": "prototype", "label": "Prototype"}}}}
		case 2, 4, 6:
			call = &genai.FunctionCall{ID: "between-call-read", Name: "read_file", Args: map[string]any{"path": betweenCallFixturePath, "force": true}}
		case 3, 5:
			call = &genai.FunctionCall{ID: "between-call-next-question", Name: "ask_user", Args: map[string]any{"prompt": "The local result is available. Docker or Kubernetes?", "kind": "select", "options": []any{map[string]any{"id": "docker", "label": "Docker"}, map[string]any{"id": "kubernetes", "label": "Kubernetes"}}}}
		case 7:
			defer func() {
				m.finalReturned = time.Now()
				m.t.Logf("FINAL_PRODUCER_RETURN at=%s", m.finalReturned.UTC().Format(time.RFC3339Nano))
			}()
			yield(&model.LLMResponse{Content: genai.NewContentFromText("# Technical design\n\nSeparate temporary compute from retained Home storage. Keep each per-call tool process disposable, mount the existing Home by its immutable scope, and recover the original session recipe after compute eviction. Verify retained bytes before reuse; keep authorization and lease checks current. The controlled retained source supports this separation, not a claim about a production deployment.\n\nSource: outputs/local-search-results.txt (LOCAL_SEARCH_RESULT_42).", "model"), TurnComplete: true}, nil)
			return
		default:
			yield(nil, fmt.Errorf("unexpected producer call %d", m.calls))
			return
		}
		// Shared Chat exposes questions through its progressive tool bridge after
		// the title-only turn. Follow the advertised catalog instead of calling
		// an undeclared tool; preserve the exact question ID and arguments.
		if call.Name == "ask_user" && r.Tools[call.Name] == nil {
			call = &genai.FunctionCall{ID: call.ID, Name: "execute_tool", Args: map[string]any{"name": call.Name, "arguments": call.Args}}
		}
		yield(&model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: call}}}}, nil)
	}
}
func betweenCallExactToolResult(response map[string]any) bool {
	return response["content"] == "1: "+betweenCallFixtureText && response["truncated"] != true && response["unchanged"] != true
}

func TestBetweenCallExactToolResult(t *testing.T) {
	for _, tc := range []struct {
		name, content string
		want          bool
	}{
		{"exact", "1: " + betweenCallFixtureText, true},
		{"marker only", "1: LOCAL_SEARCH_RESULT_42", false},
		{"extra bytes", "1: " + betweenCallFixtureText + "unexpected", false},
		{"missing bytes", "1: " + betweenCallFixtureText[:len(betweenCallFixtureText)-1], false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := betweenCallExactToolResult(map[string]any{"content": tc.content}); got != tc.want {
				t.Fatalf("exact=%t want=%t", got, tc.want)
			}
		})
	}
}

func betweenCallProgressRequest(r *model.LLMRequest) bool {
	if r == nil || r.Config == nil || r.Config.SystemInstruction == nil {
		return false
	}
	for _, p := range r.Config.SystemInstruction.Parts {
		if p != nil && strings.HasPrefix(p.Text, "BOT_EMPLOYEE_PROGRESS_V1\n") {
			return true
		}
	}
	return false
}

func TestBetweenCallProgressRequest(t *testing.T) {
	for _, tc := range []struct {
		name string
		r    *model.LLMRequest
		want bool
	}{
		{"nil", nil, false},
		{"producer", &model.LLMRequest{Contents: []*genai.Content{genai.NewContentFromText("BOT_EMPLOYEE_PROGRESS_V1\n", "user")}}, false},
		{"progress", &model.LLMRequest{Config: &genai.GenerateContentConfig{SystemInstruction: genai.NewContentFromText("BOT_EMPLOYEE_PROGRESS_V1\nEvaluate", "system")}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := betweenCallProgressRequest(tc.r); got != tc.want {
				t.Fatalf("progress=%t want=%t", got, tc.want)
			}
		})
	}
}

func betweenCallLatencyError(start, end time.Time, auxiliary time.Duration) error {
	if start.IsZero() || end.IsZero() || end.Before(start) || auxiliary < 0 || auxiliary > end.Sub(start) {
		return fmt.Errorf("missing or invalid between-call timing")
	}
	if elapsed := end.Sub(start) - auxiliary; elapsed > 15*time.Second {
		return fmt.Errorf("between-call framework regression: %s exceeds 15s (excludes tool and model latency)", elapsed)
	}
	return nil
}
func TestBotPromptDispatchDockerMaintenanceBlocked(t *testing.T) {
	runBotNewMessageDockerReproduction(t, false, store.BotScopePersonal, func(f *allModeDockerFixture) {
		directory := &promptDispatchDirectory{OrganizationStore: f.db.Organizations(), entered: make(chan struct{}), release: make(chan struct{})}
		f.worker.config.Backend = promptDispatchBackend{PlatformBackend: f.db, directory: directory}
		f.worker.Start(f.ctx)
		defer close(directory.release)
		select {
		case <-directory.entered:
		case <-time.After(5 * time.Second):
			f.t.Fatal("maintenance not blocked")
		}
		current, err := f.repo.Get(f.ctx, f.bot.ID)
		if err != nil {
			f.t.Fatal(err)
		}
		raw := f.post(fmt.Sprintf("%s/bots/%s/messages?scope=%s", f.server.URL, f.bot.ID, f.scope), map[string]any{"version": current.Version, "instructions": "Complete the technical design using the retained evidence.", "idempotency_key": "docker-maintenance-owner"}, 202)
		var receipt store.BotMessageReceipt
		if err := json.Unmarshal(raw, &receipt); err != nil {
			f.t.Fatal(err)
		}
		f.ownSession(receipt.Run.SessionID.String())
		queued, err := f.repo.GetRun(f.ctx, f.bot.ID, receipt.Run.ID)
		if err != nil || queued.StateCommittedAt.IsZero() {
			f.t.Fatalf("owner admission missing durable commit timestamp: %v", err)
		}
		committed := queued.StateCommittedAt
		f.worker.NotifyRunState(f.ctx, store.BotRunState{BotID: f.bot.ID, RunID: receipt.Run.ID, SessionID: receipt.Run.SessionID, Status: store.RunQueued, Presentation: store.BotRunPresentationHomeInteractive, StateSequence: receipt.Run.StateSequence, HomeSessionID: f.bot.HomeSessionID})
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		deadline := time.NewTimer(5 * time.Second)
		defer deadline.Stop()
		for {
			select {
			case <-deadline.C:
				f.t.Fatal("committed owner admission not claimed within 5s while maintenance blocked")
			case <-ticker.C:
				run, err := f.repo.GetRun(f.ctx, f.bot.ID, receipt.Run.ID)
				if err != nil {
					f.t.Fatal(err)
				}
				if run.StartedAt != nil && run.Lease.Fence > 0 {
					latency := run.StartedAt.Sub(committed)
					if latency < 0 || latency > 5*time.Second {
						f.t.Fatalf("durable admission-to-claim=%s exceeds unchanged 5s bound", latency)
					}
					f.t.Logf("DOCKER_COMMIT_TO_CLAIM duration=%s observed_after=%s maintenance_blocked=true fence=%d", latency, time.Since(committed), run.Lease.Fence)
					return
				}
				if run.Status != store.RunQueued {
					f.t.Fatalf("owner left queue without an actual claim: status=%s", run.Status)
				}
			}
		}
	})
}

func TestBotNewMessageDockerBetweenCallReproduction(t *testing.T) {
	runBotNewMessageDockerReproduction(t, false, store.BotScopePersonal, func(f *allModeDockerFixture) {
		current, err := f.repo.Get(f.ctx, f.bot.ID)
		if err != nil {
			f.t.Fatal(err)
		}
		started := time.Now()
		raw := f.post(fmt.Sprintf("%s/bots/%s/messages?scope=%s", f.server.URL, f.bot.ID, f.scope), map[string]any{"version": current.Version, "instructions": "Complete the technical design directly in the conversation using the retained sidecar evidence. Do not ask another question or create a file.", "idempotency_key": "between-call-employee-completion"}, 202)
		var receipt store.BotMessageReceipt
		if err := json.Unmarshal(raw, &receipt); err != nil {
			f.t.Fatal(err)
		}
		f.ownSession(receipt.Run.SessionID.String())
		admitted := time.Now()
		queued, err := f.repo.GetRun(f.ctx, f.bot.ID, receipt.Run.ID)
		if err != nil || queued.StateCommittedAt.IsZero() {
			f.t.Fatalf("employee admission missing durable commit timestamp: %v", err)
		}
		if err := f.worker.execute(f.ctx, f.repo, f.sessions, f.bot.ID, receipt.Run.ID); err != nil {
			f.t.Fatal(err)
		}
		committed := time.Now()
		run, err := f.repo.GetRun(f.ctx, f.bot.ID, receipt.Run.ID)
		if err != nil || run.Status != store.RunSucceeded || run.Usage.ToolCalls != 1 {
			f.t.Fatalf("employee completion: run=%+v err=%v", run, err)
		}
		approvals, err := f.repo.ListApprovals(f.ctx, f.bot.ID, store.BotPage{})
		if err != nil {
			f.t.Fatal(err)
		}
		for _, approval := range approvals {
			if approval.RunID == run.ID {
				f.t.Fatal("employee demanded another owner approval")
			}
		}
		events, err := f.sessions.ReadTranscriptEvents(f.ctx, "astonish", f.owner, run.SessionID.String())
		if err != nil {
			f.t.Fatal(err)
		}
		inline := false
		for _, event := range events {
			if event.Content != nil {
				for _, part := range event.Content.Parts {
					if part != nil && strings.Contains(part.Text, "# Technical design") && strings.Contains(part.Text, "LOCAL_SEARCH_RESULT_42") {
						inline = true
					}
				}
			}
		}
		if !inline {
			f.t.Fatal("completed employee answer missing from durable conversation")
		}
		if run.StartedAt == nil || run.FinishedAt == nil || run.StateCommittedAt.IsZero() {
			f.t.Fatal("employee completion missing durable startup/finish/commit timestamps")
		}
		f.t.Logf("EMPLOYEE_COMPLETED admission=%s execution_through_final_commit=%s total=%s queue_to_claim=%s claimed_through_finished=%s finished_to_state_commit=%s commit_to_execute_return=%s run=%s", admitted.Sub(started), committed.Sub(admitted), committed.Sub(started), run.StartedAt.Sub(queued.StateCommittedAt), run.FinishedAt.Sub(*run.StartedAt), run.StateCommittedAt.Sub(*run.FinishedAt), committed.Sub(run.StateCommittedAt), run.ID)
	})
}

// This is intentionally a success assertion, not an expected-error test. Run
// alone with explicit Docker opt-in; no template/layer is installed or changed.
func TestBotEnhancedChatNoPythonDocker(t *testing.T) {
	if os.Getenv("ASTONISH_TEST_DOCKER") != "1" {
		t.Skip("explicit real Docker opt-in required")
	}
	if os.Getenv("ASTONISH_BOT_HOME_TEST_TEMPLATE") != "" {
		t.Fatal("no-Python regression requires the configured default template, not a template override")
	}
	runBotNewMessageDockerReproduction(t, true, store.BotScopePersonal)
}

// Team ownership uses the same default-template no-Python path and exact retained
// bytes. This is Docker evidence only; it does not claim live Kubernetes.
func TestBotEnhancedChatTeamNoPythonDocker(t *testing.T) {
	if os.Getenv("ASTONISH_TEST_DOCKER") != "1" {
		t.Skip("explicit real Docker opt-in required")
	}
	if os.Getenv("ASTONISH_BOT_HOME_TEST_TEMPLATE") != "" {
		t.Fatal("no-Python regression requires the configured default template, not a template override")
	}
	runBotNewMessageDockerReproduction(t, true, store.BotScopeTeam)
}

type publicationReportReaderFixture struct {
	publicationReconcileStore
	reads int
}

func (s *publicationReportReaderFixture) ListActivity(context.Context, store.BotID, int64, int) ([]store.BotActivity, error) {
	return nil, fmt.Errorf("unrelated history hydrated")
}

func (s *publicationReportReaderFixture) ListBotPublicationActivity(ctx context.Context, id store.BotID, after int64, limit int) ([]store.BotActivity, error) {
	s.reads++
	return s.publicationReconcileStore.ListActivity(ctx, id, after, limit)
}

func TestPublicationReportReaderFixture(t *testing.T) {
	id, run := uuid.New(), uuid.New()
	s := &publicationReportReaderFixture{}
	for i := int64(1); i <= 201; i++ {
		payload, _ := json.Marshal(map[string]any{"intent": store.BotOutboxPublish})
		s.activities = append(s.activities, store.BotActivity{BotID: id, RunID: &run, Sequence: i * 3, Kind: store.BotActivityReport, Payload: payload})
	}
	for range 2 {
		if err := ReconcileBotPublications(t.Context(), s, id, nil); err != nil {
			t.Fatal(err)
		}
	}
	if s.reads != 4 || len(s.seen) != 402 || s.seen[200].ReportSequence != 603 {
		t.Fatalf("lost report pagination/replay: reads=%d outcomes=%d", s.reads, len(s.seen))
	}
}

func runBotNewMessageDockerReproduction(t *testing.T, noPython bool, scope store.BotScope, extensions ...func(*allModeDockerFixture)) {
	if scope != store.BotScopePersonal && scope != store.BotScopeTeam {
		t.Fatalf("unsupported bot scope %q", scope)
	}
	if os.Getenv("ASTONISH_TEST_DOCKER") != "1" {
		t.Skip("explicit real Docker opt-in required")
	}
	image, layers := os.Getenv("ASTONISH_SANDBOX_IMAGE"), os.Getenv("ASTONISH_BOT_HOME_TEST_LAYERS")
	if image == "" || !filepath.IsAbs(layers) {
		t.Fatal("explicit image and absolute existing layers required")
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", wd)
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Minute)
	t.Cleanup(cancel)
	raw, err := exec.CommandContext(ctx, "docker", "image", "inspect", "--format", "{{.Id}}", image).CombinedOutput()
	if err != nil {
		t.Fatalf("image preflight: %s %v", raw, err)
	}
	t.Logf("%s real Docker image=%s id=%s isolated_root=%s", time.Now().UTC().Format(time.RFC3339Nano), image, strings.TrimSpace(string(raw)), root)
	metrics := &continuationMetrics{stage: continuationCreate}
	defer metrics.report(t, "final")
	mdl := &betweenCallDockerModel{t: t, metrics: metrics}
	sandbox.RegisterBackendFactory(sandbox.BackendKindDocker, func(fc sandbox.BackendFactoryConfig) (sandbox.Backend, error) {
		b, e := dockersandbox.New(dockersandbox.Config{Sessions: fc.Sessions, SandboxImage: image, LayersDir: layers, UppersDir: filepath.Join(root, "uppers"), OverlayMode: dockersandbox.OverlayModeFuse})
		if e != nil {
			return nil, e
		}
		return &betweenCallDockerTrace{continuationDockerTrace: &continuationDockerTrace{DockerBackend: b, t: t, metrics: metrics}, model: mdl}, nil
	})
	t.Cleanup(func() {
		sandbox.RegisterBackendFactory(sandbox.BackendKindDocker, func(fc sandbox.BackendFactoryConfig) (sandbox.Backend, error) {
			return dockersandbox.New(dockersandbox.Config{Sessions: fc.Sessions, SandboxImage: fc.Docker.SandboxImage, OverlayMode: dockersandbox.OverlayMode(fc.Docker.OverlayMode), LayersDir: fc.Docker.LayersDir, UppersDir: fc.Docker.UppersDir, ContainerRuntimePath: fc.Docker.ContainerRuntimePath})
		})
	})
	orgSlug := "between-call-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	network := "astonish-org-" + orgSlug
	raw, err = exec.CommandContext(ctx, "docker", "network", "ls", "--filter", "name=^"+network+"$", "--format", "{{.Name}}").CombinedOutput()
	if err != nil || strings.TrimSpace(string(raw)) != "" {
		t.Fatalf("network must not preexist: %s %v", raw, err)
	}
	t.Cleanup(func() {
		// Registered before fixture setup: even failed HTTP creation can allocate
		// the org network. Later compute/Home cleanup runs first (LIFO).
		c, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		raw, e := exec.CommandContext(c, "docker", "network", "ls", "--filter", "name=^"+network+"$", "--format", "{{.ID}}").CombinedOutput()
		if e != nil {
			t.Errorf("network inventory: %s %v", raw, e)
			return
		}
		if strings.TrimSpace(string(raw)) == "" {
			return
		}
		raw, e = exec.CommandContext(c, "docker", "network", "inspect", network).CombinedOutput()
		var rows []struct {
			Id, Name   string
			Labels     map[string]string
			Containers map[string]json.RawMessage
		}
		if e != nil || json.Unmarshal(raw, &rows) != nil || len(rows) != 1 || rows[0].Name != network || len(rows[0].Labels) != 1 || rows[0].Labels["astonish.org"] != orgSlug || len(rows[0].Containers) != 0 {
			t.Errorf("refuse nonowned or attached network cleanup: %s %v", raw, e)
			return
		}
		if raw, e = exec.CommandContext(c, "docker", "network", "rm", rows[0].Id).CombinedOutput(); e != nil {
			t.Errorf("network cleanup: %s %v", raw, e)
			return
		}
		raw, e = exec.CommandContext(c, "docker", "network", "ls", "--filter", "name=^"+network+"$", "--format", "{{.ID}}").CombinedOutput()
		if e != nil || strings.TrimSpace(string(raw)) != "" {
			t.Errorf("test network remains: %s %v", raw, e)
		}
		t.Logf("removed exact unattached test network %s", network)
	})
	db, owner := setupChannelResolverFixture(t, orgSlug, "team", "telegram", "isolated-between-call")
	org, err := db.ForOrg(orgSlug)
	if err != nil {
		t.Fatal(err)
	}
	personal := org.ForUser(owner)
	teamStore := org.ForTeam("team")
	repo, ss := teamStore.Bots(), teamStore.Sessions()
	pinSession := teamStore.SetSessionPin
	if scope == store.BotScopePersonal {
		repo, ss = personal.Bots(), personal.Sessions()
		pinSession = personal.SetSessionPin
	}
	auth, err := execution.WithPrincipal(ctx, execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: owner, OrgSlug: orgSlug, TeamSlug: "team", Authenticated: true})
	if err != nil {
		t.Fatal(err)
	}
	auth, err = execution.WithAuthenticationEvidence(auth, execution.AuthenticationEvidence{Method: execution.AuthMethodPlatformJWT, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	auth = store.WithTenantContext(auth, &store.TenantContext{OrgSlug: orgSlug, TeamSlug: "team", UserID: owner})
	var stageMu sync.Mutex
	stages := make(map[entstore.BotExecutionStage]int)
	observeStage := func(v entstore.BotExecutionTiming) {
		stageMu.Lock()
		stages[v.Stage]++
		stageMu.Unlock()
		if v.Duration < 0 || v.EndedAt.Before(v.StartedAt) || v.Duration != v.EndedAt.Sub(v.StartedAt) {
			t.Errorf("invalid stage interval: %+v", v)
		}
		t.Logf("EXECUTION_STAGE stage=%d result=%d duration=%s", v.Stage, v.Result, v.Duration)
	}
	auth = entstore.WithBotExecutionObserver(auth, observeStage)
	t.Cleanup(func() {
		if noPython || t.Failed() {
			return
		}
		stageMu.Lock()
		defer stageMu.Unlock()
		for _, stage := range []entstore.BotExecutionStage{entstore.BotStageQueue, entstore.BotStageClaim, entstore.BotStageStartup, entstore.BotStageApproval, entstore.BotStagePreflight, entstore.BotStageProvider, entstore.BotStageTool, entstore.BotStageBookkeeping, entstore.BotStageCompletion, entstore.BotStageCommit, entstore.BotStageOwnerSetup} {
			if stages[stage] == 0 {
				t.Errorf("production stage %d was not observed", stage)
			}
		}
	})
	registry := execution.NewBotTurnAuthorizationRegistry(32)
	t.Cleanup(registry.Close)
	production, release, err := newProductionBotWorker(db, &config.AppConfig{}, botTestChatResources(t, &config.AppConfig{}), registry)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
	t.Cleanup(production.Stop)
	cfg := production.config
	cfg.ObserveExecution = observeStage
	cfg.ResolveModel = func(context.Context, execution.Principal, store.BotScope, string, string) (model.LLM, error) {
		return mdl, nil
	}
	prepare := cfg.Prepare
	seeded := false
	cfg.Prepare = func(c context.Context, r bot.RunRequest, s *launcher.BotChatScope) (*launcher.BotChatSafeResources, error) {
		v, e := prepare(c, r, s)
		if e != nil {
			return v, e
		}
		if !seeded {
			// Seed through the same scoped, lease-authorized factory as this run.
			// A personal file adapter must never be used for a team Bot.
			files, e := s.Services.BotFilesFactory(c, s.Services, scope)
			if e != nil {
				return v, e
			}
			_, e = files.Write(c, r.Bot.ID, store.BotFileWrite{Path: betweenCallFixturePath, SizeBytes: int64(len(betweenCallFixtureText)), Body: strings.NewReader(betweenCallFixtureText), CreateOnly: true})
			if e != nil {
				return v, e
			}
			seeded = true
		}
		return v, nil
	}
	worker, err := NewBotWorker(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(worker.Stop)
	worker.schedulerReady.Store(true)
	svc := &store.Services{Bots: repo, PersonalBots: repo, Sessions: ss, PersonalSessions: ss, BotMandatePolicy: cfg.MandatePolicy, BotTurnRegistry: registry, BotInteractiveReady: true, BotHomeSetup: worker.provisionHome, BotFilesFactory: worker.filesForRequest}
	var setupErr error
	if noPython {
		svc.BotHomeSetup = func(c context.Context, repo store.BotStore, id store.BotID, revision int64) (*store.Bot, error) {
			v, err := worker.provisionHome(c, repo, id, revision)
			setupErr = err
			return v, err
		}
	}
	if template := os.Getenv("ASTONISH_BOT_HOME_TEST_TEMPLATE"); template != "" {
		svc.PersonalBots = &sessionTemplateTitleStore{BotStore: repo, BotTitleStartStore: repo.(store.BotTitleStartStore), template: template}
	}
	router := mux.NewRouter()
	router.HandleFunc("/", api.BotCreateHandler).Methods("POST")
	router.HandleFunc("/bots/{botID}/messages", api.BotMessageHandler).Methods("POST")
	router.HandleFunc("/bots/{botID}/configuration", api.BotConfigurationHandler).Methods("PUT")
	router.HandleFunc("/bots/{botID}/activate", api.BotActivateHandler).Methods("POST")
	router.HandleFunc("/bots/{botID}/pause", api.BotPauseHandler).Methods("POST")
	router.HandleFunc("/bots/{botID}/approvals/{approvalID}/approve-and-continue", api.BotApproveContinueHandler).Methods("POST")
	router.HandleFunc("/bots/{botID}/files", api.BotFilesHandler).Methods("GET")
	router.HandleFunc("/bots/{botID}/files/download", api.BotFileDownloadHandler).Methods("GET")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		router.ServeHTTP(w, r.WithContext(store.WithServices(auth, svc)))
	}))
	t.Cleanup(server.Close)
	var diagnoseHTTPFailure func(int, []byte)
	post := func(url string, body any, status int) []byte {
		t.Helper()
		b, _ := json.Marshal(body)
		res, e := server.Client().Post(url, "application/json", strings.NewReader(string(b)))
		if e != nil {
			t.Fatal(e)
		}
		defer res.Body.Close()
		raw, e := io.ReadAll(res.Body)
		if e != nil || res.StatusCode != status {
			if diagnoseHTTPFailure != nil {
				diagnoseHTTPFailure(res.StatusCode, raw)
			}
			t.Fatalf("HTTP %d body=%s err=%v", res.StatusCode, raw, e)
		}
		return raw
	}
	title := "OpenShell sidecar design"
	if noPython {
		title = "Astonish New Proxy"
	}
	raw = post(server.URL+"?scope="+string(scope), map[string]any{"name": title, "start_from_title": true, "idempotency_key": "between-call-initial"}, 201)
	var created struct {
		store.Bot
		InitialTurn *store.BotMessageReceipt `json:"initial_turn"`
	}
	if err = json.Unmarshal(raw, &created); err != nil || created.InitialTurn == nil {
		t.Fatalf("create: %s %v", raw, err)
	}
	home := &sandbox.BotHomeBinding{Scope: sandbox.BotHomeScope{OrgID: orgSlug, TeamID: "team", UserID: owner, BotID: created.ID.String()}, Backend: sandbox.BackendKindDocker}
	home.Ref = "astonish-bot-home-" + home.Scope.Key()
	sessions := []string{created.HomeSessionID.String(), created.InitialTurn.Run.SessionID.String()}
	t.Cleanup(func() {
		c, stop := context.WithTimeout(context.Background(), 90*time.Second)
		defer stop()
		// Join dispatch and maintenance before deleting physical resources: either
		// can still resolve retained content and recreate a session until Stop joins.
		worker.Stop()
		production.Stop()
		cfg.Pool.Cleanup()
		for _, id := range sessions {
			if e := cfg.Sandbox.DestroySession(c, id); e != nil && !strings.Contains(e.Error(), "is not registered") {
				t.Errorf("cleanup %s: %v", id, e)
			}
		}
		if e := cfg.Sandbox.(sandbox.BotHomeDeleteBackend).DeleteBotHome(c, home); e != nil {
			t.Errorf("cleanup home: %v", e)
		}
	})
	if scope == store.BotScopeTeam {
		if _, personalErr := personal.Bots().Get(auth, created.ID); personalErr == nil {
			t.Fatal("team bot visible in personal repository")
		}
	}
	if err = pinSession(auth, created.HomeSessionID.String(), "controlled", "model"); err != nil {
		t.Fatal(err)
	}
	metrics.setStage(continuationInitial)
	initialStarted := time.Now()
	if err = worker.execute(auth, repo, ss, created.ID, created.InitialTurn.Run.ID); err != nil {
		t.Fatal(err)
	}
	t.Logf("INITIAL_SETUP_AND_QUESTION duration=%s", time.Since(initialStarted))
	initial, err := repo.GetRun(auth, created.ID, created.InitialTurn.Run.ID)
	if err != nil || initial.Status != store.RunWaitingApproval || mdl.calls != 1 {
		t.Fatalf("initial question: %+v %v calls=%d", initial, err, mdl.calls)
	}
	if state, e := cfg.Sandbox.SessionState(auth, created.HomeSessionID.String()); e == nil && state == sandbox.SessionStateRunning {
		t.Fatal("title-only phase started persistent compute")
	}
	questionCompute, questionErr := exec.CommandContext(ctx, "docker", "ps", "-a", "--filter", "label=astonish.org="+orgSlug, "--format", "{{.ID}}").CombinedOutput()
	if questionErr != nil || strings.TrimSpace(string(questionCompute)) != "" {
		t.Fatalf("question-only phase created physical compute: %s %v", questionCompute, questionErr)
	}
	t.Logf("QUESTION_ONLY_NO_COMPUTE scope=%s org=%s", scope, orgSlug)
	current, err := repo.Get(auth, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	svc.PersonalBots = repo // only creation customises its initial template
	if noPython {
		version, e := repo.GetVersion(auth, created.ID, current.Version)
		if e != nil || version == nil || version.SandboxTemplate != "" {
			t.Fatalf("default-template prerequisite mismatch: version=%+v err=%v", version, e)
		}
		t.Logf("NO_PYTHON_SEQUENCE title=%q initial_question=true producer_calls=%d template=default layers=%q", title, mdl.calls, layers)
		diagnoseHTTPFailure = func(status int, body []byte) {
			missing := botNoPythonDockerEvidence(t, ctx, created.HomeSessionID.String(), home.Scope.Key())
			if missing && setupErr != nil && mdl.calls == 1 && status >= 500 {
				t.Errorf("BOT_NO_PYTHON_REGRESSION: substantive-message retained Home setup failed before next model generation: HTTP=%d body=%s setup_error=%v producer_calls=%d; actual default overlay lacks executable /usr/bin/python3", status, body, setupErr, mdl.calls)
			} else {
				t.Errorf("no-Python regression prerequisite/cause mismatch: missing_overlay_python=%t setup_error=%v producer_calls=%d HTTP=%d", missing, setupErr, mdl.calls, status)
			}
		}
	}
	metrics.setStage(continuationAnswer)
	raw = post(fmt.Sprintf("%s/bots/%s/messages?scope=%s", server.URL, created.ID, scope), map[string]any{"version": current.Version, "instructions": continuationDockerAnswer, "idempotency_key": "between-call-new-message"}, 202)
	var receipt store.BotMessageReceipt
	if err = json.Unmarshal(raw, &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.Run.ID == initial.ID {
		t.Fatal("new message incorrectly resumed initial approval run")
	}
	sessions = append(sessions, receipt.Run.SessionID.String())
	t.Logf("NEW_MESSAGE initial_run=%s new_run=%s session=%s", initial.ID, receipt.Run.ID, receipt.Run.SessionID)
	// Seed the actual run transcript queried by progress, not a different Home
	// transcript that the observer would never load. Each event uses real retained IO.
	metrics.setStage("test-history-seed")
	for i := range betweenCallHistoryEvents {
		e := &session.Event{ID: fmt.Sprintf("irrelevant-history-%03d", i), InvocationID: "prior-unrelated-invocation", Author: "employee", Timestamp: time.Now(), LLMResponse: model.LLMResponse{Content: genai.NewContentFromText(fmt.Sprintf("IRRELEVANT_HISTORY_%03d", i), "model")}}
		if err = ss.AppendFleetEvent(auth, receipt.Run.SessionID.String(), e); err != nil {
			t.Fatalf("seed historical event %d: %v", i, err)
		}
	}
	t.Logf("seeded %d real retained historical events in new run transcript", betweenCallHistoryEvents)
	metrics.setStage(continuationStartup)
	if err = worker.execute(auth, repo, ss, created.ID, receipt.Run.ID); err != nil {
		t.Errorf("new message execution: %v", err)
	}
	if noPython && !botNoPythonDockerEvidence(t, ctx, created.HomeSessionID.String(), home.Scope.Key()) {
		t.Fatal("no-Python fixture mismatch: default overlay must lack Python; do not install or remove it")
	}
	metrics.setStage(continuationVerification)
	if mdl.calls != 3 || mdl.reads != 1 || !mdl.sawResult || mdl.evaluations != 1 {
		t.Errorf("sequence calls=%d reads=%d exact_result=%t evaluations=%d", mdl.calls, mdl.reads, mdl.sawResult, mdl.evaluations)
	}
	if err = betweenCallLatencyError(mdl.returned, mdl.producer, mdl.auxiliary); err != nil {
		t.Error(err)
	}
	metrics.mu.Lock()
	opens, maxPerKey := 0, 0
	for key, count := range metrics.counts {
		if key.Stage == betweenCallStage && key.Operation == continuationOpen {
			opens += count.Calls
			maxPerKey = max(maxPerKey, count.Calls)
		}
	}
	metrics.mu.Unlock()
	t.Logf("BETWEEN_CALL bounded opens=%d max_per_key=%d historical_events=%d", opens, maxPerKey, betweenCallHistoryEvents)
	if opens > 24 || maxPerKey > 8 {
		t.Errorf("between-call repeated hydration: opens=%d limit=24 max_per_key=%d limit=8", opens, maxPerKey)
	}
	final, e := repo.GetRun(auth, created.ID, receipt.Run.ID)
	if e != nil || final.Status != store.RunWaitingApproval || final.Usage.ToolCalls != 2 {
		t.Errorf("new run must settle read+question once: %+v err=%v", final, e)
	}
	approvals, e := repo.ListApprovals(auth, created.ID, store.BotPage{})
	if e != nil {
		t.Fatal(e)
	}
	questions := 0
	for _, a := range approvals {
		if a.RunID == initial.ID && a.Response != nil {
			t.Error("new message answered original question")
		}
		if a.RunID == receipt.Run.ID && a.Interaction != nil {
			questions++
		}
	}
	if questions != 1 {
		t.Errorf("new run question receipts=%d want=1", questions)
	}
	physical, e := exec.CommandContext(ctx, "docker", "ps", "-a", "--filter", "label=astonish.bot-home.scope="+home.Scope.Key(), "--format", "{{.Label \"astonish.session_id\"}}").CombinedOutput()
	if e != nil || strings.TrimSpace(string(physical)) != created.HomeSessionID.String() {
		t.Fatalf("expected exactly one Home session container, got %s: %v", physical, e)
	}
	t.Logf("SESSION_OWNED_PHYSICAL home_session=%s one_container=true final_status=%s", created.HomeSessionID, final.Status)
	// Read through the real compiled retained adapter before eviction as well as
	// after recovery. Diagnostic reads do not count as model tool dispatches.
	streamBefore, err := cfg.Sandbox.(*betweenCallDockerTrace).DockerBackend.OpenBotHomeFile(auth, home, betweenCallFixturePath, store.BotFileRange{})
	if err != nil {
		t.Fatal(err)
	}
	beforeBytes, beforeReadErr := io.ReadAll(streamBefore)
	beforeCloseErr := streamBefore.Close()
	if beforeReadErr != nil || beforeCloseErr != nil || string(beforeBytes) != betweenCallFixtureText {
		t.Fatalf("pre-eviction exact bytes mismatch: %q read=%v close=%v", beforeBytes, beforeReadErr, beforeCloseErr)
	}
	t.Logf("SESSION_OWNED_BYTES stage=before-eviction bytes=%d exact=true", len(beforeBytes))
	current, err = repo.Get(auth, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	removeOwnedCompute := func() {
		t.Helper()
		ids, err := exec.CommandContext(ctx, "docker", "ps", "-aq", "--filter", "label=astonish.session_id="+created.HomeSessionID.String(), "--filter", "label=astonish.bot-home.scope="+home.Scope.Key()).CombinedOutput()
		if err != nil || len(strings.Fields(string(ids))) != 1 {
			t.Fatalf("locate exact owned compute: %s %v", ids, err)
		}
		if out, err := exec.CommandContext(ctx, "docker", "rm", "-f", strings.TrimSpace(string(ids))).CombinedOutput(); err != nil {
			t.Fatalf("evict exact owned compute: %s %v", out, err)
		}
		t.Logf("SESSION_OWNED_EVICTED container=%s home_session=%s retained_volumes_untouched=true", strings.TrimSpace(string(ids)), created.HomeSessionID)
	}
	// Evict before HTTP admission, not merely before the worker: admission
	// itself must recover the existing session before transactional hydration.
	removeOwnedCompute()
	raw = post(fmt.Sprintf("%s/bots/%s/messages?scope=%s", server.URL, created.ID, scope), map[string]any{"version": current.Version, "instructions": "Read outputs/local-search-results.txt again and ask which platform to target.", "idempotency_key": "after-compute-eviction"}, 202)
	var resumed store.BotMessageReceipt
	if err = json.Unmarshal(raw, &resumed); err != nil {
		t.Fatal(err)
	}
	sessions = append(sessions, resumed.Run.SessionID.String())
	// Admission can read retained content without restarting compute. Explicitly
	// prepare the original recipe, then evict it again to independently exercise
	// worker-startup recovery as well as cold HTTP admission.
	physical, e = exec.CommandContext(ctx, "docker", "ps", "--filter", "label=astonish.session_id="+created.HomeSessionID.String(), "--format", "{{.ID}}").CombinedOutput()
	if e != nil || strings.TrimSpace(string(physical)) != "" {
		t.Fatalf("admission eagerly restarted compute: %s %v", physical, e)
	}
	if err = worker.prepareExistingBotHomeSession(auth, repo, created.ID); err != nil {
		t.Fatal(err)
	}
	removeOwnedCompute()
	mdl.sawResult = false
	if err = worker.execute(auth, repo, ss, created.ID, resumed.Run.ID); err != nil {
		t.Errorf("worker after eviction: %v", err)
	}
	recovered, err := repo.GetRun(auth, created.ID, resumed.Run.ID)
	if err != nil || recovered == nil {
		t.Fatalf("load recovered worker: %v", err)
	}
	if recovered.Status != store.RunWaitingApproval || mdl.calls != 5 || mdl.reads != 2 || !mdl.sawResult {
		t.Errorf("recovered worker calls=%d reads=%d run=%+v err=%v", mdl.calls, mdl.reads, recovered, err)
	}
	physical, e = exec.CommandContext(ctx, "docker", "ps", "-a", "--filter", "label=astonish.bot-home.scope="+home.Scope.Key(), "--format", "{{.Label \"astonish.session_id\"}}").CombinedOutput()
	if e != nil || strings.TrimSpace(string(physical)) != created.HomeSessionID.String() {
		t.Fatalf("recovered exactly one Home session: %s %v", physical, e)
	}
	stream, err := cfg.Sandbox.(*betweenCallDockerTrace).DockerBackend.OpenBotHomeFile(auth, home, betweenCallFixturePath, store.BotFileRange{})
	if err != nil {
		t.Fatal(err)
	}
	retained, readErr := io.ReadAll(stream)
	closeErr := stream.Close()
	if readErr != nil || closeErr != nil || string(retained) != betweenCallFixtureText {
		t.Fatalf("retained exact bytes mismatch: %q read=%v close=%v", retained, readErr, closeErr)
	}
	if noPython {
		if !botNoPythonDockerEvidence(t, ctx, created.HomeSessionID.String(), home.Scope.Key()) {
			t.Fatal("recovered default overlay no longer satisfies no-Python prerequisite")
		}
		beforeRuns, err := repo.ListRuns(auth, created.ID, store.BotPage{})
		if err != nil {
			t.Fatal(err)
		}
		beforeBot, err := repo.Get(auth, created.ID)
		if err != nil {
			t.Fatal(err)
		}
		removeOwnedCompute()
		cold, coldErr := cfg.Sandbox.(*betweenCallDockerTrace).DockerBackend.OpenBotHomeFile(auth, home, betweenCallFixturePath, store.BotFileRange{})
		if coldErr != nil {
			t.Fatal(coldErr)
		}
		coldBytes, coldReadErr := io.ReadAll(cold)
		coldCloseErr := cold.Close()
		if coldReadErr != nil || coldCloseErr != nil || string(coldBytes) != betweenCallFixtureText {
			t.Fatalf("cold retained bytes mismatch: %q read=%v close=%v", coldBytes, coldReadErr, coldCloseErr)
		}
		// Exercise the public read routes with compute absent, not just the
		// backend adapter. GETs must preserve the run inventory and Home identity.
		get := func(path string) []byte {
			t.Helper()
			res, err := server.Client().Get(server.URL + path)
			if err != nil {
				t.Fatal(err)
			}
			defer res.Body.Close()
			data, err := io.ReadAll(res.Body)
			if err != nil || res.StatusCode != http.StatusOK {
				t.Fatalf("cold GET %s: status=%d body=%s err=%v", path, res.StatusCode, data, err)
			}
			return data
		}
		base := fmt.Sprintf("/bots/%s/files", created.ID)
		listing := get(base + "?scope=" + string(scope) + "&path=outputs")
		var page store.BotFilePage
		if err := json.Unmarshal(listing, &page); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, entry := range page.Entries {
			if entry.Path == betweenCallFixturePath && entry.SizeBytes == int64(len(betweenCallFixtureText)) {
				found = true
			}
		}
		if !found {
			t.Fatalf("cold HTTP listing lost retained file: %s", listing)
		}
		download := get(base + "/download?scope=" + string(scope) + "&path=" + betweenCallFixturePath)
		if string(download) != betweenCallFixtureText {
			t.Fatalf("cold HTTP download changed bytes: %q", download)
		}
		running, runErr := exec.CommandContext(ctx, "docker", "ps", "--filter", "label=astonish.session_id="+created.HomeSessionID.String(), "--format", "{{.ID}}").CombinedOutput()
		if runErr != nil || strings.TrimSpace(string(running)) != "" {
			t.Fatalf("cold read started session compute: %s %v", running, runErr)
		}
		afterRuns, err := repo.ListRuns(auth, created.ID, store.BotPage{})
		if err != nil {
			t.Fatal(err)
		}
		afterBot, err := repo.Get(auth, created.ID)
		if err != nil {
			t.Fatal(err)
		}
		beforeJSON, _ := json.Marshal(beforeRuns)
		afterJSON, _ := json.Marshal(afterRuns)
		if !bytes.Equal(beforeJSON, afterJSON) || beforeBot.Home != afterBot.Home || beforeBot.HomeSessionID != afterBot.HomeSessionID || beforeBot.Revision != afterBot.Revision || mdl.calls != 5 {
			t.Fatal("cold HTTP reads mutated run inventory, Home or model calls")
		}
		running, runErr = exec.CommandContext(ctx, "docker", "ps", "--filter", "label=astonish.session_id="+created.HomeSessionID.String(), "--format", "{{.ID}}").CombinedOutput()
		if runErr != nil || strings.TrimSpace(string(running)) != "" {
			t.Fatalf("run inspection started session compute: %s %v", running, runErr)
		}
		t.Logf("COLD_READ_NO_RUN scope=%s exact_bytes=%d session_running=false http_list=true http_download=true run_inspection=true", scope, len(coldBytes))
		if !t.Failed() {
			t.Logf("NO_PYTHON_SUCCESS: title/question, shared read_file, two compute evictions, original-session recovery and stopped-compute read; scope=%s exact_bytes=%d no_python=true", scope, len(retained))
		}
	}
	t.Logf("SESSION_OWNED_RECOVERED home_session=%s one_container=true actual_worker_generations=%d retained_file_reads=%d exact_bytes=true final_status=%s", created.HomeSessionID, mdl.calls, mdl.reads, recovered.Status)
	for _, extend := range extensions {
		extend(&allModeDockerFixture{t: t, ctx: auth, db: db, repo: repo, sessions: ss, worker: worker, home: home, bot: created.Bot, lastRun: *recovered, scope: scope, org: org, owner: owner, server: server, post: post, evict: removeOwnedCompute, ownSession: func(id string) { sessions = append(sessions, id) }, interactiveAdmission: func() func() {
			// Select the already configured registry-backed Chat admission, not an
			// expanded routine mandate. Restore before any later scheduler work.
			previous := svc.BotMandatePolicy
			svc.BotMandatePolicy = nil
			return func() { svc.BotMandatePolicy = previous }
		}})
	}
	if len(extensions) != 0 && !mdl.finalReturned.IsZero() {
		t.Logf("FINAL_COMPLETION_TAIL producer_return_to_fixture_end=%s", time.Since(mdl.finalReturned))
	}
}

// Inspect only the UUID-and-Home-labelled disposable session made by this test.
// All probes are read-only; the template, shared layers and Python are untouched.
func botNoPythonDockerEvidence(t *testing.T, ctx context.Context, sessionID, scopeKey string) bool {
	t.Helper()
	out, err := exec.CommandContext(ctx, "docker", "ps", "--filter", "label=astonish.session_id="+sessionID, "--filter", "label=astonish.bot-home.scope="+scopeKey, "--format", "{{.ID}}").CombinedOutput()
	ids := strings.Fields(string(out))
	if err != nil || len(ids) != 1 {
		t.Errorf("no-Python physical prerequisite: expected one exact owned running session: %q %v", out, err)
		return false
	}
	probe := `test -d /sandbox/rootfs/usr && grep ' /sandbox/rootfs ' /proc/mounts && printf 'ASTONISH_LAYER_CHAIN=%s\n' "$ASTONISH_LAYER_CHAIN" && if test -x /sandbox/rootfs/usr/bin/python3; then printf 'OVERLAY_PYTHON_PRESENT\n'; else printf 'OVERLAY_PYTHON_MISSING\n'; fi`
	out, err = exec.CommandContext(ctx, "docker", "exec", ids[0], "/bin/sh", "-ec", probe).CombinedOutput()
	t.Logf("NO_PYTHON_PHYSICAL container=%s session=%s probe=%s err=%v", ids[0], sessionID, out, err)
	if err != nil || !strings.Contains(string(out), "OVERLAY_PYTHON_MISSING") {
		return false
	}
	out, err = exec.CommandContext(ctx, "docker", "exec", ids[0], "chroot", "/sandbox/rootfs", "/usr/bin/python3", "--version").CombinedOutput()
	t.Logf("NO_PYTHON_DIRECT_EXEC container=%s output=%s err=%v", ids[0], out, err)
	return err != nil && strings.Contains(string(out), "No such file or directory")
}

func TestBetweenCallCloseBoundary(t *testing.T) {
	calls := 0
	r := &betweenCallClose{ReadCloser: io.NopCloser(strings.NewReader("fixture")), done: func() { calls++ }}
	data, err := io.ReadAll(r)
	if err != nil || string(data) != "fixture" || calls != 0 {
		t.Fatalf("read boundary: %q %v calls=%d", data, err, calls)
	}
	if err = r.Close(); err != nil || calls != 1 {
		t.Fatalf("close boundary: %v calls=%d", err, calls)
	}
}

func TestBetweenCallLatencyBudget(t *testing.T) {
	start := time.Now()
	for _, tc := range []struct {
		name         string
		elapsed, aux time.Duration
		fail         bool
	}{{"at limit", 15 * time.Second, 0, false}, {"above limit", 15*time.Second + 1, 0, true}, {"provider excluded", 20 * time.Second, 5 * time.Second, false}, {"invalid exclusion", time.Second, 2 * time.Second, true}} {
		t.Run(tc.name, func(t *testing.T) {
			if err := betweenCallLatencyError(start, start.Add(tc.elapsed), tc.aux); (err != nil) != tc.fail {
				t.Fatalf("err=%v fail=%t", err, tc.fail)
			}
		})
	}
}
