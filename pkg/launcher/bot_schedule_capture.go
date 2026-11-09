package launcher

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// verifyScheduleCapture reads the durable record independently of the enqueue
// return. Neither an operation ID nor a pending-shaped response proves capture.
func verifyScheduleCapture(ctx context.Context, runtime store.BotAuthorizedOperationStore, q store.BotAuthorizedOperationRequest, returned *store.BotOperation) (*store.BotOperation, error) {
	if returned == nil || returned.ID == uuid.Nil || returned.Status != store.BotOperationPending {
		return nil, store.ErrBotConflict
	}
	saved, err := runtime.ReadAuthorizedOperation(ctx, q.MandateRef, returned.ID)
	if err != nil {
		return nil, err
	}
	if saved == nil || saved.Validate() != nil || !reflect.DeepEqual(saved, returned) || saved.MandateRef != q.MandateRef || saved.IdempotencyKey != q.IdempotencyKey || !reflect.DeepEqual(saved.Payload, q.Payload) || saved.Status != store.BotOperationPending || saved.Decision != store.BotOperationExecute || saved.Receipt != nil || saved.Provenance == nil {
		return nil, store.ErrBotConflict
	}
	data, err := json.Marshal(q.Payload)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(data)
	p := saved.Provenance
	if p.SourceRunID != q.Lease.RunID || p.SourceUserID != q.MandateRef.OwnerID || p.UserEventID == "" || p.SourceDigest == "" || p.PayloadDigest != hex.EncodeToString(digest[:]) || p.Purpose != execution.BotMandatePurposeSchedule || p.ExpectedRevision != q.Payload.Schedule.ExpectedRevision {
		return nil, store.ErrBotConflict
	}
	return saved, nil
}
