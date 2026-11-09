package entstore

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	personalent "github.com/SAP/astonish/ent/personal"
	personalbot "github.com/SAP/astonish/ent/personal/bot"
	personalmemory "github.com/SAP/astonish/ent/personal/memory"
	personalsession "github.com/SAP/astonish/ent/personal/session"
	teament "github.com/SAP/astonish/ent/team"
	teambot "github.com/SAP/astonish/ent/team/bot"
	teammemory "github.com/SAP/astonish/ent/team/memory"
	teamsession "github.com/SAP/astonish/ent/team/session"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

var _ store.BotMemoryReviewStore = (*personalBotStore)(nil)
var _ store.BotMemoryReviewStore = (*teamBotStore)(nil)

func (s *personalBotStore) ApplyReviewedBotMemory(ctx context.Context, memories store.MemoryStore, id store.BotID, q store.BotMemoryReview) (*store.BotMemoryReviewReceipt, error) {
	m, ok := memories.(*personalMemoryStore)
	if !ok || m.client != s.client {
		return nil, store.ErrBotUnsupported
	}
	var result *store.BotMemoryReviewReceipt
	var applied bool
	embeddings, err := prepareReviewedMemory(ctx, s.repository(), id, q, m.embedFunc)
	if err != nil {
		return nil, err
	}
	err = personalBotMemoryTransaction(ctx, m, func(c *personalent.Client, exec botMemorySQLExec) error {
		// The PostgreSQL bridge must carry receipt hooks as well as memory hooks.
		if c != m.client && m.dialect == DialectPostgres {
			c.BotOutbox.Use(m.client.BotOutbox.Hooks()...)
		}
		var err error
		result, err = applyReviewedMemory(ctx, s.repository(), personalBotDB{client: c}, id, q, embeddings, m.dialect, exec, &applied)
		return err
	})
	if err != nil {
		return nil, err
	}
	if applied {
		commitReviewedCache(m.vecIndex, m.dialect, result, embeddings)
	}
	return result, nil
}
func (s *teamBotStore) ApplyReviewedBotMemory(ctx context.Context, memories store.MemoryStore, id store.BotID, q store.BotMemoryReview) (*store.BotMemoryReviewReceipt, error) {
	m, ok := memories.(*teamMemoryStore)
	if !ok || m.client != s.client {
		return nil, store.ErrBotUnsupported
	}
	var result *store.BotMemoryReviewReceipt
	var applied bool
	embeddings, err := prepareReviewedMemory(ctx, s.repository(), id, q, m.embedFunc)
	if err != nil {
		return nil, err
	}
	err = teamBotMemoryTransaction(ctx, m, func(c *teament.Client, exec botMemorySQLExec) error {
		if c != m.client && m.dialect == DialectPostgres {
			c.BotOutbox.Use(m.client.BotOutbox.Hooks()...)
		}
		var err error
		result, err = applyReviewedMemory(ctx, s.repository(), teamBotDB{client: c}, id, q, embeddings, m.dialect, exec, &applied)
		return err
	})
	if err != nil {
		return nil, err
	}
	if applied {
		commitReviewedCache(m.vecIndex, m.dialect, result, embeddings)
	}
	return result, nil
}
func prepareReviewedMemory(ctx context.Context, r *botRepository, id store.BotID, q store.BotMemoryReview, embed store.EmbedFunc) ([][]float32, error) {
	if err := q.Validate(); err != nil {
		return nil, err
	}
	if _, err := r.AuthorizeBotMemoryWrite(ctx, id); err != nil {
		return nil, err
	}
	embeddings := make([][]float32, len(q.Edits))
	for i, e := range q.Edits {
		if embed != nil {
			var err error
			embeddings[i], err = embed(ctx, e.Content)
			if err != nil {
				return nil, err
			}
		}
	}
	return embeddings, ctx.Err()
}
func commitReviewedCache(index *vectorIndex, d Dialect, receipt *store.BotMemoryReviewReceipt, embeddings [][]float32) {
	// Update the in-process vector cache only after commit, never on replay.
	if index == nil || d != DialectSQLite {
		return
	}
	for i, id := range receipt.ReplacementIDs {
		if embeddings[i] != nil {
			index.add(id, embeddings[i])
		}
	}
}

// One transaction binds current authority, Bot revision, session linkage, source
// contents, replacement rows and receipt. No source content/evidence is deleted.
func applyReviewedMemory(ctx context.Context, r *botRepository, db botDraftDB, id store.BotID, q store.BotMemoryReview, embeddings [][]float32, dialect Dialect, exec botMemorySQLExec, applied *bool) (*store.BotMemoryReviewReceipt, error) {
	rec, err := db.GetBot(ctx, id)
	if err != nil {
		return nil, err
	}
	actor, err := r.auth.authorize(ctx, rec.Bot.OwnerID, true)
	if err != nil {
		return nil, err
	}
	key := "memory-review:" + q.ID.String()
	prior, err := db.FindActivity(ctx, id, key)
	if err == nil {
		var receipt store.BotMemoryReviewReceipt
		if json.Unmarshal(prior.Payload, &receipt) != nil || receipt.RequestFingerprint != q.Fingerprint() || receipt.ActorID != actor {
			return nil, store.ErrBotConflict
		}
		return &receipt, nil
	}
	if !errors.Is(err, store.ErrBotNotFound) {
		return nil, err
	}
	if rec.Bot.Revision != q.ExpectedRevision {
		return nil, store.ErrBotConflict
	}
	now := r.clock()
	var n int
	switch db := db.(type) {
	case personalBotDB:
		n, err = db.client.Bot.Update().Where(personalbot.IDEQ(id), personalbot.RevisionEQ(q.ExpectedRevision), personalbot.LastSeqEQ(rec.LastSequence)).AddRevision(1).AddLastSeq(1).SetUpdatedAt(now).Save(ctx)
	case teamBotDB:
		n, err = db.client.Bot.Update().Where(teambot.IDEQ(id), teambot.RevisionEQ(q.ExpectedRevision), teambot.LastSeqEQ(rec.LastSequence)).AddRevision(1).AddLastSeq(1).SetUpdatedAt(now).Save(ctx)
	default:
		return nil, store.ErrBotUnsupported
	}
	if err != nil {
		return nil, err
	}
	if n != 1 {
		return nil, store.ErrBotConflict
	}
	receipt := &store.BotMemoryReviewReceipt{ID: q.ID, BotID: id, Revision: q.ExpectedRevision + 1, ActorID: actor, RequestFingerprint: q.Fingerprint(), Reason: q.Reason, Sources: []store.MemorySearchResult{}, ReplacementIDs: []string{}}
	for i, e := range q.Edits {
		if err := lockReviewedSession(ctx, db, id, e.SessionID); err != nil {
			return nil, err
		}
		sources := make([]store.MemorySearchResult, 0, len(e.Sources))
		for _, source := range e.Sources {
			if err := lockReviewedSession(ctx, db, id, source.SessionID); err != nil {
				return nil, err
			}
			row, err := lockReviewedSource(ctx, db, source)
			if err != nil {
				return nil, err
			}
			if err := validateMemoryAttribution(ctx, db, row.SessionID, row.Attribution); err != nil {
				return nil, err
			}
			if row.Attribution != nil && (row.Attribution.BotID != id || row.Attribution.SupersededBy != "") {
				return nil, store.ErrBotConflict
			}
			if err := reviewedDocumentMember(q, *row); err != nil {
				return nil, err
			}
			sources = append(sources, *row)
		}
		if err := reviewedDocumentCategory(q, e.Category); err != nil {
			return nil, err
		}
		replacement := uuid.New()
		metadata, err := store.EncodeMemoryAttribution(map[string]any{"review_id": q.ID.String(), "source_records": sources}, &store.MemoryAttribution{BotID: id, Source: key, ObservedAt: now, Status: store.MemoryKnowledgeInferred})
		if err != nil {
			return nil, err
		}
		createdAt := now
		if q.Kind == store.BotMemoryReviewDocument && strings.HasPrefix(q.Document, "notes/") {
			createdAt, err = time.Parse("2006-01-02", strings.TrimSuffix(strings.TrimPrefix(q.Document, "notes/"), ".md"))
			if err != nil {
				return nil, store.ErrBotInvalid
			}
		}
		if err := insertReviewedMemory(ctx, db, replacement, uuid.MustParse(e.SessionID), actor, e, metadata, embeddings[i], dialect, exec, createdAt); err != nil {
			return nil, err
		}
		for _, row := range sources {
			if err := supersedeReviewedSource(ctx, db, row, key, now, id); err != nil {
				return nil, err
			}
		}
		receipt.Sources = append(receipt.Sources, sources...)
		receipt.ReplacementIDs = append(receipt.ReplacementIDs, replacement.String())
	}
	payload, err := json.Marshal(receipt)
	if err != nil {
		return nil, err
	}
	if err := db.InsertActivity(ctx, store.BotActivity{BotID: id, Sequence: rec.LastSequence + 1, Kind: store.BotActivityMemoryReview, Payload: payload, CreatedAt: now}, key); err != nil {
		return nil, err
	}
	if _, err := r.auth.authorize(ctx, rec.Bot.OwnerID, true); err != nil {
		return nil, err
	}
	*applied = true
	return receipt, nil
}
func lockReviewedSession(ctx context.Context, db botDraftDB, id uuid.UUID, session string) error {
	sid, err := uuid.Parse(session)
	if err != nil {
		return store.ErrBotInvalid
	}
	var n int
	switch db := db.(type) {
	case personalBotDB:
		n, err = db.client.Session.Update().Where(personalsession.IDEQ(sid.String()), personalsession.BotIDEQ(id)).SetBotID(id).Save(ctx)
	case teamBotDB:
		n, err = db.client.Session.Update().Where(teamsession.IDEQ(sid.String()), teamsession.BotIDEQ(id)).SetBotID(id).Save(ctx)
	}
	if err != nil {
		return err
	}
	if n != 1 {
		return store.ErrBotForbidden
	}
	return nil
}
func lockReviewedSource(ctx context.Context, db botDraftDB, s store.BotMemoryReviewSource) (*store.MemorySearchResult, error) {
	// Lock before reading: a content-only predicate cannot detect concurrent changes
	// to evidence/category. The subsequent fingerprint covers those fields too.
	id := uuid.MustParse(s.ID)
	sid := uuid.MustParse(s.SessionID)
	var n int
	var err error
	switch db := db.(type) {
	case personalBotDB:
		n, err = db.client.Memory.Update().Where(personalmemory.IDEQ(id), personalmemory.SessionIDEQ(sid)).SetSessionID(sid).Save(ctx)
	case teamBotDB:
		n, err = db.client.Memory.Update().Where(teammemory.IDEQ(id), teammemory.SessionIDEQ(sid)).SetSessionID(sid).Save(ctx)
	}
	if err != nil {
		return nil, err
	}
	if n != 1 {
		return nil, store.ErrBotForbidden
	}
	row, err := db.ReadCorrectionMemory(ctx, s.ID)
	if err != nil {
		return nil, err
	}
	if row == nil || row.SessionID != s.SessionID || store.MemoryContentFingerprint(*row) != s.ExpectedFingerprint {
		return nil, store.ErrBotConflict
	}
	return row, nil
}
func insertReviewedMemory(ctx context.Context, db botDraftDB, id, sid, actor uuid.UUID, e store.BotMemoryReviewEdit, metadata map[string]any, embedding []float32, d Dialect, exec botMemorySQLExec, createdAt time.Time) error {
	var err error
	switch db := db.(type) {
	case personalBotDB:
		c := db.client.Memory.Create().SetID(id).SetSessionID(sid).SetCreatedBy(actor).SetCreatedAt(createdAt).SetChunkText(e.Content).SetNillableCategory(nilStrPtr(e.Category)).SetMetadata(metadata)
		if d == DialectSQLite {
			c.SetTsv(e.Content)
			if embedding != nil {
				c.SetEmbedding(float32SliceToBytes(embedding))
			}
		}
		_, err = c.Save(ctx)
	case teamBotDB:
		c := db.client.Memory.Create().SetID(id).SetSessionID(sid).SetCreatedBy(actor).SetCreatedAt(createdAt).SetChunkText(e.Content).SetNillableCategory(nilStrPtr(e.Category)).SetMetadata(metadata)
		if d == DialectSQLite {
			c.SetTsv(e.Content)
			if embedding != nil {
				c.SetEmbedding(float32SliceToBytes(embedding))
			}
		}
		_, err = c.Save(ctx)
	}
	if err == nil && d == DialectPostgres && embedding != nil {
		_, err = exec(ctx, "UPDATE memories SET embedding = $1::vector WHERE id = $2 AND session_id = $3", float32SliceToPGVectorString(embedding), id, sid)
	}
	return err
}
func supersedeReviewedSource(ctx context.Context, db botDraftDB, row store.MemorySearchResult, key string, now time.Time, botID uuid.UUID) error {
	id := uuid.MustParse(row.ID)
	// Legacy records gain only inferred origin, never owner-confirmed knowledge.
	update := func(metadata map[string]any) (map[string]any, error) {
		if row.Attribution == nil {
			return store.EncodeMemoryAttribution(metadata, &store.MemoryAttribution{BotID: botID, Source: "legacy-memory:" + row.ID, ObservedAt: now, Status: store.MemoryKnowledgeInferred, SupersededBy: key})
		}
		return store.SupersedeMemoryAttribution(metadata, key)
	}
	switch db := db.(type) {
	case personalBotDB:
		old, err := db.client.Memory.Get(ctx, id)
		if err != nil {
			return err
		}
		metadata, err := update(old.Metadata)
		if err != nil {
			return err
		}
		return db.client.Memory.UpdateOneID(id).SetMetadata(metadata).Exec(ctx)
	case teamBotDB:
		old, err := db.client.Memory.Get(ctx, id)
		if err != nil {
			return err
		}
		metadata, err := update(old.Metadata)
		if err != nil {
			return err
		}
		return db.client.Memory.UpdateOneID(id).SetMetadata(metadata).Exec(ctx)
	}
	return store.ErrBotUnsupported
}
func reviewedDocumentCategory(q store.BotMemoryReview, category string) error {
	switch strings.ToLower(strings.TrimSpace(category)) {
	case "credential", "credentials", "secret", "secrets", "token", "tokens", "password", "passwords":
		return store.ErrBotForbidden
	}
	if q.Kind != store.BotMemoryReviewDocument {
		return nil
	}
	switch q.Document {
	case "MEMORY.md":
		return nil
	case "people/INDEX.md":
		if category == "person" || category == "people" {
			return nil
		}
		return store.ErrBotInvalid
	default:
		date := strings.TrimSuffix(strings.TrimPrefix(q.Document, "notes/"), ".md")
		if _, err := time.Parse("2006-01-02", date); err == nil && q.Document == "notes/"+date+".md" {
			return nil
		}
	}
	return store.ErrBotInvalid
}
func reviewedDocumentMember(q store.BotMemoryReview, row store.MemorySearchResult) error {
	if err := reviewedDocumentCategory(q, row.Category); err != nil {
		return err
	}
	if q.Kind == store.BotMemoryReviewDocument && strings.HasPrefix(q.Document, "notes/") {
		created, err := time.Parse(time.RFC3339, row.CreatedAt)
		if err != nil || q.Document != "notes/"+created.UTC().Format("2006-01-02")+".md" {
			return store.ErrBotConflict
		}
	}
	return nil
}
