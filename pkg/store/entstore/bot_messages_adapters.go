package entstore

import (
	"context"
	personalsession "github.com/SAP/astonish/ent/personal/session"
	teamsession "github.com/SAP/astonish/ent/team/session"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/session"
)

func (d personalBotDB) appendAdmittedMessage(ctx context.Context, b store.Bot, e *session.Event) (int64, error) {
	exists, err := d.client.Session.Query().Where(personalsession.IDEQ(b.HomeSessionID.String()), personalsession.BotIDEQ(b.ID), personalsession.UserIDEQ(b.OwnerID), personalsession.PurposeEQ(personalsession.PurposeBotHome)).Exist(ctx)
	if err != nil {
		return 0, err
	}
	if !exists {
		return 0, store.ErrBotForbidden
	}
	data, err := adkEventToMap(e)
	if err != nil {
		return 0, err
	}
	row, err := d.client.SessionEvent.Create().SetSessionID(b.HomeSessionID.String()).SetEventData(data).Save(ctx)
	if err != nil {
		return 0, err
	}
	if err = d.client.Session.UpdateOneID(b.HomeSessionID.String()).SetUpdatedAt(e.Timestamp).Exec(ctx); err != nil {
		return 0, err
	}
	return int64(row.ID), nil
}
func (d teamBotDB) appendAdmittedMessage(ctx context.Context, b store.Bot, e *session.Event) (int64, error) {
	exists, err := d.client.Session.Query().Where(teamsession.IDEQ(b.HomeSessionID.String()), teamsession.BotIDEQ(b.ID), teamsession.UserIDEQ(b.OwnerID), teamsession.PurposeEQ(teamsession.PurposeBotHome)).Exist(ctx)
	if err != nil {
		return 0, err
	}
	if !exists {
		return 0, store.ErrBotForbidden
	}
	data, err := adkEventToMap(e)
	if err != nil {
		return 0, err
	}
	row, err := d.client.SessionEvent.Create().SetSessionID(b.HomeSessionID.String()).SetEventData(data).Save(ctx)
	if err != nil {
		return 0, err
	}
	if err = d.client.Session.UpdateOneID(b.HomeSessionID.String()).SetUpdatedAt(e.Timestamp).Exec(ctx); err != nil {
		return 0, err
	}
	return int64(row.ID), nil
}
