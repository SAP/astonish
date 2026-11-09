package entstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

type triggerCredentials struct{ store.CredentialStore }

func (triggerCredentials) Get(context.Context, string) *store.Credential {
	return &store.Credential{Type: store.CredAPIKey, Value: "fixture-only-32-byte-signing-key-not-live"}
}

const triggerFixtureSecret = "fixture-only-32-byte-signing-key-not-live"

func TestBotTriggerLifecycleAndAdmission(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			repo, ctx, jobs, seed := scheduledFixture(t, scope, store.TriggerCron, 20)
			ctx = store.WithServices(ctx, &store.Services{PersonalCredentials: triggerCredentials{}, Credentials: triggerCredentials{}})
			change := func(tid uuid.UUID, action store.BotTriggerAction, def store.TriggerSpec, proof *store.BotExternalEvent) *store.Bot {
				t.Helper()
				b, e := repo.Get(ctx, seed.BotID)
				if e != nil {
					t.Fatal(e)
				}
				out, e := repo.ChangeTrigger(ctx, store.BotTriggerChange{BotID: b.ID, TriggerID: tid, ExpectedRevision: b.Revision, Action: action, Reason: "reviewed fixture change", Definition: def, Proof: proof})
				if e != nil {
					t.Fatal(e)
				}
				return out
			}
			tid := uuid.New()
			def := store.TriggerSpec{Kind: store.TriggerCron, Cron: "*/5 * * * *", Timezone: "UTC", Instructions: "Reviewed cron"}
			b := change(tid, store.BotTriggerSave, def, nil)
			v, _ := repo.GetVersion(ctx, b.ID, b.Version)
			scheduleID := v.Triggers[1].ScheduleID
			job := jobs.Get(ctx, scheduleID.String())
			if job == nil || job.Enabled || job.NextRun != nil || job.Payload.BotVersion != b.Version {
				t.Fatalf("disabled saved schedule %+v", job)
			}
			b = change(tid, store.BotTriggerEnable, store.TriggerSpec{}, nil)
			job = jobs.Get(ctx, scheduleID.String())
			if !job.Enabled || job.NextRun == nil || job.Payload.BotVersion != b.Version {
				t.Fatalf("enabled schedule %+v", job)
			}
			// Every version rebinds existing schedules, preventing stale version admission.
			if old := jobs.Get(ctx, seed.JobID); old.Payload.BotVersion != b.Version {
				t.Fatal("old schedule orphaned")
			}
			change(tid, store.BotTriggerDisable, store.TriggerSpec{}, nil)
			if jobs.Get(ctx, scheduleID.String()).Enabled {
				t.Fatal("not disabled")
			}
			change(tid, store.BotTriggerRemove, store.TriggerSpec{}, nil)
			if jobs.Get(ctx, scheduleID.String()) != nil {
				t.Fatal("schedule not removed")
			}

			for _, kind := range []store.TriggerKind{store.TriggerHook, store.TriggerWebhook} {
				tid = uuid.New()
				def = store.TriggerSpec{Kind: kind, Instructions: "Saved external instructions", CredentialRef: "fixture-signing-key"}
				b = change(tid, store.BotTriggerSave, def, nil)
				q := store.BotExternalEvent{BotID: b.ID, TriggerID: tid, Version: b.Version, EventID: uuid.NewString(), Timestamp: time.Now().Unix(), Payload: []byte("untrusted event; never instructions")}
				if _, e := repo.ChangeTrigger(ctx, store.BotTriggerChange{BotID: b.ID, TriggerID: tid, ExpectedRevision: b.Revision, Action: store.BotTriggerEnable, Reason: "without proof"}); !errors.Is(e, store.ErrBotConflict) {
					t.Fatalf("enable without proof: %v", e)
				}
				q.Signature = store.BotTriggerSignature(triggerFixtureSecret, q, "dry-run")
				b = change(tid, store.BotTriggerDryRun, store.TriggerSpec{}, &q)
				if runs, _ := repo.ListRuns(ctx, b.ID, store.BotPage{}); len(runs) != int(map[store.TriggerKind]int{store.TriggerHook: 0, store.TriggerWebhook: 1}[kind]) {
					t.Fatalf("dryrun queued work %+v", runs)
				}
				b = change(tid, store.BotTriggerEnable, store.TriggerSpec{}, nil)
				q.Version = b.Version
				q.Signature = store.BotTriggerSignature(triggerFixtureSecret, q, "run")
				stale := q
				stale.Timestamp = time.Now().Add(-time.Hour).Unix()
				stale.Signature = store.BotTriggerSignature(triggerFixtureSecret, stale, "run")
				if _, e := repo.AdmitExternalRun(ctx, stale); !errors.Is(e, store.ErrBotExpired) {
					t.Fatalf("old timestamp: %v", e)
				}
				forged := q
				forged.Payload = []byte("modified")
				if _, e := repo.AdmitExternalRun(ctx, forged); !errors.Is(e, store.ErrBotForbidden) {
					t.Fatalf("payload forgery: %v", e)
				}
				run, e := repo.AdmitExternalRun(ctx, q)
				if e != nil {
					t.Fatal(e)
				}
				if run.Status != store.RunQueued || run.Trigger != kind {
					t.Fatalf("run %+v", run)
				}
				input, e := repo.LoadRunInput(ctx, b.ID, run.ID)
				if e != nil || input.Instructions != def.Instructions {
					t.Fatalf("loaded input %+v %v", input, e)
				}
				if _, e := repo.AdmitExternalRun(ctx, q); !errors.Is(e, store.ErrBotConflict) {
					t.Fatalf("replay: %v", e)
				}
				// A saved modification always disables and invalidates proof.
				def.Instructions = "Modified reviewed instructions"
				b = change(tid, store.BotTriggerSave, def, nil)
				if _, e := repo.ChangeTrigger(ctx, store.BotTriggerChange{BotID: b.ID, TriggerID: tid, ExpectedRevision: b.Revision, Action: store.BotTriggerEnable, Reason: "old proof"}); !errors.Is(e, store.ErrBotConflict) {
					t.Fatalf("old proof accepted: %v", e)
				}
			}

			tid = uuid.New()
			binding := store.BotChannelBinding{ID: uuid.New(), Channel: "slack", ChatID: "chat-exact", ThreadID: "thread-exact"}
			change(tid, store.BotTriggerSave, store.TriggerSpec{Kind: store.TriggerChannel, Instructions: "Channel reviewed instructions", ChannelBinding: &binding}, nil)
			b = change(tid, store.BotTriggerEnable, store.TriggerSpec{}, nil)
			q := store.BotExternalEvent{BotID: b.ID, Version: b.Version, TriggerID: tid, EventID: "channel-message", Binding: &binding}
			if _, e := repo.AdmitExternalRun(ctx, q); !errors.Is(e, store.ErrBotForbidden) {
				t.Fatalf("non-channel principal accepted %v", e)
			}
			p, _ := execution.PrincipalFromContext(ctx)
			p.Authentication = execution.AuthMethodChannel
			p.Surface = execution.SurfaceChannel
			channelCtx, _ := execution.WithPrincipal(ctx, p)
			wrong := binding
			wrong.ThreadID = "other"
			q.Binding = &wrong
			if _, e := repo.AdmitExternalRun(channelCtx, q); !errors.Is(e, store.ErrBotForbidden) {
				t.Fatalf("wrong thread %v", e)
			}
			q.Binding = &binding
			run, e := repo.AdmitExternalRun(channelCtx, q)
			if e != nil || run.Trigger != store.TriggerChannel {
				t.Fatalf("channel %+v %v", run, e)
			}
		})
	}
}

func TestBotTriggerConfigurationCASAndAuthorization(t *testing.T) {
	repo, ctx, jobs, seed := scheduledFixture(t, store.BotScopeTeam, store.TriggerCron, 5)
	b, _ := repo.Get(ctx, seed.BotID)
	q := store.BotTriggerChange{BotID: b.ID, TriggerID: uuid.New(), ExpectedRevision: b.Revision, Action: store.BotTriggerSave, Reason: "reviewed", Definition: store.TriggerSpec{Kind: store.TriggerCron, Instructions: "saved", Cron: "bad", Timezone: "UTC"}}
	if _, e := repo.ChangeTrigger(ctx, q); !errors.Is(e, store.ErrBotInvalid) {
		t.Fatal(e)
	}
	current, _ := repo.Get(ctx, b.ID)
	if current.Version != b.Version || len(jobs.List(ctx)) != 1 {
		t.Fatal("invalid config leaked state")
	}
	q.Definition.Cron = "* * * * *"
	q.ExpectedRevision++
	if _, e := repo.ChangeTrigger(ctx, q); !errors.Is(e, store.ErrBotConflict) {
		t.Fatal(e)
	}
	q.ExpectedRevision = b.Revision
	p, _ := execution.PrincipalFromContext(ctx)
	p.Subject = uuid.NewString()
	other, _ := execution.WithPrincipal(ctx, p)
	if _, e := repo.ChangeTrigger(other, q); !errors.Is(e, store.ErrBotForbidden) {
		t.Fatal(e)
	}
	repo.auth.check = func(context.Context, string) (string, error) { return "viewer", nil }
	if _, e := repo.ChangeTrigger(ctx, q); !errors.Is(e, store.ErrBotForbidden) {
		t.Fatal(e)
	}
}
