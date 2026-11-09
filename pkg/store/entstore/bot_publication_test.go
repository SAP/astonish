package entstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	personalversion "github.com/SAP/astonish/ent/personal/botversion"
	personalevent "github.com/SAP/astonish/ent/personal/sessionevent"
	teamversion "github.com/SAP/astonish/ent/team/botversion"
	teamevent "github.com/SAP/astonish/ent/team/sessionevent"
	botdomain "github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	adksession "google.golang.org/adk/session"
)

type publicationTransport struct {
	calls []botdomain.NotificationRequest
	send  func() (botdomain.NotificationReceipt, error)
}

func (n *publicationTransport) Send(_ context.Context, q botdomain.NotificationRequest) (botdomain.NotificationReceipt, error) {
	n.calls = append(n.calls, q)
	if n.send != nil {
		return n.send()
	}
	return botdomain.NotificationReceipt{ProviderID: "provider-receipt"}, nil
}

// Seed an approved immutable snapshot through the scoped generated test client.
// The public draft/configuration API intentionally cannot grant channel authority.
func publicationFixture(t *testing.T, scope store.BotScope, report store.BotReport, prefs string) (*botRepository, context.Context, *store.BotRun, *time.Time, store.BotChannelBinding) {
	t.Helper()
	repo, ctx, worker, run, lease, now := botRunFixture(t, scope)
	version, err := repo.GetVersion(ctx, run.BotID, run.Version)
	if err != nil {
		t.Fatal(err)
	}
	binding := store.BotChannelBinding{ID: uuid.New(), Channel: "slack", ChatID: "approved-chat", ThreadID: "approved-thread"}
	// Trusted fixture verifier, independent of the version/body being tested.
	repo.auth.checkDelivery = func(c context.Context, owner uuid.UUID, target store.BotChannelBinding) error {
		p, ok := execution.PrincipalFromContext(c)
		if !ok || owner.String() != p.Subject || target != binding {
			return store.ErrBotForbidden
		}
		return nil
	}
	version.Delivery = store.DeliveryPolicy{Notify: store.NotifyOnChange, Preferences: prefs, Bindings: []store.BotChannelBinding{binding}}
	seedPublicationVersion(t, repo, ctx, *version)
	if err := repo.CommitRun(worker, store.BotRunCommit{Lease: lease, Status: store.RunSucceeded, Report: report}); err != nil {
		t.Fatal(err)
	}
	return repo, ctx, run, now, binding
}
func seedPublicationVersion(t *testing.T, repo *botRepository, ctx context.Context, v store.BotVersion) {
	t.Helper()
	var err error
	switch db := repo.db.(type) {
	case personalBotDB:
		_, err = db.client.BotVersion.Delete().Where(personalversion.BotIDEQ(v.BotID), personalversion.VersionEQ(v.Version)).Exec(ctx)
	case teamBotDB:
		_, err = db.client.BotVersion.Delete().Where(teamversion.BotIDEQ(v.BotID), teamversion.VersionEQ(v.Version)).Exec(ctx)
	}
	if err == nil {
		hash, hashErr := botHash(v)
		if hashErr != nil {
			t.Fatal(hashErr)
		}
		err = repo.db.InsertVersion(ctx, v, hash)
	}
	if err != nil {
		t.Fatal(err)
	}
}
func publicationTranscriptCount(t *testing.T, repo *botRepository, ctx context.Context, id uuid.UUID) int {
	t.Helper()
	b, err := repo.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	var count int
	switch db := repo.db.(type) {
	case personalBotDB:
		count, err = db.client.SessionEvent.Query().Where(personalevent.SessionIDEQ(b.HomeSessionID.String())).Count(ctx)
	case teamBotDB:
		count, err = db.client.SessionEvent.Query().Where(teamevent.SessionIDEQ(b.HomeSessionID.String())).Count(ctx)
	}
	if err != nil {
		t.Fatal(err)
	}
	return count
}

func TestBotPublicationWaitingThenFinal(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			repo, ctx, worker, run, lease, now := botRunFixture(t, scope)
			testPublicationWaitingThenFinal(t, repo, ctx, worker, run, lease, now)
		})
	}
}

func testPublicationWaitingThenFinal(t *testing.T, repo *botRepository, ctx, worker context.Context, run *store.BotRun, lease store.BotLease, now *time.Time) {
	t.Helper()
	version, err := repo.GetVersion(ctx, run.BotID, run.Version)
	if err != nil {
		t.Fatal(err)
	}
	approvedTarget := store.BotChannelBinding{ID: uuid.New(), Channel: "slack", ChatID: "approved"}
	repo.auth.checkDelivery = func(c context.Context, owner uuid.UUID, target store.BotChannelBinding) error {
		p, ok := execution.PrincipalFromContext(c)
		if !ok || owner.String() != p.Subject || target != approvedTarget {
			return store.ErrBotForbidden
		}
		return nil
	}
	version.Delivery = store.DeliveryPolicy{Notify: store.NotifyAlways, Bindings: []store.BotChannelBinding{approvedTarget}}
	seedPublicationVersion(t, repo, ctx, *version)
	action := execution.ActionDescriptor{Tool: "send", Effect: execution.EffectSend, Arguments: json.RawMessage(`{"body":"exact"}`), Recipient: "alice"}
	digest, _ := action.Digest()
	approval, err := repo.RequestApproval(worker, lease, store.BotApproval{BotID: run.BotID, RunID: run.ID, Version: run.Version, Action: action, ActionDigest: digest, ExpiresAt: now.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, _ := json.Marshal(botdomain.ApprovalCheckpoint{Format: botdomain.ApprovalCheckpointFormat, ApprovalID: approval.ID, BotID: run.BotID, RunID: run.ID, SessionID: run.SessionID, Version: run.Version, ActionDigest: digest, Fence: lease.Fence})
	if err := repo.CommitRun(worker, store.BotRunCommit{Lease: lease, Status: store.RunWaitingApproval, Checkpoint: checkpoint, Report: store.BotReport{Summary: "Waiting for a decision", NeedsApproval: []uuid.UUID{approval.ID}}}); err != nil {
		t.Fatal(err)
	}
	transport := &publicationTransport{}
	publisher := &botdomain.Publisher{Store: repo, Notifier: transport, Now: func() time.Time { return *now }}
	first, err := publisher.Publish(ctx, run.BotID, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertTranscript := func(publications ...*store.BotPublication) {
		t.Helper()
		b, err := repo.Get(ctx, run.BotID)
		if err != nil {
			t.Fatal(err)
		}
		rows := reviewTranscript(t, ctx, repo, *b)
		// Approval creation adds a metadata reference, not another report.
		// Check every row so duplicate reports or reviews cannot be filtered out.
		if len(rows) != 1+len(publications) {
			t.Fatalf("want one review and %d reports, got %d rows: %+v", len(publications), len(rows), rows)
		}
		review := rows[0].Event
		reviewID := uuid.NewSHA1(approval.ID, []byte("bot-review:"+string(botReviewApproval)))
		if review == nil || review.ID != reviewID.String() || review.Author != "bot_runtime" || review.Content != nil {
			t.Fatalf("unexpected approval reference: %+v", review)
		}
		encoded, err := json.Marshal(review.Actions.StateDelta["bot_runtime_event"])
		if err != nil {
			t.Fatal(err)
		}
		var event botdomain.RuntimeEvent
		if err := json.Unmarshal(encoded, &event); err != nil {
			t.Fatal(err)
		}
		if event.Type != "bot_review" || event.SessionID != b.HomeSessionID || event.RunID != run.ID || event.TurnID != reviewID || event.Sequence != 1 {
			t.Fatalf("unexpected review envelope: %+v", event)
		}
		var ref struct {
			Kind botReviewKind `json:"kind"`
			ID   uuid.UUID     `json:"review_id"`
		}
		if err := json.Unmarshal(event.Payload, &ref); err != nil {
			t.Fatal(err)
		}
		if ref.Kind != botReviewApproval || ref.ID != approval.ID {
			t.Fatalf("unexpected review payload: %+v", ref)
		}
		for i, publication := range publications {
			report := rows[i+1].Event
			if report == nil || report.ID != publication.EventID || report.Author != "assistant" || report.InvocationID != "bot-report:"+run.ID.String() {
				t.Fatalf("report %d identity mismatch: %+v", i, report)
			}
			if report.Content == nil || report.Content.Role != "model" || len(report.Content.Parts) != 1 || report.Content.Parts[0] == nil || report.Content.Parts[0].Text != publication.Text {
				t.Fatalf("report %d content mismatch: %+v", i, report.Content)
			}
			if _, ok := report.Actions.StateDelta["bot_runtime_event"]; ok {
				t.Fatalf("report %d unexpectedly contains review metadata", i)
			}
		}
	}
	assertTranscript(first)
	b, err := repo.Get(ctx, run.BotID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.DecideApproval(ctx, store.BotApprovalDecision{BotID: b.ID, ApprovalID: approval.ID, ActorID: b.OwnerID, ActionDigest: digest, Status: store.ApprovalApproved}); err != nil {
		t.Fatal(err)
	}
	if err := repo.RequeueApprovedRun(ctx, b.ID, run.ID, approval.ID); err != nil {
		t.Fatal(err)
	}
	fresh, err := repo.ClaimRun(worker, store.BotClaimRequest{BotID: b.ID, RunID: run.ID, WorkerID: "resumed", ExpiresAt: now.Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.ConsumeApproval(worker, store.BotApprovalConsumption{Lease: fresh, ApprovalID: approval.ID, Version: run.Version, ActionDigest: digest}); err != nil {
		t.Fatal(err)
	}
	if err := repo.CommitRun(worker, store.BotRunCommit{Lease: fresh, Status: store.RunSucceeded, Report: store.BotReport{Summary: "Final verified outcome"}}); err != nil {
		t.Fatal(err)
	}
	final, err := publisher.Publish(ctx, run.BotID, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if final.EventID == first.EventID || final.Text == first.Text {
		t.Fatalf("final outcome suppressed: first=%+v final=%+v", first, final)
	}
	assertTranscript(first, final)
	if first.ReportSequence == 0 || final.ReportSequence <= first.ReportSequence {
		t.Fatal("missing committed sequence identity")
	}
	replayedFinal, err := publisher.Publish(ctx, run.BotID, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if replayedFinal.EventID != final.EventID || replayedFinal.ReportSequence != final.ReportSequence || replayedFinal.Text != final.Text {
		t.Fatalf("final replay changed report: %+v", replayedFinal)
	}
	assertTranscript(first, final)
	replayedFirst, err := publisher.PublishOutcome(ctx, run.BotID, run.ID, first.ReportSequence)
	if err != nil {
		t.Fatal(err)
	}
	if replayedFirst.EventID != first.EventID || replayedFirst.ReportSequence != first.ReportSequence || replayedFirst.Text != first.Text {
		t.Fatalf("waiting replay changed report: %+v", replayedFirst)
	}
	assertTranscript(first, final)
	if len(transport.calls) != 2 {
		t.Fatalf("want waiting and final sends once, got %d", len(transport.calls))
	}
}
func TestBotPublicationNoChange(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			repo, ctx, run, now, _ := publicationFixture(t, scope, store.BotReport{Summary: "No change", Unchanged: []store.BotEvidence{{Summary: "stable observation"}}}, "")
			notifier := &publicationTransport{}
			publisher := &botdomain.Publisher{Store: repo, Notifier: notifier, Now: func() time.Time { return *now }}
			first, err := publisher.Publish(ctx, run.BotID, run.ID)
			if err != nil {
				t.Fatal(err)
			}
			// Fresh repository simulates process restart; no in-memory deduplication.
			publisher.Store = &botRepository{auth: repo.auth, db: repo.db, now: repo.now}
			second, err := publisher.Publish(ctx, run.BotID, run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if first.EventID != second.EventID || !strings.Contains(first.Text, "stable observation") || len(notifier.calls) != 0 || second.Notifications[0].State != store.BotNotificationSkipped {
				t.Fatalf("unexpected publication %+v calls=%d", second, len(notifier.calls))
			}
			if count := publicationTranscriptCount(t, repo, ctx, run.BotID); count != 1 {
				t.Fatalf("home transcript count=%d", count)
			}
		})
	}
}
func TestBotNotificationsThresholdOnce(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			repo, ctx, run, now, target := publicationFixture(t, scope, store.BotReport{Summary: "Changed", Changed: []store.BotEvidence{{Summary: "A"}, {Summary: "B"}}}, `{"min_changed":2}`)
			notifier := &publicationTransport{}
			publisher := &botdomain.Publisher{Store: repo, Notifier: notifier, Now: func() time.Time { return *now }}
			for i := 0; i < 3; i++ {
				p, err := publisher.Publish(ctx, run.BotID, run.ID)
				if err != nil {
					t.Fatal(err)
				}
				if p.Notifications[0].State != store.BotNotificationDelivered {
					t.Fatalf("not delivered: %+v", p)
				}
			}
			if len(notifier.calls) != 1 || notifier.calls[0].Target != target || notifier.calls[0].ID == uuid.Nil {
				t.Fatalf("wrong transport calls %+v", notifier.calls)
			}
			if count := publicationTranscriptCount(t, repo, ctx, run.BotID); count != 1 {
				t.Fatal(count)
			}
		})
	}
}

func TestBotNotificationsCrashBoundaries(t *testing.T) {
	for _, boundary := range []string{"before-send", "after-send", "ambiguous"} {
		t.Run(boundary, func(t *testing.T) {
			repo, ctx, run, now, target := publicationFixture(t, store.BotScopePersonal, store.BotReport{Summary: "Changed", Changed: []store.BotEvidence{{Summary: "A"}}}, "")
			notifier := &publicationTransport{}
			publisher := &botdomain.Publisher{Store: repo, Notifier: notifier, Now: func() time.Time { return *now }}
			if boundary == "before-send" {
				if _, err := repo.PublishReport(ctx, store.BotPublicationRequest{BotID: run.BotID, RunID: run.ID}); err != nil {
					t.Fatal(err)
				}
				if _, send, err := repo.ClaimNotification(ctx, store.BotNotificationClaim{BotID: run.BotID, RunID: run.ID, BindingID: target.ID}); err != nil || !send {
					t.Fatalf("claim %v %v", send, err)
				}
			} else {
				if boundary == "after-send" {
					publisher.Store = publicationFinishFailure{repo}
				} else {
					notifier.send = func() (botdomain.NotificationReceipt, error) {
						return botdomain.NotificationReceipt{Uncertain: true}, errors.New("connection lost after write")
					}
				}
				if _, err := publisher.Publish(ctx, run.BotID, run.ID); err == nil {
					t.Fatal("missing injected error")
				}
			}
			publisher.Store = &botRepository{auth: repo.auth, db: repo.db, now: repo.now}
			*now = now.Add(24 * time.Hour)
			p, err := publisher.Publish(ctx, run.BotID, run.ID)
			if err != nil {
				t.Fatal(err)
			}
			wantCalls := 1
			if boundary == "before-send" {
				wantCalls = 0
			}
			if len(notifier.calls) != wantCalls || p.Notifications[0].State != store.BotNotificationUncertain {
				t.Fatalf("blind retry or lost uncertainty: calls=%d %+v", len(notifier.calls), p)
			}
		})
	}
}

type publicationFinishFailure struct{ store.BotPublicationStore }

func (publicationFinishFailure) FinishNotification(context.Context, store.BotNotificationFinish) error {
	return errors.New("crash after send before receipt commit")
}

type publicationFailReceiptDB struct{ botDraftDB }

func (d publicationFailReceiptDB) AppendHomeReport(ctx context.Context, b store.Bot, e *adksession.Event) error {
	return d.botDraftDB.(botPublicationDB).AppendHomeReport(ctx, b, e)
}
func (d publicationFailReceiptDB) InsertActivity(ctx context.Context, a store.BotActivity, key string) error {
	if strings.HasPrefix(key, "home-report:") {
		return errors.New("receipt write failed")
	}
	return d.botDraftDB.InsertActivity(ctx, a, key)
}

type publicationFailReceiptTransaction struct{ botDraftDB }

func (d publicationFailReceiptTransaction) Transaction(ctx context.Context, fn func(botDraftDB) error) error {
	return d.botDraftDB.Transaction(ctx, func(tx botDraftDB) error { return fn(publicationFailReceiptDB{tx}) })
}
func TestBotNotificationsDeferredRevalidate(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, complete := range []bool{false, true} {
			t.Run(string(scope)+fmt.Sprint(complete), func(t *testing.T) {
				repo, ctx, run, now, _ := publicationFixture(t, scope, store.BotReport{Summary: "Changed", Changed: []store.BotEvidence{{Summary: "A"}}}, `{"defer_seconds":60}`)
				goal := store.BotGoal{ID: uuid.New(), BotID: run.BotID, Revision: 1, Title: "Watch goal", Status: store.BotGoalActive}
				if err := repo.db.InsertGoal(ctx, goal); err != nil {
					t.Fatal(err)
				}
				version, err := repo.GetVersion(ctx, run.BotID, run.Version)
				if err != nil {
					t.Fatal(err)
				}
				// Attach this controlled run's persisted trigger identity to the goal.
				version.Triggers = []store.TriggerSpec{{ID: run.TriggerID, GoalID: &goal.ID}}
				seedPublicationVersion(t, repo, ctx, *version)
				notifier := &publicationTransport{}
				publisher := &botdomain.Publisher{Store: repo, Notifier: notifier, Now: func() time.Time { return *now }}
				first, err := publisher.Publish(ctx, run.BotID, run.ID)
				if err != nil {
					t.Fatal(err)
				}
				if first.Notifications[0].State != store.BotNotificationDeferred || len(notifier.calls) != 0 {
					t.Fatal("not deferred")
				}
				if complete {
					goal.Status = store.BotGoalCompleted
					goal.Revision++
					if err := repo.db.UpdateGoal(ctx, goal, 1); err != nil {
						t.Fatal(err)
					}
				}
				*now = now.Add(time.Minute)
				second, err := publisher.Publish(ctx, run.BotID, run.ID)
				if err != nil {
					t.Fatal(err)
				}
				wantState, wantCalls := store.BotNotificationDelivered, 1
				if complete {
					wantState, wantCalls = store.BotNotificationSkipped, 0
				}
				if second.Notifications[0].State != wantState || len(notifier.calls) != wantCalls {
					t.Fatalf("bad deferred reconciliation: %+v calls=%d", second, len(notifier.calls))
				}
				*now = now.Add(time.Hour)
				if _, err := publisher.Publish(ctx, run.BotID, run.ID); err != nil {
					t.Fatal(err)
				}
				if len(notifier.calls) != wantCalls {
					t.Fatal("repeated deferred send")
				}
			})
		}
	}
}

func TestBotNotificationsReceiptReconciliation(t *testing.T) {
	repo, ctx, run, _, target := publicationFixture(t, store.BotScopePersonal, store.BotReport{Summary: "Changed", Changed: []store.BotEvidence{{Summary: "A"}}}, "")
	if _, err := repo.PublishReport(ctx, store.BotPublicationRequest{BotID: run.BotID, RunID: run.ID}); err != nil {
		t.Fatal(err)
	}
	claimed, send, err := repo.ClaimNotification(ctx, store.BotNotificationClaim{BotID: run.BotID, RunID: run.ID, BindingID: target.ID})
	if err != nil || !send {
		t.Fatalf("claim: %v %v", send, err)
	}
	finish := store.BotNotificationFinish{BotID: run.BotID, RunID: run.ID, BindingID: target.ID, AttemptID: uuid.New(), Delivered: true, Receipt: "positive-provider-receipt"}
	if err := repo.FinishNotification(ctx, finish); !errors.Is(err, store.ErrBotConflict) {
		t.Fatalf("accepted wrong attempt: %v", err)
	}
	finish.AttemptID = claimed.AttemptID
	for i := 0; i < 2; i++ {
		if err := repo.FinishNotification(ctx, finish); err != nil {
			t.Fatal(err)
		}
	}
	p, err := repo.PublishReport(ctx, store.BotPublicationRequest{BotID: run.BotID, RunID: run.ID})
	if err != nil {
		t.Fatal(err)
	}
	if p.Notifications[0].State != store.BotNotificationDelivered || p.Notifications[0].Receipt != finish.Receipt {
		t.Fatalf("receipt missing: %+v", p)
	}
	if _, send, err := repo.ClaimNotification(ctx, store.BotNotificationClaim{BotID: run.BotID, RunID: run.ID, BindingID: target.ID}); err != nil || send {
		t.Fatalf("reconciled delivery retried: %v %v", send, err)
	}
	if _, err := repo.PublishReport(context.Background(), store.BotPublicationRequest{BotID: run.BotID, RunID: run.ID}); !errors.Is(err, store.ErrBotForbidden) {
		t.Fatalf("unauthorized publication: %v", err)
	}
}

func TestBotPublicationAtomicReceipt(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			repo, ctx, run, _, _ := publicationFixture(t, scope, store.BotReport{Summary: "No change"}, "")
			original := repo.db
			repo.db = publicationFailReceiptTransaction{original}
			if _, err := repo.PublishReport(ctx, store.BotPublicationRequest{BotID: run.BotID, RunID: run.ID}); err == nil {
				t.Fatal("expected rollback")
			}
			repo.db = original
			if count := publicationTranscriptCount(t, repo, ctx, run.BotID); count != 0 {
				t.Fatalf("orphan transcript event: %d", count)
			}
			if _, err := repo.PublishReport(ctx, store.BotPublicationRequest{BotID: run.BotID, RunID: run.ID}); err != nil {
				t.Fatal(err)
			}
			if count := publicationTranscriptCount(t, repo, ctx, run.BotID); count != 1 {
				t.Fatal(count)
			}
		})
	}
}
