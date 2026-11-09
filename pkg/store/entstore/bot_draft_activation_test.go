package entstore

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// Use real scoped SQLite stores, without forcing the draft through an active state.
func draftActivationFixture(t *testing.T, scope store.BotScope) (*botRepository, context.Context, *store.Bot) {
	t.Helper()
	owner := uuid.New()
	ctx, err := execution.WithPrincipal(context.Background(), execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: owner.String(), OrgSlug: "org", TeamSlug: "team", Authenticated: true})
	if err != nil {
		t.Fatal(err)
	}
	auth := botRepositoryAuth{orgSlug: "org", userID: owner.String(), scope: scope, check: func(context.Context, string) (string, error) { return "member", nil }}
	var db botDraftDB
	if scope == store.BotScopeTeam {
		auth.teamSlug = "team"
		db = teamBotDB{client: newTeamClient(t)}
	} else {
		_, client := setupPersonalStore(t)
		db = personalBotDB{client: client}
	}
	now := time.Date(2026, 11, 6, 18, 0, 0, 0, time.UTC)
	r := &botRepository{auth: auth, db: db, now: func() time.Time { return now }}
	v := store.DefaultBotVersion()
	v.Charter = "Save a text-only scheduled report after separate schedule and activation consent"
	b, err := r.CreateDraft(ctx, store.BotCreateRequest{Name: "Scheduled draft", OwnerID: owner, Version: v, IdempotencyKey: "draft-activation"})
	if err != nil {
		t.Fatal(err)
	}
	return r, ctx, b
}

func draftActivationProposal(t *testing.T, r *botRepository, ctx context.Context, b *store.Bot, payload store.BotControlPayload) *store.BotControlProposal {
	t.Helper()
	message, err := r.AdmitMessage(ctx, store.BotMessageRequest{BotID: b.ID, Version: b.Version, Instructions: "Review exact change", IdempotencyKey: uuid.NewString()})
	if err != nil {
		t.Fatal(err)
	}
	run := message.Run
	worker := execution.WithBotRunIdentity(ctx, execution.BotRunIdentity{BotID: b.ID, RunID: run.ID, Version: b.Version, ActorID: b.OwnerID.String(), OrgSlug: "org", TeamSlug: "team"})
	lease, err := r.ClaimRun(worker, store.BotClaimRequest{BotID: b.ID, RunID: run.ID, WorkerID: "review", ExpiresAt: r.clock().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	p, err := r.ProposeControl(worker, store.BotControlProposalRequest{Lease: lease, IdempotencyKey: "review", ExpiresAt: r.clock().Add(time.Hour), Payload: payload})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.CommitRun(worker, store.BotRunCommit{Lease: lease, Status: store.RunSucceeded, Report: store.BotReport{Summary: "Ready for exact confirmation"}}); err != nil {
		t.Fatal(err)
	}
	return p
}

func confirmDraftSchedule(t *testing.T, r *botRepository, ctx context.Context, b *store.Bot, op store.BotControlOperation, tr store.TriggerSpec) (*store.Bot, store.TriggerSpec) {
	t.Helper()
	p := draftActivationProposal(t, r, ctx, b, store.BotControlPayload{Operation: op, Reason: "Review recurrence without activation", Schedule: &store.BotControlSchedule{ExpectedRevision: b.Revision, Trigger: tr}})
	got, err := r.ConfirmControlProposal(ctx, store.BotControlProposalDecision{BotID: b.ID, ProposalID: p.ID, Digest: p.Digest})
	if err != nil || got.Status != store.BotControlApplied {
		t.Fatalf("schedule confirmation: %+v %v", got, err)
	}
	b, err = r.Get(ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	v, err := r.GetVersion(ctx, b.ID, b.Version)
	if err != nil || len(v.Triggers) != 1 {
		t.Fatalf("version: %+v %v", v, err)
	}
	return b, v.Triggers[0]
}

func assertDraftRecurrenceInert(t *testing.T, r *botRepository, ctx context.Context, b *store.Bot, tr store.TriggerSpec) {
	t.Helper()
	current, err := r.Get(ctx, b.ID)
	if err != nil || current.Status != store.BotDraft {
		t.Fatalf("draft: %+v %v", current, err)
	}
	job := r.db.(botTriggerDB).triggerScheduler().Get(ctx, tr.ScheduleID.String())
	if job == nil || job.Enabled || job.NextRun != nil || job.Payload.BotVersion != b.Version {
		t.Fatalf("draft job not inert: %+v", job)
	}
	q := draftActivationOccurrence(b, tr, r.clock())
	if _, err := r.AdmitScheduledRun(ctx, q); err == nil {
		t.Fatal("recurrence admitted before activation consent")
	}
	runs, err := r.ListRuns(ctx, b.ID, store.BotPage{})
	if err != nil {
		t.Fatal(err)
	}
	for _, run := range runs {
		if run.Trigger != store.TriggerManual || run.Intent != store.BotIntentDraftConversation || !run.Status.Terminal() {
			t.Fatalf("unconsented work: %+v", run)
		}
	}
}

func draftActivationOccurrence(b *store.Bot, tr store.TriggerSpec, due time.Time) store.BotScheduledOccurrence {
	identity := fmt.Sprintf("%s/%d/%s/%s", b.ID, b.Version, tr.ID, due.UTC().Format(time.RFC3339Nano))
	return store.BotScheduledOccurrence{JobID: tr.ScheduleID.String(), BotID: b.ID, Version: b.Version, TriggerID: tr.ID, OccurrenceAt: due, IdempotencyKey: fmt.Sprintf("bot-schedule-v1:%x", sha256.Sum256([]byte(identity)))}
}

func TestBotDraftScheduledActivationConsent(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, path := range []string{"proposal-create-enabled", "proposal-edit-enabled", "legacy-create-enabled", "legacy-edit-enabled", "proposal-disabled", "legacy-disabled"} {
			t.Run(string(scope)+"/"+path, func(t *testing.T) {
				r, ctx, b := draftActivationFixture(t, scope)
				enabled := path == "proposal-create-enabled" || path == "legacy-create-enabled"
				b, tr := confirmDraftSchedule(t, r, ctx, b, store.BotControlScheduleCreate, store.TriggerSpec{Kind: store.TriggerCron, Cron: "* * * * *", Timezone: "Etc/UTC", Instructions: "Write the reviewed report", Enabled: enabled})
				assertDraftRecurrenceInert(t, r, ctx, b, tr)
				if path == "proposal-edit-enabled" || path == "legacy-edit-enabled" {
					tr.Enabled = true
					b, tr = confirmDraftSchedule(t, r, ctx, b, store.BotControlScheduleEdit, tr)
					assertDraftRecurrenceInert(t, r, ctx, b, tr)
				}
				before, err := r.GetVersion(ctx, b.ID, b.Version)
				if err != nil {
					t.Fatal(err)
				}
				calls := 0
				r.activationCheck = func(_ context.Context, got store.Bot, v store.BotVersion) error {
					calls++
					if got.ID != b.ID || got.Status != store.BotDraft || got.Scope != scope || !reflect.DeepEqual(v, *before) {
						t.Fatal("wrong readiness snapshot")
					}
					return nil
				}
				var active *store.Bot
				if path == "legacy-create-enabled" || path == "legacy-edit-enabled" || path == "legacy-disabled" {
					active, err = r.Activate(ctx, store.BotLifecycleRequest{BotID: b.ID, ActorID: b.OwnerID, ExpectedRevision: b.Revision, Reason: "Activate reviewed schedule"})
				} else {
					p := draftActivationProposal(t, r, ctx, b, store.BotControlPayload{Operation: store.BotControlActivate, Reason: "Activate reviewed schedule"})
					assertDraftRecurrenceInert(t, r, ctx, b, tr)
					d := store.BotControlProposalDecision{BotID: b.ID, ProposalID: p.ID, Digest: p.Digest}
					applied, e := r.ConfirmControlProposal(ctx, d)
					if e != nil || applied.Status != store.BotControlApplied {
						t.Fatalf("exact activation confirmation: %+v %v", applied, e)
					}
					job := r.db.(botTriggerDB).triggerScheduler().Get(ctx, tr.ScheduleID.String())
					replay, e := r.ConfirmControlProposal(ctx, d)
					if e != nil || !reflect.DeepEqual(applied, replay) || !reflect.DeepEqual(job, r.db.(botTriggerDB).triggerScheduler().Get(ctx, tr.ScheduleID.String())) {
						t.Fatalf("activation replay changed receipt/schedule: %+v %v", replay, e)
					}
					active, err = r.Get(ctx, b.ID)
				}
				if err != nil || active.Status != store.BotActive || active.Revision != b.Revision+1 || active.Version != b.Version || calls != 1 {
					t.Fatalf("activation: %+v readiness=%d %v", active, calls, err)
				}
				after, err := r.GetVersion(ctx, b.ID, b.Version)
				if err != nil || !reflect.DeepEqual(before, after) {
					t.Fatalf("immutable version changed: %+v %v", after, err)
				}
				runs, err := r.ListRuns(ctx, b.ID, store.BotPage{})
				if err != nil {
					t.Fatal(err)
				}
				for _, run := range runs {
					if run.Trigger != store.TriggerManual || !run.Status.Terminal() {
						t.Fatalf("activation queued immediate work: %+v", run)
					}
				}
				job := r.db.(botTriggerDB).triggerScheduler().Get(ctx, tr.ScheduleID.String())
				if job.Enabled != tr.Enabled || (job.NextRun != nil) != tr.Enabled {
					t.Fatalf("activation schedule projection: %+v", job)
				}
				if !tr.Enabled {
					return
				}
				if !job.NextRun.Equal(r.clock().Add(time.Minute)) {
					t.Fatalf("wrong next run: %v", job.NextRun)
				}
				due := *job.NextRun
				r.now = func() time.Time { return due }
				receipt, err := r.AdmitScheduledRun(ctx, draftActivationOccurrence(active, tr, due))
				if err != nil || receipt.Run.Status != store.RunQueued {
					t.Fatalf("consented recurrence admission: %+v %v", receipt, err)
				}
			})
		}
	}
}

func TestBotDraftScheduledActivationRejectsCorruption(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, scenario := range []string{"digest", "owner", "bot", "trigger", "version", "cron", "timezone", "payload", "missing", "enabled", "enabled-next", "next-run", "wrong-scope", "readiness", "transaction-binding"} {
			t.Run(string(scope)+"/"+scenario, func(t *testing.T) {
				r, ctx, b := draftActivationFixture(t, scope)
				b, tr := confirmDraftSchedule(t, r, ctx, b, store.BotControlScheduleCreate, store.TriggerSpec{Kind: store.TriggerCron, Cron: "* * * * *", Timezone: "Etc/UTC", Instructions: "Reviewed report", Enabled: true})
				p := draftActivationProposal(t, r, ctx, b, store.BotControlPayload{Operation: store.BotControlActivate, Reason: "Activate"})
				d := store.BotControlProposalDecision{BotID: b.ID, ProposalID: p.ID, Digest: p.Digest}
				jobs := r.db.(botTriggerDB).triggerScheduler()
				job := jobs.Get(ctx, tr.ScheduleID.String())
				calls := 0
				r.activationCheck = func(context.Context, store.Bot, store.BotVersion) error {
					calls++
					if scenario == "readiness" {
						return store.ErrBotUnsupported
					}
					if scenario == "transaction-binding" {
						job.Payload.BotVersion++
						return jobs.Update(ctx, job)
					}
					return nil
				}
				now := r.clock()
				switch scenario {
				case "digest":
					d.Digest = "wrong"
				case "owner":
					job.OwnerID = uuid.NewString()
				case "bot":
					job.Payload.BotID = uuid.NewString()
				case "trigger":
					job.Payload.BotTriggerID = uuid.NewString()
				case "version":
					job.Payload.BotVersion++
				case "cron":
					job.Schedule.Cron = "*/2 * * * *"
				case "timezone":
					job.Schedule.Timezone = "Europe/Berlin"
				case "payload":
					job.Payload.Instructions = "injected"
				case "enabled":
					job.Enabled = true
				case "enabled-next":
					job.Enabled = true
					job.NextRun = &now
				case "next-run":
					job.NextRun = &now
				case "wrong-scope":
					other := *b
					other.Scope = store.BotScopePersonal
					if scope == store.BotScopePersonal {
						other.Scope = store.BotScopeTeam
					}
					v, err := r.GetVersion(ctx, b.ID, b.Version)
					if err != nil {
						t.Fatal(err)
					}
					if err := validateActivationTriggers(ctx, r.db, other, *v); !errors.Is(err, store.ErrBotConflict) {
						t.Fatalf("foreign scope accepted: %v", err)
					}
					return
				}
				if scenario == "owner" {
					// Scheduler Update intentionally does not transfer ownership.
					if err := jobs.Remove(ctx, job.ID); err != nil {
						t.Fatal(err)
					}
					if err := jobs.Add(ctx, job); err != nil {
						t.Fatal(err)
					}
				}
				if scenario == "missing" {
					if err := jobs.Remove(ctx, job.ID); err != nil {
						t.Fatal(err)
					}
				} else if err := jobs.Update(ctx, job); err != nil {
					t.Fatal(err)
				}
				want := store.ErrBotConflict
				if scenario == "missing" {
					want = store.ErrBotNotFound
				}
				if scenario == "readiness" {
					want = store.ErrBotUnsupported
				}
				if _, err := r.ConfirmControlProposal(ctx, d); !errors.Is(err, want) {
					t.Fatalf("corruption accepted/wrong error: %v", err)
				}
				if (scenario == "readiness" || scenario == "transaction-binding") && calls != 1 {
					t.Fatalf("readiness never reached: %d", calls)
				}
				after, err := r.Get(ctx, b.ID)
				if err != nil || after.Status != store.BotDraft || after.Revision != b.Revision || after.Version != b.Version {
					t.Fatalf("failed activation changed draft: %+v %v", after, err)
				}
				saved, err := r.ReadControlProposal(ctx, b.ID, p.ID)
				if err != nil || saved.Status != store.BotControlProposed {
					t.Fatalf("failed activation consumed proposal: %+v %v", saved, err)
				}
			})
		}
	}
}
