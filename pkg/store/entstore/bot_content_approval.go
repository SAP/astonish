package entstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// botApprovalRecord is an explicit SQL authority allowlist, not a serialized
// BotApproval. Status/expiry columns remain authoritative for bulk revocation.
// The exact action digest is checked against the database bytes; the content
// body never creates authority.
type botApprovalRecord struct {
	botRuntimeInline
	ID                  uuid.UUID                     `json:"id"`
	BotID               store.BotID                   `json:"bot_id"`
	RunID               store.RunID                   `json:"run_id"`
	Version             int64                         `json:"version"`
	ActionDigest        string                        `json:"action_digest"`
	Status              store.ApprovalStatus          `json:"status"`
	CreatedAt           time.Time                     `json:"created_at"`
	ExpiresAt           time.Time                     `json:"expires_at"`
	DecidedAt           *time.Time                    `json:"decided_at,omitempty"`
	DecidedBy           *uuid.UUID                    `json:"decided_by,omitempty"`
	Tool                string                        `json:"tool"`
	Effect              execution.ActionEffect        `json:"effect"`
	TargetFingerprint   string                        `json:"target_fingerprint,omitempty"`
	Currency            string                        `json:"currency,omitempty"`
	SpendMinor          int64                         `json:"spend_minor,omitempty"`
	ConstraintsVerified bool                          `json:"constraints_verified"`
	Interaction         *botApprovalInteractionRecord `json:"interaction,omitempty"`
	ResponseQuestionID  *string                       `json:"response_question_id,omitempty"`
}

type botApprovalInteractionRecord struct {
	Kind       store.BotInteractionKind `json:"kind"`
	QuestionID string                   `json:"question_id"`
}

// Bytes preserve exact argument/question JSON, including whitespace and large
// numbers; none of these fields can be handed to an Ent JSON setter.
type botApprovalContent struct {
	ID             uuid.UUID   `json:"id"`
	BotID          store.BotID `json:"bot_id"`
	RunID          store.RunID `json:"run_id"`
	Version        int64       `json:"version"`
	Arguments      []byte      `json:"arguments"`
	TargetSnapshot []byte      `json:"target_snapshot"`
	Recipient      string      `json:"recipient"`
	Merchant       string      `json:"merchant"`
	Question       []byte      `json:"question"`
	Answer         string      `json:"answer"`
}

func (c *botContentCodec) encodeApproval(ctx context.Context, a store.BotApproval) (map[string]any, error) {
	digest, err := a.Action.Digest()
	if err != nil || digest != a.ActionDigest || a.ID == uuid.Nil || a.BotID == uuid.Nil || a.RunID == uuid.Nil || a.Version < 1 {
		return nil, store.ErrBotInvalid
	}
	r := botApprovalRecord{ID: a.ID, BotID: a.BotID, RunID: a.RunID, Version: a.Version, ActionDigest: a.ActionDigest, Status: a.Status, CreatedAt: a.CreatedAt, ExpiresAt: a.ExpiresAt, DecidedAt: a.DecidedAt, DecidedBy: a.DecidedBy, Tool: a.Action.Tool, Effect: a.Action.Effect, TargetFingerprint: a.Action.TargetFingerprint, Currency: a.Action.Currency, SpendMinor: a.Action.SpendMinor, ConstraintsVerified: a.Action.ConstraintsVerified}
	body := botApprovalContent{ID: a.ID, BotID: a.BotID, RunID: a.RunID, Version: a.Version, Arguments: a.Action.Arguments, TargetSnapshot: a.Action.TargetSnapshot, Recipient: a.Action.Recipient, Merchant: a.Action.Merchant}
	if a.Interaction != nil {
		r.Interaction = &botApprovalInteractionRecord{Kind: a.Interaction.Kind, QuestionID: a.Interaction.QuestionID}
		body.Question = a.Interaction.Payload
	}
	if a.Response != nil {
		qid := a.Response.QuestionID
		r.ResponseQuestionID = &qid
		body.Answer = a.Response.Answer
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	r.botRuntimeInline = runtimeInline(raw)
	return c.encodeRuntimeRecord(ctx, a.BotID, &r)
}

func (c *botContentCodec) decodeApproval(ctx context.Context, id store.BotID, data map[string]any) (_ *store.BotApproval, resultErr error) {
	var r botApprovalRecord
	if err := decodeBotRecord(data, &r); err != nil {
		return nil, err
	}
	if id == uuid.Nil || r.BotID != id || r.ID == uuid.Nil || r.RunID == uuid.Nil || r.Version < 1 || len(r.ActionDigest) != 64 {
		return nil, store.ErrBotInvalid
	}
	if err := validateRuntimePayload(r.botRuntimeInline); err != nil {
		return nil, err
	}
	raw, err := c.readRuntimeBody(ctx, id, r.botRuntimeInline)
	if err != nil {
		return nil, err
	}
	var body botApprovalContent
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		return nil, errors.Join(store.ErrBotInvalid, err)
	}
	if decoder.Decode(new(any)) != io.EOF || body.ID != r.ID || body.BotID != id || body.RunID != r.RunID || body.Version != r.Version {
		return nil, store.ErrBotConflict
	}
	a := &store.BotApproval{ID: r.ID, BotID: id, RunID: r.RunID, Version: r.Version, ActionDigest: r.ActionDigest, Status: r.Status, CreatedAt: r.CreatedAt, ExpiresAt: r.ExpiresAt, DecidedAt: r.DecidedAt, DecidedBy: r.DecidedBy,
		Action: execution.ActionDescriptor{Tool: r.Tool, Effect: r.Effect, TargetFingerprint: r.TargetFingerprint, Currency: r.Currency, SpendMinor: r.SpendMinor, ConstraintsVerified: r.ConstraintsVerified, Arguments: json.RawMessage(body.Arguments), TargetSnapshot: json.RawMessage(body.TargetSnapshot), Recipient: body.Recipient, Merchant: body.Merchant}}
	if r.Interaction != nil {
		a.Interaction = &store.BotInteraction{Kind: r.Interaction.Kind, QuestionID: r.Interaction.QuestionID, Payload: json.RawMessage(body.Question)}
	} else if body.Question != nil {
		return nil, store.ErrBotConflict
	}
	if r.ResponseQuestionID != nil {
		a.Response = &store.BotInteractionResponse{QuestionID: *r.ResponseQuestionID, Answer: body.Answer}
	} else if body.Answer != "" {
		return nil, store.ErrBotConflict
	}
	digest, err := a.Action.Digest()
	if err != nil || digest != r.ActionDigest {
		return nil, store.ErrBotConflict
	}
	return a, nil
}
