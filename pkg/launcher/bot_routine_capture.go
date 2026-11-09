package launcher

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
)

// verifyRoutineCapture compares the independent read to both enqueue and request.
// A pending-shaped response alone is not evidence, and capture cannot apply work.
func verifyRoutineCapture(q store.BotAuthorizedOperationRequest, returned, saved *store.BotOperation) error {
	if returned == nil || saved == nil || saved.Validate() != nil || !reflect.DeepEqual(returned, saved) || saved.MandateRef != q.MandateRef || saved.IdempotencyKey != q.IdempotencyKey || !reflect.DeepEqual(saved.Payload, q.Payload) || saved.Status != store.BotOperationPending || saved.Decision != store.BotOperationExecute || saved.Receipt != nil || saved.Provenance == nil {
		return store.ErrBotConflict
	}
	data, err := json.Marshal(q.Payload)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(data)
	p := saved.Provenance
	if p.SourceRunID != q.Lease.RunID || p.SourceUserID != q.MandateRef.OwnerID || p.UserEventID == "" || p.SourceDigest == "" || p.PayloadDigest != hex.EncodeToString(digest[:]) || (p.Purpose != execution.BotMandatePurposeTask && p.Purpose != execution.BotMandatePurposeOperation) || p.ExpectedRevision < 1 {
		return store.ErrBotConflict
	}
	return nil
}
