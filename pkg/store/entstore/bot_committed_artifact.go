package entstore

import (
	"context"
	"encoding/json"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

var _ store.BotCommittedArtifactStore = (*personalBotStore)(nil)
var _ store.BotCommittedArtifactStore = (*teamBotStore)(nil)

func (s *personalBotStore) ReadCommittedArtifact(c context.Context, b store.BotID, sequence int64, id uuid.UUID) (*store.BotArtifact, error) {
	return s.repository().ReadCommittedArtifact(c, b, sequence, id)
}
func (s *teamBotStore) ReadCommittedArtifact(c context.Context, b store.BotID, sequence int64, id uuid.UUID) (*store.BotArtifact, error) {
	return s.repository().ReadCommittedArtifact(c, b, sequence, id)
}

func (s *personalBotStore) ReadFilePublication(c context.Context, b store.BotID, run store.RunID, sequence int64) (*store.BotFilePublication, error) {
	return s.repository().ReadFilePublication(c, b, run, sequence)
}
func (s *teamBotStore) ReadFilePublication(c context.Context, b store.BotID, run store.RunID, sequence int64) (*store.BotFilePublication, error) {
	return s.repository().ReadFilePublication(c, b, run, sequence)
}
func (r *botRepository) ReadFilePublication(ctx context.Context, bot store.BotID, run store.RunID, sequence int64) (*store.BotFilePublication, error) {
	if bot == uuid.Nil || run == uuid.Nil || sequence < 1 {
		return nil, store.ErrBotInvalid
	}
	b, err := r.Get(ctx, bot)
	if err != nil {
		return nil, err
	}
	if b.Status == store.BotDeleted {
		return nil, store.ErrBotNotFound
	}
	p, err := readPublication(ctx, r.db, bot, run, sequence)
	if err != nil {
		return nil, err
	}
	if p.BotID != bot || p.RunID != run || p.ReportSequence != sequence || p.HomeSessionID != b.HomeSessionID {
		return nil, store.ErrBotInvalid
	}
	envelope, err := p.FilePublication()
	if err != nil {
		return nil, err
	}
	if envelope == nil || envelope.HomeID != b.Home.ID {
		return nil, store.ErrBotNotFound
	}
	for _, a := range p.Artifacts {
		committed, err := r.ReadCommittedArtifact(ctx, bot, sequence, a.ID)
		if err != nil {
			return nil, err
		}
		// Compare all immutable fields, not just the display path or digest.
		left, _ := json.Marshal(a)
		right, _ := json.Marshal(committed)
		if string(left) != string(right) {
			return nil, store.ErrBotInvalid
		}
	}
	return envelope, nil
}

func (r *botRepository) ReadCommittedArtifact(ctx context.Context, bot store.BotID, sequence int64, id uuid.UUID) (*store.BotArtifact, error) {
	if bot == uuid.Nil || sequence < 1 || id == uuid.Nil {
		return nil, store.ErrBotInvalid
	}
	b, err := r.Get(ctx, bot)
	if err != nil {
		return nil, err
	}
	if b.Status == store.BotDeleted {
		return nil, store.ErrBotNotFound
	}
	// Scope authorization above deliberately permits team readers, not only the
	// execution owner. Select a single immutable activity, never latest/run.Report.
	rows, err := r.db.ReadActivity(ctx, bot, sequence-1, 1)
	if err != nil {
		return nil, err
	}
	if len(rows) != 1 || rows[0].Sequence != sequence || rows[0].BotID != bot || rows[0].Kind != store.BotActivityReport || rows[0].RunID == nil {
		return nil, store.ErrBotNotFound
	}
	var outcome struct {
		Intent store.BotOutboxKind `json:"intent"`
		Report store.BotReport     `json:"report"`
	}
	if err := json.Unmarshal(rows[0].Payload, &outcome); err != nil {
		return nil, err
	}
	if outcome.Intent != store.BotOutboxPublish {
		return nil, store.ErrBotNotFound
	}
	for _, artifact := range outcome.Report.Artifacts {
		if artifact.ID != id {
			continue
		}
		p := store.BotPublication{BotID: bot, RunID: *rows[0].RunID, HomeSessionID: b.HomeSessionID, ReportSequence: sequence, EventID: store.BotPublicationEventID(*rows[0].RunID, sequence), Text: outcome.Report.Summary, Artifacts: []store.BotArtifact{artifact}}
		envelope, err := p.FilePublication()
		if err != nil || envelope == nil || envelope.HomeID != b.Home.ID {
			return nil, store.ErrBotInvalid
		}
		return &artifact, nil
	}
	return nil, store.ErrBotNotFound
}
