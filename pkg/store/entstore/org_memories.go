package entstore

import (
	"context"
	"database/sql"
	"encoding/binary"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"

	orgent "github.com/SAP/astonish/ent/org"
	"github.com/SAP/astonish/ent/org/orgmemory"
	"github.com/SAP/astonish/pkg/store"
)

// orgMemoryStore implements store.MemoryStore for org-level memories.
type orgMemoryStore struct {
	client    *orgent.Client
	db        *sql.DB
	dialect   Dialect
	embedFunc store.EmbedFunc
	vecIndex  *vectorIndex // in-memory vector index for SQLite
	ftsTable  string       // FTS5 table name (SQLite only)
	table     string       // main table name (for raw SQL)
}

var _ store.MemoryStore = (*orgMemoryStore)(nil)
var _ store.PreparedMemoryStore = (*orgMemoryStore)(nil)

func (ms *orgMemoryStore) Search(ctx context.Context, query string, maxResults int, minScore float64) ([]store.MemorySearchResult, error) {
	return ms.SearchByCategory(ctx, query, maxResults, minScore, "")
}

func (ms *orgMemoryStore) SearchByCategory(ctx context.Context, query string, maxResults int, minScore float64, category string) ([]store.MemorySearchResult, error) {
	if ms.dialect == DialectPostgres {
		return ms.hybridSearch(ctx, query, maxResults, minScore, category)
	}
	// SQLite: FTS5 + in-memory vector hybrid search.
	return ms.sqliteHybridSearch(ctx, query, maxResults, minScore, category)
}

func (ms *orgMemoryStore) hybridSearch(ctx context.Context, query string, maxResults int, minScore float64, category string) ([]store.MemorySearchResult, error) {
	prepared := store.PreparedMemoryQuery{KeywordQuery: query}
	if ms.embedFunc != nil && query != "" {
		prepared.SemanticQuery = query
		embedding, err := ms.embedFunc(ctx, query)
		if err != nil {
			return nil, fmt.Errorf("generate query embedding: %w", err)
		}
		prepared.Embedding = embedding
	}
	return ms.SearchPrepared(ctx, prepared, maxResults, minScore, category)
}

func (ms *orgMemoryStore) SearchPrepared(ctx context.Context, query store.PreparedMemoryQuery, maxResults int, minScore float64, category string) ([]store.MemorySearchResult, error) {
	if query.SemanticQuery != "" && len(query.Embedding) == 0 {
		return nil, fmt.Errorf("semantic query requires embedding")
	}
	if ms.dialect == DialectSQLite {
		return ms.sqlitePreparedSearch(ctx, query, maxResults, minScore, category)
	}
	var vectorResults, keywordResults []store.MemorySearchResult
	if len(query.Embedding) > 0 {
		results, err := ms.vectorSearch(ctx, query.Embedding, maxResults, minScore, category)
		if err != nil {
			return nil, err
		}
		vectorResults = results
	}
	if query.KeywordQuery != "" {
		results, err := ms.tsvectorSearch(ctx, query.KeywordQuery, maxResults, category)
		if err != nil {
			return nil, err
		}
		keywordResults = results
	}
	return mergeMemoryResults(vectorResults, keywordResults, maxResults), nil
}

func (ms *orgMemoryStore) vectorSearch(ctx context.Context, embedding []float32, maxResults int, minScore float64, category string) ([]store.MemorySearchResult, error) {
	vecStr := float32SliceToPGVectorString(embedding)
	var err error

	var rows *sql.Rows
	if category != "" {
		rows, err = ms.db.QueryContext(ctx,
			`SELECT id, chunk_text, category, source_path, promoted_by, session_id, created_at,
				1 - (embedding <=> $1::vector) AS score
			FROM org_memories
			WHERE embedding IS NOT NULL AND category = $2
			ORDER BY embedding <=> $1::vector, id ASC
			LIMIT $3`,
			vecStr, category, maxResults)
	} else {
		rows, err = ms.db.QueryContext(ctx,
			`SELECT id, chunk_text, category, source_path, promoted_by, session_id, created_at,
				1 - (embedding <=> $1::vector) AS score
			FROM org_memories
			WHERE embedding IS NOT NULL
			ORDER BY embedding <=> $1::vector, id ASC
			LIMIT $2`,
			vecStr, maxResults)
	}
	if err != nil {
		return nil, fmt.Errorf("vector search query: %w", err)
	}
	defer rows.Close()

	var results []store.MemorySearchResult
	for rows.Next() {
		var (
			id         uuid.UUID
			chunkText  string
			cat        sql.NullString
			srcPath    sql.NullString
			promotedBy uuid.UUID
			sessionID  sql.NullString
			createdAt  time.Time
			score      sql.NullFloat64
		)
		if err := rows.Scan(&id, &chunkText, &cat, &srcPath, &promotedBy, &sessionID, &createdAt, &score); err != nil {
			return nil, fmt.Errorf("scan memory search result: %w", err)
		}
		if !score.Valid || score.Float64 < minScore {
			continue
		}
		r := store.MemorySearchResult{
			ID:        id.String(),
			Snippet:   chunkText,
			Score:     score.Float64,
			Scope:     "org",
			CreatedBy: promotedBy.String(),
			CreatedAt: createdAt.Format(time.RFC3339),
		}
		if cat.Valid {
			r.Category = cat.String
		}
		if srcPath.Valid {
			r.Path = srcPath.String
		}
		if sessionID.Valid {
			r.SessionID = sessionID.String
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

func (ms *orgMemoryStore) tsvectorSearch(ctx context.Context, query string, maxResults int, category string) ([]store.MemorySearchResult, error) {
	orQuery := buildORTsquery(query)
	if orQuery == "" {
		return nil, nil
	}

	var rows *sql.Rows
	var err error
	if category != "" {
		rows, err = ms.db.QueryContext(ctx,
			`SELECT id, chunk_text, category, source_path, promoted_by, session_id, created_at,
				ts_rank(tsv, websearch_to_tsquery('english', $1)) AS score
			FROM org_memories
			WHERE tsv @@ websearch_to_tsquery('english', $1) AND category = $2
			ORDER BY score DESC, id ASC
			LIMIT $3`,
			orQuery, category, maxResults)
	} else {
		rows, err = ms.db.QueryContext(ctx,
			`SELECT id, chunk_text, category, source_path, promoted_by, session_id, created_at,
				ts_rank(tsv, websearch_to_tsquery('english', $1)) AS score
			FROM org_memories
			WHERE tsv @@ websearch_to_tsquery('english', $1)
			ORDER BY score DESC, id ASC
			LIMIT $2`,
			orQuery, maxResults)
	}
	if err != nil {
		return nil, fmt.Errorf("tsvector search query: %w", err)
	}
	defer rows.Close()

	var results []store.MemorySearchResult
	for rows.Next() {
		var (
			id         uuid.UUID
			chunkText  string
			cat        sql.NullString
			srcPath    sql.NullString
			promotedBy uuid.UUID
			sessionID  sql.NullString
			createdAt  time.Time
			score      float64
		)
		if err := rows.Scan(&id, &chunkText, &cat, &srcPath, &promotedBy, &sessionID, &createdAt, &score); err != nil {
			return nil, fmt.Errorf("scan memory search result: %w", err)
		}
		// Apply floor score (see tsvectorFloorScore doc in team_memories.go).
		if score < tsvectorFloorScore {
			score = tsvectorFloorScore
		}
		r := store.MemorySearchResult{
			ID:        id.String(),
			Snippet:   chunkText,
			Score:     score,
			Scope:     "org",
			CreatedBy: promotedBy.String(),
			CreatedAt: createdAt.Format(time.RFC3339),
		}
		if cat.Valid {
			r.Category = cat.String
		}
		if srcPath.Valid {
			r.Path = srcPath.String
		}
		if sessionID.Valid {
			r.SessionID = sessionID.String
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

// ---------------------------------------------------------------------------
// SQLite Hybrid Search (FTS5 + in-memory vector)
// ---------------------------------------------------------------------------

func (ms *orgMemoryStore) sqliteHybridSearch(ctx context.Context, query string, maxResults int, minScore float64, category string) ([]store.MemorySearchResult, error) {
	prepared := store.PreparedMemoryQuery{KeywordQuery: query}
	if ms.embedFunc != nil && query != "" {
		prepared.SemanticQuery = query
		embedding, err := ms.embedFunc(ctx, query)
		if err != nil {
			return nil, fmt.Errorf("generate query embedding: %w", err)
		}
		prepared.Embedding = embedding
	}
	return ms.sqlitePreparedSearch(ctx, prepared, maxResults, minScore, category)
}

func (ms *orgMemoryStore) sqlitePreparedSearch(ctx context.Context, query store.PreparedMemoryQuery, maxResults int, minScore float64, category string) ([]store.MemorySearchResult, error) {
	if maxResults <= 0 {
		maxResults = 10
	}
	keywordResults, err := ms.ftsSearch(ctx, query.KeywordQuery, maxResults*2, category)
	if err != nil {
		return nil, err
	}
	var vectorResults []scoredResult
	if len(query.Embedding) > 0 {
		if err := ms.ensureVecIndexLoaded(ctx); err != nil {
			return nil, err
		}
		vectorResults = ms.vecIndex.search(query.Embedding, maxResults*2, minScore)
	}
	var fused []scoredResult
	switch {
	case len(vectorResults) > 0 && len(keywordResults) > 0:
		fused = rrfFuse(vectorResults, keywordResults, 60, maxResults)
	case len(keywordResults) > 0:
		fused = normalizeSingleSource(keywordResults, maxResults)
	default:
		if len(vectorResults) > maxResults {
			vectorResults = vectorResults[:maxResults]
		}
		fused = vectorResults
	}

	return ms.loadResultsByIDs(ctx, fused, category)
}

func (ms *orgMemoryStore) ftsSearch(ctx context.Context, query string, limit int, category string) ([]scoredResult, error) {
	if query == "" || ms.ftsTable == "" {
		return nil, nil
	}

	ftsQuery := sqliteFTSQuery(query)
	if ftsQuery == "" {
		return nil, nil
	}

	var rows *sql.Rows
	var err error
	if category != "" {
		rows, err = ms.db.QueryContext(ctx,
			fmt.Sprintf(
				`SELECT m.id, -fts.rank AS score
				 FROM %s fts
				 JOIN %s m ON m.rowid = fts.rowid
				 WHERE %s MATCH ? AND m.category = ?
				 ORDER BY fts.rank, m.id ASC
				 LIMIT ?`, ms.ftsTable, ms.table, ms.ftsTable),
			ftsQuery, category, limit)
	} else {
		rows, err = ms.db.QueryContext(ctx,
			fmt.Sprintf(
				`SELECT m.id, -fts.rank AS score
				 FROM %s fts
				 JOIN %s m ON m.rowid = fts.rowid
				 WHERE %s MATCH ?
				 ORDER BY fts.rank, m.id ASC
				 LIMIT ?`, ms.ftsTable, ms.table, ms.ftsTable),
			ftsQuery, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("fts search query: %w", err)
	}
	defer rows.Close()

	var results []scoredResult
	for rows.Next() {
		var id string
		var score float64
		if err := rows.Scan(&id, &score); err != nil {
			return nil, fmt.Errorf("scan fts search result: %w", err)
		}
		results = append(results, scoredResult{ID: id, Score: score})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate fts search results: %w", err)
	}
	return results, nil
}

func (ms *orgMemoryStore) ensureVecIndexLoaded(ctx context.Context) error {
	if ms.vecIndex.isLoaded() {
		return nil
	}

	rows, err := ms.db.QueryContext(ctx,
		fmt.Sprintf(`SELECT id, embedding FROM %s WHERE embedding IS NOT NULL`, ms.table))
	if err != nil {
		return fmt.Errorf("load vector index: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var id string
		var blob []byte
		if err := rows.Scan(&id, &blob); err != nil {
			return fmt.Errorf("scan vector index: %w", err)
		}
		if vec := deserializeEmbedding(blob); vec != nil {
			ms.vecIndex.add(id, vec)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate vector index: %w", err)
	}
	ms.vecIndex.markLoaded()
	return nil
}

func (ms *orgMemoryStore) loadResultsByIDs(ctx context.Context, scored []scoredResult, category string) ([]store.MemorySearchResult, error) {
	if len(scored) == 0 {
		return nil, nil
	}

	ids := make([]interface{}, len(scored))
	placeholders := make([]string, len(scored))
	scoreMap := make(map[string]float64)
	for i, r := range scored {
		ids[i] = r.ID
		placeholders[i] = "?"
		scoreMap[r.ID] = r.Score
	}

	sqlStr := fmt.Sprintf( //nolint:gosec // table name is a hardcoded constant
		`SELECT id, chunk_text, category, source_path, promoted_by, session_id, created_at
		 FROM %s WHERE id IN (%s)`,
		ms.table, strings.Join(placeholders, ","))
	if category != "" {
		sqlStr += " AND category = ?"
		ids = append(ids, category)
	}

	rows, err := ms.db.QueryContext(ctx, sqlStr, ids...)
	if err != nil {
		return nil, fmt.Errorf("load results by IDs: %w", err)
	}
	defer rows.Close()

	var results []store.MemorySearchResult
	for rows.Next() {
		var (
			id         string
			chunkText  string
			cat        sql.NullString
			srcPath    sql.NullString
			promotedBy sql.NullString
			sessionID  sql.NullString
			createdAt  sql.NullString
		)
		if err := rows.Scan(&id, &chunkText, &cat, &srcPath, &promotedBy, &sessionID, &createdAt); err != nil {
			return nil, fmt.Errorf("scan memory result: %w", err)
		}
		r := store.MemorySearchResult{
			ID:      id,
			Snippet: chunkText,
			Scope:   "org",
		}
		if cat.Valid {
			r.Category = cat.String
		}
		if srcPath.Valid {
			r.Path = srcPath.String
		}
		if promotedBy.Valid {
			r.CreatedBy = promotedBy.String
		}
		if sessionID.Valid {
			r.SessionID = sessionID.String
		}
		if createdAt.Valid {
			r.CreatedAt = createdAt.String
		}
		if score, ok := scoreMap[id]; ok {
			r.Score = score
		}
		results = append(results, r)
	}
	sortMemoryResults(results)
	return results, nil
}
func (ms *orgMemoryStore) Add(ctx context.Context, entry store.MemoryEntry) error {
	create := ms.client.OrgMemory.Create().
		SetChunkText(entry.Content)

	if entry.Category != "" {
		create.SetCategory(entry.Category)
	}
	if entry.SourcePath != "" {
		create.SetSourcePath(entry.SourcePath)
	}
	if entry.Metadata != nil {
		create.SetMetadata(entry.Metadata)
	}
	if entry.SessionID != "" {
		sid, err := uuid.Parse(entry.SessionID)
		if err == nil {
			create.SetSessionID(sid)
		}
	}

	// Set promoted_by (required field) — use CreatedBy or a nil UUID.
	if entry.CreatedBy != "" {
		uid, err := uuid.Parse(entry.CreatedBy)
		if err == nil {
			create.SetPromotedBy(uid)
		} else {
			create.SetPromotedBy(uuid.Nil)
		}
	} else {
		create.SetPromotedBy(uuid.Nil)
	}

	// Generate embedding if embed function is available.
	var newEmb []float32
	if ms.embedFunc != nil && entry.Content != "" {
		embedding, err := ms.embedFunc(ctx, entry.Content)
		if err != nil {
			return fmt.Errorf("generate memory embedding: %w", err)
		}
		if len(embedding) > 0 {
			newEmb = embedding
			if ms.dialect == DialectSQLite {
				create.SetEmbedding(float32SliceToBytes(embedding))
			}
		}
	}

	// On SQLite, store raw text for LIKE-based keyword search.
	// On PG, the tsvector trigger generates this from chunk_text.
	if ms.dialect == DialectSQLite {
		create.SetTsv(entry.Content)
	}

	saved, err := create.Save(ctx)
	if err != nil {
		return err
	}

	// On PG, update embedding via raw SQL with vector text format.
	if newEmb != nil && ms.dialect == DialectPostgres {
		vecStr := float32SliceToPGVectorString(newEmb)
		_, err = ms.db.ExecContext(ctx,
			`UPDATE org_memories SET embedding = $1::vector WHERE id = $2`,
			vecStr, saved.ID)
		if err != nil {
			return fmt.Errorf("set embedding: %w", err)
		}
	}

	// On SQLite, update in-memory vector index.
	if newEmb != nil && ms.dialect == DialectSQLite && ms.vecIndex != nil {
		ms.vecIndex.add(saved.ID.String(), newEmb)
	}

	return nil
}

func (ms *orgMemoryStore) Get(ctx context.Context, id string) (*store.MemorySearchResult, error) {
	uid, err := uuid.Parse(id)
	if err != nil {
		return nil, fmt.Errorf("invalid memory ID: %w", err)
	}
	e, err := ms.client.OrgMemory.Get(ctx, uid)
	if err != nil {
		if orgent.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	r := orgMemoryToSearchResult(e)
	return &r, nil
}

func (ms *orgMemoryStore) Update(ctx context.Context, id string, content string, category string) error {
	uid, err := uuid.Parse(id)
	if err != nil {
		return fmt.Errorf("invalid memory ID: %w", err)
	}

	update := ms.client.OrgMemory.UpdateOneID(uid).
		SetChunkText(content)

	if category != "" {
		update.SetCategory(category)
	} else {
		update.ClearCategory()
	}

	// Re-generate embedding if content changed.
	var newEmb []float32
	if ms.embedFunc != nil && content != "" {
		embedding, err := ms.embedFunc(ctx, content)
		if err != nil {
			return fmt.Errorf("generate memory embedding: %w", err)
		}
		if len(embedding) > 0 {
			newEmb = embedding
			if ms.dialect == DialectSQLite {
				update.SetEmbedding(float32SliceToBytes(embedding))
			}
		}
	}

	if ms.dialect == DialectSQLite {
		update.SetTsv(content)
	}

	if err := update.Exec(ctx); err != nil {
		return err
	}

	// On PG, update embedding via raw SQL with vector text format.
	if newEmb != nil && ms.dialect == DialectPostgres {
		vecStr := float32SliceToPGVectorString(newEmb)
		_, err = ms.db.ExecContext(ctx,
			`UPDATE org_memories SET embedding = $1::vector WHERE id = $2`,
			vecStr, uid)
		if err != nil {
			return fmt.Errorf("set embedding: %w", err)
		}
	}

	// On SQLite, update in-memory vector index.
	if newEmb != nil && ms.dialect == DialectSQLite && ms.vecIndex != nil {
		ms.vecIndex.add(uid.String(), newEmb)
	}

	return nil
}

func (ms *orgMemoryStore) Delete(ctx context.Context, id string) error {
	uid, err := uuid.Parse(id)
	if err != nil {
		return fmt.Errorf("invalid memory ID: %w", err)
	}
	// Remove from in-memory vector index on SQLite.
	if ms.dialect == DialectSQLite && ms.vecIndex != nil {
		ms.vecIndex.remove(uid.String())
	}
	return ms.client.OrgMemory.DeleteOneID(uid).Exec(ctx)
}

func (ms *orgMemoryStore) List(ctx context.Context, category string, limit, offset int) ([]store.MemorySearchResult, error) {
	q := ms.client.OrgMemory.Query()
	if category != "" {
		q = q.Where(orgmemory.CategoryEQ(category))
	}
	if limit > 0 {
		q = q.Limit(limit)
	}
	if offset > 0 {
		q = q.Offset(offset)
	}
	q = q.Order(orgmemory.ByCreatedAt())

	ents, err := q.All(ctx)
	if err != nil {
		return nil, err
	}

	results := make([]store.MemorySearchResult, len(ents))
	for i, e := range ents {
		results[i] = orgMemoryToSearchResult(e)
	}
	return results, nil
}

func (ms *orgMemoryStore) ListBySession(ctx context.Context, sessionID string) ([]store.MemorySearchResult, error) {
	sid, err := uuid.Parse(sessionID)
	if err != nil {
		return nil, fmt.Errorf("invalid session ID: %w", err)
	}

	ents, err := ms.client.OrgMemory.Query().
		Where(orgmemory.SessionIDEQ(sid)).
		All(ctx)
	if err != nil {
		return nil, err
	}

	results := make([]store.MemorySearchResult, len(ents))
	for i, e := range ents {
		results[i] = orgMemoryToSearchResult(e)
	}
	return results, nil
}

func (ms *orgMemoryStore) Count() int {
	count, _ := ms.client.OrgMemory.Query().Count(context.Background())
	return count
}

func (ms *orgMemoryStore) Close() error {
	return nil
}

func orgMemoryToSearchResult(e *orgent.OrgMemory) store.MemorySearchResult {
	r := store.MemorySearchResult{
		ID:        e.ID.String(),
		Snippet:   e.ChunkText,
		Scope:     "org",
		CreatedAt: e.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
	if e.Category != nil {
		r.Category = *e.Category
	}
	if e.SourcePath != nil {
		r.Path = *e.SourcePath
	}
	if e.SessionID != nil {
		r.SessionID = e.SessionID.String()
	}
	r.CreatedBy = e.PromotedBy.String()
	return r
}

// float32SliceToBytes converts a []float32 to []byte for SQLite BLOB storage.
func float32SliceToBytes(v []float32) []byte {
	buf := make([]byte, len(v)*4)
	for i, f := range v {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(f))
	}
	return buf
}

// float32SliceToPGVectorString converts a []float32 to pgvector text format string: [f1,f2,...,fN]
// This format is passed as a string parameter with ::vector cast in SQL queries.
func float32SliceToPGVectorString(v []float32) string {
	if len(v) == 0 {
		return "[]"
	}
	var b strings.Builder
	b.WriteByte('[')
	for i, f := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(fmt.Sprintf("%g", f))
	}
	b.WriteByte(']')
	return b.String()
}
