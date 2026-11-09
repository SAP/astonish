package api_test

import (
	"context"
	"github.com/go-rod/rod/lib/proto"
	"iter"
	"os"
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
	"github.com/go-rod/rod"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

func TestBotTitleChatBrowserAcceptance(t *testing.T) { runBotBrowserJourney(t, "title") }
func TestBotAmbiguousTitleQuestionBrowserAcceptance(t *testing.T) {
	runBotBrowserJourney(t, "title-question")
}
func TestBotThinkingRestoreBrowserAcceptance(t *testing.T) { runBotBrowserJourney(t, "title-restore") }

type titleDelayModel struct {
	release  chan struct{}
	entered  chan struct{}
	calls    atomic.Int64
	question bool
}

func (*titleDelayModel) Name() string { return "journey-model" }
func (m *titleDelayModel) GenerateContent(ctx context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		n := m.calls.Add(1)
		if n == 1 {
			close(m.entered)
			select {
			case <-ctx.Done():
				return
			case <-m.release:
			}
		}
		answered := false
		for _, c := range req.Contents {
			for _, p := range c.Parts {
				if strings.Contains(p.Text, "Only local release notes") {
					answered = true
				}
			}
		}
		if m.question && !answered {
			call := &genai.FunctionCall{ID: "title-question", Name: "ask_user", Args: map[string]any{"prompt": "What should I help with?", "kind": "select", "options": []any{map[string]any{"id": "news", "label": "News"}, map[string]any{"id": "notes", "label": "Notes"}}, "allow_custom_answer": true}}
			yield(&model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: call}}}}, nil)
			return
		}
		yield(&model.LLMResponse{Content: genai.NewContentFromText("Title task understood. No recurring work was enabled.", "model"), TurnComplete: true}, nil)
	}
}

func runTitleBrowserAssertions(t *testing.T, ctx context.Context, page *rod.Page, repo store.BotStore, ownerCtx context.Context, db store.PlatformBackend, registry *execution.BotTurnAuthorizationRegistry, b store.Bot, journey string) {
	t.Helper()
	principal, _ := execution.PrincipalFromContext(ownerCtx)
	serviceCtx, e := execution.WithPrincipal(ctx, execution.Principal{Kind: execution.PrincipalKindService, ClientID: "title-registry", Authentication: execution.AuthMethod("internal"), Surface: execution.SurfaceCode, Actor: "title-registry", OrgSlug: principal.OrgSlug, TeamSlug: principal.TeamSlug, Authenticated: true, Scopes: []string{entstore.BotHomeRegistrationScope}})
	if e != nil {
		t.Fatal(e)
	}
	home := store.BotHomeBinding{ID: uuid.New(), BotID: b.ID, Scope: b.Scope, Backend: "controlled", StorageKey: "journey-home", Revision: 1}
	if e = entstore.RegisterBotHomeBinding(serviceCtx, repo, entstore.BotHomeRegistration{BotID: b.ID, ExpectedRevision: b.Revision, Home: home}, func(context.Context, entstore.BotHomeRegistration) error { return nil }); e != nil {
		t.Fatal(e)
	}
	pool := &interactiveHTTPPool{dir: t.TempDir()}
	m := &titleDelayModel{release: make(chan struct{}), entered: make(chan struct{}), question: journey == "title-question"}
	worker, err := daemon.NewBotWorker(daemon.BotWorkerConfig{Backend: db, Sandbox: journeyHomeBackend{}, Pool: pool, TurnRegistry: registry, WorkerID: "title-browser", ContextWindow: 8192, ModelOptions: provider.BudgetLLMOptions{MaxOutputTokens: 100, SupportsOutputLimit: true}, ResolveModel: func(context.Context, execution.Principal, store.BotScope, string, string) (model.LLM, error) {
		return m, nil
	}, Prepare: func(context.Context, bot.RunRequest, *launcher.BotChatScope) (*launcher.BotChatSafeResources, error) {
		return &launcher.BotChatSafeResources{WorkspaceDir: "/isolated", ChatConfig: &config.AppConfig{}, Catalog: &launcher.ChatCatalogResources{Pool: pool}, VerifyIsolation: func(c context.Context) error { return c.Err() }, Cleanup: func() {}}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Stop()
	page.MustElement(`[data-testid="thinking-indicator"]`).MustVisible()
	poll := make(chan error, 1)
	go func() { poll <- worker.Poll(ctx) }()
	select {
	case <-m.entered:
	case e := <-poll:
		t.Fatalf("worker stopped before model: %v", e)
	case <-time.After(20 * time.Second):
		t.Fatal("worker never reached model")
	}
	page.MustElement(`[data-testid="thinking-indicator"]`).MustVisible()
	if journey == "title-restore" {
		page.MustReload()
		page.MustElement(`[data-testid="thinking-indicator"]`).MustVisible()
	}
	close(m.release)
	if e := <-poll; e != nil {
		t.Fatal(e)
	}
	if m.question {
		rows, e := repo.ListRuns(ownerCtx, b.ID, store.BotPage{})
		if e != nil || len(rows) != 1 || rows[0].Status != store.RunWaitingApproval {
			t.Fatalf("question not waiting: %+v %v", rows, e)
		}
		page.MustElementR("p", "What should I help with").MustVisible()
		page.MustWait(`() => !document.querySelector('[data-testid="thinking-indicator"]')`)
		page.MustReload()
		page.MustElementR("p", "What should I help with").MustVisible()
		page.MustElement(`textarea[aria-label="Custom answer"]`).MustInput("Only local release notes")
		page.MustElementR("button", "^Submit answer$").MustClick()
		page.MustWait(`() => document.body.innerText.includes('Answer saved; continuation queued.')`)
		if e := worker.Poll(ctx); e != nil {
			t.Fatal(e)
		}
	}
	page.MustElementR("p", "Title task understood").MustVisible()
	page.MustWait(`() => !document.querySelector('[data-testid="thinking-indicator"]')`)
	if m.question {
		page.MustWait(`() => document.querySelector('[aria-label="Bot workspace"]').innerText.split('Only local release notes').length - 1 === 1`)
	}
	before := m.calls.Load()
	page.MustReload()
	page.MustElementR("p", "Title task understood").MustVisible()
	if m.question {
		stored := page.MustEval(`async (id, sid) => (await fetch('/api/studio/bots/'+id+'/sessions/'+sid+'/stream?scope=personal')).text()`, b.ID.String(), b.HomeSessionID.String()).Str()
		if !strings.Contains(stored, "bot-owner-answer:") || !strings.Contains(stored, "bot_owner_answer") || !strings.Contains(stored, "Only local release notes") {
			t.Fatal("structured owner answer missing from durable Home transcript")
		}
		if page.MustEval(`() => document.querySelector('[aria-label="Bot workspace"]').innerText.includes('bot_owner_answer')`).Bool() {
			t.Fatal("structured owner answer leaked into the transcript")
		}
		if got := page.MustEval(`() => document.querySelector('[aria-label="Bot workspace"]').innerText.split('Only local release notes').length - 1`).Int(); got != 1 {
			t.Logf("restored DOM: %s", page.MustEval(`() => document.querySelector('[aria-label="Bot workspace"]').innerText`).Str())
			t.Logf("stored transcript: %s", page.MustEval(`async (id, sid) => (await fetch('/api/studio/bots/'+id+'/sessions/'+sid+'/stream?scope=personal')).text()`, b.ID.String(), b.HomeSessionID.String()).Str())
			t.Fatalf("custom answer displayed %d times", got)
		}
	}
	for _, dark := range []bool{false, true} {
		page.MustEval(`dark => document.documentElement.classList.toggle('dark',dark)`, dark)
		page.MustSetViewport(390, 844, 1, false)
		page.MustElementR("p", "Title task understood").MustVisible()
	}
	if err := os.MkdirAll("../../.tmp/studio-title-artifacts", 0755); err != nil {
		t.Fatal(err)
	}
	shot, e := page.Screenshot(false, &proto.PageCaptureScreenshot{Format: proto.PageCaptureScreenshotFormatPng})
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join("../../.tmp/studio-title-artifacts", journey+".png"), shot, 0600); e != nil {
		t.Fatal(e)
	}
	if m.calls.Load() != before {
		t.Fatal("reload replayed model")
	}
	runs, e := repo.ListRuns(ownerCtx, b.ID, store.BotPage{})
	if e != nil || len(runs) != 1 {
		t.Fatalf("title did not admit exactly once: %+v %v", runs, e)
	}
	current, e := repo.Get(ownerCtx, b.ID)
	if e != nil || current.Status != store.BotDraft {
		t.Fatal("title changed lifecycle", e)
	}
	v, e := repo.GetVersion(ownerCtx, b.ID, current.Version)
	if e != nil || len(v.Triggers) != 0 {
		t.Fatal("title created recurrence", e)
	}
	t.Log("real title POST -> queued Thinking -> worker running before first token -> canonical reply/reload; one run, draft, no recurrence")
}
