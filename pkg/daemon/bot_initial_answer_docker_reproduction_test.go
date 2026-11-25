package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
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
	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

// This reproduction substitutes only the provider. Every Home/file operation
// below forwards to Docker unchanged; the wrapper records boundaries, not results.
// Run alone: the temporary backend factory registration is process-global.
type continuationDockerTrace struct {
	*dockersandbox.DockerBackend
	t             *testing.T
	metrics       *continuationMetrics
	documentOpens atomic.Int32
}

func (d *continuationDockerTrace) stage(c context.Context, name string) func(error) {
	start := time.Now()
	d.t.Logf("%s BEGIN %s deadline=%v", start.UTC().Format(time.RFC3339Nano), name, func() any {
		v, ok := c.Deadline()
		if ok {
			return v
		}
		return "none"
	}())
	return func(err error) {
		d.t.Logf("%s END %s elapsed=%s err=%v context=%v cause=%v", time.Now().UTC().Format(time.RFC3339Nano), name, time.Since(start), err, c.Err(), context.Cause(c))
	}
}
func (d *continuationDockerTrace) EnsureBotHome(c context.Context, s sandbox.BotHomeScope) (h *sandbox.BotHomeBinding, err error) {
	end := d.stage(c, "EnsureBotHome "+s.Key())
	defer func() { end(err) }()
	return d.DockerBackend.EnsureBotHome(c, s)
}
func (d *continuationDockerTrace) PutBotContent(c context.Context, h *sandbox.BotHomeBinding, k store.BotContentKind, r int64, b io.Reader, n int64, a func(func() error) error) (v *store.BotContentRef, err error) {
	end := d.metrics.begin(continuationPut, k, "")
	defer func() {
		key := ""
		if v != nil {
			key = v.Key
		}
		end(key, err)
	}()
	return d.DockerBackend.PutBotContent(c, h, k, r, b, n, a)
}
func (d *continuationDockerTrace) PutBotContentReadback(c context.Context, h *sandbox.BotHomeBinding, k store.BotContentKind, size int64, body io.Reader, revision int64, barrier func(func() error) error) (v *store.BotContentRef, raw []byte, err error) {
	end := d.metrics.begin(continuationPut, k, "")
	defer func() {
		key := ""
		if v != nil {
			key = v.Key
		}
		end(key, err)
	}()
	return d.DockerBackend.PutBotContentReadback(c, h, k, size, body, revision, barrier)
}

type continuationContentStream struct {
	io.ReadCloser
	end  func(string, error)
	once sync.Once
}

func (s *continuationContentStream) Close() error {
	err := s.ReadCloser.Close()
	s.once.Do(func() { s.end("", err) })
	return err
}

func (d *continuationDockerTrace) OpenBotContent(c context.Context, h *sandbox.BotHomeBinding, r store.BotContentRef, q store.BotFileRange) (v *store.BotFileStream, err error) {
	end := d.metrics.begin(continuationOpen, r.Kind, r.Key)
	defer func() { end("", err) }()
	v, err = d.DockerBackend.OpenBotContent(c, h, r, q)
	if err == nil && v != nil && v.ReadCloser != nil {
		v.ReadCloser = &continuationContentStream{ReadCloser: v.ReadCloser, end: d.metrics.begin(continuationConsume, r.Kind, r.Key)}
	}
	return v, err
}
func (d *continuationDockerTrace) OpenBotHomeFile(c context.Context, h *sandbox.BotHomeBinding, p string, q store.BotFileRange) (*store.BotFileStream, error) {
	d.documentOpens.Add(1)
	return d.DockerBackend.OpenBotHomeFile(c, h, p, q)
}

// Runtime envelopes use checkpoint/evidence content; ordinary documents and
// actual artifacts remain on disk. Check every stage, including creation and
// final reload, rather than only the answer-to-generation window.
func continuationRuntimeIOError(metrics *continuationMetrics) error {
	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	for key, count := range metrics.counts {
		if (key.Kind == store.BotContentCheckpoint || key.Kind == store.BotContentEvidence) && count.Calls != 0 {
			return fmt.Errorf("database runtime performed file IO: stage=%s op=%s kind=%s calls=%d", key.Stage, key.Operation, key.Kind, count.Calls)
		}
	}
	return nil
}

func (d *continuationDockerTrace) StatBotHomeFile(c context.Context, h *sandbox.BotHomeBinding, p string) (v *store.BotFileEntry, err error) {
	end := d.stage(c, "StatBotHomeFile "+p)
	defer func() { end(err) }()
	return d.DockerBackend.StatBotHomeFile(c, h, p)
}
func (d *continuationDockerTrace) WriteBotHomeFile(c context.Context, h *sandbox.BotHomeBinding, q store.BotFileWrite) (v *store.BotFileEntry, err error) {
	end := d.stage(c, "WriteBotHomeFile "+q.Path)
	defer func() { end(err) }()
	return d.DockerBackend.WriteBotHomeFile(c, h, q)
}

type continuationDockerModel struct {
	t       *testing.T
	calls   atomic.Int32
	mu      sync.Mutex
	request string
	metrics *continuationMetrics
	// Hold the actual second generation while another worker competes for
	// this Bot's durable lease. Nil channels preserve model-only tests.
	secondEntered chan struct{}
	secondRelease chan struct{}
}

func (*continuationDockerModel) Name() string { return "controlled-continuation-docker" }
func (m *continuationDockerModel) GenerateContent(c context.Context, r *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	n := m.calls.Add(1)
	if n == 2 {
		m.metrics.secondGeneration(m.t)
	}
	raw, err := json.Marshal(r)
	if err != nil {
		m.t.Errorf("marshal model request: %v", err)
	}
	if n == 2 {
		m.mu.Lock()
		m.request = string(raw)
		m.mu.Unlock()
	}
	m.t.Logf("%s MODEL generation=%d request_bytes=%d answer_present=%t cause=%v", time.Now().UTC().Format(time.RFC3339Nano), n, len(raw), strings.Contains(string(raw), "The deliverable should be a detailed technical design"), context.Cause(c))
	return func(yield func(*model.LLMResponse, error) bool) {
		if n == 2 && m.secondEntered != nil {
			close(m.secondEntered)
			select {
			case <-m.secondRelease:
			case <-c.Done():
				yield(nil, c.Err())
				return
			}
		}
		if n == 1 {
			yield(&model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: "docker-title-question", Name: "ask_user", Args: map[string]any{"prompt": "What should I design for the OpenShell sidecar?", "kind": "select", "allow_custom_answer": true, "options": []any{map[string]any{"id": "design", "label": "Design the sidecar"}, map[string]any{"id": "review", "label": "Review a design"}, map[string]any{"id": "prototype", "label": "Plan a prototype"}}}}}}}}, nil)
			return
		}
		// A second actual generation is the acceptance boundary. Ask another valid
		// question rather than fabricate successful work or invoke any paid model.
		followup := &genai.FunctionCall{ID: "docker-followup-question", Name: "ask_user", Args: map[string]any{"prompt": "Should the OpenShell sidecar design prioritize Docker or Kubernetes?", "kind": "select", "options": []any{map[string]any{"id": "docker", "label": "Docker"}, map[string]any{"id": "kubernetes", "label": "Kubernetes"}}}}
		if r.Tools[followup.Name] == nil {
			followup = &genai.FunctionCall{ID: followup.ID, Name: "execute_tool", Args: map[string]any{"name": followup.Name, "arguments": followup.Args}}
		}
		yield(&model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: followup}}}}, nil)
	}
}

const continuationDockerAnswer = `Design an OpenShell sidecar for Astonish. The sidecar should be the bridge between the agent runtime and an isolated sandbox, while preserving the existing Docker and Kubernetes execution models. Explain which component owns session creation, readiness, tool execution, retained Home storage, network access, cancellation and cleanup. Show a concrete request and response flow from Studio through the daemon and worker to the sandbox, including how an initial clarification answer resumes the same Bot run rather than creating an unrelated task.

Keep model credentials outside the sandbox. Describe the authentication boundary, short-lived scoped authorization, process supervision, health checks and failure reporting. Cover the difference between temporary compute and retained Bot files, and explain how reconnecting or replacing compute preserves documents without exposing another user's workspace. Include an architecture diagram and editable source, API examples, a rollout plan, risks and open decisions. Do not silently claim unsupported file-first capabilities for OpenShell: call out the missing capability and compare a sidecar adapter with the current Docker implementation.

The deliverable should be a detailed technical design, not merely a chat summary. Discuss timeouts during setup, how helper stderr reaches the operator, and how cancellation differs from a natural EOF. Identify the minimum prototype that proves initial question, saved answer, Home provisioning and the next model generation. Do not modify production infrastructure or deploy anything; this request is for design only.`

// Decode both JSON layers: the request contains a structured owner event as
// user text. The original ask_user FunctionResponse remains a card receipt, not
// a second tool result containing the answer.
func continuationDockerSavedAnswer(request *model.LLMRequest, approvalID uuid.UUID, questionID, answer string) error {
	answers, receipts := 0, 0
	for _, content := range request.Contents {
		if content == nil {
			continue
		}
		for _, part := range content.Parts {
			if part == nil {
				continue
			}
			if r := part.FunctionResponse; r != nil && r.Name == "ask_user" && r.ID == "docker-title-question" && r.Response["questionId"] == questionID && r.Response["status"] == "ok" {
				receipts++
			}
			if content.Role != "user" || part.Text == "" {
				continue
			}
			var event struct {
				Type       string                       `json:"type"`
				ApprovalID uuid.UUID                    `json:"approval_id"`
				ToolCallID string                       `json:"tool_call_id"`
				Tool       string                       `json:"tool"`
				Response   store.BotInteractionResponse `json:"response"`
			}
			if json.Unmarshal([]byte(part.Text), &event) != nil || event.Type != "bot_owner_answer" {
				continue
			}
			if event.ApprovalID != approvalID || event.ToolCallID != "docker-title-question" || event.Tool != "ask_user" || event.Response.QuestionID != questionID || event.Response.Answer != answer {
				return fmt.Errorf("second model request owner answer differs from exact saved response (answer bytes=%d want=%d)", len(event.Response.Answer), len(answer))
			}
			answers++
		}
	}
	if answers != 1 || receipts != 1 {
		return fmt.Errorf("second model request requires one exact saved owner answer and original question receipt: answers=%d receipts=%d", answers, receipts)
	}
	return nil
}

func TestContinuationDockerSavedAnswerDecoding(t *testing.T) {
	approvalID := uuid.New()
	for _, tc := range []struct {
		name, answer string
		wantErr      bool
	}{
		{"exact multiline", continuationDockerAnswer, false},
		{"truncated", continuationDockerAnswer[:100], true},
		{"extra byte", continuationDockerAnswer + " ", true},
		{"escaped rather than newline", strings.ReplaceAll(continuationDockerAnswer, "\n", `\n`), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			event, err := json.Marshal(map[string]any{"type": "bot_owner_answer", "approval_id": approvalID, "tool_call_id": "docker-title-question", "tool": "ask_user", "response": store.BotInteractionResponse{QuestionID: "question", Answer: tc.answer}})
			if err != nil {
				t.Fatal(err)
			}
			request := model.LLMRequest{Contents: []*genai.Content{
				{Role: "user", Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{ID: "docker-title-question", Name: "ask_user", Response: map[string]any{"questionId": "question", "status": "ok"}}}}},
				genai.NewContentFromText(string(event), "user"),
			}}
			raw, err := json.Marshal(request)
			if err != nil {
				t.Fatal(err)
			}
			var decoded model.LLMRequest
			if err = json.Unmarshal(raw, &decoded); err != nil {
				t.Fatal(err)
			}
			if err = continuationDockerSavedAnswer(&decoded, approvalID, "question", continuationDockerAnswer); (err != nil) != tc.wantErr {
				t.Fatalf("err=%v wantErr=%t", err, tc.wantErr)
			}
		})
	}
}

func TestContinuationDockerModelSharedDiscovery(t *testing.T) {
	m := &continuationDockerModel{t: t, metrics: &continuationMetrics{}}
	m.calls.Store(1)
	m.metrics.acceptAnswer()
	for response, err := range m.GenerateContent(t.Context(), &model.LLMRequest{}, false) {
		if err != nil || response == nil || response.Content == nil {
			t.Fatalf("response=%+v err=%v", response, err)
		}
		call := response.Content.Parts[0].FunctionCall
		if call == nil || call.ID != "docker-followup-question" || call.Name != "execute_tool" || call.Args["name"] != "ask_user" {
			t.Fatalf("shared discovery lost question identity or dispatch: %+v", call)
		}
	}
}

func TestContinuationDatabaseRuntimeIOGuard(t *testing.T) {
	for _, kind := range []store.BotContentKind{store.BotContentCheckpoint, store.BotContentEvidence, store.BotContentDocument, store.BotContentMemory, store.BotContentArtifact} {
		for _, op := range []continuationOperation{continuationPut, continuationOpen, continuationConsume} {
			t.Run(string(kind)+"/"+string(op), func(t *testing.T) {
				metrics := &continuationMetrics{counts: map[continuationCountKey]continuationCount{
					{continuationVerification, op, kind, "guard-test"}: {Calls: 1},
				}}
				wantErr := kind == store.BotContentCheckpoint || kind == store.BotContentEvidence
				if err := continuationRuntimeIOError(metrics); (err != nil) != wantErr {
					t.Fatalf("runtime IO guard: err=%v wantErr=%t", err, wantErr)
				}
			})
		}
	}
}

func TestBotDatabaseContinuationDocker(t *testing.T) {
	if os.Getenv("ASTONISH_TEST_DOCKER") != "1" {
		t.Fatal("database continuation acceptance requires real Docker; skips are not evidence")
	}
	runBotInitialAnswerDockerContinuation(t, true)
}

func TestBotInitialAnswerDockerContinuationReproduction(t *testing.T) {
	if os.Getenv("ASTONISH_TEST_DOCKER") != "1" {
		t.Skip("explicit real Docker opt-in required")
	}
	runBotInitialAnswerDockerContinuation(t, false)
}

func runBotInitialAnswerDockerContinuation(t *testing.T, databaseAcceptance bool) {
	t.Helper()
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
	c, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	t.Cleanup(cancel)
	raw, err := exec.CommandContext(c, "docker", "image", "inspect", "--format", "{{.Id}}", image).CombinedOutput()
	if err != nil {
		t.Fatalf("image preflight: %s %v", raw, err)
	}
	t.Logf("%s Docker image=%s id=%s layers=%s isolated_root=%s", time.Now().UTC().Format(time.RFC3339Nano), image, strings.TrimSpace(string(raw)), layers, root)
	// Preserve production composition and Prepare closure; configure only the
	// Docker factory's concrete paths so no existing registry/uppers are touched.
	realFactory := func(fc sandbox.BackendFactoryConfig) (sandbox.Backend, error) {
		return dockersandbox.New(dockersandbox.Config{Sessions: fc.Sessions, SandboxImage: fc.Docker.SandboxImage, OverlayMode: dockersandbox.OverlayMode(fc.Docker.OverlayMode), LayersDir: fc.Docker.LayersDir, UppersDir: fc.Docker.UppersDir, ContainerRuntimePath: fc.Docker.ContainerRuntimePath})
	}
	metrics := &continuationMetrics{stage: continuationCreate}
	defer metrics.report(t, "final")
	defer metrics.assertStartup(t)
	sandbox.RegisterBackendFactory(sandbox.BackendKindDocker, func(fc sandbox.BackendFactoryConfig) (sandbox.Backend, error) {
		b, e := dockersandbox.New(dockersandbox.Config{Sessions: fc.Sessions, SandboxImage: image, LayersDir: layers, UppersDir: filepath.Join(root, "uppers"), OverlayMode: dockersandbox.OverlayModeFuse})
		if e != nil {
			return nil, e
		}
		return &continuationDockerTrace{DockerBackend: b, t: t, metrics: metrics}, nil
	})
	t.Cleanup(func() { sandbox.RegisterBackendFactory(sandbox.BackendKindDocker, realFactory) })
	orgSlug := "continuation-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	networkName := "astonish-org-" + orgSlug
	raw, err = exec.CommandContext(c, "docker", "network", "ls", "--filter", "name=^"+networkName+"$", "--format", "{{.Name}}").CombinedOutput()
	if err != nil || strings.TrimSpace(string(raw)) != "" {
		t.Fatalf("test network must not pre-exist: %s %v", raw, err)
	}
	db, owner := setupChannelResolverFixture(t, orgSlug, "team", "telegram", "isolated-continuation")
	org, err := db.ForOrg(orgSlug)
	if err != nil {
		t.Fatal(err)
	}
	repo, ss := org.ForUser(owner).Bots(), org.ForUser(owner).Sessions()
	auth, err := execution.WithPrincipal(c, execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: owner, OrgSlug: orgSlug, TeamSlug: "team", Authenticated: true})
	if err != nil {
		t.Fatal(err)
	}
	auth, err = execution.WithAuthenticationEvidence(auth, execution.AuthenticationEvidence{Method: execution.AuthMethodPlatformJWT, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	auth = store.WithTenantContext(auth, &store.TenantContext{OrgSlug: orgSlug, TeamSlug: "team", UserID: owner})
	registry := execution.NewBotTurnAuthorizationRegistry(32)
	t.Cleanup(registry.Close)
	production, release, err := newProductionBotWorker(db, &config.AppConfig{}, botTestChatResources(t, &config.AppConfig{}), registry)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
	t.Cleanup(production.Stop)
	mdl := &continuationDockerModel{t: t, metrics: metrics}
	cfg := production.config
	cfg.ResolveModel = func(context.Context, execution.Principal, store.BotScope, string, string) (model.LLM, error) {
		return mdl, nil
	}
	prepare := cfg.Prepare
	var worker *BotWorker
	var bootstrapDocuments *bot.DocumentProjection
	cfg.Prepare = func(c context.Context, r bot.RunRequest, s *launcher.BotChatScope) (v *launcher.BotChatSafeResources, err error) {
		previous := metrics.setStage(continuationPrepare)
		defer metrics.setStage(previous)
		end := cfg.Sandbox.(*continuationDockerTrace).stage(c, "production Prepare")
		v, err = prepare(c, r, s)
		end(err)
		if err != nil {
			return
		}
		metrics.setStage(continuationDocuments)
		files, readErr := worker.filesForRequest(c, &store.Services{Bots: repo, PersonalBots: repo, Sessions: ss, PersonalSessions: ss}, store.BotScopePersonal)
		if readErr != nil {
			return v, readErr
		}
		documents, readErr := bot.NewDocuments(repo, files)
		if readErr != nil {
			return v, readErr
		}
		bootstrapDocuments, err = documents.Load(c, r.Bot.ID, 0)
		return
	}
	worker, err = NewBotWorker(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(worker.Stop)
	worker.schedulerReady.Store(true)
	svc := &store.Services{Bots: repo, PersonalBots: repo, Sessions: ss, PersonalSessions: ss, BotMandatePolicy: cfg.MandatePolicy, BotTurnRegistry: registry, BotInteractiveReady: true, BotHomeSetup: worker.provisionHome, BotFilesFactory: worker.filesForRequest}
	router := mux.NewRouter()
	router.HandleFunc("/", api.BotCreateHandler).Methods("POST")
	router.HandleFunc("/bots/{botID}/approvals/{approvalID}/approve-and-continue", api.BotApproveContinueHandler).Methods("POST")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		router.ServeHTTP(w, r.WithContext(store.WithServices(auth, svc)))
	}))
	t.Cleanup(server.Close)
	post := func(url string, body any, status int) []byte {
		t.Helper()
		b, _ := json.Marshal(body)
		res, e := server.Client().Post(url, "application/json", strings.NewReader(string(b)))
		if e != nil {
			t.Fatal(e)
		}
		raw, e := io.ReadAll(res.Body)
		res.Body.Close()
		if e != nil || res.StatusCode != status {
			t.Fatalf("HTTP status=%d body=%s err=%v", res.StatusCode, raw, e)
		}
		return raw
	}
	raw = post(server.URL+"?scope=personal", map[string]any{"name": "OpenShell sidecar design", "start_from_title": true, "idempotency_key": "docker-initial"}, 201)
	var created struct {
		store.Bot
		InitialTurn *store.BotMessageReceipt `json:"initial_turn"`
	}
	if err = json.Unmarshal(raw, &created); err != nil || created.InitialTurn == nil {
		t.Fatalf("create receipt %s %v", raw, err)
	}
	home := &sandbox.BotHomeBinding{Scope: sandbox.BotHomeScope{OrgID: orgSlug, TeamID: "team", UserID: owner, BotID: created.ID.String()}, Backend: sandbox.BackendKindDocker}
	home.Ref = "astonish-bot-home-" + home.Scope.Key()
	// Installed immediately after identity is known, before any Home provisioning.
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 60*time.Second)
		defer stop()
		cfg.Pool.Cleanup()
		for _, id := range []string{created.HomeSessionID.String(), created.InitialTurn.Run.SessionID.String()} {
			if id != "" {
				if e := cfg.Sandbox.DestroySession(cleanup, id); e != nil && !strings.Contains(e.Error(), "is not registered") {
					t.Errorf("cleanup session %s: %v", id, e)
				}
			}
		}
		if e := cfg.Sandbox.(sandbox.BotHomeDeleteBackend).DeleteBotHome(cleanup, home); e != nil {
			t.Errorf("cleanup owned home: %v", e)
		}
		// Remove only this run's previously absent org network, after proving
		// ownership and zero attachments. Never disconnect or prune anything.
		networkJSON, e := exec.CommandContext(cleanup, "docker", "network", "inspect", networkName).CombinedOutput()
		if e != nil {
			if !strings.Contains(string(networkJSON), "not found") && !strings.Contains(string(networkJSON), "No such network") {
				t.Errorf("inspect owned network: %s %v", networkJSON, e)
			}
			return
		}
		var networks []struct {
			Name       string
			Labels     map[string]string
			Containers map[string]json.RawMessage
		}
		if e = json.Unmarshal(networkJSON, &networks); e != nil || len(networks) != 1 || networks[0].Name != networkName || networks[0].Labels["astonish.org"] != orgSlug || len(networks[0].Containers) != 0 {
			t.Errorf("refuse network cleanup without exact unattached ownership: %s %v", networkJSON, e)
			return
		}
		if e = cfg.Sandbox.DeleteOrgNetwork(cleanup, orgSlug); e != nil {
			t.Errorf("cleanup owned network: %v", e)
		} else {
			t.Logf("removed exact unattached test network %s", networkName)
		}
	})
	// Attaching a second Docker CLI to every short-lived helper is intrusive
	// diagnostic overhead. In-memory content counters above are always enabled.
	if os.Getenv("ASTONISH_CONTINUATION_DOCKER_ATTACH_DEBUG") == "1" {
		watchContinuationDockerHelpers(t, home.Scope.Key())
	}
	if err = org.ForUser(owner).SetSessionPin(auth, created.HomeSessionID.String(), "controlled", "model"); err != nil {
		t.Fatal(err)
	}
	execute := func(stage string) error {
		t.Logf("%s BEGIN %s bot=%s run=%s", time.Now().UTC().Format(time.RFC3339Nano), stage, created.ID, created.InitialTurn.Run.ID)
		// A diagnostic watchdog does not cancel or manufacture EOF. The overall
		// context bounds a genuine stall and leaves time for t.Cleanup to run.
		done := make(chan struct{})
		joined := make(chan struct{})
		go func() {
			defer close(joined)
			timer := time.NewTimer(45 * time.Second)
			defer timer.Stop()
			select {
			case <-done:
				return
			case <-timer.C:
				metrics.report(t, "watchdog")
				if os.Getenv("ASTONISH_CONTINUATION_DOCKER_ATTACH_DEBUG") == "1" {
					buf := make([]byte, 4<<20)
					n := runtime.Stack(buf, true)
					t.Logf("%s WATCHDOG stage=%s goroutines:\n%s", time.Now().UTC().Format(time.RFC3339Nano), stage, buf[:n])
				}
			}
		}()
		e := worker.execute(auth, repo, ss, created.ID, created.InitialTurn.Run.ID)
		close(done)
		<-joined
		t.Logf("%s END %s err=%v context=%v cause=%v generations=%d", time.Now().UTC().Format(time.RFC3339Nano), stage, e, c.Err(), context.Cause(c), mdl.calls.Load())
		return e
	}
	metrics.setStage(continuationInitial)
	if err = execute("initial title question"); err != nil {
		t.Fatal(err)
	}
	waiting, err := repo.GetRun(auth, created.ID, created.InitialTurn.Run.ID)
	if err != nil || waiting.Status != store.RunWaitingApproval || mdl.calls.Load() != 1 {
		t.Fatalf("initial question not reached: %+v %v calls=%d", waiting, err, mdl.calls.Load())
	}
	approvals, err := repo.ListApprovals(auth, created.ID, store.BotPage{})
	if err != nil || len(approvals) != 1 || approvals[0].Interaction == nil {
		t.Fatalf("question: %+v %v", approvals, err)
	}
	a := approvals[0]
	metrics.setStage(continuationAnswer)
	raw = post(fmt.Sprintf("%s/bots/%s/approvals/%s/approve-and-continue?scope=personal", server.URL, created.ID, a.ID), map[string]any{"action_digest": a.ActionDigest, "response": store.BotInteractionResponse{QuestionID: a.Interaction.QuestionID, Answer: continuationDockerAnswer}}, 202)
	metrics.acceptAnswer()
	t.Logf("%s answer accepted bytes=%d receipt_bytes=%d", time.Now().UTC().Format(time.RFC3339Nano), len(continuationDockerAnswer), len(raw))
	if databaseAcceptance {
		mdl.secondEntered, mdl.secondRelease = make(chan struct{}), make(chan struct{})
		var unblock sync.Once
		releaseGeneration := func() { unblock.Do(func() { close(mdl.secondRelease) }) }
		defer releaseGeneration()
		result := make(chan error, 1)
		go func() { result <- execute("answer continuation") }()
		select {
		case <-mdl.secondEntered:
		case err := <-result:
			t.Fatalf("continuation ended before overlap barrier: %v", err)
		case <-auth.Done():
			t.Fatal(auth.Err())
		}
		// Use another actual worker identity, not just this worker's local
		// scheduling map. The SQL lease must fence both admissions.
		competitorConfig := cfg
		competitorConfig.WorkerID = cfg.WorkerID + "-overlap"
		competitor, e := NewBotWorker(competitorConfig)
		if e != nil {
			t.Fatal(e)
		}
		defer competitor.Stop()
		leased, e := repo.GetRun(auth, created.ID, waiting.ID)
		if e != nil || leased.Status != store.RunRunning || leased.Lease.Fence < 1 || leased.Lease.WorkerID != cfg.WorkerID || !leased.Lease.ExpiresAt.After(time.Now()) {
			t.Fatalf("overlap requires actual current live lease: %+v %v", leased, e)
		}
		if e = competitor.execute(auth, repo, ss, created.ID, waiting.ID); !errors.Is(e, store.ErrBotConflict) {
			t.Fatalf("concurrent same-Bot worker admission was not fenced: %v", e)
		}
		now := time.Now().UTC()
		claimContext := execution.WithBotRunIdentity(auth, execution.BotRunIdentity{BotID: created.ID, RunID: waiting.ID, Version: waiting.Version, ActorID: owner, OrgSlug: orgSlug, TeamSlug: "team"})
		if _, e = repo.ClaimRun(claimContext, store.BotClaimRequest{BotID: created.ID, RunID: waiting.ID, WorkerID: competitorConfig.WorkerID, Now: now, ExpiresAt: now.Add(time.Minute)}); !errors.Is(e, store.ErrBotConflict) {
			t.Fatalf("concurrent same-Bot SQL lease claim was not fenced: %v", e)
		}
		after, e := repo.GetRun(auth, created.ID, waiting.ID)
		if e != nil || after.Lease != leased.Lease || after.Status != store.RunRunning || mdl.calls.Load() != 2 {
			t.Fatalf("competing worker changed active lease or generated: %+v %v calls=%d", after, e, mdl.calls.Load())
		}
		releaseGeneration()
		if e = <-result; e != nil {
			t.Fatalf("natural continuation failure after overlap: %v", e)
		}
		t.Logf("DATABASE_DOCKER_NO_OVERLAP_SUCCESS bot=%s run=%s worker_conflict=true sql_claim_conflict=true fence=%d generations=2", created.ID, waiting.ID, leased.Lease.Fence)
	} else if err = execute("answer continuation"); err != nil {
		t.Fatalf("natural continuation failure: %v; generations=%d", err, mdl.calls.Load())
	}
	metrics.setStage(continuationVerification)
	if mdl.calls.Load() != 2 {
		t.Fatalf("expected exactly two controlled generations, got %d", mdl.calls.Load())
	}
	mdl.mu.Lock()
	secondRequest := mdl.request
	mdl.mu.Unlock()
	var second model.LLMRequest
	if err := json.Unmarshal([]byte(secondRequest), &second); err != nil {
		t.Fatalf("decode second model request: %v", err)
	}
	if err := continuationDockerSavedAnswer(&second, a.ID, a.Interaction.QuestionID, continuationDockerAnswer); err != nil {
		t.Fatal(err)
	}
	containsText := func(text string) bool {
		contents := append([]*genai.Content(nil), second.Contents...)
		if second.Config != nil && second.Config.SystemInstruction != nil {
			contents = append(contents, second.Config.SystemInstruction)
		}
		for _, content := range contents {
			if content == nil {
				continue
			}
			for _, part := range content.Parts {
				if part != nil && strings.Contains(part.Text, text) {
					return true
				}
			}
		}
		return false
	}
	// Prepare already read these five physical documents before generation.
	// Do not repeat expensive Docker reads after the run just to re-prove that
	// snapshot: MEMORY may legitimately change while persisting the follow-up.
	if bootstrapDocuments == nil || len(bootstrapDocuments.Documents) != 5 {
		t.Fatalf("expected five retained bootstrap documents captured before generation, got %+v", bootstrapDocuments)
	}
	for _, doc := range bootstrapDocuments.Documents {
		// Title-only creation deliberately leaves persona prose empty. Assert
		// every actual file's named prompt section, including those empty files.
		if !containsText("### " + string(doc.Name) + "\n" + doc.Content) {
			t.Errorf("second model request lacks actual %s section (bytes=%d)", doc.Name, len(doc.Content))
		}
		if (doc.Name == bot.DocumentMemory || doc.Name == bot.DocumentCharter) && strings.TrimSpace(doc.Content) == "" {
			t.Errorf("required %s content is empty", doc.Name)
		}
		t.Logf("verified physical document %s bytes=%d sha256=%s and second request content", doc.Name, len(doc.Content), doc.SourceVersion)
	}
	continued, err := repo.GetRun(auth, created.ID, created.InitialTurn.Run.ID)
	if err != nil || continued == nil || continued.Status != store.RunWaitingApproval {
		t.Fatalf("same run did not reach follow-up question: %+v %v", continued, err)
	}
	followups, err := repo.ListApprovals(auth, created.ID, store.BotPage{})
	if err != nil {
		t.Fatalf("read persisted follow-up question: %v", err)
	}
	foundFollowup, foundSaved := false, false
	for _, approval := range followups {
		if approval.ID == a.ID {
			foundSaved = approval.Response != nil && approval.Response.QuestionID == a.Interaction.QuestionID && approval.Response.Answer == continuationDockerAnswer
		} else if approval.RunID == created.InitialTurn.Run.ID && approval.Interaction != nil {
			foundFollowup = strings.Contains(string(approval.Interaction.Payload), "Should the OpenShell sidecar design prioritize Docker or Kubernetes?")
		}
	}
	if !foundSaved || !foundFollowup {
		t.Fatalf("persisted approvals lack exact saved answer or follow-up question: saved=%t followup=%t", foundSaved, foundFollowup)
	}
	if databaseAcceptance {
		trace := cfg.Sandbox.(*continuationDockerTrace)
		if trace.documentOpens.Load() == 0 {
			t.Fatal("standing documents were not read through real Docker")
		}
		if continued.SessionID != waiting.SessionID || continued.SessionID != created.InitialTurn.Run.SessionID {
			t.Fatal("continuation replaced the durable conversation session")
		}
		current, err := repo.Get(auth, created.ID)
		if err != nil || current.HomeSessionID != created.HomeSessionID {
			t.Fatalf("retained Home session changed: %+v %v", current, err)
		}
		physical, err := exec.CommandContext(auth, "docker", "ps", "--filter", "label=astonish.org="+orgSlug, "--format", "{{.Label \"astonish.session_id\"}}").CombinedOutput()
		if err != nil || strings.TrimSpace(string(physical)) != created.HomeSessionID.String() {
			t.Fatalf("expected sole original running compute session: %s %v", physical, err)
		}
		meta, err := ss.GetSessionMeta(auth, continued.SessionID.String())
		if err != nil || meta == nil {
			t.Fatalf("database conversation metadata: %+v %v", meta, err)
		}
		events, err := ss.ReadTranscriptEvents(auth, meta.AppName, meta.UserID, meta.ID)
		if err != nil || len(events) == 0 {
			t.Fatalf("database transcript reload: events=%d err=%v", len(events), err)
		}
		before, err := json.Marshal(events)
		if err != nil {
			t.Fatal(err)
		}
		// Reading and trying to rediscover the already-waiting run cannot
		// generate again or acquire a second lease for this Bot.
		if err := execute("waiting run rediscovery"); !errors.Is(err, store.ErrBotConflict) {
			t.Fatalf("waiting run must reject a new lease: %v", err)
		}
		reloaded, err := ss.ReadTranscriptEvents(auth, meta.AppName, meta.UserID, meta.ID)
		if err != nil {
			t.Fatal(err)
		}
		after, err := json.Marshal(reloaded)
		if err != nil || string(before) != string(after) || mdl.calls.Load() != 2 {
			t.Fatalf("reload/rediscovery changed conversation identity/order or replayed execution: err=%v generations=%d", err, mdl.calls.Load())
		}
		if err := continuationRuntimeIOError(metrics); err != nil {
			t.Fatal(err)
		}
		t.Logf("DATABASE_CONTINUATION_SUCCESS runtime_puts=0 runtime_opens=0 standing_document_opens=%d retained_home_session=%s same_run=%s transcript_events=%d exact_answer_bytes=%d reload_identical=true no_replay=true", trace.documentOpens.Load(), created.HomeSessionID, continued.ID, len(events), len(continuationDockerAnswer))
	}
	t.Log("Continuation reached exactly the second controlled model generation with saved answer and all five retained documents through real production Home setup and Prepare; exact saved answer and follow-up question persisted on the same run.")
}

// Capture each test-owned helper's Docker stderr before production cleanup
// removes it. Docker events and a read-only, non-signal-forwarding attach are
// observers only, never command shims. Helpers use --log-driver=none.
func watchContinuationDockerHelpers(t *testing.T, key string) {
	c, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(c, "docker", "events", "--filter", "type=container", "--filter", "label=astonish.bot-home.scope="+key, "--filter", "event=start", "--format", "{{json .}}")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		dec := json.NewDecoder(stdout)
		for {
			var event struct {
				ID    string `json:"id"`
				Actor struct {
					ID string `json:"ID"`
				} `json:"Actor"`
			}
			if err := dec.Decode(&event); err != nil {
				return
			}
			id := event.ID
			if id == "" {
				id = event.Actor.ID
			}
			if id == "" {
				continue
			}
			wg.Add(1)
			go func(id string) {
				defer wg.Done()
				raw, e := exec.CommandContext(c, "docker", "attach", "--no-stdin", "--sig-proxy=false", id).CombinedOutput()
				t.Logf("%s owned helper=%s logs_err=%v stderr/stdout=%s", time.Now().UTC().Format(time.RFC3339Nano), id, e, raw)
			}(id)
		}
	}()
	t.Cleanup(func() { cancel(); _ = cmd.Wait(); wg.Wait() })
}
