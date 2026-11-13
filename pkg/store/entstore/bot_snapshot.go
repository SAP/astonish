package entstore

import (
	"context"
	"encoding/json"
	"entgo.io/ent/dialect/sql"
	"sort"
	"strconv"

	personalevent "github.com/SAP/astonish/ent/personal/sessionevent"
	teamevent "github.com/SAP/astonish/ent/team/sessionevent"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

var _ store.BotSnapshotReader = (*personalBotStore)(nil)
var _ store.BotSnapshotReader = (*teamBotStore)(nil)

func (s *personalBotStore) Snapshot(ctx context.Context, id store.BotID) (*store.BotSnapshot, error) {
	return s.repository().Snapshot(ctx, id)
}
func (s *teamBotStore) Snapshot(ctx context.Context, id store.BotID) (*store.BotSnapshot, error) {
	return s.repository().Snapshot(ctx, id)
}
func (s *personalBotStore) ReadCursor(ctx context.Context, id store.BotID) (*store.BotReadCursor, error) {
	return s.repository().ReadCursor(ctx, id)
}
func (s *teamBotStore) ReadCursor(ctx context.Context, id store.BotID) (*store.BotReadCursor, error) {
	return s.repository().ReadCursor(ctx, id)
}

func (r *botRepository) viewerRecord(ctx context.Context, db botDraftDB, id store.BotID) (*botStoredRecord, *store.BotReadCursor, error) {
	actor, err := r.auth.authorize(ctx, uuid.Nil, false)
	if err != nil {
		return nil, nil, err
	}
	rec, err := db.GetBot(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	if _, err = r.auth.authorize(ctx, rec.Bot.OwnerID, false); err != nil {
		return nil, nil, err
	}
	cursor := &store.BotReadCursor{BotID: id, ViewerID: actor}
	if existing, ok := rec.Metadata["read_cursors"]; ok {
		raw, err := json.Marshal(existing)
		if err != nil {
			return nil, nil, err
		}
		var cursors map[string]string
		if err := json.Unmarshal(raw, &cursors); err != nil {
			return nil, nil, store.ErrBotInvalid
		}
		if value, ok := cursors[actor.String()]; ok {
			cursor.Sequence, err = strconv.ParseInt(value, 10, 64)
			if err != nil || cursor.Sequence < 0 || cursor.Sequence > rec.LastSequence {
				return nil, nil, store.ErrBotInvalid
			}
		}
	}
	return rec, cursor, nil
}

func (r *botRepository) ReadCursor(ctx context.Context, id store.BotID) (*store.BotReadCursor, error) {
	_, cursor, err := r.viewerRecord(ctx, r.db, id)
	return cursor, err
}

// Snapshot projects durable control-plane records, never session transcripts.
// All pages are traversed: UUID order is not chronology, and the first page is
// not sufficient to count decisions or locate the latest finished run.
func (r *botRepository) Snapshot(ctx context.Context, id store.BotID) (*store.BotSnapshot, error) {
	var result *store.BotSnapshot
	err := r.db.Transaction(ctx, func(db botDraftDB) error {
		rec, cursor, err := r.viewerRecord(ctx, db, id)
		if err != nil {
			return err
		}
		result = &store.BotSnapshot{Bot: rec.Bot, ReadCursor: *cursor, LatestSequence: rec.LastSequence, UnreadCount: rec.LastSequence - cursor.Sequence, PendingApprovals: []store.BotApproval{}}
		result.RunStates = []store.BotRunState{}
		result.Operations, err = snapshotBotOperations(ctx, db, rec)
		if err != nil {
			return err
		}
		if _, present := rec.Metadata[mandateHeadKey]; present {
			result.Mandate = &store.BotMandateSummary{Available: true}
		}
		switch scoped := db.(type) {
		case personalBotDB:
			rows, e := scoped.client.SessionEvent.Query().Where(personalevent.SessionIDEQ(rec.Bot.HomeSessionID.String())).Order(personalevent.ByID(sql.OrderDesc())).Limit(1).All(ctx)
			if e != nil {
				return e
			}
			if len(rows) > 0 {
				result.HistoryCursor = int64(rows[len(rows)-1].ID)
			}
		case teamBotDB:
			rows, e := scoped.client.SessionEvent.Query().Where(teamevent.SessionIDEQ(rec.Bot.HomeSessionID.String())).Order(teamevent.ByID(sql.OrderDesc())).Limit(1).All(ctx)
			if e != nil {
				return e
			}
			if len(rows) > 0 {
				result.HistoryCursor = int64(rows[len(rows)-1].ID)
			}
		default:
			return store.ErrBotUnsupported
		}
		proposals, err := listControlProposals(ctx, db, id)
		if err != nil {
			return err
		}
		result.PendingProposals = []store.BotControlProposal{}
		for _, p := range proposals {
			if p.Status == store.BotControlProposed && r.clock().Before(p.ExpiresAt) {
				result.PendingProposals = append(result.PendingProposals, p)
			}
		}
		for after := uuid.Nil; ; {
			page, err := db.ReadApprovals(ctx, id, 200, after)
			if err != nil {
				return err
			}
			for _, approval := range page {
				if approval.Status == store.ApprovalPending || approval.Status == store.ApprovalApproved {
					result.PendingApprovals = append(result.PendingApprovals, approval)
				}
			}
			if len(page) < 200 {
				break
			}
			after = page[len(page)-1].ID
		}
		sort.Slice(result.PendingApprovals, func(i, j int) bool {
			a, b := result.PendingApprovals[i], result.PendingApprovals[j]
			if a.ExpiresAt.Equal(b.ExpiresAt) {
				return a.ID.String() < b.ID.String()
			}
			return a.ExpiresAt.Before(b.ExpiresAt)
		})
		for after := uuid.Nil; ; {
			page, err := db.ReadRuns(ctx, id, 200, after)
			if err != nil {
				return err
			}
			for _, run := range page {
				// This barrier is reserved already; reads never allocate a new sequence
				// or manufacture durable events for pre-envelope databases.
				result.RunStates = append(result.RunStates, store.BotRunState{Work: run.Work, SchemaVersion: store.BotRunStateSchemaVersion, BotID: id, RunID: run.ID, SessionID: run.SessionID, HomeSessionID: rec.Bot.HomeSessionID, Presentation: run.Presentation, Status: run.Status, StateSequence: rec.LastSequence, Reason: run.ContinuationReason, UserEventID: run.UserEventID})
				previous := result.LatestOutcome
				if run.FinishedAt != nil && (previous == nil || run.FinishedAt.After(*previous.FinishedAt) || run.FinishedAt.Equal(*previous.FinishedAt) && run.ID.String() > previous.ID.String()) {
					copy := run
					result.LatestOutcome = &copy
				}
			}
			if len(page) < 200 {
				break
			}
			after = page[len(page)-1].ID
		}
		// PostgreSQL's default READ COMMITTED may advance between statements.
		// All control-plane mutations fence/advance this root, so reject a torn
		// projection instead of returning mixed counts and approval/outcome rows.
		current, _, err := r.viewerRecord(ctx, db, id)
		if err != nil {
			return err
		}
		if current.Bot.Revision != rec.Bot.Revision || current.LastSequence != rec.LastSequence {
			return store.ErrBotConflict
		}
		return nil
	})
	return result, err
}
