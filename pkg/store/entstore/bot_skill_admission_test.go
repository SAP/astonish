package entstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

type skillAdmissionRunDB struct {
	botDraftDB
	mutate func(*botStoredRun)
}

func (d skillAdmissionRunDB) ReadRun(ctx context.Context, b, id uuid.UUID) (*botStoredRun, error) {
	run, err := d.botDraftDB.ReadRun(ctx, b, id)
	if err == nil {
		d.mutate(run)
	}
	return run, err
}

func TestBotSkillRunAdmissionProvenance(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			r, owner, worker, run, lease, _ := botRunFixture(t, scope)
			// A delegated worker may carry the owner's principal; that is not
			// authenticated original user admission for durable expectations.
			delegated, err := r.RequestRun(worker, store.BotRunRequest{BotID: run.BotID, Version: run.Version, Trigger: store.TriggerManual, IdempotencyKey: "worker-request", Instructions: "Worker-generated task"})
			if err == nil {
				saved, readErr := r.db.ReadRun(owner, run.BotID, delegated.ID)
				if readErr != nil || saved.SkillAdmissionOwner != nil {
					t.Fatalf("worker instruction certified: %+v %v", saved, readErr)
				}
			} else if !errors.Is(err, store.ErrBotForbidden) {
				t.Fatal(err)
			}
			w, err := r.LoadWork(worker, lease)
			if err != nil {
				t.Fatal(err)
			}
			p := store.BotSkillProvenance{Source: store.BotSkillSourceRunAdmission, Level: store.BotSkillOwnerSpecified, BotID: run.BotID, RunID: run.ID, SessionID: run.SessionID, SourceDigest: skillSHA("Observe"), Quote: "Observe", RecordedAt: run.CreatedAt}
			if err := resolveSkillSource(worker, r.db, p, run.BotID); err != nil {
				t.Fatal(err)
			}
			for name, mutate := range map[string]func(*store.BotSkillProvenance){
				"digest":     func(p *store.BotSkillProvenance) { p.SourceDigest = skillSHA("prompt augmentation") },
				"quote":      func(p *store.BotSkillProvenance) { p.Quote = "invented requirement" },
				"timestamp":  func(p *store.BotSkillProvenance) { p.RecordedAt = p.RecordedAt.Add(time.Second) },
				"session":    func(p *store.BotSkillProvenance) { p.SessionID = uuid.New() },
				"run":        func(p *store.BotSkillProvenance) { p.RunID = uuid.New() },
				"bot":        func(p *store.BotSkillProvenance) { p.BotID = uuid.New() },
				"transcript": func(p *store.BotSkillProvenance) { p.EventID = "forged-user" },
			} {
				t.Run(name, func(t *testing.T) {
					bad := p
					mutate(&bad)
					if resolveSkillSource(worker, r.db, bad, run.BotID) == nil {
						t.Fatal("forged source accepted")
					}
				})
			}
			for name, mutate := range map[string]func(*botStoredRun){
				"not-admitted":         func(s *botStoredRun) { s.SkillAdmissionOwner = nil },
				"no-work":              func(s *botStoredRun) { s.Work = nil },
				"continuation":         func(s *botStoredRun) { s.Run.ContinuationReason = store.BotContinuationReason("test-continuation") },
				"mandate":              func(s *botStoredRun) { s.Run.MandateRef = &store.BotMandateRef{} },
				"foreign-owner":        func(s *botStoredRun) { id := uuid.New(); s.SkillAdmissionOwner = &id },
				"scheduled":            func(s *botStoredRun) { s.Run.Trigger = store.TriggerCron },
				"legacy":               func(s *botStoredRun) { s.Work.Protocol = "" },
				"transcript-admission": func(s *botStoredRun) { s.Run.UserEventID = "real-user" },
				"changed-instructions": func(s *botStoredRun) { s.Instructions = "Observe augmented" },
			} {
				t.Run(name, func(t *testing.T) {
					if resolveSkillSource(worker, skillAdmissionRunDB{r.db, mutate}, p, run.BotID) == nil {
						t.Fatal("noncanonical admission accepted")
					}
				})
			}
			v := store.BotSkillRevision{SchemaVersion: store.BotSkillSchemaVersion, ID: uuid.New(), BotID: run.BotID, SkillID: uuid.New(), Revision: 1, Responsibility: "Observe", Expectations: []store.BotSkillExpectation{{ID: "assignment", Text: p.Quote, Provenance: p}}, Method: []string{"Inspect the requested source"}, Provenance: []store.BotSkillProvenance{{Level: store.BotSkillProposed, BotID: run.BotID}}, ChangeReason: "Initial method proposal"}
			v.Digest = v.ContentDigest()
			q := store.BotSkillOrientation{Kind: store.BotSkillCreateDraft, Key: "admission", Title: "Observe", Intent: "Observe", Draft: &v, Mutation: store.BotSkillMutation{BotID: run.BotID, Lease: lease, ExpectedRevision: w.Revision, IdempotencyKey: "admission"}}
			bad := q
			bad.Mutation.Lease.Fence++
			if _, err := r.OrientSkill(worker, bad); err == nil {
				t.Fatal("stale lease admitted")
			}
			result, err := r.OrientSkill(worker, q)
			if err != nil {
				t.Fatal(err)
			}
			if result.Work.Review != nil || result.Work.Phase != store.BotWorkWorking || !result.Work.SkillState.CreatedDuringWork {
				t.Fatalf("fabricated prior skill use/review: %+v", result.Work)
			}
			got, err := r.GetSkillRevision(owner, run.BotID, v.SkillID, 1)
			if err != nil || got.Expectations[0].Provenance.Source != store.BotSkillSourceRunAdmission {
				t.Fatalf("source lost: %+v %v", got, err)
			}
		})
	}
}
