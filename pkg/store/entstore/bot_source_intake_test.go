package entstore

import (
	"context"
	"errors"
	"testing"
	"time"

	personal "github.com/SAP/astonish/ent/personal"
	team "github.com/SAP/astonish/ent/team"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestBotSourceIntakeEventEditInvalidates(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			ctx, _, _, _, reopen := ApprovalScopeReopenFixture(t, scope)
			bots, sessions := reopen()
			principal, _ := execution.PrincipalFromContext(ctx)
			owner := uuid.MustParse(principal.Subject)
			sid := uuid.New()
			if err := sessions.AddSessionMeta(ctx, store.SessionMeta{ID: sid.String(), UserID: owner.String(), Purpose: store.SessionPurposeChat, CreatedAt: time.Now(), UpdatedAt: time.Now()}); err != nil {
				t.Fatal(err)
			}
			var edit func()
			switch b := bots.(type) {
			case *personalBotStore:
				e, err := b.client.SessionEvent.Create().SetSessionID(sid.String()).SetEventData(map[string]any{"text": "private transcript"}).Save(ctx)
				if err != nil {
					t.Fatal(err)
				}
				edit = func() {
					if err := b.client.SessionEvent.UpdateOneID(e.ID).SetEventData(map[string]any{"text": "edited"}).Exec(ctx); err != nil {
						t.Fatal(err)
					}
				}
			case *teamBotStore:
				e, err := b.client.SessionEvent.Create().SetSessionID(sid.String()).SetEventData(map[string]any{"text": "private transcript"}).Save(ctx)
				if err != nil {
					t.Fatal(err)
				}
				edit = func() {
					if err := b.client.SessionEvent.UpdateOneID(e.ID).SetEventData(map[string]any{"text": "edited"}).Exec(ctx); err != nil {
						t.Fatal(err)
					}
				}
			}
			source := bots.(store.BotSourceIntakeStore)
			p, err := source.ProposeSourceIntake(ctx, store.BotSourceIntakeRequest{Mode: store.BotIntakeChat, SourceID: sid, Name: "Selected", Charter: "Observe", Carryover: store.BotCarryover{Summary: "redacted selection"}, IdempotencyKey: "events"}, nil)
			if err != nil {
				t.Fatal(err)
			}
			edit()
			result, err := source.DecideSourceIntake(ctx, p.ID, store.BotSourceIntakeDecision{Digest: p.Digest, Decision: "confirm", ReviewedForTeam: true}, nil, nil)
			if err != nil || result.Status != store.BotSourceIntakeStale || result.BotID != nil {
				t.Fatalf("edited event accepted: %+v %v", result, err)
			}
		})
	}
}

func TestBotSourceIntakeDurabilityAndReceiptRollback(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			ctx, _, _, _, reopen := ApprovalScopeReopenFixture(t, scope)
			bots, _ := reopen()
			principal, _ := execution.PrincipalFromContext(ctx)
			owner := uuid.MustParse(principal.Subject)
			var jobs store.SchedulerStore
			fail := false
			switch b := bots.(type) {
			case *personalBotStore:
				jobs = personalBotDB{client: b.client}.triggerScheduler()
				b.client.BotIntake.Use(func(next personal.Mutator) personal.Mutator {
					return personal.MutateFunc(func(ctx context.Context, m personal.Mutation) (personal.Value, error) {
						if fail {
							return nil, errors.New("receipt failure")
						}
						return next.Mutate(ctx, m)
					})
				})
			case *teamBotStore:
				jobs = teamBotDB{client: b.client}.triggerScheduler()
				b.client.BotIntake.Use(func(next team.Mutator) team.Mutator {
					return team.MutateFunc(func(ctx context.Context, m team.Mutation) (team.Value, error) {
						if fail {
							return nil, errors.New("receipt failure")
						}
						return next.Mutate(ctx, m)
					})
				})
			}
			job := &store.ScheduledJob{Name: "Old adaptive", Mode: "adaptive", Enabled: true, OwnerID: owner.String(), Schedule: store.JobSchedule{Cron: "0 8 * * *", Timezone: "UTC"}, Payload: store.JobPayload{Instructions: "Watch"}}
			if err := jobs.Add(ctx, job); err != nil {
				t.Fatal(err)
			}
			source := bots.(store.BotSourceIntakeStore)
			p, err := source.ProposeSourceIntake(ctx, store.BotSourceIntakeRequest{Mode: store.BotIntakeAdaptive, SourceID: uuid.MustParse(job.ID), Name: "Replacement", Charter: "Observe", IdempotencyKey: "receipt", SkillSeed: testIntakeSkillSeed()}, nil)
			if err != nil {
				t.Fatal(err)
			}
			q := store.BotSourceIntakeDecision{Digest: p.Digest, Decision: "confirm", ReviewedForTeam: true}
			worker := execution.WithBotRunIdentity(ctx, execution.BotRunIdentity{BotID: uuid.New(), RunID: uuid.New(), Version: 1, ActorID: owner.String(), OrgSlug: "org", TeamSlug: "team"})
			if _, err := source.DecideSourceIntake(worker, p.ID, q, nil, nil); !errors.Is(err, store.ErrBotForbidden) {
				t.Fatalf("worker self consent: %v", err)
			}
			fail = true
			if _, err := source.DecideSourceIntake(ctx, p.ID, q, nil, nil); err == nil {
				t.Fatal("missing receipt failure")
			}
			fail = false
			saved, err := source.GetSourceIntake(ctx, p.ID)
			if err != nil || saved.Status != store.BotSourceIntakeProposed || saved.BotID != nil {
				t.Fatalf("ambiguous receipt: %+v %v", saved, err)
			}
			if !jobs.Get(ctx, job.ID).Enabled {
				t.Fatal("original paused after rollback")
			}
			var db botDraftDB
			switch b := bots.(type) {
			case *personalBotStore:
				db = personalBotDB{client: b.client}
			case *teamBotStore:
				db = teamBotDB{client: b.client}
			}
			switch d := db.(type) {
			case personalBotDB:
				if d.client.BotSkill.Query().CountX(ctx) != 0 || d.client.BotSkillRevision.Query().CountX(ctx) != 0 {
					t.Fatal("skills escaped receipt rollback")
				}
			case teamBotDB:
				if d.client.BotSkill.Query().CountX(ctx) != 0 || d.client.BotSkillRevision.Query().CountX(ctx) != 0 {
					t.Fatal("skills escaped receipt rollback")
				}
			}
			key, _ := botHash([]string{owner.String(), sourceDraftKey(p)})
			if _, err := db.FindCreate(ctx, key); !errors.Is(err, store.ErrBotNotFound) {
				t.Fatalf("replacement escaped rollback: %v", err)
			}
			bots, _ = reopen()
			source = bots.(store.BotSourceIntakeStore)
			applied, err := source.DecideSourceIntake(ctx, p.ID, q, nil, nil)
			if err != nil || applied.Status != store.BotSourceIntakeApplied {
				t.Fatalf("retry %+v %v", applied, err)
			}
			bots, _ = reopen()
			source = bots.(store.BotSourceIntakeStore)
			replay, err := source.DecideSourceIntake(ctx, p.ID, q, nil, nil)
			if err != nil || replay.BotID == nil || *replay.BotID != *applied.BotID {
				t.Fatalf("reopen receipt %+v %v", replay, err)
			}
			b, err := bots.Get(ctx, *replay.BotID)
			if err != nil {
				t.Fatal(err)
			}
			assertIntakeSeed(t, ctx, bots, b)
		})
	}
}
