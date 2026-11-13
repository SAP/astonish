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

// scheduleAcknowledgment also binds the closed routine metadata set. Candidate
// bodies are evidence, never authority. This runs under the source lease before
// application; its immutable marker is independent of publication order.
func scheduleAcknowledgment(ctx context.Context, tx botDraftDB, rec *botStoredRecord, run store.BotRun, work *store.BotWorkState, q store.BotRunCommit) (*store.BotReportAcknowledgment, error) {
	if q.Status != store.RunSucceeded || q.Report.Failure != nil || len(q.Report.Changed) != 0 || len(q.Report.Unverified) != 0 || len(q.Report.Artifacts) != 0 || len(q.Report.NeedsApproval) != 0 || q.Report.NextRunAt != nil || work == nil || work.Candidate == nil || run.MandateRef == nil {
		return nil, nil
	}
	switch work.Protocol {
	case store.BotWorkProtocolAgentLedV1:
		if store.ValidateBotWorkCompletion(work, q) != nil {
			return nil, nil
		}
	case store.BotWorkProtocolLegacyAssessment:
		if work.Phase != store.BotWorkAccepted {
			return nil, nil
		}
	default:
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
	responses := map[uuid.UUID]map[string]any{}
	var texts []string
	for i := 0; i < count; i++ {
		var body struct {
			OperationID    uuid.UUID                 `json:"operation_id"`
			Operation      store.BotControlOperation `json:"operation"`
			Acknowledgment string                    `json:"acknowledgment"`
			Payload        store.BotControlPayload   `json:"payload"`
		}
		if json.Unmarshal([]byte(c.Bodies[fmt.Sprintf("pending-control:%d", i)]), &body) != nil || body.OperationID == uuid.Nil || responses[body.OperationID] != nil {
			return nil, nil
		}
		if !scheduleOperation(body.Operation) && !store.BotControlConversationalMetadata(body.Operation) {
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
		var text string
		var evidence store.BotEvidence
		response := map[string]any{"operation_id": op.ID, "status": op.Status}
		if scheduleOperation(body.Operation) {
			if verifyAuthorizedGrant(ctx, tx, *op) != nil {
				return nil, nil
			}
			capture := botdomain.ScheduleCapture{OperationID: op.ID, BotID: rec.Bot.ID, SourceRunID: run.ID, OwnerID: rec.Bot.OwnerID, Operation: op.Payload.Operation, ObservedAt: observations[i].ObservedAt}
			text, evidence = capture.Acknowledgment(), capture.Observation()
		} else {
			if verifyRoutineGrant(ctx, tx, *op) != nil {
				return nil, nil
			}
			capture := botdomain.RoutineCapture{OperationID: op.ID, BotID: rec.Bot.ID, SourceRunID: run.ID, OwnerID: rec.Bot.OwnerID, Operation: op.Payload.Operation, Status: op.Status, Revision: op.Revision, ObservedAt: observations[i].ObservedAt}
			text, evidence = capture.Acknowledgment(), capture.Observation()
			response["bot_id"], response["digest"], response["decision"] = rec.Bot.ID, op.Provenance.PayloadDigest, op.Decision
			marker.Kind = store.BotReportAcknowledgmentControlPending
		}
		if evidence.ObservedAt.IsZero() || body.Acknowledgment != text || !reflect.DeepEqual(observations[i], evidence) {
			return nil, nil
		}
		response["message"] = text
		responses[op.ID] = response
		texts = append(texts, text)
		marker.OperationIDs = append(marker.OperationIDs, op.ID)
	}
	canonical := strings.Join(texts, "\n\n")
	if canonical != c.Text {
		return nil, nil
	}
	// A server-owned acknowledgment may replace the producer's final text.
	// Suppression is safe only when that original draft contains no independent
	// content; candidate equality alone cannot establish this.
	if draft := strings.TrimSpace(c.Bodies["producer-final-draft"]); draft != "" && draft != canonical {
		return nil, nil
	}
	// Compare complete response envelopes, including routine scope/digest/decision.
	// Unknown fields, errors and unrelated tool results keep the report visible.
	for key, raw := range c.Bodies {
		if !strings.HasPrefix(key, "tool-response:") {
			continue
		}
		var response map[string]any
		var identity struct {
			ID uuid.UUID `json:"operation_id"`
		}
		if json.Unmarshal([]byte(raw), &response) != nil || json.Unmarshal([]byte(raw), &identity) != nil || responses[identity.ID] == nil {
			return nil, nil
		}
		expectedRaw, _ := json.Marshal(responses[identity.ID])
		var expected map[string]any
		if json.Unmarshal(expectedRaw, &expected) != nil || !reflect.DeepEqual(response, expected) {
			return nil, nil
		}
	}
	return marker, nil
}
