package entstore

import (
	"context"
	"github.com/SAP/astonish/pkg/store"
)

// ReadBotPublication is a read-only exact receipt lookup for file delivery. It
// deliberately does not publish/reconcile, hydrate tool transcripts, or select
// the latest mutable report when an earlier sequence was requested.
func (s *personalBotStore) ReadBotPublication(ctx context.Context, bot store.BotID, run store.RunID, sequence int64) (*store.BotPublication, error) {
	return s.repository().ReadBotPublication(ctx, bot, run, sequence)
}
func (s *teamBotStore) ReadBotPublication(ctx context.Context, bot store.BotID, run store.RunID, sequence int64) (*store.BotPublication, error) {
	return s.repository().ReadBotPublication(ctx, bot, run, sequence)
}
func (r *botRepository) ReadBotPublication(ctx context.Context, bot store.BotID, run store.RunID, sequence int64) (*store.BotPublication, error) {
	b, err := r.Get(ctx, bot)
	if err != nil {
		return nil, err
	}
	if b.PermanentDelete {
		return nil, store.ErrBotNotFound
	}
	if sequence < 1 {
		return nil, store.ErrBotInvalid
	}
	pub, err := readPublication(ctx, r.db, bot, run, sequence)
	if err != nil {
		return nil, err
	}
	if pub == nil || pub.BotID != bot || pub.RunID != run || pub.ReportSequence != sequence || pub.HomeSessionID != b.HomeSessionID {
		return nil, store.ErrBotNotFound
	}
	// Never promote an old unbound receipt. The exact immutable source must match.
	if pub.EmployeeSpeech {
		source, exact, err := committedPublicationReport(ctx, r.db, bot, run, sequence)
		if err != nil {
			return nil, err
		}
		stored, err := r.db.ReadRun(ctx, bot, run)
		if err != nil {
			return nil, err
		}
		var presentation *store.BotEmployeePresentation
		if p := stored.EmployeePresentation; p != nil && p.ReportSequence == sequence {
			presentation = p
		}
		expected, speech := store.EmployeeSpeech(source, stored.EmployeeOutcomes[sequence].Speech, presentation)
		if !speech {
			return nil, store.ErrBotInvalid
		}
		if exact != sequence || pub.Version != source.Version || pub.SourceSessionID != source.SessionID || pub.SourceUserEventID != source.UserEventID || pub.ReportDigest != source.Report.EmployeeMessageSourceDigest(source.Status) || pub.EventID != store.BotPublicationEventID(run, sequence) || pub.Text == "" || pub.Text != expected {
			return nil, store.ErrBotInvalid
		}
	}
	return pub, nil
}
