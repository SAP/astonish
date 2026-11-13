package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/channels"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/launcher"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

var errTriggerControlledModel = errors.New("controlled external-trigger model failure")

const triggerExecutionAnswer = "I have no independently verified external result to report."

type triggerExecutionModel struct {
	calls   int
	succeed bool
}

func (*triggerExecutionModel) Name() string { return "controlled" }
func (m *triggerExecutionModel) GenerateContent(_ context.Context, r *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	if response, ok := workerWorkAssessment(r, func(w *store.BotWorkState) bool { return w.Candidate.Text == triggerExecutionAnswer }); ok {
		return response
	}
	return func(yield func(*model.LLMResponse, error) bool) {
		m.calls++
		if m.succeed {
			yield(&model.LLMResponse{Content: genai.NewContentFromText(triggerExecutionAnswer, "model"), UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 5, CandidatesTokenCount: 3, TotalTokenCount: 8}}, nil)
			return
		}
		yield(nil, errTriggerControlledModel)
	}
}

type triggerWorkerBackend struct{ sandbox.Backend }

func (triggerWorkerBackend) EnsureBotHome(_ context.Context, scope sandbox.BotHomeScope) (*sandbox.BotHomeBinding, error) {
	return &sandbox.BotHomeBinding{Scope: scope, Backend: "controlled", Ref: "home"}, nil
}

func TestChannelBotServices(t *testing.T) {
	backend, owner := setupChannelResolverFixture(t, "triggers", "team", "telegram", "linked")
	resolver := &channelPlatformResolver{backend: backend}
	ctx, _, _, err := resolver.ResolveChannelUser(context.Background(), "telegram", "linked")
	if err != nil {
		t.Fatal(err)
	}
	services := store.FromContext(ctx)
	if services == nil || services.Bots == nil || services.PersonalBots == nil || services.PersonalCredentials == nil || store.UserIDFromContext(ctx) != owner {
		t.Fatalf("missing scoped services: %+v", services)
	}
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		repo := store.BotStoreFromContext(ctx, scope)
		v := store.DefaultBotVersion()
		v.Charter = "Scoped channel bot"
		b, err := repo.CreateDraft(ctx, store.BotCreateRequest{Name: string(scope), OwnerID: uuid.MustParse(owner), Version: v, IdempotencyKey: string(scope)})
		if err != nil || b.Scope != scope {
			t.Fatalf("scoped create %+v %v", b, err)
		}
	}
	if _, _, _, err := resolver.ResolveChannelUser(context.Background(), "telegram", "unknown"); err == nil {
		t.Fatal("unlinked sender authorized")
	}
}

// A real socket -> production HTTP admission -> durable SQLite -> production
// BotWorker.execute/Supervisor.Execute -> ChatRuntime. Only model, home backend
// and isolation preparation are controlled; no outbound transport is installed.
// Both a deliberate model failure and a completed text-only report are persisted;
// a successful report does not claim independent verification of external effects.
func TestBotExternalTriggersHTTPWorkerExecution(t *testing.T) {
	backend, owner := setupChannelResolverFixture(t, "triggers", "team", "telegram", "linked")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resolver := &channelPlatformResolver{backend: backend}
	channelCtx, _, _, err := resolver.ResolveChannelUser(ctx, "telegram", "linked")
	if err != nil {
		t.Fatal(err)
	}
	org, err := backend.ForOrg("triggers")
	if err != nil {
		t.Fatal(err)
	}
	principal, _ := execution.PrincipalFromContext(channelCtx)
	principal.Authentication = execution.AuthMethodPlatformJWT
	principal.Surface = execution.SurfaceStudio
	auth, err := execution.WithPrincipal(channelCtx, principal)
	if err != nil {
		t.Fatal(err)
	}
	repo := store.BotStoreFromContext(auth, store.BotScopePersonal)
	triggers := repo.(store.BotTriggerStore)
	const secret = "fixture-external-trigger-signing-key-at-least-32-bytes"
	if err := org.ForUser(owner).Credentials().Set(auth, "signer", &store.Credential{Type: store.CredAPIKey, Value: secret}); err != nil {
		t.Fatal(err)
	}
	v := store.DefaultBotVersion()
	v.Charter = "Observe only reviewed trigger instructions"
	b, err := repo.CreateDraft(auth, store.BotCreateRequest{Name: "External execution", OwnerID: uuid.MustParse(owner), Version: v, IdempotencyKey: "external"})
	if err != nil {
		t.Fatal(err)
	}
	backend.(interface {
		SetBotActivationReadiness(store.BotActivationReadiness)
	}).SetBotActivationReadiness(func(context.Context, store.Bot, store.BotVersion) error { return nil })
	registry, err := execution.WithPrincipal(ctx, execution.Principal{Kind: execution.PrincipalKindService, ClientID: "registry", Authentication: execution.AuthMethod("internal"), Surface: execution.SurfaceCode, OrgSlug: "triggers", TeamSlug: "team", Authenticated: true, Scopes: []string{entstore.BotHomeRegistrationScope}})
	if err != nil {
		t.Fatal(err)
	}
	home := store.BotHomeBinding{ID: uuid.New(), BotID: b.ID, Scope: b.Scope, Backend: "controlled", StorageKey: "home", Revision: 1}
	if err := entstore.RegisterBotHomeBinding(registry, repo, entstore.BotHomeRegistration{BotID: b.ID, ExpectedRevision: b.Revision, Home: home}, func(context.Context, entstore.BotHomeRegistration) error { return nil }); err != nil {
		t.Fatal(err)
	}
	b, err = repo.Get(auth, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	b, err = repo.Activate(auth, store.BotLifecycleRequest{BotID: b.ID, ActorID: b.OwnerID, ExpectedRevision: b.Revision, Reason: "controlled readiness"})
	if err != nil {
		t.Fatal(err)
	}
	if err := org.ForUser(owner).SetSessionPin(auth, b.HomeSessionID.String(), "controlled", "controlled"); err != nil {
		t.Fatal(err)
	}
	llm := &triggerExecutionModel{}
	worker, err := NewBotWorker(BotWorkerConfig{Backend: backend, Sandbox: triggerWorkerBackend{}, Pool: workerTestPool{}, WorkerID: "external-worker", ContextWindow: 8192, ModelOptions: provider.BudgetLLMOptions{MaxOutputTokens: 100, SupportsOutputLimit: true}, ResolveModel: func(_ context.Context, p execution.Principal, scope store.BotScope, _, _ string) (model.LLM, error) {
		if p.Subject != owner || scope != store.BotScopePersonal {
			t.Errorf("wrong worker principal/scope: %+v %s", p, scope)
		}
		return llm, nil
	}, Prepare: func(context.Context, bot.RunRequest, *launcher.BotChatScope) (*launcher.BotChatSafeResources, error) {
		return &launcher.BotChatSafeResources{WorkspaceDir: "/workspace", PolicyRuntime: agent.BotPolicyRuntime{Guard: workerTestGuard{}, Classifier: botUnknownClassifier{}}, VerifyIsolation: func(context.Context) error { return nil }, Cleanup: func() {}}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	router := mux.NewRouter()
	router.HandleFunc("/bots/{botID}/triggers/{triggerID}/events", api.BotTriggerEventHandler).Methods("POST")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := r.Context()
		if r.Header.Get("X-Fixture-Actor") == owner {
			c, _ = execution.WithPrincipal(c, principal)
			c = store.WithTenantContext(c, &store.TenantContext{UserID: owner, OrgSlug: "triggers", TeamSlug: "team"})
			c = store.WithServices(c, store.FromContext(auth))
		}
		router.ServeHTTP(w, r.WithContext(c))
	}))
	defer server.Close()
	client := server.Client()
	client.Timeout = 5 * time.Second
	send := func(q store.BotExternalEvent, actor string, want int) store.BotRun {
		t.Helper()
		raw, _ := json.Marshal(map[string]any{"version": q.Version, "event_id": q.EventID, "timestamp": q.Timestamp, "payload": q.Payload})
		req, _ := http.NewRequestWithContext(ctx, "POST", fmt.Sprintf("%s/bots/%s/triggers/%s/events", server.URL, q.BotID, q.TriggerID), bytes.NewReader(raw))
		req.Header.Set("X-Fixture-Actor", actor)
		req.Header.Set("X-Astonish-Signature", q.Signature)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != want {
			t.Fatalf("HTTP %d want %d: %s", resp.StatusCode, want, body)
		}
		var run store.BotRun
		if want == 202 {
			if err := json.Unmarshal(body, &run); err != nil {
				t.Fatal(err)
			}
		}
		return run
	}
	change := func(id uuid.UUID, action store.BotTriggerAction, def store.TriggerSpec, proof *store.BotExternalEvent) {
		t.Helper()
		current, err := repo.Get(auth, b.ID)
		if err != nil {
			t.Fatal(err)
		}
		b, err = triggers.ChangeTrigger(auth, store.BotTriggerChange{BotID: b.ID, TriggerID: id, ExpectedRevision: current.Revision, Action: action, Reason: "reviewed", Definition: def, Proof: proof})
		if err != nil {
			t.Fatal(err)
		}
	}
	execute := func(run store.BotRun) {
		t.Helper()
		before := llm.calls
		wantStatus := store.RunFailed
		var wantErr error = errTriggerControlledModel
		if llm.succeed {
			wantStatus, wantErr = store.RunSucceeded, nil
		}
		if err := worker.execute(auth, repo, org.ForUser(owner).Sessions(), b.ID, run.ID); !errors.Is(err, wantErr) {
			saved, _ := repo.GetRun(auth, b.ID, run.ID)
			t.Fatalf("execute: %v saved=%+v calls=%d", err, saved, llm.calls)
		}
		saved, err := repo.GetRun(auth, b.ID, run.ID)
		if err != nil || saved.Status != wantStatus || saved.Report.Summary == "" || saved.FinishedAt == nil || llm.calls <= before {
			t.Fatalf("worker did not execute %+v calls=%d %v", saved, llm.calls, err)
		}
		if llm.succeed && (saved.Report.Summary != triggerExecutionAnswer || len(saved.Report.Unverified) != 0 || saved.Usage.ToolCalls != 0 || saved.Usage.InputTokens != 5 || saved.Usage.OutputTokens != 3 || saved.Report.Quality != nil || saved.Report.Work == nil || saved.Report.Work.Protocol != store.BotWorkProtocolAgentLedV1 || saved.Report.Work.Phase != store.BotWorkFinished || llm.calls != before+1) {
			t.Fatalf("successful report/accounting not persisted truthfully: %+v", saved)
		}
		events, err := org.ForUser(owner).Sessions().ReadTranscriptEvents(auth, "astonish", owner, saved.SessionID.String())
		if err != nil || len(events) == 0 {
			t.Fatalf("no durable transcript: %v", err)
		}
	}
	for _, kind := range []store.TriggerKind{store.TriggerHook, store.TriggerWebhook} {
		tid := uuid.New()
		def := store.TriggerSpec{Kind: kind, Instructions: "Report reviewed observation", CredentialRef: "signer"}
		change(tid, store.BotTriggerSave, def, nil)
		q := store.BotExternalEvent{BotID: b.ID, Version: b.Version, TriggerID: tid, EventID: uuid.NewString(), Timestamp: time.Now().Unix(), Payload: []byte("untrusted body")}
		q.Signature = store.BotTriggerSignature(secret, q, "dry-run")
		change(tid, store.BotTriggerDryRun, store.TriggerSpec{}, &q)
		change(tid, store.BotTriggerEnable, store.TriggerSpec{}, nil)
		q.Version = b.Version
		q.Signature = store.BotTriggerSignature(secret, q, "run")
		send(q, "", 401)
		tampered := q
		tampered.Payload = []byte("tampered body")
		send(tampered, owner, 403)
		expired := q
		expired.Timestamp -= 3600
		expired.Signature = store.BotTriggerSignature(secret, expired, "run")
		send(expired, owner, 410)
		run := send(q, owner, 202)
		llm.succeed = false
		execute(run)
		send(q, owner, 409)
		q.EventID = uuid.NewString()
		q.Signature = store.BotTriggerSignature(secret, q, "run")
		run = send(q, owner, 202)
		llm.succeed = true
		execute(run)
		send(q, owner, 409)
		def.Instructions = "Changed hook"
		change(tid, store.BotTriggerSave, def, nil)
		_, err := triggers.ChangeTrigger(auth, store.BotTriggerChange{BotID: b.ID, TriggerID: tid, ExpectedRevision: b.Revision, Action: store.BotTriggerEnable, Reason: "stale proof"})
		if err == nil {
			t.Fatal("modified trigger reused proof")
		}
	}
	tid := uuid.New()
	binding := store.BotChannelBinding{ID: uuid.New(), Channel: "telegram", ChatID: "chat", ThreadID: "thread"}
	change(tid, store.BotTriggerSave, store.TriggerSpec{Kind: store.TriggerChannel, Instructions: "Report channel observation", ChannelBinding: &binding}, nil)
	change(tid, store.BotTriggerEnable, store.TriggerSpec{}, nil)
	commands := channels.DefaultCommands()
	command := fmt.Sprintf("/bot_run personal %s %s", b.ID, tid)
	cc := channels.CommandContext{UserID: owner, ChannelID: binding.Channel, ChatID: binding.ChatID, ThreadID: binding.ThreadID, MessageID: "message"}
	for _, wrong := range []channels.CommandContext{{UserID: "other", ChannelID: binding.Channel, ChatID: binding.ChatID, ThreadID: binding.ThreadID, MessageID: "message"}, {UserID: owner, ChannelID: binding.Channel, ChatID: binding.ChatID, ThreadID: "other", MessageID: "message"}} {
		if _, err := commands.Execute(channelCtx, command, wrong); err == nil {
			t.Fatal("unauthorized channel command admitted")
		}
	}
	response, err := commands.Execute(channelCtx, command, cc)
	if err != nil || !strings.Contains(response, "queued") {
		t.Fatalf("command %q %v", response, err)
	}
	if _, err := commands.Execute(channelCtx, command, cc); err == nil {
		t.Fatal("channel replay admitted")
	}
	runs, err := repo.ListRuns(auth, b.ID, store.BotPage{})
	if err != nil {
		t.Fatal(err)
	}
	for _, run := range runs {
		if run.Trigger == store.TriggerChannel {
			execute(run)
		}
	}
	if len(runs) != 5 {
		t.Fatalf("expected exactly five runs: %+v", runs)
	}
}
