package entstore

import (
	"context"
	"encoding/json"
	"fmt"

	pevent "github.com/SAP/astonish/ent/personal/sessionevent"
	tevent "github.com/SAP/astonish/ent/team/sessionevent"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/session"
)

const botPlanSnapshotKey = "astonish_plan_snapshot"

var _ store.BotPreparedPlanStore = (*personalBotStore)(nil)
var _ store.BotPreparedPlanStore = (*teamBotStore)(nil)

func (s *personalBotStore) PrepareBotPlan(c context.Context, l store.BotLease, p store.BotPreparedPlan) (*store.BotPreparedPlan, error) {
	return s.repository().PrepareBotPlan(c, l, p)
}
func (s *teamBotStore) PrepareBotPlan(c context.Context, l store.BotLease, p store.BotPreparedPlan) (*store.BotPreparedPlan, error) {
	return s.repository().PrepareBotPlan(c, l, p)
}
func (s *personalBotStore) LoadBotPlan(c context.Context, l store.BotLease, id uuid.UUID) (*store.BotPreparedPlan, error) {
	return s.repository().LoadBotPlan(c, l, id)
}
func (s *teamBotStore) LoadBotPlan(c context.Context, l store.BotLease, id uuid.UUID) (*store.BotPreparedPlan, error) {
	return s.repository().LoadBotPlan(c, l, id)
}
func (s *personalBotStore) ChangeBotPlan(c context.Context, l store.BotLease, q store.BotPreparedPlanChange) (*store.BotPreparedPlan, error) {
	return s.repository().ChangeBotPlan(c, l, q)
}
func (s *teamBotStore) ChangeBotPlan(c context.Context, l store.BotLease, q store.BotPreparedPlanChange) (*store.BotPreparedPlan, error) {
	return s.repository().ChangeBotPlan(c, l, q)
}

func (s *personalBotStore) LoadBotPlanByApproval(c context.Context, l store.BotLease, id uuid.UUID) (*store.BotPreparedPlan, error) {
	return s.repository().LoadBotPlanByApproval(c, l, id)
}
func (s *teamBotStore) LoadBotPlanByApproval(c context.Context, l store.BotLease, id uuid.UUID) (*store.BotPreparedPlan, error) {
	return s.repository().LoadBotPlanByApproval(c, l, id)
}
func (s *personalBotStore) AdvanceBotPlan(c context.Context, l store.BotLease, q store.BotPreparedPlanAdvance) (*store.BotPreparedPlan, error) {
	return s.repository().AdvanceBotPlan(c, l, q)
}
func (s *teamBotStore) AdvanceBotPlan(c context.Context, l store.BotLease, q store.BotPreparedPlanAdvance) (*store.BotPreparedPlan, error) {
	return s.repository().AdvanceBotPlan(c, l, q)
}

// The root write lock precedes all reads, exactly as in appendBotRunEvent.
func (r *botRepository) withPreparedPlan(ctx context.Context, l store.BotLease, fn func(botDB, botTranscriptDB, *botStoredRecord, *botStoredRun) error) error {
	if _, err := r.auth.authorize(ctx, uuid.Nil, false); err != nil {
		return err
	}
	return r.db.Transaction(ctx, func(tx botDB) error {
		transcript, ok := tx.(botTranscriptDB)
		if !ok {
			return store.ErrBotUnsupported
		}
		if err := transcript.fenceTranscript(ctx, l, r.clock()); err != nil {
			return err
		}
		rec, err := r.checkLease(ctx, tx, l, r.clock())
		if err != nil {
			return err
		}
		saved, err := tx.ReadRun(ctx, l.BotID, l.RunID)
		if err != nil {
			return err
		}
		return fn(tx, transcript, rec, saved)
	})
}

func preparedPlanEvents(ctx context.Context, tx botDB, run store.BotRun) ([]*session.Event, error) {
	var events []*session.Event
	// Restored sessions start with authenticated initial state before replaying
	// event deltas. Use that same base inside the transaction fence.
	initial := func(c *botContentCodec, botID *uuid.UUID, metadata map[string]any) error {
		if botID == nil || *botID != run.BotID {
			return store.ErrBotConflict
		}
		if raw, exists := metadata["bot_initial_state"]; exists {
			data, ok := raw.(map[string]any)
			if !ok {
				return store.ErrBotInvalid
			}
			e, err := c.decodeSessionEvent(ctx, run.BotID, run.SessionID.String(), data)
			if err != nil {
				return err
			}
			events = append(events, e)
		}
		return nil
	}
	switch d := tx.(type) {
	case personalBotDB:
		row, err := d.client.Session.Get(ctx, run.SessionID.String())
		if err != nil {
			return nil, err
		}
		if err := initial(d.contentCodec(), row.BotID, row.Metadata); err != nil {
			return nil, err
		}
		rows, err := d.client.SessionEvent.Query().Where(pevent.SessionIDEQ(run.SessionID.String())).Order(pevent.ByID()).All(ctx)
		if err != nil {
			return nil, err
		}
		s := personalSessionStore{client: d.client, content: d.contentCodec()}
		for _, row := range rows {
			e, err := s.decodeStoredEvent(ctx, run.SessionID.String(), row.EventData)
			if err != nil {
				return nil, err
			}
			events = append(events, e)
		}
	case teamBotDB:
		row, err := d.client.Session.Get(ctx, run.SessionID.String())
		if err != nil {
			return nil, err
		}
		if err := initial(d.contentCodec(), row.BotID, row.Metadata); err != nil {
			return nil, err
		}
		rows, err := d.client.SessionEvent.Query().Where(tevent.SessionIDEQ(run.SessionID.String())).Order(tevent.ByID()).All(ctx)
		if err != nil {
			return nil, err
		}
		s := teamSessionStore{client: d.client, content: d.contentCodec()}
		for _, row := range rows {
			e, err := s.decodeStoredEvent(ctx, run.SessionID.String(), row.EventData)
			if err != nil {
				return nil, err
			}
			events = append(events, e)
		}
	default:
		return nil, store.ErrBotUnsupported
	}
	return events, nil
}
func preparedPlanBase(events []*session.Event) (store.BotPlanBase, error) {
	var base store.BotPlanBase
	for _, e := range events {
		if value, ok := e.Actions.StateDelta[botPlanSnapshotKey]; ok {
			raw, err := json.Marshal(value)
			if err != nil {
				return base, err
			}
			base = store.BotPlanBase{}
			if err = json.Unmarshal(raw, &base); err != nil || base.PlanID == "" || base.Revision < 1 || base.Sequence < 1 {
				return base, store.ErrBotInvalid
			}
		}
	}
	return base, nil
}
func (r *botRepository) PrepareBotPlan(ctx context.Context, l store.BotLease, p store.BotPreparedPlan) (*store.BotPreparedPlan, error) {
	digest, err := p.Action.Digest()
	if err != nil || digest != p.ActionDigest || p.OperationID == uuid.Nil || p.BotID != l.BotID || p.RunID != l.RunID || p.SessionID == uuid.Nil || p.Version < 1 || !json.Valid(p.EngineRecord) || p.Revision != 0 || p.Stage != "" && p.Stage != store.BotPlanPrepared || p.ApprovalID != uuid.Nil || p.DispatchID != uuid.Nil || len(p.TrustedResult) > 0 || len(p.Snapshot) > 0 || p.PublicationDigest != "" || p.EventID != "" {
		return nil, store.ErrBotInvalid
	}
	if p.LeafKind != "" && p.LeafKind != store.BotPlanLeafVerifier && p.LeafKind != store.BotPlanLeafMirror || len(p.History) != 0 {
		return nil, store.ErrBotInvalid
	}
	p.Revision, p.Stage = 1, store.BotPlanPrepared
	err = r.withPreparedPlan(ctx, l, func(tx botDB, _ botTranscriptDB, _ *botStoredRecord, saved *botStoredRun) error {
		if p.SessionID != saved.Run.SessionID || p.Version != saved.Run.Version {
			return store.ErrBotConflict
		}
		if saved.Run.ControlOnly() || saved.Run.TitleConfirmation != nil && saved.Run.TitleConfirmation.State != store.BotTitleConfirmationConfirmed {
			return store.ErrBotForbidden
		}
		if old, ok := saved.PreparedPlans[p.OperationID.String()]; ok {
			a, _ := runContentDigest(old)
			b, _ := runContentDigest(p)
			if a != b {
				return store.ErrBotConflict
			}
			p = old
			return nil
		}
		events, err := preparedPlanEvents(ctx, tx, saved.Run)
		if err != nil {
			return err
		}
		base, err := preparedPlanBase(events)
		if err != nil {
			return err
		}
		if base != p.Base {
			return store.ErrBotConflict
		}
		// One unsettled operation per run prevents two independently prepared leaves
		// from both dispatching against the same base snapshot.
		for _, old := range saved.PreparedPlans {
			if old.Stage != store.BotPlanPublished && old.Stage != store.BotPlanObsolete {
				return store.ErrBotConflict
			}
		}
		if saved.PreparedPlans == nil {
			saved.PreparedPlans = map[string]store.BotPreparedPlan{}
		}
		saved.PreparedPlans[p.OperationID.String()] = p
		return persistTitleQuestion(ctx, tx, saved)
	})
	if err != nil {
		return nil, err
	}
	return &p, nil
}
func (r *botRepository) LoadBotPlan(ctx context.Context, l store.BotLease, id uuid.UUID) (*store.BotPreparedPlan, error) {
	var p store.BotPreparedPlan
	err := r.withPreparedPlan(ctx, l, func(_ botDB, _ botTranscriptDB, _ *botStoredRecord, s *botStoredRun) error {
		var ok bool
		p, ok = s.PreparedPlans[id.String()]
		if !ok {
			return store.ErrBotNotFound
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// Approval lookup deliberately does not use LoadResumeApproval: consumed
// approvals are evidence for recovery here, never renewed execution authority.
func (r *botRepository) LoadBotPlanByApproval(ctx context.Context, l store.BotLease, id uuid.UUID) (*store.BotPreparedPlan, error) {
	if id == uuid.Nil {
		return nil, store.ErrBotInvalid
	}
	var found *store.BotPreparedPlan
	err := r.withPreparedPlan(ctx, l, func(tx botDB, _ botTranscriptDB, _ *botStoredRecord, saved *botStoredRun) error {
		a, err := tx.ReadApproval(ctx, l.BotID, id)
		if err != nil {
			return err
		}
		digest, err := a.Action.Digest()
		if err != nil || a.BotID != l.BotID || a.RunID != l.RunID || a.Version != saved.Run.Version || digest != a.ActionDigest {
			return store.ErrBotConflict
		}
		for _, p := range saved.PreparedPlans {
			boundDigest := ""
			if p.ApprovalID == id {
				boundDigest = p.ActionDigest
			}
			for _, h := range p.History {
				if h.ApprovalID == id {
					if boundDigest != "" {
						return store.ErrBotConflict
					}
					boundDigest = h.ActionDigest
				}
			}
			if boundDigest == "" {
				continue
			}
			if found != nil || boundDigest != digest || p.BotID != l.BotID || p.RunID != l.RunID || p.Version != saved.Run.Version || p.SessionID != saved.Run.SessionID {
				return store.ErrBotConflict
			}
			copy := p
			found = &copy
		}
		if found == nil {
			return store.ErrBotNotFound
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return found, nil
}

func (r *botRepository) AdvanceBotPlan(ctx context.Context, l store.BotLease, q store.BotPreparedPlanAdvance) (*store.BotPreparedPlan, error) {
	digest, err := q.Action.Digest()
	if err != nil || digest != q.ActionDigest {
		return nil, store.ErrBotInvalid
	}
	var p store.BotPreparedPlan
	err = r.withPreparedPlan(ctx, l, func(tx botDB, _ botTranscriptDB, _ *botStoredRecord, saved *botStoredRun) error {
		var ok bool
		p, ok = saved.PreparedPlans[q.OperationID.String()]
		if !ok {
			return store.ErrBotNotFound
		}
		if p.Revision != q.ExpectedRevision || p.Stage != store.BotPlanSettled || p.LeafKind != store.BotPlanLeafVerifier || len(p.History) != 0 || digest == p.ActionDigest {
			return store.ErrBotConflict
		}
		events, err := preparedPlanEvents(ctx, tx, saved.Run)
		if err != nil {
			return err
		}
		base, err := preparedPlanBase(events)
		if err != nil {
			return err
		}
		if base != p.Base {
			return store.ErrBotConflict
		}
		p.History = []store.BotPreparedPlanLeafEvidence{{LeafKind: p.LeafKind, Action: p.Action, ActionDigest: p.ActionDigest, ApprovalID: p.ApprovalID, DispatchID: p.DispatchID, EngineRecord: p.EngineRecord, TrustedResult: p.TrustedResult}}
		p.LeafKind, p.Stage = store.BotPlanLeafMirror, store.BotPlanPrepared
		p.Action, p.ActionDigest = q.Action, digest
		p.ApprovalID, p.DispatchID = uuid.Nil, uuid.Nil
		p.TrustedResult, p.Snapshot = nil, nil
		p.PublicationDigest, p.EventID = "", ""
		p.Revision++
		saved.PreparedPlans[p.OperationID.String()] = p
		return persistTitleQuestion(ctx, tx, saved)
	})
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// Never bind another leaf's approval or dispatch identity, even after that leaf
// is obsolete, settled or published. IDs survive advancement in History.
func preparedPlanIDUsed(plans map[string]store.BotPreparedPlan, approval, dispatch uuid.UUID) bool {
	for _, p := range plans {
		if approval != uuid.Nil && p.ApprovalID == approval || dispatch != uuid.Nil && p.DispatchID == dispatch {
			return true
		}
		for _, h := range p.History {
			if approval != uuid.Nil && h.ApprovalID == approval || dispatch != uuid.Nil && h.DispatchID == dispatch {
				return true
			}
		}
	}
	return false
}

func (r *botRepository) ChangeBotPlan(ctx context.Context, l store.BotLease, q store.BotPreparedPlanChange) (*store.BotPreparedPlan, error) {
	var p store.BotPreparedPlan
	err := r.withPreparedPlan(ctx, l, func(tx botDB, transcript botTranscriptDB, rec *botStoredRecord, saved *botStoredRun) error {
		var ok bool
		p, ok = saved.PreparedPlans[q.OperationID.String()]
		if !ok {
			return store.ErrBotNotFound
		}
		// Publication retries must not supply replacement evidence.
		if q.Stage != store.BotPlanSettled && (len(q.Snapshot) > 0 || len(q.TrustedResult) > 0 || len(q.EngineRecord) > 0) {
			return store.ErrBotInvalid
		}
		if p.Stage == store.BotPlanPublished && q.Stage == store.BotPlanPublished && (q.ExpectedRevision == p.Revision || q.ExpectedRevision == p.Revision-1) {
			return nil
		}
		if q.ExpectedRevision != p.Revision {
			return store.ErrBotConflict
		}
		events, err := preparedPlanEvents(ctx, tx, saved.Run)
		if err != nil {
			return err
		}
		base, err := preparedPlanBase(events)
		if err != nil {
			return err
		}
		// A superseded operation that has never dispatched must remain
		// discardable. The stage guard below still forbids discarding effects.
		if base != p.Base && q.Stage != store.BotPlanObsolete {
			return store.ErrBotConflict
		}
		now := r.clock()
		switch q.Stage {
		case store.BotPlanAwaitingApproval:
			if p.Stage != store.BotPlanPrepared {
				return store.ErrBotConflict
			}
			if !q.ApprovalExpiresAt.After(now) {
				return store.ErrBotExpired
			}
			a, err := tx.FindApproval(ctx, l.BotID, l.RunID, p.ActionDigest)
			if err == store.ErrBotNotFound {
				a = &store.BotApproval{ID: uuid.New(), BotID: p.BotID, RunID: p.RunID, Version: p.Version, Action: p.Action, ActionDigest: p.ActionDigest, Status: store.ApprovalPending, CreatedAt: now, ExpiresAt: q.ApprovalExpiresAt.UTC()}
				if err = tx.InsertApproval(ctx, *a); err != nil {
					return err
				}
				if err = tx.AdvanceLeaseSequence(ctx, l, rec.LastSequence, now); err != nil {
					return err
				}
				if err = botApprovalActivity(ctx, tx, *a, rec.LastSequence+1, now); err != nil {
					return err
				}
			} else if err != nil {
				return err
			}
			if a.Version != p.Version || a.Status != store.ApprovalPending && a.Status != store.ApprovalApproved || !now.Before(a.ExpiresAt) {
				return store.ErrBotConflict
			}
			if preparedPlanIDUsed(saved.PreparedPlans, a.ID, uuid.Nil) {
				return store.ErrBotConflict
			}
			actualDigest, digestErr := a.Action.Digest()
			if digestErr != nil || actualDigest != p.ActionDigest || a.ActionDigest != p.ActionDigest || a.BotID != p.BotID || a.RunID != p.RunID {
				return store.ErrBotConflict
			}
			p.ApprovalID = a.ID
		case store.BotPlanDispatchStarted:
			if q.DispatchID == uuid.Nil || preparedPlanIDUsed(saved.PreparedPlans, uuid.Nil, q.DispatchID) || p.Stage != store.BotPlanPrepared && p.Stage != store.BotPlanAwaitingApproval {
				return store.ErrBotConflict
			}
			if p.Stage == store.BotPlanAwaitingApproval {
				if q.ApprovalID != p.ApprovalID {
					return store.ErrBotConflict
				}
				a, err := tx.ReadApproval(ctx, l.BotID, p.ApprovalID)
				if err != nil {
					return err
				}
				digest, err := a.Action.Digest()
				if err != nil {
					return store.ErrBotInvalid
				}
				if a.RunID != p.RunID || a.Version != p.Version || a.ActionDigest != p.ActionDigest || digest != p.ActionDigest || a.Status != store.ApprovalApproved {
					return store.ErrBotConflict
				}
				if !now.Before(a.ExpiresAt) {
					return store.ErrBotExpired
				}
				a.Status = store.ApprovalConsumed
				if err = tx.UpdateApproval(ctx, *a, store.ApprovalApproved, now); err != nil {
					return err
				}
				if err = tx.AdvanceLeaseSequence(ctx, l, rec.LastSequence, now); err != nil {
					return err
				}
				if err = botApprovalActivity(ctx, tx, *a, rec.LastSequence+1, now); err != nil {
					return err
				}
			} else if q.ApprovalID != uuid.Nil {
				return store.ErrBotConflict
			}
			p.DispatchID = q.DispatchID
		case store.BotPlanSettled:
			if p.Stage != store.BotPlanDispatchStarted || p.DispatchID != q.DispatchID || !json.Valid(q.TrustedResult) {
				return store.ErrBotConflict
			}
			if p.LeafKind == store.BotPlanLeafVerifier {
				if !json.Valid(q.EngineRecord) || len(q.Snapshot) != 0 {
					return store.ErrBotInvalid
				}
				p.EngineRecord = append(json.RawMessage(nil), q.EngineRecord...)
				p.TrustedResult = append(json.RawMessage(nil), q.TrustedResult...)
				break
			}
			if !json.Valid(q.Snapshot) || len(q.EngineRecord) > 0 && !json.Valid(q.EngineRecord) {
				return store.ErrBotInvalid
			}
			if len(q.EngineRecord) > 0 {
				p.EngineRecord = append(json.RawMessage(nil), q.EngineRecord...)
			}
			var next store.BotPlanBase
			if json.Unmarshal(q.Snapshot, &next) != nil || next.PlanID == "" || next.Revision < 1 || next.Sequence <= p.Base.Sequence || p.Base.PlanID != "" && (next.PlanID != p.Base.PlanID || next.Revision < p.Base.Revision) {
				return store.ErrBotInvalid
			}
			p.TrustedResult = append(json.RawMessage(nil), q.TrustedResult...)
			p.Snapshot = append(json.RawMessage(nil), q.Snapshot...)
			p.PublicationDigest, err = runContentDigest(p.Snapshot)
			if err != nil {
				return err
			}
			p.EventID = fmt.Sprintf("bot-plan-%s-%s", p.RunID, p.OperationID)
		case store.BotPlanPublished:
			if p.Stage != store.BotPlanSettled || p.LeafKind == store.BotPlanLeafVerifier || len(p.Snapshot) == 0 {
				return store.ErrBotConflict
			}
			digest, err := runContentDigest(p.Snapshot)
			if err != nil || digest != p.PublicationDigest {
				return store.ErrBotConflict
			}
			for _, e := range events {
				if e.ID == p.EventID {
					return store.ErrBotConflict
				}
			}
			e := &session.Event{ID: p.EventID, Author: "system", Timestamp: now, Actions: session.EventActions{StateDelta: map[string]any{botPlanSnapshotKey: json.RawMessage(p.Snapshot)}}}
			if err = transcript.appendRunTranscript(workTranscriptContext(ctx, saved.Work), rec.Bot, saved.Run, e); err != nil {
				return err
			}
		case store.BotPlanUncertain:
			if p.Stage != store.BotPlanDispatchStarted || p.DispatchID != q.DispatchID {
				return store.ErrBotConflict
			}
		case store.BotPlanObsolete:
			if p.Stage != store.BotPlanPrepared && p.Stage != store.BotPlanAwaitingApproval {
				return store.ErrBotConflict
			}
		default:
			return store.ErrBotInvalid
		}
		p.Stage = q.Stage
		p.Revision++
		saved.PreparedPlans[p.OperationID.String()] = p
		return persistTitleQuestion(ctx, tx, saved)
	})
	if err != nil {
		return nil, err
	}
	return &p, nil
}
