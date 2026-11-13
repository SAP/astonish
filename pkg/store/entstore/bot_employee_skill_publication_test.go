package entstore

import (
	"context"
	"testing"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
)

func TestEmployeeModelSkillReceiptPublication(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			r, owner, worker, run, lease, _ := botRunFixture(t, scope)
			// Historical model-reviewed publication retains its original protocol.
			pinLegacyWorkFixture(t, r, worker, lease)
			v := skillTestRevision(t, r, owner, run.BotID)
			w, err := r.LoadWork(worker, lease)
			if err != nil {
				t.Fatal(err)
			}
			_, err = r.OrientSkill(worker, store.BotSkillOrientation{Kind: store.BotSkillCreateDraft, Key: "report", Title: "Report", Intent: "Report", Draft: &v, Mutation: store.BotSkillMutation{BotID: run.BotID, Lease: lease, ExpectedRevision: w.Revision, IdempotencyKey: "orient"}})
			if err != nil {
				t.Fatal(err)
			}
			report := store.BotReport{Summary: "Private rejected candidate"}
			m := bot.ComposeEmployeeMessage(worker, store.RunPartial, report, nil, bot.EmployeeMessageOptions{LLM: employeeCommitModel{}, Boundary: func(context.Context) error { return nil }})
			if m.Origin != store.BotEmployeeMessageModel {
				t.Fatal("no model review")
			}
			report.EmployeeMessage = &m
			q := store.BotRunCommit{Lease: lease, Status: store.RunPartial, Report: report}
			forged := q
			forged.Report.Skill = &store.BotSkillReceipt{SchemaVersion: 1, BotID: run.BotID, RunID: run.ID, SkillID: v.SkillID, Kind: store.BotSkillCreated, UsedRevision: 1, Digest: v.Digest}
			if err := r.CommitRun(worker, forged); err == nil {
				t.Fatal("caller-supplied skill receipt accepted")
			}
			if err = r.CommitRun(worker, q); err != nil {
				t.Fatal(err)
			}
			saved, err := r.GetRun(owner, run.BotID, run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if saved.Report.Skill == nil || saved.Report.Skill.Kind != store.BotSkillCreated {
				t.Fatal("no receipt")
			}
			if err = saved.Report.ValidateEmployeeMessageIntegrity(saved.Status); err != nil {
				t.Fatal("final receipt invalidated model review", err)
			}
			for _, mutate := range []func(*store.BotReport){
				func(r *store.BotReport) { receipt := *r.Skill; receipt.UsedRevision++; r.Skill = &receipt },
				func(r *store.BotReport) { r.Summary += "tampered" },
				func(r *store.BotReport) {
					message := *r.EmployeeMessage
					message.Text += "tampered"
					r.EmployeeMessage = &message
				},
				func(r *store.BotReport) {
					message := *r.EmployeeMessage
					message.ReviewedSourceDigest = "forged"
					r.EmployeeMessage = &message
				},
			} {
				bad := saved.Report
				mutate(&bad)
				if bad.ValidateEmployeeMessageIntegrity(saved.Status) == nil {
					t.Fatal("tampered publication accepted")
				}
			}
			if saved.Report.ValidateEmployeeMessageIntegrity(store.RunSucceeded) == nil {
				t.Fatal("tampered status accepted")
			}
			for i := 0; i < 2; i++ {
				p, err := r.PublishReport(owner, store.BotPublicationRequest{BotID: run.BotID, RunID: run.ID})
				if err != nil || p.Text != m.Text {
					t.Fatalf("publication: %+v %v", p, err)
				}
			}
		})
	}
}
