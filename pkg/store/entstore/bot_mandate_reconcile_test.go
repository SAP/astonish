package entstore

import (
	"context"
	"errors"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"sync"
	"testing"
	"time"
)

func TestEmployeeScheduleCoordination(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, denied := range []bool{false, true} {
			t.Run(string(scope)+map[bool]string{false: "/enabled", true: "/denied"}[denied], func(t *testing.T) {
				repo, rt, owner, worker, q := authorizedScheduleFixture(t, scope)
				op, err := rt.EnqueueAuthorizedOperation(worker, q)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = rt.CoordinateEmployeeSchedules(owner, q.MandateRef, q.Lease.RunID); !errors.Is(err, store.ErrBotForbidden) {
					t.Fatalf("active source: %v", err)
				}
				report := acceptedWorkReport(t, repo, worker, q.Lease, "private draft")
				if err = repo.CommitRun(WithEmployeeScheduleCoordinator(worker), store.BotRunCommit{Lease: q.Lease, Status: store.RunSucceeded, Report: report}); err != nil {
					t.Fatal(err)
				}
				saved, err := repo.db.ReadRun(owner, q.MandateRef.BotID, q.Lease.RunID)
				if err != nil || !saved.EmployeeScheduleCoordinator {
					t.Fatalf("marker: %+v %v", saved, err)
				}
				if denied {
					rt.readiness = func(context.Context, store.Bot, store.BotVersion) error { return store.ErrBotUnsupported }
				}
				results, err := rt.CoordinateEmployeeSchedules(owner, q.MandateRef, q.Lease.RunID)
				if len(results) != 1 {
					t.Fatalf("results %+v %v", results, err)
				}
				if denied {
					if !errors.Is(err, store.ErrBotUnsupported) || results[0].Status != store.BotOperationCancelled {
						t.Fatalf("denial %+v %v", results, err)
					}
					rt.readiness = func(context.Context, store.Bot, store.BotVersion) error { return nil }
					if err = rt.ReconcileAuthorizedOperations(owner, q.MandateRef.BotID, 32); err != nil {
						t.Fatal(err)
					}
					read, err := rt.ReadAuthorizedOperation(owner, q.MandateRef, op.ID)
					if err != nil || read.Status != store.BotOperationCancelled {
						t.Fatalf("later activation %+v %v", read, err)
					}
				} else {
					if err != nil || results[0].Receipt.Schedule == nil || !results[0].Receipt.Schedule.Enabled || results[0].Receipt.Schedule.NextRun == nil {
						t.Fatalf("enabled %+v %v", results, err)
					}
					replay, err := rt.CoordinateEmployeeSchedules(owner, q.MandateRef, q.Lease.RunID)
					if err != nil || len(replay) != 1 || replay[0].Revision != results[0].Revision {
						t.Fatalf("replay %+v %v", replay, err)
					}
				}
				if _, err = repo.db.FindActivity(owner, q.MandateRef.BotID, "authorized-schedule-receipt:"+op.ID.String()); !errors.Is(err, store.ErrBotNotFound) {
					t.Fatalf("separate runtime voice: %v", err)
				}
			})
		}
	}
}

func TestEmployeeScheduleCoordinationFailedSourceNeverActivates(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, status := range []store.RunStatus{store.RunFailed, store.RunCancelled} {
			t.Run(string(scope)+"/"+string(status), func(t *testing.T) {
				repo, rt, owner, worker, q := authorizedScheduleFixture(t, scope)
				op, err := rt.EnqueueAuthorizedOperation(worker, q)
				if err != nil {
					t.Fatal(err)
				}
				work, err := repo.LoadWork(worker, q.Lease)
				if err != nil {
					t.Fatal(err)
				}
				report := store.BotReport{Summary: "private failed source", Work: store.BuildBotWorkProjection(work)}
				if err = repo.CommitRun(WithEmployeeScheduleCoordinator(worker), store.BotRunCommit{Lease: q.Lease, Status: status, Report: report}); err != nil {
					t.Fatal(err)
				}
				if _, err = rt.ApplyAuthorizedOperation(owner, q.MandateRef, op.ID); !errors.Is(err, store.ErrBotForbidden) {
					t.Fatalf("direct failed source activation: %v", err)
				}
				if err = rt.ReconcileAuthorizedOperations(owner, q.MandateRef.BotID, 32); err != nil {
					t.Fatal(err)
				}
				read, err := rt.ReadAuthorizedOperation(owner, q.MandateRef, op.ID)
				if err != nil || read.Status != store.BotOperationPending {
					t.Fatalf("background adopted source: %+v %v", read, err)
				}
				results, err := rt.CoordinateEmployeeSchedules(owner, q.MandateRef, q.Lease.RunID)
				if err != nil || len(results) != 1 || results[0].Status != store.BotOperationCancelled {
					t.Fatalf("terminal cancellation: %+v %v", results, err)
				}
				if _, err = rt.CoordinateEmployeeSchedules(owner, q.MandateRef, q.Lease.RunID); err != nil {
					t.Fatal(err)
				}
				if jobs := repo.db.(botTriggerDB).triggerScheduler().List(owner); len(jobs) != 0 {
					t.Fatalf("failed source created jobs: %+v", jobs)
				}
			})
		}
	}
}

func TestPresentationBudgetSaturation(t *testing.T) {
	if presentationAdd(1<<63-2, 10) != 1<<63-1 || presentationTokens(store.BotUsage{InputTokens: 1<<63 - 2, OutputTokens: 10}) != 1<<63-1 {
		t.Fatal("usage accounting overflowed")
	}
}

func TestEmployeePresentationCumulativePersistence(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			repo, ctx, run, reopen := questionQuotaFixture(t, scope)
			rt := &botMandateRuntime{repo: repo, policy: questionQuotaPolicy}
			ref := *run.MandateRef
			if err := rt.ReserveBotMandateRun(ctx, ref, run.ID); err != nil {
				t.Fatal(err)
			}
			id := uuid.New()
			if err := rt.ReserveBotMandateModel(ctx, ref, run.ID, id, 50); err != nil {
				t.Fatal(err)
			}
			if err := rt.SettleBotMandateModel(ctx, ref, run.ID, id, 40); err != nil {
				t.Fatal(err)
			}
			lease, err := repo.ClaimRun(ctx, store.BotClaimRequest{BotID: run.BotID, RunID: run.ID, WorkerID: "employee", ExpiresAt: time.Now().Add(time.Minute)})
			if err != nil {
				t.Fatal(err)
			}
			work, err := repo.LoadWork(ctx, lease)
			if err != nil {
				t.Fatal(err)
			}
			if err = repo.CommitRun(ctx, store.BotRunCommit{Lease: lease, Status: store.RunFailed, ModelSelection: &store.SessionPin{Provider: "test", Model: "pinned"}, Usage: store.BotUsage{InputTokens: 40}, Report: store.BotReport{Work: store.BuildBotWorkProjection(work), Summary: "private definitive result"}}); err != nil {
				t.Fatal(err)
			}
			if err = rt.ReleaseBotMandateRun(ctx, ref, run.ID); err != nil {
				t.Fatal(err)
			}
			// The execution runtime has already elapsed; original authentication remains live.
			source, err := repo.GetRun(ctx, run.BotID, run.ID)
			if err != nil {
				t.Fatal(err)
			}
			repo.now = func() time.Time { return source.StartedAt.Add(61 * time.Second) }
			q := store.BotEmployeePresentationRequest{BotID: run.BotID, RunID: run.ID}
			p, err := repo.PrepareEmployeePresentation(ctx, q)
			if err != nil {
				t.Fatal(err)
			}
			if p.Usage.InputTokens != 40 || p.Provider != "test" || p.Model != "pinned" {
				t.Fatalf("source reset: %+v", p)
			}
			// Reopen a historical envelope with exhausted execution caps.
			if err = repo.db.Transaction(ctx, func(tx botDB) error {
				rec, saved, e := repo.presentationSource(ctx, tx, q)
				if e != nil {
					return e
				}
				zero := int64(0)
				saved.EmployeePresentation.EffectiveLimits = store.BudgetSpec{MaxRuntimeSeconds: 1, MaxTokens: &zero, MaxModelCostMicros: &zero}
				saved.EmployeePresentation.Deadline = source.StartedAt.Add(time.Second)
				return saveEmployeePresentation(ctx, tx, rec, saved)
			}); err != nil {
				t.Fatal(err)
			}
			reopen()
			q.ClaimToken = "private-claim"
			p, err = repo.ClaimEmployeePresentation(ctx, store.BotEmployeePresentationClaim{BotEmployeePresentationRequest: q, ExpectedReportSequence: p.ReportSequence, ExpectedReportDigest: p.ReportDigest})
			if err != nil {
				t.Fatal(err)
			}
			if p.State != store.BotEmployeePresentationClaimed || p.ClaimToken != q.ClaimToken || p.BotID != q.BotID || p.RunID != q.RunID || p.Usage.InputTokens != 40 {
				t.Fatal("reopened presentation claim lost state, binding, or source usage")
			}
			foreign := q
			foreign.ClaimToken = "foreign"
			if _, err = repo.ReadEmployeePresentation(ctx, foreign); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("foreign claim: %v", err)
			}
			reservation := store.BotEmployeePresentationModelReservation{BotEmployeePresentationRequest: q, TurnID: "final", DispatchToken: "dispatch", Reserved: store.BotUsage{InputTokens: 61}}
			if _, err = repo.ReserveEmployeePresentationModel(ctx, reservation); err != nil {
				t.Fatalf("historical execution quota blocked final feedback: %v", err)
			}
			reopen()
			if _, err = repo.ReserveEmployeePresentationModel(ctx, reservation); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("duplicate reservation: %v", err)
			}
			settlement := store.BotEmployeePresentationModelSettlement{BotEmployeePresentationRequest: q, TurnID: "final", DispatchToken: "dispatch", Usage: store.BotUsage{InputTokens: 9, RuntimeNanos: 10}}
			if _, err = repo.SettleEmployeePresentationModel(ctx, settlement); err != nil {
				t.Fatal(err)
			}
			if _, err = repo.SettleEmployeePresentationModel(ctx, settlement); err != nil {
				t.Fatal(err)
			}
			settlement.Usage.RuntimeNanos++
			if _, err = repo.SettleEmployeePresentationModel(ctx, settlement); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("inexact retry: %v", err)
			}
			usage := questionQuotaUsage(t, repo, ctx, run.BotID)
			if usage.Tokens != 49 || usage.Released[run.ID.String()].Tokens != 49 || usage.DailyRuns != 1 || len(usage.Active) != 0 {
				t.Fatalf("aggregate reset: %+v", usage)
			}
			if _, err = repo.CompleteEmployeePresentation(ctx, store.BotEmployeePresentationCompletion{BotEmployeePresentationRequest: q, DispatchToken: "dispatch", Text: "The requested work did not complete."}); err != nil {
				t.Fatal(err)
			}
			reopen()
			p, err = repo.ReadEmployeePresentation(ctx, q)
			if err != nil || p.State != store.BotEmployeePresentationCompleted {
				t.Fatalf("completion lost: %+v %v", p, err)
			}
		})
	}
}

func TestMandateWeatherReservationReconciliation(t *testing.T) {
	repo, bots, ctx, _, _ := runtimeFixture(t, store.BotScopePersonal)
	p, _ := execution.PrincipalFromContext(ctx)
	ctx, err := execution.WithAuthenticationEvidence(ctx, execution.AuthenticationEvidence{Method: p.Authentication, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	policy := func(c context.Context, r store.BotMandateRef) (store.BotMandatePolicy, error) {
		v, e := mandateTestPolicy(c, r)
		v.Limits.MaxTokens = 100000
		v.Limits.MaxTotalTokens = 150000
		return v, e
	}
	ctx = store.WithBotMessageAdmission(ctx, execution.NewBotTurnAuthorizationRegistry(2))
	ctx = store.WithBotMandateAdmission(ctx, policy)
	v := store.DefaultBotVersion()
	v.Charter = "Weather"
	first, err := repo.CreateAndStart(ctx, store.BotCreateRequest{Name: "Weather", OwnerID: uuid.MustParse(p.Subject), Version: v, IdempotencyKey: "weather-budget"})
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := NewBotMandateRuntime(bots, policy)
	if err != nil {
		t.Fatal(err)
	}
	ref := *first.Run.MandateRef
	run := first.Run.ID
	if err = runtime.ReserveBotMandateRun(ctx, ref, run); err != nil {
		t.Fatal(err)
	}
	// Recorded first two reservations and complete provider usage. Repeated prompts
	// remain billable; only unused reservation capacity may be returned.
	for _, call := range []struct{ hold, actual int64 }{{33308, 9986 + 111}, {38278, 12140 + 98}} {
		id := uuid.New()
		if err = runtime.ReserveBotMandateModel(ctx, ref, run, id, call.hold); err != nil {
			t.Fatal(err)
		}
		if err = runtime.SettleBotMandateModel(ctx, ref, run, id, call.actual); err != nil {
			t.Fatal(err)
		}
		if err = runtime.SettleBotMandateModel(ctx, ref, run, id, 0); err != nil {
			t.Fatal(err)
		}
	}
	// A representative 40k continuation fails before reconciliation: 71586+40000.
	if err = runtime.ConsumeBotMandateQuota(ctx, ref, run, 0, 40000, 0); err != nil {
		t.Fatalf("weather continuation denied by stale reservations: %v", err)
	}
	var quota *store.BotMandateQuotaError
	err = runtime.ConsumeBotMandateQuota(ctx, ref, run, 0, 40000, 0)
	if !errors.As(err, &quota) || quota.Dimension != "run_tokens" || quota.Used != 62335 || quota.Limit != 100000 {
		t.Fatalf("exact finite gate: %+v %v", quota, err)
	}
	id := uuid.New()
	if err = runtime.ReserveBotMandateModel(ctx, ref, run, id, 10000); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if e := runtime.SettleBotMandateModel(ctx, ref, run, id, 1000); e != nil {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	// Must remain 63335 after all duplicate settlements, never negative/refunded twice.
	err = runtime.ConsumeBotMandateQuota(ctx, ref, run, 0, 40000, 0)
	if !errors.As(err, &quota) || quota.Used != 63335 {
		t.Fatalf("duplicate settlement: %+v %v", quota, err)
	}
	if err = runtime.SettleBotMandateModel(ctx, ref, run, uuid.New(), 0); !errors.Is(err, store.ErrBotInvalid) {
		t.Fatalf("unknown refund: %v", err)
	}
	// An unexpectedly over-limit actual response must be retained, not rolled back.
	over := uuid.New()
	if err = runtime.ReserveBotMandateModel(ctx, ref, run, over, 1); err != nil {
		t.Fatal(err)
	}
	if err = runtime.SettleBotMandateModel(ctx, ref, run, over, 100000); err != nil {
		t.Fatal(err)
	}
	if err = runtime.ConsumeBotMandateQuota(ctx, ref, run, 0, 1, 0); !errors.As(err, &quota) || quota.Used != 163335 {
		t.Fatalf("overage discarded: %v", err)
	}
}
