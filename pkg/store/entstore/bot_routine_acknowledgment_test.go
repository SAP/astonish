package entstore

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	botdomain "github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func routineConfirmationFixture(t *testing.T, scope store.BotScope, kind store.BotControlOperation) (*botRepository, *botAuthorizedOperationRuntime, context.Context, context.Context, store.BotAuthorizedOperationRequest) {
	t.Helper()
	r, rt, owner, worker, q := authorizedScheduleFixture(t, scope)
	if err := r.CommitRun(worker, store.BotRunCommit{Lease: q.Lease, Status: store.RunSucceeded, Report: acceptedWorkReport(t, r, worker, q.Lease, "initial")}); err != nil {
		t.Fatal(err)
	}
	policy := func(ctx context.Context, ref store.BotMandateRef) (store.BotMandatePolicy, error) {
		p, e := authorizedSchedulePolicy(ctx, ref)
		p.Constraints[0].Resources = append(p.Constraints[0].Resources, RoutineControlResource(kind))
		return p, e
	}
	owner = store.WithBotMandateAdmission(owner, policy)
	rt.policy = policy
	admission, err := r.AdmitMessage(owner, store.BotMessageRequest{BotID: q.Lease.BotID, Version: 1, Instructions: "Every day at nine, read the report. Keep other schedules unchanged.", IdempotencyKey: uuid.NewString()})
	if err != nil {
		t.Fatal(err)
	}
	q.MandateRef = *admission.Run.MandateRef
	b, _ := r.Get(owner, q.Lease.BotID)
	worker = execution.WithBotRunIdentity(owner, execution.BotRunIdentity{BotID: b.ID, RunID: admission.Run.ID, Version: b.Version, ActorID: b.OwnerID.String(), OrgSlug: q.MandateRef.OrgSlug, TeamSlug: q.MandateRef.TeamSlug})
	q.Lease, err = r.ClaimRun(worker, store.BotClaimRequest{BotID: b.ID, RunID: admission.Run.ID, WorkerID: "routine-confirmation", ExpiresAt: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	text := "concise reports"
	q.Payload = store.BotControlPayload{Operation: kind, Reason: "owner preference"}
	switch kind {
	case store.BotControlResponsibility:
		q.Payload.Responsibility = &text
	case store.BotControlPersona:
		v, _ := r.db.ReadVersion(owner, b.ID, 1)
		q.Payload.Persona = &v.Persona
	case store.BotControlGoalCreate:
		q.Payload.Goal = &store.BotControlGoal{Goal: store.BotGoal{BotID: b.ID, Title: "report", CompletionCriteria: "report exists"}}
	case store.BotControlGoalEdit:
		goal := store.BotGoal{ID: uuid.New(), BotID: b.ID, Revision: 1, Status: store.BotGoalActive, Title: "old report", CompletionCriteria: "old criteria", WorkspacePath: "goals/fixture"}
		if err := r.db.InsertGoal(owner, goal); err != nil {
			t.Fatal(err)
		}
		goal.Title = "report"
		goal.CompletionCriteria = "report exists"
		q.Payload.Goal = &store.BotControlGoal{Goal: goal, ExpectedRevision: 1}
	case store.BotControlMemoryRemember, store.BotControlMemoryCorrect, store.BotControlMemoryForget:
		q.Payload.Memory = &store.BotControlMemory{Content: text, ExpectedRevision: b.Revision}
		if kind != store.BotControlMemoryRemember {
			id := uuid.New()
			if err := insertControlMemory(owner, r.db, id, admission.Run.SessionID, b.OwnerID, "old memory", "fact", nil, time.Now()); err != nil {
				t.Fatal(err)
			}
			row, err := r.db.ReadCorrectionMemory(owner, id.String())
			if err != nil {
				t.Fatal(err)
			}
			q.Payload.Memory.MemoryID = id.String()
			q.Payload.Memory.ExpectedFingerprint = store.MemoryContentFingerprint(*row)
		}
		if kind == store.BotControlMemoryForget {
			q.Payload.Memory.Content = ""
		}
	}
	return r, rt, owner, worker, q
}

func acceptedRoutineAcknowledgment(t *testing.T, r *botRepository, worker context.Context, q store.BotAuthorizedOperationRequest, op *store.BotOperation, mutate func(*store.BotWorkCandidate)) store.BotReport {
	return acceptedScheduleAcknowledgment(t, r, worker, q, op, func(c *store.BotWorkCandidate) {
		capture := botdomain.RoutineCapture{OperationID: op.ID, BotID: q.Lease.BotID, SourceRunID: q.Lease.RunID, OwnerID: uuid.MustParse(q.MandateRef.OwnerID), Operation: op.Payload.Operation, Status: op.Status, Revision: op.Revision, ObservedAt: c.Observations[0].ObservedAt}
		body, _ := json.Marshal(map[string]any{"operation_id": op.ID, "operation": op.Payload.Operation, "acknowledgment": capture.Acknowledgment(), "payload": op.Payload})
		response, _ := json.Marshal(map[string]any{"operation_id": op.ID, "bot_id": q.Lease.BotID, "digest": op.Provenance.PayloadDigest, "status": op.Status, "decision": op.Decision, "message": capture.Acknowledgment()})
		c.Text = capture.Acknowledgment()
		c.Bodies["producer-final-draft"] = c.Text
		c.Bodies["pending-control:0"] = string(body)
		c.Bodies["tool-response:enqueue"] = string(response)
		c.Observations[0] = capture.Observation()
		if mutate != nil {
			mutate(c)
		}
	})
}

func TestBotRoutineAcknowledgmentPublicationOrder(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, kind := range []store.BotControlOperation{store.BotControlResponsibility, store.BotControlPersona, store.BotControlGoalCreate, store.BotControlGoalEdit, store.BotControlMemoryRemember, store.BotControlMemoryCorrect, store.BotControlMemoryForget} {
			for _, applyFirst := range []bool{false, true} {
				name := string(scope) + "/" + string(kind) + map[bool]string{true: "/apply-first", false: "/publish-first"}[applyFirst]
				t.Run(name, func(t *testing.T) {
					r, rt, owner, worker, q := routineConfirmationFixture(t, scope, kind)
					op, err := rt.EnqueueAuthorizedOperation(worker, q)
					if err != nil {
						t.Fatal(err)
					}
					pending, err := store.ProjectBotOperation(store.Bot{ID: q.Lease.BotID, HomeSessionID: uuid.New()}, *op, 1)
					if err != nil || pending.Presentation != store.BotOperationPresentationInternal {
						t.Errorf("pending must be internal: %+v %v", pending, err)
					}
					report := acceptedRoutineAcknowledgment(t, r, worker, q, op, nil)
					commit := store.BotRunCommit{Lease: q.Lease, Status: store.RunSucceeded, Report: report}
					if err := r.CommitRun(worker, commit); err != nil {
						t.Fatal(err)
					}
					if err := r.CommitRun(worker, commit); err != nil {
						t.Fatal(err)
					}
					saved, _ := r.GetRun(owner, q.Lease.BotID, q.Lease.RunID)
					if saved.Report.Acknowledgment == nil || string(saved.Report.Acknowledgment.Kind) != "control_pending" {
						t.Fatalf("missing routine marker: %+v", saved.Report)
					}
					rec, _ := r.db.GetBot(owner, q.Lease.BotID)
					sequence := rec.LastSequence
					var applied *store.BotOperation
					apply := func() {
						t.Helper()
						applied, err = rt.ApplyAuthorizedOperation(owner, q.MandateRef, op.ID)
						if err != nil {
							t.Fatal(err)
						}
					}
					if applyFirst {
						apply()
					}
					request := store.BotPublicationRequest{BotID: q.Lease.BotID, RunID: q.Lease.RunID, ReportSequence: sequence}
					pub, err := r.PublishReport(owner, request)
					if err != nil || !pub.Suppressed || pub.Text != "" || len(pub.Notifications) != 0 {
						t.Fatalf("pending publication: %+v %v", pub, err)
					}
					if !applyFirst {
						apply()
					}
					again, err := r.PublishReport(owner, request)
					if err != nil || !again.Suppressed || again.EventID != pub.EventID {
						t.Fatalf("publication retry: %+v %v", again, err)
					}
					replay, err := rt.ApplyAuthorizedOperation(owner, q.MandateRef, op.ID)
					if err != nil || replay.Revision != applied.Revision {
						t.Fatalf("apply retry: %+v %v", replay, err)
					}
					final, err := r.PublishReport(owner, store.BotPublicationRequest{BotID: q.Lease.BotID, RunID: q.Lease.RunID})
					if err != nil {
						t.Fatal(err)
					}
					view, err := store.ProjectBotOperation(rec.Bot, *applied, sequence+1)
					if err != nil || view.Presentation != store.BotOperationPresentationConversation || strings.Contains(view.Summary, op.ID.String()) {
						t.Fatalf("final view: %+v %v", view, err)
					}
					canonical, _ := store.NewBotOperationEvent(view, time.Now())
					if final.EventID != canonical.ID {
						t.Fatalf("publication did not reuse canonical operation: %+v", final)
					}
					finalRetry, err := r.PublishReport(owner, store.BotPublicationRequest{BotID: q.Lease.BotID, RunID: q.Lease.RunID, ReportSequence: final.ReportSequence})
					if err != nil || finalRetry.EventID != canonical.ID || finalRetry.Text != final.Text {
						t.Fatalf("final publication retry: %+v %v", finalRetry, err)
					}
					for _, notification := range final.Notifications {
						if notification.Decision != store.NotificationSkip {
							t.Fatalf("metadata receipt authorized delivery: %+v", notification)
						}
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
					count := 0
					for _, row := range rows {
						if row.Event.ID == pub.EventID {
							t.Fatal("pending acknowledgment leaked")
						}
						if row.Event.ID == canonical.ID {
							count++
						}
					}
					if count != 1 {
						t.Fatalf("canonical count %d", count)
					}
				})
			}
		}
	}
}
