package entstore

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"

	entsql "entgo.io/ent/dialect/sql"
	personalent "github.com/SAP/astonish/ent/personal"
	personalmemory "github.com/SAP/astonish/ent/personal/memory"
	teament "github.com/SAP/astonish/ent/team"
	teammemory "github.com/SAP/astonish/ent/team/memory"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

var _ store.BotMemoryMutationStore = (*teamBotStore)(nil)
var _ store.BotMemoryMutationStore = (*personalBotStore)(nil)

// guardBotMemory checks fresh membership and immutable session linkage in the
// transaction. A no-op Bot/session CAS below locks those records through commit.
func guardBotMemory(ctx context.Context, r *botRepository, tx botDraftDB, id store.BotID, q store.BotMemoryMutation) (uuid.UUID, uuid.UUID, error) {
	if lease, ok := ctx.Value(botMemoryLeaseKey{}).(store.BotLease); ok {
		return guardBotRunMemory(ctx, r, tx, id, q, lease)
	}
	if _, runtime := execution.BotRunIdentityFromContext(ctx); runtime {
		return uuid.Nil, uuid.Nil, store.ErrBotForbidden
	}
	rec, err := tx.GetBot(ctx, id)
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	actor, err := r.auth.authorize(ctx, rec.Bot.OwnerID, true)
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	sid := q.Entry.SessionID
	if q.Kind != store.BotMemoryAdd {
		sid = q.Previous.SessionID
	}
	sessionID, err := uuid.Parse(sid)
	if err != nil || sessionID == uuid.Nil {
		return uuid.Nil, uuid.Nil, store.ErrBotForbidden
	}
	linked, err := tx.CorrectionSessionBot(ctx, sessionID)
	if err != nil || linked != id {
		return uuid.Nil, uuid.Nil, store.ErrBotForbidden
	}
	if q.Kind == store.BotMemoryAdd && q.Entry.CreatedBy != "" && q.Entry.CreatedBy != actor.String() {
		return uuid.Nil, uuid.Nil, store.ErrBotForbidden
	}
	if err := guardMemoryAttribution(ctx, tx, id, q); err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	// The persisted attribution was just compared with Previous, so omitting it
	// cannot bypass this guard. Evidence belongs to the original text: ordinary
	// updates may recategorize it, but only reviewed replacement may change it.
	// The content CAS below also prevents a forged Previous.Snippet bypass.
	if q.Kind == store.BotMemoryUpdate && q.Previous.Attribution != nil && q.Entry.Content != q.Previous.Snippet {
		return uuid.Nil, uuid.Nil, fmt.Errorf("%w: attributed memory content requires a reviewed replacement transaction", store.ErrBotConflict)
	}
	return actor, sessionID, nil
}

// PostgreSQL embedding is the existing documented vector SQL exception. It must
// use the SAME transaction as Ent content writes, never the parent pool.
type botMemorySQLExec func(context.Context, string, ...any) (sql.Result, error)

func (s *teamBotStore) MutateBotMemory(ctx context.Context, memories store.MemoryStore, id store.BotID, q store.BotMemoryMutation) error {
	m, ok := memories.(*botDiskMemoryStore)
	if !ok || m == nil || m.repo == nil || m.owner != s.client || m.id != id || memoryBackendIdentity(m.repo.db) != s.content {
		return store.ErrBotUnsupported
	}
	return mutateBotDiskMemory(ctx, s.repository(), id, q)
}

func (s *personalBotStore) MutateBotMemory(ctx context.Context, memories store.MemoryStore, id store.BotID, q store.BotMemoryMutation) error {
	m, ok := memories.(*botDiskMemoryStore)
	if !ok || m == nil || m.repo == nil || m.owner != s.client || m.id != id || memoryBackendIdentity(m.repo.db) != s.content {
		return store.ErrBotUnsupported
	}
	return mutateBotDiskMemory(ctx, s.repository(), id, q)
}

var _ store.BotMemoryPrefilterStore = (*teamMemoryStore)(nil)
var _ store.BotMemoryPrefilterStore = (*personalMemoryStore)(nil)
var _ store.BotMemoryWriteAuthorizer = (*teamBotStore)(nil)
var _ store.BotMemoryWriteAuthorizer = (*personalBotStore)(nil)

func (s *teamBotStore) AuthorizeBotMemoryWrite(ctx context.Context, id store.BotID) (uuid.UUID, error) {
	return s.repository().AuthorizeBotMemoryWrite(ctx, id)
}
func (s *personalBotStore) AuthorizeBotMemoryWrite(ctx context.Context, id store.BotID) (uuid.UUID, error) {
	return s.repository().AuthorizeBotMemoryWrite(ctx, id)
}

// AuthorizeBotMemoryWrite grants interactive edits only. A runtime identity is
// not an interactive owner: this interface has no lease fence to validate, so
// workers must use their transactional, fenced publication/correction paths.
func (r *botRepository) AuthorizeBotMemoryWrite(ctx context.Context, id store.BotID) (uuid.UUID, error) {
	if lease, ok := ctx.Value(botMemoryLeaseKey{}).(store.BotLease); ok {
		if lease.BotID != id {
			return uuid.Nil, store.ErrBotForbidden
		}
		rec, err := r.checkLease(ctx, r.db, lease, r.clock())
		if err != nil {
			return uuid.Nil, err
		}
		return rec.Bot.OwnerID, nil
	}
	if err := ctx.Err(); err != nil {
		return uuid.Nil, err
	}
	if _, runtime := execution.BotRunIdentityFromContext(ctx); runtime {
		return uuid.Nil, store.ErrBotForbidden
	}
	b, err := r.Get(ctx, id)
	if err != nil {
		return uuid.Nil, err
	}
	return r.auth.authorize(ctx, b.OwnerID, true)
}

func botMemorySessionIDs(ids []string) ([]uuid.UUID, error) {
	result := make([]uuid.UUID, 0, len(ids))
	seen := make(map[uuid.UUID]bool, len(ids))
	for _, value := range ids {
		id, err := uuid.Parse(value)
		if err != nil || id == uuid.Nil {
			return nil, fmt.Errorf("%w: invalid memory session ID", store.ErrBotInvalid)
		}
		if !seen[id] {
			result = append(result, id)
			seen[id] = true
		}
	}
	return result, nil
}

func (m *teamMemoryStore) SearchBySessions(ctx context.Context, sessions []string, query string, limit int, minScore float64, category string) ([]store.MemorySearchResult, error) {
	ids, err := botMemorySessionIDs(sessions)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 || query == "" || ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if m.client == nil || m.db == nil {
		return nil, store.ErrBotUnsupported
	}
	candidates, err := m.ListBySessions(ctx, sessions, category, 0, 0)
	if err != nil {
		return nil, err
	}
	currentIDs := currentBotMemoryIDs(candidates)
	scores, err := searchBotMemory(ctx, m.db, m.dialect, m.table, m.ftsTable, m.embedFunc, ids, query, limit, minScore, category, currentIDs)
	if err != nil || len(scores) == 0 {
		return nil, err
	}
	keys, scoreMap, err := botMemoryScores(scores)
	if err != nil {
		return nil, err
	}
	q := m.client.Memory.Query().Where(teammemory.SessionIDIn(ids...), teammemory.IDIn(keys...))
	if category != "" {
		q.Where(teammemory.CategoryEQ(category))
	}
	rows, err := q.All(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]store.MemorySearchResult, 0, len(rows))
	for _, row := range rows {
		r := entMemoryToResult(row)
		// Recheck after scoring: a concurrent forget must not reappear through
		// an ID selected before its tombstone committed.
		if !store.IsCurrentBotMemory(r) {
			return nil, store.ErrBotConflict
		}
		r.Score = scoreMap[r.ID]
		result = append(result, r)
	}
	sortMemoryResults(result)
	return result, nil
}

func (m *personalMemoryStore) SearchBySessions(ctx context.Context, sessions []string, query string, limit int, minScore float64, category string) ([]store.MemorySearchResult, error) {
	ids, err := botMemorySessionIDs(sessions)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 || query == "" || ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if m.client == nil || m.db == nil {
		return nil, store.ErrBotUnsupported
	}
	candidates, err := m.ListBySessions(ctx, sessions, category, 0, 0)
	if err != nil {
		return nil, err
	}
	currentIDs := currentBotMemoryIDs(candidates)
	scores, err := searchBotMemory(ctx, m.db, m.dialect, m.table, m.ftsTable, m.embedFunc, ids, query, limit, minScore, category, currentIDs)
	if err != nil || len(scores) == 0 {
		return nil, err
	}
	keys, scoreMap, err := botMemoryScores(scores)
	if err != nil {
		return nil, err
	}
	q := m.client.Memory.Query().Where(personalmemory.SessionIDIn(ids...), personalmemory.IDIn(keys...))
	if category != "" {
		q.Where(personalmemory.CategoryEQ(category))
	}
	rows, err := q.All(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]store.MemorySearchResult, 0, len(rows))
	for _, row := range rows {
		r := personalMemoryToSearchResult(row)
		// Recheck after scoring: a concurrent forget must not reappear through
		// an ID selected before its tombstone committed.
		if !store.IsCurrentBotMemory(r) {
			return nil, store.ErrBotConflict
		}
		r.Score = scoreMap[r.ID]
		result = append(result, r)
	}
	sortMemoryResults(result)
	return result, nil
}

func botMemoryScores(scores []scoredResult) ([]uuid.UUID, map[string]float64, error) {
	ids := make([]uuid.UUID, 0, len(scores))
	values := make(map[string]float64, len(scores))
	for _, score := range scores {
		id, err := uuid.Parse(score.ID)
		if err != nil {
			return nil, nil, err
		}
		ids = append(ids, id)
		values[id.String()] = score.Score
	}
	return ids, values, nil
}

// botMemorySQLScope binds all caller-controlled values. Table identifiers come
// only from the same routed store configuration used by ordinary memory search.
func botMemorySQLScope(dialect Dialect, ids []uuid.UUID, category string) (string, []any) {
	args := make([]any, 0, len(ids)+1)
	placeholders := make([]string, 0, len(ids))
	for _, id := range ids {
		args = append(args, id.String())
		p := "?"
		if dialect == DialectPostgres {
			p = fmt.Sprintf("$%d", len(args))
		}
		placeholders = append(placeholders, p)
	}
	where := "session_id IN (" + strings.Join(placeholders, ",") + ")"
	if category != "" {
		args = append(args, category)
		p := "?"
		if dialect == DialectPostgres {
			p = fmt.Sprintf("$%d", len(args))
		}
		where += " AND category = " + p
	}
	return where, args
}

// searchBotMemory never queries the global vector cache: persisted embeddings
// are selected by session/category first, then scored with the existing cosine
// and fusion mechanisms. No second index or unrestricted top-K fallback exists.
func searchBotMemory(ctx context.Context, db *sql.DB, dialect Dialect, table, fts string, embed store.EmbedFunc, ids []uuid.UUID, query string, limit int, minScore float64, category string, candidateIDs ...[]uuid.UUID) ([]scoredResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(ids) == 0 || query == "" {
		return nil, nil
	}
	if db == nil || (dialect != DialectSQLite && dialect != DialectPostgres) {
		return nil, store.ErrBotUnsupported
	}
	if limit <= 0 {
		limit = 10
	}
	// Both supported routed scopes use this table; reject an incomplete or
	// unexpected adapter rather than interpolate an arbitrary identifier.
	if table != "memories" {
		return nil, store.ErrBotUnsupported
	}
	if dialect == DialectSQLite && fts != "" && fts != "memories_fts" {
		return nil, store.ErrBotUnsupported
	}
	if dialect == DialectSQLite && fts == "" && embed == nil {
		return nil, store.ErrBotUnsupported
	}
	var embedding []float32
	if embed != nil {
		var err error
		embedding, err = embed(ctx, query)
		if err != nil {
			return nil, fmt.Errorf("generate query embedding: %w", err)
		}
		if len(embedding) == 0 {
			return nil, fmt.Errorf("semantic query requires embedding")
		}
	}
	where, args := botMemorySQLScope(dialect, ids, category)
	if len(candidateIDs) > 0 {
		if len(candidateIDs[0]) == 0 {
			return nil, nil
		}
		var placeholders []string
		for _, id := range candidateIDs[0] {
			args = append(args, id.String())
			p := "?"
			if dialect == DialectPostgres {
				p = fmt.Sprintf("$%d", len(args))
			}
			placeholders = append(placeholders, p)
		}
		// Qualify ID for the SQLite FTS join without changing the PG CTE.
		where += " AND memories.id IN (" + strings.Join(placeholders, ",") + ")"
	}
	var vectors, keywords []scoredResult
	var err error
	if dialect == DialectPostgres {
		// Materialization forces exact ranking over authorized candidates instead of
		// allowing an ANN index to select global neighbors before the session filter.
		prefix := "WITH scoped AS MATERIALIZED (SELECT id, embedding, tsv FROM memories WHERE " + where + ") "
		if len(embedding) > 0 {
			p := fmt.Sprintf("$%d", len(args)+1)
			sqlQuery := prefix + "SELECT id, 1 - (embedding <=> " + p + "::vector) AS score FROM scoped WHERE embedding IS NOT NULL ORDER BY score DESC, id ASC LIMIT " + fmt.Sprintf("$%d", len(args)+2)
			vectors, err = queryBotMemoryScores(ctx, db, sqlQuery, append(append([]any{}, args...), float32SliceToPGVectorString(embedding), limit)...)
			if err != nil {
				return nil, err
			}
			filtered := vectors[:0]
			for _, v := range vectors {
				if v.Score >= minScore {
					filtered = append(filtered, v)
				}
			}
			vectors = filtered
		}
		if keyword := buildORTsquery(query); keyword != "" {
			p := fmt.Sprintf("$%d", len(args)+1)
			sqlQuery := prefix + "SELECT id, ts_rank(tsv, websearch_to_tsquery('english', " + p + ")) AS score FROM scoped WHERE tsv @@ websearch_to_tsquery('english', " + p + ") ORDER BY score DESC, id ASC LIMIT " + fmt.Sprintf("$%d", len(args)+2)
			keywords, err = queryBotMemoryScores(ctx, db, sqlQuery, append(append([]any{}, args...), keyword, limit)...)
			if err != nil {
				return nil, err
			}
			for i := range keywords {
				if keywords[i].Score < tsvectorFloorScore {
					keywords[i].Score = tsvectorFloorScore
				}
			}
		}
		merged := make(map[string]float64)
		for _, source := range [][]scoredResult{vectors, keywords} {
			for _, r := range source {
				if old, ok := merged[r.ID]; !ok || r.Score > old {
					merged[r.ID] = r.Score
				}
			}
		}
		var results []scoredResult
		for id, score := range merged {
			results = append(results, scoredResult{ID: id, Score: score})
		}
		return rankBotMemoryScores(results, limit), nil
	}
	if len(embedding) > 0 {
		// Build the vector query with bound predicates rather than SQL fragments.
		vectorQuery := entsql.Select("id", "embedding").From(entsql.Table("memories")).
			Where(entsql.And(entsql.In("session_id", uuidStringsToAny(ids)...), entsql.NotNull("embedding")))
		if category != "" {
			vectorQuery.Where(entsql.EQ("category", category))
		}
		if len(candidateIDs) > 0 {
			vectorQuery.Where(entsql.In("id", uuidStringsToAny(candidateIDs[0])...))
		}
		vectorSQL, vectorArgs := vectorQuery.Query()
		rows, err := db.QueryContext(ctx, vectorSQL, vectorArgs...)
		if err != nil {
			return nil, fmt.Errorf("scoped vector search: %w", err)
		}
		for rows.Next() {
			var id string
			var blob []byte
			if err := rows.Scan(&id, &blob); err != nil {
				rows.Close()
				return nil, err
			}
			if vector := deserializeEmbedding(blob); len(vector) > 0 {
				score := cosineSimilarity(embedding, vector)
				if score >= minScore {
					vectors = append(vectors, scoredResult{ID: id, Score: score})
				}
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
		vectors = rankBotMemoryScores(vectors, limit*2)
	}
	if keyword := sqliteFTSQuery(query); fts != "" && keyword != "" {
		sqlQuery := "SELECT m.id, -fts.rank AS score FROM memories_fts fts JOIN memories m ON m.rowid = fts.rowid WHERE " + strings.ReplaceAll(where, "memories.id", "m.id") + " AND memories_fts MATCH ? ORDER BY fts.rank, m.id ASC LIMIT ?"
		keywords, err = queryBotMemoryScores(ctx, db, sqlQuery, append(append([]any{}, args...), keyword, limit*2)...)
		if err != nil {
			return nil, err
		}
	}
	switch {
	case len(vectors) > 0 && len(keywords) > 0:
		return rrfFuse(vectors, keywords, 60, limit), nil
	case len(keywords) > 0:
		return normalizeSingleSource(keywords, limit), nil
	default:
		return rankBotMemoryScores(vectors, limit), nil
	}
}

func uuidStringsToAny(ids []uuid.UUID) []any {
	values := make([]any, len(ids))
	for i, id := range ids {
		values[i] = id.String()
	}
	return values
}

func rankBotMemoryScores(scores []scoredResult, limit int) []scoredResult {
	sort.Slice(scores, func(i, j int) bool {
		if scores[i].Score != scores[j].Score {
			return scores[i].Score > scores[j].Score
		}
		return scores[i].ID < scores[j].ID
	})
	if len(scores) > limit {
		scores = scores[:limit]
	}
	return scores
}

func queryBotMemoryScores(ctx context.Context, db *sql.DB, query string, args ...any) ([]scoredResult, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("scoped memory search: %w", err)
	}
	defer rows.Close()
	var results []scoredResult
	for rows.Next() {
		var row scoredResult
		var score sql.NullFloat64
		if err := rows.Scan(&row.ID, &score); err != nil {
			return nil, err
		}
		if score.Valid {
			row.Score = score.Float64
			results = append(results, row)
		}
	}
	return results, rows.Err()
}

func (m *teamMemoryStore) ListBySessions(ctx context.Context, sessions []string, category string, limit, offset int) ([]store.MemorySearchResult, error) {
	ids, err := botMemorySessionIDs(sessions)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, nil
	}
	if limit < 0 || offset < 0 {
		return nil, store.ErrBotInvalid
	}
	q := m.client.Memory.Query().Where(teammemory.SessionIDIn(ids...)).Order(teament.Asc(teammemory.FieldCreatedAt), teament.Asc(teammemory.FieldID))
	if category != "" {
		q.Where(teammemory.CategoryEQ(category))
	}
	if limit > 0 {
		q.Limit(limit)
	}
	if offset > 0 {
		q.Offset(offset)
	}
	rows, err := q.All(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]store.MemorySearchResult, 0, len(rows))
	for _, row := range rows {
		result = append(result, entMemoryToResult(row))
	}
	return result, nil
}

func (m *personalMemoryStore) ListBySessions(ctx context.Context, sessions []string, category string, limit, offset int) ([]store.MemorySearchResult, error) {
	ids, err := botMemorySessionIDs(sessions)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, nil
	}
	if limit < 0 || offset < 0 {
		return nil, store.ErrBotInvalid
	}
	q := m.client.Memory.Query().Where(personalmemory.SessionIDIn(ids...)).Order(personalent.Asc(personalmemory.FieldCreatedAt), personalent.Asc(personalmemory.FieldID))
	if category != "" {
		q.Where(personalmemory.CategoryEQ(category))
	}
	if limit > 0 {
		q.Limit(limit)
	}
	if offset > 0 {
		q.Offset(offset)
	}
	rows, err := q.All(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]store.MemorySearchResult, 0, len(rows))
	for _, row := range rows {
		result = append(result, personalMemoryToSearchResult(row))
	}
	return result, nil
}
