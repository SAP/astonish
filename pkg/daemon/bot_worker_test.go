package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"path/filepath"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/channels"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/launcher"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
)

type workerTestBackend struct{ sandbox.Backend }

func (workerTestBackend) EnsureBotHome(context.Context, sandbox.BotHomeScope) (*sandbox.BotHomeBinding, error) {
	return nil, store.ErrBotUnsupported
}

type workerTestPool struct{ sandbox.ToolNodePool }

func (workerTestPool) SetBotHome(string, *sandbox.BotHomeBinding) error { return nil }

type workerTestModel struct {
	calls int
	owner string
}

func (*workerTestModel) Name() string { return "controlled" }
func (m *workerTestModel) GenerateContent(ctx context.Context, r *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		m.calls++
		m.owner = store.UserIDFromContext(ctx)
		yield(&model.LLMResponse{Content: genai.NewContentFromText("durable controlled result", "model"), TurnComplete: true, UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 7, CandidatesTokenCount: 3, TotalTokenCount: 10}}, nil)
	}
}

type workerEventRepo struct {
	store.BotStore
	b           store.Bot
	r           store.BotRun
	appendEvent func(context.Context, string, *session.Event) error
}

// Controlled sink adapter; atomic database fencing is tested in entstore.
func (r *workerEventRepo) AppendBotRunEvent(ctx context.Context, l store.BotLease, id uuid.UUID, e *session.Event) error {
	if l.Fence != r.r.Lease.Fence || l.WorkerID != r.r.Lease.WorkerID || r.r.Status != store.RunRunning || id != r.r.SessionID {
		return store.ErrBotConflict
	}
	if r.appendEvent == nil {
		return store.ErrBotUnsupported
	}
	return r.appendEvent(ctx, id.String(), e)
}

func (r *workerEventRepo) ListRuns(context.Context, store.BotID, store.BotPage) ([]store.BotRun, error) {
	return []store.BotRun{r.r}, nil
}

// This controlled runtime has no approval checkpoints. History replay still
// queries approvals to project durable question state, so implement the empty
// fixture explicitly rather than invoking the nil embedded BotStore.
func (*workerEventRepo) ListApprovals(context.Context, store.BotID, store.BotPage) ([]store.BotApproval, error) {
	return nil, nil
}

func (r *workerEventRepo) Get(context.Context, store.BotID) (*store.Bot, error) { return &r.b, nil }
func (r *workerEventRepo) GetRun(context.Context, store.BotID, store.RunID) (*store.BotRun, error) {
	return &r.r, nil
}

// The linkage projection is controlled; all transcript/session I/O goes through
// the real SQLite store, not an in-memory transcript fixture.
type workerEventSessions struct {
	store.SessionStore
	meta store.SessionMeta
	fail error
}

func (s workerEventSessions) GetSessionMeta(context.Context, string) (*store.SessionMeta, error) {
	return &s.meta, nil
}
func (s workerEventSessions) AppendFleetEvent(c context.Context, id string, e *session.Event) error {
	if s.fail != nil {
		return s.fail
	}
	return s.SessionStore.AppendFleetEvent(c, id, e)
}

func TestBotWorkerWiredRuntimeDurableEventsAndOwnerPin(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	_, backend, err := entstore.NewPlatformServices(ctx, entstore.Config{DSN: filepath.Join(dir, "platform.db"), DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	org := &store.Organization{ID: uuid.NewString(), Slug: "worker-org", Name: "worker", Status: "active"}
	if err := backend.Organizations().Create(ctx, org); err != nil {
		t.Fatal(err)
	}
	if err := backend.ProvisionOrg(ctx, org.ID, org.Slug); err != nil {
		t.Fatal(err)
	}
	ods, err := backend.ForOrg(org.Slug)
	if err != nil {
		t.Fatal(err)
	}
	team := &store.Team{ID: uuid.NewString(), Slug: "worker-team", Name: "worker", SchemaName: "team_worker-team"}
	if err := ods.Teams().CreateTeam(ctx, team); err != nil {
		t.Fatal(err)
	}
	owner := uuid.New()
	if err := backend.Users().Create(ctx, &store.User{ID: owner.String(), Email: owner.String() + "@example.test", DisplayName: "owner", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	if err := backend.Organizations().AddMember(ctx, owner.String(), org.ID, "member"); err != nil {
		t.Fatal(err)
	}
	if err := ods.Teams().AddMember(ctx, &store.TeamMembership{UserID: owner.String(), TeamID: team.ID, Role: "member"}); err != nil {
		t.Fatal(err)
	}
	llm := &workerTestModel{}
	cleaned := 0
	var eventRepo *workerEventRepo
	w, err := NewBotWorker(BotWorkerConfig{Backend: backend, Sandbox: workerTestBackend{}, Pool: workerTestPool{}, WorkerID: "controlled-worker", ContextWindow: 8192, ModelOptions: provider.BudgetLLMOptions{MaxOutputTokens: 100, SupportsOutputLimit: true}, ResolveModel: func(c context.Context, p execution.Principal, scope store.BotScope, providerName, modelName string) (model.LLM, error) {
		if p.Subject != owner.String() || scope != store.BotScopeTeam || providerName != "home-provider" || modelName != "home-model" {
			t.Fatalf("wrong pin/owner: %+v %s/%s", p, providerName, modelName)
		}
		return llm, nil
	}, Prepare: func(_ context.Context, _ bot.RunRequest, scope *launcher.BotChatScope) (*launcher.BotChatSafeResources, error) {
		scope.Services.Bots = eventRepo
		return &launcher.BotChatSafeResources{WorkspaceDir: "/workspace", PolicyRuntime: agent.BotPolicyRuntime{Guard: workerTestGuard{}, Classifier: botUnknownClassifier{}}, VerifyIsolation: func(context.Context) error { return nil }, Cleanup: func() { cleaned++ }}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	type viewerKey struct{}
	viewer := store.WithUserID(context.WithValue(ctx, viewerKey{}, "viewer secret"), uuid.NewString())
	auth, _, err := w.authenticateOwner(viewer, BotSchedulerScope{org.Slug, team.Slug, owner.String()})
	if err != nil {
		t.Fatal(err)
	}
	if auth.Value(viewerKey{}) != nil || store.UserIDFromContext(auth) != owner.String() {
		t.Fatal("viewer ambient scope survived")
	}
	principal, _ := execution.PrincipalFromContext(auth)
	ss := ods.ForTeam(team.Slug).Sessions()
	homeID, sessionID := uuid.New(), uuid.New()
	for _, id := range []uuid.UUID{homeID, sessionID} {
		if _, err := ss.Create(auth, &session.CreateRequest{AppName: "astonish", UserID: owner.String(), SessionID: id.String()}); err != nil {
			t.Fatal(err)
		}
	}
	if err := ods.ForTeam(team.Slug).SetSessionPin(auth, homeID.String(), "home-provider", "home-model"); err != nil {
		t.Fatal(err)
	}
	if err := ods.ForTeam(team.Slug).SetSessionPin(auth, sessionID.String(), "wrong-provider", "wrong-model"); err != nil {
		t.Fatal(err)
	}
	botID, runID := uuid.New(), uuid.New()
	identity := execution.BotRunIdentity{BotID: botID, RunID: runID, Version: 1, ActorID: owner.String(), OrgSlug: org.Slug, TeamSlug: team.Slug}
	policy, err := execution.NewRunPolicy(execution.RunPolicySpec{Identity: identity, Level: execution.AutonomySupervised})
	if err != nil {
		t.Fatal(err)
	}
	version := store.DefaultBotVersion()
	version.BotID = botID
	budget, err := bot.NewRunBudget(version.Budget, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	req := bot.RunRequest{Bot: store.Bot{ID: botID, OwnerID: owner, Scope: store.BotScopeTeam, HomeSessionID: homeID, Status: store.BotActive, Version: 1}, Version: version, Run: store.BotRun{ID: runID, BotID: botID, Version: 1, SessionID: sessionID, Status: store.RunRunning, Lease: store.BotLease{BotID: botID, RunID: runID, Fence: 1, WorkerID: "controlled-worker", ExpiresAt: time.Now().Add(time.Minute)}}, Principal: principal, Policy: policy, Budget: budget, Instructions: "Report the controlled observation."}
	repo := &workerEventRepo{b: req.Bot, r: req.Run}
	eventRepo = repo
	projected := workerEventSessions{SessionStore: ss, meta: store.SessionMeta{ID: sessionID.String(), UserID: owner.String(), BotID: botID.String(), BotRunID: runID.String()}}
	repo.appendEvent = func(c context.Context, id string, e *session.Event) error {
		return projected.AppendFleetEvent(c, id, e)
	}
	published := 0
	sink := NewBotDurableEventSink(repo, projected, func(c context.Context, e bot.RuntimeEvent) error {
		events, err := ss.ReadTranscriptEvents(c, "astonish", owner.String(), sessionID.String())
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, event := range events {
			if raw, ok := event.Actions.StateDelta["bot_runtime_event"]; ok {
				encoded, _ := json.Marshal(raw)
				var actual bot.RuntimeEvent
				if json.Unmarshal(encoded, &actual) == nil && actual.Sequence == e.Sequence && actual.TurnID == e.TurnID {
					found = true
				}
			}
		}
		if !found {
			t.Fatal("publication preceded durable transcript append")
		}
		published++
		return nil
	})
	result, err := w.runtime.Start(auth, req, sink)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != store.RunSucceeded || result.Report.Summary != "durable controlled result" || published == 0 || cleaned != 1 || llm.owner != owner.String() {
		t.Fatalf("result=%+v published=%d cleanup=%d", result, published, cleaned)
	}
	if result.Usage.InputTokens != int64(llm.calls*7) || result.Usage.OutputTokens != int64(llm.calls*3) || result.Usage.CostMicros != nil {
		t.Fatalf("shared metering=%+v calls=%d", result.Usage, llm.calls)
	}
	beforeReplay := llm.calls
	assertWorkerHistoryHTTP(t, auth, repo, projected)
	if llm.calls != beforeReplay {
		t.Fatal("history replay executed model")
	}
	// Storage failure and mismatched event identity cannot publish.
	eventCtx := execution.WithBotRunIdentity(context.WithValue(auth, botEventLeaseKey{}, req.Run.Lease), identity)
	event := bot.RuntimeEvent{RunID: runID, SessionID: sessionID, TurnID: uuid.New(), Sequence: 1, Payload: json.RawMessage(`{"type":"message"}`)}
	storageFailure := errors.New("storage unavailable")
	projected.fail = storageFailure
	if err := NewBotDurableEventSink(repo, projected, func(context.Context, bot.RuntimeEvent) error { t.Fatal("published on failure"); return nil })(eventCtx, event); !errors.Is(err, storageFailure) {
		t.Fatal(err)
	}
	repo.r.Lease.Fence++
	if err := sink(eventCtx, event); !errors.Is(err, store.ErrBotConflict) {
		t.Fatalf("stale event fence: %v", err)
	}
	repo.r.Lease.Fence--
	event.RunID = uuid.New()
	if err := sink(eventCtx, event); !errors.Is(err, store.ErrBotForbidden) {
		t.Fatal(err)
	}
}

type workerTestGuard struct{}

func (workerTestGuard) Authorize(context.Context, execution.ActionDescriptor) (execution.PolicyDecision, error) {
	return execution.PolicyDecision{}, store.ErrBotForbidden
}

func TestBotWorkerRequiresIsolation(t *testing.T) {
	if _, err := NewBotWorker(BotWorkerConfig{}); !errors.Is(err, store.ErrBotUnsupported) {
		t.Fatal(err)
	}
}

// The repository models the durable claim contract; real SQLite publication and
// transcript idempotency are additionally exercised by the HTTP runtime journey.
type workerPublicationRepo struct {
	publicationReconcileStore
	bots                    []store.Bot
	n                       store.BotNotification
	failPublish, failFinish error
	finishes                []store.BotNotificationFinish
}

func (s *workerPublicationRepo) AuthorizeBotDelivery(ctx context.Context, id store.BotID, target store.BotChannelBinding) error {
	p, ok := execution.PrincipalFromContext(ctx)
	if !ok || len(s.bots) == 0 || p.Subject != s.bots[0].OwnerID.String() || id != s.bots[0].ID || target != s.n.Target {
		return store.ErrBotForbidden
	}
	return nil
}

func (s *workerPublicationRepo) List(context.Context, store.BotPage) ([]store.Bot, error) {
	return s.bots, nil
}
func (s *workerPublicationRepo) ListRuns(context.Context, store.BotID, store.BotPage) ([]store.BotRun, error) {
	return nil, nil
}
func (s *workerPublicationRepo) PublishReport(_ context.Context, q store.BotPublicationRequest) (*store.BotPublication, error) {
	if s.failPublish != nil {
		return nil, s.failPublish
	}
	s.seen = append(s.seen, q)
	return &store.BotPublication{BotID: q.BotID, RunID: q.RunID, ReportSequence: q.ReportSequence, Notifications: []store.BotNotification{s.n}}, nil
}
func (s *workerPublicationRepo) ClaimNotification(context.Context, store.BotNotificationClaim) (*store.BotNotification, bool, error) {
	if s.n.State != store.BotNotificationPending {
		return &s.n, false, nil
	}
	s.n.State = store.BotNotificationUncertain
	s.n.AttemptID = uuid.New()
	return &s.n, true, nil
}
func (s *workerPublicationRepo) FinishNotification(_ context.Context, q store.BotNotificationFinish) error {
	s.finishes = append(s.finishes, q)
	return s.failFinish
}

type workerCaptureChannel struct {
	channels.Channel
	t        *testing.T
	targets  []channels.Target
	messages []channels.OutboundMessage
	err      error
}

func (*workerCaptureChannel) ID() string { return "controlled" }
func (c *workerCaptureChannel) Send(_ context.Context, target channels.Target, message channels.OutboundMessage) error {
	c.targets = append(c.targets, target)
	c.messages = append(c.messages, message)
	return c.err
}
func (c *workerCaptureChannel) BroadcastTargets() []channels.Target {
	c.t.Fatal("Bot publication broadcast")
	return nil
}

func TestBotWorkerPublicationIdlePollAndUncertainReplay(t *testing.T) {
	for _, failure := range []string{"none", "before-publication", "transport", "after-send"} {
		t.Run(failure, func(t *testing.T) {
			owner, id, run := uuid.New(), uuid.New(), uuid.New()
			ctx, err := execution.WithPrincipal(context.Background(), execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: owner.String(), OrgSlug: "org", TeamSlug: "team", Authenticated: true})
			if err != nil {
				t.Fatal(err)
			}
			s := &workerPublicationRepo{bots: []store.Bot{{ID: id, OwnerID: owner, Status: store.BotActive}, {ID: uuid.New(), OwnerID: uuid.New(), Status: store.BotActive}}, n: store.BotNotification{Target: store.BotChannelBinding{ID: uuid.New(), Channel: "controlled", ChatID: "exact-chat", ThreadID: "exact-thread"}, Decision: store.NotificationSend, State: store.BotNotificationPending, Text: "committed report"}}
			s.activities = []store.BotActivity{{BotID: id, RunID: &run, Sequence: 1, Kind: store.BotActivityReport}}
			s.activities[0].Payload, _ = json.Marshal(map[string]any{"intent": store.BotOutboxPublish})
			capture := &workerCaptureChannel{t: t}
			manager := channels.NewChannelManager(nil, nil, nil, nil)
			manager.Register(capture)
			var live *channels.ChannelManager
			w := &BotWorker{config: BotWorkerConfig{ChannelManager: func() *channels.ChannelManager { return live }}}
			if err := w.pollStore(context.Background(), s, nil); !errors.Is(err, store.ErrBotForbidden) {
				t.Fatalf("unauthenticated poll: %v", err)
			}
			if len(s.seen) != 0 {
				t.Fatal("unauthenticated publication")
			}
			// With channels absent, publish the report but leave notification pending.
			if err := w.pollStore(ctx, s, nil); err != nil {
				t.Fatal(err)
			}
			if len(s.seen) != 1 || s.n.State != store.BotNotificationPending {
				t.Fatalf("idle publication=%+v state=%s", s.seen, s.n.State)
			}
			live = manager
			fault := errors.New("controlled publication fault")
			switch failure {
			case "before-publication":
				s.failPublish = fault
			case "transport":
				capture.err = fault
			case "after-send":
				s.failFinish = fault
			}
			err = w.pollStore(ctx, s, nil)
			if failure == "none" && err != nil || failure != "none" && !errors.Is(err, fault) {
				t.Fatalf("failure contract: %v", err)
			}
			if failure == "before-publication" && len(capture.targets) != 0 {
				t.Fatal("sent before publication committed")
			}
			s.failPublish, s.failFinish, capture.err = nil, nil, nil
			// A paused Bot still reconciles committed publications, never runs work.
			s.bots[0].Status = store.BotPaused
			for i := 0; i < 2; i++ {
				if err := w.pollStore(ctx, s, nil); err != nil {
					t.Fatal(err)
				}
			}
			if len(capture.targets) != 1 {
				t.Fatalf("duplicate/missing send: %+v", capture.targets)
			}
			if got := capture.targets[0]; got.ChannelID != "controlled" || got.ChatID != "exact-chat" || got.ThreadID != "exact-thread" {
				t.Fatalf("wrong target: %+v", got)
			}
			if capture.messages[0].ThreadID != "exact-thread" || capture.messages[0].Text != "committed report" {
				t.Fatalf("wrong message: %+v", capture.messages)
			}
			if len(s.finishes) != 1 || s.finishes[0].Delivered || s.finishes[0].Receipt != "" || s.n.State != store.BotNotificationUncertain {
				t.Fatalf("fabricated delivery proof: %+v", s.finishes)
			}
			for _, q := range s.seen {
				if q.BotID != id {
					t.Fatal("published another owner's Bot")
				}
			}
		})
	}
}

func TestBotWorkerChannelNotifierUsesLiveManager(t *testing.T) {
	first, second := &workerCaptureChannel{t: t}, &workerCaptureChannel{t: t}
	live := channels.NewChannelManager(nil, nil, nil, nil)
	live.Register(first)
	n := botChannelNotifier{getManager: func() *channels.ChannelManager { return live }}
	q := bot.NotificationRequest{ID: uuid.New(), Decision: store.NotificationSend, Target: store.BotChannelBinding{ID: uuid.New(), Channel: "controlled", ChatID: "exact", ThreadID: "thread"}, Body: "report"}
	n.authority = notificationAuthorityFunc(func(_ context.Context, _ store.BotID, target store.BotChannelBinding) error {
		if target != q.Target {
			return store.ErrBotForbidden
		}
		return nil
	})
	for i := 0; i < 2; i++ {
		r, err := n.Send(context.Background(), q)
		if err != nil || !r.Uncertain || r.ProviderID != "" || r.DeliveredAt != nil {
			t.Fatalf("receipt=%+v error=%v", r, err)
		}
		live = channels.NewChannelManager(nil, nil, nil, nil)
		live.Register(second)
	}
	if len(first.targets) != 1 || len(second.targets) != 1 {
		t.Fatal("retained stale manager")
	}
	q.Target.ChatID = ""
	if _, err := n.Send(context.Background(), q); !errors.Is(err, store.ErrBotInvalid) {
		t.Fatal(err)
	}
}
