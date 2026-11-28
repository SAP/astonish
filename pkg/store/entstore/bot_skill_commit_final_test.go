package entstore

import (
	"errors"
	"reflect"
	"testing"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestBotAgentSkillLearningCommitPreservesFinal(t *testing.T) {
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
			next.Digest = next.ContentDigest()
			evidence, _ := botHash(w.Candidate.AvailableEvidenceRefs())
			w, err = r.StageSkillLearning(worker, store.BotSkillLearningRequest{Mutation: store.BotSkillMutation{BotID: run.BotID, Lease: lease, ExpectedRevision: 1, IdempotencyKey: "learn"}, ExpectedWorkRevision: w.Revision, Learning: store.BotSkillLearning{SkillID: v.SkillID, ParentRevision: v.Revision, ParentDigest: v.Digest, SourceRunID: run.ID, CandidateDigest: w.Candidate.Digest, EvidenceDigest: evidence, Proposed: *next}})
			if err != nil {
				t.Fatal(err)
			}
			report := store.BotReport{Summary: "Useful final", Work: store.BuildBotWorkProjection(w)}
			report.FinalizeEmployeeMessage(store.RunSucceeded, w)
			if err := report.ValidateEmployeeMessage(store.RunSucceeded, w); err != nil {
				t.Fatal(err)
			}
			q := store.BotRunCommit{Lease: lease, Status: store.RunSucceeded, Report: report}
			original, _ := botHash(q)
			// Even a trusted final cannot be rebound across unrelated work/report
			// changes masquerading as the learning revision increment.
			after := w.Clone()
			after.Revision++
			after.SkillState.Stage = store.BotSkillLearningCommitted
			after.SkillState.Receipt = &store.BotSkillReceipt{SchemaVersion: 1, BotID: run.BotID, RunID: run.ID, SkillID: v.SkillID, Kind: store.BotSkillUnchanged, UsedRevision: v.Revision, CommittedRevision: v.Revision, Digest: v.Digest}
			for _, mutate := range []func(*store.BotRunCommit, *store.BotWorkState){
				func(q *store.BotRunCommit, w *store.BotWorkState) { w.Revision++ },
				func(q *store.BotRunCommit, w *store.BotWorkState) { w.AgentWork.Notes += "unrelated change" },
				func(q *store.BotRunCommit, w *store.BotWorkState) { w.Candidate.Text += "forged" },
				func(q *store.BotRunCommit, w *store.BotWorkState) { q.Report.Summary += "forged" },
			} {
				changed, nextWork := q, after.Clone()
				changed.Report.Skill = nextWork.SkillState.Receipt
				mutate(&changed, nextWork)
				if err := rebindBotSkillReport(&changed, report, w, nextWork); !errors.Is(err, store.ErrBotInvalid) {
					t.Fatalf("unrelated transition accepted: %v", err)
				}
			}
			for _, mutate := range []func(*store.BotReport){
				func(r *store.BotReport) { p := *r.Work; p.WorkRevision++; r.Work = &p },
				func(r *store.BotReport) { r.Summary += " forged" },
				func(r *store.BotReport) { m := *r.EmployeeMessage; m.SourceDigest = "forged"; r.EmployeeMessage = &m },
			} {
				forged := q
				mutate(&forged.Report)
				if err := r.CommitRun(worker, forged); !errors.Is(err, store.ErrBotInvalid) {
					t.Fatalf("forged mismatch accepted: %v", err)
				}
			}
			if err := r.CommitRun(worker, q); err != nil {
				t.Fatal(err)
			}
			saved, err := r.GetRun(owner, run.BotID, run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if saved.Report.Skill == nil || saved.Report.Skill.Kind != store.BotSkillUnchanged {
				t.Fatalf("learning not committed: %+v", saved.Report.Skill)
			}
			if saved.Report.EmployeeMessage == nil || saved.Report.EmployeeMessage.Origin != store.BotEmployeeMessageAgent || saved.Report.EmployeeMessage.Text != report.Summary {
				t.Fatalf("final replaced: %+v", saved.Report.EmployeeMessage)
			}
			if !reflect.DeepEqual(saved.Report.Work, saved.Work) || saved.Report.Work.WorkRevision != w.Revision+1 {
				t.Fatal("stale final work projection")
			}
			if err := saved.Report.ValidateEmployeeMessageIntegrity(saved.Status); err != nil {
				t.Fatal(err)
			}
			unchanged, _ := botHash(q)
			if unchanged != original {
				t.Fatal("commit mutated caller request")
			}
			if err := r.CommitRun(worker, q); err != nil {
				t.Fatalf("exact retry: %v", err)
			}
			for i := 0; i < 2; i++ {
				p, err := r.PublishReport(owner, store.BotPublicationRequest{BotID: run.BotID, RunID: run.ID})
				if err != nil || p.Text != report.Summary {
					t.Fatalf("final publication: %+v %v", p, err)
				}
			}
		})
	}
}
