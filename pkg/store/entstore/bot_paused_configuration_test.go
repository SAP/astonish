package entstore

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	botdomain "github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestBotPausedStoppedConfiguration(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopeTeam, store.BotScopePersonal} {
		t.Run(string(scope), func(t *testing.T) {
			for _, status := range []store.BotStatus{store.BotPaused, store.BotStopped} {
				t.Run(string(status), func(t *testing.T) {
					repo, ctx, worker, run, lease, _ := botRunFixture(t, scope)
					exerciseBotInactiveConfiguration(t, repo, ctx, worker, run, lease, status)
				})
			}
		})
	}
}

// Shared by SQLite and live PostgreSQL tests. Start with an executing Bot so the
// version edit also proves it cannot clear the lifecycle's shutdown barrier.
func exerciseBotInactiveConfiguration(t *testing.T, repo *botRepository, ctx, worker context.Context, run *store.BotRun, lease store.BotLease, status store.BotStatus) {
	t.Helper()
	b, err := repo.Get(ctx, run.BotID)
	if err != nil {
		t.Fatal(err)
	}
	original, err := repo.GetVersion(ctx, b.ID, b.Version)
	if err != nil {
		t.Fatal(err)
	}
	docs, err := botdomain.NewDocuments(repo)
	if err != nil {
		t.Fatal(err)
	}
	edit := botdomain.DocumentEdit{BotID: b.ID, ExpectedRevision: b.Revision, Name: botdomain.DocumentCharter, Content: "Corrected charter while inactive", Reason: "Owner correction", Confirmed: true}
	if _, err := docs.Edit(ctx, edit); !errors.Is(err, store.ErrBotUnsupported) {
		t.Fatalf("active edit: %v", err)
	}
	action := execution.ActionDescriptor{Tool: "send", Effect: execution.EffectSend, Recipient: "alice", Arguments: []byte(`{"body":"review"}`)}
	digest, err := action.Digest()
	if err != nil {
		t.Fatal(err)
	}
	approval, err := repo.RequestApproval(worker, lease, store.BotApproval{BotID: b.ID, RunID: run.ID, Version: b.Version, Action: action, ActionDigest: digest, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	// A persisted schedule is deliberately enabled before lifecycle quiescence.
	var scheduleEnabled func() bool
	switch d := repo.db.(type) {
	case teamBotDB:
		job, err := d.client.ScheduledJob.Create().SetName("configuration").SetSchedule("0 9 * * *").SetMode("bot_trigger").SetStatus("active").SetPayload(map[string]any{"bot_id": b.ID.String()}).Save(ctx)
		if err != nil {
			t.Fatal(err)
		}
		scheduleEnabled = func() bool {
			row, err := d.client.ScheduledJob.Get(ctx, job.ID)
			if err != nil {
				t.Fatal(err)
			}
			return row.Status == "active"
		}
	case personalBotDB:
		job, err := d.client.ScheduledJob.Create().SetName("configuration").SetSchedule("0 9 * * *").SetMode("bot_trigger").SetStatus("active").SetPayload(map[string]any{"bot_id": b.ID.String()}).Save(ctx)
		if err != nil {
			t.Fatal(err)
		}
		scheduleEnabled = func() bool {
			row, err := d.client.ScheduledJob.Get(ctx, job.ID)
			if err != nil {
				t.Fatal(err)
			}
			return row.Status == "active"
		}
	}
	q := store.BotLifecycleRequest{BotID: b.ID, ExpectedRevision: b.Revision, ActorID: b.OwnerID, Reason: "Edit configuration safely"}
	if status == store.BotPaused {
		b, err = repo.Pause(ctx, q)
	} else {
		b, err = repo.Stop(ctx, q)
	}
	if err != nil {
		t.Fatal(err)
	}
	before, err := repo.db.GetBot(ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if scheduleEnabled() {
		t.Fatal("lifecycle left schedule enabled")
	}
	if status == store.BotPaused {
		pending, err := repo.db.ReadApproval(ctx, b.ID, approval.ID)
		if err != nil || pending.Status != store.ApprovalPending {
			t.Fatalf("pause did not preserve undecided approval: %+v %v", pending, err)
		}
	}
	edit.ExpectedRevision = b.Revision
	edit.Confirmed = false
	if _, err := docs.Edit(ctx, edit); !errors.Is(err, store.ErrBotForbidden) {
		t.Fatalf("unconfirmed edit: %v", err)
	}
	edit.Confirmed = true
	ack, err := docs.Edit(ctx, edit)
	if err != nil {
		t.Fatal(err)
	}
	if ack.BotID != b.ID || ack.Version != b.Version+1 || ack.Revision != b.Revision+1 || ack.Document != edit.Name || ack.ActivityKind != store.BotActivityPersona {
		t.Fatalf("incorrect acknowledgment: %+v", ack)
	}
	after, err := repo.db.GetBot(ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Bot.Status != status || after.Fence != before.Fence || !reflect.DeepEqual(after.ActiveRunID, before.ActiveRunID) || after.LastSequence != before.LastSequence+1 {
		t.Fatalf("edit changed lifecycle/barrier: before=%+v after=%+v", before, after)
	}
	projection, err := docs.Load(ctx, b.ID, 0)
	if err != nil || projection.Version != ack.Version {
		t.Fatalf("projection: %+v %v", projection, err)
	}
	saved, err := repo.GetVersion(ctx, b.ID, ack.Version)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Charter != edit.Content || saved.CreatedBy != b.OwnerID || !reflect.DeepEqual(saved.Autonomy, original.Autonomy) || !reflect.DeepEqual(saved.Triggers, original.Triggers) {
		t.Fatalf("incorrect saved configuration: %+v", saved)
	}
	history, err := repo.GetVersion(ctx, b.ID, original.Version)
	if err != nil || !reflect.DeepEqual(history, original) {
		t.Fatalf("history modified: %+v %v", history, err)
	}
	activities, err := repo.ListActivity(ctx, b.ID, before.LastSequence, 10)
	if err != nil || len(activities) != 1 || activities[0].Kind != store.BotActivityPersona {
		t.Fatalf("configuration activity: %+v %v", activities, err)
	}
	var payload struct {
		Version int64     `json:"version"`
		Actor   uuid.UUID `json:"actor_id"`
	}
	if err := json.Unmarshal(activities[0].Payload, &payload); err != nil || payload.Version != ack.Version || payload.Actor != b.OwnerID {
		t.Fatalf("activity attribution: %+v %v", payload, err)
	}
	oldApproval, err := repo.db.ReadApproval(ctx, b.ID, approval.ID)
	if err != nil || oldApproval.Status != store.ApprovalRevoked {
		t.Fatalf("old approval not revoked: %+v %v", oldApproval, err)
	}
	if _, err := repo.DecideApproval(ctx, store.BotApprovalDecision{BotID: b.ID, ApprovalID: approval.ID, ActorID: b.OwnerID, ActionDigest: digest, Status: store.ApprovalApproved}); err == nil {
		t.Fatal("obsolete approval accepted")
	}
	if _, err := docs.Edit(ctx, edit); !errors.Is(err, store.ErrBotConflict) {
		t.Fatalf("stale version edit: %v", err)
	}
	mutations := []struct {
		name  string
		apply func(*store.BotVersion)
	}{
		{"authority", func(v *store.BotVersion) { v.Autonomy.CanPublish = !v.Autonomy.CanPublish }},
		{"trigger", func(v *store.BotVersion) {
			v.Triggers = []store.TriggerSpec{{ID: uuid.New(), Kind: store.TriggerCron, Enabled: true, Cron: "0 9 * * *"}}
		}},
	}
	for _, mutation := range mutations {
		next := *saved
		mutation.apply(&next)
		if _, err := repo.ApplyVersion(ctx, store.BotVersionRequest{BotID: b.ID, ExpectedRevision: ack.Revision, Version: next}); !errors.Is(err, store.ErrBotForbidden) {
			t.Fatalf("%s policy edit: %v", mutation.name, err)
		}
	}
	if scheduleEnabled() {
		t.Fatal("configuration edit re-enabled schedule")
	}
	if _, err := repo.RequestRun(ctx, store.BotRunRequest{BotID: b.ID, Version: ack.Version, Trigger: store.TriggerManual, IdempotencyKey: "after-edit", Instructions: "Do not execute"}); err == nil {
		t.Fatal("inactive Bot accepted run")
	}
	if err := repo.CommitRun(worker, store.BotRunCommit{Lease: lease, Status: store.RunSucceeded, Report: store.BotReport{Summary: "stale"}}); err == nil {
		t.Fatal("stale worker committed")
	}
	runs, err := repo.ListRuns(ctx, b.ID, store.BotPage{})
	if err != nil || len(runs) != 1 || runs[0].Status != store.RunInterrupted {
		t.Fatalf("edit executed work: %+v %v", runs, err)
	}
	personaEdit := edit
	personaEdit.ExpectedRevision = ack.Revision
	personaEdit.Name = botdomain.DocumentSoul
	personaEdit.Content = "Patient and precise"
	personaAck, err := docs.Edit(ctx, personaEdit)
	if err != nil {
		t.Fatal(err)
	}
	persona, err := repo.GetVersion(ctx, b.ID, personaAck.Version)
	if err != nil || persona.Persona.Soul != personaEdit.Content || persona.Charter != edit.Content || personaAck.Revision != ack.Revision+1 {
		t.Fatalf("persona correction: %+v %v", persona, err)
	}
	if scheduleEnabled() {
		t.Fatal("persona edit re-enabled schedule")
	}
	deleted, err := repo.BeginDelete(ctx, store.BotLifecycleRequest{BotID: b.ID, ExpectedRevision: personaAck.Revision, ActorID: b.OwnerID, Reason: "Done"})
	if err != nil {
		t.Fatal(err)
	}
	edit.ExpectedRevision = deleted.Revision
	if _, err := docs.Edit(ctx, edit); !errors.Is(err, store.ErrBotUnsupported) {
		t.Fatalf("deleted edit: %v", err)
	}
}
