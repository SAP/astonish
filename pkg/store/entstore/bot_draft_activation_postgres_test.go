//go:build integration

package entstore

import (
	"context"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Fail rather than skip, and reject ordinary development/user databases before
// invoking the create/drop fixture. Use a newly owned loopback-only server with
// this dedicated bootstrap role and explicitly acknowledge its disposable use.
func requireDraftActivationPostgres(t *testing.T) {
	t.Helper()
	dsn := os.Getenv("ASTONISH_TEST_DSN")
	if dsn == "" || os.Getenv("ASTONISH_PG_ACTIVATION_DISPOSABLE") != "1" {
		t.Fatal("set ASTONISH_TEST_DSN and ASTONISH_PG_ACTIVATION_DISPOSABLE=1 for a newly owned disposable PostgreSQL server")
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid disposable PostgreSQL DSN")
	}
	if cfg.Host != "127.0.0.1" || cfg.Database != "postgres" || cfg.User != "astonish_activation_final" || cfg.Port == 5432 || len(cfg.Fallbacks) != 0 {
		t.Fatal("disposable DSN must use 127.0.0.1, a nondefault port, database postgres, role astonish_activation_final, and no fallback hosts/TLS")
	}
}

func newDraftActivationPostgres(t *testing.T, scope store.BotScope) (*postgresBotServicesFixture, *store.Bot) {
	t.Helper()
	requireDraftActivationPostgres(t)
	f := newPostgresBotServicesFixture(t, scope, 100, 100)
	// Reuse routing/provisioning, not its active test Bot. Finish that unrelated
	// fixture run and create the actual test subject through CreateDraft only.
	if err := f.repo.CommitRun(f.worker, store.BotRunCommit{Lease: f.lease, Status: store.RunSucceeded, Report: store.BotReport{Summary: "Fixture provisioning complete"}}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 6, 18, 0, 0, 0, time.UTC)
	f.repo.now = func() time.Time { return now }
	p, _ := execution.PrincipalFromContext(f.owner)
	v := store.DefaultBotVersion()
	v.Charter = "Save only the separately reviewed scheduled report"
	b, err := f.repo.CreateDraft(f.owner, store.BotCreateRequest{Name: "New scheduled draft", OwnerID: uuid.MustParse(p.Subject), Version: v, IdempotencyKey: "pg-new-draft"})
	if err != nil || b.Status != store.BotDraft || b.Version != 1 {
		t.Fatalf("fresh draft: %+v %v", b, err)
	}
	return f, b
}

func postgresDraftProposal(t *testing.T, f *postgresBotServicesFixture, b *store.Bot, payload store.BotControlPayload) *store.BotControlProposal {
	t.Helper()
	r, ctx := f.repo, f.owner
	message, err := r.AdmitMessage(ctx, store.BotMessageRequest{BotID: b.ID, Version: b.Version, Instructions: "Review exact change", IdempotencyKey: uuid.NewString()})
	if err != nil {
		t.Fatal(err)
	}
	p, _ := execution.PrincipalFromContext(ctx)
	run := message.Run
	worker := execution.WithBotRunIdentity(ctx, execution.BotRunIdentity{BotID: b.ID, RunID: run.ID, Version: b.Version, ActorID: p.Subject, OrgSlug: p.OrgSlug, TeamSlug: p.TeamSlug})
	lease, err := r.ClaimRun(worker, store.BotClaimRequest{BotID: b.ID, RunID: run.ID, WorkerID: "pg-draft-review", ExpiresAt: r.clock().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	proposal, err := r.ProposeControl(worker, store.BotControlProposalRequest{Lease: lease, IdempotencyKey: "review", ExpiresAt: r.clock().Add(time.Hour), Payload: payload})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.CommitRun(worker, store.BotRunCommit{Lease: lease, Status: store.RunSucceeded, Report: store.BotReport{Summary: "Ready for exact confirmation"}}); err != nil {
		t.Fatal(err)
	}
	return proposal
}

func postgresDraftSchedule(t *testing.T, f *postgresBotServicesFixture, b *store.Bot, op store.BotControlOperation, tr store.TriggerSpec) (*store.Bot, store.TriggerSpec) {
	t.Helper()
	p := postgresDraftProposal(t, f, b, store.BotControlPayload{Operation: op, Reason: "Review recurrence without activation", Schedule: &store.BotControlSchedule{ExpectedRevision: b.Revision, Trigger: tr}})
	got, err := f.repo.ConfirmControlProposal(f.owner, store.BotControlProposalDecision{BotID: b.ID, ProposalID: p.ID, Digest: p.Digest})
	if err != nil || got.Status != store.BotControlApplied {
		t.Fatalf("schedule confirmation: %+v %v", got, err)
	}
	b, err = f.repo.Get(f.owner, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	v, err := f.repo.GetVersion(f.owner, b.ID, b.Version)
	if err != nil || len(v.Triggers) != 1 {
		t.Fatalf("reviewed version: %+v %v", v, err)
	}
	return b, v.Triggers[0]
}

func reopenDraftActivationPostgres(t *testing.T, f *postgresBotServicesFixture) {
	t.Helper()
	now, readiness := f.repo.now, f.repo.activationCheck
	f.reopenReviewed(t)
	f.repo.now, f.repo.activationCheck = now, readiness
}

func assertPostgresActivationNoRun(t *testing.T, f *postgresBotServicesFixture, b *store.Bot) {
	t.Helper()
	runs, err := f.repo.ListRuns(f.owner, b.ID, store.BotPage{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	for _, run := range runs {
		if run.Trigger != store.TriggerManual || !run.Status.Terminal() {
			t.Fatalf("lifecycle queued immediate/catch-up work: %+v", run)
		}
	}
}

func TestIntegrationPostgresBotDraftScheduledActivationConsent(t *testing.T) {
	requireDraftActivationPostgres(t)
	eachSpendScope(t, func(t *testing.T, scope store.BotScope) {
		for _, method := range []string{"proposal", "legacy"} {
			for _, mode := range []string{"create-enabled", "edit-enabled", "disabled"} {
				t.Run(method+"/"+mode, func(t *testing.T) {
					f, b := newDraftActivationPostgres(t, scope)
					b, tr := postgresDraftSchedule(t, f, b, store.BotControlScheduleCreate, store.TriggerSpec{Kind: store.TriggerCron, Cron: "* * * * *", Timezone: "Etc/UTC", Instructions: "Write reviewed report", Enabled: mode == "create-enabled"})
					assertDraftRecurrenceInert(t, f.repo, f.owner, b, tr)
					if mode == "edit-enabled" {
						tr.Enabled = true
						b, tr = postgresDraftSchedule(t, f, b, store.BotControlScheduleEdit, tr)
						assertDraftRecurrenceInert(t, f.repo, f.owner, b, tr)
					}
					before, err := f.repo.GetVersion(f.owner, b.ID, b.Version)
					if err != nil {
						t.Fatal(err)
					}
					calls := 0
					f.repo.activationCheck = func(_ context.Context, got store.Bot, v store.BotVersion) error {
						calls++
						if got.Status != store.BotDraft || got.ID != b.ID || got.Scope != scope || !reflect.DeepEqual(v, *before) {
							t.Fatal("wrong readiness snapshot")
						}
						return nil
					}
					var decision *store.BotControlProposalDecision
					var applied *store.BotControlProposal
					if method == "proposal" {
						p := postgresDraftProposal(t, f, b, store.BotControlPayload{Operation: store.BotControlActivate, Reason: "Activate reviewed recurrence"})
						decision = &store.BotControlProposalDecision{BotID: b.ID, ProposalID: p.ID, Digest: p.Digest}
						// Pending consent survives closing every router connection.
						reopenDraftActivationPostgres(t, f)
						assertDraftRecurrenceInert(t, f.repo, f.owner, b, tr)
						applied, err = f.repo.ConfirmControlProposal(f.owner, *decision)
						if err != nil || applied.Status != store.BotControlApplied {
							t.Fatalf("activation confirmation: %+v %v", applied, err)
						}
					} else {
						_, err = f.repo.Activate(f.owner, store.BotLifecycleRequest{BotID: b.ID, ActorID: b.OwnerID, ExpectedRevision: b.Revision, Reason: "Activate reviewed recurrence"})
						if err != nil {
							t.Fatal(err)
						}
					}
					reopenDraftActivationPostgres(t, f)
					active, err := f.repo.Get(f.owner, b.ID)
					if err != nil || active.Status != store.BotActive || active.Version != b.Version || active.Revision != b.Revision+1 || calls != 1 {
						t.Fatalf("durable activation: %+v readiness=%d %v", active, calls, err)
					}
					after, err := f.repo.GetVersion(f.owner, b.ID, b.Version)
					if err != nil || !reflect.DeepEqual(before, after) {
						t.Fatalf("immutable version changed: %+v %v", after, err)
					}
					jobs := f.repo.db.(botTriggerDB).triggerScheduler()
					job := jobs.Get(f.owner, tr.ScheduleID.String())
					if job == nil || job.Enabled != tr.Enabled || (job.NextRun != nil) != tr.Enabled || job.Payload.BotVersion != b.Version {
						t.Fatalf("durable activation schedule: %+v", job)
					}
					if decision != nil {
						replay, e := f.repo.ConfirmControlProposal(f.owner, *decision)
						if e != nil || !reflect.DeepEqual(applied, replay) || !reflect.DeepEqual(job, jobs.Get(f.owner, job.ID)) || calls != 1 {
							t.Fatalf("durable activation replay: %+v %v", replay, e)
						}
					}
					assertPostgresActivationNoRun(t, f, active)
					if tr.Enabled {
						if !job.NextRun.Equal(f.repo.clock().Add(time.Minute)) {
							t.Fatalf("wrong next occurrence: %v", job.NextRun)
						}
						due := *job.NextRun
						f.repo.now = func() time.Time { return due }
						receipt, e := f.repo.AdmitScheduledRun(f.owner, draftActivationOccurrence(active, tr, due))
						if e != nil || receipt.Run.Status != store.RunQueued {
							t.Fatalf("consented due admission: %+v %v", receipt, e)
						}
					}
				})
			}
		}
	})
}

func TestIntegrationPostgresBotDraftScheduledActivationRollback(t *testing.T) {
	requireDraftActivationPostgres(t)
	eachSpendScope(t, func(t *testing.T, scope store.BotScope) {
		f, b := newDraftActivationPostgres(t, scope)
		b, tr := postgresDraftSchedule(t, f, b, store.BotControlScheduleCreate, store.TriggerSpec{Kind: store.TriggerCron, Cron: "* * * * *", Timezone: "Etc/UTC", Instructions: "Reviewed report", Enabled: true})
		p := postgresDraftProposal(t, f, b, store.BotControlPayload{Operation: store.BotControlActivate, Reason: "Activate"})
		f.repo.activationCheck = func(context.Context, store.Bot, store.BotVersion) error { return nil }
		before, err := f.repo.db.GetBot(f.owner, b.ID)
		if err != nil {
			t.Fatal(err)
		}
		jobBefore := f.repo.db.(botTriggerDB).triggerScheduler().Get(f.owner, tr.ScheduleID.String())
		original := f.repo.db
		f.repo.db = draftActivationReceiptFailTransaction{original, original.(botScheduledDB)}
		d := store.BotControlProposalDecision{BotID: b.ID, ProposalID: p.ID, Digest: p.Digest}
		_, err = f.repo.ConfirmControlProposal(f.owner, d)
		f.repo.db = original
		if err == nil || err.Error() != "injected receipt failure" {
			t.Fatalf("receipt failure not reached: %v", err)
		}
		reopenDraftActivationPostgres(t, f)
		after, err := f.repo.db.GetBot(f.owner, b.ID)
		if err != nil || !reflect.DeepEqual(before, after) {
			t.Fatalf("lifecycle/sequence escaped rollback: %+v %v", after, err)
		}
		if !reflect.DeepEqual(jobBefore, f.repo.db.(botTriggerDB).triggerScheduler().Get(f.owner, tr.ScheduleID.String())) {
			t.Fatal("schedule escaped rollback")
		}
		assertDraftRecurrenceInert(t, f.repo, f.owner, b, tr)
		saved, err := f.repo.ReadControlProposal(f.owner, b.ID, p.ID)
		if err != nil || saved.Status != store.BotControlProposed {
			t.Fatalf("receipt escaped rollback: %+v %v", saved, err)
		}
		got, err := f.repo.ConfirmControlProposal(f.owner, d)
		if err != nil || got.Status != store.BotControlApplied {
			t.Fatalf("retry: %+v %v", got, err)
		}
		reopenDraftActivationPostgres(t, f)
		active, err := f.repo.Get(f.owner, b.ID)
		job := f.repo.db.(botTriggerDB).triggerScheduler().Get(f.owner, tr.ScheduleID.String())
		if err != nil || active.Status != store.BotActive || active.Revision != b.Revision+1 || job == nil || !job.Enabled || job.NextRun == nil {
			t.Fatalf("retry not atomically durable: bot=%+v job=%+v err=%v", active, job, err)
		}
		assertPostgresActivationNoRun(t, f, active)
	})
}

func TestIntegrationPostgresBotDraftActivationPauseRestartSchedulesDisabled(t *testing.T) {
	requireDraftActivationPostgres(t)
	eachSpendScope(t, func(t *testing.T, scope store.BotScope) {
		for _, status := range []store.BotStatus{store.BotPaused, store.BotStopped} {
			t.Run(string(status), func(t *testing.T) {
				f, b := newDraftActivationPostgres(t, scope)
				b, tr := postgresDraftSchedule(t, f, b, store.BotControlScheduleCreate, store.TriggerSpec{Kind: store.TriggerCron, Cron: "* * * * *", Timezone: "Etc/UTC", Instructions: "Reviewed report", Enabled: true})
				f.repo.activationCheck = func(context.Context, store.Bot, store.BotVersion) error { return nil }
				b, err := f.repo.Activate(f.owner, store.BotLifecycleRequest{BotID: b.ID, ActorID: b.OwnerID, ExpectedRevision: b.Revision, Reason: "Activate"})
				if err != nil {
					t.Fatal(err)
				}
				q := store.BotLifecycleRequest{BotID: b.ID, ActorID: b.OwnerID, ExpectedRevision: b.Revision, Reason: "Disable recurrence"}
				if status == store.BotPaused {
					b, err = f.repo.Pause(f.owner, q)
				} else {
					b, err = f.repo.Stop(f.owner, q)
				}
				if err != nil {
					t.Fatal(err)
				}
				reopenDraftActivationPostgres(t, f)
				persisted, err := f.repo.Get(f.owner, b.ID)
				job := f.repo.db.(botTriggerDB).triggerScheduler().Get(f.owner, tr.ScheduleID.String())
				if err != nil || persisted.Status != status || job == nil || job.Enabled || job.NextRun != nil {
					t.Fatalf("pause/stop not durable: %+v job=%+v %v", persisted, job, err)
				}
				versionBefore, err := f.repo.GetVersion(f.owner, b.ID, b.Version)
				if err != nil {
					t.Fatal(err)
				}
				q.ExpectedRevision, q.Reason = b.Revision, "Resume lifecycle only"
				b, err = f.repo.Resume(f.owner, q)
				if err != nil {
					t.Fatal(err)
				}
				reopenDraftActivationPostgres(t, f)
				persisted, err = f.repo.Get(f.owner, b.ID)
				afterJob := f.repo.db.(botTriggerDB).triggerScheduler().Get(f.owner, tr.ScheduleID.String())
				if err != nil || persisted.Status != store.BotActive || persisted.Version != b.Version || !reflect.DeepEqual(job, afterJob) {
					t.Fatalf("resume changed recurrence: %+v job=%+v %v", persisted, afterJob, err)
				}
				versionAfter, err := f.repo.GetVersion(f.owner, b.ID, b.Version)
				if err != nil || !reflect.DeepEqual(versionBefore, versionAfter) || persisted.Revision != q.ExpectedRevision+1 {
					t.Fatalf("resume changed immutable version/revision: %+v %v", versionAfter, err)
				}
				if _, err := f.repo.AdmitScheduledRun(f.owner, draftActivationOccurrence(b, tr, f.repo.clock().Add(time.Minute))); err == nil {
					t.Fatal("resume admitted disabled recurrence")
				}
				assertPostgresActivationNoRun(t, f, b)
			})
		}
	})
}
