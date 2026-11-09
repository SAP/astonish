package api

import (
	"context"
	"github.com/SAP/astonish/pkg/store"
)

func (s *botSessionFixtureStore) ListApprovals(context.Context, store.BotID, store.BotPage) ([]store.BotApproval, error) {
	return nil, nil
}
