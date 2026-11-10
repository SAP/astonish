package entstore

import (
	"context"
	"database/sql"
	"encoding/json"
	entsql "entgo.io/ent/dialect/sql"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"

	personalent "github.com/SAP/astonish/ent/personal"
	teament "github.com/SAP/astonish/ent/team"
	"testing"
	"time"

	botdomain "github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	adksession "google.golang.org/adk/session"
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
	if err := repo.CommitRun(worker, store.BotRunCommit{Lease: lease, Status: store.RunSucceeded, Report: acceptedWorkReport(t, repo, worker, lease, "Fixture setup")}); err != nil {
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

func TestBotWorkSnapshotScopeAndRevision(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			repo, _, worker, run, lease, _ := botRunFixture(t, scope)
			work, err := repo.LoadWork(worker, lease)
			if err != nil || work == nil {
				t.Fatalf("claim did not persist requirements: %v", err)
			}
			if work.Phase != store.BotWorkPreparing || work.Contract.RunID != run.ID || work.Contract.BotID != run.BotID || work.Contract.Instructions == "" {
				t.Fatalf("invalid pinned contract: %+v", work)
			}
			original := work.Clone()
			work.Contract.Instructions = "weakened requirements"
			if _, err := repo.SaveWork(worker, store.BotWorkUpdate{Lease: lease, ExpectedRevision: original.Revision, State: *work}); !errors.Is(err, store.ErrBotInvalid) {
				t.Fatalf("mutable assignment: %v", err)
			}
			got, err := repo.LoadWork(worker, lease)
			if err != nil || !reflect.DeepEqual(original, got) {
				t.Fatalf("failed update changed snapshot: %v", err)
			}
			next := original.Clone()
			next.Phase = store.BotWorkProducing
			next.Criteria = []store.BotWorkCriterion{{ID: "assignment", SourceIDs: []string{"assignment", "charter"}, Text: original.Contract.Instructions}}
			got, err = repo.SaveWork(worker, store.BotWorkUpdate{Lease: lease, ExpectedRevision: original.Revision, State: *next})
			if err != nil || got.Revision != original.Revision+1 {
				t.Fatalf("save: %+v %v", got, err)
			}
			if _, err := repo.SaveWork(worker, store.BotWorkUpdate{Lease: lease, ExpectedRevision: original.Revision, State: *next}); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("stale update: %v", err)
			}
			foreign := lease
			foreign.RunID = uuid.New()
			if _, err := repo.LoadWork(worker, foreign); err == nil {
				t.Fatal("foreign run accepted")
			}
			public, err := repo.GetRun(worker, run.BotID, run.ID)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(public)
			if err != nil || strings.Contains(string(raw), "requirements_digest") {
				t.Fatal("private contract leaked")
			}
		})
	}
}

func TestBotWorkEnvelopeRoundTrip(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			repo, _, worker, run, lease, _ := botRunFixture(t, scope)
			before, err := repo.LoadWork(worker, lease)
			if err != nil {
				t.Fatal(err)
			}
			saved, err := repo.db.ReadRun(worker, run.BotID, run.ID)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(saved)
			if err != nil {
				t.Fatal(err)
			}
			var restored botStoredRun
			if err := json.Unmarshal(raw, &restored); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, restored.Work) {
				t.Fatal("private work lost during envelope round trip")
			}
			if err := json.Unmarshal([]byte(`{"Run":{}}`), &restored); err != nil || restored.Work != nil {
				t.Fatal("legacy run was retroactively assessed")
			}
		})
	}
}

func TestBotWorkSQLiteReopenAndClaim(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			owner := uuid.New()
			ctx, err := execution.WithPrincipal(context.Background(), execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: owner.String(), OrgSlug: "org", TeamSlug: "team", Authenticated: true})
			if err != nil {
				t.Fatal(err)
			}
			dsn := "file:" + filepath.Join(t.TempDir(), "work.db") + "?_fk=1&_busy_timeout=5000"
			var closeDB func() error
			var repo *botRepository
			open := func() {
				var db botDraftDB
				connection, err := sql.Open("sqlite", dsn)
				if err != nil {
					t.Fatal(err)
				}
				connection.SetMaxOpenConns(1)
				driver := entsql.OpenDB("sqlite3", connection)
				if scope == store.BotScopePersonal {
					c := personalent.NewClient(personalent.Driver(driver))
					if err != nil {
						t.Fatal(err)
					}
					if err := c.Schema.Create(ctx); err != nil {
						t.Fatal(err)
					}
					db, closeDB = personalBotDB{client: c}, c.Close
				} else {
					c := teament.NewClient(teament.Driver(driver))
					if err != nil {
						t.Fatal(err)
					}
					if err := c.Schema.Create(ctx); err != nil {
						t.Fatal(err)
					}
					db, closeDB = teamBotDB{client: c}, c.Close
				}
				repo = &botRepository{db: db, auth: botRepositoryAuth{orgSlug: "org", teamSlug: "team", userID: owner.String(), scope: scope, check: func(context.Context, string) (string, error) { return "member", nil }}}
			}
			open()
			t.Cleanup(func() { _ = closeDB() })
			v := store.DefaultBotVersion()
			v.Charter = "Daily factual report"
			b, err := repo.CreateDraft(ctx, store.BotCreateRequest{Name: "Work", OwnerID: owner, Version: v, Goals: []store.BotGoal{{Title: "Report", CompletionCriteria: "Include dated sources"}}, IdempotencyKey: "work"})
			if err != nil {
				t.Fatal(err)
			}
			// Controlled fixture activation; no physical execution is claimed.
			switch d := repo.db.(type) {
			case personalBotDB:
				err = d.client.Bot.UpdateOneID(b.ID).SetStatus("active").Exec(ctx)
			case teamBotDB:
				err = d.client.Bot.UpdateOneID(b.ID).SetStatus("active").Exec(ctx)
			}
			if err != nil {
				t.Fatal(err)
			}
			run, err := repo.RequestRun(ctx, store.BotRunRequest{BotID: b.ID, Version: b.Version, Trigger: store.TriggerManual, IdempotencyKey: "run", Instructions: "Produce the daily report"})
			if err != nil {
				t.Fatal(err)
			}
			worker := execution.WithBotRunIdentity(ctx, execution.BotRunIdentity{BotID: b.ID, RunID: run.ID, Version: b.Version, ActorID: owner.String(), OrgSlug: "org", TeamSlug: "team"})
			lease, err := repo.ClaimRun(worker, store.BotClaimRequest{BotID: b.ID, RunID: run.ID, WorkerID: "work", ExpiresAt: time.Now().Add(time.Minute)})
			if err != nil {
				t.Fatal(err)
			}
			work, err := repo.LoadWork(worker, lease)
			if err != nil {
				t.Fatal(err)
			}
			if len(work.Contract.Goals) != 1 || work.Contract.Goals[0].CompletionCriteria != "Include dated sources" {
				t.Fatalf("criteria not pinned: %+v", work)
			}
			goal := work.Contract.Goals[0]
			if err := repo.db.Transaction(ctx, func(tx botDraftDB) error {
				goal.Revision++
				goal.CompletionCriteria = "New criteria"
				return tx.UpdateGoal(ctx, goal, goal.Revision-1)
			}); err != nil {
				t.Fatal(err)
			}
			if err := closeDB(); err != nil {
				t.Fatal(err)
			}
			open()
			reopened, err := repo.LoadWork(worker, lease)
			if err != nil || !reflect.DeepEqual(work, reopened) {
				t.Fatalf("reopen replaced pinned criteria: %v", err)
			}
			// Two concurrent CAS writers cannot both consume the same phase.
			next := work.Clone()
			next.Phase = store.BotWorkProducing
			next.Criteria = []store.BotWorkCriterion{{ID: "all", SourceIDs: []string{"assignment", "charter", "goal:" + goal.ID.String()}, Text: "Produce sourced report"}}
			var wg sync.WaitGroup
			var wins atomic.Int32
			for range 2 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					if _, err := repo.SaveWork(worker, store.BotWorkUpdate{Lease: lease, ExpectedRevision: work.Revision, State: *next}); err == nil {
						wins.Add(1)
					}
				}()
			}
			wg.Wait()
			if wins.Load() != 1 {
				t.Fatalf("CAS winners: %d", wins.Load())
			}
			saved, err := repo.LoadWork(worker, lease)
			if err != nil || saved.Revision != 2 {
				t.Fatalf("CAS result: %+v %v", saved, err)
			}
			// An actual DB error rolls back the attempted next transition.
			fail := fmt.Errorf("injected work persistence failure")
			originalDB := repo.db
			repo.db = botWorkFailDB{botDraftDB: originalDB, failure: fail}
			blocked := saved.Clone()
			blocked.Phase = store.BotWorkBlocked
			blocked.Unresolved = []string{"missing source"}
			if _, err := repo.SaveWork(worker, store.BotWorkUpdate{Lease: lease, ExpectedRevision: saved.Revision, State: *blocked}); !errors.Is(err, fail) {
				t.Fatalf("missing injected failure: %v", err)
			}
			repo.db = originalDB
			after, err := repo.LoadWork(worker, lease)
			if err != nil || !reflect.DeepEqual(saved, after) {
				t.Fatal("failed write did not roll back", err)
			}
		})
	}
}

type botWorkFailDB struct {
	botDraftDB
	failure error
}

func (d botWorkFailDB) Transaction(ctx context.Context, fn func(botDraftDB) error) error {
	return d.botDraftDB.Transaction(ctx, func(tx botDraftDB) error { return fn(botWorkFailDB{botDraftDB: tx, failure: d.failure}) })
}
func (d botWorkFailDB) fenceTranscript(ctx context.Context, l store.BotLease, now time.Time) error {
	return d.botDraftDB.(botTranscriptDB).fenceTranscript(ctx, l, now)
}
func (d botWorkFailDB) appendRunTranscript(ctx context.Context, b store.Bot, r store.BotRun, e *adksession.Event) error {
	return d.botDraftDB.(botTranscriptDB).appendRunTranscript(ctx, b, r, e)
}
func (d botWorkFailDB) updateWorkRun(context.Context, botStoredRun, store.BotLease, time.Time) error {
	return d.failure
}

func TestBotWorkStateTransitions(t *testing.T) {
	work := store.NewBotWorkState(store.BotWorkContract{BotID: uuid.New(), RunID: uuid.New(), Version: 1, Instructions: "Give two sourced facts", Charter: "Report facts"})
	next := work.Clone()
	next.Phase = store.BotWorkAccepted
	if err := store.ValidateBotWorkUpdate(work, next); err == nil {
		t.Fatal("accepted without producing or reviewing")
	}
	next = work.Clone()
	next.SchemaVersion++
	if err := store.ValidateBotWorkUpdate(work, next); err == nil {
		t.Fatal("unknown schema accepted")
	}
	next = work.Clone()
	next.Phase = store.BotWorkProducing
	if err := store.ValidateBotWorkUpdate(work, next); err == nil {
		t.Fatal("missing source coverage accepted")
	}
	next.Criteria = []store.BotWorkCriterion{{ID: "facts", SourceIDs: []string{"assignment", "charter"}, Text: "Give two sourced facts"}}
	if err := store.ValidateBotWorkUpdate(work, next); err != nil {
		t.Fatal(err)
	}
	work = next
	next = work.Clone()
	next.Phase = store.BotWorkCandidateReady
	if err := store.ValidateBotWorkUpdate(work, next); err == nil {
		t.Fatal("missing candidate accepted")
	}
	next.Candidate = &store.BotWorkCandidate{Revision: 1, Text: "Two facts", EventID: "final-1"}
	next.Candidate.Digest = next.Candidate.ContentDigest()
	if err := store.ValidateBotWorkUpdate(work, next); err != nil {
		t.Fatal(err)
	}
	work = next
	next = work.Clone()
	next.Phase = store.BotWorkReviewing
	if err := store.ValidateBotWorkUpdate(work, next); err != nil {
		t.Fatal(err)
	}
	work = next
	next = work.Clone()
	next.Phase = store.BotWorkAccepted
	next.Review = &store.BotWorkReview{CandidateDigest: work.Candidate.Digest, Criteria: []store.BotCriterionAssessment{{CriterionID: "facts", Verdict: store.BotCriterionSatisfied, Method: store.BotAssessmentModel, EvidenceRefs: []string{"candidate:" + work.Candidate.Digest}, Explanation: "The requested facts are present"}}}
	if err := store.ValidateBotWorkUpdate(work, next); err != nil {
		t.Fatal(err)
	}
	next.Candidate.Text = "substituted after review"
	if err := store.ValidateBotWorkUpdate(work, next); err == nil {
		t.Fatal("candidate substitution accepted")
	}
}

func TestBotWorkRepairLimitsAndEvidence(t *testing.T) {
	work := store.NewBotWorkState(store.BotWorkContract{BotID: uuid.New(), RunID: uuid.New(), Version: 1, Instructions: "Two sourced facts", Charter: "Report"})
	advance := func(next *store.BotWorkState) {
		t.Helper()
		if err := store.ValidateBotWorkUpdate(work, next); err != nil {
			t.Fatalf("%s -> %s: %v", work.Phase, next.Phase, err)
		}
		next.Revision++
		work = next
	}
	next := work.Clone()
	next.Phase = store.BotWorkProducing
	next.Criteria = []store.BotWorkCriterion{{ID: "facts", SourceIDs: []string{"assignment", "charter"}, Text: "Two sourced facts"}}
	advance(next)
	for attempt := 0; attempt <= store.BotWorkMaxRepairs; attempt++ {
		next = work.Clone()
		next.Phase = store.BotWorkCandidateReady
		next.Candidate = &store.BotWorkCandidate{Revision: int64(attempt + 1), EventID: fmt.Sprintf("attempt-%d", attempt), Text: "One unsupported fact"}
		next.Candidate.Digest = next.Candidate.ContentDigest()
		next.Review = nil
		advance(next)
		next = work.Clone()
		next.Phase = store.BotWorkReviewing
		advance(next)
		next = work.Clone()
		next.Review = &store.BotWorkReview{CandidateDigest: work.Candidate.Digest, Criteria: []store.BotCriterionAssessment{{CriterionID: "facts", Verdict: store.BotCriterionFailed, Method: store.BotAssessmentModel, EvidenceRefs: []string{"candidate:" + work.Candidate.Digest}, Explanation: "Second fact and sources missing"}}}
		next.Phase = store.BotWorkAccepted
		if err := store.ValidateBotWorkUpdate(work, next); err == nil {
			t.Fatal("failed criterion became success")
		}
		next.Phase = store.BotWorkRepairReady
		if attempt == store.BotWorkMaxRepairs {
			if err := store.ValidateBotWorkUpdate(work, next); err == nil {
				t.Fatal("unbounded repairs")
			}
			next.Phase = store.BotWorkBlocked
			next.Unresolved = []string{"Sources and second fact remain missing"}
			advance(next)
			break
		}
		advance(next)
		next = work.Clone()
		next.Phase = store.BotWorkRepairing
		if err := store.ValidateBotWorkUpdate(work, next); err == nil {
			t.Fatal("repair counter not consumed")
		}
		next.Repairs++
		advance(next)
	}
	next = work.Clone()
	next.Phase = store.BotWorkProducing
	if err := store.ValidateBotWorkUpdate(work, next); err == nil {
		t.Fatal("blocked work silently reopened")
	}
	oversized := store.NewBotWorkState(store.BotWorkContract{BotID: uuid.New(), RunID: uuid.New(), Version: 1, Instructions: strings.Repeat("x", store.BotWorkMaxContractBytes+1)})
	if oversized.Phase != store.BotWorkBlocked || len(oversized.Contract.Instructions) != store.BotWorkMaxContractBytes+1 || oversized.Validate() != nil {
		t.Fatal("oversized source silently discarded")
	}
}

func TestBotWorkAuthorizationAndRecovery(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			repo, owner, worker, run, lease, now := botRunFixture(t, scope)
			original, err := repo.LoadWork(worker, lease)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := repo.LoadWork(owner, lease); !errors.Is(err, store.ErrBotForbidden) {
				t.Fatalf("owner without run identity accepted: %v", err)
			}
			check := repo.auth.check
			repo.auth.check = func(context.Context, string) (string, error) { return "viewer", nil }
			if _, err := repo.LoadWork(worker, lease); !errors.Is(err, store.ErrBotForbidden) {
				t.Fatalf("demoted owner: %v", err)
			}
			repo.auth.check = check
			*now = lease.ExpiresAt.Add(time.Second)
			if _, err := repo.LoadWork(worker, lease); !errors.Is(err, store.ErrBotExpired) {
				t.Fatalf("expired lease: %v", err)
			}
			if err := repo.RecoverRun(owner, lease); err != nil {
				t.Fatal(err)
			}
			saved, err := repo.db.ReadRun(owner, run.BotID, run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if saved.Work.Phase != store.BotWorkInterrupted || !reflect.DeepEqual(saved.Work.Contract, original.Contract) || saved.Work.Repairs != original.Repairs || saved.Run.Status != store.RunInterrupted {
				t.Fatal("recovery lost work or replayed it")
			}
		})
	}
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
				return botdomain.RunResult{Status: store.RunSucceeded, Report: acceptedWorkReport(t, repo, c, q.Run.Lease, "Observed"), Usage: store.BotUsage{InputTokens: 11, ToolCalls: 1}}, nil
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
