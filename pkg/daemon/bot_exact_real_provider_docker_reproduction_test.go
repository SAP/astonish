package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
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
	"google.golang.org/adk/tool"
	"google.golang.org/genai"
	"io"
	"iter"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Opt-in real-provider reproduction. Never replaces model responses or native tools.
// Logs phase times and HTTP milestones only, not credentials or tool arguments.
type exactRunnable interface {
	tool.Tool
	Declaration() *genai.FunctionDeclaration
	Run(tool.Context, any) (map[string]any, error)
	ProcessRequest(tool.Context, *model.LLMRequest) error
}
type exactTimedTool struct {
	exactRunnable
	t *testing.T
}
type exactToolContext struct {
	tool.Context
	traced context.Context
}

func (c exactToolContext) Value(k any) any { return c.traced.Value(k) }
func exactHTTPTrace(c context.Context, t *testing.T, name string) context.Context {
	start := time.Now()
	return httptrace.WithClientTrace(c, &httptrace.ClientTrace{
		GetConn: func(string) {
			t.Logf("%s HTTP %s get_conn elapsed=%s", time.Now().UTC().Format(time.RFC3339Nano), name, time.Since(start))
		},
		GotConn: func(httptrace.GotConnInfo) {
			t.Logf("%s HTTP %s got_conn elapsed=%s", time.Now().UTC().Format(time.RFC3339Nano), name, time.Since(start))
		},
		WroteRequest: func(httptrace.WroteRequestInfo) {
			t.Logf("%s HTTP %s wrote_request elapsed=%s", time.Now().UTC().Format(time.RFC3339Nano), name, time.Since(start))
		},
		GotFirstResponseByte: func() {
			t.Logf("%s HTTP %s first_byte elapsed=%s", time.Now().UTC().Format(time.RFC3339Nano), name, time.Since(start))
		},
	})
}
func (t *exactTimedTool) Run(c tool.Context, a any) (map[string]any, error) {
	start := time.Now()
	t.t.Logf("%s INNER_BEGIN %s", start.UTC().Format(time.RFC3339Nano), t.Name())
	r, e := t.exactRunnable.Run(exactToolContext{Context: c, traced: exactHTTPTrace(c, t.t, t.Name())}, a)
	t.t.Logf("%s INNER_END %s elapsed=%s err=%v status=%v", time.Now().UTC().Format(time.RFC3339Nano), t.Name(), time.Since(start), e, r["status"])
	return r, e
}

type exactTimedModel struct {
	model.LLM
	t *testing.T
}

func (m *exactTimedModel) GenerateContent(c context.Context, r *model.LLMRequest, s bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		start := time.Now()
		m.t.Logf("%s MODEL_BEGIN model=%s auxiliary=%t", start.UTC().Format(time.RFC3339Nano), m.Name(), betweenCallProgressRequest(r))
		defer func() {
			m.t.Logf("%s MODEL_END elapsed=%s", time.Now().UTC().Format(time.RFC3339Nano), time.Since(start))
		}()
		for v, e := range m.LLM.GenerateContent(exactHTTPTrace(c, m.t, "model"), r, s) {
			if !yield(v, e) {
				return
			}
		}
	}
}
func exactTraceModel(v model.LLM, t *testing.T) model.LLM {
	if configured, ok := v.(*botGenerationModel); ok {
		copy := *configured
		copy.LLM = &exactTimedModel{LLM: configured.LLM, t: t}
		return &copy
	}
	return &exactTimedModel{LLM: v, t: t}
}

func TestExactTraceModelPreservesCapacity(t *testing.T) {
	original := NewBotGenerationModel(nil, 196000, 32768)
	traced := exactTraceModel(original, t)
	if _, ok := traced.(*botGenerationModel); !ok {
		t.Fatal("tracing hid resolved numeric model capabilities")
	}
	if got := botModelContextWindow(t.Context(), traced); got != 196000 {
		t.Fatalf("tracing changed context capacity: %d", got)
	}
	if got := traced.(*botGenerationModel).outputCapacity; got != 32768 {
		t.Fatalf("tracing changed output capacity: %d", got)
	}
}

func TestBotExactRealProviderDockerReproduction(t *testing.T) {
	runBotExactDockerReproduction(t, nil)
}

func runBotExactDockerReproduction(t *testing.T, captured model.LLM) {
	if os.Getenv("ASTONISH_BOT_EXACT_LIVE_REPRO") != "1" || os.Getenv("ASTONISH_TEST_DOCKER") != "1" {
		t.Skip("explicit real Docker opt-in required")
	}
	image, layers := os.Getenv("ASTONISH_SANDBOX_IMAGE"), os.Getenv("ASTONISH_BOT_HOME_TEST_LAYERS")
	if image == "" || !filepath.IsAbs(layers) {
		t.Fatal("explicit image and absolute existing layers required")
	}
	app, err := config.LoadAppConfig()
	if err != nil {
		t.Fatal(err)
	}
	dsn := os.Getenv("ASTONISH_LIVE_READONLY_DSN")
	if !strings.HasPrefix(dsn, "file:") || !strings.Contains(dsn, "mode=ro") || os.Getenv("ASTONISH_LIVE_DATA_DIR") == "" {
		t.Fatal("explicit read-only SQLite source and data directory required")
	}
	live, err := entstore.New(t.Context(), entstore.Config{DSN: dsn, DataDir: os.Getenv("ASTONISH_LIVE_DATA_DIR")})
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()
	ps, err := live.PlatformSettings().Get(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	osx, err := live.OrgSettings("my-org").Get(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	sourceOrg, err := live.ForOrg("my-org")
	if err != nil {
		t.Fatal(err)
	}
	ts, err := sourceOrg.ForTeam("general").Settings().Get(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	fixtureBytes, err := os.ReadFile(os.Getenv("ASTONISH_EXACT_FIXTURE"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Prompts []struct {
			Text string `json:"text"`
		} `json:"owner_prompt_sequence"`
	}
	if err = json.Unmarshal(fixtureBytes, &fixture); err != nil || len(fixture.Prompts) != 2 {
		t.Fatal("exact two prompts required", err)
	}
	t.Logf("EXACT prompt_sha256=%x,%x", sha256.Sum256([]byte(fixture.Prompts[0].Text)), sha256.Sum256([]byte(fixture.Prompts[1].Text)))
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", wd)
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Minute)
	t.Cleanup(cancel)
	raw, err := exec.CommandContext(ctx, "docker", "image", "inspect", "--format", "{{.Id}}", image).CombinedOutput()
	if err != nil {
		t.Fatalf("image preflight: %s %v", raw, err)
	}
	t.Logf("%s real Docker image=%s id=%s isolated_root=%s", time.Now().UTC().Format(time.RFC3339Nano), image, strings.TrimSpace(string(raw)), root)
	metrics := &continuationMetrics{stage: continuationCreate}
	defer metrics.report(t, "final")

	sandbox.RegisterBackendFactory(sandbox.BackendKindDocker, func(fc sandbox.BackendFactoryConfig) (sandbox.Backend, error) {
		b, e := dockersandbox.New(dockersandbox.Config{Sessions: fc.Sessions, SandboxImage: image, LayersDir: layers, UppersDir: filepath.Join(root, "uppers"), OverlayMode: dockersandbox.OverlayModeFuse})
		if e != nil {
			return nil, e
		}
		return &continuationDockerTrace{DockerBackend: b, t: t, metrics: metrics}, nil
	})
	t.Cleanup(func() {
		sandbox.RegisterBackendFactory(sandbox.BackendKindDocker, func(fc sandbox.BackendFactoryConfig) (sandbox.Backend, error) {
			return dockersandbox.New(dockersandbox.Config{Sessions: fc.Sessions, SandboxImage: fc.Docker.SandboxImage, OverlayMode: dockersandbox.OverlayMode(fc.Docker.OverlayMode), LayersDir: fc.Docker.LayersDir, UppersDir: fc.Docker.UppersDir, ContainerRuntimePath: fc.Docker.ContainerRuntimePath})
		})
	})
	orgSlug := "exact-live-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	network := "astonish-org-" + orgSlug
	raw, err = exec.CommandContext(ctx, "docker", "network", "ls", "--filter", "name=^"+network+"$", "--format", "{{.Name}}").CombinedOutput()
	if err != nil || strings.TrimSpace(string(raw)) != "" {
		t.Fatalf("network must not preexist: %s %v", raw, err)
	}
	db, owner := setupChannelResolverFixture(t, orgSlug, "team", "telegram", "isolated-between-call")
	org, err := db.ForOrg(orgSlug)
	if err != nil {
		t.Fatal(err)
	}
	// Only settings needed for the same provider/search routes; no production writes.
	ps.Channels = nil
	ps.Auth = nil
	if err = db.PlatformSettings().Save(ctx, ps); err != nil {
		t.Fatal(err)
	}
	if err = db.OrgSettings(orgSlug).Save(ctx, osx); err != nil {
		t.Fatal(err)
	}
	if err = org.ForTeam("team").Settings().Save(ctx, ts); err != nil {
		t.Fatal(err)
	}
	repo, ss := org.ForUser(owner).Bots(), org.ForUser(owner).Sessions()
	auth, err := execution.WithPrincipal(ctx, execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: owner, OrgSlug: orgSlug, TeamSlug: "team", Authenticated: true})
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
	production, release, err := newProductionBotWorker(db, app, botTestChatResources(t, app), registry)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
	t.Cleanup(production.Stop)
	cfg := production.config
	resolve := cfg.ResolveModel
	cfg.ResolveModel = func(c context.Context, p execution.Principal, s store.BotScope, n, m string) (model.LLM, error) {
		if captured != nil {
			if genuine, ok := captured.(*genuineCorrectedDeliveryModel); ok && genuine.real == nil {
				v, err := resolve(c, p, s, n, m)
				if err != nil {
					return nil, err
				}
				genuine.real = v
			}
			return NewBotGenerationModel(captured, 1050000, 64000), nil
		}
		v, e := resolve(c, p, s, n, m)
		if e != nil {
			return nil, e
		}
		traced := exactTraceModel(v, t)
		if configured, ok := traced.(*botGenerationModel); ok {
			t.Logf("EXACT MODEL_CAPACITY context=%d output=%d", configured.contextWindow, configured.outputCapacity)
		}
		return traced, nil
	}
	prepare := cfg.Prepare
	cfg.Prepare = func(c context.Context, r bot.RunRequest, s *launcher.BotChatScope) (*launcher.BotChatSafeResources, error) {
		v, e := prepare(c, r, s)
		if e != nil {
			t.Logf("EXACT PREPARE_FAILURE cause=%v context=%v", e, c.Err())
			return v, e
		}
		if genuine, ok := captured.(*genuineCorrectedDeliveryModel); ok && genuine.sourceOffset == 0 {
			// Explicit historical input setup under the active run's normal commit
			// fence. This is not attributed to model research or external execution.
			h := &sandbox.BotHomeBinding{Scope: sandbox.BotHomeScope{OrgID: orgSlug, TeamID: "team", UserID: owner, BotID: r.Bot.ID.String()}, Backend: sandbox.BackendKindDocker}
			h.Ref = "astonish-bot-home-" + h.Scope.Key()
			backend := cfg.Sandbox.(interface {
				WriteBotHomeFile(context.Context, *sandbox.BotHomeBinding, store.BotFileWrite) (*store.BotFileEntry, error)
			})
			entry, err := backend.WriteBotHomeFile(c, h, store.BotFileWrite{Path: "outputs/historical-captured-research.json", Body: strings.NewReader(genuine.sources), SizeBytes: int64(len(genuine.sources)), CreateOnly: true, BeforeCommit: func(commit func() error) error {
				lease := r.Run.Lease
				if r.CurrentLease != nil {
					lease = r.CurrentLease()
				}
				return repo.(store.BotFileCommitStore).WithBotFileCommit(c, r.Bot.Home, lease, commit)
			}})
			if err != nil || entry == nil || entry.SHA256 != fmt.Sprintf("%x", sha256.Sum256([]byte(genuine.sources))) {
				return nil, fmt.Errorf("historical fixture write: %v", err)
			}
			genuine.sourceOffset = len(genuine.sources)
			t.Logf("HISTORICAL_INPUT_FIXTURE bytes=%d sha256=%s fresh_research=false model_effect=false", entry.SizeBytes, entry.SHA256)
		}
		for i, x := range v.Tools {
			if runnable, ok := x.(exactRunnable); ok && (x.Name() == "perplexity_web_search" || x.Name() == "web_fetch" || x.Name() == "http_request") {
				v.Tools[i] = &exactTimedTool{exactRunnable: runnable, t: t}
			}
		}
		t.Logf("EXACT runtime=%+v budget=%+v native_tools=%d", r.Version.Budget.RuntimePolicy, r.Version.Budget, len(v.Tools))
		return v, nil
	}
	worker, err := NewBotWorker(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(worker.Stop)
	worker.schedulerReady.Store(true)
	svc := &store.Services{Bots: repo, PersonalBots: repo, Sessions: ss, PersonalSessions: ss, BotMandatePolicy: cfg.MandatePolicy, BotTurnRegistry: registry, BotInteractiveReady: true, BotHomeSetup: worker.provisionHome, BotFilesFactory: worker.filesForRequest}
	router := mux.NewRouter()
	router.HandleFunc("/", api.BotCreateHandler).Methods("POST")
	router.HandleFunc("/bots/{botID}/messages", api.BotMessageHandler).Methods("POST")
	router.HandleFunc("/bots/{botID}/files/download", api.BotFileDownloadHandler).Methods("GET")
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
		defer res.Body.Close()
		raw, e := io.ReadAll(res.Body)
		if e != nil || res.StatusCode != status {
			t.Fatalf("HTTP %d body=%s err=%v", res.StatusCode, raw, e)
		}
		return raw
	}
	raw = post(server.URL+"?scope=personal", map[string]any{"name": fixture.Prompts[0].Text, "start_from_title": true, "idempotency_key": "between-call-initial"}, 201)
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
		cfg.Pool.Cleanup()
		for _, id := range sessions {
			if e := cfg.Sandbox.DestroySession(c, id); e != nil && !strings.Contains(e.Error(), "is not registered") {
				t.Errorf("cleanup %s: %v", id, e)
			}
		}
		if e := cfg.Sandbox.(sandbox.BotHomeDeleteBackend).DeleteBotHome(c, home); e != nil {
			t.Errorf("cleanup home: %v", e)
		}
		raw, e := exec.CommandContext(c, "docker", "network", "inspect", network).CombinedOutput()
		if e != nil {
			if !strings.Contains(string(raw), "not found") && !strings.Contains(string(raw), "No such network") {
				t.Errorf("network inspect: %s %v", raw, e)
			}
			return
		}
		var rows []struct {
			Name       string
			Labels     map[string]string
			Containers map[string]json.RawMessage
		}
		if e = json.Unmarshal(raw, &rows); e != nil || len(rows) != 1 || rows[0].Name != network || rows[0].Labels["astonish.org"] != orgSlug || len(rows[0].Containers) != 0 {
			t.Errorf("refuse nonowned or attached network cleanup: %s", raw)
			return
		}
		if e = cfg.Sandbox.DeleteOrgNetwork(c, orgSlug); e != nil {
			t.Errorf("network cleanup: %v", e)
		} else {
			t.Logf("removed exact unattached test network %s", network)
		}
	})
	if err = org.ForUser(owner).SetSessionPin(auth, created.HomeSessionID.String(), "New AI Core Provider", "gpt-6-astra"); err != nil {
		t.Fatal(err)
	}
	metrics.setStage(continuationInitial)
	if err = worker.execute(auth, repo, ss, created.ID, created.InitialTurn.Run.ID); err != nil {
		t.Fatal(err)
	}
	initial, err := repo.GetRun(auth, created.ID, created.InitialTurn.Run.ID)
	if err != nil || initial.Status != store.RunWaitingApproval {
		t.Fatalf("initial question: %+v %v", initial, err)
	}
	current, err := repo.Get(auth, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	metrics.setStage(continuationAnswer)
	message := map[string]any{"version": current.Version, "instructions": fixture.Prompts[1].Text, "idempotency_key": "between-call-new-message"}
	// Original prompts remain unchanged; captured sources are read from retained Home below.
	raw = post(fmt.Sprintf("%s/bots/%s/messages?scope=personal", server.URL, created.ID), message, 202)
	var receipt store.BotMessageReceipt
	if err = json.Unmarshal(raw, &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.Run.ID == initial.ID {
		t.Fatal("new message incorrectly resumed initial approval run")
	}
	sessions = append(sessions, receipt.Run.SessionID.String())
	t.Logf("NEW_MESSAGE initial_run=%s new_run=%s session=%s", initial.ID, receipt.Run.ID, receipt.Run.SessionID)
	metrics.setStage(continuationStartup)
	if err = worker.execute(auth, repo, ss, created.ID, receipt.Run.ID); err != nil {
		t.Errorf("new message execution: %v", err)
		var failure *bot.RunError
		if errors.As(err, &failure) {
			t.Logf("EXACT FAILURE class=%s cause=%v", failure.Class, failure.Cause)
		}
	}
	metrics.setStage(continuationVerification)
	final, e := repo.GetRun(auth, created.ID, receipt.Run.ID)
	t.Logf("EXACT FINAL bot=%s run=%s err=%v", created.ID, receipt.Run.ID, e)
	if e != nil || final == nil {
		t.Fatal("missing final run", e)
	}
	t.Logf("EXACT status=%s usage=%+v", final.Status, final.Usage)
	files, e := worker.filesForRequest(auth, svc, store.BotScopePersonal)
	if e != nil {
		t.Error(e)
	} else {
		entries, listErr := files.List(auth, created.ID, ".", "", 200)
		t.Logf("EXACT FILES %+v err=%v", entries, listErr)
		if listErr != nil {
			t.Error(listErr)
		}
		var verifyDirectory func(string, int)
		var deliverables int
		verifyDirectory = func(directory string, depth int) {
			if depth > 8 {
				t.Error("file evidence depth exceeds bound")
				return
			}
			cursor := ""
			for {
				page, err := files.List(auth, created.ID, directory, cursor, 200)
				if err != nil {
					t.Error(err)
					return
				}
				for _, entry := range page.Entries {
					if entry.Kind == store.BotFileDirectory {
						verifyDirectory(entry.Path, depth+1)
						continue
					}
					if depth == 0 && (entry.Path == "MEMORY.md" || entry.Path == "SOUL.md" || entry.Path == "USER.md" || entry.Path == "IDENTITY.md" || entry.Path == "CHARTER.md") {
						continue
					}
					stream, err := files.Open(auth, created.ID, entry.Path, store.BotFileRange{})
					if err != nil {
						t.Error(err)
						continue
					}
					hash := sha256.New()
					var destination *os.File
					writer := io.Writer(hash)
					if output := os.Getenv("ASTONISH_EXACT_DELIVERABLE_DIR"); output != "" {
						if !filepath.IsLocal(entry.Path) {
							t.Fatal("nonlocal deliverable path")
						}
						path := filepath.Join(output, entry.Path)
						if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
							t.Fatal(err)
						}
						destination, err = os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
						if err != nil {
							t.Fatal(err)
						}
						writer = io.MultiWriter(hash, destination)
					}
					n, err := io.Copy(writer, stream)
					if destination != nil {
						err = errors.Join(err, destination.Close())
					}
					closeErr := stream.Close()
					if err != nil || closeErr != nil || n != entry.SizeBytes {
						t.Errorf("file verification %s bytes=%d err=%v close=%v", entry.Path, n, err, closeErr)
						continue
					}
					t.Logf("EXACT DELIVERABLE path=%s bytes=%d sha256=%x", entry.Path, n, hash.Sum(nil))
					if captured != nil {
						// Ordinary retained-file download is not a committed artifact card.
						// Keep the strict publication assertion below independent.
						res, getErr := server.Client().Get(fmt.Sprintf("%s/bots/%s/files/download?scope=personal&path=%s", server.URL, created.ID, url.QueryEscape(entry.Path)))
						if getErr != nil {
							t.Fatal(getErr)
						}
						h := sha256.New()
						downloaded, copyErr := io.Copy(h, res.Body)
						closeErr := res.Body.Close()
						if res.StatusCode != http.StatusOK || copyErr != nil || closeErr != nil || downloaded != n || fmt.Sprintf("%x", h.Sum(nil)) != fmt.Sprintf("%x", hash.Sum(nil)) || !strings.HasPrefix(res.Header.Get("Content-Disposition"), "attachment;") {
							t.Fatalf("captured retained download status=%d bytes=%d copy=%v close=%v", res.StatusCode, downloaded, copyErr, closeErr)
						}
						t.Logf("CAPTURED_RETAINED_HTTP_DOWNLOAD path=%s status=%d bytes=%d sha256=%x publication_verified=false", entry.Path, res.StatusCode, downloaded, h.Sum(nil))
					}
					deliverables++
				}
				if page.Next == "" {
					break
				}
				cursor = page.Next
			}
		}
		verifyDirectory(".", 0)
		if deliverables == 0 {
			t.Error("no non-bootstrap deliverable files found")
		}
	}
	if captured != nil {
		if len(final.Report.Artifacts) != 1 || final.Report.Artifacts[0].ContentRef == nil {
			t.Fatalf("captured completion lacks committed artifact: status=%s artifacts=%d summary=%q unverified=%v", final.Status, len(final.Report.Artifacts), final.Report.Summary, final.Report.Unverified)
		}
		artifact := final.Report.Artifacts[0]
		wantSize, wantHash := int64(47138), "726b6a77d1c633decaa611794bd83b9b7f1ce7f1cbdc407172484de5eaee7271"
		if _, ok := captured.(*genuineCorrectedDeliveryModel); ok {
			wantSize, wantHash = 45639, "b5a06dff23615f0947c2c8d63115a8930853eaa68ddf13876b59839171677302"
		}
		if artifact.SizeBytes != wantSize || artifact.SHA256 != wantHash {
			t.Fatal("captured completion bytes changed")
		}
		if len(final.Report.Summary) > 1000 {
			t.Fatal("summary contains full captured body")
		}
		t.Logf("CAPTURED_COMMITTED artifact=%s key=%s status=%s", artifact.ID, artifact.ContentRef.Key, final.Status)
		if genuine, ok := captured.(*genuineCorrectedDeliveryModel); ok {
			if final.Status != store.RunSucceeded {
				t.Fatalf("genuine corrected run not completed: %s", final.Status)
			}
			pub, err := repo.(store.BotPublicationStore).PublishReport(auth, store.BotPublicationRequest{BotID: created.ID, RunID: final.ID, Now: time.Now()})
			if err != nil || pub == nil {
				t.Fatalf("native publication: %v", err)
			}
			if pub.Suppressed || len(pub.Artifacts) != 1 || pub.Artifacts[0].ID != artifact.ID || pub.ReportSequence < 1 || pub.EventID == "" {
				t.Fatalf("invalid native card: %+v", pub)
			}
			router.HandleFunc("/bots/{botID}/runs/{runID}/reports/{reportSequence}/artifacts/{artifactID}/download", api.BotFileDownloadHandler).Methods("GET")
			res, err := server.Client().Get(fmt.Sprintf("%s/bots/%s/runs/%s/reports/%d/artifacts/%s/download?scope=personal", server.URL, created.ID, final.ID, pub.ReportSequence, artifact.ID))
			if err != nil {
				t.Fatal(err)
			}
			data, err := io.ReadAll(res.Body)
			closeErr := res.Body.Close()
			if err != nil || closeErr != nil || res.StatusCode != 200 || string(data) != genuine.body {
				t.Fatalf("native download status=%d bytes=%d error=%v close=%v", res.StatusCode, len(data), err, closeErr)
			}
			if err = os.WriteFile(filepath.Join(genuine.dir, "native-downloaded-design.md"), data, 0600); err != nil {
				t.Fatal(err)
			}
			raw, _ := json.MarshalIndent(pub, "", "  ")
			if err = os.WriteFile(filepath.Join(genuine.dir, "native-publication.json"), raw, 0600); err != nil {
				t.Fatal(err)
			}
			t.Logf("GENUINE_NATIVE_DOWNLOAD artifact=%s report_sequence=%d event=%s HTTP=200 bytes=%d sha256=%x", artifact.ID, pub.ReportSequence, pub.EventID, len(data), sha256.Sum256(data))
		}
	}
	if e != nil || (final.Status != store.RunSucceeded && !(captured != nil && final.Status == store.RunPartial)) {
		t.Errorf("exact task did not complete: status=%s err=%v", final.Status, e)
	}
}
