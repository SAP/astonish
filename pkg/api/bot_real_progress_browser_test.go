package api_test

// Opt-in live proof: disposable HTTP/SQLite and actual built Studio + provider.
// Home/node transport is isolated; no native daemon or container parity claim.
import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/daemon"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/launcher"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/store"
	"github.com/go-rod/rod"
	browserlauncher "github.com/go-rod/rod/lib/launcher"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
	"gopkg.in/yaml.v3"
)

type liveProgressTitleStore struct {
	store.BotStore
	policy *execution.BotRuntimePolicy
}

func (s liveProgressTitleStore) CreateAndStart(ctx context.Context, q store.BotCreateRequest) (*store.BotTitleStartReceipt, error) {
	q.Version.Budget.RuntimePolicy = s.policy.Clone()
	return s.BotStore.(store.BotTitleStartStore).CreateAndStart(ctx, q)
}

// This test-only composition uses the production routine-read policy and route
// resolver. It adds the same public-research ceiling as the daemon composition;
// title admission, not this helper, derives and persists the subject/grant.
func liveProgressMandatePolicy(svc *store.Services, ceiling config.BotMandateConfig, runtime *execution.BotRuntimePolicy) store.BotMandatePolicyResolver {
	return func(ctx context.Context, ref store.BotMandateRef) (store.BotMandatePolicy, error) {
		p, err := launcher.NewBotRoutineReadMandatePolicy(svc, ceiling)(ctx, ref)
		if err != nil {
			return p, err
		}
		route, err := launcher.BotEffectiveConfiguredSearchRoute(ctx, svc)
		if err != nil {
			return p, err
		}
		p.PublicResearch = &execution.BotPublicResearchScope{Version: 1, Route: route, Facets: []execution.BotResearchFacet{execution.BotResearchOverview, execution.BotResearchRecent, execution.BotResearchPrimary, execution.BotResearchContradictions}, MaxQueryBytes: 2048, MaxResults: 5, MaxCalls: 12}
		for i := range p.Constraints {
			if p.Constraints[i].Action == execution.CapabilityToolExecute {
				p.Constraints[i].Resources = append(p.Constraints[i].Resources, "ask_user", launcher.BotConfiguredSearchToolName)
			}
		}
		p.RuntimePolicy = runtime.Clone()
		return p, nil
	}
}

type liveProgressModel struct {
	inner                            model.LLM
	recorder                         *realStockHTTPRecorder
	evaluations, evidenceEvaluations atomic.Int64
	evidence                         func(*model.LLMRequest) bool
	checkRequest                     func(*model.LLMRequest)
}

func (m *liveProgressModel) Name() string { return m.inner.Name() }
func (m *liveProgressModel) GenerateContent(ctx context.Context, req *model.LLMRequest, streaming bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		if m.checkRequest != nil {
			m.checkRequest(req)
		}
		prompt := ""
		if req.Config != nil && req.Config.SystemInstruction != nil {
			for _, p := range req.Config.SystemInstruction.Parts {
				if p != nil {
					prompt += p.Text
				}
			}
		}
		if strings.HasPrefix(prompt, "BOT_EMPLOYEE_PROGRESS_V1") {
			n := m.evaluations.Add(1)
			if err := m.recorder.save(fmt.Sprintf("evaluator-%02d-request.json", n), req); err != nil {
				yield(nil, err)
				return
			}
			evidence := m.evidence
			if evidence == nil {
				evidence = liveProgressHasEvidence
			}
			if evidence(req) {
				m.evidenceEvaluations.Add(1)
			}
		}
		for response, err := range m.inner.GenerateContent(ctx, req, streaming) {
			if !yield(response, err) {
				return
			}
		}
	}
}
func liveProgressHasEvidence(req *model.LLMRequest) bool {
	if len(req.Contents) != 1 || len(req.Contents[0].Parts) != 1 {
		return false
	}
	var in struct {
		Observation bot.ProgressObservation `json:"observation"`
	}
	if json.Unmarshal([]byte(req.Contents[0].Parts[0].Text), &in) != nil || !strings.Contains(in.Observation.Assignment, "New AI Model Releases") {
		return false
	}
	for _, excerpt := range in.Observation.Context {
		if excerpt.Kind == bot.ProgressContextTool && len(excerpt.Text) > 200 && strings.Contains(excerpt.Text, "http") && (excerpt.Tool == "perplexity_web_search" || excerpt.Tool == "web_fetch") {
			return true
		}
	}
	return false
}

type liveProgressSearchModel struct {
	inner    model.LLM
	recorder *realStockHTTPRecorder
	calls    *atomic.Int64
}

func (m *liveProgressSearchModel) Name() string { return m.inner.Name() }
func (m *liveProgressSearchModel) GenerateContent(ctx context.Context, req *model.LLMRequest, streaming bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		n := m.calls.Add(1)
		if err := m.recorder.save(fmt.Sprintf("sonar-%02d-request.json", n), req); err != nil {
			yield(nil, err)
			return
		}
		i := 0
		for response, err := range m.inner.GenerateContent(ctx, req, streaming) {
			i++
			if response != nil && !response.Partial && response.UsageMetadata != nil {
				m.recorder.mu.Lock()
				m.recorder.tokens += int64(response.UsageMetadata.TotalTokenCount)
				m.recorder.mu.Unlock()
			}
			if e := m.recorder.save(fmt.Sprintf("sonar-%02d-response-%04d.json", n, i), map[string]any{"response": response, "failed": err != nil}); e != nil {
				yield(nil, e)
				return
			}
			if !yield(response, err) {
				return
			}
		}
	}
}

// The long worker wait uses a plain parent context, not Rod's timeout context.
// CancelTimeout on that page panics; attach the bounded final-delivery deadline
// directly to the still-live page, retaining the original browser/tab identity.
func liveProgressFinalPage(page *rod.Page, ctx context.Context) *rod.Page {
	return page.Context(ctx).Timeout(40 * time.Second)
}

// Exact prose, section order, ordered links and diagram placement are compared
// separately from Mermaid's presentation. Each expected diagram is independently
// rendered from the persisted source; SVG labels and structural/edge semantics
// must match. Generated IDs, CSS and viewport geometry are not report content.
const liveProgressExactReport = `(html) => {
 const expected = new DOMParser().parseFromString(html, 'text/html').body;
 const norm = s => s.replace(/\s+/gu, '');
 const links = e => Array.from(e.querySelectorAll('a[href^="http"]'), a => a.getAttribute('href'));
 const semanticSVG = svg => {
  if (!svg) return null;
  const walk = e => {
   if (['style','defs','title','desc'].includes(e.localName)) return null;
   const attrs = ['class','role','aria-roledescription','data-node','data-edge','marker-start','marker-end'].map(k => {
    let v=e.getAttribute(k); if (k.startsWith('marker-') && v) v='present';
    return v ? [k,v] : null;
   }).filter(Boolean);
   const children = [...e.childNodes].map(n=>n.nodeType===3 ? norm(n.textContent) : n.nodeType===1 ? walk(n) : null).filter(v=>v!==null && v!=='');
   return [e.localName,attrs,children];
  };
  return walk(svg);
 };
 const sources = [...expected.querySelectorAll('pre > code.language-mermaid')];
 const prepared = window.__proofExpectedDiagrams || [];
 if (sources.length !== prepared.length || sources.some((c,i)=>c.textContent.trim()!==prepared[i].source)) return {count:0, error:'unprepared diagram source'};
 const wantDiagrams = prepared.map(d=>semanticSVG(new DOMParser().parseFromString(d.svg,'text/html').querySelector('svg')));
 sources.forEach((code,i)=>code.parentElement.replaceWith(document.createTextNode('DIAGRAM_BOUNDARY_'+i)));
 const want = norm(expected.textContent);
 const candidates = Array.from(document.querySelectorAll('[aria-label="Bot workspace"] .markdown-body'))
   .filter(e => !e.querySelector('.markdown-body') && e.getClientRects().length);
 const matches = candidates.filter(e => {
  const diagrams=[...e.querySelectorAll('.mermaid-diagram')];
  if (e.querySelector('.mermaid-error') || diagrams.length!==wantDiagrams.length) return false;
  if (diagrams.some((d,i)=>d.querySelectorAll('svg').length!==1 || JSON.stringify(semanticSVG(d.querySelector('svg')))!==JSON.stringify(wantDiagrams[i]))) return false;
  const copy=e.cloneNode(true);
  [...copy.querySelectorAll('.mermaid-diagram')].forEach((d,i)=>d.replaceWith(document.createTextNode('DIAGRAM_BOUNDARY_'+i)));
  return want.length>0 && norm(copy.textContent)===want && JSON.stringify(links(copy))===JSON.stringify(links(expected));
 });
 return {count:matches.length, text:matches[0]?.innerText || '', links:matches[0]?links(matches[0]):[], diagrams:matches[0]?prepared.map((d,i)=>({source:d.source,semantics:wantDiagrams[i]})):[]};
}`

func liveProgressReportHTML(t *testing.T, summary string) string {
	t.Helper()
	var rendered bytes.Buffer
	if err := goldmark.New(goldmark.WithExtensions(extension.GFM)).Convert([]byte(summary), &rendered); err != nil {
		t.Fatal(err)
	}
	return rendered.String()
}

func TestLiveProgressBrowserTimeoutTransition(t *testing.T) {
	if os.Getenv("ASTONISH_BROWSER_CONTEXT_TEST") != "1" {
		t.Skip("explicit local browser regression opt-in required; no provider calls")
	}
	launch := browserlauncher.New().Bin(os.Getenv("ASTONISH_BOT_BROWSER_PATH")).UserDataDir(t.TempDir()).Headless(true).Leakless(false)
	control, err := launch.Launch()
	if err != nil {
		t.Fatal(err)
	}
	defer launch.Cleanup()
	browser := rod.New().ControlURL(control).MustConnect()
	defer browser.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	page := browser.MustPage("about:blank").Timeout(time.Second)
	page = page.CancelTimeout().Context(ctx)
	page.MustEval(`() => { window.originalTab = 'retained'; document.body.textContent = 'terminal answer'; }`)
	final := liveProgressFinalPage(page, ctx)
	defer final.CancelTimeout()
	if final.TargetID != page.TargetID || final.MustEval(`() => window.originalTab`).Str() != "retained" {
		t.Fatal("final delivery replaced the original page")
	}
	final.MustWait(`() => document.body.innerText === 'terminal answer'`)
	expectedHTML := liveProgressReportHTML(t, "## Releases\n\n**Model A** launched. [Source](https://example.org/a)\n\nModel B available.")
	final.MustEval(`html => { document.body.innerHTML = '<section aria-label="Bot workspace"><div class="markdown-body"><div class="markdown-body">' + html + '</div></div></section>'; }`, expectedHTML)
	if final.MustEval(liveProgressExactReport, expectedHTML).Get("count").Int() != 1 {
		t.Fatal("complete report rejected")
	}
	final.MustEval(`() => { const leaf = document.querySelector('.markdown-body .markdown-body'); leaf.parentElement.parentElement.append(leaf.parentElement.cloneNode(true)); }`)
	if final.MustEval(liveProgressExactReport, expectedHTML).Get("count").Int() != 2 {
		t.Fatal("two actual report messages were not detected")
	}
	final.MustEval(`() => document.querySelector('[aria-label="Bot workspace"]').lastElementChild.remove()`)
	final.MustEval(`() => document.querySelector('.markdown-body p:last-child').remove()`)
	if final.MustEval(liveProgressExactReport, expectedHTML).Get("count").Int() != 0 {
		t.Fatal("partial report with source link accepted")
	}
	final.MustEval(`html => document.querySelector('.markdown-body').innerHTML = html + html`, expectedHTML)
	if final.MustEval(liveProgressExactReport, expectedHTML).Get("count").Int() != 0 {
		t.Fatal("duplicated report accepted")
	}
	final.MustEval(`html => { const e = document.querySelector('.markdown-body'); e.innerHTML = html; e.querySelector('a').href = 'https://example.org/wrong'; }`, expectedHTML)
	if final.MustEval(liveProgressExactReport, expectedHTML).Get("count").Int() != 0 {
		t.Fatal("wrong source accepted")
	}
	if dir := os.Getenv("ASTONISH_PROGRESS_CAPTURE_DIR"); dir != "" {
		// Offline selector regression only: the prior diagnostic retained text,
		// not HTML. Never write acceptance evidence from this reconstruction.
		var run store.BotRun
		raw, err := os.ReadFile(filepath.Join(dir, "run.json"))
		if err != nil || json.Unmarshal(raw, &run) != nil || run.Report.Summary == "" {
			t.Fatal("captured report unavailable")
		}
		var diagnostic struct {
			DOM struct {
				Value string `json:"value"`
			} `json:"dom"`
		}
		raw, err = os.ReadFile(filepath.Join(dir, "browser-failure.json"))
		if err != nil || json.Unmarshal(raw, &diagnostic) != nil {
			t.Fatal("captured original-tab diagnostic unavailable")
		}
		html := liveProgressReportHTML(t, run.Report.Summary)
		final.MustEval(`html => { document.body.innerHTML = '<section aria-label="Bot workspace"><div class="markdown-body"><div class="markdown-body">' + html + '</div></div></section>'; }`, html)
		if final.MustEval(liveProgressExactReport, html).Get("count").Int() != 1 {
			t.Fatal("complete captured report rejected by corrected selector")
		}
		if !final.MustEval(`(html, retained) => { const norm = s => s.replace(/\s+/gu, ''); const want = norm(new DOMParser().parseFromString(html, 'text/html').body.textContent); return want.length > 0 && norm(retained).split(want).length === 2; }`, html, diagnostic.DOM.Value).Bool() {
			t.Fatal("captured original-tab text does not contain complete persisted report exactly once")
		}
		t.Log("offline captured-report selector and retained original-tab text agree; not live/reload acceptance")
	}
	deadline, ok := final.GetContext().Deadline()
	if !ok || time.Until(deadline) > 40*time.Second || time.Until(deadline) <= 0 {
		t.Fatal("final delivery must have a fresh bounded deadline")
	}
	cancel()
	if final.GetContext().Err() != context.Canceled {
		t.Fatal("final delivery escaped the overall run budget")
	}
}

func TestLiveProgressEvidenceRequiresAssignmentAndSource(t *testing.T) {
	observation := bot.ProgressObservation{Assignment: "New AI Model Releases", Context: []bot.ProgressContextExcerpt{{Kind: bot.ProgressContextTool, Tool: "perplexity_web_search", Text: "https://example.org/releases " + strings.Repeat("actual source excerpt ", 15)}}}
	request := func(o bot.ProgressObservation) *model.LLMRequest {
		raw, _ := json.Marshal(map[string]any{"observation": o})
		return &model.LLMRequest{Contents: []*genai.Content{genai.NewContentFromText(string(raw), "user")}}
	}
	if !liveProgressHasEvidence(request(observation)) {
		t.Fatal("substantive source and assignment rejected")
	}
	missing := observation
	missing.Assignment = "AI News"
	if liveProgressHasEvidence(request(missing)) {
		t.Fatal("lost confirmation accepted")
	}
	missing = observation
	missing.Context = []bot.ProgressContextExcerpt{{Kind: bot.ProgressContextTool, Tool: "web_fetch", Text: "receipt-id"}}
	if liveProgressHasEvidence(request(missing)) {
		t.Fatal("receipt-only evidence accepted")
	}
}

func realProgressBrowserConfig(t *testing.T) *config.AppConfig {
	t.Helper()
	// Historical private fixture credentials are reused, but not its old model pin.
	c, err := readStockAgentConfig(os.Getenv("LOCAL_BOT_AGENT_CONFIG"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(os.Getenv("LIVE_SEARCH_CONFIG"))
	if err != nil {
		t.Fatal("explicit search configuration unavailable")
	}
	var cfg config.AppConfig
	if yaml.Unmarshal(raw, &cfg) != nil {
		t.Fatal("search configuration invalid")
	}
	if cfg.PerplexityWebSearch.Provider == "" || !strings.Contains(cfg.PerplexityWebSearch.Model, "sonar") {
		t.Fatal("configured sonar search required")
	}
	if cfg.Providers == nil {
		cfg.Providers = map[string]config.ProviderConfig{}
	}
	cfg.Providers[c.Instance] = c.Provider
	cfg.General.WebSearchTool = "perplexity_web_search"
	t.Logf("identity provider=%s model=gpt-5.6-terra search_provider=%s search_model=%s", c.Instance, cfg.PerplexityWebSearch.Provider, cfg.PerplexityWebSearch.Model)
	return &cfg
}
func TestBotRealProgressBrowserPreflight(t *testing.T) {
	if os.Getenv("LOCAL_BOT_AGENT_CONFIG") == "" {
		t.Skip("explicit private configuration required")
	}
	_ = realProgressBrowserConfig(t)
}
func TestBotRealProgressBrowserAcceptance(t *testing.T) {
	if os.Getenv("ASTONISH_REAL_PROGRESS_BROWSER") != "1" {
		t.Skip("explicit paid browser acceptance opt-in required")
	}
	runLiveProgressBrowser(t, liveProgressScenario{title: "AI News", answer: "New AI Model Releases", prefix: "real-progress-browser", evaluationInterval: 10, evidence: liveProgressHasEvidence})
}

// Scenario callbacks assess real outputs; they never inject model responses.
type liveProgressScenario struct {
	title, answer, prefix string
	evaluationInterval    int
	evidence              func(*model.LLMRequest) bool
	checkRequest          func(*model.LLMRequest)
	assess                func(*testing.T, *store.BotRun, string)
}

func runLiveProgressBrowser(t *testing.T, scenario liveProgressScenario) {
	t.Helper()
	cfg := realProgressBrowserConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 9*time.Minute)
	defer cancel()
	dir, err := os.MkdirTemp("../../.tmp", scenario.prefix+"-")
	if err != nil {
		t.Fatal(err)
	}
	dir, _ = filepath.Abs(dir)
	_ = os.Chmod(dir, 0700)
	if err = os.WriteFile("../../.tmp/"+scenario.prefix+"-latest", []byte(dir), 0600); err != nil {
		t.Fatal(err)
	}
	rec := &realStockHTTPRecorder{dir: dir, redact: []string{"nonempty-redaction-enabled"}}
	for _, p := range cfg.Providers {
		for _, k := range []string{"client_id", "client_secret", "auth_url", "base_url", "api_key"} {
			rec.redact = append(rec.redact, p[k])
		}
	}
	save := func(name string, v any) {
		t.Helper()
		if e := rec.save(name, v); e != nil {
			t.Fatal(e)
		}
	}
	f := newInteractiveHTTPFixture(t, "", nil, []string{"chat", "tool:execute"}, store.BotScopePersonal)
	f.worker.Stop()
	if err != nil {
		t.Fatal(err)
	}
	principal, _ := execution.PrincipalFromContext(f.ctx)
	principal.Authentication = execution.AuthMethodPlatformJWT
	f.ctx, err = execution.WithPrincipal(f.ctx, principal)
	if err != nil {
		t.Fatal(err)
	}
	f.ctx, err = execution.WithAuthenticationEvidence(f.ctx, execution.AuthenticationEvidence{Method: principal.Authentication, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	policy := execution.NewBotProgressRuntimePolicy()
	policy.EvaluationIntervalSeconds = int64(scenario.evaluationInterval)
	org, _ := f.db.ForOrg("interactive")
	personal := org.ForUser(f.b.OwnerID.String())
	team := org.ForTeam("team")
	if err := f.db.PlatformSettings().Save(f.ctx, &store.PlatformSettings{PerplexityWebSearch: &store.PerplexityWebSearchSettings{Provider: cfg.PerplexityWebSearch.Provider, Model: cfg.PerplexityWebSearch.Model}}); err != nil {
		t.Fatal(err)
	}
	providers := map[string]store.ProviderConfig{}
	for name, p := range cfg.Providers {
		providers[name] = store.ProviderConfig(p)
	}
	if err := team.Settings().Save(f.ctx, &store.TeamSettings{WebSearchTool: "perplexity:perplexity_web_search", Providers: providers}); err != nil {
		t.Fatal(err)
	}
	svc := &store.Services{TenantRouter: f.db, Bots: team.Bots(), Sessions: team.Sessions(), PersonalBots: personal.Bots(), PersonalSessions: personal.Sessions(), BotInteractiveReady: true, PlatformSettings: f.db.PlatformSettings(), OrgSettings: f.db.OrgSettings("interactive"), Settings: team.Settings(), NetworkPolicies: org.OrgNetworkPolicies(), TeamNetworkPolicies: team.NetworkPolicies()}
	ceiling := config.DefaultBotMandateConfig()
	ceiling.MaxTokens, ceiling.MaxRuntimeSeconds = 500000, 540
	mandatePolicy := liveProgressMandatePolicy(svc, ceiling, policy)
	svc.BotMandatePolicy = mandatePolicy
	*f.policy = mandatePolicy
	f.registry.Close() // The fresh title and continuation must use durable authority, never the interactive registry.
	target, _ := url.Parse(f.server.URL)
	proxy := httputil.NewSingleHostReverseProxy(target)
	routes := mux.NewRouter()
	root := "/api/studio/bots"
	for _, r := range []struct {
		path, method string
		handler      http.HandlerFunc
	}{
		{root + "/{botID}/approvals/{approvalID}/approve-and-continue", "POST", api.BotApproveContinueHandler},
		{root, "POST", api.BotCreateHandler}, {root, "GET", api.BotListHandler}, {root + "/{botID}/activity", "GET", api.BotActivityHandler}, {root + "/{botID}/read", "GET", api.BotReadCursorHandler}, {root + "/{botID}/read", "POST", api.BotMarkReadHandler}, {root + "/{botID}/versions/{version}", "GET", api.BotGetVersionHandler}, {root + "/{botID}/runs", "GET", api.BotRunsHandler}, {root + "/{botID}/approvals", "GET", api.BotApprovalsHandler}, {root + "/{botID}/proposals", "GET", api.BotControlProposalsHandler},
	} {
		routes.HandleFunc(r.path, r.handler).Methods(r.method)
	}
	routes.NotFoundHandler = proxy
	routes.MethodNotAllowedHandler = proxy
	var pkg struct {
		Version string `json:"version"`
	}
	raw, _ := os.ReadFile("../../web/package.json")
	_ = json.Unmarshal(raw, &pkg)
	dist, _ := filepath.Abs(filepath.Join("../../web/dist", pkg.Version))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/__proof_mermaid/") {
			liveProgressMermaidFiles().ServeHTTP(w, r)
			return
		}
		if r.URL.Path == root && r.Method == http.MethodPost {
			createSvc := *svc
			createSvc.PersonalBots = liveProgressTitleStore{BotStore: personal.Bots(), policy: policy}
			routes.ServeHTTP(w, r.WithContext(store.WithServices(f.ctx, &createSvc)))
			return
		}
		if strings.HasPrefix(r.URL.Path, root) || strings.HasPrefix(r.URL.Path, "/api/studio/sessions/") {
			routes.ServeHTTP(w, r.WithContext(store.WithServices(f.ctx, svc)))
			return
		}
		var v any
		switch r.URL.Path {
		case "/api/auth/setup-status":
			v = map[string]any{"initialized": true, "auth_mode": "builtin"}
		case "/api/auth/me":
			v = map[string]any{"user": map[string]any{"id": f.b.OwnerID.String(), "email": "live@example.invalid", "display_name": "Live acceptance", "role": "owner", "platform_role": "superadmin"}, "org": map[string]any{"slug": "interactive", "name": "Interactive"}, "team": "team"}
		case "/api/orgs":
			v = map[string]any{"orgs": []any{}, "active_org": "interactive"}
		case "/api/teams":
			v = []any{map[string]any{"slug": "team", "name": "Team"}}
		case "/api/settings/status":
			v = map[string]any{"setupRequired": false}
		case "/api/version":
			v = map[string]any{"version": "dev"}
		case "/api/agents", "/api/tools":
			v = []any{}
		case "/api/settings/config":
			v = map[string]any{}
		case "/api/settings/providers/effective":
			v = map[string]any{"providers": map[string]any{}}
		}
		if v != nil {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(v)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(w, r)
			return
		}
		path := filepath.Join(dist, filepath.Clean("/"+r.URL.Path))
		if st, e := os.Stat(path); e == nil && !st.IsDir() {
			http.FileServer(http.Dir(dist)).ServeHTTP(w, r)
			return
		}
		http.ServeFile(w, r, filepath.Join(dist, "index.html"))
	}))
	defer server.Close()
	launch := browserlauncher.New().Bin(os.Getenv("ASTONISH_BOT_BROWSER_PATH")).UserDataDir(t.TempDir()).Headless(true).Leakless(false)
	control, err := launch.Launch()
	if err != nil {
		t.Fatal(err)
	}
	defer launch.Cleanup()
	browser := rod.New().ControlURL(control).MustConnect()
	defer browser.Close()
	page := browser.MustPage(server.URL + "/#/chat").Timeout(45 * time.Second)
	page = page.WithPanic(func(v any) {
		rescue := page.Context(context.Background()).Timeout(8 * time.Second)
		text, _ := rescue.Eval(`()=>document.body.innerText`)
		if png, e := rescue.Screenshot(false, nil); e == nil {
			_ = os.WriteFile(filepath.Join(dir, "browser-failure.png"), png, 0600)
		}
		save("browser-failure.json", map[string]any{"failure": fmt.Sprint(v), "dom": text})
		t.Logf("original browser retained for bounded inspection: %s; CDP %s; artifacts=%s", server.URL, control, dir)
		if os.Getenv("ASTONISH_BROWSER_FAILURE_HOLD") == "1" {
			time.Sleep(2 * time.Minute)
		}
		t.Fatalf("browser failure; artifacts=%s", dir)
	})
	started := time.Now()
	page.MustElementR("button", "^New Bot$").MustClick()
	page.MustElement(`[aria-label="Bot name"]`).MustInput(scenario.title)
	page.MustElementR("button", "^Start conversation$").MustClick()
	page.MustElement(`[aria-label="Bot workspace"]`).MustVisible()
	id := strings.Split(page.MustEval(`()=>location.hash`).Str(), "/")[4]
	if err = json.Unmarshal(f.request("GET", root+"/"+id, "", 200), &f.b); err != nil {
		t.Fatal(err)
	}
	f.path = root + "/" + id
	f.request("PATCH", "/api/studio/sessions/"+f.b.HomeSessionID.String()+"/model", `{"provider":"New AI Core Provider","model":"gpt-5.6-terra"}`, 200)
	runs, err := f.repo.ListRuns(f.ctx, f.b.ID, store.BotPage{})
	if err != nil || len(runs) != 1 {
		t.Fatal("fresh title run missing")
	}
	runID := runs[0].ID
	if runs[0].MandateRef == nil {
		t.Fatal("fresh title lacks durable mandate")
	}
	if os.Getenv("ASTONISH_REAL_PROGRESS_ADMISSION_ONLY") == "1" {
		t.Log("fresh browser title admission has durable mandate; no provider calls")
		return
	}
	var sonarCalls atomic.Int64
	observed := &liveProgressModel{inner: rec, recorder: rec, evidence: scenario.evidence, checkRequest: scenario.checkRequest}
	f.pool.callOverride = func(name string, args map[string]interface{}) (json.RawMessage, error) {
		return nil, fmt.Errorf("unmandated native tool transport forbidden: %s", name)
	}
	expected, _ := execution.PrincipalFromContext(f.ctx)
	f.worker, err = daemon.NewBotWorker(daemon.BotWorkerConfig{Backend: f.db, Sandbox: journeyHomeBackend{}, Pool: f.pool, MandatePolicy: mandatePolicy, WorkerID: uuid.NewString(), ResolveModel: func(c context.Context, owner execution.Principal, scope store.BotScope, instance, name string) (model.LLM, error) {
		if owner.Subject != expected.Subject || scope != f.scope || instance != stockAgentProvider || name != "gpt-5.6-terra" {
			return nil, fmt.Errorf("model identity mismatch")
		}
		window := provider.ResolveContextWindow(c, instance, name, cfg)
		capacity := provider.ResolveModelOutputCapacity(c, instance, name, cfg)
		if rec.inner == nil {
			llm, e := provider.GetProvider(c, instance, name, cfg)
			if e != nil {
				return nil, fmt.Errorf("provider initialization failed (details withheld)")
			}
			rec.inner = llm
			output := provider.BudgetOutputTokens(capacity, 0)
			if e := rec.save("generation-settings.json", map[string]any{"context_window": window, "output_capacity": capacity, "max_output_tokens": output, "numeric_wrapper": true, "explicit_worker_override": false}); e != nil {
				return nil, e
			}
			if window != 1050000 || output != 32768 {
				return nil, fmt.Errorf("unexpected live model-aware generation limits: %d/%d", window, output)
			}
			_ = rec.save("identity.json", map[string]any{"provider": instance, "model": name, "adapter": fmt.Sprintf("%T", llm), "adapter_model": llm.Name()})
		}
		return daemon.NewBotGenerationModel(observed, window, capacity), nil
	}, Prepare: func(c context.Context, q bot.RunRequest, s *launcher.BotChatScope) (*launcher.BotChatSafeResources, error) {
		s.Services.PlatformSettings, s.Services.OrgSettings, s.Services.Settings = svc.PlatformSettings, svc.OrgSettings, svc.Settings
		s.Services.NetworkPolicies, s.Services.TeamNetworkPolicies = svc.NetworkPolicies, svc.TeamNetworkPolicies
		reads, err := launcher.BotMandatedReadTools(q.Mandate, launcher.BotRoutineReadPolicy(s.Services))
		if err != nil {
			return nil, err
		}
		workflow, err := launcher.BotWorkflowTools(q, s)
		if err != nil {
			return nil, err
		}
		for _, candidate := range workflow {
			if candidate.Name() == "ask_user" {
				reads = append(reads, candidate)
			}
		}
		episode, err := launcher.NewBotResearchEpisode(q.Run.ID, q.Run.SessionID.String(), launcher.BotResearchOpenWeb, nil)
		if err != nil {
			return nil, err
		}
		if q.Mandate.Authorization.Snapshot().PublicResearch != nil {
			search, err := launcher.BotConfiguredResearchTool(c, q, s, func(c context.Context, p, m string, scoped *config.AppConfig) (model.LLM, error) {
				if !provider.IsConfinedProviderSecrets(c) || p != cfg.PerplexityWebSearch.Provider || m != cfg.PerplexityWebSearch.Model {
					return nil, fmt.Errorf("wrong search identity")
				}
				llm, err := provider.GetProvider(c, p, m, scoped)
				if err != nil {
					return nil, fmt.Errorf("search provider initialization failed")
				}
				return &liveProgressSearchModel{inner: llm, recorder: rec, calls: &sonarCalls}, nil
			}, nil, episode)
			if err != nil {
				return nil, err
			}
			reads = append(reads, search)
		}
		return &launcher.BotChatSafeResources{WorkspaceDir: "/isolated", ChatConfig: cfg, Tools: reads, Research: episode, PolicyRuntime: agent.BotPolicyRuntime{Mandate: q.Mandate, Guard: restartMandateGuard{q, f.repo}, Classifier: launcher.WithBotMandatedReadClassifier(launcher.BotCatalogueClassifier{}, reads)}, VerifyIsolation: func(c context.Context) error { return c.Err() }, Cleanup: func() {}}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer f.worker.Stop()
	pollErr := f.worker.Poll(ctx)
	run, err := f.repo.GetRun(f.ctx, f.b.ID, runID)
	save("confirmation.json", map[string]any{"run": run, "elapsed_seconds": time.Since(started).Seconds(), "poll_error": fmt.Sprint(pollErr), "load_error": fmt.Sprint(err)})
	if pollErr != nil || run == nil || run.Status != store.RunWaitingApproval {
		t.Fatalf("real title did not ask; artifacts=%s", dir)
	}
	page.MustElement(`textarea[aria-label="Custom answer"]`).MustVisible()
	save("question-dom.json", page.MustEval(`()=>document.body.innerText`).Str())
	page.MustElement(`textarea[aria-label="Custom answer"]`).MustInput(scenario.answer)
	page.MustElementR("button", "^Submit answer$").MustClick()
	page.MustElement(`article[aria-label="Answered question"]`).MustVisible()
	confirmed, err := f.repo.(store.BotRuntimeStore).LoadRunInput(f.ctx, f.b.ID, runID)
	if err != nil {
		t.Fatal(err)
	}
	save("confirmed-input.json", confirmed)
	if confirmed.Version.Budget.RuntimePolicy.EffectiveMode() != execution.BotRuntimeModeProgressV1 {
		t.Fatal("not progress_v1")
	}
	page = page.CancelTimeout().Context(ctx)
	poll := make(chan error, 1)
	go func() { poll <- f.worker.Poll(ctx) }()
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	samples := 0
wait:
	for {
		select {
		case pollErr = <-poll:
			break wait
		case <-ticker.C:
			samples++
			r, e := f.repo.GetRun(f.ctx, f.b.ID, runID)
			save(fmt.Sprintf("progress-%02d.json", samples), map[string]any{"run": r, "error": fmt.Sprint(e), "elapsed_seconds": time.Since(started).Seconds(), "dom": page.MustEval(`()=>document.body.innerText`).Str()})
			t.Logf("heartbeat elapsed=%s sonar_calls=%d", time.Since(started).Round(time.Second), sonarCalls.Load())
		case <-ctx.Done():
			pollErr = ctx.Err()
			break wait
		}
	}
	run, err = f.repo.GetRun(f.ctx, f.b.ID, runID)
	save("run.json", run)
	home := f.request("GET", f.path+"/sessions/"+f.b.HomeSessionID.String()+"/stream", "", 200)
	save("home.json", string(home))
	rec.mu.Lock()
	calls, tokens := rec.calls, rec.tokens
	rec.mu.Unlock()
	save("execution.json", map[string]any{"elapsed_seconds": time.Since(started).Seconds(), "calls": calls, "tokens": tokens, "sonar_calls": sonarCalls.Load(), "evaluations": observed.evaluations.Load(), "evidence_evaluations": observed.evidenceEvaluations.Load(), "poll_error": fmt.Sprint(pollErr), "load_error": fmt.Sprint(err), "status": run.Status, "report_occurrences": strings.Count(string(home), "bot-report:"+runID.String())})
	if pollErr != nil || err != nil || run.Status != store.RunSucceeded || run.Report.Summary == "" {
		t.Fatalf("no completed answer: status=%s; artifacts=%s", run.Status, dir)
	}
	if sonarCalls.Load() == 0 || observed.evidenceEvaluations.Load() == 0 || tokens > 500000 || strings.Count(string(home), "bot-report:"+runID.String()) != 1 {
		t.Errorf("missing strict search/progress/budget/publication evidence; artifacts=%s", dir)
	}
	approvals, err := f.repo.ListApprovals(f.ctx, f.b.ID, store.BotPage{})
	if err != nil || len(approvals) != 1 {
		t.Fatalf("redundant scope question: %d approvals, %v", len(approvals), err)
	}
	if scenario.assess != nil {
		scenario.assess(t, run, dir)
	}
	originalTab := page.TargetID
	page = liveProgressFinalPage(page, ctx)
	// Wait on the original page for the complete persisted report exactly once.
	expectedHTML := liveProgressReportHTML(t, run.Report.Summary)
	save("expected-diagrams.json", page.MustEval(liveProgressPrepareDiagrams, expectedHTML))
	visible := `(html) => { const w = document.querySelector('[aria-label="Bot workspace"]'); return w && w.querySelectorAll('a[href^="http"]').length >= 2 && !w.innerText.includes('Working…') && !w.querySelector('textarea[aria-label="Custom answer"]') && (` + liveProgressExactReport + `)(html).count === 1; }`
	page.MustWait(visible, expectedHTML)
	if page.TargetID != originalTab {
		t.Fatal("original tab replaced")
	}
	liveReport := page.MustEval(liveProgressExactReport, expectedHTML)
	save("final-live-report.json", liveReport)
	liveDOM := page.MustEval(`()=>document.querySelector('[aria-label="Bot workspace"]').innerText`).Str()
	save("final-live-dom.json", liveDOM)
	page.MustScreenshot(filepath.Join(dir, "final-live.png"))
	page.MustReload() // exactly one reload, only after live final delivery
	page.MustElement(`[aria-label="Bot workspace"]`).MustVisible()
	page.MustEval(liveProgressPrepareDiagrams, expectedHTML)
	page.MustWait(visible, expectedHTML)
	reloadReport := page.MustEval(liveProgressExactReport, expectedHTML)
	save("final-reload-report.json", reloadReport)
	if liveReport.JSON("", "") != reloadReport.JSON("", "") {
		t.Fatal("live/reload report mismatch")
	}
	reloadDOM := page.MustEval(`()=>document.querySelector('[aria-label="Bot workspace"]').innerText`).Str()
	save("final-reload-dom.json", reloadDOM)
	page.MustScreenshot(filepath.Join(dir, "final-reload.png"))
	if !strings.Contains(liveDOM, scenario.title) || !strings.Contains(reloadDOM, scenario.title) {
		t.Fatal("chosen scope missing from live/reload")
	}
	save("browser-accepted.json", map[string]any{"live_visible": true, "reload_visible": true, "exact_report_match": true, "report_occurrences_live": liveReport.Get("count").Int(), "report_occurrences_reload": reloadReport.Get("count").Int(), "approval_count": len(approvals), "original_tab": originalTab, "evaluations": observed.evaluations.Load(), "reload_count": 1, "sonar_calls": sonarCalls.Load(), "evidence_evaluations": observed.evidenceEvaluations.Load(), "bot_id": f.b.ID, "run_id": runID, "summary": run.Report.Summary, "dom": reloadDOM})
	t.Logf("LIVE completed calls=%d tokens=%d sonar_calls=%d elapsed=%s artifacts=%s", calls, tokens, sonarCalls.Load(), time.Since(started).Round(time.Second), dir)
}

// Read-only backend evidence assessment; deliberately not browser acceptance.
func TestBotRealProgressCapturedExecution(t *testing.T) {
	dir := os.Getenv("ASTONISH_PROGRESS_CAPTURE_DIR")
	if dir == "" {
		t.Skip("explicit captured execution directory required; no provider calls")
	}
	read := func(name string, v any) {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, v); err != nil {
			t.Fatal(err)
		}
	}
	var run store.BotRun
	read("run.json", &run)
	if run.Status != store.RunSucceeded || run.Report.Summary == "" || run.Progress == nil || run.Progress.Revision < 1 {
		t.Fatal("missing terminal result or evaluated progress")
	}
	var home string
	read("home.json", &home)
	if strings.Count(home, "bot-report:"+run.ID.String()) != 1 {
		t.Fatal("report publication is not unique")
	}
	evaluators, err := filepath.Glob(filepath.Join(dir, "evaluator-*-request.json"))
	if err != nil || len(evaluators) == 0 {
		t.Fatal("missing evaluator requests")
	}
	for _, path := range evaluators {
		var req model.LLMRequest
		read(filepath.Base(path), &req)
		if !liveProgressHasEvidence(&req) {
			t.Fatalf("missing confirmed assignment/source evidence: %s", path)
		}
	}
	sonar, err := filepath.Glob(filepath.Join(dir, "sonar-*-response-*.json"))
	if err != nil || len(sonar) == 0 {
		t.Fatal("missing actual sonar responses")
	}
	var sonarTokens int64
	for _, path := range sonar {
		var captured struct {
			Failed   bool               `json:"failed"`
			Response *model.LLMResponse `json:"response"`
		}
		read(filepath.Base(path), &captured)
		if captured.Failed || captured.Response == nil || captured.Response.UsageMetadata == nil {
			t.Fatalf("failed sonar response: %s", path)
		}
		sonarTokens += int64(captured.Response.UsageMetadata.TotalTokenCount)
	}
	var execution struct {
		Elapsed float64 `json:"elapsed_seconds"`
		Tokens  int64   `json:"tokens"`
	}
	read("execution.json", &execution)
	if execution.Elapsed > 540 || execution.Tokens > 500000 {
		t.Fatal("captured execution exceeded budget")
	}
	t.Logf("BACKEND ONLY: succeeded, unique report, %d evidence-bearing evaluations, %d sonar responses (%d reported search tokens), %d recorded total tokens, %.2fs; live/reload NOT asserted", len(evaluators), len(sonar), sonarTokens, execution.Tokens, execution.Elapsed)
}

func TestBotRealProgressBrowserEvidence(t *testing.T) {
	if os.Getenv("ASTONISH_REAL_PROGRESS_EVIDENCE") != "1" {
		t.Skip("explicit evidence opt-in")
	}
	p, e := os.ReadFile("../../.tmp/real-progress-browser-latest")
	if e != nil {
		t.Fatal(e)
	}
	raw, e := os.ReadFile(filepath.Join(string(p), "browser-accepted.json"))
	if e != nil {
		t.Fatal(e)
	}
	var v map[string]any
	if json.Unmarshal(raw, &v) != nil || v["live_visible"] != true || v["reload_visible"] != true || v["reload_count"] != float64(1) || v["exact_report_match"] != true || v["report_occurrences_live"] != float64(1) || v["report_occurrences_reload"] != float64(1) || v["approval_count"] != float64(1) || v["sonar_calls"].(float64) < 1 || v["evidence_evaluations"].(float64) < 1 {
		t.Fatal("missing strict live/search/progress/reload proof")
	}
	t.Logf("real browser evidence=%s", p)
}
