package entstore

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	botdomain "github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	adksession "google.golang.org/adk/session"
	"google.golang.org/genai"
)

func TestBotConversationPublication(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, tc := range []struct {
			name   string
			intent store.BotExecutionIntent
			status store.RunStatus
			reply  bool
		}{
			{"conversation", store.BotIntentConversation, store.RunSucceeded, true},
			{"success-no-reply", store.BotIntentConversation, store.RunSucceeded, false},
			{"no-reply", store.BotIntentConversation, store.RunFailed, false},
			{"partial-failure", store.BotIntentConversation, store.RunFailed, true},
			{"regular", store.BotIntentRegular, store.RunSucceeded, true},
			{"draft-test", store.BotIntentDraftTest, store.RunSucceeded, true},
		} {
			t.Run(string(scope)+"/"+tc.name, func(t *testing.T) {
				repo, _, ctx, initial, _ := runtimeFixture(t, scope)
				nowValue := time.Now().UTC()
				now := &nowValue
				b, err := repo.Get(ctx, initial.BotID)
				if err != nil {
					t.Fatal(err)
				}
				if tc.intent == store.BotIntentConversation {
					b, err = repo.Pause(ctx, store.BotLifecycleRequest{BotID: b.ID, ActorID: b.OwnerID, ExpectedRevision: b.Revision, Reason: "owner paused"})
				} else if tc.intent == store.BotIntentDraftTest {
					v := store.DefaultBotVersion()
					v.Charter = "Draft test"
					b, err = repo.CreateDraft(ctx, store.BotCreateRequest{Name: "Draft", OwnerID: b.OwnerID, Version: v, IdempotencyKey: "draft"})
				}
				if err != nil {
					t.Fatal(err)
				}
				v, err := repo.GetVersion(ctx, b.ID, b.Version)
				if err != nil {
					t.Fatal(err)
				}
				v.Delivery = store.DeliveryPolicy{Notify: store.NotifyAlways, Bindings: []store.BotChannelBinding{{ID: uuid.New(), Channel: "slack", ChatID: "approved"}}}
				seedPublicationVersion(t, repo, ctx, *v)
				repo.auth.checkDelivery = func(context.Context, uuid.UUID, store.BotChannelBinding) error { return nil }
				run, err := repo.RequestRun(ctx, store.BotRunRequest{BotID: b.ID, Version: b.Version, Intent: tc.intent, Trigger: store.TriggerManual, IdempotencyKey: "publication", Instructions: "Please answer"})
				if err != nil {
					t.Fatal(err)
				}
				worker := execution.WithBotRunIdentity(ctx, execution.BotRunIdentity{BotID: b.ID, RunID: run.ID, Version: b.Version, ActorID: b.OwnerID.String(), OrgSlug: "org", TeamSlug: "team"})
				lease, err := repo.ClaimRun(worker, store.BotClaimRequest{BotID: b.ID, RunID: run.ID, WorkerID: "publication", ExpiresAt: now.Add(time.Minute)})
				if err != nil {
					t.Fatal(err)
				}
				var sessions store.BotTranscriptReader
				var appendRun func(*adksession.Event) error
				switch db := repo.db.(type) {
				case personalBotDB:
					s := &personalSessionStore{client: db.client}
					sessions = s
					appendRun = func(e *adksession.Event) error { return s.appendEventInternal(ctx, run.SessionID.String(), e) }
				case teamBotDB:
					s := &teamSessionStore{client: db.client}
					sessions = s
					appendRun = func(e *adksession.Event) error { return s.appendEventInternal(ctx, run.SessionID.String(), e) }
				}
				user := &adksession.Event{ID: uuid.NewString(), Author: "user", Timestamp: *now, LLMResponse: model.LLMResponse{Content: genai.NewContentFromText("Please answer", "user")}}
				reply := &adksession.Event{ID: uuid.NewString(), Author: "assistant", Timestamp: *now, LLMResponse: model.LLMResponse{Content: genai.NewContentFromText("Actual conversational reply", "model")}}
				events := []*adksession.Event{user}
				if tc.reply {
					events = append(events, reply)
				}
				for _, e := range events {
					if err := repo.appendBotRunEvent(worker, lease, run.SessionID, e); err != nil {
						t.Fatal(err)
					}
					if tc.intent != store.BotIntentRegular {
						if err := appendRun(e); err != nil {
							t.Fatal(err)
						}
					}
				}
				before, err := sessions.ReadBotTranscript(ctx, b.HomeSessionID, 0, 200)
				if err != nil {
					t.Fatal(err)
				}
				var cursor int64
				if len(before) > 0 {
					cursor = before[len(before)-1].Cursor
				}
				report := store.BotReport{Summary: "Actual conversational reply"}
				if tc.status == store.RunFailed {
					report.Unverified = []string{"diagnostic retained"}
				}
				if err := repo.CommitRun(worker, store.BotRunCommit{Lease: lease, Status: tc.status, Report: report}); err != nil {
					t.Fatal(err)
				}
				notifier := &publicationTransport{}
				publisher := &botdomain.Publisher{Store: repo, Notifier: notifier, Now: func() time.Time { return *now }}
				p, err := publisher.Publish(ctx, b.ID, run.ID)
				if err != nil {
					t.Fatal(err)
				}
				again, err := publisher.Publish(ctx, b.ID, run.ID)
				if err != nil {
					t.Fatal(err)
				}
				if p.EventID != again.EventID || p.ReportSequence != again.ReportSequence {
					t.Fatal("receipt not stable")
				}
				live, err := sessions.ReadBotTranscript(ctx, b.HomeSessionID, cursor, 200)
				if err != nil {
					t.Fatal(err)
				}
				restored, err := sessions.ReadBotTranscript(ctx, b.HomeSessionID, 0, 200)
				if err != nil {
					t.Fatal(err)
				}
				wantNew := 1
				if tc.intent == store.BotIntentConversation && tc.status == store.RunSucceeded && tc.reply {
					wantNew = 0
					if p.EventID != reply.ID {
						t.Errorf("receipt did not reuse actual reply: %s", p.EventID)
					}
				}
				if len(live) != wantNew || len(restored) != len(before)+wantNew {
					t.Errorf("duplicate publication: live=%d restored=%d before=%d wantNew=%d", len(live), len(restored), len(before), wantNew)
				}
				if wantNew == 1 && (len(live) != 1 || !strings.Contains(live[0].Event.Content.Parts[0].Text, "Run report")) {
					t.Error("missing diagnostic report")
				}
				if tc.intent == store.BotIntentConversation {
					if len(notifier.calls) != 0 {
						t.Error("conversation sent external notification")
					}
					if len(p.Notifications) != 1 || p.Notifications[0].State != store.BotNotificationSkipped || !strings.Contains(p.Notifications[0].Reason, "conversation") {
						t.Errorf("missing intent-based notification skip: %+v", p.Notifications)
					}
				}
				saved, err := repo.db.ReadRun(ctx, b.ID, run.ID)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(saved.Run.Report, report) || saved.Run.Status != tc.status {
					t.Error("run diagnostics changed")
				}
				receipt, err := readPublication(ctx, repo.db, b.ID, run.ID, p.ReportSequence)
				if err != nil || receipt.EventID != p.EventID {
					t.Fatal("missing durable receipt", err)
				}
			})
		}
	}
}
