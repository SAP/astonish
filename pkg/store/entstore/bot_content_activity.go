package entstore

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"reflect"
	"time"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// botActivityRecord is the SQL coordination envelope. Payloads (including nested
// reports and outbox bodies) use database bytes. Sequence/deduplication,
// availability, attempts and delivery leases remain SQL columns. The digest pins
// exact evidence, never a newer mutable report or a path supplied by an agent.
type botActivityRecord struct {
	botRuntimeInline
	SchemaVersion    int                          `json:"schema_version"`
	BotID            store.BotID                  `json:"bot_id"`
	RunID            *store.RunID                 `json:"run_id,omitempty"`
	Sequence         int64                        `json:"sequence"`
	Kind             store.BotActivityKind        `json:"kind"`
	CreatedAt        time.Time                    `json:"created_at"`
	Intent           store.BotOutboxKind          `json:"intent,omitempty"`
	Dispatch         *botProgressDispatch         `json:"dispatch,omitempty"`
	ProgressContext  *botProgressContextAuthority `json:"progress_context,omitempty"`
	ControlProposal  *botControlProposalAuthority `json:"control_proposal,omitempty"`
	RunStatus        store.RunStatus              `json:"run_status,omitempty"`
	CleanupAuthority *botCleanupAuthority         `json:"cleanup_authority,omitempty"`
}

type botActivityContent struct {
	SchemaVersion int               `json:"schema_version"`
	Activity      store.BotActivity `json:"activity"`
}

func botActivityContentKind(kind store.BotActivityKind) (store.BotContentKind, error) {
	switch kind {
	case store.BotActivityReport:
		return store.BotContentArtifact, nil
	case store.BotActivityLifecycle, store.BotActivityRun, store.BotActivityApproval,
		store.BotActivityCorrection, store.BotActivityPersona, store.BotActivityNotification,
		store.BotActivityMemoryReview, progressRecordKind, mandateRecordKind:
		return store.BotContentEvidence, nil
	default:
		return "", store.ErrBotInvalid
	}
}

func activityCoordination(a store.BotActivity) (store.BotOutboxKind, *botProgressDispatch, error) {
	var routing struct {
		Intent store.BotOutboxKind `json:"intent"`
	}
	if len(a.Payload) == 0 || !json.Valid(a.Payload) {
		return "", nil, store.ErrBotInvalid
	}
	if err := json.Unmarshal(a.Payload, &routing); err != nil {
		return "", nil, err
	}
	switch routing.Intent {
	case "", store.BotOutboxDispatch, store.BotOutboxPublish, store.BotOutboxNotify, store.BotOutboxReconcile, store.BotOutboxCleanup:
	default:
		return "", nil, store.ErrBotInvalid
	}
	// These are private, typed request/receipt/settlement coordination records, not
	// free-form checkpoint or continuation state. Keep their exact fencing metadata.
	var dispatch *botProgressDispatch
	if a.Kind == progressRecordKind {
		var d botProgressDispatch
		if err := json.Unmarshal(a.Payload, &d); err != nil {
			return "", nil, err
		}
		if d.Request.DispatchID != uuid.Nil {
			if a.RunID == nil || d.Request.Mutation.Binding.BotID != a.BotID || d.Request.Mutation.Binding.RunID != *a.RunID ||
				d.Request.Validate(d.Request.Mutation.Lease.ExpiresAt.Add(-time.Nanosecond)) != nil ||
				d.Receipt.DispatchID != d.Request.DispatchID || d.Receipt.LedgerCursor < 1 ||
				d.Settlement != nil && (d.Settlement.DispatchID != d.Request.DispatchID || d.Settlement.Validate(d.Settlement.Mutation.Lease.ExpiresAt.Add(-time.Nanosecond)) != nil) {
				return "", nil, store.ErrBotInvalid
			}
			dispatch = &d
		}
	}
	return routing.Intent, dispatch, nil
}

func (c *botContentCodec) encodeActivity(ctx context.Context, a store.BotActivity) (map[string]any, error) {
	_, err := botActivityContentKind(a.Kind)
	if err != nil {
		return nil, err
	}
	if a.BotID == uuid.Nil || a.Sequence < 1 || a.CreatedAt.IsZero() || a.RunID != nil && *a.RunID == uuid.Nil {
		return nil, store.ErrBotInvalid
	}
	intent, dispatch, err := activityCoordination(a)
	if err != nil {
		return nil, err
	}
	progressContext, controlProposal, err := activityPrivateAuthority(a)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(botActivityContent{SchemaVersion: store.BotSchemaVersion, Activity: a})
	if err != nil {
		return nil, err
	}
	record := botActivityRecord{SchemaVersion: store.BotSchemaVersion, BotID: a.BotID, RunID: a.RunID, Sequence: a.Sequence, Kind: a.Kind, CreatedAt: a.CreatedAt, Intent: intent, Dispatch: dispatch, ProgressContext: progressContext, ControlProposal: controlProposal, botRuntimeInline: runtimeInline(body)}
	if a.Kind == mandateRecordKind {
		record.CleanupAuthority, err = cleanupAuthority(a.Payload)
		if err != nil {
			return nil, err
		}
	}
	if a.Kind == store.BotActivityReport || a.Kind == store.BotActivityRun {
		var state struct {
			Status store.RunStatus `json:"status"`
		}
		if json.Unmarshal(a.Payload, &state) != nil {
			return nil, store.ErrBotInvalid
		}
		record.RunStatus = state.Status
	}
	return c.encodeRuntimeRecord(ctx, a.BotID, &record)
}

func (c *botContentCodec) decodeActivity(ctx context.Context, id store.BotID, sequence int64, kind store.BotActivityKind, created time.Time, data map[string]any) (_ *store.BotActivity, resultErr error) {
	total := store.ObserveBotActivity(ctx, "activity_row_total", sequence)
	defer total()
	if _, ok := data[botCleanupRecordKey]; ok {
		return decodeCleanupActivity(id, sequence, kind, created, data)
	}
	var record botActivityRecord
	if err := decodeBotRecord(data, &record); err != nil {
		return nil, err
	}
	if _, err := botActivityContentKind(kind); err != nil {
		return nil, err
	}
	if record.SchemaVersion != store.BotSchemaVersion || record.BotID != id || id == uuid.Nil || record.Sequence != sequence || sequence < 1 || record.Kind != kind || record.CreatedAt.IsZero() || !record.CreatedAt.Equal(created) || record.RunID != nil && *record.RunID == uuid.Nil {
		return nil, store.ErrBotInvalid
	}
	if err := validateRuntimePayload(record.botRuntimeInline); err != nil {
		return nil, err
	}
	raw, err := c.readRuntimeBody(ctx, id, record.botRuntimeInline)
	if err != nil {
		return nil, err
	}
	// The shared reader reports read/hash time under this same stage;
	// this disjoint span adds JSON decoding of the database body.
	decode := store.ObserveBotActivity(ctx, "content_read_decode", sequence)
	var body botActivityContent
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	err = decoder.Decode(&body)
	if err == nil && decoder.Decode(new(any)) != io.EOF {
		err = store.ErrBotInvalid
	}
	decode()
	if err != nil {
		return nil, err
	}
	validate := store.ObserveBotActivity(ctx, "content_validate", sequence)
	defer validate()
	a := body.Activity
	if body.SchemaVersion != record.SchemaVersion || a.BotID != record.BotID || a.Sequence != record.Sequence || a.Kind != record.Kind || !a.CreatedAt.Equal(record.CreatedAt) || !reflect.DeepEqual(a.RunID, record.RunID) {
		return nil, store.ErrBotInvalid
	}
	intent, dispatch, err := activityCoordination(a)
	if err != nil {
		return nil, err
	}
	if intent != record.Intent || !reflect.DeepEqual(dispatch, record.Dispatch) {
		return nil, store.ErrBotInvalid
	}
	progressContext, controlProposal, err := activityPrivateAuthority(a)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(progressContext, record.ProgressContext) || !reflect.DeepEqual(controlProposal, record.ControlProposal) {
		return nil, store.ErrBotInvalid
	}
	if a.Kind == mandateRecordKind {
		authority, err := cleanupAuthority(a.Payload)
		if err != nil || !reflect.DeepEqual(authority, record.CleanupAuthority) {
			return nil, store.ErrBotInvalid
		}
	} else if record.CleanupAuthority != nil {
		return nil, store.ErrBotInvalid
	}
	if a.Kind == store.BotActivityReport || a.Kind == store.BotActivityRun {
		var state struct {
			Status store.RunStatus `json:"status"`
		}
		if json.Unmarshal(a.Payload, &state) != nil || state.Status != record.RunStatus {
			return nil, store.ErrBotInvalid
		}
	} else if record.RunStatus != "" {
		return nil, store.ErrBotInvalid
	}
	return &a, nil
}
