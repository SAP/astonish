package entstore

import (
	"context"
	teament "github.com/SAP/astonish/ent/team"
	bot "github.com/SAP/astonish/ent/team/bot"
	kb "github.com/SAP/astonish/ent/team/botknowledgeextraction"
	kh "github.com/SAP/astonish/ent/team/botknowledgefilehead"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"time"
)

var _ store.BotKnowledgeExtractionStore = (*teamBotStore)(nil)
var _ botKnowledgeDB = teamBotDB{}

func (s *teamBotStore) EnqueueBotKnowledgeExtraction(c context.Context, i store.BotKnowledgeExtractionInput) (*store.BotKnowledgeExtraction, error) {
	return s.repository().EnqueueBotKnowledgeExtraction(c, i)
}
func (s *teamBotStore) ReadBotKnowledgeExtraction(c context.Context, i store.BotKnowledgeExtractionSource) (*store.BotKnowledgeExtraction, error) {
	return s.repository().ReadBotKnowledgeExtraction(c, i)
}
func (s *teamBotStore) ClaimBotKnowledgeExtraction(c context.Context, i store.BotKnowledgeExtractionSource, w string, t time.Duration) (store.BotKnowledgeExtractionLease, error) {
	return s.repository().ClaimBotKnowledgeExtraction(c, i, w, t)
}
func (s *teamBotStore) BeginBotKnowledgeExtraction(c context.Context, l store.BotKnowledgeExtractionLease, v int64) (*store.BotKnowledgeExtraction, bool, error) {
	return s.repository().BeginBotKnowledgeExtraction(c, l, v)
}
func (s *teamBotStore) TransitionBotKnowledgeExtraction(c context.Context, q store.BotKnowledgeExtractionTransition) (*store.BotKnowledgeExtraction, error) {
	return s.repository().TransitionBotKnowledgeExtraction(c, q)
}
func (d teamBotDB) lockKnowledgeRoot(c context.Context, id uuid.UUID) error {
	n, e := d.client.Bot.Update().Where(bot.IDEQ(id)).AddRevision(0).Save(c)
	if e != nil {
		return teamBotError(e)
	}
	if n != 1 {
		return store.ErrBotNotFound
	}
	return nil
}
func (d teamBotDB) decodeTeamKnowledge(c context.Context, row *teament.BotKnowledgeExtraction) (*knowledgeEnvelope, error) {
	v, e := d.contentCodec().decodeKnowledge(c, row.BotID, row.ID, row.Data)
	if e != nil {
		return nil, e
	}
	s := v.Record.Input.Source
	if v.Record.ID != row.ID || s.BotID != row.BotID || s.RunID != row.RunID || s.OutcomeDigest != row.OutcomeDigest || s.Sequence != row.SourceSequence || s.Version != row.SourceVersion || v.Record.Revision != row.Revision || v.Record.State != store.BotKnowledgeExtractionState(row.State) || !validKnowledgeRetainedInput(v.Record) || v.Lease.Fence != row.Fence || v.Lease.WorkerID != row.WorkerID {
		return nil, store.ErrBotInvalid
	}
	if row.LeaseExpiresAt != nil && !v.Lease.ExpiresAt.Equal(*row.LeaseExpiresAt) {
		return nil, store.ErrBotInvalid
	}
	return v, nil
}
func (d teamBotDB) readKnowledge(c context.Context, b, id uuid.UUID) (*knowledgeEnvelope, error) {
	v, e := d.client.BotKnowledgeExtraction.Query().Where(kb.BotIDEQ(b), kb.IDEQ(id)).Only(c)
	if e != nil {
		return nil, teamBotError(e)
	}
	return d.decodeTeamKnowledge(c, v)
}
func (d teamBotDB) findKnowledge(c context.Context, s store.BotKnowledgeExtractionSource) (*knowledgeEnvelope, error) {
	v, e := d.client.BotKnowledgeExtraction.Query().Where(kb.BotIDEQ(s.BotID), kb.RunIDEQ(s.RunID), kb.OutcomeDigestEQ(s.OutcomeDigest), kb.SourceSequenceEQ(s.Sequence), kb.SourceVersionEQ(s.Version)).Only(c)
	if e != nil {
		return nil, teamBotError(e)
	}
	return d.decodeTeamKnowledge(c, v)
}
func (d teamBotDB) saveKnowledge(c context.Context, v knowledgeEnvelope, expected int64) error {
	data, e := d.contentCodec().encodeKnowledge(c, v)
	if e != nil {
		return e
	}
	r := v.Record
	s := r.Input.Source
	if expected == 0 {
		_, e = d.client.BotKnowledgeExtraction.Create().SetID(r.ID).SetBotID(s.BotID).SetRunID(s.RunID).SetOutcomeDigest(s.OutcomeDigest).SetSourceSequence(s.Sequence).SetSourceVersion(s.Version).SetRevision(r.Revision).SetState(kb.State(r.State)).SetFence(v.Lease.Fence).SetWorkerID(v.Lease.WorkerID).SetData(data).Save(c)
		return teamBotError(e)
	}
	n, e := d.client.BotKnowledgeExtraction.Update().Where(kb.IDEQ(r.ID), kb.BotIDEQ(s.BotID), kb.RevisionEQ(expected)).SetRevision(r.Revision).SetState(kb.State(r.State)).SetFence(v.Lease.Fence).SetWorkerID(v.Lease.WorkerID).SetLeaseExpiresAt(v.Lease.ExpiresAt).SetData(data).Save(c)
	if e != nil {
		return teamBotError(e)
	}
	if n != 1 {
		return store.ErrBotConflict
	}
	return nil
}
func (d teamBotDB) commitKnowledgeHeads(c context.Context, r store.BotKnowledgeExtraction) error {
	for _, p := range r.Decision.Proposals {
		digest := skillSHA(p.Content)
		if p.ExpectedDigest == "" {
			_, e := d.client.BotKnowledgeFileHead.Create().SetBotID(r.Input.Source.BotID).SetPath(p.Path).SetDigest(digest).SetExtractionID(r.ID).Save(c)
			if e != nil {
				return teamBotError(e)
			}
			continue
		}
		n, e := d.client.BotKnowledgeFileHead.Update().Where(kh.BotIDEQ(r.Input.Source.BotID), kh.PathEQ(p.Path), kh.DigestEQ(p.ExpectedDigest)).SetDigest(digest).SetExtractionID(r.ID).AddRevision(1).Save(c)
		if e != nil {
			return teamBotError(e)
		}
		if n != 1 {
			return store.ErrBotConflict
		}
	}
	return nil
}
func (d teamBotDB) purgeKnowledge(c context.Context, b uuid.UUID) error {
	if _, e := d.client.BotKnowledgeFileHead.Delete().Where(kh.BotIDEQ(b)).Exec(c); e != nil {
		return e
	}
	_, e := d.client.BotKnowledgeExtraction.Delete().Where(kb.BotIDEQ(b)).Exec(c)
	return e
}
