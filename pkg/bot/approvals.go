package bot

import (
	"bytes"
	"context"
	"encoding/json"
	"io"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

const ApprovalCheckpointFormat = "exact-action-report-v1"
const InteractionCheckpointFormat = "user-decision-report-v1"

// ApprovalCheckpoint binds either one exact action or a typed decision to a run.
// Decision continuations report the saved answer; they never grant tool authority.
type ApprovalCheckpoint struct {
	Format       string    `json:"format"`
	ApprovalID   uuid.UUID `json:"approval_id"`
	BotID        uuid.UUID `json:"bot_id"`
	RunID        uuid.UUID `json:"run_id"`
	SessionID    uuid.UUID `json:"session_id"`
	Version      int64     `json:"version"`
	ActionDigest string    `json:"action_digest"`
	Fence        int64     `json:"fence"`
}

func DecodeApprovalCheckpoint(raw json.RawMessage, run store.BotRun) (ApprovalCheckpoint, error) {
	var c ApprovalCheckpoint
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&c); err != nil {
		return c, store.ErrBotInvalid
	}
	var extra any
	if d.Decode(&extra) != io.EOF || (c.Format != ApprovalCheckpointFormat && c.Format != InteractionCheckpointFormat) || c.ApprovalID == uuid.Nil || c.BotID != run.BotID || c.RunID != run.ID || c.SessionID != run.SessionID || c.Version != run.Version || c.Fence <= 0 || len(c.ActionDigest) != 64 {
		return c, store.ErrBotInvalid
	}
	return c, nil
}
func (c ApprovalCheckpoint) Matches(a store.BotApproval) bool {
	if c.Format == InteractionCheckpointFormat {
		if a.Interaction == nil || a.Interaction.Validate() != nil || a.Action.Tool != "ask_user" || !bytes.Equal(a.Action.Arguments, a.Interaction.Payload) {
			return false
		}
	} else if a.Interaction != nil {
		return false
	}
	digest, err := a.Action.Digest()
	return err == nil && c.ApprovalID == a.ID && c.BotID == a.BotID && c.RunID == a.RunID && c.Version == a.Version && c.ActionDigest == a.ActionDigest && digest == a.ActionDigest
}

type approvalStoreKey struct{}

func WithApprovalStore(ctx context.Context, s store.BotStore) context.Context {
	return context.WithValue(ctx, approvalStoreKey{}, s)
}
func ApprovalStoreFromContext(ctx context.Context) store.BotStore {
	s, _ := ctx.Value(approvalStoreKey{}).(store.BotStore)
	return s
}

// ApprovalGrant never turns an answer into permission to execute an action.
func ApprovalGrant(id execution.BotRunIdentity, a store.BotApproval) execution.ScopedGrant {
	if a.Interaction != nil {
		return execution.ScopedGrant{}
	}
	return execution.ScopedGrant{ID: a.ID, Identity: id, ActionDigest: a.ActionDigest, ExpiresAt: a.ExpiresAt}
}
