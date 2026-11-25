package entstore

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	personalbot "github.com/SAP/astonish/ent/personal/bot"
	personalsession "github.com/SAP/astonish/ent/personal/session"
	teambot "github.com/SAP/astonish/ent/team/bot"
	teamsession "github.com/SAP/astonish/ent/team/session"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

var _ store.BotMemoryReviewStore = (*personalBotStore)(nil)
var _ store.BotMemoryReviewStore = (*teamBotStore)(nil)

func (s *personalBotStore) ApplyReviewedBotMemory(ctx context.Context, memories store.MemoryStore, id store.BotID, q store.BotMemoryReview) (*store.BotMemoryReviewReceipt, error) {
	m, ok := memories.(*botDiskMemoryStore)
	if !ok || m == nil || m.repo == nil || m.owner != s.client || m.id != id || memoryBackendIdentity(m.repo.db) != s.content {
		return nil, store.ErrBotUnsupported
	}
	return applyReviewedDiskMemory(ctx, s.repository(), id, q)
}
func (s *teamBotStore) ApplyReviewedBotMemory(ctx context.Context, memories store.MemoryStore, id store.BotID, q store.BotMemoryReview) (*store.BotMemoryReviewReceipt, error) {
	m, ok := memories.(*botDiskMemoryStore)
	if !ok || m == nil || m.repo == nil || m.owner != s.client || m.id != id || memoryBackendIdentity(m.repo.db) != s.content {
		return nil, store.ErrBotUnsupported
	}
	return applyReviewedDiskMemory(ctx, s.repository(), id, q)
}
func applyReviewedDiskMemory(ctx context.Context, r *botRepository, id store.BotID, q store.BotMemoryReview) (*store.BotMemoryReviewReceipt, error) {
	embeddings, err := prepareReviewedMemory(ctx, r, id, q, nil)
	if err != nil {
		return nil, err
	}
	var result *store.BotMemoryReviewReceipt
	var applied bool
	err = r.db.Transaction(ctx, func(db botDraftDB) error {
		var err error
		result, err = applyReviewedMemory(ctx, r, db, id, q, embeddings, DialectSQLite, nil, &applied)
		return err
	})
	return result, err
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
	row, err := db.ReadCorrectionMemory(ctx, s.ID)
	if err != nil {
		return nil, err
	}
	if row == nil || row.SessionID != s.SessionID {
		return nil, store.ErrBotForbidden
	}
	if store.MemoryContentFingerprint(*row) != s.ExpectedFingerprint {
		return nil, store.ErrBotConflict
	}
	return row, nil
}
func insertReviewedMemory(ctx context.Context, db botDraftDB, id, sid, actor uuid.UUID, e store.BotMemoryReviewEdit, metadata map[string]any, embedding []float32, d Dialect, exec botMemorySQLExec, createdAt time.Time) error {
	return insertBotDiskMemory(ctx, db, id, sid, actor, e.Content, e.Category, metadata, createdAt)
}
func supersedeReviewedSource(ctx context.Context, db botDraftDB, row store.MemorySearchResult, key string, now time.Time, botID uuid.UUID) error {
	// Disk corrections preserve the old row rather than replacing it in place.
	// Do not reuse the legacy correction: marker, which means current to older
	// recall consumers because that protocol rewrote the source row itself.
	if strings.HasPrefix(key, "correction:") {
		key = "superseded:" + key
	}
	return changeBotDiskMemory(ctx, db, botID, row, func(e *botDiskMemoryEntry) error {
		var err error
		if e.Row.Attribution == nil {
			e.Metadata, err = store.EncodeMemoryAttribution(e.Metadata, &store.MemoryAttribution{BotID: botID, Source: "memory:" + row.ID, ObservedAt: now, Status: store.MemoryKnowledgeInferred, SupersededBy: key})
		} else {
			e.Metadata, err = store.SupersedeMemoryAttribution(e.Metadata, key)
		}
		if err != nil {
			return err
		}
		e.Row.Attribution, err = store.DecodeMemoryAttribution(e.Metadata)
		return err
	})
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
