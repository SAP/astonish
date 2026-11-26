package entstore

import (
	"context"

	personalent "github.com/SAP/astonish/ent/personal"
	personaloutbox "github.com/SAP/astonish/ent/personal/botoutbox"
	teament "github.com/SAP/astonish/ent/team"
	teamoutbox "github.com/SAP/astonish/ent/team/botoutbox"
	"github.com/SAP/astonish/pkg/store"
)

// controlProposalKeys selects only committed control records. It never opens
// transcript/content files. Consumers still hydrate the selected record through
// FindActivity, which checks its digest and SQL authority before exposing bytes.
func controlProposalKeys(ctx context.Context, db botDB, bot store.BotID, after int64) ([]string, int64, error) {
	var keys []string
	switch d := db.(type) {
	case personalBotDB:
		rows, err := d.client.BotOutbox.Query().Where(personaloutbox.BotIDEQ(bot), personaloutbox.SequenceGT(after), personaloutbox.DeduplicationKeyHasPrefix("control-proposal:")).Order(personalent.Asc(personaloutbox.FieldSequence)).Limit(200).All(ctx)
		if err != nil {
			return nil, after, err
		}
		for _, row := range rows {
			keys = append(keys, row.DeduplicationKey)
			after = row.Sequence
		}
	case teamBotDB:
		rows, err := d.client.BotOutbox.Query().Where(teamoutbox.BotIDEQ(bot), teamoutbox.SequenceGT(after), teamoutbox.DeduplicationKeyHasPrefix("control-proposal:")).Order(teament.Asc(teamoutbox.FieldSequence)).Limit(200).All(ctx)
		if err != nil {
			return nil, after, err
		}
		for _, row := range rows {
			keys = append(keys, row.DeduplicationKey)
			after = row.Sequence
		}
	default:
		return nil, after, store.ErrBotUnsupported
	}
	return keys, after, nil
}
