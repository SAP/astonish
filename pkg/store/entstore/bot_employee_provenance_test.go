package entstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"iter"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

func employeePresentationEnvelopeFixture() botStoredRun {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	run := store.BotRun{ID: uuid.New(), BotID: uuid.New(), Version: 1, SessionID: uuid.New(), UserEventID: "user", StateSequence: 7, Status: store.RunFailed, Report: store.BotReport{Summary: "private worker failure"}}
	p := &store.BotEmployeePresentation{SchemaVersion: store.BotEmployeePresentationVersion, State: store.BotEmployeePresentationClaimed, BotID: run.BotID, RunID: run.ID, BotVersion: run.Version, OwnerID: uuid.New(), OrgSlug: "org", TeamSlug: "team", HomeSessionID: uuid.New(), SourceSessionID: run.SessionID, UserEventID: run.UserEventID, EffectiveLimits: store.BudgetSpec{MaxRuntimeSeconds: 60}, Deadline: now.Add(time.Minute), AuthorityExpiresAt: now.Add(time.Minute), Provider: "test", Model: "test", ReportSequence: run.StateSequence, ReportDigest: run.Report.EmployeeMessageSourceDigest(run.Status), ClaimToken: "claim", DispatchToken: "dispatch", Usage: store.BotUsage{InputTokens: 12}, Turns: []store.BotEmployeeModelTurn{{ID: "turn", State: store.BotEmployeeModelTurnStarted, DispatchToken: "dispatch", StartedAt: now}}, Workers: []store.BotEmployeeWorkerHistory{{WorkerID: "opaque", SessionID: uuid.New(), Messages: []store.BotEmployeeWorkerMessage{{Role: "user", Text: "private question"}}}}}
	return botStoredRun{Run: run, EmployeePresentation: p}
}
func TestEmployeePresentationPrivateEnvelopeRoundTrip(t *testing.T) {
	original := employeePresentationEnvelopeFixture()
	original.EmployeePresentation.Operations = []store.BotEmployeeOperationIdentity{{OperationID: uuid.New(), Revision: 2, ReceiptDigest: "receipt"}}
	original.EmployeePresentation.Workers[0].Turns = []store.BotEmployeeModelTurn{{ID: "worker-turn", State: store.BotEmployeeModelTurnSettled, DispatchToken: "worker-dispatch", StartedAt: original.EmployeePresentation.Deadline.Add(-time.Minute), SettledAt: original.EmployeePresentation.Deadline.Add(-time.Second), Usage: store.BotUsage{InputTokens: 9}}}
	raw, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	var restored botStoredRun
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(restored.EmployeePresentation, original.EmployeePresentation) {
		t.Fatal("private ownership, usage, workers or unsettled turn lost")
	}
	for _, mutate := range []func(*store.BotEmployeePresentation){
		func(p *store.BotEmployeePresentation) { p.SchemaVersion++ },
		func(p *store.BotEmployeePresentation) { p.RunID = uuid.New() },
		func(p *store.BotEmployeePresentation) { p.ReportDigest = "forged" },
		func(p *store.BotEmployeePresentation) { p.State = "unknown" },
	} {
		p := *original.EmployeePresentation
		mutate(&p)
		bad := original
		bad.EmployeePresentation = &p
		data, _ := json.Marshal(bad)
		if json.Unmarshal(data, &restored) == nil {
			t.Fatal("invalid private binding restored")
		}
	}
	legacy, _ := json.Marshal(botStoredRun{Run: original.Run})
	if err := json.Unmarshal(legacy, &restored); err != nil || restored.EmployeePresentation != nil {
		t.Fatal("legacy run changed", err)
	}
}
func TestEmployeePresentationPrivatePublicJSONCannotMintClaim(t *testing.T) {
	original := employeePresentationEnvelopeFixture()
	private, _ := original.EmployeePresentation.MarshalPrivateJSON()
	var public store.BotEmployeePresentation
	if err := json.Unmarshal(private, &public); err != nil {
		t.Fatal(err)
	}
	if public.ValidateBinding(original.Run) == nil {
		t.Fatal("public JSON minted claim")
	}
	raw, _ := json.Marshal(original.EmployeePresentation)
	if string(raw) != "{}" {
		t.Fatalf("private presentation leaked: %s", raw)
	}
	var request bot.EmployeeChatRequest
	_ = json.Unmarshal([]byte(`{"worker_id":"opaque","message":"follow up","owner_id":"forged","run_id":"forged","session_id":"forged","authority":"forged"}`), &request)
	if request.WorkerID != "opaque" || request.Message != "follow up" {
		t.Fatal("conversation input lost")
	}
	wire, _ := json.Marshal(request)
	if strings.Contains(string(wire), "forged") {
		t.Fatal("worker request accepted authority")
	}
}

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
			if len(saved.EmployeeOutcomes) != 1 {
				t.Fatal("missing immutable per-outcome speech snapshot")
			}
			for sequence, outcome := range saved.EmployeeOutcomes {
				if sequence != outcome.Run.StateSequence {
					t.Fatal("wrong report intent sequence")
				}
				_, speech := store.EmployeeSpeech(outcome.Run, outcome.Speech, nil)
				if speech != (outcome.Run.Report.EmployeeMessage.Origin != store.BotEmployeeMessageFallback) {
					t.Fatal("finalized authorship mismatch")
				}
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
			if err != nil || first.EventID != again.EventID || first.Text != again.Text || first.EmployeeSpeech != again.EmployeeSpeech {
				t.Fatalf("publication retry: %+v %+v %v", first, again, err)
			}
		})
	}
}

// A resumed run can change every mutable communication identity. Exact receipt
// replay must use the saved per-sequence outcome, never those newer fields.
type employeeLaterOutcomeRead struct {
	botDB
	publicSpeechSequence int64
}

func (d employeeLaterOutcomeRead) ReadRun(ctx context.Context, botID, runID uuid.UUID) (*botStoredRun, error) {
	saved, err := d.botDB.ReadRun(ctx, botID, runID)
	if err != nil {
		return nil, err
	}
	if d.publicSpeechSequence != 0 {
		outcome := saved.EmployeeOutcomes[d.publicSpeechSequence]
		// Simulate a public JSON roundtrip, which must strip private provenance.
		//nolint:staticcheck // SA9005: Public JSON must strip private provenance to exercise receipt spoof rejection.
		raw, err := json.Marshal(outcome.Speech)
		if err != nil {
			return nil, err
		}
		var public store.BotEmployeeSpeechProvenance
		//nolint:staticcheck // SA9005: Public JSON must not restore private receipt authority.
		if err := json.Unmarshal(raw, &public); err != nil {
			return nil, err
		}
		outcome.Speech = &public
		saved.EmployeeOutcomes[d.publicSpeechSequence] = outcome
	}
	saved.Run.Status = store.RunRunning
	saved.Run.SessionID = uuid.New()
	saved.Run.UserEventID = "later-user-event"
	saved.Run.StateSequence++
	saved.Run.Report = store.BotReport{Summary: "later mutable private diagnostics"}
	return saved, nil
}

func TestEmployeeSpeechExactOutcomeSurvivesResume(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			r, owner, worker, run, lease, now := botRunFixture(t, scope)
			pinLegacyWorkFixture(t, r, worker, lease)
			action := execution.ActionDescriptor{Tool: "send", Effect: execution.EffectSend, Arguments: json.RawMessage(`{"body":"exact"}`), Recipient: "alice"}
			digest, _ := action.Digest()
			a, err := r.RequestApproval(worker, lease, store.BotApproval{BotID: run.BotID, RunID: run.ID, Version: run.Version, Action: action, ActionDigest: digest, ExpiresAt: now.Add(time.Hour)})
			if err != nil {
				t.Fatal(err)
			}
			checkpoint, _ := json.Marshal(bot.ApprovalCheckpoint{Format: bot.ApprovalCheckpointFormat, ApprovalID: a.ID, BotID: run.BotID, RunID: run.ID, SessionID: run.SessionID, Version: run.Version, ActionDigest: digest, Fence: lease.Fence})
			// Waiting approvals do not invoke the historical failure writer. Preserve
			// this first outcome as private, then resume into a genuinely reviewed
			// Employee failure reply below.
			report := store.BotReport{Summary: "Private approval diagnostics", NeedsApproval: []store.RunID{a.ID}}
			if err := r.CommitRun(worker, store.BotRunCommit{Lease: lease, Status: store.RunWaitingApproval, Report: report, Checkpoint: checkpoint}); err != nil {
				t.Fatal(err)
			}
			p, err := r.PublishReport(owner, store.BotPublicationRequest{BotID: run.BotID, RunID: run.ID})
			if err != nil || p.EmployeeSpeech || p.Text != "" {
				t.Fatalf("waiting diagnostics became speech: %+v %v", p, err)
			}
			b, _ := r.Get(owner, run.BotID)
			if _, err := r.DecideApproval(owner, store.BotApprovalDecision{BotID: run.BotID, ApprovalID: a.ID, ActorID: b.OwnerID, ActionDigest: digest, Status: store.ApprovalApproved}); err != nil {
				t.Fatal(err)
			}
			r = &botRepository{db: r.db, auth: r.auth, now: r.now}
			if err := r.RequeueApprovedRun(owner, run.BotID, run.ID, a.ID); err != nil {
				t.Fatal(err)
			}
			fresh, err := r.ClaimRun(worker, store.BotClaimRequest{BotID: run.BotID, RunID: run.ID, WorkerID: "resumed", ExpiresAt: now.Add(time.Minute)})
			if err != nil {
				t.Fatal(err)
			}
			laterReport := store.BotReport{Summary: "later private failure"}
			m := bot.ComposeEmployeeMessage(worker, store.RunFailed, laterReport, nil, bot.EmployeeMessageOptions{LLM: employeeCommitModel{}, Boundary: func(context.Context) error { return nil }})
			if m.Origin != store.BotEmployeeMessageModel {
				t.Fatal("missing reviewed Employee message")
			}
			laterReport.EmployeeMessage = &m
			if err := r.CommitRun(worker, store.BotRunCommit{Lease: fresh, Status: store.RunFailed, Report: laterReport}); err != nil {
				t.Fatal(err)
			}
			r = &botRepository{db: r.db, auth: r.auth, now: r.now}
			final, err := r.PublishReport(owner, store.BotPublicationRequest{BotID: run.BotID, RunID: run.ID})
			if err != nil || !final.EmployeeSpeech || final.Text != m.Text {
				t.Fatalf("reviewed final speech: %+v %v", final, err)
			}
			// Exercise the exact receipt with a newer active source identity, as a
			// subsequent continuation would expose after this reviewed outcome.
			laterDB := employeeLaterOutcomeRead{botDB: r.db}
			laterRepo := &botRepository{db: laterDB, auth: r.auth, now: r.now}
			if exact, err := laterRepo.ReadBotPublication(owner, run.BotID, run.ID, final.ReportSequence); err != nil || !exact.EmployeeSpeech || exact.Text != final.Text {
				t.Fatalf("newer mutable source changed exact speech: %+v %v", exact, err)
			}
			if replay, err := readPublication(owner, laterDB, run.BotID, run.ID, final.ReportSequence); err != nil || replay.Text != final.Text || !replay.EmployeeSpeech {
				t.Fatalf("newer mutable source changed replay: %+v %v", replay, err)
			}
			spoofDB := employeeLaterOutcomeRead{botDB: r.db, publicSpeechSequence: final.ReportSequence}
			spoofRepo := &botRepository{db: spoofDB, auth: r.auth, now: r.now}
			if _, err := spoofRepo.ReadBotPublication(owner, run.BotID, run.ID, final.ReportSequence); !errors.Is(err, store.ErrBotInvalid) {
				t.Fatalf("public provenance authorized prior receipt: %v", err)
			}
			old, err := r.ReadBotPublication(owner, run.BotID, run.ID, p.ReportSequence)
			if err != nil || old.Text != p.Text || old.EmployeeSpeech {
				t.Fatalf("old private outcome changed after resume: %+v %v", old, err)
			}
			if replay, err := r.PublishReport(owner, store.BotPublicationRequest{BotID: run.BotID, RunID: run.ID, ReportSequence: p.ReportSequence}); err != nil || replay.EmployeeSpeech || replay.Text != "" {
				t.Fatalf("prior exact replay promoted: %+v %v", replay, err)
			}
			saved, err := r.db.ReadRun(owner, run.BotID, run.ID)
			if err != nil || len(saved.EmployeeOutcomes) != 2 {
				t.Fatal("snapshots lost", err)
			}
			raw, _ := json.Marshal(saved)
			var reopened botStoredRun
			if err := json.Unmarshal(raw, &reopened); err != nil {
				t.Fatal(err)
			}
			x := reopened.EmployeeOutcomes[final.ReportSequence]
			if text, speech := store.EmployeeSpeech(x.Run, x.Speech, nil); !speech || text != final.Text {
				t.Fatal("private codec lost old authority")
			}
			//nolint:staticcheck // SA9005: Public JSON must strip private provenance after private codec restore.
			public, _ := json.Marshal(x.Speech)
			var spoof store.BotEmployeeSpeechProvenance
			//nolint:staticcheck // SA9005: Public JSON must not mint speech authority.
			if err := json.Unmarshal(public, &spoof); err != nil {
				t.Fatal(err)
			}
			if _, speech := store.EmployeeSpeech(x.Run, &spoof, nil); speech {
				t.Fatal("public JSON minted speech")
			}
			x.Run.SessionID = uuid.New()
			if _, speech := store.EmployeeSpeech(x.Run, x.Speech, nil); speech {
				t.Fatal("copied old authority accepted different source")
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
