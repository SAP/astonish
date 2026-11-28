package entstore

import (
	"errors"
	"testing"

	"github.com/SAP/astonish/pkg/store"
)

// Personal production repositories are org/user scoped: unlike botRunFixture,
// personalDataStore.Bots does not set auth.teamSlug. Routing is authenticated
// per request and is persisted on the schedule.
func TestBotControlProposalPersonalScheduleRequestTeam(t *testing.T) {
	for _, wrongTeam := range []bool{false, true} {
		name := "matching_authenticated_team"
		if wrongTeam {
			name = "foreign_schedule_team"
		}
		t.Run(name, func(t *testing.T) {
			r, ctx, b, schedule := scheduledControlSafety(t, store.BotScopePersonal)
			r.auth.teamSlug = ""
			text := "Track reviewed browser notices"
			p := pendingControlSafety(t, r, ctx, b, store.BotControlPayload{Operation: store.BotControlResponsibility, Reason: "owner review", Responsibility: &text})
			// Establish that digest restoration and the busy/version gates are valid.
			saved, err := r.ReadControlProposal(ctx, b.ID, p.ID)
			if err != nil || saved.Digest != p.Digest {
				t.Fatalf("saved proposal: %+v %v", saved, err)
			}
			rec, err := r.db.GetBot(ctx, b.ID)
			if err != nil || rec.ActiveRunID != nil || rec.Bot.Revision != p.ExpectedRevision || rec.Bot.Version != p.Version {
				t.Fatalf("proposal preconditions: %+v %v", rec, err)
			}
			jobs := r.db.(botTriggerDB).triggerScheduler()
			job := jobs.Get(ctx, schedule)
			if wrongTeam {
				job.TeamSlug = "foreign"
				if err := jobs.Update(ctx, job); err != nil {
					t.Fatal(err)
				}
			}
			decision := store.BotControlProposalDecision{BotID: b.ID, ProposalID: p.ID, Digest: p.Digest}
			applied, err := r.ConfirmControlProposal(ctx, decision)
			if wrongTeam {
				if !errors.Is(err, store.ErrBotConflict) {
					t.Fatalf("foreign schedule accepted: %+v %v", applied, err)
				}
				return
			}
			if err != nil || applied.Status != store.BotControlApplied {
				t.Fatalf("matching personal schedule confirmation: %+v %v", applied, err)
			}
			replay, err := r.ConfirmControlProposal(ctx, decision)
			if err != nil || !replay.DecidedAt.Equal(*applied.DecidedAt) {
				t.Fatalf("replay: %+v %v", replay, err)
			}
			after, err := r.Get(ctx, b.ID)
			if err != nil || after.Version != b.Version+1 {
				t.Fatalf("version: %+v %v", after, err)
			}
			version, err := r.GetVersion(ctx, b.ID, after.Version)
			if err != nil || version.Charter != text {
				t.Fatalf("responsibility: %+v %v", version, err)
			}
			updated := jobs.Get(ctx, schedule)
			if updated.TeamSlug != job.TeamSlug || updated.Payload.BotVersion != after.Version || updated.NextRun == nil || !updated.NextRun.Equal(*job.NextRun) {
				t.Fatalf("schedule changed: before=%+v after=%+v", job, updated)
			}
		})
	}
}
