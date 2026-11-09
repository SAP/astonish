package entstore

import (
	"context"
	"encoding/json"
	personal "github.com/SAP/astonish/ent/personal"
	pi "github.com/SAP/astonish/ent/personal/botintake"
	team "github.com/SAP/astonish/ent/team"
	ti "github.com/SAP/astonish/ent/team/botintake"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"strings"
)

const sourceIntakeEnvelope = "source-intake-v1:"

func sourceProposalRead(ctx context.Context, tx botDraftDB, id, owner uuid.UUID) (*store.BotSourceIntakeProposal, int64, error) {
	var raw []byte
	var hash string
	var revision int64
	switch d := tx.(type) {
	case personalBotDB:
		row, e := d.client.BotIntake.Query().Where(pi.IDEQ(id), pi.OwnerIDEQ(owner)).Only(ctx)
		if e != nil {
			return nil, 0, personalBotError(e)
		}
		raw, hash, revision = row.Content, row.MutationHash, row.Revision
	case teamBotDB:
		row, e := d.client.BotIntake.Query().Where(ti.IDEQ(id), ti.OwnerIDEQ(owner)).Only(ctx)
		if e != nil {
			return nil, 0, teamBotError(e)
		}
		raw, hash, revision = row.Content, row.MutationHash, row.Revision
	default:
		return nil, 0, store.ErrBotUnsupported
	}
	if !strings.HasPrefix(hash, sourceIntakeEnvelope) {
		return nil, 0, store.ErrBotConflict
	}
	var p store.BotSourceIntakeProposal
	if e := json.Unmarshal(raw, &p); e != nil {
		return nil, 0, e
	}
	if p.ID != id || p.OwnerID != owner {
		return nil, 0, store.ErrBotForbidden
	}
	return &p, revision, nil
}
func sourceProposalWrite(ctx context.Context, tx botDraftDB, p *store.BotSourceIntakeProposal, revision int64) error {
	raw, e := json.Marshal(p)
	if e != nil {
		return e
	}
	hash := sourceIntakeEnvelope + p.Digest
	var n int
	switch d := tx.(type) {
	case personalBotDB:
		if revision == 0 {
			_, e = d.client.BotIntake.Create().SetID(p.ID).SetOwnerID(p.OwnerID).SetRevision(1).SetContent(raw).SetMutationHash(hash).SetUpdatedAt(p.CreatedAt).Save(ctx)
			if personal.IsConstraintError(e) {
				return store.ErrBotConflict
			}
			return e
		}
		n, e = d.client.BotIntake.Update().Where(pi.IDEQ(p.ID), pi.OwnerIDEQ(p.OwnerID), pi.RevisionEQ(revision), pi.MutationHashEQ(hash)).SetContent(raw).AddRevision(1).Save(ctx)
	case teamBotDB:
		if revision == 0 {
			_, e = d.client.BotIntake.Create().SetID(p.ID).SetOwnerID(p.OwnerID).SetRevision(1).SetContent(raw).SetMutationHash(hash).SetUpdatedAt(p.CreatedAt).Save(ctx)
			if team.IsConstraintError(e) {
				return store.ErrBotConflict
			}
			return e
		}
		n, e = d.client.BotIntake.Update().Where(ti.IDEQ(p.ID), ti.OwnerIDEQ(p.OwnerID), ti.RevisionEQ(revision), ti.MutationHashEQ(hash)).SetContent(raw).AddRevision(1).Save(ctx)
	default:
		return store.ErrBotUnsupported
	}
	if e != nil {
		return e
	}
	if n != 1 {
		return store.ErrBotConflict
	}
	return nil
}
