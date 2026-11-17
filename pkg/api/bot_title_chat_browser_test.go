package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
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
	"github.com/SAP/astonish/pkg/sandbox"
	_ "github.com/SAP/astonish/pkg/sandbox/docker"
	"github.com/SAP/astonish/pkg/store"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

func TestBotTitleConfirmationDockerBrowserAcceptance(t *testing.T) {
	if os.Getenv("ASTONISH_TEST_DOCKER") != "1" {
		t.Skip("set ASTONISH_TEST_DOCKER=1 for owned physical Home evidence")
	}
	runBotBrowserJourney(t, "title-docker")
}

func TestBotTitleTeamOptionBrowserAcceptance(t *testing.T) { runBotBrowserJourney(t, "title-team") }
func TestBotTitleTeamCustomBrowserAcceptance(t *testing.T) {
	runBotBrowserJourney(t, "title-team-question")
}
func TestBotTitleChatBrowserAcceptance(t *testing.T) { runBotBrowserJourney(t, "title") }
func TestBotAmbiguousTitleQuestionBrowserAcceptance(t *testing.T) {
	runBotBrowserJourney(t, "title-question")
}
func TestBotThinkingRestoreBrowserAcceptance(t *testing.T) { runBotBrowserJourney(t, "title-restore") }

type titleDelayModel struct {
	release    chan struct{}
	entered    chan struct{}
	calls      atomic.Int64
	assignment string
	readCalled bool
}

const titleBrowserPrivateResult = "PRIVATE_RELEASE_SOURCE_BODY_MUST_NOT_APPEAR"

func (m *titleDelayModel) acknowledgement() string {
	return "I'll read the isolated release notes for your assignment: " + m.assignment + "."
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
			call := &genai.FunctionCall{ID: "title-question", Name: "ask_user", Args: map[string]any{
				"prompt": "Which release-note assignment should I carry out?", "kind": "select", "allow_custom_answer": true,
				"options": []any{map[string]any{"id": "notes", "label": "Summarize local release notes"}, map[string]any{"id": "changes", "label": "Compare changes between releases"}, map[string]any{"id": "watch", "label": "Explain how to watch future releases"}},
			}}
			yield(&model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: call}}}}, nil)
			return
		}
		if req.Config != nil && req.Config.SystemInstruction != nil && len(req.Config.SystemInstruction.Parts) > 0 {
			prompt := req.Config.SystemInstruction.Parts[0].Text
			if strings.HasPrefix(prompt, "BOT_SKILL_ORIENT_V1") {
				yield(&model.LLMResponse{Content: genai.NewContentFromText(`{"kind":"no_skill_needed","skill_id":"","title":"","responsibility":"","method":[],"expectations":[],"reason":"Read the isolated local release notes"}`, "model")}, nil)
				return
			}
			if strings.HasPrefix(prompt, "BOT_EMPLOYEE_REVIEW") || strings.HasPrefix(prompt, "BOT_WORK_REVIEW") {
				yield(nil, fmt.Errorf("read-only title unexpectedly requested end review"))
				return
			}
		}
		raw, _ := json.Marshal(req.Contents)
		if !strings.Contains(string(raw), m.assignment) {
			yield(nil, fmt.Errorf("confirmed assignment missing from ordinary producer input"))
			return
		}
		if !m.readCalled {
			m.readCalled = true
			yield(&model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{Text: m.acknowledgement()}, {FunctionCall: &genai.FunctionCall{ID: "read-local-notes", Name: "read_file", Args: map[string]any{"path": "/isolated/receipt.txt"}}}}}, TurnComplete: true}, nil)
			return
		}
		if !strings.Contains(string(raw), titleBrowserPrivateResult) {
			yield(nil, fmt.Errorf("ordinary producer never received the actual private read result"))
			return
		}
		yield(&model.LLMResponse{Content: genai.NewContentFromText("Title task understood. No recurring work was enabled.", "model"), TurnComplete: true}, nil)
	}
}

type titleBrowserHomeBackend struct {
	sandbox.Backend
	physical sandbox.BotHomeBackend
	calls    atomic.Int64
}

func (b *titleBrowserHomeBackend) EnsureBotHome(ctx context.Context, s sandbox.BotHomeScope) (*sandbox.BotHomeBinding, error) {
	b.calls.Add(1)
	if b.physical != nil {
		return b.physical.EnsureBotHome(ctx, s)
	}
	return &sandbox.BotHomeBinding{Scope: s, Backend: "controlled", Ref: "title-home-" + s.BotID}, nil
}

// Physical proof is narrower than a full tool-node session: the production
// worker provisions the real Docker Home; owned helpers verify its mount/bytes.
func titlePhysicalHomeProbe(t *testing.T, ctx, ownerCtx context.Context, b store.Bot, home *titleBrowserHomeBackend, repo store.BotStore) (func(), func()) {
	t.Helper()
	image := os.Getenv("ASTONISH_SANDBOX_IMAGE")
	if image == "" {
		t.Fatal("ASTONISH_SANDBOX_IMAGE must name an existing local image")
	}
	command := func(args ...string) string {
		t.Helper()
		out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("docker %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	image = command("image", "inspect", "--format", "{{.Id}}", image)
	p, _ := execution.PrincipalFromContext(ownerCtx)
	scope := sandbox.BotHomeScope{OrgID: p.OrgSlug, TeamID: p.TeamSlug, UserID: b.OwnerID.String(), BotID: b.ID.String()}
	volume := "astonish-bot-home-" + scope.Key()
	root := t.TempDir()
	st, err := sandbox.NewLocalSessionStore(filepath.Join(root, "registry"))
	if err != nil {
		t.Fatal(err)
	}
	backend, err := sandbox.NewBackend(sandbox.BackendFactoryConfig{Kind: sandbox.BackendKindDocker, Sessions: sandbox.NewSessionRegistryFromStore(st), Docker: sandbox.DockerRuntimeConfig{SandboxImage: image, LayersDir: filepath.Join(root, "layers"), UppersDir: filepath.Join(root, "uppers")}})
	if err != nil {
		t.Fatal(err)
	}
	home.physical = backend.(sandbox.BotHomeBackend)
	before := func() {
		if got := command("volume", "ls", "-q", "--filter", "label=astonish.bot-home.scope="+scope.Key()); got != "" {
			t.Fatalf("physical Home existed before answer: %s", got)
		}
		if got := command("ps", "-aq", "--filter", "volume="+volume); got != "" {
			t.Fatalf("physical Home container before answer: %s", got)
		}
	}
	before()
	helper := "astonish-title-confirmation-" + b.ID.String()
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		out, err := exec.CommandContext(c, "docker", "ps", "-aq", "--filter", "name=^/"+helper+"$").CombinedOutput()
		if err != nil {
			t.Errorf("cleanup lookup: %v %s", err, out)
			return
		}
		if strings.TrimSpace(string(out)) != "" {
			if out, err := exec.CommandContext(c, "docker", "rm", "-f", helper).CombinedOutput(); err != nil {
				t.Errorf("cleanup helper: %v %s", err, out)
				return
			}
		}
		out, err = exec.CommandContext(c, "docker", "volume", "ls", "-q", "--filter", "label=astonish.bot-home.scope="+scope.Key()).CombinedOutput()
		if err != nil {
			t.Errorf("cleanup ownership: %v %s", err, out)
			return
		}
		if strings.TrimSpace(string(out)) == volume {
			if out, err := exec.CommandContext(c, "docker", "volume", "rm", volume).CombinedOutput(); err != nil {
				t.Errorf("cleanup Home: %v %s", err, out)
			}
		}
	})
	after := func() {
		current, err := repo.Get(ownerCtx, b.ID)
		if err != nil || current.Home.Backend != "docker" || current.Home.StorageKey != volume {
			t.Fatalf("physical binding mismatch: %+v %v", current, err)
		}
		if got := command("volume", "inspect", "--format", `{{index .Labels "astonish.bot-home.scope"}}`, volume); got != scope.Key() {
			t.Fatal("physical ownership mismatch")
		}
		command("run", "--pull=never", "--name", helper, "--label", "astonish.title-confirmation="+b.ID.String(), "--network=none", "--mount", "type=volume,src="+volume+",dst=/owned-home", "--entrypoint", "/bin/sh", image, "-c", "printf confirmation-mount-receipt > /owned-home/confirmation-mount-receipt")
		if got := command("inspect", "--format", `{{range .Mounts}}{{if eq .Destination "/owned-home"}}{{.Name}}{{end}}{{end}}`, helper); got != volume {
			t.Fatal("helper mounted a foreign Home")
		}
		command("rm", helper)
		got := command("run", "--pull=never", "--rm", "--name", helper, "--network=none", "--mount", "type=volume,src="+volume+",dst=/owned-home,readonly", "--entrypoint", "/bin/sh", image, "-c", "cat /owned-home/confirmation-mount-receipt")
		if got != "confirmation-mount-receipt" {
			t.Fatal("physical receipt missing")
		}
		t.Logf("physical Docker: no scoped volume/container before answer; worker-created binding %s; owned helper mount and persisted receipt verified (not a tool-node/Task-file claim)", volume)
	}
	return before, after
}

func runTitleBrowserAssertions(t *testing.T, ctx context.Context, page *rod.Page, repo store.BotStore, ownerCtx context.Context, db store.PlatformBackend, registry *execution.BotTurnAuthorizationRegistry, b store.Bot, journey string) {
	t.Helper()
	pool := &interactiveHTTPPool{dir: t.TempDir()}
	readEntered, releaseRead := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-releaseRead:
		default:
			close(releaseRead)
		}
	}()
	pool.callOverride = func(name string, args map[string]interface{}) (json.RawMessage, error) {
		if name != "read_file" || args["path"] != "/isolated/receipt.txt" {
			return nil, fmt.Errorf("unexpected useful-work tool %s: %v", name, args)
		}
		close(readEntered)
		select {
		case <-releaseRead:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return json.Marshal(map[string]any{"content": titleBrowserPrivateResult})
	}
	home := &titleBrowserHomeBackend{}
	physicalBefore, physicalAfter := func() {}, func() {}
	if journey == "title-docker" {
		physicalBefore, physicalAfter = titlePhysicalHomeProbe(t, ctx, ownerCtx, b, home, repo)
	}
	physicalBefore()
	var prepared atomic.Int64
	assignment := "Summarize local release notes"
	if strings.HasSuffix(journey, "question") {
		assignment = "Only local release notes"
	}
	m := &titleDelayModel{release: make(chan struct{}), entered: make(chan struct{}), assignment: assignment}
	cfg := daemon.BotWorkerConfig{Backend: db, Sandbox: home, Pool: pool, TurnRegistry: registry, WorkerID: "title-browser", ContextWindow: 8192,
		ModelOptions: provider.BudgetLLMOptions{MaxOutputTokens: 512, SupportsOutputLimit: true},
		ResolveModel: func(context.Context, execution.Principal, store.BotScope, string, string) (model.LLM, error) {
			return m, nil
		},
		Prepare: func(_ context.Context, req bot.RunRequest, _ *launcher.BotChatScope) (*launcher.BotChatSafeResources, error) {
			prepared.Add(1)
			if req.Instructions != assignment {
				return nil, fmt.Errorf("Prepare received unconfirmed assignment %q", req.Instructions)
			}
			return &launcher.BotChatSafeResources{WorkspaceDir: "/isolated", ChatConfig: &config.AppConfig{}, Catalog: &launcher.ChatCatalogResources{Pool: pool}, VerifyIsolation: func(c context.Context) error { return c.Err() }, Cleanup: func() {}}, nil
		},
	}
	worker, err := daemon.NewBotWorker(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		select {
		case <-releaseRead:
		default:
			close(releaseRead)
		}
		worker.Stop()
	}()
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
	// Observe every DOM update, not just the settled card, so a flashing technical
	// placeholder is an acceptance failure as well.
	page.MustEval(`() => {
		window.__titleQuestionLeaks = [];
		const check = () => { const text = document.querySelector('[aria-label="Bot workspace"]')?.innerText || ''; for (const marker of ['Loading saved review', 'Refresh review', 'Waiting for your answer']) if (text.includes(marker)) window.__titleQuestionLeaks.push(marker); };
		new MutationObserver(check).observe(document.body, {subtree:true, childList:true, characterData:true}); check();
	}`)
	close(m.release)
	if err := <-poll; err != nil {
		t.Fatal(err)
	}
	rows, err := repo.ListRuns(ownerCtx, b.ID, store.BotPage{})
	if err != nil || len(rows) != 1 || rows[0].Status != store.RunWaitingApproval || rows[0].TitleConfirmation == nil || rows[0].TitleConfirmation.State != store.BotTitleConfirmationWaiting {
		t.Fatalf("question not waiting: %+v %v", rows, err)
	}
	approvals, err := repo.ListApprovals(ownerCtx, b.ID, store.BotPage{})
	if err != nil || len(approvals) != 1 || approvals[0].Interaction == nil {
		t.Fatalf("question approval: %+v %v", approvals, err)
	}
	approval := approvals[0]
	var question struct {
		Options           []struct{ ID, Label string }
		AllowCustomAnswer bool `json:"allow_custom_answer"`
	}
	if err := json.Unmarshal(approval.Interaction.Payload, &question); err != nil || len(question.Options) != 3 || !question.AllowCustomAnswer {
		t.Fatalf("invalid title choices: %s %v", approval.Interaction.Payload, err)
	}
	confirmation := rows[0].TitleConfirmation
	if err := confirmation.ValidateBinding(rows[0], b.OwnerID); err != nil || confirmation.ApprovalID != approval.ID || confirmation.Question == nil || string(confirmation.Question.Payload) != string(approval.Interaction.Payload) {
		t.Fatalf("persisted title confirmation mismatch: %+v error=%v", confirmation, err)
	}
	current, err := repo.Get(ownerCtx, b.ID)
	if err != nil || current == nil {
		t.Fatalf("load waiting Bot: bot=%+v error=%v", current, err)
	}
	// LoadRunInput is deliberately restricted to queued runs. While waiting,
	// inspect the persisted confirmation and Task reference, not executable input.
	if current.Home.ID != uuid.Nil || rows[0].Task != nil || home.calls.Load() != 0 || prepared.Load() != 0 || pool.calls != 0 {
		t.Fatalf("work before confirmation: home=%+v task=%+v homeCalls=%d prepare=%d tools=%d", current.Home, rows[0].Task, home.calls.Load(), prepared.Load(), pool.calls)
	}
	t.Log("waiting confirmation persisted; checking live question card")
	assertCleanQuestion := func() {
		t.Helper()
		page.MustElementR("p", "Which release-note assignment").MustVisible()
		page.MustWait(`() => !document.querySelector('[data-testid="thinking-indicator"]')`)
		// Studio keeps an off-screen responsive copy mounted; count the controls
		// the owner can actually see, retaining the strict one-card assertion.
		if got := page.MustEval(`() => [...document.querySelectorAll('[data-testid="ask-user-options"][aria-label="Which release-note assignment should I carry out?"]')].filter(e => e.getClientRects().length).length`).Int(); got != 1 {
			t.Fatalf("standard question groups=%d, want one; DOM=%s", got, page.MustEval(`() => [...document.querySelectorAll('[data-testid="ask-user-options"]')].map(e => ({label:e.getAttribute('aria-label'), visible:!!e.getClientRects().length, parent:e.parentElement.parentElement.parentElement.outerHTML})).map(e=>JSON.stringify(e)).join('\n')`).Str())
		}
		if got := page.MustEval(`() => [...document.querySelectorAll('textarea[aria-label="Custom answer"]')].filter(e => e.getClientRects().length).length`).Int(); got != 1 {
			t.Fatalf("custom answer controls=%d, want one", got)
		}
		if got := page.MustEval(`() => [...document.querySelectorAll('textarea[aria-label="Custom answer"]')].find(e => e.getClientRects().length)?.getAttribute('placeholder')`).Str(); got != "Something else…" {
			t.Fatalf("custom answer placeholder=%q, want Something else…", got)
		}
		if got := page.MustEval(`() => (window.__titleQuestionLeaks || []).join(',')`).Str(); got != "" {
			t.Fatalf("transient technical fallback: %s", got)
		}
		for _, forbidden := range []string{"Loading saved review", "Refresh review", "Waiting for your answer", "Waiting for approval", "[[ASTONISH_CHAT_QUESTION", "bot_owner_answer"} {
			if page.MustEval(`text => document.querySelector('[aria-label="Bot workspace"]').innerText.includes(text)`, forbidden).Bool() {
				t.Fatalf("technical fallback leaked: %s", forbidden)
			}
		}
	}
	assertCleanQuestion()
	t.Log("live question card settled; replacing worker")
	// Replace the worker while waiting. Registry remains the live HTTP authority;
	// separate HTTP/store tests exercise loss and reauthorization of that authority.
	worker.Stop()
	worker, err = daemon.NewBotWorker(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err = worker.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	page.MustReload()
	assertCleanQuestion()
	restored, err := repo.ListApprovals(ownerCtx, b.ID, store.BotPage{})
	if err != nil || len(restored) != 1 || restored[0].ID != approval.ID || restored[0].Interaction == nil || string(restored[0].Interaction.Payload) != string(approval.Interaction.Payload) || m.calls.Load() != 1 || home.calls.Load() != 0 {
		t.Fatal("waiting reload/restart regenerated question or prepared Home", err)
	}
	t.Log("waiting question restored; submitting answer")
	assertNoAuthorizationError := func() {
		t.Helper()
		if got := page.MustEval(`() => {
			const text = document.querySelector('[aria-label="Bot workspace"]').innerText;
			return text.match(/authorization (?:failed|error|expired|denied|required|unavailable)|not authorized|unauthorized|forbidden|permission denied|no longer authorized|could not authorize|couldn't authorize|can’t access this Bot with your current authorization|need fresh authorization|need your authorization to continue/gi)?.join(',') || '';
		}`).Str(); got != "" {
			t.Fatalf("authorization error after confirmed answer: %s", got)
		}
	}
	physicalBefore()
	if strings.HasSuffix(journey, "question") {
		page.MustElement(`textarea[aria-label="Custom answer"]`).MustInput(assignment)
		page.MustElementR("button", "^Submit answer$").MustClick()
	} else {
		page.MustElementR(`[role="radio"]`, "^Summarize local release notes$").MustClick()
	}
	assertAnswered := func() {
		t.Helper()
		page.MustWait(`answer => [...document.querySelectorAll('article[aria-label="Answered question"]')].some(e => e.getClientRects().length && e.querySelector('dt')?.textContent === 'Which release-note assignment should I carry out?' && e.querySelector('dd')?.textContent === 'Your answer: ' + answer)`, assignment)
		if got := page.MustEval(`() => [...document.querySelectorAll('article[aria-label="Answered question"]')].filter(e => e.getClientRects().length).length`).Int(); got != 1 {
			t.Fatalf("answered cards=%d, want one", got)
		}
		if page.MustEval(`() => [...document.querySelectorAll('[data-testid="ask-user-options"], textarea[aria-label="Custom answer"]')].some(e => e.getClientRects().length)`).Bool() {
			t.Fatal("answered question retained expanded choices or custom input")
		}
		if page.MustEval(`() => [...document.querySelectorAll('[aria-label="Bot workspace"] [data-testid="tool-activity-block"]')].some(e => e.getClientRects().length)`).Bool() {
			t.Fatal("Bot Home exposed a tool activity card")
		}
		for _, forbidden := range []string{"I saved your answer", "queued the continuation", "Consumed", "[[ASTONISH_CHAT_QUESTION", "bot_owner_answer", "read_file", "Tool activity", titleBrowserPrivateResult} {
			if page.MustEval(`text => document.querySelector('[aria-label="Bot workspace"]').innerText.includes(text)`, forbidden).Bool() {
				t.Fatalf("technical receipt or private payload leaked: %s", forbidden)
			}
		}
	}
	assertAnswered()
	assertNoAuthorizationError()
	if page.MustEval(`text => document.querySelector('[aria-label="Bot workspace"]').innerText.includes(text)`, m.acknowledgement()).Bool() {
		t.Fatal("UI fabricated acknowledgement before producer ran")
	}
	page.MustReload()
	assertAnswered()
	go func() { poll <- worker.Poll(ctx) }()
	select {
	case <-readEntered:
	case e := <-poll:
		t.Fatalf("worker stopped before useful work: %v", e)
	case <-time.After(20 * time.Second):
		t.Fatal("confirmed worker never reached real read_file")
	}
	ackCtx, cancelAck := context.WithTimeout(ctx, 10*time.Second)
	ackErr := page.Context(ackCtx).Wait(rod.Eval(`text => document.querySelector('[aria-label="Bot workspace"]').innerText.includes(text)`, m.acknowledgement()))
	cancelAck()
	if ackErr != nil {
		dom := page.MustEval(`() => document.querySelector('[aria-label="Bot workspace"]').innerText`).Str()
		t.Errorf("actual model acknowledgement absent while real read_file blocked: %v\nDOM: %s", ackErr, dom)
	}
	assertAnswered()
	if page.MustEval(`() => document.querySelector('[aria-label="Bot workspace"]').innerText.includes('Title task understood.')`).Bool() {
		t.Fatal("final appeared while useful work was still blocked")
	}
	if !page.MustEval(`ack => {
		const text = document.querySelector('[aria-label="Bot workspace"]').innerText;
		return text.indexOf('Your answer: ') < text.indexOf(ack);
	}`, m.acknowledgement()).Bool() {
		t.Errorf("model acknowledgement appeared before answered question; DOM: %s", page.MustEval(`() => document.querySelector('[aria-label="Bot workspace"]').innerText`).Str())
	}
	close(releaseRead)
	if err = <-poll; err != nil {
		t.Fatal(err)
	}
	page.MustElementR("p", "Title task understood").MustVisible()
	assertAnswered()
	assertNoAuthorizationError()
	page.MustWait(`() => !document.querySelector('[data-testid="thinking-indicator"]')`)
	consumed, err := repo.ListApprovals(ownerCtx, b.ID, store.BotPage{})
	if err != nil || len(consumed) != 1 || consumed[0].Status != store.ApprovalConsumed {
		t.Fatalf("answer was not consumed: %+v error=%v", consumed, err)
	}
	if prepared.Load() != 1 || home.calls.Load() < 1 || pool.calls != 1 {
		t.Fatalf("confirmed execution count prepare=%d homeEnsures=%d reads=%d", prepared.Load(), home.calls.Load(), pool.calls)
	}
	physicalAfter()
	before := m.calls.Load()
	if err = worker.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	page.MustReload()
	page.MustElementR("p", "Title task understood").MustVisible()
	assertAnswered()
	if !page.MustEval(`ack => {
		const text = document.querySelector('[aria-label="Bot workspace"]').innerText;
		return text.indexOf(ack) > text.indexOf('Your answer: ') && text.indexOf(ack) < text.indexOf('Title task understood.');
	}`, m.acknowledgement()).Bool() {
		t.Fatal("reload lost answered-question / actual acknowledgement / final order")
	}
	assertNoAuthorizationError()
	if got := page.MustEval(`() => document.querySelector('[aria-label="Bot workspace"]').innerText.split('Title task understood.').length-1`).Int(); got != 1 {
		t.Fatalf("final published %d times", got)
	}
	if page.MustEval(`() => document.querySelector('[aria-label="Bot workspace"]').innerText.includes('bot_owner_answer')`).Bool() {
		t.Fatal("structured answer leaked")
	}
	for _, dark := range []bool{false, true} {
		page.MustEval(`dark => document.documentElement.classList.toggle('dark',dark)`, dark)
		page.MustSetViewport(390, 844, 1, false)
		page.MustElementR("p", "Title task understood").MustVisible()
		assertAnswered()
		assertNoAuthorizationError()
	}
	if err = os.MkdirAll("../../.tmp/studio-title-artifacts", 0755); err != nil {
		t.Fatal(err)
	}
	shot, err := page.Screenshot(false, &proto.PageCaptureScreenshot{Format: proto.PageCaptureScreenshotFormatPng})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join("../../.tmp/studio-title-artifacts", journey+".png"), shot, 0600); err != nil {
		t.Fatal(err)
	}
	runs, err := repo.ListRuns(ownerCtx, b.ID, store.BotPage{})
	if err != nil || len(runs) != 1 || runs[0].Status != store.RunSucceeded || m.calls.Load() != before || prepared.Load() != 1 {
		t.Fatalf("execution replay or non-success: %+v %v", runs, err)
	}
	current, err = repo.Get(ownerCtx, b.ID)
	if err != nil || current.Status != store.BotDraft {
		t.Fatal("title changed lifecycle", err)
	}
	v, err := repo.GetVersion(ownerCtx, b.ID, current.Version)
	if err != nil || len(v.Triggers) != 0 {
		t.Fatal("title created recurrence", err)
	}
	t.Log("built Studio: title -> three choices/custom -> waiting reload and worker replacement -> compact answered prompt/label before worker and after consumption/reload -> actual model acknowledgement while read_file is blocked -> one final; no queue receipt, expanded controls or raw private result. Zero controlled Home/Prepare/tool calls and no Task before answer. Registry-backed browser; mandate-backed HTTP and physical Docker are separate evidence.")
}
