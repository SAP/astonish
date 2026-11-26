package api

import (
	"bytes"
	"context"
	"fmt"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// listBotPresentationApprovals drains the scoped repository in durable cursor
// order. Presentation needs saved decisions as well as pending controls.
func listBotPresentationApprovals(ctx context.Context, repo store.BotStore, id store.BotID) ([]store.BotApproval, error) {
	if repo == nil || id == uuid.Nil {
		return nil, fmt.Errorf("presentation approvals: %w", store.ErrBotInvalid)
	}
	out := []store.BotApproval{}
	last := uuid.Nil
	for {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("presentation approvals: %w", err)
		}
		page := store.BotPage{Limit: 200}
		if last != uuid.Nil {
			page.After = last.String()
		}
		rows, err := repo.ListApprovals(ctx, id, page)
		if err != nil {
			return nil, fmt.Errorf("presentation approvals: %w", err)
		}
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("presentation approvals: %w", err)
		}
		if len(rows) > page.Limit {
			return nil, fmt.Errorf("presentation approval page: %w", store.ErrBotInvalid)
		}
		for _, row := range rows {
			if row.BotID != id || row.ID == uuid.Nil || bytes.Compare(row.ID[:], last[:]) <= 0 {
				return nil, fmt.Errorf("presentation approval cursor: %w", store.ErrBotInvalid)
			}
			last = row.ID
			out = append(out, row)
		}
		if len(rows) < page.Limit {
			return out, nil
		}
	}
}
