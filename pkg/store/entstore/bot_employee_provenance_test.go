package entstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"iter"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

type employeeCommitModel struct{}

func (employeeCommitModel) Name() string { return "owner-test" }
func (employeeCommitModel) GenerateContent(_ context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		var input struct {
			SourceDigest string `json:"source_digest"`
			TextDigest   string `json:"text_digest"`
			Facts        struct {
				SourceDigest string `json:"source_digest"`
			} `json:"facts"`
		}
		_ = json.Unmarshal([]byte(req.Contents[0].Parts[0].Text), &input)
		out := map[string]any{"source_digest": input.SourceDigest, "outcome": "incomplete", "text": "I couldn't complete this work because an operational failure stopped it. Please review the run diagnostics before continuing."}
		if strings.Contains(req.Config.SystemInstruction.Parts[0].Text, "BOT_EMPLOYEE_MESSAGE_REVIEW") {
			out = map[string]any{"source_digest": input.Facts.SourceDigest, "text_digest": input.TextDigest, "outcome": "incomplete", "approved": true}
		}
		raw, _ := json.Marshal(out)
		yield(&model.LLMResponse{Content: genai.NewContentFromText(string(raw), "model")}, nil)
	}
}

func TestBotEmployeeCommitRetryAfterLearning(t *testing.T) {
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
			next.ParentRevision, next.ParentDigest = v.Revision, v.Digest
			next.Method = []string{"An untested alternative"}
			next.Digest = next.ContentDigest()
			source := v.Expectations[0].Provenance
			_, err = r.StageSkillLearning(worker, store.BotSkillLearningRequest{Mutation: store.BotSkillMutation{BotID: run.BotID, Lease: lease, ExpectedRevision: 1, IdempotencyKey: "learn", Source: source}, ExpectedWorkRevision: oriented.Work.Revision, Learning: store.BotSkillLearning{SkillID: v.SkillID, ParentRevision: v.Revision, ParentDigest: v.Digest, SourceRunID: run.ID, CandidateDigest: source.SourceDigest, EvidenceDigest: source.SourceDigest, Proposed: *next}})
			if err != nil {
				t.Fatal(err)
			}
			w, err = r.LoadWork(worker, lease)
			if err != nil {
				t.Fatal(err)
			}
			report := store.BotReport{Summary: "No work observed"}
			report.Quality = &store.BotQualityReport{SchemaVersion: w.SchemaVersion, WorkRevision: w.Revision, RequirementsDigest: w.RequirementsDigest, Outcome: w.Phase, Repairs: w.Repairs}
			m := bot.ComposeEmployeeMessage(worker, store.RunPartial, report, nil, bot.EmployeeMessageOptions{LLM: employeeCommitModel{}, Boundary: func(context.Context) error { return nil }})
			if m.Origin != store.BotEmployeeMessageModel {
				t.Fatal("missing trusted model message")
			}
			report.EmployeeMessage = &m
			q := store.BotRunCommit{Lease: lease, Status: store.RunPartial, Report: report}
			if err := r.CommitRun(worker, q); err != nil {
				t.Fatal("initial commit", err)
			}
			saved, err := r.db.ReadRun(owner, run.BotID, run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if saved.Work.Revision <= w.Revision || saved.Run.Report.Skill == nil || saved.Run.Report.Skill.Kind != store.BotSkillDeferred {
				t.Fatal("commit did not finalize learning and advance work revision")
			}
			if err := r.CommitRun(worker, q); err != nil {
				t.Fatal("exact pre-learning retry", err)
			}
			for _, change := range []func(*store.BotRunCommit){
				func(q *store.BotRunCommit) { q.Report.Summary += "tampered" },
				func(q *store.BotRunCommit) { q.Lease.Fence++ },
				func(q *store.BotRunCommit) { q.Lease.WorkerID += "other" },
			} {
				bad := q
				change(&bad)
				if err := r.CommitRun(worker, bad); !errors.Is(err, store.ErrBotConflict) {
					t.Fatalf("changed retry: %v", err)
				}
			}
			raw, _ := json.Marshal(q)
			var public store.BotRunCommit
			if err := json.Unmarshal(raw, &public); err != nil {
				t.Fatal(err)
			}
			if err := r.CommitRun(worker, public); !errors.Is(err, store.ErrBotInvalid) {
				t.Fatalf("public model retry: %v", err)
			}
			after, err := r.db.ReadRun(owner, run.BotID, run.ID)
			if err != nil || after.Work.Revision != saved.Work.Revision || after.CommitHash != saved.CommitHash {
				t.Fatal("retry changed committed work", err)
			}
			first, err := r.PublishReport(owner, store.BotPublicationRequest{BotID: run.BotID, RunID: run.ID})
			if err != nil {
				t.Fatal(err)
			}
			again, err := r.PublishReport(owner, store.BotPublicationRequest{BotID: run.BotID, RunID: run.ID})
			if err != nil || first.EventID != again.EventID || first.Text == "" || first.Text != again.Text {
				t.Fatalf("publication retry: %+v %+v %v", first, again, err)
			}
		})
	}
}

func TestEmployeeModelCommitRequiresPrivateReview(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			repo, owner, worker, run, lease, _ := botRunFixture(t, scope)
			pinLegacyWorkFixture(t, repo, worker, lease)
			report := store.BotReport{Summary: "private failure"}
			m := store.NewBotEmployeeFallback(store.RunFailed, report, "")
			m.Origin, m.Text = store.BotEmployeeMessageModel, "I sent the payment successfully."
			sum := sha256.Sum256([]byte(m.Text))
			m.TextDigest = hex.EncodeToString(sum[:])
			m.ReviewedTextDigest, m.ReviewedSourceDigest = m.TextDigest, m.SourceDigest
			report.EmployeeMessage = &m
			commit := store.BotRunCommit{Lease: lease, Status: store.RunFailed, Report: report}
			if err := repo.CommitRun(worker, commit); !errors.Is(err, store.ErrBotInvalid) {
				t.Fatalf("forged hashes committed: %v", err)
			}
			m = bot.ComposeEmployeeMessage(worker, store.RunFailed, report, nil, bot.EmployeeMessageOptions{LLM: employeeCommitModel{}, Boundary: func(context.Context) error { return nil }})
			if m.Origin != store.BotEmployeeMessageModel {
				t.Fatal("composer did not review")
			}
			report.EmployeeMessage = &m
			report.FinalizeEmployeeMessage(store.RunFailed, nil)
			commit.Report = report
			raw, _ := json.Marshal(commit)
			var roundtrip store.BotRunCommit
			if err := json.Unmarshal(raw, &roundtrip); err != nil {
				t.Fatal(err)
			}
			if err := repo.CommitRun(worker, roundtrip); !errors.Is(err, store.ErrBotInvalid) {
				t.Fatalf("public roundtrip committed: %v", err)
			}
			if err := repo.CommitRun(worker, commit); err != nil {
				t.Fatal("trusted composer commit", err)
			}
			if err := repo.CommitRun(worker, commit); err != nil {
				t.Fatal("trusted retry", err)
			}
			if err := repo.CommitRun(worker, roundtrip); !errors.Is(err, store.ErrBotInvalid) {
				t.Fatalf("public retry committed: %v", err)
			}
			saved, err := repo.GetRun(owner, run.BotID, run.ID)
			if err != nil || saved.Report.ValidateEmployeeMessageIntegrity(store.RunFailed) != nil {
				t.Fatal("persisted integrity", err)
			}
			pub, err := repo.PublishReport(owner, store.BotPublicationRequest{BotID: run.BotID, RunID: run.ID})
			if err != nil || pub.Text != m.Text {
				t.Fatalf("trusted publication: %+v %v", pub, err)
			}
		})
	}
}
