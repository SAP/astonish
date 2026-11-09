package entstore

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/SAP/astonish/pkg/store"
)

// The source digest is verified against the persisted authenticated owner event
// and instructions before capture. It is not a caller-provided renewal flag.
func routineSourceSuppressionKey(payload store.BotControlPayload, mandate store.BotMandate, policy store.BotMandatePolicy, sourceDigest string) string {
	return routineSuppressionKey(payload, mandate, policy) + ":source:" + sourceDigest
}

func routineSourceSuppressed(ctx context.Context, tx botDraftDB, rec *botStoredRecord, payload store.BotControlPayload, mandate store.BotMandate, policy store.BotMandatePolicy, sourceDigest string) (bool, error) {
	if sourceDigest == "" {
		return true, store.ErrBotForbidden
	}
	if denied, _ := rec.Metadata[routineSourceSuppressionKey(payload, mandate, policy, sourceDigest)].(bool); denied {
		return true, nil
	}
	if legacy, _ := rec.Metadata[routineSuppressionKey(payload, mandate, policy)].(bool); !legacy {
		return false, nil
	}
	// Old binaries persisted a bot-wide bool. Recover its source from immutable
	// denial receipts; never clear the old marker or rewrite the prior operation.
	// Use only the reserved head index, not conversation/history text.
	foundDenial := false
	for key, value := range rec.Metadata {
		if !strings.HasPrefix(key, "mandate_operation_head:") {
			continue
		}
		raw, err := json.Marshal(value)
		if err != nil {
			return true, err
		}
		var revision int64
		if json.Unmarshal(raw, &revision) != nil || revision < 1 {
			return true, store.ErrBotInvalid
		}
		a, err := tx.FindActivity(ctx, rec.Bot.ID, "mandate-operation:"+strings.TrimPrefix(key, "mandate_operation_head:")+":"+strconv.FormatInt(revision, 10))
		if err != nil {
			return true, err
		}
		var o store.BotOperation
		if a.Kind != mandateRecordKind || json.Unmarshal(a.Payload, &o) != nil || o.Validate() != nil || operationHeadKey(o.ID) != key || o.Revision != revision || o.MandateRef.BotID != rec.Bot.ID {
			return true, store.ErrBotInvalid
		}
		if o.Status != store.BotOperationCancelled || o.Decision != store.BotOperationDeny || o.Provenance == nil || o.Receipt == nil || o.Receipt.Reason != "authorization_denied" {
			continue
		}
		if routineSuppressionKey(o.Payload, mandate, policy) != routineSuppressionKey(payload, mandate, policy) {
			continue
		}
		foundDenial = true
		if o.Provenance.SourceDigest == sourceDigest {
			return true, nil
		}
	}
	// An unexplained legacy marker fails closed. A different verified owner source
	// may capture a NEW blocked exception, never revive this cancelled operation.
	return !foundDenial, nil
}
