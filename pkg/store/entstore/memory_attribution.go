package entstore

import (
	"context"
	"database/sql"
	"reflect"

	personalmemory "github.com/SAP/astonish/ent/personal/memory"
	teammemory "github.com/SAP/astonish/ent/team/memory"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// Ordinary search hydration preserves optional provenance without changing its
// candidate/ranking semantics. Attributed search has separate prefiltering.
func (m *teamMemoryStore) hydrateMemoryAttribution(ctx context.Context, results []store.MemorySearchResult) error {
	if len(results) == 0 || m.client == nil {
		return nil
	}
	ids := make([]uuid.UUID, 0, len(results))
	for _, r := range results {
		id, err := uuid.Parse(r.ID)
		if err != nil {
			return err
		}
		ids = append(ids, id)
	}
	rows, err := m.client.Memory.Query().Where(teammemory.IDIn(ids...)).All(ctx)
	if err != nil {
		return err
	}
	attrs := make(map[string]*store.MemoryAttribution, len(rows))
	for _, r := range rows {
		attrs[r.ID.String()] = memoryAttributionResult(r.Metadata)
	}
	for i := range results {
		results[i].Attribution = attrs[results[i].ID]
	}
	return nil
}
func (m *personalMemoryStore) hydrateMemoryAttribution(ctx context.Context, results []store.MemorySearchResult) error {
	if len(results) == 0 || m.client == nil {
		return nil
	}
	ids := make([]uuid.UUID, 0, len(results))
	for _, r := range results {
		id, err := uuid.Parse(r.ID)
		if err != nil {
			return err
		}
		ids = append(ids, id)
	}
	rows, err := m.client.Memory.Query().Where(personalmemory.IDIn(ids...)).All(ctx)
	if err != nil {
		return err
	}
	attrs := make(map[string]*store.MemoryAttribution, len(rows))
	for _, r := range rows {
		attrs[r.ID.String()] = memoryAttributionResult(r.Metadata)
	}
	for i := range results {
		results[i].Attribution = attrs[results[i].ID]
	}
	return nil
}

// Corrupt structured provenance must not turn into a legacy unattributed row.
// The invalid sentinel is rejected by scoped consumers and attributed search.
func memoryAttributionResult(metadata map[string]any) *store.MemoryAttribution {
	a, err := store.DecodeMemoryAttribution(metadata)
	if err != nil {
		return &store.MemoryAttribution{}
	}
	return a
}

func validateMemoryAttribution(ctx context.Context, db botDraftDB, session string, a *store.MemoryAttribution) error {
	if a == nil {
		return nil
	}
	if err := a.Validate(); err != nil {
		return err
	}
	sid, err := uuid.Parse(session)
	if err != nil || sid == uuid.Nil {
		return store.ErrBotForbidden
	}
	linked, err := db.CorrectionSessionBot(ctx, sid)
	if err != nil || linked != a.BotID {
		return store.ErrBotForbidden
	}
	if a.GoalID != uuid.Nil {
		goal, err := db.ReadGoal(ctx, a.BotID, a.GoalID)
		if err != nil || goal == nil || goal.BotID != a.BotID {
			return store.ErrBotForbidden
		}
	}
	if a.RunID != uuid.Nil {
		run, err := db.ReadRun(ctx, a.BotID, a.RunID)
		if err != nil || run == nil || run.Run.BotID != a.BotID || run.Run.SessionID != sid {
			return store.ErrBotForbidden
		}
	}
	return nil
}

func prepareMemoryAttribution(ctx context.Context, db botDraftDB, entry store.MemoryEntry) (map[string]any, error) {
	if err := validateMemoryAttribution(ctx, db, entry.SessionID, entry.Attribution); err != nil {
		return nil, err
	}
	return store.EncodeMemoryAttribution(entry.Metadata, entry.Attribution)
}

func guardMemoryAttribution(ctx context.Context, db botDraftDB, botID store.BotID, q store.BotMemoryMutation) error {
	if q.Kind == store.BotMemoryAdd {
		if q.Entry.Attribution != nil && q.Entry.Attribution.BotID != botID {
			return store.ErrBotForbidden
		}
		return validateMemoryAttribution(ctx, db, q.Entry.SessionID, q.Entry.Attribution)
	}
	row, err := db.ReadCorrectionMemory(ctx, q.Previous.ID)
	if err != nil {
		return err
	}
	if row == nil {
		return store.ErrBotNotFound
	}
	if !reflect.DeepEqual(row.Attribution, q.Previous.Attribution) {
		return store.ErrBotConflict
	}
	if row.Attribution != nil && row.Attribution.BotID != botID {
		return store.ErrBotForbidden
	}
	return validateMemoryAttribution(ctx, db, row.SessionID, row.Attribution)
}

func (d personalBotDB) SupersedeMemoryAttribution(ctx context.Context, id uuid.UUID, source string) error {
	row, err := d.client.Memory.Get(ctx, id)
	if err != nil {
		return err
	}
	metadata, err := store.SupersedeMemoryAttribution(row.Metadata, source)
	if err != nil {
		return err
	}
	return d.client.Memory.UpdateOneID(id).SetMetadata(metadata).Exec(ctx)
}
func (d teamBotDB) SupersedeMemoryAttribution(ctx context.Context, id uuid.UUID, source string) error {
	row, err := d.client.Memory.Get(ctx, id)
	if err != nil {
		return err
	}
	metadata, err := store.SupersedeMemoryAttribution(row.Metadata, source)
	if err != nil {
		return err
	}
	return d.client.Memory.UpdateOneID(id).SetMetadata(metadata).Exec(ctx)
}

var _ store.AttributedMemoryStore = (*personalMemoryStore)(nil)
var _ store.AttributedMemoryStore = (*teamMemoryStore)(nil)

func (m *personalMemoryStore) SearchAttributed(ctx context.Context, sessions []string, filter store.MemoryAttributionFilter, query string, limit int, minScore float64, category string) ([]store.MemorySearchResult, error) {
	ids, err := botMemorySessionIDs(sessions)
	if err != nil {
		return nil, err
	}
	if filter.BotID == uuid.Nil {
		return nil, store.ErrBotInvalid
	}
	if len(ids) == 0 {
		return nil, ctx.Err()
	}
	q := m.client.Memory.Query().Where(personalmemory.SessionIDIn(ids...))
	if category != "" {
		q.Where(personalmemory.CategoryEQ(category))
	}
	rows, err := q.All(ctx)
	if err != nil {
		return nil, err
	}
	candidates := make([]store.MemorySearchResult, 0, len(rows))
	for _, row := range rows {
		candidates = append(candidates, personalMemoryToSearchResult(row))
	}
	return searchAttributedCandidates(ctx, personalBotDB{client: m.client}, m.db, m.dialect, m.table, m.ftsTable, m.embedFunc, ids, candidates, filter, query, limit, minScore, category)
}

func (m *teamMemoryStore) SearchAttributed(ctx context.Context, sessions []string, filter store.MemoryAttributionFilter, query string, limit int, minScore float64, category string) ([]store.MemorySearchResult, error) {
	ids, err := botMemorySessionIDs(sessions)
	if err != nil {
		return nil, err
	}
	if filter.BotID == uuid.Nil {
		return nil, store.ErrBotInvalid
	}
	if len(ids) == 0 {
		return nil, ctx.Err()
	}
	q := m.client.Memory.Query().Where(teammemory.SessionIDIn(ids...))
	if category != "" {
		q.Where(teammemory.CategoryEQ(category))
	}
	rows, err := q.All(ctx)
	if err != nil {
		return nil, err
	}
	candidates := make([]store.MemorySearchResult, 0, len(rows))
	for _, row := range rows {
		candidates = append(candidates, entMemoryToResult(row))
	}
	return searchAttributedCandidates(ctx, teamBotDB{client: m.client}, m.db, m.dialect, m.table, m.ftsTable, m.embedFunc, ids, candidates, filter, query, limit, minScore, category)
}

// Complete scoped candidate enumeration is intentional: metadata validation and
// linkage checks precede BOTH semantic/keyword ranking and their limits. The
// existing persisted index supplies scores; no index or memory permission exists
// here. Enumeration can later move to equivalent JSON predicates, never a
// bounded/global search followed by filtering.
func searchAttributedCandidates(ctx context.Context, repo botDraftDB, db *sql.DB, dialect Dialect, table, fts string, embed store.EmbedFunc, sessions []uuid.UUID, candidates []store.MemorySearchResult, filter store.MemoryAttributionFilter, query string, limit int, minScore float64, category string) ([]store.MemorySearchResult, error) {
	if filter.GoalID != uuid.Nil {
		if goal, err := repo.ReadGoal(ctx, filter.BotID, filter.GoalID); err != nil || goal == nil || goal.BotID != filter.BotID {
			return nil, store.ErrBotForbidden
		}
	}
	if filter.RunID != uuid.Nil {
		if run, err := repo.ReadRun(ctx, filter.BotID, filter.RunID); err != nil || run == nil || run.Run.BotID != filter.BotID {
			return nil, store.ErrBotForbidden
		}
	}
	var keys []uuid.UUID
	allowed := make(map[string]store.MemorySearchResult)
	for _, row := range candidates {
		a := row.Attribution
		if a == nil || a.BotID != filter.BotID || (!filter.IncludeSuperseded && a.SupersededBy != "") || (filter.GoalID != uuid.Nil && a.GoalID != filter.GoalID) || (filter.RunID != uuid.Nil && a.RunID != filter.RunID) {
			continue
		}
		if err := validateMemoryAttribution(ctx, repo, row.SessionID, a); err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			continue // malformed/foreign provenance is not an authorized candidate
		}
		keys = append(keys, uuid.MustParse(row.ID))
		allowed[row.ID] = row
	}
	if len(keys) == 0 {
		return nil, ctx.Err()
	}
	scores, err := searchBotMemory(ctx, db, dialect, table, fts, embed, sessions, query, limit, minScore, category, keys)
	if err != nil {
		return nil, err
	}
	results := make([]store.MemorySearchResult, 0, len(scores))
	for _, score := range scores {
		row := allowed[score.ID]
		current, err := repo.ReadCorrectionMemory(ctx, row.ID)
		if err != nil {
			return nil, err
		}
		if current == nil || current.SessionID != row.SessionID || current.Snippet != row.Snippet || !reflect.DeepEqual(current.Attribution, row.Attribution) {
			return nil, store.ErrBotConflict
		}
		if err := validateMemoryAttribution(ctx, repo, current.SessionID, current.Attribution); err != nil {
			return nil, err
		}
		row.Score = score.Score
		results = append(results, row)
	}
	return results, nil
}
