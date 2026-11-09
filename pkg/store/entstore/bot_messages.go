package entstore

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
)

var _ store.BotMessageStore = (*personalBotStore)(nil)
var _ store.BotMessageStore = (*teamBotStore)(nil)

func (s *personalBotStore) AdmitMessage(ctx context.Context, q store.BotMessageRequest) (*store.BotMessageReceipt, error) {
	return s.repository().AdmitMessage(ctx, q)
}
func (s *teamBotStore) AdmitMessage(ctx context.Context, q store.BotMessageRequest) (*store.BotMessageReceipt, error) {
	return s.repository().AdmitMessage(ctx, q)
}

type botMessageDB interface {
	appendAdmittedMessage(context.Context, store.Bot, *session.Event) (int64, error)
}

func (r *botRepository) AdmitMessage(ctx context.Context, q store.BotMessageRequest) (*store.BotMessageReceipt, error) {
	actor, err := r.auth.authorize(ctx, uuid.Nil, true)
	if err != nil {
		return nil, err
	}
	if q.BotID == uuid.Nil || q.Version < 1 || strings.TrimSpace(q.Instructions) == "" || strings.TrimSpace(q.IdempotencyKey) == "" || len(q.IdempotencyKey) > 200 {
		return nil, store.ErrBotInvalid
	}
	hash, err := botHash(q)
	if err != nil {
		return nil, err
	}
	var receipt *store.BotMessageReceipt
	admission, interactive := store.BotMessageAdmissionFromContext(ctx)
	var registered *execution.BotRunIdentity
	err = r.db.Transaction(ctx, func(tx botDraftDB) error {
		rec, err := tx.GetBot(ctx, q.BotID)
		if err != nil {
			return err
		}
		if _, err = r.auth.authorize(ctx, rec.Bot.OwnerID, true); err != nil {
			return err
		}
		if actor != rec.Bot.OwnerID {
			return store.ErrBotForbidden
		}
		previous, err := tx.FindRun(ctx, q.BotID, q.IdempotencyKey)
		if err == nil {
			if previous.RequestHash != hash || previous.Run.Presentation != store.BotRunPresentationHomeInteractive {
				return store.ErrBotConflict
			}
			receipt = messageReceipt(rec.Bot, previous.Run)
			return nil
		}
		if err != store.ErrBotNotFound {
			return err
		}
		if rec.Bot.Version != q.Version {
			return store.ErrBotConflict
		}
		var intent store.BotExecutionIntent
		switch rec.Bot.Status {
		case store.BotDraft:
			intent = store.BotIntentDraftConversation
		case store.BotPaused:
			intent = store.BotIntentConversation
		case store.BotActive:
			intent = store.BotIntentRegular
		default:
			return store.ErrBotForbidden
		}
		v, err := tx.ReadVersion(ctx, q.BotID, q.Version)
		if err != nil {
			return err
		}
		now := r.clock()
		if v.Budget.MaxRunsPerDay != nil {
			loc, err := time.LoadLocation(v.Persona.Timezone)
			if err != nil {
				return store.ErrBotInvalid
			}
			local := now.In(loc)
			count, err := tx.CountRunsSince(ctx, q.BotID, time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc))
			if err != nil {
				return err
			}
			if int64(count) >= *v.Budget.MaxRunsPerDay {
				return store.ErrBotForbidden
			}
		}
		// Only an authenticated owner's new message yields autonomous work.
		// All mutations share this transaction, including failures later in admission.
		if err := r.QuiesceSingleAutonomousRun(ctx, tx, rec); err != nil {
			return err
		}
		// This CAS serializes admission with edits, lifecycle changes and other turns.
		if err := tx.AdvanceSequence(ctx, q.BotID, rec.LastSequence); err != nil {
			return err
		}
		dispatchSequence := rec.LastSequence + 1
		rec.LastSequence = dispatchSequence
		adapter, ok := tx.(botMessageDB)
		if !ok {
			return store.ErrBotUnsupported
		}
		run := store.BotRun{ID: uuid.New(), BotID: q.BotID, Version: q.Version, SessionID: uuid.New(), Trigger: store.TriggerManual, Intent: intent, Presentation: store.BotRunPresentationHomeInteractive, IdempotencyKey: q.IdempotencyKey, Status: store.RunQueued, CreatedAt: now}
		if interactive {
			p, ok := execution.PrincipalFromContext(ctx)
			if !ok || p.Validate() != nil || p.Kind != execution.PrincipalKindUser || p.Subject != actor.String() || p.OrgSlug != r.auth.orgSlug || (r.auth.teamSlug != "" && p.TeamSlug != r.auth.teamSlug) {
				return store.ErrBotForbidden
			}
			identity := execution.BotRunIdentity{BotID: run.BotID, RunID: run.ID, Version: run.Version, ActorID: actor.String(), OrgSlug: p.OrgSlug, TeamSlug: p.TeamSlug}
			authority, err := admission.Register(ctx, identity)
			if err != nil {
				return store.ErrBotForbidden
			}
			registered = &identity
			if authority.Identity != identity {
				return store.ErrBotForbidden
			}
			ceiling := authority.CallerCeiling()
			run.ExecutionMode = execution.BotExecutionModeChatInteractive
			run.CallerCeiling = &ceiling
		}
		run.UserEventID = "bot-message:" + run.ID.String()
		parts := []*genai.Part{genai.NewPartFromText(q.Instructions)}
		for _, file := range q.Attachments {
			parts = append(parts, genai.NewPartFromBytes(file.Data, file.MimeType))
		}
		event := &session.Event{ID: run.UserEventID, Author: "user", Timestamp: now, LLMResponse: model.LLMResponse{Content: &genai.Content{Role: "user", Parts: parts}}}
		run.HistoryCursor, err = adapter.appendAdmittedMessage(ctx, rec.Bot, event)
		if err != nil {
			return err
		}
		if err := r.issueMessageMandate(ctx, tx, rec, &run, q); err != nil {
			return err
		}
		if err := tx.InsertRun(ctx, botStoredRun{Run: run, RequestHash: hash, Instructions: q.Instructions, Attachments: q.Attachments}, rec.Bot.OwnerID); err != nil {
			return err
		}
		payload, err := json.Marshal(map[string]any{"run_id": run.ID, "status": run.Status, "intent": store.BotOutboxDispatch, "actor_id": actor})
		if err != nil {
			return err
		}
		if err := insertBotRunStateActivity(ctx, tx, store.BotActivity{BotID: q.BotID, RunID: &run.ID, Sequence: dispatchSequence, Kind: store.BotActivityRun, Payload: payload, CreatedAt: now}, "dispatch:"+run.ID.String()); err != nil {
			return err
		}
		receipt = messageReceipt(rec.Bot, run)
		return nil
	})
	if err != nil {
		// Even an uncertain commit outcome must not leave runnable authority.
		if registered != nil {
			admission.Cancel(*registered)
		}
		return nil, err
	}
	return receipt, nil
}
func messageReceipt(b store.Bot, run store.BotRun) *store.BotMessageReceipt {
	return &store.BotMessageReceipt{Run: run, HomeSessionID: b.HomeSessionID, UserEventID: run.UserEventID, HistoryCursor: run.HistoryCursor}
}
