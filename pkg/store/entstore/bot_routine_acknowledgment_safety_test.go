package entstore

import (
	"encoding/json"
	"errors"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"strings"
	"testing"
)

func TestBotRoutineAcknowledgmentRejectsMixedForgedAndFailures(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, name := range []string{"mixed", "payload", "foreign-operation", "observation", "tool-error", "extra-body", "assessment-scope", "failed", "marker", "digest", "bot-id", "decision", "extra-field", "schedule-envelope"} {
			t.Run(string(scope)+"/"+name, func(t *testing.T) {
				r, rt, owner, worker, q := routineConfirmationFixture(t, scope, store.BotControlResponsibility)
				op, err := rt.EnqueueAuthorizedOperation(worker, q)
				if err != nil {
					t.Fatal(err)
				}
				report := acceptedRoutineAcknowledgment(t, r, worker, q, op, func(c *store.BotWorkCandidate) {
					switch name {
					case "mixed":
						c.Text += "\nHere is today's report."
					case "payload":
						c.Bodies["pending-control:0"] = strings.Replace(c.Bodies["pending-control:0"], "concise reports", "different responsibility", 1)
					case "foreign-operation":
						c.Bodies["pending-control:0"] = strings.ReplaceAll(c.Bodies["pending-control:0"], op.ID.String(), uuid.NewString())
					case "observation":
						c.Observations[0].Source = "forged"
					case "assessment-scope":
						c.Observations[1].Source = "bot-assessment-state://foreign"
					case "extra-body":
						c.Bodies["deliverable"] = "report"
					case "tool-error":
						c.Bodies["tool-response:enqueue"] = `{"status":"error","message":"failed"}`
					case "digest", "bot-id", "decision", "extra-field", "schedule-envelope":
						var response map[string]any
						_ = json.Unmarshal([]byte(c.Bodies["tool-response:enqueue"]), &response)
						switch name {
						case "digest":
							response["digest"] = "forged"
						case "bot-id":
							response["bot_id"] = uuid.NewString()
						case "decision":
							response["decision"] = store.BotOperationRequireAuthorization
						case "extra-field":
							response["error"] = "failed"
						case "schedule-envelope":
							delete(response, "digest")
							delete(response, "bot_id")
							delete(response, "decision")
						}
						raw, _ := json.Marshal(response)
						c.Bodies["tool-response:enqueue"] = string(raw)
					}
				})
				status := store.RunSucceeded
				if name == "failed" {
					status = store.RunFailed
					report.Failure = &store.BotReportFailure{Kind: store.BotReportFailureExecutionFormat}
				}
				if name == "marker" {
					report.Acknowledgment = &store.BotReportAcknowledgment{Kind: store.BotReportAcknowledgmentControlPending, OperationIDs: []uuid.UUID{op.ID}}
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
			})
		}
	}
}
