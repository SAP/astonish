package entstore

import (
	"context"

	personalent "github.com/SAP/astonish/ent/personal"
	personaloutbox "github.com/SAP/astonish/ent/personal/botoutbox"
	teament "github.com/SAP/astonish/ent/team"
	teamoutbox "github.com/SAP/astonish/ent/team/botoutbox"
	"github.com/SAP/astonish/pkg/store"
)

// ListBotPublicationActivity excludes unrelated evidence before opening retained
// bodies. It does not cache authorization or omit older report intents/receipts:
// restart reconciliation must still replay exact outcomes and pending delivery.
func (s *personalBotStore) ListBotPublicationActivity(ctx context.Context, id store.BotID, after int64, limit int) ([]store.BotActivity, error) {
	r := s.repository()
	n, err := publicationActivityPage(ctx, r, id, after, limit)
	if err != nil {
		return nil, err
	}
	rows, err := s.client.BotOutbox.Query().Where(personaloutbox.BotIDEQ(id), personaloutbox.SequenceGT(after), personaloutbox.KindEQ(string(store.BotActivityReport))).Order(personalent.Asc(personaloutbox.FieldSequence)).Limit(n).All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]store.BotActivity, 0, len(rows))
	for _, row := range rows {
		a, err := (personalBotDB{client: s.client, content: s.content}).contentCodec().decodeActivity(ctx, id, row.Sequence, store.BotActivityKind(row.Kind), row.CreatedAt, row.Payload)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, nil
}

func (s *teamBotStore) ListBotPublicationActivity(ctx context.Context, id store.BotID, after int64, limit int) ([]store.BotActivity, error) {
	r := s.repository()
	n, err := publicationActivityPage(ctx, r, id, after, limit)
	if err != nil {
		return nil, err
	}
	rows, err := s.client.BotOutbox.Query().Where(teamoutbox.BotIDEQ(id), teamoutbox.SequenceGT(after), teamoutbox.KindEQ(string(store.BotActivityReport))).Order(teament.Asc(teamoutbox.FieldSequence)).Limit(n).All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]store.BotActivity, 0, len(rows))
	for _, row := range rows {
		a, err := (teamBotDB{client: s.client, content: s.content}).contentCodec().decodeActivity(ctx, id, row.Sequence, store.BotActivityKind(row.Kind), row.CreatedAt, row.Payload)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, nil
}

func publicationActivityPage(ctx context.Context, r *botRepository, id store.BotID, after int64, limit int) (int, error) {
	if _, err := r.Get(ctx, id); err != nil {
		return 0, err
	}
	if after < 0 {
		return 0, store.ErrBotInvalid
	}
	n, _, err := botPage(store.BotPage{Limit: limit})
	return n, err
}
