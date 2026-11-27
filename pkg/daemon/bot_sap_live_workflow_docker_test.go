package daemon

import (
	"bufio"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/launcher"
	"github.com/SAP/astonish/pkg/sandbox"
	dockersandbox "github.com/SAP/astonish/pkg/sandbox/docker"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"github.com/robfig/cron/v3"
	"google.golang.org/adk/model"
)

const sapWorkflowModel = "gpt-6.1-sol"
const sapWorkflowTitle = "Stock Market News"
const sapWorkflowResearch = `Yes, give me a substantive update here on current US stock market news, including the S&P 500, Nasdaq and Dow, key drivers, dated findings, implications, uncertainty and links to fresh actual sources. Start with configured Perplexity search, then read relevant sources with web_fetch. Try Reuters coverage as one source; if access is blocked or any source fails, use accessible alternatives and disclose the limitation rather than repeatedly retrying or stopping. I want the report in this chat, not an output file. Do not schedule yet.`
const sapWorkflowSchedule = `Please keep checking stock market news every five minutes and report meaningful changes here. Enable that schedule now using the current research objective. Preserve the completed research update in this chat.`

// A transparent observer: every response and tool remains the production object.
// Evidence stays in memory; neither provider configuration nor tool payloads are logged.
type sapWorkflowModelObserver struct {
	model.LLM
	evidence *sapWorkflowResearchEvidence
}
type sapWorkflowResearchEvidence struct {
	mu                          sync.Mutex
	calls                       map[string]sapWorkflowCall
	seen                        map[string]bool
	searches, failed, recovered int
	failedSeen                  bool
	sources                     map[string]bool
}
type sapWorkflowCall struct{ name, url string }

func (m *sapWorkflowModelObserver) GenerateContent(ctx context.Context, req *model.LLMRequest, streaming bool) iter.Seq2[*model.LLMResponse, error] {
	m.evidence.observe(req)
	return m.LLM.GenerateContent(ctx, req, streaming)
}
func (e *sapWorkflowResearchEvidence) observe(req *model.LLMRequest) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, content := range req.Contents {
		if content == nil {
			continue
		}
		for _, p := range content.Parts {
			if p.FunctionCall != nil {
				c := p.FunctionCall
				name, args := c.Name, c.Args
				if name == "execute_tool" {
					name, _ = args["name"].(string)
					args, _ = args["arguments"].(map[string]any)
				}
				u, _ := args["url"].(string)
				e.calls[c.ID] = sapWorkflowCall{name, u}
			}
			if p.FunctionResponse == nil {
				continue
			}
			r := p.FunctionResponse
			call := e.calls[r.ID]
			name := call.name
			if name == "" {
				name = r.Name
			}
			raw, _ := json.Marshal(r.Response)
			key := r.ID + string(raw)
			if e.seen[key] {
				continue
			}
			e.seen[key] = true
			if name == "perplexity_web_search" || name == launcher.BotConfiguredSearchToolName {
				e.searches++
			}
			if name != "web_fetch" && name != "http_request" {
				continue
			}
			failed := sapWorkflowSourceFailed(r.Response)
			if failed {
				e.failed++
				e.failedSeen = true
				continue
			}
			if call.url != "" && sapWorkflowSourceSucceeded(r.Response) {
				e.sources[call.url] = true
				if e.failedSeen {
					e.recovered++
				}
			}
		}
	}
}
func sapWorkflowSourceFailed(value any) bool {
	switch v := value.(type) {
	case map[string]any:
		for k, x := range v {
			if k == "error" && x != nil && fmt.Sprint(x) != "" {
				message := strings.ToLower(fmt.Sprint(x))
				// Authorization/budget/schema failures are not external-source failures.
				for _, internal := range []string{"forbidden", "unauthorized", "budget", "policy", "approval", "invalid argument", "not registered"} {
					if strings.Contains(message, internal) {
						return false
					}
				}
				return strings.Contains(message, "fetch") || strings.Contains(message, "http") || strings.Contains(message, "connection") || strings.Contains(message, "timeout") || strings.Contains(message, "tls") || strings.Contains(message, "eof")
			}
			if k == "status" || k == "status_code" {
				if n, ok := x.(float64); ok && n >= 400 {
					return true
				}
				if s, ok := x.(string); ok && (s == "error" || s == "failed") {
					return true
				}
			}
			if sapWorkflowSourceFailed(x) {
				return true
			}
		}
	case string:
		var nested any
		if json.Unmarshal([]byte(v), &nested) == nil {
			return sapWorkflowSourceFailed(nested)
		}
	case []any:
		for _, x := range v {
			if sapWorkflowSourceFailed(x) {
				return true
			}
		}
	}
	return false
}

func sapWorkflowSourceSucceeded(value any) bool {
	switch v := value.(type) {
	case string:
		var nested any
		if json.Unmarshal([]byte(v), &nested) == nil {
			return sapWorkflowSourceSucceeded(nested)
		}
	case map[string]any:
		status, ok := v["status"].(float64)
		if !ok {
			status, ok = v["status_code"].(float64)
		}
		content, _ := v["content"].(string)
		if content == "" {
			content, _ = v["body"].(string)
		}
		if ok && status >= 200 && status < 300 && len(content) > 300 {
			return true
		}
		for _, x := range v {
			if sapWorkflowSourceSucceeded(x) {
				return true
			}
		}
	case []any:
		for _, x := range v {
			if sapWorkflowSourceSucceeded(x) {
				return true
			}
		}
	}
	return false
}

// Never use the Ent tenant router on a live source: opening a child SQLite store
// performs migrations and WAL pragmas. Raw mode=ro SELECTs copy only settings.
func sapWorkflowReadSettings(t *testing.T, ctx context.Context, app *config.AppConfig) (*store.PlatformSettings, *store.OrgSettings, *store.TeamSettings, string) {
	t.Helper()
	dsn := os.Getenv("ASTONISH_LIVE_READONLY_DSN")
	u, err := url.Parse(dsn)
	if err != nil || u.Scheme != "file" || u.Query().Get("mode") != "ro" {
		t.Fatal("explicit file: SQLite mode=ro source required")
	}
	sourceDir := os.Getenv("ASTONISH_LIVE_DATA_DIR")
	if !filepath.IsAbs(sourceDir) {
		t.Fatal("absolute live data directory required")
	}
	read := func(dsn, query, key string, dst any) {
		db, err := sql.Open("sqlite", dsn)
		if err != nil {
			t.Fatal("open read-only settings")
		}
		defer db.Close()
		var raw []byte
		if err = db.QueryRowContext(ctx, query, key).Scan(&raw); err != nil {
			t.Fatal("read selected settings failed (payload withheld)")
		}
		if json.Unmarshal(raw, dst) != nil {
			t.Fatal("invalid selected settings")
		}
	}
	ps, org, team := &store.PlatformSettings{}, &store.OrgSettings{}, &store.TeamSettings{}
	read(dsn, "SELECT value FROM platform_settings WHERE key = ?", "providers", ps)
	sourceOrg := os.Getenv("ASTONISH_LIVE_SOURCE_ORG")
	if sourceOrg == "" {
		sourceOrg = "my-org"
	}
	sourceTeam := os.Getenv("ASTONISH_LIVE_SOURCE_TEAM")
	if sourceTeam == "" {
		sourceTeam = "general"
	}
	for _, s := range []string{sourceOrg, sourceTeam} {
		if !regexp.MustCompile(`^[a-zA-Z0-9_-]+$`).MatchString(s) {
			t.Fatal("invalid source scope")
		}
	}
	read(dsn, "SELECT value FROM platform_settings WHERE key = ?", "org_settings:"+sourceOrg, org)
	teamURL := url.URL{Scheme: "file", Path: filepath.Join(sourceDir, "orgs", sourceOrg, "teams", sourceTeam+".db"), RawQuery: "mode=ro"}
	read(teamURL.String(), "SELECT value FROM settings WHERE key = ?", "team_settings", team)
	providers := map[string]store.ProviderConfig{}
	for _, layer := range []map[string]store.ProviderConfig{ps.Providers, org.Providers, team.Providers} {
		for n, c := range layer {
			cp := map[string]string{}
			for k, v := range c {
				cp[k] = v
			}
			if local, ok := app.Providers[n]; ok {
				for k, v := range local {
					if cp[k] == "" {
						cp[k] = v
					}
				}
			}
			providers[n] = cp
		}
	}
	selected := os.Getenv("ASTONISH_LIVE_SAP_PROVIDER")
	if selected == "" {
		selected = "New AI Core Provider"
	}
	if config.GetProviderType(selected, config.ProviderConfig(providers[selected])) != "sap_ai_core" {
		t.Fatal("selected provider must be actual SAP")
	}
	search := ps.PerplexityWebSearch
	if search == nil || search.Provider == "" || search.Model == "" {
		t.Fatal("configured Perplexity route required")
	}
	if _, ok := providers[search.Provider]; !ok {
		t.Fatal("configured search provider missing")
	}
	filtered := map[string]store.ProviderConfig{selected: providers[selected], search.Provider: providers[search.Provider]}
	copySearch := *search
	return &store.PlatformSettings{Providers: filtered, DefaultProvider: selected, DefaultModel: sapWorkflowModel, WebSearchTool: ps.WebSearchTool, WebExtractTool: ps.WebExtractTool, PerplexityWebSearch: &copySearch}, &store.OrgSettings{}, &store.TeamSettings{WebSearchTool: team.WebSearchTool, WebExtractTool: team.WebExtractTool}, selected
}

type sapWorkflowFrame struct{ ID, Event, Data string }
type sapWorkflowStream struct {
	mu     sync.Mutex
	frames []sapWorkflowFrame
	done   chan struct{}
	err    error
	cancel context.CancelFunc
}

func sapWorkflowStartStream(ctx context.Context, client *http.Client, endpoint string) (*sapWorkflowStream, error) {
	ctx, cancel := context.WithCancel(ctx)
	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		cancel()
		return nil, err
	}
	res, err := client.Do(req)
	if err != nil {
		cancel()
		return nil, err
	}
	if res.StatusCode != 200 {
		res.Body.Close()
		cancel()
		return nil, fmt.Errorf("stream HTTP %d", res.StatusCode)
	}
	s := &sapWorkflowStream{done: make(chan struct{}), cancel: cancel}
	go func() {
		defer close(s.done)
		defer res.Body.Close()
		scanner := bufio.NewScanner(res.Body)
		scanner.Buffer(make([]byte, 4096), 8<<20)
		var f sapWorkflowFrame
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				if f.Event != "" && f.Event != "replay_complete" && f.Event != "done" {
					s.mu.Lock()
					s.frames = append(s.frames, f)
					s.mu.Unlock()
				}
				f = sapWorkflowFrame{}
				continue
			}
			if strings.HasPrefix(line, "id: ") {
				f.ID = strings.TrimPrefix(line, "id: ")
			}
			if strings.HasPrefix(line, "event: ") {
				f.Event = strings.TrimPrefix(line, "event: ")
			}
			if strings.HasPrefix(line, "data: ") {
				f.Data = strings.TrimPrefix(line, "data: ")
			}
		}
		s.mu.Lock()
		s.err = scanner.Err()
		s.mu.Unlock()
	}()
	return s, nil
}
func (s *sapWorkflowStream) snapshot() []sapWorkflowFrame {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]sapWorkflowFrame(nil), s.frames...)
}

// Opt-in acceptance, not a captured-response branch. Missing opt-in is a skip;
// missing credentials/prerequisites after opt-in is a failure, never a false pass.
func TestBotSAPLiveScheduleOnlyDocker(t *testing.T) {
	if os.Getenv("ASTONISH_SAP_SCHEDULE_ONLY") != "1" {
		t.Skip("explicit schedule-only opt-in required")
	}
	testBotSAPLiveWorkflowDocker(t, true)
}

func TestBotSAPLiveResearchReportScheduleDocker(t *testing.T) {
	testBotSAPLiveWorkflowDocker(t, false)
}

func testBotSAPLiveWorkflowDocker(t *testing.T, scheduleOnly bool) {
	if os.Getenv("ASTONISH_BOT_SAP_WORKFLOW_LIVE") != "1" || os.Getenv("ASTONISH_TEST_DOCKER") != "1" {
		t.Skip("explicit paid SAP and Docker workflow opt-in required")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Minute)
	defer cancel()
	app, err := config.LoadAppConfig()
	if err != nil {
		t.Fatal("configuration load failed (details withheld)")
	}
	ps, osx, ts, selected := sapWorkflowReadSettings(t, ctx, app)
	image, layers := os.Getenv("ASTONISH_SANDBOX_IMAGE"), os.Getenv("ASTONISH_BOT_HOME_TEST_LAYERS")
	if image == "" || !filepath.IsAbs(layers) {
		t.Fatal("explicit candidate image and absolute existing layers required")
	}
	inspect, err := exec.CommandContext(ctx, "docker", "image", "inspect", "--format", "{{.Id}}", image).Output()
	if err != nil {
		t.Fatal("candidate image unavailable")
	}
	image = strings.TrimSpace(string(inspect))
	t.Logf("SAP_WORKFLOW image=%s model=%s", image, sapWorkflowModel)
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", wd)
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	// Infrastructure is real but isolated; no global factory override is necessary.
	sessionsRegistry, err := sandbox.NewSessionRegistry()
	if err != nil {
		t.Fatal(err)
	}
	backend, err := dockersandbox.New(dockersandbox.Config{Sessions: sessionsRegistry, SandboxImage: image, LayersDir: layers, UppersDir: filepath.Join(root, "uppers"), OverlayMode: dockersandbox.OverlayModeFuse})
	if err != nil {
		t.Fatal(err)
	}
	pool := sandbox.NewBackendPool(backend, sandbox.ResourceLimits{})
	defer pool.Cleanup()
	orgSlug := "sap-workflow-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	db, owner := setupChannelResolverFixture(t, orgSlug, "team", "telegram", "isolated-sap-workflow")
	org, err := db.ForOrg(orgSlug)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.PlatformSettings().Save(ctx, ps); err != nil {
		t.Fatal("copy selected platform settings")
	}
	if err = db.OrgSettings(orgSlug).Save(ctx, osx); err != nil {
		t.Fatal(err)
	}
	if err = org.ForTeam("team").Settings().Save(ctx, ts); err != nil {
		t.Fatal(err)
	}
	personal := org.ForUser(owner)
	repo, ss := personal.Bots(), personal.Sessions()
	// Match the authenticated Studio caller's membership role before capturing its
	// ceiling. The isolated directory fixture grants member at both tenant tiers.
	auth, err := execution.WithPrincipal(ctx, execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: owner, OrgSlug: orgSlug, TeamSlug: "team", Roles: []string{"member"}, Scopes: []string{string(execution.CapabilityChat), string(execution.CapabilityMemoryRead), string(execution.CapabilityMemoryWrite), string(execution.CapabilityIntegration), string(execution.CapabilityToolExecute)}, Authenticated: true})
	if err != nil {
		t.Fatal(err)
	}
	auth, err = execution.WithAuthenticationEvidence(auth, execution.AuthenticationEvidence{Method: execution.AuthMethodPlatformJWT, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	auth = store.WithTenantContext(auth, &store.TenantContext{OrgSlug: orgSlug, TeamSlug: "team", UserID: owner})
	registry := execution.NewBotTurnAuthorizationRegistry(32)
	defer registry.Close()
	worker, release, err := newProductionBotWorker(db, app, &launcher.ChatCatalogResources{Backend: backend, Pool: pool}, registry)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	defer worker.Stop()
	worker.schedulerReady.Store(true)
	evidence := &sapWorkflowResearchEvidence{calls: map[string]sapWorkflowCall{}, seen: map[string]bool{}, sources: map[string]bool{}}
	resolve := worker.config.ResolveModel
	worker.config.ResolveModel = func(c context.Context, p execution.Principal, s store.BotScope, n, m string) (model.LLM, error) {
		if n != selected || m != sapWorkflowModel {
			return nil, errors.New("unexpected SAP model selection")
		}
		v, e := resolve(c, p, s, n, m)
		if e != nil {
			return nil, e
		}
		configured, ok := v.(*botGenerationModel)
		if !ok {
			return nil, errors.New("production model capability wrapper missing")
		}
		cp := *configured
		cp.LLM = &sapWorkflowModelObserver{LLM: configured.LLM, evidence: evidence}
		return &cp, nil
	}
	svc := &store.Services{Bots: repo, PersonalBots: repo, Sessions: ss, PersonalSessions: ss, BotMandatePolicy: worker.config.MandatePolicy, BotTurnRegistry: registry, BotInteractiveReady: true, BotHomeSetup: worker.provisionHome, BotFilesFactory: worker.filesForRequest}
	router := mux.NewRouter()
	router.HandleFunc("/", api.BotCreateHandler).Methods("POST")
	router.HandleFunc("/bots/{botID}/messages", api.BotMessageHandler).Methods("POST")
	router.HandleFunc("/bots/{botID}/approvals/{approvalID}/approve-and-continue", api.BotApproveContinueHandler).Methods("POST")
	router.HandleFunc("/bots/{botID}/sessions/{sessionID}/events", api.BotEventsHandler).Methods("GET")
	router.HandleFunc("/bots/{botID}/sessions/{sessionID}/stream", api.BotHistoryStreamHandler).Methods("GET")
	router.HandleFunc("/bots/{botID}/runs/{runID}/reports/{reportSequence}/artifacts/{artifactID}/download", api.BotFileDownloadHandler).Methods("GET")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bound, stop := context.WithCancel(store.WithServices(auth, svc))
		unlink := context.AfterFunc(r.Context(), stop)
		defer unlink()
		defer stop()
		router.ServeHTTP(w, r.WithContext(bound))
	}))
	defer server.Close()
	post := func(endpoint string, value any, status int) []byte {
		t.Helper()
		raw, _ := json.Marshal(value)
		req, err := http.NewRequestWithContext(ctx, "POST", endpoint, strings.NewReader(string(raw)))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		res, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		raw, err = io.ReadAll(res.Body)
		if err != nil || res.StatusCode != status {
			t.Fatalf("owner admission HTTP=%d expected=%d (payload withheld)", res.StatusCode, status)
		}
		return raw
	}
	var created struct {
		store.Bot
		InitialTurn *store.BotMessageReceipt `json:"initial_turn"`
	}
	raw := post(server.URL+"?scope=personal", map[string]any{"name": sapWorkflowTitle, "start_from_title": !scheduleOnly, "idempotency_key": "sap-title"}, 201)
	if json.Unmarshal(raw, &created) != nil || (!scheduleOnly && created.InitialTurn == nil) {
		t.Fatal("missing title run")
	}
	home := &sandbox.BotHomeBinding{Scope: sandbox.BotHomeScope{OrgID: orgSlug, TeamID: "team", UserID: owner, BotID: created.ID.String()}, Backend: sandbox.BackendKindDocker}
	home.Ref = "astonish-bot-home-" + home.Scope.Key()
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 90*time.Second)
		defer stop()
		pool.Cleanup()
		if e := backend.DestroySession(cleanup, created.HomeSessionID.String()); e != nil && !strings.Contains(e.Error(), "is not registered") {
			t.Error(e)
		}
		if e := backend.DeleteBotHome(cleanup, home); e != nil {
			t.Error(e)
		}
		if e := backend.DeleteOrgNetwork(cleanup, orgSlug); e != nil {
			// Title-only turns never create the org network. Accept absence only
			// after Docker's authoritative listing verifies this exact name absent.
			networks, inspectErr := exec.CommandContext(cleanup, "docker", "network", "ls", "--filter", "name=^astonish-org-"+orgSlug+"$", "--format", "{{.Name}}").Output()
			if inspectErr != nil || strings.TrimSpace(string(networks)) != "" || !strings.Contains(e.Error(), "not found") {
				t.Error(e)
			}
		}
	}()
	if err = personal.SetSessionPin(auth, created.HomeSessionID.String(), selected, sapWorkflowModel); err != nil {
		t.Fatal(err)
	}
	streamBase := fmt.Sprintf("%s/bots/%s/sessions/%s", server.URL, created.ID, created.HomeSessionID)
	live, err := sapWorkflowStartStream(ctx, server.Client(), streamBase+"/events?scope=personal")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { live.cancel(); <-live.done }()
	var initial *store.BotRun
	if !scheduleOnly {
		if err = worker.execute(auth, repo, ss, created.ID, created.InitialTurn.Run.ID); err != nil {
			observed, readErr := repo.GetRun(auth, created.ID, created.InitialTurn.Run.ID)
			if readErr == nil && observed != nil {
				t.Logf("SAP_WORKFLOW title status=%s state_sequence=%d", observed.Status, observed.StateSequence)
			}
			t.Fatal("title execution failed", err)
		}
		initial, err = repo.GetRun(auth, created.ID, created.InitialTurn.Run.ID)
		if err != nil || initial.Status != store.RunWaitingApproval {
			t.Fatal("normal title clarification not observed")
		}
	}
	executeMessage := func(text, key string) *store.BotRun {
		t.Helper()
		current, err := repo.Get(auth, created.ID)
		if err != nil {
			t.Fatal(err)
		}
		var receipt store.BotMessageReceipt
		raw := post(fmt.Sprintf("%s/bots/%s/messages?scope=personal", server.URL, created.ID), map[string]any{"version": current.Version, "instructions": text, "idempotency_key": key}, 202)
		if json.Unmarshal(raw, &receipt) != nil {
			t.Fatal("invalid owner message receipt")
		}
		if err = worker.execute(auth, repo, ss, created.ID, receipt.Run.ID); err != nil {
			observed, readErr := repo.GetRun(auth, created.ID, receipt.Run.ID)
			if readErr == nil && observed != nil {
				t.Logf("SAP_WORKFLOW message_failure status=%s effect=%s reason=%s", observed.Status, observed.Report.EffectState, observed.TerminalReason)
			}
			principal, _ := execution.PrincipalFromContext(auth)
			redactor, redactErr := productionKnowledgeRedactor(db, app)(auth, principal, store.BotScopePersonal)
			if redactErr == nil && redactor != nil {
				var logCause func(error, int)
				logCause = func(cause error, depth int) {
					if cause == nil || depth > 8 {
						return
					}
					t.Logf("SAP_WORKFLOW error_depth=%d error_type=%T cause=%q", depth, cause, redactor.Redact(cause.Error()))
					if joined, ok := cause.(interface{ Unwrap() []error }); ok {
						for _, child := range joined.Unwrap() {
							logCause(child, depth+1)
						}
					} else if wrapped, ok := cause.(interface{ Unwrap() error }); ok {
						logCause(wrapped.Unwrap(), depth+1)
					}
				}
				logCause(err, 0)
			}
			var readiness *botScheduleReadinessError
			if errors.As(err, &readiness) && redactErr == nil && redactor != nil {
				t.Logf("SAP_WORKFLOW readiness_cause=%q", redactor.Redact(fmt.Sprint(readiness.cause)))
			}
			if input, inputErr := repo.(store.BotRuntimeStore).LoadRunInput(auth, created.ID, receipt.Run.ID); inputErr == nil && input.Work != nil && redactErr == nil && redactor != nil {
				w := input.Work
				t.Logf("SAP_WORKFLOW message_work phase=%s review_attempts=%d repairs=%d unresolved_count=%d", w.Phase, w.EmployeeReviewAttempts, w.Repairs, len(w.Unresolved))
				for i, unresolved := range w.Unresolved {
					if i >= 3 {
						break
					}
					text := redactor.Redact(unresolved)
					if len(text) > 1600 {
						text = text[:1600]
					}
					t.Logf("SAP_WORKFLOW message_unresolved=%q", text)
				}
				if w.AgentWork != nil {
					t.Logf("SAP_WORKFLOW message_disposition=%s evidence_refs=%d checks=%d", w.AgentWork.Disposition, len(w.AgentWork.Evidence), len(w.AgentWork.Checks))
				}
			}
			for _, job := range personal.ScheduledJobs().List(auth) {
				if job.Payload.BotID == created.ID.String() {
					t.Logf("SAP_WORKFLOW message_job enabled=%t next_run_present=%t mode=%s cron=%s", job.Enabled, job.NextRun != nil, job.Mode, job.Schedule.Cron)
				}
			}
			t.Fatal("real workflow execution failed", err)
		}
		run, err := repo.GetRun(auth, created.ID, receipt.Run.ID)
		if err != nil || run == nil || run.Status != store.RunSucceeded {
			t.Fatal("real workflow did not succeed")
		}
		return run
	}
	if scheduleOnly {
		// A separate scenario, not seeded research or a substitute for the full
		// research acceptance. The owner schedule turn uses the actual SAP model.
		if os.Getenv("ASTONISH_SAP_SEQUENTIAL_SCHEDULE") == "1" {
			previous := executeMessage("Reply with a short arithmetic report: 2 + 2 = 4. This is a completed-turn sequencing test. Do not research or use tools.", "sap-sequential-report")
			t.Logf("SAP_SEQUENTIAL completed_previous_run=%s status=%s", previous.ID, previous.Status)
		}
		schedule := executeMessage("Create and enable a recurring schedule every five minutes in UTC. Each run must say schedule test ready in this Bot's home chat. This is a schedule-only test: do not research, write files, or deliver externally. Apply the schedule now.", "sap-schedule-only")
		matched := 0
		parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
		for _, job := range personal.ScheduledJobs().List(auth) {
			if job.Payload.BotID != created.ID.String() {
				continue
			}
			parsed, e := parser.Parse(job.Schedule.Cron)
			if e != nil || !job.Enabled || job.NextRun == nil || job.Mode != "bot_trigger" || parsed.Next(*job.NextRun).Sub(*job.NextRun) != 5*time.Minute {
				t.Fatal("schedule-only job is not enabled every five minutes")
			}
			matched++
		}
		if matched != 1 {
			t.Fatalf("schedule-only expected one job, got %d", matched)
		}
		physical, e := sessionsRegistry.GetSession(created.HomeSessionID.String())
		if e != nil || physical == nil || !strings.HasPrefix(physical.ContainerName, "astonish-session-") {
			t.Fatal("schedule-only retained Docker session absent")
		}
		state, e := exec.CommandContext(ctx, "docker", "inspect", "--format", "{{.State.Running}} {{.Image}}", physical.ContainerName).Output()
		if e != nil || strings.TrimSpace(string(state)) != "true "+image {
			t.Fatal("schedule-only pinned Docker compute not running")
		}
		pub, e := sapWorkflowPublication(t, auth, repo, created.ID, schedule.ID)
		if e != nil || pub == nil || !pub.EmployeeSpeech || pub.Suppressed || schedule.Report.EmployeeMessage == nil || schedule.Report.EmployeeMessage.Origin == store.BotEmployeeMessageFallback {
			t.Fatal("schedule-only Employee result absent")
		}
		presentation, e := repo.(store.BotEmployeePresentationStore).ReadEmployeePresentation(auth, store.BotEmployeePresentationRequest{BotID: created.ID, RunID: schedule.ID, ClaimToken: "employee-final:" + schedule.ID.String()})
		if e != nil || presentation == nil || presentation.State != store.BotEmployeePresentationCompleted || strings.TrimSpace(presentation.FinalText) == "" || len(presentation.Turns) != 1 {
			t.Fatal("schedule-only terminal Employee presentation not completed")
		}
		t.Logf("SAP_SCHEDULE_ONLY_ACCEPTED model=%s physical_container=%s enabled_five_minute_jobs=%d status=%s presentation=%s final_text_bytes=%d", sapWorkflowModel, physical.ContainerName, matched, schedule.Status, presentation.State, len(presentation.FinalText))
		return
	}
	approvals, err := repo.ListApprovals(auth, created.ID, store.BotPage{})
	if err != nil || len(approvals) != 1 || approvals[0].Interaction == nil {
		t.Fatal("missing normal title question", err)
	}
	approval := approvals[0]
	post(fmt.Sprintf("%s/bots/%s/approvals/%s/approve-and-continue?scope=personal", server.URL, created.ID, approval.ID), map[string]any{"action_digest": approval.ActionDigest, "response": store.BotInteractionResponse{QuestionID: approval.Interaction.QuestionID, Answer: sapWorkflowResearch}}, 202)
	if err = worker.execute(auth, repo, ss, created.ID, initial.ID); err != nil {
		t.Fatal("confirmed title research failed", err)
	}
	research, err := repo.GetRun(auth, created.ID, initial.ID)
	// Public source reads can legitimately require an exact owner approval.
	// Never approve writes, credentials, browser actions or broad policy changes.
	for attempts := 0; err == nil && research != nil && research.Status == store.RunWaitingApproval && attempts < 8; attempts++ {
		pending, approvalErr := repo.ListApprovals(auth, created.ID, store.BotPage{})
		if approvalErr != nil {
			t.Fatal("read source approval", approvalErr)
		}
		approved := false
		for _, a := range pending {
			if a.Status != store.ApprovalPending || a.RunID != initial.ID {
				continue
			}
			var descriptor map[string]any
			_ = json.Unmarshal(a.Action.Arguments, &descriptor)
			u, parseErr := url.Parse(fmt.Sprint(descriptor["url"]))
			if parseErr != nil {
				u = &url.URL{}
			}
			t.Logf("SAP_WORKFLOW pending tool=%s interaction=%t method=%v host=%s path_present=%t exact_digest_present=%t", a.Action.Tool, a.Interaction != nil, descriptor["method"], u.Hostname(), u.Path != "", a.ActionDigest != "")
			if !sapWorkflowExactPublicRead(a) {
				continue
			}
			post(fmt.Sprintf("%s/bots/%s/approvals/%s/approve-and-continue?scope=personal", server.URL, created.ID, a.ID), map[string]any{"action_digest": a.ActionDigest}, 202)
			if executeErr := worker.execute(auth, repo, ss, created.ID, initial.ID); executeErr != nil {
				t.Fatal("approved public source read failed", executeErr)
			}
			research, err = repo.GetRun(auth, created.ID, initial.ID)
			approved = true
			break
		}
		if !approved {
			break
		}
	}
	if research != nil {
		if pending, approvalErr := repo.ListApprovals(auth, created.ID, store.BotPage{}); approvalErr == nil {
			for _, a := range pending {
				t.Logf("SAP_WORKFLOW approval status=%s tool=%s interaction=%t", a.Status, a.Action.Tool, a.Interaction != nil)
			}
		}
		principal, _ := execution.PrincipalFromContext(auth)
		redactor, redactErr := productionKnowledgeRedactor(db, app)(auth, principal, store.BotScopePersonal)
		diagnostic := func(text string) string {
			if redactErr != nil || redactor == nil {
				return "[diagnostic unavailable]"
			}
			text = redactor.Redact(text)
			if len(text) > 1600 {
				text = text[:1600]
			}
			return text
		}
		t.Logf("SAP_WORKFLOW terminal status=%s effect=%s reason=%s artifacts=%d limitation_count=%d", research.Status, research.Report.EffectState, research.TerminalReason, len(research.Report.Artifacts), len(research.Report.Unverified))
		if research.Report.ReviewLimitation != nil {
			t.Logf("SAP_WORKFLOW review_limitation=%q", diagnostic(research.Report.ReviewLimitation.Text))
		}
		if input, readErr := repo.(store.BotRuntimeStore).LoadRunInput(auth, created.ID, initial.ID); readErr == nil && input.Work != nil {
			w := input.Work
			t.Logf("SAP_WORKFLOW work phase=%s review_attempts=%d repairs=%d unresolved_count=%d", w.Phase, w.EmployeeReviewAttempts, w.Repairs, len(w.Unresolved))
			if w.Review != nil {
				t.Logf("SAP_WORKFLOW review recorded=true")
				for i, criterion := range w.Review.Criteria {
					if i >= 8 {
						break
					}
					t.Logf("SAP_WORKFLOW criterion verdict=%s finding=%q", criterion.Verdict, diagnostic(criterion.Explanation))
				}
			}
			for i, unresolved := range w.Unresolved {
				if i >= 3 {
					break
				}
				t.Logf("SAP_WORKFLOW unresolved=%q", diagnostic(unresolved))
			}
			if w.AgentWork != nil {
				t.Logf("SAP_WORKFLOW disposition=%s evidence_refs=%d checks=%d source_access=%d", w.AgentWork.Disposition, len(w.AgentWork.Evidence), len(w.AgentWork.Checks), len(w.AgentWork.SourceAccess))
			}
		}
		evidence.mu.Lock()
		t.Logf("SAP_WORKFLOW sources searches=%d failed=%d recovered=%d successful=%d", evidence.searches, evidence.failed, evidence.recovered, len(evidence.sources))
		evidence.mu.Unlock()
	}
	if err != nil || research == nil || research.Status != store.RunSucceeded {
		t.Fatal("confirmed title research did not succeed", err)
	}
	physical, err := sessionsRegistry.GetSession(created.HomeSessionID.String())
	if err != nil || physical == nil || !strings.HasPrefix(physical.ContainerName, "astonish-session-") {
		t.Fatal("real retained Docker session not observed")
	}
	state, err := exec.CommandContext(ctx, "docker", "inspect", "--format", "{{.State.Running}} {{.Image}}", physical.ContainerName).Output()
	if err != nil || strings.TrimSpace(string(state)) != "true "+image {
		t.Fatal("retained compute is not running the pinned candidate image")
	}
	t.Logf("SAP_WORKFLOW physical_container=%s image=%s", physical.ContainerName, image)
	evidence.mu.Lock()
	searches, failed, recovered := evidence.searches, evidence.failed, evidence.recovered
	sources := map[string]bool{}
	for u := range evidence.sources {
		sources[u] = true
	}
	evidence.mu.Unlock()
	if searches == 0 || failed == 0 || recovered == 0 || len(sources) < 2 {
		t.Fatalf("actual research evidence missing: searches=%d failed=%d successful-after-failure=%d source-count=%d", searches, failed, recovered, len(sources))
	}
	publication, err := sapWorkflowPublication(t, auth, repo, created.ID, research.ID)
	if err != nil || publication == nil || !publication.EmployeeSpeech || publication.Suppressed || strings.TrimSpace(publication.Text) == "" {
		t.Fatal("no real Employee research result", err)
	}
	if research.Report.EmployeeMessage == nil || research.Report.EmployeeMessage.Origin == store.BotEmployeeMessageFallback {
		t.Fatal("assistant fallback cannot certify research")
	}
	reportURLs := regexp.MustCompile(`https?://[^\s)\]>"\x60]+`)
	if len(publication.Text) < 1500 || len(reportURLs.FindAllString(publication.Text, -1)) < 2 {
		t.Fatal("Employee chat report lacks substantive source-linked findings")
	}
	matches := 0
	for u := range sources {
		if strings.Contains(publication.Text, u) {
			matches++
		}
	}
	if matches < 2 {
		t.Fatal("Employee chat report does not cite actual successful source reads")
	}
	for _, index := range []string{"S&P", "Nasdaq", "Dow"} {
		if !strings.Contains(strings.ToLower(publication.Text), strings.ToLower(index)) {
			t.Fatalf("Employee report missing %s coverage", index)
		}
	}
	t.Logf("SAP_WORKFLOW chat_report substantive=true characters=%d actual_cited_sources=%d", len(publication.Text), matches)
	var reportHashes []string
	for _, artifact := range publication.Artifacts {
		endpoint := fmt.Sprintf("%s/bots/%s/runs/%s/reports/%d/artifacts/%s/download?scope=personal", server.URL, created.ID, research.ID, publication.ReportSequence, artifact.ID)
		res, err := server.Client().Get(endpoint)
		if err != nil {
			t.Fatal(err)
		}
		body, readErr := io.ReadAll(res.Body)
		closeErr := res.Body.Close()
		if res.StatusCode != 200 || readErr != nil || closeErr != nil || int64(len(body)) != artifact.SizeBytes || fmt.Sprintf("%x", sha256.Sum256(body)) != artifact.SHA256 {
			t.Fatal("published report download mismatch")
		}
		if len(body) < 1500 || len(reportURLs.FindAll(body, -1)) < 2 {
			t.Fatal("report lacks substantive source-linked findings")
		}
		matches := 0
		for u := range sources {
			if strings.Contains(string(body), u) {
				matches++
			}
		}
		if matches < 2 {
			t.Fatal("report does not cite actual successful source reads")
		}
		reportHashes = append(reportHashes, artifact.SHA256)
	}
	schedule := executeMessage(sapWorkflowSchedule, "sap-five-minute")
	jobs := personal.ScheduledJobs().List(auth)
	matched := 0
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	for _, job := range jobs {
		if job.Payload.BotID != created.ID.String() {
			continue
		}
		c, err := parser.Parse(job.Schedule.Cron)
		if err != nil || !job.Enabled || job.NextRun == nil || job.Mode != "bot_trigger" {
			t.Fatal("schedule not enabled and runnable")
		}
		next := c.Next(*job.NextRun)
		if next.Sub(*job.NextRun) != 5*time.Minute {
			t.Fatal("schedule is not every five minutes")
		}
		matched++
	}
	if matched != 1 {
		t.Fatalf("expected exactly one enabled five-minute job, got %d", matched)
	}
	unchanged, err := repo.(store.BotPublicationReader).ReadBotPublication(auth, created.ID, research.ID, publication.ReportSequence)
	if err != nil || unchanged == nil || unchanged.Text != publication.Text || !reflect.DeepEqual(publication.Artifacts, unchanged.Artifacts) {
		t.Fatal("scheduling changed committed report")
	}
	for _, artifact := range unchanged.Artifacts {
		endpoint := fmt.Sprintf("%s/bots/%s/runs/%s/reports/%d/artifacts/%s/download?scope=personal", server.URL, created.ID, research.ID, unchanged.ReportSequence, artifact.ID)
		res, err := server.Client().Get(endpoint)
		if err != nil {
			t.Fatal(err)
		}
		hash := sha256.New()
		n, copyErr := io.Copy(hash, res.Body)
		closeErr := res.Body.Close()
		if res.StatusCode != 200 || copyErr != nil || closeErr != nil || n != artifact.SizeBytes || fmt.Sprintf("%x", hash.Sum(nil)) != artifact.SHA256 {
			t.Fatal("schedule froze or replaced completed report download")
		}
	}
	history, err := sapWorkflowStartStream(ctx, server.Client(), streamBase+"/stream?scope=personal")
	if err != nil {
		t.Fatal(err)
	}
	defer history.cancel()
	select {
	case <-history.done:
	case <-ctx.Done():
		t.Fatal("history deadline")
	}
	expected := history.snapshot()
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		got := live.snapshot()
		if len(got) >= len(expected) {
			if !reflect.DeepEqual(got, expected) {
				t.Fatal("live SSE and retained history differ")
			}
			break
		}
		select {
		case <-tick.C:
		case <-timer.C:
			t.Fatal("live SSE did not deliver committed final history")
		case <-ctx.Done():
			t.Fatal("workflow deadline")
		}
	}
	finalPub, err := sapWorkflowPublication(t, auth, repo, created.ID, schedule.ID)
	if err != nil || finalPub == nil || !finalPub.EmployeeSpeech || finalPub.Suppressed || schedule.Report.EmployeeMessage == nil || schedule.Report.EmployeeMessage.Origin == store.BotEmployeeMessageFallback {
		t.Fatal("no Employee scheduling result")
	}
	for _, pub := range []*store.BotPublication{publication, finalPub} {
		needle, _ := json.Marshal(pub.Text)
		found := false
		for _, frame := range expected {
			if strings.Contains(frame.Data, string(needle)) {
				found = true
				break
			}
		}
		if !found {
			t.Fatal("Employee result missing from live and restored conversation")
		}
	}
	t.Logf("SAP_WORKFLOW_ACCEPTED searches=%d source_failures=%d alternatives=%d cited_sources=%d artifacts=%d report_hashes=%v enabled_five_minute_jobs=%d live_history_frames=%d", searches, failed, recovered, len(sources), len(publication.Artifacts), reportHashes, matched, len(expected))
}

func sapWorkflowPublication(t *testing.T, ctx context.Context, repo store.BotStore, botID store.BotID, runID store.RunID) (*store.BotPublication, error) {
	t.Helper()
	var after int64
	var latest *store.BotPublication
	for {
		page, err := repo.ListActivity(ctx, botID, after, 200)
		if err != nil {
			return nil, err
		}
		for _, activity := range page {
			after = activity.Sequence
			if activity.Kind != store.BotActivityReport || activity.RunID == nil || *activity.RunID != runID {
				continue
			}
			var pub store.BotPublication
			if json.Unmarshal(activity.Payload, &pub) == nil && pub.BotID == botID && pub.RunID == runID && pub.ReportSequence > 0 && pub.EventID != "" {
				checked, err := repo.(store.BotPublicationReader).ReadBotPublication(ctx, botID, runID, pub.ReportSequence)
				if err != nil {
					return nil, err
				}
				latest = checked
			}
		}
		if len(page) < 200 {
			break
		}
	}
	if latest == nil {
		return nil, store.ErrBotNotFound
	}
	return latest, nil
}

func sapWorkflowExactPublicRead(a store.BotApproval) bool {
	if a.Interaction != nil || a.Status != store.ApprovalPending || a.Action.Tool != "http_request" || time.Now().After(a.ExpiresAt) {
		return false
	}
	digest, err := a.Action.Digest()
	if err != nil || digest != a.ActionDigest {
		return false
	}
	var args map[string]any
	if json.Unmarshal(a.Action.Arguments, &args) != nil {
		return false
	}
	for k, v := range args {
		switch k {
		case "url", "method", "timeout", "max_bytes":
		case "credential", "body", "body_path", "content_type":
			if v != nil && v != "" {
				return false
			}
		case "headers":
			if v != nil {
				if h, ok := v.(map[string]any); !ok || len(h) != 0 {
					return false
				}
			}
		case "multipart":
			if v != nil {
				if p, ok := v.([]any); !ok || len(p) != 0 {
					return false
				}
			}
		default:
			return false
		}
	}
	method, _ := args["method"].(string)
	if method != "" && method != "GET" {
		return false
	}
	raw, _ := args["url"].(string)
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Port() != "" && u.Port() != "443") {
		return false
	}
	// A closed public-news host list avoids localhost/private-network requests.
	for _, host := range []string{"reuters.com", "www.reuters.com", "apnews.com", "www.cnbc.com", "finance.yahoo.com", "www.marketwatch.com", "www.nasdaq.com", "www.bbc.com", "www.theguardian.com", "www.investing.com", "www.cnn.com", "edition.cnn.com", "www.ft.com", "www.bloomberg.com"} {
		if u.Hostname() == host {
			return true
		}
	}
	return false
}

func TestSAPWorkflowExactPublicRead(t *testing.T) {
	for _, tc := range []struct {
		args string
		want bool
	}{
		{`{"url":"https://www.cnbc.com/markets/","method":"GET"}`, true},
		{`{"url":"https://www.reuters.com/markets/"}`, true},
		{`{"url":"https://www.cnbc.com/markets/","method":"POST"}`, false},
		{`{"url":"https://127.0.0.1/private"}`, false},
		{`{"url":"https://www.cnbc.com/markets/?token=secret"}`, false},
		{`{"url":"https://www.cnbc.com/markets/","credential":"stored"}`, false},
		{`{"url":"https://www.cnbc.com/markets/","headers":{"Authorization":"secret"}}`, false},
		{`{"url":"https://www.cnbc.com/markets/","body":"upload"}`, false},
	} {
		a := store.BotApproval{Status: store.ApprovalPending, ExpiresAt: time.Now().Add(time.Minute), Action: execution.ActionDescriptor{Tool: "http_request", Effect: execution.EffectUnknown, Arguments: json.RawMessage(tc.args)}}
		a.ActionDigest, _ = a.Action.Digest()
		if sapWorkflowExactPublicRead(a) != tc.want {
			t.Errorf("exact public read mismatch for %s", tc.args)
		}
		a.ActionDigest = "stale"
		if sapWorkflowExactPublicRead(a) {
			t.Fatal("stale digest approved")
		}
		a.Action.Tool = "write_file"
		a.ActionDigest, _ = a.Action.Digest()
		if sapWorkflowExactPublicRead(a) {
			t.Fatal("write approved")
		}
	}
}

func TestSAPWorkflowSourceFailureEvidence(t *testing.T) {
	for _, tc := range []struct {
		value  any
		failed bool
	}{{map[string]any{"status": float64(403)}, true}, {map[string]any{"result": map[string]any{"error": "connection reset"}}, true}, {map[string]any{"result": `{"status":503}`}, true}, {map[string]any{"error": "policy forbidden"}, false}, {map[string]any{"result": map[string]any{"status": float64(200), "content": "Actual public source"}}, false}} {
		if sapWorkflowSourceFailed(tc.value) != tc.failed {
			t.Fatalf("source classification mismatch")
		}
	}
	if !sapWorkflowSourceSucceeded(map[string]any{"result": map[string]any{"status": float64(200), "content": strings.Repeat("actual source ", 40)}}) || sapWorkflowSourceSucceeded(map[string]any{"error": "long tool error"}) {
		t.Fatal("successful source requires 2xx and substantive body")
	}
}

func TestSAPWorkflowLiveHistoryStreamReader(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "id: one\nevent: message\ndata: {\"text\":\"Employee result\"}\n\nid: one\nevent: replay_complete\ndata: {}\n\nid: one\nevent: done\ndata: {}\n\n")
	}))
	defer server.Close()
	stream, err := sapWorkflowStartStream(t.Context(), server.Client(), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.cancel()
	<-stream.done
	frames := stream.snapshot()
	if len(frames) != 1 || frames[0].ID != "one" || frames[0].Event != "message" || frames[0].Data != `{"text":"Employee result"}` {
		t.Fatalf("stream normalization=%+v", frames)
	}
}
