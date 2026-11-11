package entstore

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	botdomain "github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func acceptedScheduleAcknowledgment(t *testing.T, r *botRepository, worker context.Context, q store.BotAuthorizedOperationRequest, op *store.BotOperation, mutate func(*store.BotWorkCandidate)) store.BotReport {
	t.Helper()
	payload, _ := json.Marshal(op.Payload)
	capture := botdomain.ScheduleCapture{OperationID: op.ID, BotID: q.Lease.BotID, SourceRunID: q.Lease.RunID, OwnerID: uuid.MustParse(q.MandateRef.OwnerID), Operation: op.Payload.Operation, ObservedAt: time.Now().UTC()}
	body, _ := json.Marshal(map[string]any{"operation_id": op.ID, "operation": op.Payload.Operation, "acknowledgment": capture.Acknowledgment(), "payload": json.RawMessage(payload)})
	work, err := r.LoadWork(worker, q.Lease)
	if err != nil || work == nil {
		t.Fatalf("load work: %v", err)
	}
	save := func(next *store.BotWorkState) {
		t.Helper()
		work, err = r.SaveWork(worker, store.BotWorkUpdate{Lease: q.Lease, ExpectedRevision: work.Revision, State: *next})
		if err != nil {
			t.Fatal(err)
		}
	}
	next := work.Clone()
	next.Phase = store.BotWorkProducing
	sources := []string{"assignment", "charter"}
	for _, g := range work.Contract.Goals {
		sources = append(sources, "goal:"+g.ID.String())
	}
	next.Criteria = []store.BotWorkCriterion{{ID: "enqueue", SourceIDs: sources, Text: "Queue the requested schedule"}}
	save(next)
	next = work.Clone()
	next.Phase = store.BotWorkCandidateReady
	next.Candidate = &store.BotWorkCandidate{Revision: 1, EventID: uuid.NewString(), Text: capture.Acknowledgment(), Bodies: map[string]string{"pending-control:0": string(body), "producer-final-draft": "Done (private untrusted draft)"}, Observations: []store.BotEvidence{capture.Observation()}}
	response, _ := json.Marshal(map[string]any{"operation_id": op.ID, "status": store.BotOperationPending, "message": capture.Acknowledgment()})
	next.Candidate.Bodies["tool-response:enqueue"] = string(response)
	next.Candidate.Bodies["observed-current-controls"] = `{}`
	rec, _ := r.db.GetBot(worker, q.Lease.BotID)
	next.Candidate.Observations = append(next.Candidate.Observations, store.BotEvidence{Kind: store.BotEvidenceObservation, ActorID: rec.Bot.OwnerID, ObservedAt: time.Now().UTC(), Source: "bot-assessment-state://" + string(rec.Bot.Scope) + "/" + rec.Bot.ID.String() + "/" + q.Lease.RunID.String()})
	if mutate != nil {
		mutate(next.Candidate)
	}
	next.Candidate.Digest = next.Candidate.ContentDigest()
	save(next)
	next = work.Clone()
	next.Phase = store.BotWorkReviewing
	save(next)
	next = work.Clone()
	next.Phase = store.BotWorkAccepted
	next.Review = &store.BotWorkReview{CandidateDigest: work.Candidate.Digest, Criteria: []store.BotCriterionAssessment{{CriterionID: "enqueue", Verdict: store.BotCriterionSatisfied, Method: store.BotAssessmentModel, EvidenceRefs: []string{"candidate:" + work.Candidate.Digest}, Explanation: "Controlled pending enqueue assessment"}}}
	save(next)
	return store.BotReport{Summary: work.Candidate.Text, Unchanged: work.Candidate.Observations[:1], Quality: &store.BotQualityReport{SchemaVersion: work.SchemaVersion, WorkRevision: work.Revision, RequirementsDigest: work.RequirementsDigest, CandidateDigest: work.Candidate.Digest, Outcome: work.Phase, Criteria: work.Review.Criteria, Repairs: work.Repairs}}
}

func TestBotScheduleAcknowledgmentPublicationOrder(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, applyFirst := range []bool{false, true} {
			name := string(scope) + "/publish-first"
			if applyFirst {
				name = string(scope) + "/apply-first"
			}
			t.Run(name, func(t *testing.T) {
				r, runtime, owner, worker, q := authorizedScheduleFixture(t, scope)
				op, err := runtime.EnqueueAuthorizedOperation(worker, q)
				if err != nil {
					t.Fatal(err)
				}
				report := acceptedScheduleAcknowledgment(t, r, worker, q, op, nil)
				work, err := r.LoadWork(worker, q.Lease)
				if err != nil {
					t.Fatal(err)
				}
				report.FinalizeEmployeeMessage(store.RunSucceeded, work)
				original := *report.EmployeeMessage
				commit := store.BotRunCommit{Lease: q.Lease, Status: store.RunSucceeded, Report: report}
				if err := r.CommitRun(worker, commit); err != nil {
					t.Fatal(err)
				}
				if err := r.CommitRun(worker, commit); err != nil {
					t.Fatalf("commit retry: %v", err)
				}
				saved, err := r.GetRun(owner, q.Lease.BotID, q.Lease.RunID)
				if err != nil || saved.Report.Acknowledgment == nil || saved.Report.Acknowledgment.Kind != store.BotReportAcknowledgmentSchedulePending {
					t.Fatalf("missing server marker: %+v %v", saved, err)
				}
				if *report.EmployeeMessage != original || saved.Report.EmployeeMessage == nil || saved.Report.ValidateEmployeeMessage(store.RunSucceeded, work) != nil || saved.Report.EmployeeMessage.SourceDigest == original.SourceDigest {
					t.Fatal("acknowledgment did not copy/rebind validated envelope")
				}
				if ids := saved.Report.Acknowledgment.OperationIDs; len(ids) != 1 || ids[0] != op.ID {
					t.Fatalf("acknowledgment lost scoped operation reference: %v", ids)
				}
				rec, _ := r.db.GetBot(owner, q.Lease.BotID)
				sequence := rec.LastSequence
				var applied *store.BotOperation
				apply := func() {
					t.Helper()
					applied, err = runtime.ApplyAuthorizedOperation(context.Background(), q.MandateRef, op.ID)
					if err != nil {
						t.Fatal(err)
					}
				}
				if applyFirst {
					apply()
				}
				pub, err := r.PublishReport(owner, store.BotPublicationRequest{BotID: q.Lease.BotID, RunID: q.Lease.RunID, ReportSequence: sequence})
				if err != nil || !pub.Suppressed || pub.Text != "" || len(pub.Notifications) != 0 {
					t.Fatalf("ack publication: %+v %v", pub, err)
				}
				if !applyFirst {
					apply()
				}
				again, err := r.PublishReport(owner, store.BotPublicationRequest{BotID: q.Lease.BotID, RunID: q.Lease.RunID, ReportSequence: sequence})
				if err != nil || !again.Suppressed || again.EventID != pub.EventID {
					t.Fatalf("retry: %+v %v", again, err)
				}
				final, err := r.PublishReport(owner, store.BotPublicationRequest{BotID: q.Lease.BotID, RunID: q.Lease.RunID})
				if err != nil {
					t.Fatal(err)
				}
				outcome, _, err := committedPublicationReport(owner, r.db, q.Lease.BotID, q.Lease.RunID, final.ReportSequence)
				if err != nil || outcome.Report.EmployeeMessage == nil || outcome.Report.EmployeeMessage.Origin != store.BotEmployeeMessageOperation || outcome.Report.ValidateEmployeeMessageIntegrity(outcome.Status) != nil || final.Text != outcome.Report.EmployeeMessage.Text {
					t.Fatal("missing receipt-bound employee outcome", err)
				}
				retried, err := runtime.ApplyAuthorizedOperation(context.Background(), q.MandateRef, op.ID)
				if err != nil || retried.Revision != applied.Revision {
					t.Fatal("operation retry changed receipt", err)
				}
				finalAgain, err := r.PublishReport(owner, store.BotPublicationRequest{BotID: q.Lease.BotID, RunID: q.Lease.RunID, ReportSequence: final.ReportSequence})
				if err != nil || finalAgain.EventID != final.EventID || finalAgain.Text != final.Text {
					t.Fatal("operation publication retry changed outcome", err)
				}
				view, err := store.ProjectBotOperation(rec.Bot, *applied, sequence+1)
				if err != nil || view.Presentation != store.BotOperationPresentationConversation {
					t.Fatalf("final view: %+v %v", view, err)
				}
				canonical, _ := store.NewBotOperationEvent(view, time.Now())
				if final.EventID != canonical.ID {
					t.Fatalf("final publication did not reuse operation event: %+v", final)
				}
				var reader store.BotTranscriptReader
				switch d := r.db.(type) {
				case personalBotDB:
					reader = &personalSessionStore{client: d.client}
				case teamBotDB:
					reader = &teamSessionStore{client: d.client}
				}
				rows, err := reader.ReadBotTranscript(owner, rec.Bot.HomeSessionID, 0, 200)
				if err != nil {
					t.Fatal(err)
				}
				finals := 0
				for _, row := range rows {
					if row.Event.ID == pub.EventID {
						t.Fatal("ack leaked into Home")
					}
					if row.Event.ID == canonical.ID {
						finals++
					}
					if row.Event.Content != nil {
						for _, part := range row.Event.Content.Parts {
							if part != nil && strings.Contains(part.Text, "durably queued") {
								t.Fatal("queued prose leaked")
							}
						}
					}
				}
				if finals != 1 {
					t.Fatalf("final event count %d", finals)
				}
			})
		}
	}
}

func TestBotScheduleAcknowledgmentRejectsMixedForgedAndFailures(t *testing.T) {
	for _, name := range []string{"mixed", "payload", "foreign-operation", "observation", "tool-error", "extra-body", "assessment-scope", "failed", "marker"} {
		t.Run(name, func(t *testing.T) {
			r, runtime, owner, worker, q := authorizedScheduleFixture(t, store.BotScopePersonal)
			op, err := runtime.EnqueueAuthorizedOperation(worker, q)
			if err != nil {
				t.Fatal(err)
			}
			report := acceptedScheduleAcknowledgment(t, r, worker, q, op, func(c *store.BotWorkCandidate) {
				switch name {
				case "mixed":
					c.Text += "\nToday's requested report is here."
				case "payload":
					c.Bodies["pending-control:0"] = strings.Replace(c.Bodies["pending-control:0"], "read the report", "different instructions", 1)
				case "foreign-operation":
					c.Bodies["pending-control:0"] = strings.ReplaceAll(c.Bodies["pending-control:0"], op.ID.String(), uuid.NewString())
				case "tool-error":
					c.Bodies["tool-response:enqueue"] = `{"status":"error","message":"failed"}`
				case "extra-body":
					c.Bodies["mixed-deliverable"] = "report"
				case "assessment-scope":
					c.Observations[1].Source = "bot-assessment-state://foreign"
				case "observation":
					c.Observations[0].Source = "forged"
				}
			})
			status := store.RunSucceeded
			if name == "failed" {
				status = store.RunFailed
				report.Failure = &store.BotReportFailure{Kind: store.BotReportFailureExecutionFormat}
			}
			if name == "marker" {
				report.Acknowledgment = &store.BotReportAcknowledgment{Kind: store.BotReportAcknowledgmentSchedulePending, OperationIDs: []uuid.UUID{op.ID}}
			}
			err = r.CommitRun(worker, store.BotRunCommit{Lease: q.Lease, Status: status, Report: report})
			if name == "marker" {
				if !errors.Is(err, store.ErrBotInvalid) {
					t.Fatalf("forged marker: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			pub, err := r.PublishReport(owner, store.BotPublicationRequest{BotID: q.Lease.BotID, RunID: q.Lease.RunID})
			if err != nil || pub.Suppressed || pub.Text == "" {
				t.Fatalf("non-ack hidden: %+v %v", pub, err)
			}
			if strings.Contains(pub.Text, op.ID.String()) || strings.Contains(pub.Text, "durably queued") || strings.Contains(pub.Text, "Today's requested report is here") {
				t.Fatalf("raw mixed acknowledgment leaked: %s", pub.Text)
			}
			if name == "failed" && !strings.Contains(pub.Text, "model response could not be processed") {
				t.Fatalf("independent failure was hidden: %s", pub.Text)
			}
			if name == "mixed" || name == "tool-error" || name == "extra-body" {
				if !strings.Contains(pub.Text, "independent result") || !strings.Contains(pub.Text, "reported separately") {
					t.Fatalf("missing mixed-work limitation: %s", pub.Text)
				}
			}
		})
	}
}
