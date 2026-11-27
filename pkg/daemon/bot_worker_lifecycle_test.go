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
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/launcher"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/gorilla/mux"
	"google.golang.org/adk/model"
	"google.golang.org/genai"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/google/uuid"
)

// reviewedRecoveryReply supplies a controlled text reply for lifecycle tests
// without a model, following the persisted protocol (including historical review).
func reviewedRecoveryReply(t *testing.T, repo store.BotStore, ctx context.Context, lease store.BotLease, text string) store.BotReport {
	t.Helper()
	switch text {
	case "Home ready", "review ready", "No tools needed":
		// Closed lifecycle acknowledgments; no outside fact or applied control.
	default:
		t.Fatalf("unsupported lifecycle fixture reply %q", text)
	}
	ws := repo.(store.BotWorkStore)
	w, err := ws.LoadWork(ctx, lease)
	if err != nil {
		t.Fatal(err)
	}
	save := func(n *store.BotWorkState) {
		t.Helper()
		w, err = ws.SaveWork(ctx, store.BotWorkUpdate{Lease: lease, ExpectedRevision: w.Revision, State: *n})
		if err != nil {
			t.Fatal(err)
		}
	}
	n := w.Clone()
	if w.Protocol == store.BotWorkProtocolAgentLedV1 {
		n.Phase = store.BotWorkFinished
		n.AgentWork.Disposition = store.AgentWorkAnswered
		n.Candidate = &store.BotWorkCandidate{Revision: 1, EventID: uuid.NewString(), Text: text}
		n.Candidate.Digest = n.Candidate.ContentDigest()
		if n.EmployeeReviewPolicy == store.BotEmployeeReviewEvidenceBoundV1 {
			sources := []string{"assignment", "charter"}
			for _, goal := range n.Contract.Goals {
				sources = append(sources, "goal:"+goal.ID.String())
			}
			n.Criteria = []store.BotWorkCriterion{{ID: "reply", SourceIDs: sources, Text: "Return the controlled lifecycle acknowledgment without claiming external action"}}
			n.Review = &store.BotWorkReview{CandidateDigest: n.Candidate.Digest, Criteria: []store.BotCriterionAssessment{{CriterionID: "reply", Verdict: store.BotCriterionSatisfied, Method: store.BotAssessmentModel, EvidenceRefs: []string{"candidate:" + n.Candidate.Digest}, Explanation: "Literal lifecycle fixture acknowledgment; no external fact, tool effect, or proposal application claimed."}}}
		}
		save(n)
		report := store.BotReport{Summary: text, Work: store.BuildBotWorkProjection(w)}
		message, e := store.NewBotEmployeeAgentMessage(store.RunSucceeded, report, w)
		if e != nil {
			t.Fatal(e)
		}
		report.EmployeeMessage = &message
		return report
	}
	// Only preexisting legacy work follows the historical assessment transitions.
	n.Phase = store.BotWorkProducing
	n.Criteria = []store.BotWorkCriterion{{ID: "reply", SourceIDs: []string{"assignment", "charter"}, Text: "Reply without tools"}}
	save(n)
	n = w.Clone()
	n.Phase = store.BotWorkCandidateReady
	n.Candidate = &store.BotWorkCandidate{Revision: 1, EventID: uuid.NewString(), Text: text}
	n.Candidate.Digest = n.Candidate.ContentDigest()
	save(n)
	n = w.Clone()
	n.Phase = store.BotWorkReviewing
	save(n)
	n = w.Clone()
	n.Phase = store.BotWorkAccepted
	n.Review = &store.BotWorkReview{CandidateDigest: w.Candidate.Digest, Criteria: []store.BotCriterionAssessment{{CriterionID: "reply", Verdict: store.BotCriterionSatisfied, Method: store.BotAssessmentModel, EvidenceRefs: []string{"candidate:" + w.Candidate.Digest}, Explanation: "Controlled tool-free reply"}}}
	save(n)
	return store.BotReport{Summary: text, Quality: &store.BotQualityReport{SchemaVersion: w.SchemaVersion, WorkRevision: w.Revision, RequirementsDigest: w.RequirementsDigest, CandidateDigest: w.Candidate.Digest, Outcome: w.Phase, Criteria: w.Review.Criteria}}
}

func TestBotWorkerResumedBudgetExhausted(t *testing.T) {
	cap := int64(4)
	started := time.Now().Add(-time.Second)
	in := store.BotRunInput{Version: store.DefaultBotVersion(), Run: store.BotRun{StartedAt: &started, Lease: store.BotLease{Fence: 1}, Checkpoint: []byte(`{}`), Usage: store.BotUsage{ToolCalls: cap, RuntimeNanos: int64(time.Second)}}}
	in.Version.Budget.MaxToolCalls = &cap
	b, err := botRunBudget(in, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := b.ReserveTool(context.Background(), "read"); err != nil || b.Usage().ToolCalls != cap+1 {
		t.Fatalf("historic tool cap reinstated or ledger reset: %v", err)
	}
	in.Run.Usage = store.BotUsage{RuntimeNanos: int64(in.Version.Budget.MaxRuntimeSeconds) * int64(time.Second)}
	b, err = botRunBudget(in, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Check(context.Background()); err != nil || !b.Deadline().IsZero() || b.Usage().RuntimeNanos < in.Run.Usage.RuntimeNanos {
		t.Fatalf("historic runtime cap reinstated or ledger reset: %v", err)
	}
}

func TestBotWorkerPollRecoversCrashAfterDatabaseReopen(t *testing.T) {
	testBotWorkerRecoveryAfterReopen(t, false)
}

func TestBotWorkerPauseRecoveryAfterDatabaseReopen(t *testing.T) {
	testBotWorkerRecoveryAfterReopen(t, true)
}

// Isolate polling of the crashed writer; admission of the queued conversation
// below uses the real scoped store and fencing transaction.
type interruptedRecoveryPollStore struct {
	store.BotStore
	store.BotPublicationStore
	store.BotPublicationMemoryStore
	store.BotKnowledgeExtractionStore
}

func (s interruptedRecoveryPollStore) ListRuns(ctx context.Context, id store.BotID, page store.BotPage) ([]store.BotRun, error) {
	runs, err := s.BotStore.ListRuns(ctx, id, page)
	var interrupted []store.BotRun
	for _, run := range runs {
		if run.Status == store.RunInterrupted {
			interrupted = append(interrupted, run)
		}
	}
	return interrupted, err
}

func (s interruptedRecoveryPollStore) ConfirmCleanup(ctx context.Context, q store.BotCleanupRequest) error {
	return s.BotStore.(store.BotCleanupStore).ConfirmCleanup(ctx, q)
}

func testBotWorkerRecoveryAfterReopen(t *testing.T, paused bool) {
	for _, scope := range []store.BotScope{store.BotScopeTeam, store.BotScopePersonal} {
		t.Run(string(scope), func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			cfg := entstore.Config{DSN: filepath.Join(dir, "platform.db"), DataDir: dir}
			_, backend, err := entstore.NewPlatformServices(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = backend.Close() }()
			org := &store.Organization{ID: uuid.NewString(), Slug: "recovery", Name: "Recovery", Status: "active"}
			if err := backend.Organizations().Create(ctx, org); err != nil {
				t.Fatal(err)
			}
			if err := backend.ProvisionOrg(ctx, org.ID, org.Slug); err != nil {
				t.Fatal(err)
			}
			data, err := backend.ForOrg(org.Slug)
			if err != nil {
				t.Fatal(err)
			}
			team := &store.Team{ID: uuid.NewString(), Slug: "team", Name: "Team", SchemaName: "team_team"}
			if err := data.Teams().CreateTeam(ctx, team); err != nil {
				t.Fatal(err)
			}
			owner := uuid.New()
			if err := backend.Users().Create(ctx, &store.User{ID: owner.String(), Email: owner.String() + "@test.invalid", DisplayName: "Owner", Status: "active"}); err != nil {
				t.Fatal(err)
			}
			if err := backend.Organizations().AddMember(ctx, owner.String(), org.ID, "member"); err != nil {
				t.Fatal(err)
			}
			if err := data.Teams().AddMember(ctx, &store.TeamMembership{UserID: owner.String(), TeamID: team.ID, Role: "member"}); err != nil {
				t.Fatal(err)
			}
			homeBackend := testRetainedContentBackend(t, backend)
			w := &BotWorker{config: BotWorkerConfig{Backend: backend, WorkerID: "replacement", Sandbox: homeBackend, Pool: workerTestPool{}}}
			auth, _, err := w.authenticateOwner(ctx, BotSchedulerScope{org.Slug, team.Slug, owner.String()})
			if err != nil {
				t.Fatal(err)
			}
			repo := data.ForTeam(team.Slug).Bots()
			if scope == store.BotScopePersonal {
				repo = data.ForUser(owner.String()).Bots()
			}
			v := store.DefaultBotVersion()
			v.Charter = "Recover without repeating uncertain work"
			b, err := repo.Create(auth, store.BotCreateRequest{Name: "Recovery", OwnerID: owner, Version: v, IdempotencyKey: "recovery"})
			if err != nil {
				t.Fatal(err)
			}
			// Controlled readiness is test-only; all lifecycle and recovery writes use
			// the real owner-authorized repository.
			backend.SetBotActivationReadiness(func(context.Context, store.Bot, store.BotVersion) error { return nil })
			b, err = w.provisionHome(auth, repo, b.ID, b.Revision)
			if err != nil {
				t.Fatal(err)
			}
			home := b.Home
			b, err = repo.Get(auth, b.ID)
			if err != nil {
				t.Fatal(err)
			}
			r, err := repo.RequestRun(auth, store.BotRunRequest{BotID: b.ID, Version: b.Version, Trigger: store.TriggerManual, Instructions: "Do not replay", IdempotencyKey: "crash"})
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now()
			workerCtx := execution.WithBotRunIdentity(auth, execution.BotRunIdentity{BotID: b.ID, RunID: r.ID, Version: b.Version, ActorID: owner.String(), OrgSlug: org.Slug, TeamSlug: team.Slug})
			lease, err := repo.ClaimRun(workerCtx, store.BotClaimRequest{BotID: b.ID, RunID: r.ID, WorkerID: "crashed", Now: now, ExpiresAt: now.Add(500 * time.Millisecond)})
			if err != nil {
				t.Fatal(err)
			}
			var conversation *store.BotRun
			if paused {
				b, err = repo.Pause(auth, store.BotLifecycleRequest{BotID: b.ID, ActorID: owner, ExpectedRevision: b.Revision, Reason: "pause during execution"})
				if err != nil {
					t.Fatal(err)
				}
				conversation, err = repo.RequestRun(auth, store.BotRunRequest{BotID: b.ID, Version: b.Version, Intent: store.BotIntentConversation, Trigger: store.TriggerManual, Instructions: "Reply without tools", IdempotencyKey: "paused-conversation"})
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := backend.Close(); err != nil {
				t.Fatal(err)
			}
			_, backend, err = entstore.NewPlatformServices(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			if err := backend.InstallBotContentHomeBackend(homeBackend); err != nil {
				t.Fatal(err)
			}
			w = &BotWorker{config: BotWorkerConfig{Backend: backend, WorkerID: "replacement"}}
			if delay := time.Until(lease.ExpiresAt); delay > 0 {
				time.Sleep(delay + 10*time.Millisecond)
			}
			if paused {
				auth, data, err = w.authenticateOwner(ctx, BotSchedulerScope{org.Slug, team.Slug, owner.String()})
				if err != nil {
					t.Fatal(err)
				}
				repo = data.ForTeam(team.Slug).Bots()
				if scope == store.BotScopePersonal {
					repo = data.ForUser(owner.String()).Bots()
				}
				physical := &cancellationReceiptBackend{err: errors.New("physical writer still exists")}
				w.config.Sandbox = &regressionCancellationBackend{regressionHomeBackend: homeBackend, receipt: physical}
				conversationCtx := execution.WithBotRunIdentity(auth, execution.BotRunIdentity{BotID: b.ID, RunID: conversation.ID, Version: b.Version, ActorID: owner.String(), OrgSlug: org.Slug, TeamSlug: team.Slug})
				claim := store.BotClaimRequest{BotID: b.ID, RunID: conversation.ID, WorkerID: "replacement", ExpiresAt: time.Now().Add(time.Minute)}
				pollRepo := interruptedRecoveryPollStore{BotStore: repo, BotPublicationStore: repo.(store.BotPublicationStore), BotPublicationMemoryStore: repo.(store.BotPublicationMemoryStore), BotKnowledgeExtractionStore: repo.(store.BotKnowledgeExtractionStore)}
				if err = w.pollStore(auth, pollRepo, nil); err == nil {
					t.Fatal("missing physical evidence not reported")
				}
				if _, err = repo.ClaimRun(conversationCtx, claim); !errors.Is(err, store.ErrBotConflict) {
					t.Fatalf("claim before physical receipt: %v", err)
				}
				physical.err = nil
				if err = w.pollStore(auth, pollRepo, nil); err != nil {
					t.Fatal(err)
				}
				if physical.session != r.SessionID.String() {
					t.Fatal("restart did not verify exact interrupted physical session")
				}
				newLease, err := repo.ClaimRun(conversationCtx, claim)
				if err != nil {
					t.Fatalf("paused conversation blocked after verified restart recovery: %v", err)
				}
				if newLease.Fence <= lease.Fence {
					t.Fatal("fence not advanced")
				}
				if err = repo.CommitRun(workerCtx, store.BotRunCommit{Lease: lease, Status: store.RunSucceeded}); err == nil {
					t.Fatal("stale worker committed")
				}
				if err = repo.CommitRun(conversationCtx, store.BotRunCommit{Lease: newLease, Status: store.RunSucceeded, Report: reviewedRecoveryReply(t, repo, conversationCtx, newLease, "No tools needed")}); err != nil {
					t.Fatal(err)
				}
				final, err := repo.Get(auth, b.ID)
				if err != nil || final.Status != store.BotPaused || final.Home != home {
					t.Fatalf("changed paused home: %+v %v", final, err)
				}
				return
			}
			if err := w.Poll(ctx); err != nil {
				t.Fatal(err)
			}
			auth, data, err = w.authenticateOwner(ctx, BotSchedulerScope{org.Slug, team.Slug, owner.String()})
			if err != nil {
				t.Fatal(err)
			}
			repo = data.ForTeam(team.Slug).Bots()
			if scope == store.BotScopePersonal {
				repo = data.ForUser(owner.String()).Bots()
			}
			saved, err := repo.GetRun(auth, b.ID, r.ID)
			if err != nil || saved.Status != store.RunInterrupted || len(saved.Report.Unverified) == 0 {
				t.Fatalf("crashed attempt not recovered: %+v %v", saved, err)
			}
			if err := repo.CommitRun(auth, store.BotRunCommit{Lease: lease, Status: store.RunSucceeded}); err == nil {
				t.Fatal("late crashed worker committed")
			}
			if err := w.Poll(ctx); err != nil {
				t.Fatal(err)
			}
			saved, err = repo.GetRun(auth, b.ID, r.ID)
			if err != nil || saved.Status != store.RunInterrupted || saved.Lease.Fence != lease.Fence {
				t.Fatalf("recovered run retried: %+v %v", saved, err)
			}
			next, err := repo.RequestRun(auth, store.BotRunRequest{BotID: b.ID, Version: b.Version, Trigger: store.TriggerManual, Instructions: "Explicit reviewed work", IdempotencyKey: "next"})
			if err != nil {
				t.Fatal(err)
			}
			now = time.Now()
			workerCtx = execution.WithBotRunIdentity(auth, execution.BotRunIdentity{BotID: b.ID, RunID: next.ID, Version: b.Version, ActorID: owner.String(), OrgSlug: org.Slug, TeamSlug: team.Slug})
			if _, err := repo.ClaimRun(workerCtx, store.BotClaimRequest{BotID: b.ID, RunID: next.ID, WorkerID: "replacement", Now: now, ExpiresAt: now.Add(time.Minute)}); err != nil {
				t.Fatalf("expired active-run lock retained: %v", err)
			}
		})
	}
}

// promptDispatchDirectory blocks only the maintenance generation, not fresh owner
// authentication or durable fallback discovery.
type promptDispatchDirectory struct {
	store.OrganizationStore
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (d *promptDispatchDirectory) List(ctx context.Context) ([]*store.Organization, error) {
	if mode, _ := ctx.Value(botScanModeKey{}).(botScanMode); mode == botScanMaintenance {
		d.once.Do(func() { close(d.entered) })
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-d.release:
		}
	}
	return d.OrganizationStore.List(ctx)
}

type promptDispatchBackend struct {
	store.PlatformBackend
	directory *promptDispatchDirectory
}

func (b promptDispatchBackend) Organizations() store.OrganizationStore { return b.directory }

type promptDispatchModel struct {
	calls   atomic.Int64
	entered chan *model.LLMRequest
	release chan struct{}
}

func (*promptDispatchModel) Name() string { return "controlled" }
func (m *promptDispatchModel) GenerateContent(ctx context.Context, r *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		m.calls.Add(1)
		select {
		case m.entered <- r:
		case <-ctx.Done():
			yield(nil, ctx.Err())
			return
		}
		select {
		case <-m.release:
		case <-ctx.Done():
			yield(nil, ctx.Err())
			return
		}
		yield(&model.LLMResponse{Content: genai.NewContentFromText("The requested observation is complete.", "model"), TurnComplete: true}, nil)
	}
}

// Real loopback HTTP admission -> committed SQLite -> worker Start -> Supervisor
// claim -> shared Chat model. No production daemon, paid model or real Bot is used.
func TestBotPromptDispatchMaintenanceBlocked(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			backend, owner := setupChannelResolverFixture(t, "prompt-dispatch", "team", "telegram", "linked")
			physical := testRetainedContentBackend(t, backend)
			directory := &promptDispatchDirectory{OrganizationStore: backend.Organizations(), entered: make(chan struct{}), release: make(chan struct{})}
			routed := promptDispatchBackend{PlatformBackend: backend, directory: directory}
			resolver := &channelPlatformResolver{backend: backend}
			auth, _, _, err := resolver.ResolveChannelUser(context.Background(), "telegram", "linked")
			if err != nil {
				t.Fatal(err)
			}
			principal, _ := execution.PrincipalFromContext(auth)
			principal.Authentication = execution.AuthMethodPlatformJWT
			principal.Surface = execution.SurfaceStudio
			auth, err = execution.WithPrincipal(auth, principal)
			if err != nil {
				t.Fatal(err)
			}
			auth, err = execution.WithAuthenticationEvidence(auth, execution.AuthenticationEvidence{Method: execution.AuthMethodPlatformJWT, ExpiresAt: time.Now().Add(time.Hour)})
			if err != nil {
				t.Fatal(err)
			}
			org, err := backend.ForOrg("prompt-dispatch")
			if err != nil {
				t.Fatal(err)
			}
			repo := store.BotStoreFromContext(auth, scope)
			pin := org.ForTeam("team").SetSessionPin
			if scope == store.BotScopePersonal {
				pin = org.ForUser(owner).SetSessionPin
			}
			backgroundEntered := make(chan struct{})
			backgroundRelease := make(chan struct{})
			llm := &promptDispatchModel{entered: make(chan *model.LLMRequest, 4), release: make(chan struct{})}
			var backgroundID store.BotID
			registry := execution.NewBotTurnAuthorizationRegistry(32)
			defer registry.Close()
			worker, err := NewBotWorker(BotWorkerConfig{Backend: routed, Sandbox: physical, Pool: workerTestPool{}, WorkerID: "prompt-http", PollInterval: time.Hour, TurnRegistry: registry, ModelOptions: provider.BudgetLLMOptions{MaxOutputTokens: 100, SupportsOutputLimit: true, EstimateInput: func(context.Context, *model.LLMRequest) (int64, error) { return 100, nil }}, MandatePolicy: authorizedHTTPPolicy, ContextWindow: 8192, ResolveModel: func(context.Context, execution.Principal, store.BotScope, string, string) (model.LLM, error) {
				return llm, nil
			}, Prepare: func(c context.Context, q bot.RunRequest, s *launcher.BotChatScope) (*launcher.BotChatSafeResources, error) {
				if q.Bot.ID == backgroundID {
					close(backgroundEntered)
					select {
					case <-backgroundRelease:
					case <-c.Done():
						return nil, c.Err()
					}
				}
				s.Services.BotFilesFactory = func(c context.Context, svc *store.Services, scope store.BotScope) (store.BotFiles, error) {
					return productionBotFiles(c, physical, svc, scope)
				}
				return &launcher.BotChatSafeResources{ChatConfig: &config.AppConfig{}, Catalog: &launcher.ChatCatalogResources{Pool: &titleSkillPool{}}, WorkspaceDir: "/workspace", PolicyRuntime: agent.BotPolicyRuntime{Guard: botLiveGuard{repo: repo, request: q}, Classifier: launcher.BotCatalogueClassifier{}}, VerifyIsolation: func(context.Context) error { return nil }, Cleanup: func() {}}, nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			defer worker.Stop()
			backend.(store.BotActivationInstaller).SetBotActivationReadiness(func(context.Context, store.Bot, store.BotVersion) error { return nil })
			create := func(name string) *store.Bot {
				t.Helper()
				v := store.DefaultBotVersion()
				v.Charter = "Report observations"
				v.Budget.MaxTokens = nil
				b, e := repo.Create(auth, store.BotCreateRequest{Name: name, OwnerID: uuid.MustParse(owner), Version: v, IdempotencyKey: name})
				if e != nil {
					t.Fatal(e)
				}
				b, e = worker.provisionHome(auth, repo, b.ID, b.Revision)
				if e != nil {
					t.Fatal(e)
				}
				if e = pin(auth, b.HomeSessionID.String(), "controlled", "model"); e != nil {
					t.Fatal(e)
				}
				return b
			}
			background := create("background")
			backgroundID = background.ID
			background, err = repo.Get(auth, background.ID)
			if err != nil {
				t.Fatal(err)
			}
			_, err = repo.RequestRun(auth, store.BotRunRequest{BotID: background.ID, Version: background.Version, Trigger: store.TriggerManual, IdempotencyKey: "background", Instructions: "Hold background observation"})
			if err != nil {
				t.Fatal(err)
			}
			target := create("owner")
			svc := &store.Services{Mode: store.ModePlatform, TenantRouter: backend, BotMandatePolicy: authorizedHTTPPolicy, BotHomeSetup: worker.provisionHome, BotRunStateNotify: worker.NotifyRunState, BotTurnRegistry: worker.config.TurnRegistry}
			setBotInteractiveReady(svc, worker)
			httpCtx := store.WithTenantContext(auth, &store.TenantContext{UserID: owner, OrgSlug: "prompt-dispatch", TeamSlug: "team"})
			router := mux.NewRouter()
			api.RegisterRoutes(router, svc, backend, entstore.TenantMiddleware(backend.(*entstore.Store)))
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { router.ServeHTTP(w, r.WithContext(httpCtx)) }))
			defer server.Close()
			worker.Start(context.Background())
			wait := func(ch <-chan struct{}, label string) {
				t.Helper()
				select {
				case <-ch:
				case <-time.After(5 * time.Second):
					t.Fatalf("%s did not start", label)
				}
			}
			wait(directory.entered, "maintenance")
			wait(backgroundEntered, "autonomous")
			begin := time.Now()
			resp, err := server.Client().Post(server.URL+"/api/studio/bots/"+target.ID.String()+"/messages?scope="+string(scope), "application/json", strings.NewReader(fmt.Sprintf(`{"version":%d,"idempotency_key":"prompt-owner","instructions":"Observe the violet signal exactly once."}`, target.Version)))
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != 202 {
				t.Fatalf("admit %d: %s", resp.StatusCode, raw)
			}
			var receipt store.BotMessageReceipt
			if err = json.Unmarshal(raw, &receipt); err != nil {
				t.Fatal(err)
			}
			select {
			case req := <-llm.entered:
				encoded, _ := json.Marshal(req)
				if !strings.Contains(string(encoded), "violet signal") {
					t.Fatalf("accepted message missing from model: %s", encoded)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("owner continuation blocked behind unrelated maintenance/autonomous work")
			}
			saved, err := repo.GetRun(auth, target.ID, receipt.Run.ID)
			if err != nil || saved.Status != store.RunRunning || saved.Lease.WorkerID != "prompt-http" {
				t.Fatalf("not claimed: %+v %v", saved, err)
			}
			if elapsed := time.Since(begin); elapsed > 5*time.Second {
				t.Fatalf("commit-to-claim exceeded five seconds: %s", elapsed)
			}
			// Duplicate hints cannot overlap an active same-Bot execution, even
			// though both slots are occupied and its durable claim is live.
			for i := 0; i < 20; i++ {
				worker.NotifyRunState(auth, store.BotRunState{BotID: target.ID, RunID: receipt.Run.ID, Status: store.RunQueued, Presentation: store.BotRunPresentationHomeInteractive, StateSequence: receipt.Run.StateSequence})
			}
			if llm.calls.Load() != 1 {
				t.Fatalf("accepted turn reached model %d times", llm.calls.Load())
			}
			worker.lifecycleMu.Lock()
			slots, active := worker.activeSlots, worker.activeBots[target.ID]
			worker.lifecycleMu.Unlock()
			if slots != 2 || !active {
				t.Fatalf("expected independent owner/autonomous slots: %d %t", slots, active)
			}
			// A different process-local worker must honor the durable live claim,
			// not just this worker's activeBots exclusion.
			competitor := &BotWorker{config: worker.config}
			err = competitor.executeHint(context.Background(), botDispatchHint{key: botDispatchKey{target.ID, receipt.Run.ID}, route: BotSchedulerScope{"prompt-dispatch", "team", owner}, scope: scope, lane: botDispatchOwner})
			if err != nil && !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("competing worker: %v", err)
			}
			if llm.calls.Load() != 1 {
				t.Fatalf("competing worker replayed the claimed turn: %d", llm.calls.Load())
			}
			t.Logf("real HTTP %s commit-to-model/claim=%s, maintenance and unrelated autonomous still held, model calls=1", scope, time.Since(begin))
			worker.Stop()
		})
	}
}

func TestBotWorkerDispatchMailboxCoalescingOverflowAndStop(t *testing.T) {
	w := &BotWorker{}
	h := botDispatchHint{key: botDispatchKey{uuid.New(), uuid.New()}, sequence: 2, lane: botDispatchOwner}
	w.enqueueDispatch(h, true)
	h.sequence = 1
	w.enqueueDispatch(h, true)
	if len(w.pending) != 1 || w.pending[h.key].sequence != 2 {
		t.Fatal("duplicate/stale hint displaced latest")
	}
	h.sequence = 3
	w.enqueueDispatch(h, false)
	if len(w.pending) != 0 {
		t.Fatal("newer nonqueued state remained pending")
	}
	for i := 0; i < 257; i++ {
		w.enqueueDispatch(botDispatchHint{key: botDispatchKey{uuid.New(), uuid.New()}}, true)
	}
	if len(w.pending) != 256 || w.dispatchOverflow.Load() != 1 {
		t.Fatalf("unbounded mailbox: %d drops=%d", len(w.pending), w.dispatchOverflow.Load())
	}
	w.Stop()
	w.enqueueDispatch(h, true)
	if len(w.pending) != 256 {
		t.Fatal("stopped worker accepted hint")
	}
}

type promptDispatchRunPages struct {
	store.BotStore
	runs  []store.BotRun
	calls int
}

func (s *promptDispatchRunPages) ListRuns(_ context.Context, _ store.BotID, page store.BotPage) ([]store.BotRun, error) {
	s.calls++
	start := 0
	if page.After != "" {
		for i, r := range s.runs {
			if r.ID.String() == page.After {
				start = i + 1
				break
			}
		}
	}
	end := min(start+page.Limit, len(s.runs))
	return s.runs[start:end], nil
}

func TestBotWorkerDispatchOverflowRediscoverySinglePagePriority(t *testing.T) {
	w := &BotWorker{}
	for i := 0; i < botDispatchMailboxCapacity; i++ {
		w.enqueueDispatch(botDispatchHint{key: botDispatchKey{uuid.New(), uuid.New()}}, true)
	}
	p := execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: uuid.NewString(), OrgSlug: "org", TeamSlug: "team", Authenticated: true}
	ctx, err := execution.WithPrincipal(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	b := store.Bot{ID: uuid.New(), Scope: store.BotScopePersonal}
	r := store.BotRun{ID: uuid.New(), BotID: b.ID, Status: store.RunQueued, StateSequence: 1, Presentation: store.BotRunPresentationHomeInteractive}
	pages := &promptDispatchRunPages{}
	for i := 0; i < 200; i++ {
		pages.runs = append(pages.runs, store.BotRun{ID: uuid.New(), BotID: b.ID, Status: store.RunSucceeded})
	}
	pages.runs = append(pages.runs, r)
	if err := w.discoverBot(ctx, pages, b); err != nil {
		t.Fatal(err)
	}
	if w.dispatchOverflow.Load() != 1 || pages.calls != 2 {
		t.Fatalf("overflow=%d pages=%d", w.dispatchOverflow.Load(), pages.calls)
	}
	w.pending = make(map[botDispatchKey]botDispatchHint) // simulate admitted/drained accelerations
	pages.calls = 0
	if err := w.discoverBot(ctx, pages, b); err != nil {
		t.Fatal(err)
	}
	h, ok := w.pending[botDispatchKey{b.ID, r.ID}]
	if !ok || h.lane != botDispatchOwner || pages.calls != 2 {
		t.Fatalf("durable owner not recovered once per page: %+v pages=%d", h, pages.calls)
	}
	r.Trigger = store.TriggerCron
	if dispatchLane(r) != botDispatchAutonomous {
		t.Fatal("cron sharing Home consumed owner priority")
	}
}

func TestBotWorkerDispatchDrainJoinsAndRejectsLateAdmission(t *testing.T) {
	backend, _ := setupChannelResolverFixture(t, "dispatch-drain", "team", "telegram", "linked")
	directory := &promptDispatchDirectory{OrganizationStore: backend.Organizations(), entered: make(chan struct{}), release: make(chan struct{})}
	w := &BotWorker{config: BotWorkerConfig{Backend: promptDispatchBackend{PlatformBackend: backend, directory: directory}, WorkerID: "drain", PollInterval: time.Hour}}
	w.Start(context.Background())
	defer w.Stop()
	select {
	case <-directory.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("maintenance not entered")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := w.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	if err := w.Poll(context.Background()); !errors.Is(err, context.Canceled) {
		t.Fatalf("late poll admitted: %v", err)
	}
	w.enqueueDispatch(botDispatchHint{key: botDispatchKey{uuid.New(), uuid.New()}, lane: botDispatchOwner}, true)
	w.lifecycleMu.Lock()
	pending, active := len(w.pending), w.activeSlots
	w.lifecycleMu.Unlock()
	if pending != 0 || active != 0 {
		t.Fatalf("drained worker admitted work: pending=%d active=%d", pending, active)
	}
	w.Start(context.Background()) // stopped workers cannot reopen wg.Add admission
}

func TestBotWorkerDispatchNotifierTrustedRoutingOnly(t *testing.T) {
	w := &BotWorker{}
	state := store.BotRunState{BotID: uuid.New(), RunID: uuid.New(), SessionID: uuid.New(), StateSequence: 7, Status: store.RunQueued, Presentation: store.BotRunPresentationHomeInteractive, UserEventID: "private request content"}
	w.NotifyRunState(context.Background(), state)
	if len(w.pending) != 0 {
		t.Fatal("unauthenticated notifier created acceleration")
	}
	p := execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: uuid.NewString(), OrgSlug: "org", TeamSlug: "team", Authenticated: true}
	ctx, err := execution.WithPrincipal(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(ctx)
	w.NotifyRunState(ctx, state)
	cancel() // request lifetime is not dispatch authority or cancellation
	key := botDispatchKey{state.BotID, state.RunID}
	h, ok := w.pending[key]
	if !ok || h.route != (BotSchedulerScope{"org", "team", p.Subject}) || h.sequence != 7 || h.lane != botDispatchOwner || h.scope != "" {
		t.Fatalf("incorrect trusted routing copy: %+v", h)
	}
	state.StateSequence = 6
	state.Status = store.RunInterrupted
	w.NotifyRunState(ctx, state)
	if _, ok := w.pending[key]; !ok {
		t.Fatal("stale nonqueued hint removed latest queue")
	}
	state.StateSequence = 8
	w.NotifyRunState(ctx, state)
	if len(w.pending) != 0 {
		t.Fatal("newer stopped hint remained pending")
	}
}

func TestBotWorkerDispatchConcurrentNotifyStop(t *testing.T) {
	w := &BotWorker{}
	var writers sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 8; i++ {
		writers.Add(1)
		go func() {
			defer writers.Done()
			<-start
			for j := 0; j < 100; j++ {
				w.enqueueDispatch(botDispatchHint{key: botDispatchKey{uuid.New(), uuid.New()}, lane: botDispatchOwner}, true)
			}
		}()
	}
	close(start)
	w.Stop()
	writers.Wait()
	w.lifecycleMu.Lock()
	defer w.lifecycleMu.Unlock()
	if len(w.pending) > botDispatchMailboxCapacity || w.activeSlots != 0 || !w.activationStopped.Load() {
		t.Fatal("concurrent late notifier reopened stopped admission")
	}
}

// promptDispatchUsers holds an admitted attempt at the fresh identity boundary.
// Cancellation is deliberately joined before releasing per-Bot exclusion, even
// when an adapter does not return immediately on context cancellation.
type promptDispatchUsers struct {
	store.UserStore
	entered chan context.Context
	release chan struct{}
}

func (s *promptDispatchUsers) GetByID(ctx context.Context, _ string) (*store.User, error) {
	s.entered <- ctx
	<-s.release
	return nil, store.ErrBotForbidden
}

type promptDispatchIdentityBackend struct {
	store.PlatformBackend
	users *promptDispatchUsers
}

func (b promptDispatchIdentityBackend) Users() store.UserStore { return b.users }

func TestBotWorkerDispatchOwnerBurstReservation(t *testing.T) {
	users := &promptDispatchUsers{entered: make(chan context.Context, 4), release: make(chan struct{})}
	w := &BotWorker{config: BotWorkerConfig{Backend: promptDispatchIdentityBackend{users: users}}}
	ctx := context.Background()
	hints := []botDispatchHint{
		{key: botDispatchKey{uuid.New(), uuid.New()}, lane: botDispatchAutonomous},
		{key: botDispatchKey{uuid.New(), uuid.New()}, lane: botDispatchOwner},
		{key: botDispatchKey{uuid.New(), uuid.New()}, lane: botDispatchOwner},
	}
	for i, hint := range hints {
		w.enqueueDispatch(hint, true)
		w.dispatchPending(ctx)
		select {
		case <-users.entered:
		case <-time.After(time.Second):
			t.Fatalf("dispatch %d did not start while bounded owner capacity was available", i+1)
		}
	}
	w.lifecycleMu.Lock()
	if w.activeSlots != 3 || w.autonomousSlots != 1 || w.ownerSlots != 2 {
		t.Errorf("owner burst reservation unavailable: active=%d owner=%d autonomous=%d", w.activeSlots, w.ownerSlots, w.autonomousSlots)
	}
	w.lifecycleMu.Unlock()
	close(users.release)
	w.wg.Wait()
}

func TestBotWorkerDispatchCapacitySameBotAndJoinedStop(t *testing.T) {
	users := &promptDispatchUsers{entered: make(chan context.Context, 4), release: make(chan struct{})}
	w := &BotWorker{config: BotWorkerConfig{Backend: promptDispatchIdentityBackend{users: users}}}
	ctx := context.Background()
	background := botDispatchHint{key: botDispatchKey{uuid.New(), uuid.New()}, lane: botDispatchAutonomous}
	anotherBackground := botDispatchHint{key: botDispatchKey{uuid.New(), uuid.New()}, lane: botDispatchAutonomous}
	owner := botDispatchHint{key: botDispatchKey{uuid.New(), uuid.New()}, lane: botDispatchOwner, sequence: 1}
	w.enqueueDispatch(background, true)
	w.dispatchPending(ctx)
	first := <-users.entered
	w.enqueueDispatch(anotherBackground, true)
	w.dispatchPending(ctx)
	w.lifecycleMu.Lock()
	if w.activeSlots != 1 || w.autonomousSlots != 1 {
		t.Errorf("autonomous work consumed reserved owner slot: %d/%d", w.activeSlots, w.autonomousSlots)
	}
	w.lifecycleMu.Unlock()
	w.enqueueDispatch(owner, true)
	w.dispatchPending(ctx)
	second := <-users.entered
	owner.sequence++
	w.enqueueDispatch(owner, true) // running -> pending, never overlapping
	w.dispatchPending(ctx)
	w.lifecycleMu.Lock()
	if w.activeSlots != 2 || !w.activeBots[owner.key.botID] || w.pending[owner.key].sequence != 2 {
		t.Errorf("newer queued hint lost or active exclusion released: slots=%d pending=%+v", w.activeSlots, w.pending[owner.key])
	}
	w.lifecycleMu.Unlock()
	stopped := make(chan struct{})
	go func() { w.Stop(); close(stopped) }()
	select {
	case <-first.Done():
	case <-time.After(5 * time.Second):
		t.Error("Stop did not cancel autonomous attempt")
	}
	select {
	case <-second.Done():
	case <-time.After(5 * time.Second):
		t.Error("Stop did not cancel owner attempt")
	}
	select {
	case <-stopped:
		t.Error("Stop returned before identity adapters joined")
	default:
	}
	w.lifecycleMu.Lock()
	if !w.activeBots[owner.key.botID] || w.activeSlots != 2 {
		t.Error("per-Bot exclusion released before joined teardown")
	}
	before := len(w.pending)
	w.lifecycleMu.Unlock()
	w.enqueueDispatch(botDispatchHint{key: botDispatchKey{uuid.New(), uuid.New()}}, true)
	w.dispatchPending(ctx)
	close(users.release)
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop failed to join released attempts")
	}
	w.lifecycleMu.Lock()
	defer w.lifecycleMu.Unlock()
	if w.activeSlots != 0 || w.ownerSlots != 0 || w.autonomousSlots != 0 || len(w.activeBots) != 0 || len(w.activeCancels) != 0 || len(w.pending) != before {
		t.Fatalf("stopped lifecycle leaked admission/exclusion: slots=%d owner=%d autonomous=%d active=%d cancels=%d pending=%d", w.activeSlots, w.ownerSlots, w.autonomousSlots, len(w.activeBots), len(w.activeCancels), len(w.pending))
	}
}

func (b promptDispatchBackend) PlatformNetworkPolicies() store.NetworkPolicyStore {
	return b.PlatformBackend.(interface {
		PlatformNetworkPolicies() store.NetworkPolicyStore
	}).PlatformNetworkPolicies()
}
