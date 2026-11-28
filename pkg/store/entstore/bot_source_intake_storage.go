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

// SQL retains lifecycle/digest coordination, never reviewed source prose.
type botSourceIntakeRecord struct {
	Content map[string]any              `json:"content"`
	Status  store.BotSourceIntakeStatus `json:"status"`
	Digest  string                      `json:"digest"`
	BotID   *uuid.UUID                  `json:"bot_id,omitempty"`
}

func sourceProposalContent(ctx context.Context, tx botDB, id, owner uuid.UUID) (*botIndependentContent, error) {
	b := &store.BotUnfinishedIntake{ID: id, OwnerID: owner}
	switch d := tx.(type) {
	case personalBotDB:
		b.Scope = store.BotScopePersonal
		return d.content.intakeContent(ctx, b)
	case teamBotDB:
		b.Scope = store.BotScopeTeam
		return d.content.intakeContent(ctx, b)
	default:
		return nil, store.ErrBotUnsupported
	}
}

func sourceProposalRead(ctx context.Context, tx botDB, id, owner uuid.UUID) (*store.BotSourceIntakeProposal, int64, error) {
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
	var data map[string]any
	if e := json.Unmarshal(raw, &data); e != nil {
		return nil, 0, errBotDevelopmentContentFormat
	}
	var record botSourceIntakeRecord
	if e := decodeBotRecord(data, &record); e != nil {
		return nil, 0, e
	}
	content, e := sourceProposalContent(ctx, tx, id, owner)
	if e != nil {
		return nil, 0, e
	}
	if e := content.decode(ctx, id, store.BotContentIntake, revision, record.Content, &p); e != nil {
		return nil, 0, e
	}
	if p.ID != id || p.OwnerID != owner {
		return nil, 0, store.ErrBotForbidden
	}
	if p.Status != record.Status || p.Digest != record.Digest || hash != sourceIntakeEnvelope+p.Digest || (p.BotID == nil) != (record.BotID == nil) || p.BotID != nil && *p.BotID != *record.BotID {
		return nil, 0, store.ErrBotConflict
	}
	return &p, revision, nil
}
func sourceProposalWrite(ctx context.Context, tx botDB, p *store.BotSourceIntakeProposal, revision int64) error {
	content, e := sourceProposalContent(ctx, tx, p.ID, p.OwnerID)
	if e != nil {
		return e
	}
	data, e := content.encode(ctx, p.ID, store.BotContentIntake, revision+1, p)
	if e != nil {
		return e
	}
	encoded, e := json.Marshal(botSourceIntakeRecord{Content: data, Status: p.Status, Digest: p.Digest, BotID: p.BotID})
	if e != nil {
		return e
	}
	raw, e := json.Marshal(map[string]any{"bot_record_v2": string(encoded)})
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
