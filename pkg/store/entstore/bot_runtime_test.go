package entstore

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"

	botdomain "github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

type runtimeTestHome struct {
	home    store.BotHomeBinding
	bind    func(context.Context, botdomain.HomeRunBinding) error
	ensured int
}

func (h *runtimeTestHome) Ensure(context.Context, botdomain.HomeRequest) (store.BotHomeBinding, error) {
	h.ensured++
	return h.home, nil
}
func (h *runtimeTestHome) BindRun(ctx context.Context, b botdomain.HomeRunBinding) error {
	if h.bind != nil {
		return h.bind(ctx, b)
	}
	return nil
}
func (*runtimeTestHome) ReadArtifact(context.Context, store.BotHomeBinding, store.BotArtifact) (io.ReadCloser, error) {
	return nil, store.ErrBotUnsupported
}
func (*runtimeTestHome) Delete(context.Context, store.BotHomeBinding) error {
	return store.ErrBotUnsupported
}

type runtimeTestAdapter struct {
	start   func(context.Context, botdomain.RunRequest, botdomain.EventSink) (botdomain.RunResult, error)
	calls   atomic.Int32
	cancels atomic.Int32
}

func (r *runtimeTestAdapter) Start(ctx context.Context, q botdomain.RunRequest, s botdomain.EventSink) (botdomain.RunResult, error) {
	r.calls.Add(1)
	return r.start(ctx, q, s)
}
func (*runtimeTestAdapter) Resume(context.Context, botdomain.RunRequest, botdomain.EventSink) (botdomain.RunResult, error) {
	return botdomain.RunResult{}, store.ErrBotUnsupported
}
func (r *runtimeTestAdapter) Cancel(context.Context, store.BotLease) error {
	r.cancels.Add(1)
	return nil
}

type runtimeTestBudget struct{}

func (runtimeTestBudget) ReserveModel(_ context.Context, r execution.BudgetReservation) (execution.BudgetReservation, error) {
	return r, nil
}
func (runtimeTestBudget) SettleModel(context.Context, uuid.UUID, execution.BudgetUsage) error {
	return nil
}
func (runtimeTestBudget) ReserveTool(context.Context, string) error { return nil }
func (runtimeTestBudget) Check(context.Context) error               { return nil }

func runtimeFixture(t *testing.T, scope store.BotScope) (*botRepository, store.BotStore, context.Context, *store.BotRun, *runtimeTestHome) {
	t.Helper()
	// Reuse the scoped SQLite fixture, finish its initial run, then register a
	// separate draft's home through the internal attestation seam before activation.
	repo, ctx, worker, initial, lease, _ := botRunFixture(t, scope)
	repo.now = nil
	if err := repo.CommitRun(worker, store.BotRunCommit{Lease: lease, Status: store.RunSucceeded, Report: store.BotReport{Summary: "Fixture setup"}}); err != nil {
		t.Fatal(err)
	}
	owner, _ := execution.PrincipalFromContext(ctx)
	v := store.DefaultBotVersion()
	v.Charter = "Observe saved input"
	b, err := repo.CreateDraft(ctx, store.BotCreateRequest{Name: "Runtime", OwnerID: uuid.MustParse(owner.Subject), Version: v, IdempotencyKey: initial.ID.String()})
	if err != nil {
		t.Fatal(err)
	}
	home := store.BotHomeBinding{ID: uuid.New(), BotID: b.ID, Scope: scope, Backend: "controlled", StorageKey: "opaque", Revision: 1}
	service, err := execution.WithPrincipal(context.Background(), execution.Principal{Kind: execution.PrincipalKindService, Authentication: execution.AuthMethodOAuth, Surface: execution.SurfaceCode, ClientID: "registry", OrgSlug: "org", TeamSlug: "team", Scopes: []string{BotHomeRegistrationScope}, Authenticated: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.registerHomeBinding(service, BotHomeRegistration{BotID: b.ID, ExpectedRevision: b.Revision, Home: home}, func(context.Context, BotHomeRegistration) error { return nil }); err != nil {
		t.Fatal(err)
	}
	b, err = repo.Get(ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	repo.activationCheck = func(context.Context, store.Bot, store.BotVersion) error { return nil }
	b, err = repo.Activate(ctx, store.BotLifecycleRequest{Reason: "Controlled runtime test", BotID: b.ID, ActorID: b.OwnerID, ExpectedRevision: b.Revision})
	if err != nil {
		t.Fatal(err)
	}
	run, err := repo.RequestRun(ctx, store.BotRunRequest{BotID: b.ID, Version: b.Version, Trigger: store.TriggerManual, IdempotencyKey: "supervisor", Instructions: "Persisted instructions only", Attachments: []store.ChatFile{{Filename: "note.txt", MimeType: "text/plain", Data: []byte("persisted file")}}})
	if err != nil {
		t.Fatal(err)
	}
	var scoped store.BotStore
	switch db := repo.db.(type) {
	case personalBotDB:
		scoped = &personalBotStore{client: db.client, auth: repo.auth}
	case teamBotDB:
		scoped = &teamBotStore{client: db.client, auth: repo.auth}
	}
	return repo, scoped, ctx, run, &runtimeTestHome{home: home}
}
func runtimeConfig(scoped store.BotStore, h *runtimeTestHome, r *runtimeTestAdapter) botdomain.SupervisorConfig {
	return botdomain.SupervisorConfig{Store: scoped, Homes: h, Runtime: r, WorkerID: "test-supervisor", LeaseDuration: time.Minute, RenewInterval: 5 * time.Millisecond, Events: func(context.Context, botdomain.RuntimeEvent) error { return nil }, Ready: func(ctx context.Context, input store.BotRunInput) (botdomain.RunPreparation, error) {
		id, _ := execution.BotRunIdentityFromContext(ctx)
		policy, err := execution.NewRunPolicy(execution.RunPolicySpec{Identity: id, Level: input.Version.Autonomy.Level})
		return botdomain.RunPreparation{Policy: policy, Budget: runtimeTestBudget{}}, err
	}}
}

func TestBotRuntimeSupervisorLifecycle(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			repo, scoped, ctx, run, home := runtimeFixture(t, scope)
			bound := false
			home.bind = func(_ context.Context, b botdomain.HomeRunBinding) error {
				if b.Home != home.home || b.PhysicalSessionID != run.SessionID || b.Lease.Fence < 1 {
					t.Fatal("invalid binding")
				}
				bound = true
				return nil
			}
			adapter := &runtimeTestAdapter{start: func(c context.Context, q botdomain.RunRequest, _ botdomain.EventSink) (botdomain.RunResult, error) {
				if len(q.Attachments) != 1 || string(q.Attachments[0].Data) != "persisted file" {
					t.Fatal("durable attachment did not reach runtime")
				}
				if !bound || q.Instructions != "Persisted instructions only" || q.Run.Status != store.RunRunning || q.Principal.Subject != q.Bot.OwnerID.String() || execution.RunPolicyFromContext(c) != q.Policy {
					t.Fatal("incorrect execution input")
				}
				return botdomain.RunResult{Status: store.RunSucceeded, Report: store.BotReport{Summary: "Observed"}, Usage: store.BotUsage{InputTokens: 11, ToolCalls: 1}}, nil
			}}
			supervisor, err := botdomain.NewSupervisor(runtimeConfig(scoped, home, adapter))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := supervisor.Execute(ctx, run.BotID, run.ID); err != nil {
				t.Fatal(err)
			}
			saved, err := repo.GetRun(ctx, run.BotID, run.ID)
			if err != nil || saved.Status != store.RunSucceeded || saved.Usage.InputTokens != 11 || saved.FinishedAt == nil {
				t.Fatalf("not committed: %+v %v", saved, err)
			}
			if _, err := supervisor.Execute(ctx, run.BotID, run.ID); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("duplicate execution: %v", err)
			}
			if adapter.calls.Load() != 1 {
				t.Fatal("runtime repeated")
			}
			activity, err := repo.ListActivity(ctx, run.BotID, 0, 100)
			if err != nil {
				t.Fatal(err)
			}
			if activity[len(activity)-1].Kind != store.BotActivityReport {
				t.Fatal("missing durable report activity")
			}
		})
	}
}

func TestBotRuntimeSupervisorFailClosed(t *testing.T) {
	for _, mode := range []string{"missing-policy", "missing-budget", "readiness-error", "wrong-policy", "wrong-home", "non-owner", "viewer", "wrong-tenant"} {
		t.Run(mode, func(t *testing.T) {
			repo, scoped, ctx, run, home := runtimeFixture(t, store.BotScopeTeam)
			adapter := &runtimeTestAdapter{start: func(context.Context, botdomain.RunRequest, botdomain.EventSink) (botdomain.RunResult, error) {
				t.Fatal("unexpected execution")
				return botdomain.RunResult{}, nil
			}}
			c := runtimeConfig(scoped, home, adapter)
			ready := c.Ready
			switch mode {
			case "missing-policy":
				c.Ready = func(context.Context, store.BotRunInput) (botdomain.RunPreparation, error) {
					return botdomain.RunPreparation{Budget: runtimeTestBudget{}}, nil
				}
			case "missing-budget":
				c.Ready = func(ctx context.Context, i store.BotRunInput) (botdomain.RunPreparation, error) {
					p, e := ready(ctx, i)
					p.Budget = nil
					return p, e
				}
			case "readiness-error":
				c.Ready = func(context.Context, store.BotRunInput) (botdomain.RunPreparation, error) {
					return botdomain.RunPreparation{}, store.ErrBotUnsupported
				}
			case "wrong-policy":
				c.Ready = func(ctx context.Context, i store.BotRunInput) (botdomain.RunPreparation, error) {
					id, _ := execution.BotRunIdentityFromContext(ctx)
					id.RunID = uuid.New()
					return ready(execution.WithBotRunIdentity(ctx, id), i)
				}
			case "wrong-home":
				home.home.ID = uuid.New()
			case "non-owner", "wrong-tenant":
				p, _ := execution.PrincipalFromContext(ctx)
				if mode == "non-owner" {
					p.Subject = uuid.NewString()
				} else {
					p.TeamSlug = "other"
				}
				ctx, _ = execution.WithPrincipal(ctx, p)
			case "viewer":
				s := scoped.(*teamBotStore)
				s.auth.check = func(context.Context, string) (string, error) { return "viewer", nil }
			}
			supervisor, err := botdomain.NewSupervisor(c)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := supervisor.Execute(ctx, run.BotID, run.ID); err == nil {
				t.Fatal("missing denial")
			}
			saved, err := repo.db.ReadRun(context.Background(), run.BotID, run.ID)
			if err != nil || saved.Run.Status != store.RunQueued {
				t.Fatalf("claimed despite denial: %+v %v", saved, err)
			}
		})
	}
}

func TestBotRuntimeSupervisorLeaseLossCancels(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			repo, scoped, ctx, run, home := runtimeFixture(t, scope)
			adapter := &runtimeTestAdapter{start: func(c context.Context, q botdomain.RunRequest, _ botdomain.EventSink) (botdomain.RunResult, error) {
				b, err := repo.Get(ctx, run.BotID)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := repo.Pause(ctx, store.BotLifecycleRequest{Reason: "Controlled runtime test", BotID: b.ID, ExpectedRevision: b.Revision, ActorID: b.OwnerID}); err != nil {
					t.Fatal(err)
				}
				select {
				case <-c.Done():
				case <-time.After(time.Second):
					t.Fatal("runtime not cancelled")
				}
				return botdomain.RunResult{Status: store.RunSucceeded, Report: store.BotReport{Summary: "Late result"}}, nil
			}}
			supervisor, err := botdomain.NewSupervisor(runtimeConfig(scoped, home, adapter))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := supervisor.Execute(ctx, run.BotID, run.ID); err == nil {
				t.Fatal("late success accepted")
			}
			saved, err := repo.GetRun(ctx, run.BotID, run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if saved.Status == store.RunSucceeded || saved.Report.Summary == "Late result" || adapter.cancels.Load() != 1 {
				t.Fatalf("unfenced result/cancellation: %+v %d", saved, adapter.cancels.Load())
			}
		})
	}
}

func TestBotRuntimeSupervisorFailureCommit(t *testing.T) {
	for _, mode := range []string{"bind", "runtime", "uncertain"} {
		t.Run(mode, func(t *testing.T) {
			repo, scoped, ctx, run, home := runtimeFixture(t, store.BotScopePersonal)
			expected := store.RunFailed
			adapter := &runtimeTestAdapter{start: func(context.Context, botdomain.RunRequest, botdomain.EventSink) (botdomain.RunResult, error) {
				return botdomain.RunResult{Usage: store.BotUsage{InputTokens: 7}}, errors.New("controlled failure")
			}}
			if mode == "bind" {
				home.bind = func(context.Context, botdomain.HomeRunBinding) error { return errors.New("bind failure") }
			}
			if mode == "uncertain" {
				expected = store.RunPartial
				adapter.start = func(context.Context, botdomain.RunRequest, botdomain.EventSink) (botdomain.RunResult, error) {
					return botdomain.RunResult{}, &botdomain.RunError{Class: botdomain.ErrorTransient, Message: "uncertain", UncertainEffect: true}
				}
			}
			supervisor, err := botdomain.NewSupervisor(runtimeConfig(scoped, home, adapter))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := supervisor.Execute(ctx, run.BotID, run.ID); err == nil {
				t.Fatal("failure hidden")
			}
			saved, err := repo.GetRun(ctx, run.BotID, run.ID)
			if err != nil || saved.Status != expected || saved.Report.Summary == "" {
				t.Fatalf("failure not recorded: %+v %v", saved, err)
			}
			if mode == "bind" && adapter.calls.Load() != 0 {
				t.Fatal("started without home")
			}
			if mode == "runtime" && saved.Usage.InputTokens != 7 {
				t.Fatal("usage discarded")
			}
		})
	}
}
