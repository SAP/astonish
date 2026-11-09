package entstore

import (
	"context"

	personalsession "github.com/SAP/astonish/ent/personal/session"
	teamsession "github.com/SAP/astonish/ent/team/session"
	"github.com/SAP/astonish/pkg/store"
	adksession "google.golang.org/adk/session"
)

// The adapter carries the transaction-bound client into the existing transcript
// writer. Receipt failure rolls back both the transcript and message count.
func (d personalBotDB) AppendHomeReport(ctx context.Context, b store.Bot, event *adksession.Event) error {
	exists, err := d.client.Session.Query().Where(personalsession.IDEQ(b.HomeSessionID.String()), personalsession.BotIDEQ(b.ID), personalsession.UserIDEQ(b.OwnerID), personalsession.PurposeEQ(personalsession.PurposeBotHome)).Exist(ctx)
	if err != nil {
		return err
	}
	if !exists {
		return store.ErrBotForbidden
	}
	return (&personalSessionStore{client: d.client}).appendEventInternal(ctx, b.HomeSessionID.String(), event)
}
func (d teamBotDB) AppendHomeReport(ctx context.Context, b store.Bot, event *adksession.Event) error {
	exists, err := d.client.Session.Query().Where(teamsession.IDEQ(b.HomeSessionID.String()), teamsession.BotIDEQ(b.ID), teamsession.UserIDEQ(b.OwnerID), teamsession.PurposeEQ(teamsession.PurposeBotHome)).Exist(ctx)
	if err != nil {
		return err
	}
	if !exists {
		return store.ErrBotForbidden
	}
	return (&teamSessionStore{client: d.client}).appendEventInternal(ctx, b.HomeSessionID.String(), event)
}
