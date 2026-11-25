package entstore

import (
	"context"
	personalbot "github.com/SAP/astonish/ent/personal/bot"
	teambot "github.com/SAP/astonish/ent/team/bot"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"reflect"
	"time"
)

func mutateBotDiskMemory(ctx context.Context, r *botRepository, id store.BotID, q store.BotMemoryMutation) error {
	if q.Kind != store.BotMemoryAdd && q.Kind != store.BotMemoryUpdate && q.Kind != store.BotMemoryDelete {
		return store.ErrBotInvalid
	}
	actor, err := r.AuthorizeBotMemoryWrite(ctx, id)
	if err != nil {
		return err
	}
	before, err := readBotDiskMemory(ctx, r.db, id)
	if err != nil {
		return err
	}
	// Clone entries: the baseline must remain immutable for the transaction CAS.
	next := *before
	next.Entries = append([]botDiskMemoryEntry(nil), before.Entries...)
	if q.Kind == store.BotMemoryAdd {
		metadata, err := prepareMemoryAttribution(ctx, r.db, q.Entry)
		if err != nil {
			return err
		}
		clone := make(map[string]any, len(metadata)+1)
		for k, v := range metadata {
			clone[k] = v
		}
		clone["source_path"] = q.Entry.SourcePath
		rec, err := r.db.GetBot(ctx, id)
		if err != nil {
			return err
		}
		next.Entries = append(next.Entries, botDiskMemoryEntry{Row: store.MemorySearchResult{ID: uuid.NewString(), Snippet: q.Entry.Content, Category: q.Entry.Category, SessionID: q.Entry.SessionID, CreatedBy: actor.String(), CreatedAt: r.clock().UTC().Format(time.RFC3339), Attribution: q.Entry.Attribution, Path: q.Entry.SourcePath, Scope: string(rec.Bot.Scope)}, Metadata: clone})
	} else {
		found := false
		for i := range next.Entries {
			e := &next.Entries[i]
			if e.Row.ID != q.Previous.ID {
				continue
			}
			found = true
			if store.MemoryContentFingerprint(e.Row) != store.MemoryContentFingerprint(q.Previous) || !reflect.DeepEqual(e.Row.Attribution, q.Previous.Attribution) {
				return store.ErrBotConflict
			}
			if q.Kind == store.BotMemoryDelete {
				a := store.MemoryAttribution{BotID: id, Source: "memory:" + e.Row.ID, ObservedAt: r.clock(), Status: store.MemoryKnowledgeInferred}
				if e.Row.Attribution != nil {
					a = *e.Row.Attribution
				}
				a.SupersededBy = "deleted:" + uuid.NewString()
				e.Row.Attribution = &a
			} else {
				e.Row.Snippet = q.Entry.Content
				e.Row.Category = q.Entry.Category
			}
		}
		if !found {
			return store.ErrBotNotFound
		}
	}
	ref, err := stageBotDiskMemory(ctx, r.db, &next)
	if err != nil {
		return err
	}
	return r.db.Transaction(ctx, func(db botDraftDB) error {
		_, sid, err := guardBotMemory(ctx, r, db, id, q)
		if err != nil {
			return err
		}
		rec, err := db.GetBot(ctx, id)
		if err != nil {
			return err
		}
		var n int
		switch d := db.(type) {
		case personalBotDB:
			n, err = d.client.Bot.Update().Where(personalbot.IDEQ(id), personalbot.RevisionEQ(rec.Bot.Revision), personalbot.LastSeqEQ(rec.LastSequence)).SetLastSeq(rec.LastSequence).Save(ctx)
		case teamBotDB:
			n, err = d.client.Bot.Update().Where(teambot.IDEQ(id), teambot.RevisionEQ(rec.Bot.Revision), teambot.LastSeqEQ(rec.LastSequence)).SetLastSeq(rec.LastSequence).Save(ctx)
		default:
			return store.ErrBotUnsupported
		}
		if err != nil {
			return err
		}
		if n != 1 {
			return store.ErrBotConflict
		}
		if err := lockReviewedSession(ctx, db, id, sid.String()); err != nil {
			return err
		}
		current, err := readBotDiskMemory(ctx, db, id)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(current, before) {
			return store.ErrBotConflict
		}
		if err := commitBotDiskMemory(ctx, db, id, ref); err != nil {
			return err
		}
		// Recheck after the head statement as well as after staging. Failure rolls
		// back SQL; an unreachable immutable object is never an authorized memory.
		if err := ctx.Err(); err != nil {
			return err
		}
		if lease, ok := ctx.Value(botMemoryLeaseKey{}).(store.BotLease); ok {
			_, err := r.checkLease(ctx, db, lease, r.clock())
			return err
		}
		_, err = r.auth.authorize(ctx, rec.Bot.OwnerID, true)
		return err
	})
}
