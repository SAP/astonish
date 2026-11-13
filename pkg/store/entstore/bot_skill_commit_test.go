package entstore

import (
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"testing"
)

func TestBotAgentSkillCommitRequiresProposalEvidence(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			r, owner, worker, run, lease, _ := botRunFixture(t, scope)
			v := skillTestRevision(t, r, owner, run.BotID)
			w, err := r.LoadWork(worker, lease)
			if err != nil {
				t.Fatal(err)
			}
			_, err = r.OrientSkill(worker, store.BotSkillOrientation{Kind: store.BotSkillCreateDraft, Key: "report", Title: "Report", Intent: "Report", Draft: &v, Mutation: store.BotSkillMutation{BotID: run.BotID, Lease: lease, ExpectedRevision: w.Revision, IdempotencyKey: "orient"}})
			if err != nil {
				t.Fatal(err)
			}
			finishedAgentWorkReport(t, r, worker, lease, "Useful final", store.AgentWorkAnswered)
			w, err = r.LoadWork(worker, lease)
			if err != nil {
				t.Fatal(err)
			}
			next := v.Clone()
			next.ID, next.ParentRevision, next.ParentDigest = uuid.New(), v.Revision, v.Digest
			next.Revision++
			next.Method = []string{"Unrelated method without proposal evidence"}
			next.Digest = next.ContentDigest()
			evidence, _ := botHash(w.Candidate.AvailableEvidenceRefs())
			w, err = r.StageSkillLearning(worker, store.BotSkillLearningRequest{Mutation: store.BotSkillMutation{BotID: run.BotID, Lease: lease, ExpectedRevision: 1, IdempotencyKey: "learn"}, ExpectedWorkRevision: w.Revision, Learning: store.BotSkillLearning{SkillID: v.SkillID, ParentRevision: v.Revision, ParentDigest: v.Digest, SourceRunID: run.ID, CandidateDigest: w.Candidate.Digest, EvidenceDigest: evidence, Proposed: *next}})
			if err != nil {
				t.Fatal(err)
			}
			report := store.BotReport{Summary: "Useful final", Work: store.BuildBotWorkProjection(w)}
			report.FinalizeEmployeeMessage(store.RunSucceeded, w)
			if err := r.CommitRun(worker, store.BotRunCommit{Lease: lease, Status: store.RunSucceeded, Report: report}); err != nil {
				t.Fatal(err)
			}
			saved, err := r.GetRun(owner, run.BotID, run.ID)
			if err != nil || saved.Report.Skill == nil || saved.Report.Skill.Kind != store.BotSkillRejected {
				t.Fatalf("missing explicit method rejection: %+v %v", saved, err)
			}
			head, err := r.GetSkill(owner, run.BotID, v.SkillID)
			if err != nil || head.HeadRevision != 1 || head.Status != store.BotSkillDraft {
				t.Fatalf("unsupported method changed draft: %+v %v", head, err)
			}
		})
	}
}
func TestBotSkillCommitRejectsCallerReceipt(t *testing.T) {
	r, _, worker, run, lease, _ := botRunFixture(t, store.BotScopePersonal)
	err := r.CommitRun(worker, store.BotRunCommit{Lease: lease, Status: store.RunPartial, Report: store.BotReport{Summary: "Not verified", Skill: &store.BotSkillReceipt{SchemaVersion: 1, BotID: run.BotID, RunID: run.ID, SkillID: uuid.New(), Kind: store.BotSkillUpdated, CommittedRevision: 2}}})
	if err == nil {
		t.Fatal("caller minted a committed learning receipt")
	}
}

func TestBotSkillCommitStagedWithoutEvidenceDefers(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			r, owner, worker, run, lease, _ := botRunFixture(t, scope)
			pinLegacyWorkFixture(t, r, worker, lease)
			v := skillTestRevision(t, r, owner, run.BotID)
			w, err := r.LoadWork(worker, lease)
			if err != nil {
				t.Fatal(err)
			}
			oriented, err := r.OrientSkill(worker, store.BotSkillOrientation{Kind: store.BotSkillCreateDraft, Key: "report", Title: "Report", Intent: "Report", Draft: &v, Mutation: store.BotSkillMutation{BotID: run.BotID, Lease: lease, ExpectedRevision: w.Revision, IdempotencyKey: "orient"}})
			if err != nil {
				t.Fatal(err)
			}
			next := v.Clone()
			next.ID = uuid.New()
			next.Revision++
			next.ParentRevision = v.Revision
			next.ParentDigest = v.Digest
			next.Method = []string{"An untested alternative"}
			next.Digest = next.ContentDigest()
			source := v.Expectations[0].Provenance
			_, err = r.StageSkillLearning(worker, store.BotSkillLearningRequest{Mutation: store.BotSkillMutation{BotID: run.BotID, Lease: lease, ExpectedRevision: 1, IdempotencyKey: "learn", Source: source}, ExpectedWorkRevision: oriented.Work.Revision, Learning: store.BotSkillLearning{SkillID: v.SkillID, ParentRevision: v.Revision, ParentDigest: v.Digest, SourceRunID: run.ID, CandidateDigest: source.SourceDigest, EvidenceDigest: source.SourceDigest, Proposed: *next}})
			if err != nil {
				t.Fatal(err)
			}
			q := store.BotRunCommit{Lease: lease, Status: store.RunPartial, Report: store.BotReport{Summary: "No work observed"}}
			if err = r.CommitRun(worker, q); err != nil {
				t.Fatal(err)
			}
			saved, err := r.GetRun(owner, run.BotID, run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if saved.Report.Skill == nil || saved.Report.Skill.Kind != store.BotSkillDeferred {
				t.Fatalf("missing durable deferred receipt: %+v", saved.Report.Skill)
			}
			head, err := r.GetSkill(owner, run.BotID, v.SkillID)
			if err != nil || head.HeadRevision != 1 {
				t.Fatalf("unobserved learning committed: %+v %v", head, err)
			}
			if err = r.CommitRun(worker, q); err != nil {
				t.Fatalf("idempotent completion: %v", err)
			}
		})
	}
}
