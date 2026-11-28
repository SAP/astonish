package client

import (
	"context"
	"fmt"
	"github.com/SAP/astonish/pkg/store"
	"net/url"
)

// ContinueProgress resumes only the exact owner-reviewed checkpoint. It sends no
// answer and does not change the Bot lifecycle or schedule.
func (b Bots) ContinueProgress(ctx context.Context, id, runID string, revision, checkpointRevision int64, key string) (*store.BotRun, error) {
	if revision < 1 || checkpointRevision < 1 || key == "" {
		return nil, fmt.Errorf("reviewed Bot revision, checkpoint revision and idempotency key required")
	}
	var run store.BotRun
	err := b.request(ctx, "POST", b.path("/"+url.PathEscape(id)+"/runs/"+url.PathEscape(runID)+"/continue-interactive"), struct {
		Kind               string `json:"kind"`
		CheckpointRevision int64  `json:"checkpoint_revision"`
		ExpectedRevision   int64  `json:"expected_revision"`
		IdempotencyKey     string `json:"idempotency_key"`
	}{"progress_v1", checkpointRevision, revision, key}, &run)
	return &run, err
}
