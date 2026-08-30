package entstore

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	teament "github.com/SAP/astonish/ent/team"
	"github.com/SAP/astonish/ent/team/memory"
	"github.com/SAP/astonish/pkg/store"
)

// teamMemoryStore implements store.MemoryStore backed by the team Ent client.
type teamMemoryStore struct {
	client    *teament.Client
	db        *sql.DB
	dialect   Dialect
	embedFunc store.EmbedFunc
	vecIndex  *vectorIndex // in-memory vector index for SQLite
	ftsTable  string       // FTS5 table name (SQLite only)
	table     string       // main table name (for raw SQL)
}

var _ store.MemoryStore = (*teamMemoryStore)(nil)
var _ store.PreparedMemoryStore = (*teamMemoryStore)(nil)

// tsvectorFloorScore is the minimum score assigned to any tsvector keyword
// match. ts_rank() returns very small values (0.01–0.1) but any keyword hit
// is a high-precision signal. This floor ensures results survive the 0.3
// minScore threshold applied by three-tier search.
const tsvectorFloorScore = 0.5

// ---------------------------------------------------------------------------
// Search
// ---------------------------------------------------------------------------

func (m *teamMemoryStore) Search(ctx context.Context, query string, maxResults int, minScore float64) ([]store.MemorySearchResult, error) {
	return m.searchInternal(ctx, query, maxResults, minScore, "")
}

func (m *teamMemoryStore) SearchByCategory(ctx context.Context, query string, maxResults int, minScore float64, category string) ([]store.MemorySearchResult, error) {
	return m.searchInternal(ctx, query, maxResults, minScore, category)
}

func (m *teamMemoryStore) searchInternal(ctx context.Context, query string, maxResults int, minScore float64, category string) ([]store.MemorySearchResult, error) {
	if m.dialect == DialectPostgres {
		return m.hybridSearch(ctx, query, maxResults, minScore, category)
	}
	// SQLite: FTS5 + in-memory vector hybrid search.
	return m.sqliteHybridSearch(ctx, query, maxResults, minScore, category)
}

func (m *teamMemoryStore) hybridSearch(ctx context.Context, query string, maxResults int, minScore float64, category string) ([]store.MemorySearchResult, error) {
	prepared := store.PreparedMemoryQuery{KeywordQuery: query}
	if m.embedFunc != nil && query != "" {
		prepared.SemanticQuery = query
		embedding, err := m.embedFunc(ctx, query)
		if err != nil {
			return nil, fmt.Errorf("generate query embedding: %w", err)
		}
		prepared.Embedding = embedding
	}
	return m.SearchPrepared(ctx, prepared, maxResults, minScore, category)
}

func (m *teamMemoryStore) SearchPrepared(ctx context.Context, query store.PreparedMemoryQuery, maxResults int, minScore float64, category string) ([]store.MemorySearchResult, error) {
	if query.SemanticQuery != "" && len(query.Embedding) == 0 {
		return nil, fmt.Errorf("semantic query requires embedding")
	}
	if m.dialect == DialectSQLite {
		return m.sqlitePreparedSearch(ctx, query, maxResults, minScore, category)
	}
	var vectorResults, keywordResults []store.MemorySearchResult
	if len(query.Embedding) > 0 {
		results, err := m.vectorSearch(ctx, query.Embedding, maxResults, minScore, category)
		if err != nil {
			return nil, err
		}
		vectorResults = results
	}
	if query.KeywordQuery != "" {
		results, err := m.tsvectorSearch(ctx, query.KeywordQuery, maxResults, category)
		if err != nil {
			return nil, err
		}
		keywordResults = results
	}
	return mergeMemoryResults(vectorResults, keywordResults, maxResults), nil
}

func (m *teamMemoryStore) tsvectorSearch(ctx context.Context, query string, maxResults int, category string) ([]store.MemorySearchResult, error) {
	orQuery := buildORTsquery(query)
	if orQuery == "" {
		return nil, nil
	}

	var rows *sql.Rows
	var err error
	if category != "" {
		rows, err = m.db.QueryContext(ctx,
			`SELECT id, chunk_text, category, source_path, created_by, session_id, created_at,
				ts_rank(tsv, websearch_to_tsquery('english', $1)) AS score
			FROM memories
			WHERE tsv @@ websearch_to_tsquery('english', $1) AND category = $2
			ORDER BY score DESC, id ASC
			LIMIT $3`,
			orQuery, category, maxResults)
	} else {
		rows, err = m.db.QueryContext(ctx,
			`SELECT id, chunk_text, category, source_path, created_by, session_id, created_at,
				ts_rank(tsv, websearch_to_tsquery('english', $1)) AS score
			FROM memories
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
			id        uuid.UUID
			chunkText string
			cat       sql.NullString
			srcPath   sql.NullString
			createdBy sql.NullString
			sessionID sql.NullString
			createdAt time.Time
			score     float64
		)
		if err := rows.Scan(&id, &chunkText, &cat, &srcPath, &createdBy, &sessionID, &createdAt, &score); err != nil {
			return nil, fmt.Errorf("scan memory search result: %w", err)
		}
		// ts_rank returns very small values (0.01–0.1). Since any tsvector
		// match is a high-precision keyword hit, apply a floor score so these
		// results survive downstream minScore filtering (typically 0.3).
		if score < tsvectorFloorScore {
			score = tsvectorFloorScore
		}
		r := store.MemorySearchResult{
			ID:        id.String(),
			Snippet:   chunkText,
			Scope:     "team",
			Score:     score,
			CreatedAt: createdAt.Format(time.RFC3339),
		}
		if cat.Valid {
			r.Category = cat.String
		}
		if srcPath.Valid {
			r.Path = srcPath.String
		}
		if createdBy.Valid {
			r.CreatedBy = createdBy.String
		}
		if sessionID.Valid {
			r.SessionID = sessionID.String
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

func (m *teamMemoryStore) vectorSearch(ctx context.Context, embedding []float32, maxResults int, minScore float64, category string) ([]store.MemorySearchResult, error) {
	vecStr := float32SliceToPGVectorString(embedding)
	var err error

	var rows *sql.Rows
	if category != "" {
		rows, err = m.db.QueryContext(ctx,
			`SELECT id, chunk_text, category, source_path, created_by, session_id, created_at,
				1 - (embedding <=> $1::vector) AS score
			FROM memories
			WHERE embedding IS NOT NULL AND category = $2
			ORDER BY embedding <=> $1::vector, id ASC
			LIMIT $3`,
			vecStr, category, maxResults)
	} else {
		rows, err = m.db.QueryContext(ctx,
			`SELECT id, chunk_text, category, source_path, created_by, session_id, created_at,
				1 - (embedding <=> $1::vector) AS score
			FROM memories
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
			id        uuid.UUID
			chunkText string
			cat       sql.NullString
			srcPath   sql.NullString
			createdBy sql.NullString
			sessionID sql.NullString
			createdAt time.Time
			score     sql.NullFloat64
		)
		if err := rows.Scan(&id, &chunkText, &cat, &srcPath, &createdBy, &sessionID, &createdAt, &score); err != nil {
			return nil, fmt.Errorf("scan memory search result: %w", err)
		}
		if !score.Valid || score.Float64 < minScore {
			continue
		}
		r := store.MemorySearchResult{
			ID:        id.String(),
			Snippet:   chunkText,
			Scope:     "team",
			Score:     score.Float64,
			CreatedAt: createdAt.Format(time.RFC3339),
		}
		if cat.Valid {
			r.Category = cat.String
		}
		if srcPath.Valid {
			r.Path = srcPath.String
		}
		if createdBy.Valid {
			r.CreatedBy = createdBy.String
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

func (m *teamMemoryStore) sqliteHybridSearch(ctx context.Context, query string, maxResults int, minScore float64, category string) ([]store.MemorySearchResult, error) {
	prepared := store.PreparedMemoryQuery{KeywordQuery: query}
	if m.embedFunc != nil && query != "" {
		prepared.SemanticQuery = query
		embedding, err := m.embedFunc(ctx, query)
		if err != nil {
			return nil, fmt.Errorf("generate query embedding: %w", err)
		}
		prepared.Embedding = embedding
	}
	return m.sqlitePreparedSearch(ctx, prepared, maxResults, minScore, category)
}

func (m *teamMemoryStore) sqlitePreparedSearch(ctx context.Context, query store.PreparedMemoryQuery, maxResults int, minScore float64, category string) ([]store.MemorySearchResult, error) {
	if maxResults <= 0 {
		maxResults = 10
	}
	keywordResults, err := m.ftsSearch(ctx, query.KeywordQuery, maxResults*2, category)
	if err != nil {
		return nil, err
	}
	var vectorResults []scoredResult
	if len(query.Embedding) > 0 {
		if err := m.ensureVecIndexLoaded(ctx); err != nil {
			return nil, err
		}
		vectorResults = m.vecIndex.search(query.Embedding, maxResults*2, minScore)
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

	// Load full records for the fused IDs.
	return m.loadResultsByIDs(ctx, fused, category)
}

func (m *teamMemoryStore) ftsSearch(ctx context.Context, query string, limit int, category string) ([]scoredResult, error) {
	if query == "" || m.ftsTable == "" {
		return nil, nil
	}

	ftsQuery := sqliteFTSQuery(query)
	if ftsQuery == "" {
		return nil, nil
	}

	var rows *sql.Rows
	var err error
	if category != "" {
		rows, err = m.db.QueryContext(ctx,
			fmt.Sprintf(
				`SELECT m.id, -fts.rank AS score
				 FROM %s fts
				 JOIN %s m ON m.rowid = fts.rowid
				 WHERE %s MATCH ? AND m.category = ?
				 ORDER BY fts.rank, m.id ASC
				 LIMIT ?`, m.ftsTable, m.table, m.ftsTable),
			ftsQuery, category, limit)
	} else {
		rows, err = m.db.QueryContext(ctx,
			fmt.Sprintf(
				`SELECT m.id, -fts.rank AS score
				 FROM %s fts
				 JOIN %s m ON m.rowid = fts.rowid
				 WHERE %s MATCH ?
				 ORDER BY fts.rank, m.id ASC
				 LIMIT ?`, m.ftsTable, m.table, m.ftsTable),
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

func (m *teamMemoryStore) ensureVecIndexLoaded(ctx context.Context) error {
	if m.vecIndex.isLoaded() {
		return nil
	}

	rows, err := m.db.QueryContext(ctx,
		fmt.Sprintf(`SELECT id, embedding FROM %s WHERE embedding IS NOT NULL`, m.table))
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
			m.vecIndex.add(id, vec)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate vector index: %w", err)
	}
	m.vecIndex.markLoaded()
	return nil
}

func (m *teamMemoryStore) loadResultsByIDs(ctx context.Context, scored []scoredResult, category string) ([]store.MemorySearchResult, error) {
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
		`SELECT id, chunk_text, category, source_path, created_by, session_id, created_at
		 FROM %s WHERE id IN (%s)`,
		m.table, strings.Join(placeholders, ","))
	if category != "" {
		sqlStr += " AND category = ?"
		ids = append(ids, category)
	}

	rows, err := m.db.QueryContext(ctx, sqlStr, ids...)
	if err != nil {
		return nil, fmt.Errorf("load results by IDs: %w", err)
	}
	defer rows.Close()

	var results []store.MemorySearchResult
	for rows.Next() {
		var (
			id        string
			chunkText string
			cat       sql.NullString
			srcPath   sql.NullString
			createdBy sql.NullString
			sessionID sql.NullString
			createdAt sql.NullString
		)
		if err := rows.Scan(&id, &chunkText, &cat, &srcPath, &createdBy, &sessionID, &createdAt); err != nil {
			return nil, fmt.Errorf("scan memory result: %w", err)
		}
		r := store.MemorySearchResult{
			ID:      id,
			Snippet: chunkText,
			Scope:   "team",
		}
		if cat.Valid {
			r.Category = cat.String
		}
		if srcPath.Valid {
			r.Path = srcPath.String
		}
		if createdBy.Valid {
			r.CreatedBy = createdBy.String
		}
		if sessionID.Valid {
			r.SessionID = sessionID.String
		}
		if createdAt.Valid {
			r.CreatedAt = createdAt.String
		}
		// Apply fused score.
		if score, ok := scoreMap[id]; ok {
			r.Score = score
		}
		results = append(results, r)
	}
	sortMemoryResults(results)
	return results, nil
}

func (m *teamMemoryStore) Add(ctx context.Context, entry store.MemoryEntry) error {
	create := m.client.Memory.Create().
		SetChunkText(entry.Content).
		SetNillableCategory(nilStrPtr(entry.Category)).
		SetNillableSourcePath(nilStrPtr(entry.SourcePath))

	if entry.Metadata != nil {
		create.SetMetadata(entry.Metadata)
	}

	if entry.CreatedBy != "" {
		uid, err := uuid.Parse(entry.CreatedBy)
		if err == nil {
			create.SetCreatedBy(uid)
		}
	}

	if entry.SessionID != "" {
		sid, err := uuid.Parse(entry.SessionID)
		if err == nil {
			create.SetSessionID(sid)
		}
	}

	// Generate embedding if function is available.
	if m.embedFunc != nil && entry.Embedding == nil {
		emb, err := m.embedFunc(ctx, entry.Content)
		if err != nil {
			return fmt.Errorf("generate memory embedding: %w", err)
		}
		entry.Embedding = emb
	}

	// On SQLite, store embedding as raw bytes via Ent.
	// On PG, we must use raw SQL because Ent sends []byte as bytea, not vector text.
	if entry.Embedding != nil && m.dialect == DialectSQLite {
		create.SetEmbedding(float32SliceToBytes(entry.Embedding))
	}

	// On SQLite, store raw text for LIKE-based keyword search.
	// On PG, the tsvector trigger generates this from chunk_text.
	if m.dialect == DialectSQLite {
		create.SetTsv(entry.Content)
	}

	saved, err := create.Save(ctx)
	if err != nil {
		return err
	}

	// On PG, update embedding via raw SQL with vector text format.
	if entry.Embedding != nil && m.dialect == DialectPostgres {
		vecStr := float32SliceToPGVectorString(entry.Embedding)
		_, err = m.db.ExecContext(ctx,
			`UPDATE memories SET embedding = $1::vector WHERE id = $2`,
			vecStr, saved.ID)
		if err != nil {
			return fmt.Errorf("set embedding: %w", err)
		}
	}

	// On SQLite, update in-memory vector index for instant search availability.
	if entry.Embedding != nil && m.dialect == DialectSQLite && m.vecIndex != nil {
		m.vecIndex.add(saved.ID.String(), entry.Embedding)
	}

	return nil
}

func (m *teamMemoryStore) Get(ctx context.Context, id string) (*store.MemorySearchResult, error) {
	uid, err := uuid.Parse(id)
	if err != nil {
		return nil, fmt.Errorf("invalid memory ID: %w", err)
	}

	ent, err := m.client.Memory.Get(ctx, uid)
	if err != nil {
		if teament.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("get memory: %w", err)
	}

	r := entMemoryToResult(ent)
	return &r, nil
}

func (m *teamMemoryStore) Update(ctx context.Context, id string, content string, category string) error {
	uid, err := uuid.Parse(id)
	if err != nil {
		return fmt.Errorf("invalid memory ID: %w", err)
	}

	update := m.client.Memory.UpdateOneID(uid).
		SetChunkText(content)

	if m.dialect == DialectSQLite {
		update.SetTsv(content)
	}

	if category != "" {
		update.SetCategory(category)
	} else {
		update.ClearCategory()
	}

	// Re-generate embedding if function is available.
	var newEmb []float32
	if m.embedFunc != nil {
		emb, err := m.embedFunc(ctx, content)
		if err != nil {
			return fmt.Errorf("generate memory embedding: %w", err)
		}
		newEmb = emb
		if m.dialect == DialectSQLite {
			update.SetEmbedding(float32SliceToBytes(emb))
		}
	}

	if err := update.Exec(ctx); err != nil {
		return err
	}

	// On PG, update embedding via raw SQL with vector text format.
	if newEmb != nil && m.dialect == DialectPostgres {
		vecStr := float32SliceToPGVectorString(newEmb)
		_, err = m.db.ExecContext(ctx,
			`UPDATE memories SET embedding = $1::vector WHERE id = $2`,
			vecStr, uid)
		if err != nil {
			return fmt.Errorf("set embedding: %w", err)
		}
	}

	// On SQLite, update in-memory vector index.
	if newEmb != nil && m.dialect == DialectSQLite && m.vecIndex != nil {
		m.vecIndex.add(uid.String(), newEmb)
	}

	return nil
}

func (m *teamMemoryStore) Delete(ctx context.Context, id string) error {
	uid, err := uuid.Parse(id)
	if err != nil {
		return fmt.Errorf("invalid memory ID: %w", err)
	}
	// Remove from in-memory vector index on SQLite.
	if m.dialect == DialectSQLite && m.vecIndex != nil {
		m.vecIndex.remove(uid.String())
	}
	return m.client.Memory.DeleteOneID(uid).Exec(ctx)
}

func (m *teamMemoryStore) List(ctx context.Context, category string, limit, offset int) ([]store.MemorySearchResult, error) {
	q := m.client.Memory.Query().
		Limit(limit).
		Offset(offset).
		Order(memory.ByCreatedAt())

	if category != "" {
		q = q.Where(memory.CategoryEQ(category))
	}

	ents, err := q.All(ctx)
	if err != nil {
		return nil, fmt.Errorf("list memories: %w", err)
	}

	results := make([]store.MemorySearchResult, 0, len(ents))
	for _, e := range ents {
		results = append(results, entMemoryToResult(e))
	}
	return results, nil
}

func (m *teamMemoryStore) ListBySession(ctx context.Context, sessionID string) ([]store.MemorySearchResult, error) {
	sid, err := uuid.Parse(sessionID)
	if err != nil {
		return nil, fmt.Errorf("invalid session ID: %w", err)
	}

	ents, err := m.client.Memory.Query().
		Where(memory.SessionIDEQ(sid)).
		Order(memory.ByCreatedAt()).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("list by session: %w", err)
	}

	results := make([]store.MemorySearchResult, 0, len(ents))
	for _, e := range ents {
		results = append(results, entMemoryToResult(e))
	}
	return results, nil
}

func (m *teamMemoryStore) Count() int {
	n, err := m.client.Memory.Query().Count(context.Background())
	if err != nil {
		return 0
	}
	return n
}

func (m *teamMemoryStore) Close() error {
	return nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// entMemoryToResult converts a team Memory entity to store.MemorySearchResult.
func entMemoryToResult(e *teament.Memory) store.MemorySearchResult {
	r := store.MemorySearchResult{
		ID:        e.ID.String(),
		Snippet:   e.ChunkText,
		Scope:     "team",
		CreatedAt: e.CreatedAt.Format(time.RFC3339),
	}
	if e.Category != nil {
		r.Category = *e.Category
	}
	if e.SourcePath != nil {
		r.Path = *e.SourcePath
	}
	if e.CreatedBy != nil {
		r.CreatedBy = e.CreatedBy.String()
	}
	if e.SessionID != nil {
		r.SessionID = e.SessionID.String()
	}
	return r
}

// mergeMemoryResults combines vector and keyword search results, deduplicating
// by ID and keeping the higher score. Results are sorted by score descending.
func mergeMemoryResults(vector, keyword []store.MemorySearchResult, maxResults int) []store.MemorySearchResult {
	seen := make(map[string]int) // ID → index in merged
	var merged []store.MemorySearchResult

	// Add vector results first (cosine similarity scores 0–1).
	for _, r := range vector {
		seen[r.ID] = len(merged)
		merged = append(merged, r)
	}

	// Add keyword results, dedup by ID (keep higher score).
	for _, r := range keyword {
		if idx, exists := seen[r.ID]; exists {
			if r.Score > merged[idx].Score {
				merged[idx].Score = r.Score
			}
		} else {
			seen[r.ID] = len(merged)
			merged = append(merged, r)
		}
	}

	// Sort by score descending with a stable identity tie-breaker.
	sortMemoryResults(merged)

	if len(merged) > maxResults {
		merged = merged[:maxResults]
	}
	return merged
}

// tsvectorWordRe matches contiguous alphanumeric/hyphen tokens for tsquery
// construction. Hyphens are kept so identifiers like "pve-prod-01" stay intact.
var tsvectorWordRe = regexp.MustCompile(`[a-zA-Z0-9][\w-]*`)

// buildORTsquery constructs a tsquery string with OR semantics from natural
// language text. Example: "Proxmox server pve-prod-01" → uses
// websearch_to_tsquery which implicitly ORs unquoted words when we reformat.
//
// Instead of relying on plainto_tsquery (AND semantics) we extract meaningful
// tokens and join them with " or " for websearch_to_tsquery, which produces
// OR-connected lexemes. This ensures that a document matching ANY of the
// query terms is returned.
func buildORTsquery(text string) string {
	words := tsvectorWordRe.FindAllString(text, -1)
	// Filter out very short tokens (1 char) and common stop words.
	var filtered []string
	for _, w := range words {
		if len(w) <= 1 {
			continue
		}
		filtered = append(filtered, strings.ToLower(w))
	}
	if len(filtered) == 0 {
		return ""
	}
	// Deduplicate.
	seen := make(map[string]struct{})
	var unique []string
	for _, w := range filtered {
		if _, ok := seen[w]; !ok {
			seen[w] = struct{}{}
			unique = append(unique, w)
		}
	}
	// Join with " or " for websearch_to_tsquery syntax.
	return strings.Join(unique, " or ")
}
