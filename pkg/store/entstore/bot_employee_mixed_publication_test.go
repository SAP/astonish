package entstore

import (
	"context"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/store"
)

func TestMixedControlSafePublicationOrder(t *testing.T) {
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
				report := acceptedScheduleAcknowledgment(t, r, worker, q, op, func(c *store.BotWorkCandidate) { c.Bodies["mixed-deliverable"] = "SECRET independent draft" })
				if err := r.CommitRun(worker, store.BotRunCommit{Lease: q.Lease, Status: store.RunSucceeded, Report: report}); err != nil {
					t.Fatal(err)
				}
				rec, _ := r.db.GetBot(owner, q.Lease.BotID)
				seq := rec.LastSequence
				apply := func() {
					t.Helper()
					if _, err := runtime.ApplyAuthorizedOperation(context.Background(), q.MandateRef, op.ID); err != nil {
						t.Fatal(err)
					}
				}
				if applyFirst {
					apply()
				}
				pub, err := r.PublishReport(owner, store.BotPublicationRequest{BotID: q.Lease.BotID, RunID: q.Lease.RunID, ReportSequence: seq})
				if err != nil || pub.Suppressed || !strings.Contains(pub.Text, "independent result") || strings.Contains(pub.Text, op.ID.String()) || strings.Contains(pub.Text, "pending") {
					t.Fatalf("unsafe or hidden mixed result: %+v %v", pub, err)
				}
				if !applyFirst {
					apply()
				}
				again, err := r.PublishReport(owner, store.BotPublicationRequest{BotID: q.Lease.BotID, RunID: q.Lease.RunID, ReportSequence: seq})
				if err != nil || again.Text != pub.Text || again.EventID != pub.EventID || again.Suppressed {
					t.Fatal("mixed result changed after application", err)
				}
				final, err := r.PublishReport(owner, store.BotPublicationRequest{BotID: q.Lease.BotID, RunID: q.Lease.RunID})
				if err != nil || !strings.HasPrefix(final.Text, "I've created the schedule.") {
					t.Fatalf("missing independent control outcome: %+v %v", final, err)
				}
			})
		}
	}
}
