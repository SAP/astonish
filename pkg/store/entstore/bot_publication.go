package entstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	botdomain "github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	adksession "google.golang.org/adk/session"
	"google.golang.org/genai"
)

var _ store.BotPublicationStore = (*personalBotStore)(nil)
var _ store.BotPublicationStore = (*teamBotStore)(nil)

func (s *personalBotStore) PublishReport(c context.Context, q store.BotPublicationRequest) (*store.BotPublication, error) {
	return s.repository().PublishReport(c, q)
}
func (s *teamBotStore) PublishReport(c context.Context, q store.BotPublicationRequest) (*store.BotPublication, error) {
	return s.repository().PublishReport(c, q)
}
func (s *personalBotStore) ClaimNotification(c context.Context, q store.BotNotificationClaim) (*store.BotNotification, bool, error) {
	return s.repository().ClaimNotification(c, q)
}
func (s *teamBotStore) ClaimNotification(c context.Context, q store.BotNotificationClaim) (*store.BotNotification, bool, error) {
	return s.repository().ClaimNotification(c, q)
}
func (s *personalBotStore) FinishNotification(c context.Context, q store.BotNotificationFinish) error {
	return s.repository().FinishNotification(c, q)
}
func (s *teamBotStore) FinishNotification(c context.Context, q store.BotNotificationFinish) error {
	return s.repository().FinishNotification(c, q)
}

type botPublicationDB interface {
	AppendHomeReport(context.Context, store.Bot, *adksession.Event) error
}

func publicationKey(run uuid.UUID, sequence int64) string {
	return fmt.Sprintf("home-report:%s:%d", run, sequence)
}
func notificationKey(run, binding uuid.UUID, sequence int64, state string) string {
	return fmt.Sprintf("notification:%s:%d:%s:%s", run, sequence, binding, state)
}

// committedPublicationReport selects only committed outcome intents, never the
// publication receipts that share the report activity kind. Explicit sequences
// allow replay of earlier outcomes after the mutable run has resumed.
func committedPublicationReport(ctx context.Context, db botDraftDB, bot, run uuid.UUID, sequence int64) (store.BotRun, int64, error) {
	saved, err := db.ReadRun(ctx, bot, run)
	if err != nil {
		return store.BotRun{}, 0, err
	}
	var selected store.BotRun
	var selectedSequence int64
	after := int64(0)
	if sequence > 0 {
		after = sequence - 1
	}
	for {
		activities, err := db.ReadActivity(ctx, bot, after, 200)
		if err != nil {
			return selected, 0, err
		}
		for _, a := range activities {
			after = a.Sequence
			if sequence > 0 && a.Sequence > sequence {
				return selected, selectedSequence, store.ErrBotNotFound
			}
			if a.Kind != store.BotActivityReport || a.RunID == nil || *a.RunID != run {
				continue
			}
			var outcome struct {
				Intent store.BotOutboxKind `json:"intent"`
				Status store.RunStatus     `json:"status"`
				Report store.BotReport     `json:"report"`
			}
			if err := json.Unmarshal(a.Payload, &outcome); err != nil {
				return selected, 0, err
			}
			if outcome.Intent != store.BotOutboxPublish {
				continue
			}
			selected = saved.Run
			selected.Status, selected.Report = outcome.Status, outcome.Report
			selectedSequence = a.Sequence
			if sequence > 0 {
				return selected, selectedSequence, nil
			}
		}
		if len(activities) < 200 {
			break
		}
	}
	if selectedSequence == 0 {
		return selected, 0, store.ErrBotNotFound
	}
	return selected, selectedSequence, nil
}

func (r *botRepository) publicationOwner(ctx context.Context, db botDraftDB, id uuid.UUID) (*botStoredRecord, error) {
	if _, err := r.auth.authorize(ctx, uuid.Nil, true); err != nil {
		return nil, err
	}
	rec, err := db.GetBot(ctx, id)
	if err != nil {
		return nil, err
	}
	actor, err := r.auth.authorize(ctx, rec.Bot.OwnerID, true)
	if err != nil {
		return nil, err
	}
	if actor != rec.Bot.OwnerID || rec.Bot.Scope != r.auth.scope {
		return nil, store.ErrBotForbidden
	}
	return rec, nil
}
func publicationTime(now time.Time, r *botRepository) time.Time {
	if now.IsZero() {
		return r.clock()
	}
	return now.UTC()
}
func insertPublicationActivity(ctx context.Context, db botDraftDB, rec *botStoredRecord, run uuid.UUID, kind store.BotActivityKind, key string, payload any, now time.Time) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if err := db.AdvanceSequence(ctx, rec.Bot.ID, rec.LastSequence); err != nil {
		return err
	}
	rec.LastSequence++
	return db.InsertActivity(ctx, store.BotActivity{BotID: rec.Bot.ID, RunID: &run, Sequence: rec.LastSequence, Kind: kind, Payload: data, CreatedAt: now}, key)
}
func readPublication(ctx context.Context, db botDraftDB, bot, run uuid.UUID, sequence int64) (*store.BotPublication, error) {
	if sequence == 0 {
		_, latest, err := committedPublicationReport(ctx, db, bot, run, 0)
		if err != nil {
			return nil, err
		}
		sequence = latest
	}
	a, err := db.FindActivity(ctx, bot, publicationKey(run, sequence))
	if err != nil {
		return nil, err
	}
	var p store.BotPublication
	if err := json.Unmarshal(a.Payload, &p); err != nil {
		return nil, err
	}
	return &p, nil
}
func (r *botRepository) PublishReport(ctx context.Context, q store.BotPublicationRequest) (*store.BotPublication, error) {
	if q.BotID == uuid.Nil || q.RunID == uuid.Nil {
		return nil, store.ErrBotInvalid
	}
	var result *store.BotPublication
	err := r.db.Transaction(ctx, func(tx botDraftDB) error {
		rec, err := r.publicationOwner(ctx, tx, q.BotID)
		if err != nil {
			return err
		}
		prior, err := readPublication(ctx, tx, q.BotID, q.RunID, q.ReportSequence)
		if err == nil {
			result = prior
			for i := range result.Notifications {
				n, err := readNotification(ctx, tx, result, result.Notifications[i].Target.ID)
				if err != nil {
					return err
				}
				result.Notifications[i] = *n
			}
			return nil
		}
		if !errors.Is(err, store.ErrBotNotFound) {
			return err
		}
		if rec.Bot.Status == store.BotDeleted {
			return store.ErrBotConflict
		}
		savedRun, sequence, err := committedPublicationReport(ctx, tx, q.BotID, q.RunID, q.ReportSequence)
		if err != nil {
			return err
		}
		saved := struct{ Run store.BotRun }{savedRun}
		if !saved.Run.Status.Terminal() && saved.Run.Status != store.RunWaitingApproval && saved.Run.Status != store.RunInterrupted {
			return store.ErrBotConflict
		}
		if strings.TrimSpace(saved.Run.Report.Summary) == "" {
			return store.ErrBotConflict
		}
		version, err := tx.ReadVersion(ctx, q.BotID, saved.Run.Version)
		if err != nil {
			return err
		}
		adapter, ok := tx.(botPublicationDB)
		if !ok {
			return store.ErrBotUnsupported
		}
		now := publicationTime(q.Now, r)
		p := &store.BotPublication{ReportSequence: sequence, BotID: q.BotID, RunID: q.RunID, Version: saved.Run.Version, HomeSessionID: rec.Bot.HomeSessionID, EventID: uuid.NewSHA1(q.RunID, []byte(publicationKey(q.RunID, sequence))).String(), Text: botdomain.RenderReport(saved.Run), PublishedAt: now}
		if ack := saved.Run.Report.Acknowledgment; ack != nil && ack.Kind == store.BotReportAcknowledgmentSchedulePending && len(ack.OperationIDs) > 0 {
			p.Suppressed = true
			p.Text = ""
			if err := insertPublicationActivity(ctx, tx, rec, q.RunID, store.BotActivityReport, publicationKey(q.RunID, sequence), p, now); err != nil {
				return err
			}
			result = p
			return nil
		}
		// A committed schedule receipt is a distinct source-bound Home event, not
		// the model's conversational reply, and never authorizes delivery.
		scheduleReceipt, err := authorizedSchedulePublication(ctx, tx, rec, q.RunID, sequence)
		if err != nil {
			return err
		}
		canonicalOperationEvent := ""
		if scheduleReceipt {
			canonicalOperationEvent, err = canonicalScheduleOperationEvent(ctx, tx, rec, q.RunID, sequence)
			if err != nil {
				return err
			}
			if canonicalOperationEvent != "" {
				p.EventID = canonicalOperationEvent
			}
		}
		evaluation := botdomain.EvaluateNotification(version.Delivery, saved.Run.Report, now, false)
		if saved.Run.ControlOnly() || scheduleReceipt {
			evaluation.Decision = store.NotificationSkip
			evaluation.Reason = "Owner conversation does not authorize external notifications"
		}
		stored, err := tx.ReadRun(ctx, q.BotID, q.RunID)
		if err != nil {
			return err
		}
		var reply *adksession.Event
		if stored.Work == nil && !scheduleReceipt && saved.Run.UsesHomeHistory() && saved.Run.Status == store.RunSucceeded {
			reply, err = persistedConversationReply(ctx, tx, rec.Bot, saved.Run)
			if err != nil {
				return err
			}
			if reply != nil {
				p.EventID = reply.ID
			}
		}
		seen := map[uuid.UUID]bool{}
		targets := map[string]bool{}
		for _, binding := range version.Delivery.Bindings {
			target := binding.Channel + "\x00" + binding.ChatID + "\x00" + binding.ThreadID
			if binding.ID == uuid.Nil || strings.TrimSpace(binding.Channel) == "" || strings.TrimSpace(binding.ChatID) == "" || seen[binding.ID] || targets[target] {
				continue
			}
			seen[binding.ID] = true
			targets[target] = true
			state := store.BotNotificationPending
			if evaluation.Decision == store.NotificationSkip {
				state = store.BotNotificationSkipped
			}
			if evaluation.Decision == store.NotificationDefer {
				state = store.BotNotificationDeferred
			}
			p.Notifications = append(p.Notifications, store.BotNotification{ReportSequence: sequence, BotID: q.BotID, RunID: q.RunID, Target: binding, State: state, Decision: evaluation.Decision, Reason: evaluation.Reason, AvailableAt: evaluation.AvailableAt, Text: p.Text})
		}
		event := &adksession.Event{ID: p.EventID, InvocationID: "bot-report:" + q.RunID.String(), Author: "assistant", Timestamp: now, LLMResponse: model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{Text: p.Text}}}}}
		if reply == nil && canonicalOperationEvent == "" {
			if err := adapter.AppendHomeReport(ctx, rec.Bot, event); err != nil {
				return err
			}
		}
		if err := insertPublicationActivity(ctx, tx, rec, q.RunID, store.BotActivityReport, publicationKey(q.RunID, sequence), p, now); err != nil {
			return err
		}
		result = p
		return nil
	})
	return result, err
}

// persistedConversationReply requires the same actual model event in this run
// and its home. Older turns, user text, thoughts and partial chunks cannot hide
// a missing reply. Read through transaction-bound scoped stores, never UI state.
func persistedConversationReply(ctx context.Context, db botDraftDB, b store.Bot, run store.BotRun) (*adksession.Event, error) {
	var reader store.BotTranscriptReader
	switch d := db.(type) {
	case personalBotDB:
		reader = &personalSessionStore{client: d.client}
	case teamBotDB:
		reader = &teamSessionStore{client: d.client}
	default:
		return nil, store.ErrBotUnsupported
	}
	ids := map[string]bool{}
	var reply *adksession.Event
	for _, id := range []uuid.UUID{run.SessionID, b.HomeSessionID} {
		var cursor int64
		for {
			rows, err := reader.ReadBotTranscript(ctx, id, cursor, 200)
			if err != nil {
				return nil, err
			}
			for _, row := range rows {
				cursor = row.Cursor
				e := row.Event
				if e == nil || e.ID == "" || e.Partial || e.Content == nil || e.Content.Role != "model" {
					continue
				}
				var text strings.Builder
				for _, part := range e.Content.Parts {
					if part != nil && !part.Thought {
						text.WriteString(part.Text)
					}
				}
				if strings.TrimSpace(text.String()) == "" {
					continue
				}
				if id == run.SessionID {
					ids[e.ID] = true
				} else if ids[e.ID] {
					reply = e
				}
			}
			if len(rows) < 200 {
				break
			}
		}
	}
	return reply, nil
}

func readNotification(ctx context.Context, db botDraftDB, p *store.BotPublication, binding uuid.UUID) (*store.BotNotification, error) {
	var original *store.BotNotification
	for i := range p.Notifications {
		if p.Notifications[i].Target.ID == binding {
			n := p.Notifications[i]
			original = &n
			break
		}
	}
	if original == nil {
		return nil, store.ErrBotNotFound
	}
	for _, state := range []string{"delivered", "claim", "skip"} {
		a, err := db.FindActivity(ctx, p.BotID, notificationKey(p.RunID, binding, p.ReportSequence, state))
		if errors.Is(err, store.ErrBotNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		var n store.BotNotification
		if err := json.Unmarshal(a.Payload, &n); err != nil {
			return nil, err
		}
		return &n, nil
	}
	return original, nil
}

func (r *botRepository) ClaimNotification(ctx context.Context, q store.BotNotificationClaim) (*store.BotNotification, bool, error) {
	var result *store.BotNotification
	send := false
	err := r.db.Transaction(ctx, func(tx botDraftDB) error {
		rec, err := r.publicationOwner(ctx, tx, q.BotID)
		if err != nil {
			return err
		}
		p, err := readPublication(ctx, tx, q.BotID, q.RunID, q.ReportSequence)
		if err != nil {
			return err
		}
		n, err := readNotification(ctx, tx, p, q.BindingID)
		if err != nil {
			return err
		}
		result = n
		if n.State != store.BotNotificationPending && n.State != store.BotNotificationDeferred {
			return nil
		}
		now := publicationTime(q.Now, r)
		if now.Before(n.AvailableAt) {
			return nil
		}
		skip := func(reason string) error {
			n.State = store.BotNotificationSkipped
			n.Decision = store.NotificationSkip
			n.Reason = reason
			return insertPublicationActivity(ctx, tx, rec, q.RunID, store.BotActivityNotification, notificationKey(q.RunID, q.BindingID, p.ReportSequence, "skip"), n, now)
		}
		if rec.Bot.Status != store.BotActive || rec.Bot.Version != p.Version {
			return skip("bot inactive or approved version changed")
		}
		savedRun, _, err := committedPublicationReport(ctx, tx, q.BotID, q.RunID, p.ReportSequence)
		if err != nil {
			return err
		}
		saved := struct{ Run store.BotRun }{savedRun}
		if saved.Run.ControlOnly() {
			return skip("Owner conversation does not authorize external notifications")
		}
		version, err := tx.ReadVersion(ctx, q.BotID, p.Version)
		if err != nil {
			return err
		}
		matched := false
		for _, binding := range version.Delivery.Bindings {
			if binding == n.Target {
				matched = true
			}
		}
		if !matched {
			return skip("channel binding changed")
		}
		if err := r.authorizeDeliveryIdentity(ctx, rec.Bot.OwnerID, n.Target); err != nil {
			return skip("notification destination authority revoked or unproven")
		}
		// Goal-scoped notifications are obsolete once the goal completes, even if a
		// reminder was deferred while it was still active.
		for _, trigger := range version.Triggers {
			if trigger.ID == saved.Run.TriggerID && trigger.GoalID != nil {
				goal, err := tx.ReadGoal(ctx, q.BotID, *trigger.GoalID)
				if errors.Is(err, store.ErrBotNotFound) {
					return skip("goal no longer exists")
				}
				if err != nil {
					return err
				}
				if goal.Status != store.BotGoalActive {
					return skip("goal completed")
				}
			}
		}
		evaluation := botdomain.EvaluateNotification(version.Delivery, saved.Run.Report, now, true)
		if evaluation.Decision != store.NotificationSend {
			return skip(evaluation.Reason)
		}
		n.State = store.BotNotificationUncertain
		n.Decision = store.NotificationSend
		n.Reason = "delivery claimed; awaiting positive transport receipt"
		n.AttemptID = uuid.New()
		if err := insertPublicationActivity(ctx, tx, rec, q.RunID, store.BotActivityNotification, notificationKey(q.RunID, q.BindingID, p.ReportSequence, "claim"), n, now); err != nil {
			return err
		}
		send = true
		return nil
	})
	if err != nil {
		send = false
	}
	return result, send, err
}

func (r *botRepository) FinishNotification(ctx context.Context, q store.BotNotificationFinish) error {
	if q.AttemptID == uuid.Nil || q.Delivered && strings.TrimSpace(q.Receipt) == "" {
		return store.ErrBotInvalid
	}
	return r.db.Transaction(ctx, func(tx botDraftDB) error {
		rec, err := r.publicationOwner(ctx, tx, q.BotID)
		if err != nil {
			return err
		}
		p, err := readPublication(ctx, tx, q.BotID, q.RunID, q.ReportSequence)
		if err != nil {
			return err
		}
		n, err := readNotification(ctx, tx, p, q.BindingID)
		if err != nil {
			return err
		}
		if n.AttemptID != q.AttemptID {
			return store.ErrBotConflict
		}
		if n.State == store.BotNotificationDelivered {
			if q.Delivered && n.Receipt == q.Receipt {
				return nil
			}
			return store.ErrBotConflict
		}
		if n.State != store.BotNotificationUncertain {
			return store.ErrBotConflict
		}
		if !q.Delivered {
			return nil
		}
		n.State = store.BotNotificationDelivered
		n.Receipt = q.Receipt
		n.Reason = "transport confirmed delivery"
		return insertPublicationActivity(ctx, tx, rec, q.RunID, store.BotActivityNotification, notificationKey(q.RunID, q.BindingID, p.ReportSequence, "delivered"), n, publicationTime(q.Now, r))
	})
}
