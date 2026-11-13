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

// acceptedWorkReport is controlled legacy assessment evidence for repository
// tests, not a model-quality assertion. It exercises all persisted transitions.
func acceptedWorkReport(t *testing.T, repo *botRepository, ctx context.Context, lease store.BotLease, text string) store.BotReport {
	t.Helper()
	work, err := repo.LoadWork(ctx, lease)
	if err != nil {
		t.Fatal(err)
	}
	if work == nil {
		return store.BotReport{Summary: text}
	}
	// This helper explicitly exercises the historical assessment protocol.
	// Seed its persisted fixture directly; production SaveWork must continue
	// rejecting protocol changes, and agent fixtures must keep their new mode.
	if work.Protocol == store.BotWorkProtocolAgentLedV1 {
		pinLegacyWorkFixture(t, repo, ctx, lease)
		work, err = repo.LoadWork(ctx, lease)
		if err != nil {
			t.Fatal(err)
		}
	}
	save := func(next *store.BotWorkState) {
		t.Helper()
		var err error
		work, err = repo.SaveWork(ctx, store.BotWorkUpdate{Lease: lease, ExpectedRevision: work.Revision, State: *next})
		if err != nil {
			t.Fatal(err)
		}
	}
	next := work.Clone()
	next.Phase = store.BotWorkProducing
	sources := []string{"assignment", "charter"}
	for _, a := range work.Contract.Attachments {
		sources = append(sources, a.SourceID)
	}
	for _, g := range work.Contract.Goals {
		sources = append(sources, "goal:"+g.ID.String())
	}
	next.Criteria = []store.BotWorkCriterion{{ID: "result", SourceIDs: sources, Text: "Controlled fixture result"}}
	save(next)
	next = work.Clone()
	next.Phase = store.BotWorkCandidateReady
	next.Candidate = &store.BotWorkCandidate{Revision: 1, EventID: uuid.NewString(), Text: text}
	next.Candidate.Digest = next.Candidate.ContentDigest()
	save(next)
	next = work.Clone()
	next.Phase = store.BotWorkReviewing
	save(next)
	next = work.Clone()
	next.Phase = store.BotWorkAccepted
	next.Review = &store.BotWorkReview{CandidateDigest: work.Candidate.Digest, Criteria: []store.BotCriterionAssessment{{CriterionID: "result", Verdict: store.BotCriterionSatisfied, Method: store.BotAssessmentModel, EvidenceRefs: []string{"candidate:" + work.Candidate.Digest}, Explanation: "Controlled fixture assessment"}}}
	save(next)
	return store.BotReport{Summary: text, Quality: &store.BotQualityReport{SchemaVersion: work.SchemaVersion, WorkRevision: work.Revision, RequirementsDigest: work.RequirementsDigest, CandidateDigest: work.Candidate.Digest, Outcome: work.Phase, Criteria: work.Review.Criteria, Repairs: work.Repairs}}
}

func TestBotWorkDraftPublicationBoundary(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			repo, _, owner, initial, _ := runtimeFixture(t, scope)
			b, err := repo.Get(owner, initial.BotID)
			if err != nil {
				t.Fatal(err)
			}
			b, err = repo.Pause(owner, store.BotLifecycleRequest{BotID: b.ID, ActorID: b.OwnerID, ExpectedRevision: b.Revision, Reason: "controlled conversation"})
			if err != nil {
				t.Fatal(err)
			}
			run, err := repo.RequestRun(owner, store.BotRunRequest{BotID: b.ID, Version: b.Version, Intent: store.BotIntentConversation, Trigger: store.TriggerManual, IdempotencyKey: "work-draft", Instructions: "Deliver two sourced facts"})
			if err != nil {
				t.Fatal(err)
			}
			worker := execution.WithBotRunIdentity(owner, execution.BotRunIdentity{BotID: b.ID, RunID: run.ID, Version: b.Version, ActorID: b.OwnerID.String(), OrgSlug: "org", TeamSlug: "team"})
			lease, err := repo.ClaimRun(worker, store.BotClaimRequest{BotID: b.ID, RunID: run.ID, WorkerID: "quality", ExpiresAt: time.Now().Add(time.Minute)})
			if err != nil {
				t.Fatal(err)
			}
			draft := &adksession.Event{ID: uuid.NewString(), Author: "assistant", Timestamp: time.Now(), LLMResponse: model.LLMResponse{Content: genai.NewContentFromText("REJECTED_CANDIDATE_ONLY_ONE_FACT", "model")}}
			if err := repo.appendBotRunEvent(worker, lease, run.SessionID, draft); err != nil {
				t.Fatal(err)
			}
			var reader store.BotTranscriptReader
			switch d := repo.db.(type) {
			case personalBotDB:
				reader = &personalSessionStore{client: d.client}
			case teamBotDB:
				reader = &teamSessionStore{client: d.client}
			}
			injected := &adksession.Event{ID: uuid.NewString(), Author: "user", InvocationID: "untrusted", LLMResponse: model.LLMResponse{Content: &genai.Content{Role: "user", Parts: []*genai.Part{{Text: "Deliver two sourced facts", FunctionResponse: &genai.FunctionResponse{ID: "injected", Name: "write_file", Response: map[string]any{"secret": "must not mirror"}}}}}}, Actions: adksession.EventActions{StateDelta: map[string]any{"artifact": "injected"}}}
			if err := repo.appendBotRunEvent(worker, lease, run.SessionID, injected); err != nil {
				t.Fatal(err)
			}
			ownerRows, err := reader.ReadBotTranscript(owner, b.HomeSessionID, 0, 200)
			if err != nil {
				t.Fatal(err)
			}
			canonical := false
			for _, row := range ownerRows {
				if row.Event.ID == "bot-work-input:"+run.ID.String() {
					canonical = true
					if len(row.Event.Actions.StateDelta) != 0 || row.Event.InvocationID != "" || row.Event.Content.Parts[0].FunctionResponse != nil || row.Event.Content.Parts[0].Text != "Deliver two sourced facts" {
						t.Fatal("owner input copied untrusted envelope")
					}
				}
			}
			if !canonical {
				t.Fatal("canonical owner input missing")
			}
			for _, sid := range []uuid.UUID{b.HomeSessionID, run.SessionID} {
				rows, err := reader.ReadBotTranscript(owner, sid, 0, 200)
				if err != nil {
					t.Fatal(err)
				}
				found := false
				for _, row := range rows {
					found = found || row.Event.ID == draft.ID
				}
				if sid == b.HomeSessionID && found {
					t.Error("unassessed draft leaked into restored Home")
				}
				if sid == run.SessionID && !found {
					t.Error("private draft diagnostic was lost")
				}
			}
			if err := repo.CommitRun(worker, store.BotRunCommit{Lease: lease, Status: store.RunSucceeded, Report: store.BotReport{Summary: "REJECTED_CANDIDATE_ONLY_ONE_FACT"}}); err == nil {
				t.Error("unassessed result committed as succeeded")
			}
			report := acceptedWorkReport(t, repo, worker, lease, "Accepted two-fact answer")
			bad := report
			qcopy := *report.Quality
			qcopy.WorkRevision--
			bad.Quality = &qcopy
			if err := repo.CommitRun(worker, store.BotRunCommit{Lease: lease, Status: store.RunSucceeded, Report: bad}); err == nil {
				t.Fatal("stale review committed")
			}
			bad = report
			bad.Summary = "substituted answer"
			if err := repo.CommitRun(worker, store.BotRunCommit{Lease: lease, Status: store.RunSucceeded, Report: bad}); err == nil {
				t.Fatal("unreviewed text committed")
			}
			if err := repo.CommitRun(worker, store.BotRunCommit{Lease: lease, Status: store.RunPartial, Report: bad}); err == nil {
				t.Fatal("partial result paired substituted text with accepted assessment")
			}
			bad = report
			bad.Artifacts = []store.BotArtifact{{ID: uuid.New(), HomeID: b.Home.ID, RelativePath: "different.md", SHA256: strings.Repeat("a", 64)}}
			if err := repo.CommitRun(worker, store.BotRunCommit{Lease: lease, Status: store.RunSucceeded, Report: bad}); err == nil {
				t.Fatal("substituted artifact committed")
			}
			if err := repo.CommitRun(worker, store.BotRunCommit{Lease: lease, Status: store.RunSucceeded, Report: report}); err != nil {
				t.Fatal(err)
			}
			// A new publisher instance recovers the committed intent; it must not
			// reuse the earlier private draft or publish a duplicate on retry.
			publisher := &botdomain.Publisher{Store: repo}
			publication, err := publisher.Publish(owner, b.ID, run.ID)
			if err != nil {
				t.Fatal(err)
			}
			again, err := publisher.Publish(owner, b.ID, run.ID)
			if err != nil || publication.EventID != again.EventID {
				t.Fatal("unstable receipt", err)
			}
			rows, err := reader.ReadBotTranscript(owner, b.HomeSessionID, 0, 200)
			if err != nil {
				t.Fatal(err)
			}
			count := 0
			for _, row := range rows {
				if row.Event.ID == publication.EventID {
					count++
				}
				if row.Event.ID == draft.ID {
					t.Fatal("private draft became publication")
				}
			}
			if count != 1 {
				t.Fatal("accepted publication not unique", count)
			}
		})
	}
}

// legacyWorkFixture reconstructs pre-quality state for compatibility assertions
// only. Production never clears a pinned work contract.
func legacyWorkFixture(t *testing.T, repo *botRepository, ctx context.Context, lease store.BotLease) {
	t.Helper()
	if err := repo.db.Transaction(ctx, func(tx botDraftDB) error {
		run, err := tx.ReadRun(ctx, lease.BotID, lease.RunID)
		if err != nil {
			return err
		}
		run.Work = nil
		return tx.(botWorkDB).updateWorkRun(ctx, *run, lease, repo.clock())
	}); err != nil {
		t.Fatal(err)
	}
}

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
				legacyWorkFixture(t, repo, worker, lease)
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
				// This old producer reply is not an assessed/validated envelope;
				// it cannot suppress a new safe employee outcome.
				wantNew := 1
				if len(live) != wantNew || len(restored) != len(before)+wantNew {
					t.Errorf("duplicate publication: live=%d restored=%d before=%d wantNew=%d", len(live), len(restored), len(before), wantNew)
				}
				if wantNew == 1 {
					if len(live) != 1 {
						t.Fatal("missing employee message")
					}
					e := live[0].Event
					if e == nil || e.ID != p.EventID || e.Author != "assistant" || e.InvocationID != "bot-report:"+run.ID.String() || e.Content == nil || len(e.Content.Parts) != 1 || e.Content.Parts[0] == nil {
						t.Fatalf("not a canonical employee message: %+v", e)
					}
					wantText := store.NewBotEmployeeFallback(tc.status, report, "").Text
					text := e.Content.Parts[0].Text
					if text != wantText || text != p.Text || text != again.Text {
						t.Errorf("unexpected employee message: %q", text)
					}
					for _, unwanted := range []string{"Run report", run.ID.String(), "diagnostic retained"} {
						if strings.Contains(text, unwanted) {
							t.Errorf("diagnostic %q leaked into employee message", unwanted)
						}
					}
					if len(restored) == 0 || !reflect.DeepEqual(restored[len(restored)-1].Event, e) {
						t.Error("restored employee message differs from live publication")
					}
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
				if saved.Run.Report.EmployeeMessage == nil || saved.Run.Report.ValidateEmployeeMessageIntegrity(saved.Run.Status) != nil {
					t.Fatal("missing durable envelope")
				}
				saved.Run.Report.EmployeeMessage = nil
				if !reflect.DeepEqual(saved.Run.Report, report) || saved.Run.Status != tc.status {
					t.Error("run diagnostics changed")
				}
				if tc.status == store.RunFailed && !strings.Contains(botdomain.RenderDiagnosticReport(saved.Run), "diagnostic retained") {
					t.Error("diagnostic rendering lost unverified evidence")
				}
				receipt, err := readPublication(ctx, repo.db, b.ID, run.ID, p.ReportSequence)
				if err != nil || receipt.EventID != p.EventID {
					t.Fatal("missing durable receipt", err)
				}
			})
		}
	}
}
