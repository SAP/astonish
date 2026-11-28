package entstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// botKnowledgeRecord is the SQL coordination allowlist, not a serialized domain
// envelope. Source and decision digests bind the exact hydrated evidence; leases,
// actor authority, budget baselines and CAS revisions never come from disk text.
type botKnowledgeRecord struct {
	Format         int                                `json:"format"`
	ID             uuid.UUID                          `json:"id"`
	Source         store.BotKnowledgeExtractionSource `json:"source"`
	InputDigest    string                             `json:"input_digest"`
	State          store.BotKnowledgeExtractionState  `json:"state"`
	Revision       int64                              `json:"revision"`
	AttemptID      uuid.UUID                          `json:"attempt_id"`
	DecisionDigest string                             `json:"decision_digest,omitempty"`
	UsageBase      *store.BotUsage                    `json:"usage_base,omitempty"`
	Lease          store.BotKnowledgeExtractionLease  `json:"lease"`
	ActorID        uuid.UUID                          `json:"actor_id"`
	RootFence      int64                              `json:"root_fence"`
	Content        store.BotContentRef                `json:"content"`
}

// botKnowledgeContent is disk-only. It includes all conversation, outcome/tool
// evidence, prior-file summaries, proposal bundles and free-form hold prose.
type botKnowledgeContent struct {
	Input      store.BotKnowledgeExtractionInput     `json:"input"`
	Decision   *store.BotKnowledgeExtractionDecision `json:"decision,omitempty"`
	HoldReason string                                `json:"hold_reason,omitempty"`
}

func (c *botContentCodec) encodeKnowledge(ctx context.Context, v knowledgeEnvelope) (map[string]any, error) {
	if c == nil || c.Open == nil {
		return nil, store.ErrBotUnsupported
	}
	r := v.Record
	if r.ID == uuid.Nil || r.Revision < 1 || !validKnowledgeRetainedInput(r) || !validKnowledgeDecisionDigest(r) {
		return nil, store.ErrBotInvalid
	}
	body := botKnowledgeContent{Input: r.Input, Decision: r.Decision, HoldReason: r.HoldReason}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	disk, err := c.Open(ctx, r.Input.Source.BotID)
	if err != nil {
		return nil, err
	}
	ref, putErr := disk.Put(ctx, store.BotContentEvidence, r.Revision, int64(len(raw)), bytes.NewReader(raw), func(commit func() error) error { return commit() })
	if err = errors.Join(putErr, disk.Close()); err != nil {
		return nil, err
	}
	if ref == nil || ref.Validate() != nil || ref.Scope.BotID != r.Input.Source.BotID || ref.Kind != store.BotContentEvidence || ref.Revision != r.Revision {
		return nil, store.ErrBotConflict
	}
	record := botKnowledgeRecord{Format: botDiskRecordFormat, ID: r.ID, Source: r.Input.Source, InputDigest: store.BotKnowledgeDigest(r.Input), State: r.State, Revision: r.Revision, AttemptID: r.AttemptID, DecisionDigest: r.DecisionDigest, UsageBase: v.UsageBase, Lease: v.Lease, ActorID: v.ActorID, RootFence: v.RootFence, Content: *ref}
	encoded, err := json.Marshal(record)
	if err != nil {
		return nil, err
	}
	return map[string]any{"bot_record_v2": string(encoded)}, nil
}

func validKnowledgeDecisionDigest(r store.BotKnowledgeExtraction) bool {
	if r.Decision == nil {
		return r.DecisionDigest == ""
	}
	return r.Decision.Validate() == nil && store.BotKnowledgeDigest(*r.Decision) == r.DecisionDigest
}

func (c *botContentCodec) decodeKnowledge(ctx context.Context, botID, id uuid.UUID, data map[string]any) (*knowledgeEnvelope, error) {
	if c == nil || c.Open == nil {
		return nil, store.ErrBotUnsupported
	}
	var record botKnowledgeRecord
	if err := decodeBotRecord(data, &record); err != nil {
		return nil, err
	}
	ref := record.Content
	if record.Format != botDiskRecordFormat || record.ID != id || id == uuid.Nil || record.Source.BotID != botID || record.Source.Validate() != nil || record.Revision < 1 || ref.Validate() != nil || ref.Scope.BotID != botID || ref.Kind != store.BotContentEvidence || ref.Revision != record.Revision {
		return nil, store.ErrBotInvalid
	}
	disk, err := c.Open(ctx, botID)
	if err != nil {
		return nil, err
	}
	defer disk.Close()
	stream, err := disk.Open(ctx, ref, store.BotFileRange{})
	if err != nil {
		return nil, err
	}
	var body botKnowledgeContent
	decoder := json.NewDecoder(stream)
	decoder.DisallowUnknownFields()
	err = decoder.Decode(&body)
	if err == nil && decoder.Decode(new(any)) != io.EOF {
		err = store.ErrBotInvalid
	}
	if err = errors.Join(err, stream.Close()); err != nil {
		return nil, err
	}
	r := store.BotKnowledgeExtraction{ID: record.ID, Input: body.Input, State: record.State, Revision: record.Revision, AttemptID: record.AttemptID, Decision: body.Decision, DecisionDigest: record.DecisionDigest, HoldReason: body.HoldReason}
	if body.Input.Source != record.Source || store.BotKnowledgeDigest(body.Input) != record.InputDigest || !validKnowledgeRetainedInput(r) || !validKnowledgeDecisionDigest(r) {
		return nil, store.ErrBotInvalid
	}
	return &knowledgeEnvelope{Record: r, UsageBase: record.UsageBase, Lease: record.Lease, ActorID: record.ActorID, RootFence: record.RootFence}, nil
}
