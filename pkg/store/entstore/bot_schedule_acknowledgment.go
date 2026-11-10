package entstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	botdomain "github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// scheduleAcknowledgment binds only an acknowledgment-only accepted candidate to
// independently stored operations. Candidate bodies are evidence, not authority.
// Called under the source lease before application; the resulting marker belongs
// to the immutable committed outcome, so publication order cannot change it.
func scheduleAcknowledgment(ctx context.Context, tx botDraftDB, rec *botStoredRecord, run store.BotRun, work *store.BotWorkState, q store.BotRunCommit) (*store.BotReportAcknowledgment, error) {
	if q.Status != store.RunSucceeded || q.Report.Failure != nil || len(q.Report.Changed) != 0 || len(q.Report.Unverified) != 0 || len(q.Report.Artifacts) != 0 || len(q.Report.NeedsApproval) != 0 || q.Report.NextRunAt != nil || work == nil || work.Phase != store.BotWorkAccepted || work.Candidate == nil || run.MandateRef == nil {
		return nil, nil
	}
	c := work.Candidate
	if c.Text != q.Report.Summary || len(c.Artifacts) != 0 {
		return nil, nil
	}
	count := 0
	for key := range c.Bodies {
		if strings.HasPrefix(key, "pending-control:") {
			count++
		} else if key != "producer-final-draft" && key != "observed-current-controls" && !strings.HasPrefix(key, "tool-response:") {
			return nil, nil
		}
	}
	observations := c.Observations
	if raw, ok := c.Bodies["observed-current-controls"]; ok {
		if !json.Valid([]byte(raw)) || len(observations) != count+1 {
			return nil, nil
		}
		observed := observations[len(observations)-1]
		if observed.Kind != store.BotEvidenceObservation || observed.ActorID != rec.Bot.OwnerID || observed.ObservedAt.IsZero() || observed.Source != "bot-assessment-state://"+string(rec.Bot.Scope)+"/"+rec.Bot.ID.String()+"/"+run.ID.String() {
			return nil, nil
		}
		observations = observations[:len(observations)-1]
	}
	if count == 0 || len(observations) != count || len(q.Report.Unchanged) != count || !reflect.DeepEqual(observations, q.Report.Unchanged) {
		return nil, nil
	}
	marker := &store.BotReportAcknowledgment{Kind: store.BotReportAcknowledgmentSchedulePending}
	seen := map[uuid.UUID]bool{}
	var texts []string
	for i := 0; i < count; i++ {
		var body struct {
			OperationID    uuid.UUID                 `json:"operation_id"`
			Operation      store.BotControlOperation `json:"operation"`
			Acknowledgment string                    `json:"acknowledgment"`
			Payload        store.BotControlPayload   `json:"payload"`
		}
		if json.Unmarshal([]byte(c.Bodies[fmt.Sprintf("pending-control:%d", i)]), &body) != nil || body.OperationID == uuid.Nil || seen[body.OperationID] {
			return nil, nil
		}
		switch body.Operation {
		case store.BotControlScheduleCreate, store.BotControlScheduleEdit, store.BotControlSchedulePause, store.BotControlScheduleRemove:
		default:
			return nil, nil
		}
		op, err := readMandateOperation(ctx, tx, rec, *run.MandateRef, body.OperationID)
		if errors.Is(err, store.ErrBotNotFound) || errors.Is(err, store.ErrBotForbidden) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		if op.Status != store.BotOperationPending || op.Decision != store.BotOperationExecute || op.Provenance == nil || op.Provenance.SourceRunID != run.ID || op.Provenance.UserEventID != run.UserEventID || op.Provenance.SourceUserID != rec.Bot.OwnerID.String() || op.Payload.Operation != body.Operation || !reflect.DeepEqual(op.Payload, body.Payload) {
			return nil, nil
		}
		if err := verifyAuthorizedGrant(ctx, tx, *op); err != nil {
			return nil, nil
		}
		capture := botdomain.ScheduleCapture{OperationID: op.ID, BotID: rec.Bot.ID, SourceRunID: run.ID, OwnerID: rec.Bot.OwnerID, Operation: op.Payload.Operation, ObservedAt: c.Observations[i].ObservedAt}
		if capture.ObservedAt.IsZero() || body.Acknowledgment != capture.Acknowledgment() || !reflect.DeepEqual(c.Observations[i], capture.Observation()) {
			return nil, nil
		}
		texts = append(texts, capture.Acknowledgment())
		marker.OperationIDs = append(marker.OperationIDs, op.ID)
		seen[op.ID] = true
	}
	if strings.Join(texts, "\n\n") != q.Report.Summary {
		return nil, nil
	}
	// Tool response snapshots are private review context too. Only exact pending
	// response envelopes for the independently bound operations qualify; errors
	// and unrelated tool bodies keep the report visible.
	for key, raw := range c.Bodies {
		if !strings.HasPrefix(key, "tool-response:") {
			continue
		}
		var response map[string]json.RawMessage
		if json.Unmarshal([]byte(raw), &response) != nil || len(response) != 3 {
			return nil, nil
		}
		var id uuid.UUID
		var status store.BotOperationStatus
		var message string
		if json.Unmarshal(response["operation_id"], &id) != nil || json.Unmarshal(response["status"], &status) != nil || json.Unmarshal(response["message"], &message) != nil || !seen[id] || status != store.BotOperationPending {
			return nil, nil
		}
		matched := false
		for i, operationID := range marker.OperationIDs {
			if operationID == id && texts[i] == message {
				matched = true
			}
		}
		if !matched {
			return nil, nil
		}
	}
	return marker, nil
}
