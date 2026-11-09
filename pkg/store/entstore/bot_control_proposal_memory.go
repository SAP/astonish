package entstore

import (
	"context"
	"encoding/json"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"strings"
	"time"
)

// Bind the source contents to the saved review, not the later confirmation.
func prepareControlMemory(ctx context.Context, db botDraftDB, rec *botStoredRecord, payload *store.BotControlPayload) error {
	q := payload.Memory
	if q == nil {
		return nil
	}
	copy := *q
	payload.Memory = &copy
	q = &copy
	if q.ExpectedRevision != rec.Bot.Revision {
		return store.ErrBotConflict
	}
	if payload.Operation == store.BotControlMemoryRemember {
		if q.MemoryID != "" || q.ExpectedFingerprint != "" {
			return store.ErrBotInvalid
		}
	} else {
		if _, err := uuid.Parse(q.MemoryID); err != nil {
			return store.ErrBotInvalid
		}
		row, err := db.ReadCorrectionMemory(ctx, q.MemoryID)
		if err != nil {
			return err
		}
		if row == nil {
			return store.ErrBotNotFound
		}
		if err = lockReviewedSession(ctx, db, rec.Bot.ID, row.SessionID); err != nil {
			return err
		}
		if err = validateMemoryAttribution(ctx, db, row.SessionID, row.Attribution); err != nil {
			return err
		}
		if row.Attribution != nil && (row.Attribution.BotID != rec.Bot.ID || row.Attribution.SupersededBy != "") {
			return store.ErrBotConflict
		}
		fingerprint := store.MemoryContentFingerprint(*row)
		if q.ExpectedFingerprint != "" && q.ExpectedFingerprint != fingerprint {
			return store.ErrBotConflict
		}
		q.ExpectedFingerprint = fingerprint
	}
	if payload.Operation == store.BotControlMemoryForget {
		if q.Content != "" || q.Category != "" {
			return store.ErrBotInvalid
		}
	} else if strings.TrimSpace(q.Content) == "" || len(q.Content) > 65536 {
		return store.ErrBotInvalid
	}
	return reviewedDocumentCategory(store.BotMemoryReview{}, q.Category)
}

type botControlMemoryDB interface {
	controlMemoryMutation(context.Context, store.BotControlProposal, uuid.UUID, uuid.UUID, time.Time) error
}

func (d personalBotDB) controlMemoryMutation(ctx context.Context, p store.BotControlProposal, session, actor uuid.UUID, now time.Time) error {
	return mutateControlMemory(ctx, d, p, session, actor, now)
}
func (d teamBotDB) controlMemoryMutation(ctx context.Context, p store.BotControlProposal, session, actor uuid.UUID, now time.Time) error {
	return mutateControlMemory(ctx, d, p, session, actor, now)
}
func mutateControlMemory(ctx context.Context, db botDraftDB, p store.BotControlProposal, session, actor uuid.UUID, now time.Time) error {
	q := p.Payload.Memory
	if err := lockReviewedSession(ctx, db, p.BotID, session.String()); err != nil {
		return err
	}
	var sources []store.MemorySearchResult
	if p.Payload.Operation != store.BotControlMemoryRemember {
		row, err := db.ReadCorrectionMemory(ctx, q.MemoryID)
		if err != nil {
			return err
		}
		if row == nil {
			return store.ErrBotNotFound
		}
		if err = lockReviewedSession(ctx, db, p.BotID, row.SessionID); err != nil {
			return err
		}
		row, err = lockReviewedSource(ctx, db, store.BotMemoryReviewSource{ID: q.MemoryID, SessionID: row.SessionID, ExpectedFingerprint: q.ExpectedFingerprint})
		if err != nil {
			return err
		}
		if err = validateMemoryAttribution(ctx, db, row.SessionID, row.Attribution); err != nil {
			return err
		}
		if row.Attribution != nil && (row.Attribution.BotID != p.BotID || row.Attribution.SupersededBy != "") {
			return store.ErrBotConflict
		}
		sources = append(sources, *row)
	}
	key := controlProposalKey(p.ID)
	attributes := map[string]any{"proposal_id": p.ID.String(), "source_records": sources, "source_user_id": p.SourceUserID.String(), "reviewed_by": actor.String()}
	if routine, _ := ctx.Value(routineMutationKey{}).(bool); routine {
		key = "routine-operation:" + p.ID.String()
		delete(attributes, "reviewed_by")
		delete(attributes, "proposal_id")
		attributes["operation_id"] = p.ID.String()
		attributes["authorization"] = "mandate"
	}
	if p.Payload.Operation != store.BotControlMemoryForget {
		metadata, err := store.EncodeMemoryAttribution(attributes, &store.MemoryAttribution{BotID: p.BotID, Source: key, ObservedAt: now, Status: store.MemoryKnowledgeInferred})
		if err != nil {
			return err
		}
		// No external embeddings or dialect-specific vector/tsv writes. SQLite
		// FTS and PostgreSQL's generated tsv follow chunk_text in the database.
		if err = insertControlMemory(ctx, db, uuid.NewSHA1(p.ID, []byte("memory")), session, actor, q.Content, q.Category, metadata, now); err != nil {
			return err
		}
	}
	for _, row := range sources {
		if err := supersedeReviewedSource(ctx, db, row, key, now, p.BotID); err != nil {
			return err
		}
	}
	return nil
}
func insertControlMemory(ctx context.Context, db botDraftDB, id, session, actor uuid.UUID, content, category string, metadata map[string]any, now time.Time) error {
	switch d := db.(type) {
	case personalBotDB:
		_, err := d.client.Memory.Create().SetID(id).SetSessionID(session).SetCreatedBy(actor).SetCreatedAt(now).SetChunkText(content).SetNillableCategory(nilStrPtr(category)).SetMetadata(metadata).Save(ctx)
		return err
	case teamBotDB:
		_, err := d.client.Memory.Create().SetID(id).SetSessionID(session).SetCreatedBy(actor).SetCreatedAt(now).SetChunkText(content).SetNillableCategory(nilStrPtr(category)).SetMetadata(metadata).Save(ctx)
		return err
	default:
		return store.ErrBotUnsupported
	}
}

func (r *botRepository) applyControlMemoryTx(ctx context.Context, tx botDraftDB, rec *botStoredRecord, p store.BotControlProposal, actor uuid.UUID) error {
	q := p.Payload.Memory
	if q == nil || q.ExpectedRevision != rec.Bot.Revision {
		return store.ErrBotConflict
	}
	if p.Payload.Operation != store.BotControlMemoryRemember && q.ExpectedFingerprint == "" {
		return store.ErrBotConflict
	}
	db, ok := tx.(botControlMemoryDB)
	if !ok {
		return store.ErrBotUnsupported
	}
	cas, ok := tx.(botControlVersionDB)
	if !ok {
		return store.ErrBotUnsupported
	}
	source, err := tx.ReadRun(ctx, p.BotID, p.SourceRunID)
	if err != nil {
		return err
	}
	if err = cas.updateControlVersion(ctx, *rec, rec.Bot.Version, r.clock()); err != nil {
		return err
	}
	if err = db.controlMemoryMutation(ctx, p, source.Run.SessionID, actor, r.clock()); err != nil {
		return err
	}
	if err = verifyControlMemoryMutation(ctx, tx, p); err != nil {
		return err
	}
	activity := map[string]any{"operation": p.Payload.Operation, "proposal_id": p.ID, "source_user_id": p.SourceUserID, "actor_id": actor}
	if routine, _ := ctx.Value(routineMutationKey{}).(bool); routine {
		delete(activity, "proposal_id")
		delete(activity, "actor_id")
		delete(activity, "source_user_id")
		activity["operation_id"] = p.ID
		activity["authorization"] = "mandate"
	}
	data, err := json.Marshal(activity)
	if err != nil {
		return err
	}
	return tx.InsertActivity(ctx, store.BotActivity{BotID: p.BotID, Sequence: rec.LastSequence + 1, Kind: store.BotActivityMemoryReview, Payload: data, CreatedAt: r.clock()}, "control-memory:"+p.ID.String())
}
