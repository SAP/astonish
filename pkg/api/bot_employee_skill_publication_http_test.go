package api

import (
	"context"
	"encoding/json"
	"iter"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

type skillFailureNarrator struct{ calls int }

func (*skillFailureNarrator) Name() string { return "reviewed-failure" }
func (m *skillFailureNarrator) GenerateContent(_ context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		m.calls++
		var in struct {
			SourceDigest string `json:"source_digest"`
			TextDigest   string `json:"text_digest"`
			Facts        struct {
				SourceDigest string `json:"source_digest"`
			} `json:"facts"`
		}
		_ = json.Unmarshal([]byte(req.Contents[0].Parts[0].Text), &in)
		out := map[string]any{"source_digest": in.SourceDigest, "outcome": "incomplete", "text": "I couldn't verify the requested result. Please review the incomplete work before deciding how to continue."}
		if strings.Contains(req.Config.SystemInstruction.Parts[0].Text, "BOT_EMPLOYEE_MESSAGE_REVIEW") {
			out = map[string]any{"source_digest": in.Facts.SourceDigest, "text_digest": in.TextDigest, "outcome": "incomplete", "approved": true}
		}
		raw, _ := json.Marshal(out)
		yield(&model.LLMResponse{Content: genai.NewContentFromText(string(raw), "model")}, nil)
	}
}

// Real loopback HTTP history and disk close/reopen, using a controlled private
// work checkpoint after exhausted citation assessment. Composition, commit and
// publication are production implementations; no historical user run is replayed.
func TestBotReviewedSkillFailurePublicationHTTPReopen(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, kind := range []store.BotSkillReceiptKind{store.BotSkillCreated, store.BotSkillSelected, store.BotSkillDeferred} {
			t.Run(string(scope)+"/"+string(kind), func(t *testing.T) {
				f := newSkillStorageHTTPFixture(t)
				b := f.bot(scope)
				worker, q, oriented := f.create(b)
				if kind == store.BotSkillSelected {
					f.finish(b, worker, q.Mutation.Lease)
					var w *store.BotWorkState
					worker, q.Mutation.Lease, w = f.claim(b, time.Minute)
					q.Kind = store.BotSkillUseExisting
					q.SkillID = &oriented.Invocation.SkillID
					q.Draft = nil
					q.Mutation.ExpectedRevision = w.Revision
					q.Mutation.IdempotencyKey = "select"
					var err error
					oriented, err = f.skills(scope).OrientSkill(worker, q)
					if err != nil {
						t.Fatal(err)
					}
				}
				work := oriented.Work
				// create and claim pin the historical protocol before claim.
				// Do not downgrade an already started agent-led run for this case.
				if work.Protocol != store.BotWorkProtocolLegacyAssessment || work.AgentWork != nil || work.Phase != store.BotWorkPreparing || work.Candidate != nil {
					t.Fatalf("expected pristine legacy publication fixture: %+v", work)
				}
				save := func(phase store.BotWorkPhase) {
					t.Helper()
					next := work.Clone()
					next.Phase = phase
					if phase == store.BotWorkProducing {
						sources := []string{"assignment", "charter"}
						for _, g := range next.Contract.Goals {
							sources = append(sources, "goal:"+g.ID.String())
						}
						next.Criteria = []store.BotWorkCriterion{{ID: "c1", SourceIDs: sources, Text: "Cite all sources"}}
					}
					if phase == store.BotWorkCandidateReady {
						next.Candidate = &store.BotWorkCandidate{Revision: 1, EventID: "candidate-event", Text: "REJECTED_PRIVATE_CANDIDATE"}
						next.Candidate.Digest = next.Candidate.ContentDigest()
					}
					if phase == store.BotWorkBlocked {
						next.Unresolved = []string{"Citation assessment exhausted"}
					}
					var err error
					work, err = f.repo(scope).(store.BotWorkStore).SaveWork(worker, store.BotWorkUpdate{Lease: q.Mutation.Lease, ExpectedRevision: work.Revision, State: *next})
					if err != nil {
						t.Fatal(err)
					}
				}
				save(store.BotWorkProducing)
				save(store.BotWorkCandidateReady)
				save(store.BotWorkReviewing)
				save(store.BotWorkBlocked)
				if kind == store.BotSkillDeferred {
					_, err := f.skills(scope).StageSkillLearning(worker, store.BotSkillLearningRequest{Mutation: store.BotSkillMutation{BotID: b.ID, Lease: q.Mutation.Lease, ExpectedRevision: work.Contract.Skill.Revision, IdempotencyKey: "defer"}, ExpectedWorkRevision: work.Revision, Failure: store.BotSkillDeferred})
					if err != nil {
						t.Fatal(err)
					}
					work, _ = f.repo(scope).(store.BotWorkStore).LoadWork(worker, q.Mutation.Lease)
				}
				report := store.BotReport{Summary: "Citation assessment exhausted"}
				narrator := &skillFailureNarrator{}
				m := bot.ComposeEmployeeMessage(worker, store.RunPartial, report, nil, bot.EmployeeMessageOptions{LLM: narrator, Work: work, Boundary: func(context.Context) error { return nil }})
				if narrator.calls != 2 || m.Origin != store.BotEmployeeMessageModel || !m.HasTrustedReviewAttestation() {
					t.Fatalf("missing writer/review: %+v calls=%d", m, narrator.calls)
				}
				report.EmployeeMessage = &m
				commit := store.BotRunCommit{Lease: q.Mutation.Lease, Status: store.RunPartial, Report: report}
				if err := f.repo(scope).CommitRun(worker, commit); err != nil {
					t.Fatal(err)
				}
				if err := f.repo(scope).CommitRun(worker, commit); err != nil {
					t.Fatal("commit retry", err)
				}
				ctx := f.context(f.owner)
				request := store.BotPublicationRequest{BotID: b.ID, RunID: q.Mutation.Lease.RunID}
				p, err := f.repo(scope).(store.BotPublicationStore).PublishReport(ctx, request)
				if err != nil {
					t.Fatal(err)
				}
				for i := 0; i < 2; i++ {
					if i == 1 {
						f.reopen()
					}
					again, err := f.repo(scope).(store.BotPublicationStore).PublishReport(ctx, request)
					if err != nil || again.EventID != p.EventID || again.Text != m.Text {
						t.Fatalf("publication retry: %+v %v", again, err)
					}
					run := skillDecode[store.BotRun](t, f.request("GET", skillPath(b, "/runs/"+request.RunID.String()), f.owner, "", 200))
					if run.Report.Skill == nil || run.Report.Skill.Kind != kind || run.Report.EmployeeMessage.Origin != store.BotEmployeeMessageModel || run.Report.ValidateEmployeeMessageIntegrity(run.Status) != nil {
						t.Fatalf("lost durable receipt/review: %+v", run.Report)
					}
					history := string(f.request("GET", skillPath(b, "/sessions/"+b.HomeSessionID.String()+"/stream"), f.owner, "", 200))
					if strings.Count(history, m.Text) != 1 || strings.Contains(history, "REJECTED_PRIVATE_CANDIDATE") {
						t.Fatalf("Home not exact once: %s", history)
					}
				}
			})
		}
	}
}
