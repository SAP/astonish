package api_test

// This opt-in diagnostic uses live native public reads, never recorded sources.
// Only SQLite/Home/node transport are isolated fixtures; worker/factory/controller
// and every model stage are production implementations.
import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/daemon"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/launcher"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/provider/litellm"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/SAP/astonish/pkg/tools"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
)

func TestBotRealStockTitleLiveHTTPDiagnostic(t *testing.T) {
	if os.Getenv("ASTONISH_REAL_STOCK_TITLE_LIVE_HTTP") != "1" {
		t.Skip("explicit paid live diagnostic opt-in required")
	}
	// Once opted in, absent/invalid config is a failure, never a strict-driver skip.
	c, err := readStockAgentConfig(os.Getenv("LOCAL_BOT_AGENT_CONFIG"))
	if err != nil {
		t.Fatal(err)
	}
	runStockTitleLiveHTTPDiagnostic(t, &c)
}

// The historical substitute-provider experiment remains separate and is never
// evidence of same-provider parity.
func TestBotRealStockTitleLegacyLiveHTTPDiagnostic(t *testing.T) {
	if os.Getenv("ASTONISH_REAL_STOCK_TITLE_LEGACY_LIVE_HTTP") != "1" {
		t.Skip("explicit legacy paid diagnostic opt-in required")
	}
	runStockTitleLiveHTTPDiagnostic(t, nil)
}

func runStockTitleLiveHTTPDiagnostic(t *testing.T, sameProvider *stockAgentConfig) {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		URL   string `json:"provider_url"`
		Key   string `json:"provider_api_key"`
		Model string `json:"model"`
	}
	instance := "litellm"
	if sameProvider != nil {
		instance, cfg.Model = sameProvider.Instance, sameProvider.Model
	} else {
		raw, e := os.ReadFile(filepath.Join(root, ".tmp", "employee-native-config.json"))
		if e != nil || json.Unmarshal(raw, &cfg) != nil || cfg.URL == "" || cfg.Key == "" || cfg.Model == "" {
			t.Fatal("legacy private provider configuration unavailable")
		}
		if cfg.URL == "http://host.docker.internal:4000" {
			cfg.URL = "http://127.0.0.1:4000"
		}
	}
	dir, err := os.MkdirTemp(filepath.Join(root, ".tmp"), "real-stock-title-live-")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, ".tmp", "real-stock-title-live-http-latest"), []byte(dir), 0600); err != nil {
		t.Fatal(err)
	}
	rec := &realStockHTTPRecorder{dir: dir}
	if sameProvider != nil {
		rec.redact = []string{sameProvider.Provider["client_id"], sameProvider.Provider["client_secret"], sameProvider.Provider["auth_url"], sameProvider.Provider["base_url"]}
	}
	if sameProvider == nil {
		rec.inner = litellm.NewProvider(cfg.Key, cfg.URL, cfg.Model)
	}
	save := func(name string, v any) {
		t.Helper()
		if e := rec.save(name, v); e != nil {
			t.Fatal(e)
		}
	}
	parity := "legacy substitute adapter; no same-provider parity"
	if sameProvider != nil {
		parity = "factory identity checked at worker resolution; native daemon and live inference wire not verified"
	}
	save("metadata.json", map[string]any{"provider": instance, "model": cfg.Model, "live_provider_parity": parity, "title": "Stock Market News", "source_mode": "live native public network; producer-selected URLs", "infrastructure": "temporary SQLite; test Home and node transport; production worker/factory", "max_model_calls": 24, "max_actual_tokens": 500000})
	t.Logf("private artifacts: %s; provider=%s model=%s", dir, instance, cfg.Model)
	f := newInteractiveHTTPFixture(t, "web_fetch", nil, []string{"chat", "tool:execute"}, store.BotScopePersonal)
	f.worker.Stop()
	const body = `{"name":"Stock Market News","idempotency_key":"exact-title-live-once","start_from_title":true}`
	save("create-request.json", json.RawMessage(body))
	admitted := f.request("POST", "/api/studio/bots", body, 201)
	save("admission.json", json.RawMessage(admitted))
	var receipt titleHTTPReceipt
	if err = json.Unmarshal(admitted, &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.InitialTurn == nil || !receipt.InitialTurn.Run.IsInteractiveChat() {
		t.Fatal("no title-start interactive run")
	}
	input, err := f.repo.(store.BotRuntimeStore).LoadRunInput(f.ctx, receipt.ID, receipt.InitialTurn.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	save("initial-input.json", input)
	if input.Instructions != "Stock Market News" || !strings.Contains(input.Version.Charter, "Stock Market News") || len(input.Version.Autonomy.AllowedTools) != 0 {
		t.Fatal("title defaults changed")
	}
	f.b = receipt.Bot
	f.path = "/api/studio/bots/" + receipt.ID.String()
	p, _ := execution.PrincipalFromContext(f.ctx)
	internal, err := execution.WithPrincipal(context.Background(), execution.Principal{Kind: execution.PrincipalKindService, ClientID: "registry", Authentication: execution.AuthMethod("internal"), Surface: execution.SurfaceCode, OrgSlug: p.OrgSlug, TeamSlug: p.TeamSlug, Authenticated: true, Scopes: []string{entstore.BotHomeRegistrationScope}})
	if err != nil {
		t.Fatal(err)
	}
	if err = entstore.RegisterBotHomeBinding(internal, f.repo, entstore.BotHomeRegistration{BotID: f.b.ID, ExpectedRevision: f.b.Revision, Home: store.BotHomeBinding{ID: uuid.New(), BotID: f.b.ID, Scope: f.scope, Backend: "controlled", StorageKey: "journey-home", Revision: 1}}, func(context.Context, entstore.BotHomeRegistration) error { return nil }); err != nil {
		t.Fatal(err)
	}
	pinRaw := f.request("PATCH", "/api/studio/sessions/"+f.b.HomeSessionID.String()+"/model", fmt.Sprintf(`{"provider":%q,"model":%q}`, instance, cfg.Model), 200)
	save("home-model-pin.json", json.RawMessage(pinRaw))
	var pin struct {
		Provider string `json:"pinnedProvider"`
		Model    string `json:"pinnedModel"`
	}
	if json.Unmarshal(pinRaw, &pin) != nil || pin.Provider != instance || pin.Model != cfg.Model {
		t.Fatal("Home pin differs from intended identity")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 9*time.Minute)
	defer cancel()
	sourceCalls := 0
	f.pool.callOverride = func(name string, args map[string]interface{}) (json.RawMessage, error) {
		sourceCalls++
		n := sourceCalls
		if err := rec.save(fmt.Sprintf("source-%02d-request.json", n), map[string]any{"tool": name, "args": args, "at": time.Now()}); err != nil {
			return nil, err
		}
		b, e := json.Marshal(args)
		if e != nil {
			return nil, e
		}
		var result any
		// Ordinary WebFetch retains its own SSRF checks. HTTP uses the production
		// public-read policy to deny credentials, bodies, private IPs and writes.
		tc := mandateHTTPToolContext{base: ctx, session: f.b.HomeSessionID.String()}
		switch name {
		case "web_fetch":
			var a tools.WebFetchArgs
			e = json.Unmarshal(b, &a)
			if e == nil {
				result, e = tools.WebFetch(tc, a)
			}
		case "http_request":
			var a tools.HttpRequestArgs
			e = json.Unmarshal(b, &a)
			if e == nil {
				tc.base = tools.WithRoutineReadPolicy(ctx, launcher.BotRoutineReadPolicy(&store.Services{}))
				result, e = tools.HttpRequest(tc, a)
			}
		default:
			e = fmt.Errorf("isolated diagnostic transport does not implement %s", name)
		}
		if saveErr := rec.save(fmt.Sprintf("source-%02d-response.json", n), map[string]any{"result": result, "error": fmt.Sprint(e), "at": time.Now()}); saveErr != nil {
			return nil, saveErr
		}
		if e != nil {
			return nil, e
		}
		return json.Marshal(result)
	}
	f.worker, err = daemon.NewBotWorker(daemon.BotWorkerConfig{Backend: f.db, Sandbox: journeyHomeBackend{}, Pool: f.pool, TurnRegistry: f.registry, WorkerID: uuid.NewString(), ContextWindow: 200000, ModelOptions: provider.BudgetLLMOptions{MaxOutputTokens: 8192, SupportsOutputLimit: true}, ResolveModel: func(c context.Context, owner execution.Principal, scope store.BotScope, instanceName, modelName string) (model.LLM, error) {
		if instanceName != instance || modelName != cfg.Model || scope != f.scope || owner.Subject != p.Subject {
			return nil, fmt.Errorf("worker scoped model identity mismatch")
		}
		if sameProvider != nil {
			llm, resolveErr := sameProvider.resolveScoped(c, p, f.scope, owner, scope, instanceName, modelName)
			if resolveErr != nil {
				return nil, resolveErr
			}
			rec.inner = llm
		}
		identity := map[string]any{"provider": instanceName, "model": modelName, "adapter": fmt.Sprintf("%T", rec.inner), "owner_scope_verified": true}
		if sameProvider != nil {
			observation, observationErr := sameProvider.observeAdapter(rec.inner)
			if observationErr != nil {
				return nil, observationErr
			}
			identity["adapter_observation"] = observation
		}
		if err := rec.save("worker-model-identity.json", identity); err != nil {
			return nil, err
		}
		return rec, nil
	}, Prepare: func(c context.Context, q bot.RunRequest, s *launcher.BotChatScope) (*launcher.BotChatSafeResources, error) {
		if q.Instructions != "Stock Market News" {
			return nil, fmt.Errorf("assignment changed")
		}
		return &launcher.BotChatSafeResources{WorkspaceDir: "/isolated", ChatConfig: &config.AppConfig{}, Catalog: &launcher.ChatCatalogResources{Pool: f.pool}, VerifyIsolation: func(c context.Context) error { return c.Err() }, Cleanup: func() {}}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer f.worker.Stop()
	historyPath := f.path + "/sessions/" + f.b.HomeSessionID.String() + "/stream"
	reviews := 0
	rec.beforeReview = func(req *model.LLMRequest) error {
		reviews++
		if len(req.Tools) > 0 || (req.Config != nil && len(req.Config.Tools) > 0) {
			return fmt.Errorf("reviewer unexpectedly has tools")
		}
		return rec.save(fmt.Sprintf("home-before-review-%02d.json", reviews), string(f.request("GET", historyPath, "", 200)))
	}
	started := time.Now()
	if sameProvider == nil {
		pinLegacyHTTPAttempt(t, f, receipt.InitialTurn.Run)
	}
	pollErr := f.worker.Poll(ctx) // exactly once: engine recovery only, never resubmit
	runRaw := f.request("GET", f.path+"/runs/"+receipt.InitialTurn.Run.ID.String(), "", 200)
	save("run.json", json.RawMessage(runRaw))
	var run store.BotRun
	if err = json.Unmarshal(runRaw, &run); err != nil {
		t.Fatal(err)
	}
	home := f.request("GET", historyPath, "", 200)
	save("home.json", string(home))
	save("execution.json", map[string]any{"elapsed_seconds": time.Since(started).Seconds(), "calls": rec.calls, "review_calls": rec.reviews, "tokens": rec.tokens, "source_calls": sourceCalls, "poll_error": fmt.Sprint(pollErr), "status": run.Status, "report_occurrences": strings.Count(string(home), "bot-report:"+run.ID.String())})
	if sameProvider != nil && pollErr != nil {
		pollErr = fmt.Errorf("same-provider diagnostic failed (details withheld)")
	}
	t.Logf("DIAGNOSTIC status=%s calls=%d reviews=%d source_calls=%d elapsed=%s poll_error=%v", run.Status, rec.calls, rec.reviews, sourceCalls, time.Since(started).Round(time.Second), pollErr)
	if sameProvider == nil && run.Report.Quality != nil {
		t.Logf("quality=%s limitations=%v", run.Report.Quality.Outcome, run.Report.Quality.Limitations)
	}
	if pollErr != nil {
		t.Error(pollErr)
	}
	if rec.calls == 0 {
		t.Error("no real model call")
	}
	// A diagnostic transport pass is not acceptance: fail after preserving all
	// evidence unless the exact title-only task produced an accepted Home report.
	if run.Status != store.RunSucceeded || run.Report.Summary == "" {
		t.Fatalf("title-only live task did not complete: status=%s; diagnostics=%s", run.Status, dir)
	}
	if sameProvider == nil && (run.Report.Quality == nil || run.Report.Quality.Outcome != store.BotWorkAccepted) {
		t.Fatal("legacy diagnostic lacks accepted review")
	}
	if sameProvider != nil && rec.reviews != 0 {
		t.Fatal("agent-led diagnostic invoked mandatory legacy review")
	}
	quoted, _ := json.Marshal(run.Report.Summary)
	if strings.Count(string(home), "bot-report:"+run.ID.String()) != 1 || !strings.Contains(string(home), string(quoted)) {
		t.Fatal("accepted report missing or duplicated in Home")
	}
}

// inspectTitleLiveReviewer only reads prior evidence; it cannot dispatch a model.
func inspectTitleLiveReviewer(t *testing.T, dir, name string) {
	raw, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Response *model.LLMResponse `json:"response"`
	}
	if json.Unmarshal(raw, &response) != nil || response.Response == nil || response.Response.Partial || response.Response.Content == nil {
		return
	}
	text := ""
	for _, p := range response.Response.Content.Parts {
		if p != nil {
			text += p.Text
		}
	}
	var review struct {
		Assessments []struct {
			Item    string `json:"item_id"`
			Sources []struct {
				Ref   string `json:"ref"`
				Quote string `json:"quote"`
			} `json:"sources"`
		} `json:"content_assessments"`
	}
	if err = json.Unmarshal([]byte(text), &review); err != nil {
		t.Logf("raw reviewer %s invalid JSON: %v", name, err)
		return
	}
	requestName := strings.Split(name, "-response-")[0] + "-request.json"
	raw, err = os.ReadFile(filepath.Join(dir, requestName))
	if err != nil {
		t.Fatal(err)
	}
	var request model.LLMRequest
	if err = json.Unmarshal(raw, &request); err != nil {
		t.Fatal(err)
	}
	var input struct {
		Sources map[string]string `json:"content_sources"`
		Origins map[string]any    `json:"content_source_origins"`
	}
	if len(request.Contents) == 0 || len(request.Contents[0].Parts) == 0 {
		t.Fatal("missing reviewer input")
	}
	if err = json.Unmarshal([]byte(request.Contents[0].Parts[0].Text), &input); err != nil {
		t.Fatal(err)
	}
	mismatches := 0
	for i, a := range review.Assessments {
		for j, s := range a.Sources {
			if !strings.Contains(input.Sources[s.Ref], s.Quote) {
				mismatches++
				if mismatches <= 5 {
					t.Logf("RAW_MISMATCH response=%s assessment=%d source=%d item=%s ref=%s quote=%q", name, i, j, a.Item, s.Ref, s.Quote)
					for ref, body := range input.Sources {
						if strings.Contains(body, s.Quote) {
							t.Logf("quote exists in alternate captured ref=%s chosen_origin=%v actual_origin=%v", ref, input.Origins[s.Ref], input.Origins[ref])
						}
					}
				}
			}
		}
	}
	t.Logf("reviewer=%s assessments=%d exact_quote_mismatches=%d", name, len(review.Assessments), mismatches)
}

func TestBotRealStockTitleLiveHTTPEvidence(t *testing.T) {
	if os.Getenv("ASTONISH_STOCK_TITLE_EVIDENCE") != "1" {
		t.Skip("evidence inspection opt-in")
	}
	pointer, err := os.ReadFile("../../.tmp/real-stock-title-live-http-latest")
	if err != nil {
		t.Fatal(err)
	}
	dir := string(pointer)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	responses := 0
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0600 {
			t.Fatalf("non-private artifact %s", e.Name())
		}
		if strings.Contains(e.Name(), "-response-") {
			responses++
		}
		if strings.Contains(e.Name(), "-BOT_WORK_REVIEW_V1-response-") {
			inspectTitleLiveReviewer(t, dir, e.Name())
		}
	}
	var run store.BotRun
	raw, err := os.ReadFile(filepath.Join(dir, "run.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &run); err != nil {
		t.Fatal(err)
	}
	if run.ID == uuid.Nil || responses == 0 {
		t.Fatal("missing run or raw model outputs")
	}
	if _, err = os.Stat(filepath.Join(dir, "home.json")); err != nil {
		t.Fatal(err)
	}
	t.Logf("captured diagnostic, NOT an accepted-result assertion: status=%s raw_response_files=%d artifacts=%s", run.Status, responses, dir)
}
