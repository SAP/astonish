package launcher

import (
	"context"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// Corrupt only the independent read-back; the enqueue itself still persists.
type scheduleCaptureReadback struct {
	store.BotAuthorizedOperationStore
	scenario string
}

func (s scheduleCaptureReadback) ReadAuthorizedOperation(ctx context.Context, ref store.BotMandateRef, id uuid.UUID) (*store.BotOperation, error) {
	op, err := s.BotAuthorizedOperationStore.ReadAuthorizedOperation(ctx, ref, id)
	if err != nil {
		return nil, err
	}
	switch s.scenario {
	case "absent":
		return nil, nil
	case "read-error":
		return nil, store.ErrBotNotFound
	case "wrong-id":
		op.ID = uuid.New()
	case "wrong-run":
		op.Provenance.SourceRunID = uuid.New()
	case "wrong-digest":
		op.Provenance.PayloadDigest = "tampered"
	case "wrong-payload":
		op.Payload.Schedule.Trigger.Cron = "* * * * *"
	case "applied":
		op.Status = store.BotOperationApplied
	case "missing-provenance":
		op.Provenance = nil
	}
	return op, nil
}
