package entstore

import (
	"context"
	"encoding/json"
	"entgo.io/ent/dialect/sql"
	"entgo.io/ent/dialect/sql/sqljson"
	personalbot "github.com/SAP/astonish/ent/personal/bot"
	personalsession "github.com/SAP/astonish/ent/personal/session"
	personalevent "github.com/SAP/astonish/ent/personal/sessionevent"
	teambot "github.com/SAP/astonish/ent/team/bot"
	teamsession "github.com/SAP/astonish/ent/team/session"
	teamevent "github.com/SAP/astonish/ent/team/sessionevent"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	adksession "google.golang.org/adk/session"
	"google.golang.org/genai"
	"time"
)

// botHomeArtifactEvent projects only the canonical runtime attachment envelope.
// Keep internal state out of the home conversation and never manufacture model
// content to make a state-only attachment pass the content projection gate.
func botHomeArtifactEvent(r store.BotRun, e *adksession.Event) bool {
	if e.Author != "bot_runtime" {
		return false
	}
	raw, err := json.Marshal(e.Actions.StateDelta["bot_runtime_event"])
	if err != nil {
		return false
	}
	var envelope struct {
		Type      string    `json:"type"`
		RunID     uuid.UUID `json:"run_id"`
		SessionID uuid.UUID `json:"session_id"`
		Payload   struct {
			Path string `json:"path"`
		} `json:"payload"`
	}
	return json.Unmarshal(raw, &envelope) == nil && envelope.Type == "artifact" && envelope.RunID == r.ID && envelope.SessionID == r.SessionID && envelope.Payload.Path != ""
}

func botHomeRunStateEvent(r store.BotRun, e *adksession.Event) bool {
	if r.Presentation != store.BotRunPresentationHomeInteractive || e.Author != "bot_runtime" {
		return false
	}
	raw, err := json.Marshal(e.Actions.StateDelta[store.BotRunStateKey])
	if err != nil {
		return false
	}
	var state store.BotRunState
	return json.Unmarshal(raw, &state) == nil && state.SchemaVersion == store.BotRunStateSchemaVersion && state.RunID == r.ID && state.BotID == r.BotID && state.SessionID == r.SessionID && state.Presentation == r.Presentation
}

var _ store.BotTranscriptReader = (*teamSessionStore)(nil)
var _ store.BotTranscriptReader = (*personalSessionStore)(nil)

func (s *teamSessionStore) ReadBotTranscript(ctx context.Context, id uuid.UUID, after int64, limit int) ([]store.BotTranscriptEvent, error) {
	if id == uuid.Nil || after < 0 || limit < 1 || limit > 200 {
		return nil, store.ErrBotInvalid
	}
	rows, err := s.client.SessionEvent.Query().Where(teamevent.SessionIDEQ(id.String()), teamevent.IDGT(after)).Order(teamevent.ByID()).Limit(limit).All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]store.BotTranscriptEvent, 0, len(rows))
	for _, row := range rows {
		e, err := eventDataToADKEvent(row.EventData)
		if err != nil {
			return nil, err
		}
		out = append(out, store.BotTranscriptEvent{Cursor: int64(row.ID), Event: e})
	}
	return out, nil
}

func (s *personalSessionStore) ReadBotTranscript(ctx context.Context, id uuid.UUID, after int64, limit int) ([]store.BotTranscriptEvent, error) {
	if id == uuid.Nil || after < 0 || limit < 1 || limit > 200 {
		return nil, store.ErrBotInvalid
	}
	rows, err := s.client.SessionEvent.Query().Where(personalevent.SessionIDEQ(id.String()), personalevent.IDGT(int(after))).Order(personalevent.ByID()).Limit(limit).All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]store.BotTranscriptEvent, 0, len(rows))
	for _, row := range rows {
		e, err := eventDataToADKEvent(row.EventData)
		if err != nil {
			return nil, err
		}
		out = append(out, store.BotTranscriptEvent{Cursor: int64(row.ID), Event: e})
	}
	return out, nil
}

var _ store.BotTranscriptStore = (*teamBotStore)(nil)
var _ store.BotTranscriptStore = (*personalBotStore)(nil)

func (s *teamBotStore) AppendBotRunEvent(ctx context.Context, lease store.BotLease, id uuid.UUID, event *adksession.Event) error {
	return s.repository().appendBotRunEvent(ctx, lease, id, event)
}
func (s *personalBotStore) AppendBotRunEvent(ctx context.Context, lease store.BotLease, id uuid.UUID, event *adksession.Event) error {
	return s.repository().appendBotRunEvent(ctx, lease, id, event)
}

type botWorkTranscriptKey struct{}
type botWorkOwnerInputKey struct{}

// workTranscriptContext is private routing metadata derived from the stored run,
// never from model content or a serialized runtime envelope.
func workTranscriptContext(ctx context.Context, work *store.BotWorkState) context.Context {
	return context.WithValue(ctx, botWorkTranscriptKey{}, work != nil)
}

func mirrorBotRunEvent(ctx context.Context, r store.BotRun, e *adksession.Event) bool {
	work, _ := ctx.Value(botWorkTranscriptKey{}).(bool)
	if work {
		// Owner admissions/answers and approval cards have their own canonical
		// repository writers. No producer text, tool result or artifact is final.
		ownerInput, _ := ctx.Value(botWorkOwnerInputKey{}).(bool)
		return ownerInput || (e.Content == nil && botHomeRunStateEvent(r, e))
	}
	return (r.Intent != store.BotIntentRegular || r.Presentation == store.BotRunPresentationHomeInteractive) && (e.Content != nil || botHomeArtifactEvent(r, e) || botHomeRunStateEvent(r, e))
}

type botTranscriptDB interface {
	fenceTranscript(context.Context, store.BotLease, time.Time) error
	appendRunTranscript(context.Context, store.Bot, store.BotRun, *adksession.Event) error
}

func (r *botRepository) appendBotRunEvent(ctx context.Context, lease store.BotLease, id uuid.UUID, event *adksession.Event) error {
	if id == uuid.Nil || event == nil || event.ID == "" {
		return store.ErrBotInvalid
	}
	if _, err := r.auth.authorize(ctx, uuid.Nil, false); err != nil {
		return err
	}
	return r.db.Transaction(ctx, func(tx botDraftDB) error {
		adapter, ok := tx.(botTranscriptDB)
		if !ok {
			return store.ErrBotUnsupported
		}
		// Acquire the same root-row write lock used by revocation/lease claims BEFORE
		// reading authority. Keep it until both event and session metadata commit.
		if err := adapter.fenceTranscript(ctx, lease, r.clock()); err != nil {
			return err
		}
		rec, err := r.checkLease(ctx, tx, lease, r.clock())
		if err != nil {
			return err
		}
		run, err := tx.ReadRun(ctx, lease.BotID, lease.RunID)
		if err != nil {
			return err
		}
		if run.Run.SessionID != id {
			return store.ErrBotForbidden
		}
		projectCtx := workTranscriptContext(ctx, run.Work)
		// Legacy explicit /conversation and /test-run have no canonical admission
		// row. Preserve only the exact persisted owner assignment, never role alone
		// or model-generated correction instructions, under one deterministic ID.
		if run.Work != nil && run.Run.UserEventID == "" && run.Run.Intent != store.BotIntentRegular && !event.Partial && event.Author == "user" && event.Content != nil && event.Content.Role == "user" && len(event.Content.Parts) == 1 && event.Content.Parts[0] != nil && event.Content.Parts[0].Text == run.Instructions && event.Actions.StateDelta["_astonish_turn_context"] != true {
			event = &adksession.Event{ID: "bot-work-input:" + run.Run.ID.String(), Author: "user", Timestamp: run.Run.CreatedAt, LLMResponse: model.LLMResponse{Content: genai.NewContentFromText(run.Instructions, "user")}}
			projectCtx = context.WithValue(projectCtx, botWorkOwnerInputKey{}, true)
		}
		return adapter.appendRunTranscript(projectCtx, rec.Bot, run.Run, event)
	})
}
func (d teamBotDB) fenceTranscript(ctx context.Context, l store.BotLease, now time.Time) error {
	// Write before reading: a SQLite read snapshot cannot be promoted after a
	// concurrent commit (SQLITE_BUSY_SNAPSHOT). checkLease validates intent/status
	// and worker authority while this lock is held, before writing the transcript.
	n, err := d.client.Bot.Update().Where(teambot.IDEQ(l.BotID), teambot.ActiveRunIDEQ(l.RunID), teambot.FenceEQ(l.Fence), teambot.LeaseExpiresAtGT(now)).SetFence(l.Fence).Save(ctx)
	if err != nil {
		return err
	}
	if n != 1 {
		return store.ErrBotConflict
	}
	saved, err := d.ReadRun(ctx, l.BotID, l.RunID)
	if err != nil {
		return err
	}
	if saved.Run.Intent.RequiredStatus() == "" {
		return store.ErrBotForbidden
	}
	return nil
}
func (d personalBotDB) fenceTranscript(ctx context.Context, l store.BotLease, now time.Time) error {
	// As above, acquire the write lock before establishing a read snapshot.
	n, err := d.client.Bot.Update().Where(personalbot.IDEQ(l.BotID), personalbot.ActiveRunIDEQ(l.RunID), personalbot.FenceEQ(l.Fence), personalbot.LeaseExpiresAtGT(now)).SetFence(l.Fence).Save(ctx)
	if err != nil {
		return err
	}
	if n != 1 {
		return store.ErrBotConflict
	}
	saved, err := d.ReadRun(ctx, l.BotID, l.RunID)
	if err != nil {
		return err
	}
	if saved.Run.Intent.RequiredStatus() == "" {
		return store.ErrBotForbidden
	}
	return nil
}
func (d teamBotDB) appendRunTranscript(ctx context.Context, b store.Bot, r store.BotRun, e *adksession.Event) error {
	exists, err := d.client.Session.Query().Where(teamsession.IDEQ(r.SessionID.String()), teamsession.BotIDEQ(b.ID), teamsession.BotRunIDEQ(r.ID), teamsession.UserIDEQ(b.OwnerID), teamsession.PurposeEQ(teamsession.PurposeBotRun)).Exist(ctx)
	if err != nil {
		return err
	}
	if !exists {
		return store.ErrBotForbidden
	}
	target := r.SessionID.String()
	targets := []string{target}

	if mirrorBotRunEvent(ctx, r, e) {
		exists, err := d.client.Session.Query().Where(teamsession.IDEQ(b.HomeSessionID.String()), teamsession.BotIDEQ(b.ID), teamsession.UserIDEQ(b.OwnerID), teamsession.PurposeEQ(teamsession.PurposeBotHome)).Exist(ctx)
		if err != nil {
			return err
		}
		if !exists {
			return store.ErrBotForbidden
		}
		target = b.HomeSessionID.String()
		if r.Presentation == store.BotRunPresentationHomeInteractive || botHomeArtifactEvent(r, e) {
			targets = append(targets, target)
		} else {
			targets = []string{target}
		}
	}

	for _, target := range targets {
		// The Bot root-row lock serializes retries; IDs are deterministic per turn.
		duplicate, err := d.client.SessionEvent.Query().Where(func(s *sql.Selector) {
			s.Where(sql.And(sql.EQ(s.C("session_id"), target), sqljson.ValueEQ("event_data", e.ID, sqljson.Path("ID"))))
		}).Exist(ctx)
		if err != nil {
			return err
		}
		if duplicate {
			continue
		}
		data, err := adkEventToMap(e)
		if err != nil {
			return err
		}
		if _, err = d.client.SessionEvent.Create().SetSessionID(target).SetEventData(data).Save(ctx); err != nil {
			return err
		}
		if err := d.client.Session.UpdateOneID(target).SetUpdatedAt(time.Now().UTC()).Exec(ctx); err != nil {
			return err
		}
	}
	return nil
}
func (d personalBotDB) appendRunTranscript(ctx context.Context, b store.Bot, r store.BotRun, e *adksession.Event) error {
	exists, err := d.client.Session.Query().Where(personalsession.IDEQ(r.SessionID.String()), personalsession.BotIDEQ(b.ID), personalsession.BotRunIDEQ(r.ID), personalsession.UserIDEQ(b.OwnerID), personalsession.PurposeEQ(personalsession.PurposeBotRun)).Exist(ctx)
	if err != nil {
		return err
	}
	if !exists {
		return store.ErrBotForbidden
	}
	target := r.SessionID.String()
	targets := []string{target}

	if mirrorBotRunEvent(ctx, r, e) {
		exists, err := d.client.Session.Query().Where(personalsession.IDEQ(b.HomeSessionID.String()), personalsession.BotIDEQ(b.ID), personalsession.UserIDEQ(b.OwnerID), personalsession.PurposeEQ(personalsession.PurposeBotHome)).Exist(ctx)
		if err != nil {
			return err
		}
		if !exists {
			return store.ErrBotForbidden
		}
		target = b.HomeSessionID.String()
		if r.Presentation == store.BotRunPresentationHomeInteractive || botHomeArtifactEvent(r, e) {
			targets = append(targets, target)
		} else {
			targets = []string{target}
		}
	}

	for _, target := range targets {
		// The Bot root-row lock serializes retries; IDs are deterministic per turn.
		duplicate, err := d.client.SessionEvent.Query().Where(func(s *sql.Selector) {
			s.Where(sql.And(sql.EQ(s.C("session_id"), target), sqljson.ValueEQ("event_data", e.ID, sqljson.Path("ID"))))
		}).Exist(ctx)
		if err != nil {
			return err
		}
		if duplicate {
			continue
		}
		data, err := adkEventToMap(e)
		if err != nil {
			return err
		}
		if _, err = d.client.SessionEvent.Create().SetSessionID(target).SetEventData(data).Save(ctx); err != nil {
			return err
		}
		if err := d.client.Session.UpdateOneID(target).SetUpdatedAt(time.Now().UTC()).Exec(ctx); err != nil {
			return err
		}
	}
	return nil
}
