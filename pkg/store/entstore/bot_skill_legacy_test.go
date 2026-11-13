package entstore

import (
	"errors"
	"reflect"
	"testing"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestBotSkillLegacyConversion(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			r, owner, worker, run, lease, _ := botRunFixture(t, scope)
			pinLegacyWorkFixture(t, r, worker, lease)
			if err := r.CommitRun(worker, store.BotRunCommit{Lease: lease, Status: store.RunFailed, Report: store.BotReport{Summary: "Finished", Unverified: []string{"No work performed"}}}); err != nil {
				t.Fatal(err)
			}
			b, err := r.Get(owner, run.BotID)
			if err != nil {
				t.Fatal(err)
			}
			trigger := store.TriggerSpec{ID: uuid.New(), Kind: store.TriggerCron, Instructions: "Read the public release notes and cite their links", Cron: "0 8 * * *", Timezone: "UTC"}
			b, err = r.ChangeTrigger(owner, store.BotTriggerChange{BotID: b.ID, TriggerID: trigger.ID, ExpectedRevision: b.Revision, Action: store.BotTriggerSave, Reason: "legacy schedule", Definition: trigger})
			if err != nil {
				t.Fatal(err)
			}
			before, err := r.GetVersion(owner, b.ID, b.Version)
			if err != nil {
				t.Fatal(err)
			}
			q := store.BotSkillLegacyConversion{Mutation: store.BotSkillMutation{BotID: b.ID, IdempotencyKey: "import"}, ExpectedBotRevision: b.Revision, TriggerID: trigger.ID, Version: b.Version, InstructionsDigest: skillSHA(trigger.Instructions)}
			bad := q
			bad.InstructionsDigest = skillSHA("wrong")
			if _, err = r.ConvertLegacySkill(owner, bad); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("digest mismatch: %v", err)
			}
			list, err := r.ListSkills(owner, b.ID, store.BotSkillPage{})
			if err != nil || len(list.Skills) != 0 {
				t.Fatalf("failed import wrote data: %v", err)
			}
			if _, err = r.ConvertLegacySkill(worker, q); !errors.Is(err, store.ErrBotForbidden) {
				t.Fatalf("worker imported: %v", err)
			}
			skill, err := r.ConvertLegacySkill(owner, q)
			if err != nil {
				t.Fatal(err)
			}
			if skill.Status != store.BotSkillDraft || skill.Legacy == nil || skill.Legacy.TriggerID != trigger.ID {
				t.Fatalf("unsafe import: %+v", skill)
			}
			revision, err := r.GetSkillRevision(owner, b.ID, skill.ID, 1)
			if err != nil || revision.Validate() != nil {
				t.Fatalf("invalid revision: %+v %v", revision, err)
			}
			if revision.Provenance[0].Level == store.BotSkillObserved || revision.Expectations[0].Provenance.Level == store.BotSkillOwnerSpecified {
				t.Fatal("legacy content upgraded to owner instruction or observed knowledge")
			}
			again, err := r.ConvertLegacySkill(owner, q)
			if err != nil || again.ID != skill.ID {
				t.Fatalf("replay: %v", err)
			}
			after, err := r.GetVersion(owner, b.ID, b.Version)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("import changed version: %v", err)
			}
			current, err := r.Get(owner, b.ID)
			if err != nil || current.Revision != b.Revision {
				t.Fatalf("import changed Bot revision: %v", err)
			}
			foreign := q
			foreign.Mutation.BotID = uuid.New()
			if _, err = r.ConvertLegacySkill(owner, foreign); err == nil {
				t.Fatal("foreign import accepted")
			}
		})
	}
}
