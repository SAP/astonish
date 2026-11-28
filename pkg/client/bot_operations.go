package client

import (
	"context"
	"fmt"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"net/url"
	"time"
)

// BotOperationDecision contains only the exact public exception binding, never authority.
type BotOperationDecision struct {
	OperationID      uuid.UUID `json:"-"`
	PayloadDigest    string    `json:"payload_digest"`
	ExpectedRevision int64     `json:"expected_revision"`
	ExpiresAt        time.Time `json:"expires_at"`
	Approve          bool      `json:"approve"`
}

func (b Bots) DecideOperation(ctx context.Context, id string, q BotOperationDecision) (*store.BotOperationView, error) {
	if q.OperationID == uuid.Nil || q.PayloadDigest == "" || q.ExpectedRevision < 1 || !q.ExpiresAt.After(time.Now()) {
		return nil, fmt.Errorf("invalid or expired operation exception")
	}
	var v store.BotOperationView
	err := b.request(ctx, "POST", b.path("/"+url.PathEscape(id)+"/operations/"+q.OperationID.String()+"/decision"), q, &v)
	return &v, err
}
