package client

import (
	"context"
	"fmt"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"net/url"
)

// CancelRun requests durable cancellation of one exact run, never Bot lifecycle changes.
func (b Bots) CancelRun(ctx context.Context, id, runID string) (*store.BotRun, error) {
	for _, value := range []string{id, runID} {
		parsed, err := uuid.Parse(value)
		if err != nil || parsed == uuid.Nil {
			return nil, fmt.Errorf("exact nonzero Bot and run UUIDs required")
		}
	}
	var run store.BotRun
	err := b.request(ctx, "POST", b.path("/"+url.PathEscape(id)+"/runs/"+url.PathEscape(runID)+"/cancel"), nil, &run)
	return &run, err
}
