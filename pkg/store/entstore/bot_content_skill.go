package entstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// Only immutable identity and lineage are SQL coordination metadata. In
// particular provenance quotes, source URLs, methods and change reasons belong
// exclusively to the disk object, never to this record.
type botSkillRevisionRecord struct {
	Format          int                 `json:"format"`
	SchemaVersion   int                 `json:"schema_version"`
	ID              uuid.UUID           `json:"id"`
	BotID           store.BotID         `json:"bot_id"`
	SkillID         uuid.UUID           `json:"skill_id"`
	Revision        int64               `json:"revision"`
	ParentRevision  int64               `json:"parent_revision"`
	ParentDigest    string              `json:"parent_digest"`
	Digest          string              `json:"digest"`
	RevertsRevision *int64              `json:"reverts_revision"`
	Content         store.BotContentRef `json:"content"`
}

// The complete hydrated revision is archived, including canonical provenance.
// It is not an authority grant and its logical digest is revalidated on read.
type botSkillRevisionContent struct {
	Format   int                    `json:"format"`
	Revision store.BotSkillRevision `json:"revision"`
}

func (c *botContentCodec) encodeSkillRevision(ctx context.Context, v store.BotSkillRevision) (map[string]any, error) {
	if c == nil || c.Open == nil {
		return nil, store.ErrBotUnsupported
	}
	if err := v.Validate(); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(botSkillRevisionContent{Format: botDiskRecordFormat, Revision: v})
	if err != nil {
		return nil, err
	}
	disk, err := c.Open(ctx, v.BotID)
	if err != nil {
		return nil, err
	}
	ref, putErr := disk.Put(ctx, store.BotContentSkill, v.Revision, int64(len(raw)), bytes.NewReader(raw), func(commit func() error) error { return commit() })
	if err := errors.Join(putErr, disk.Close()); err != nil {
		return nil, err
	}
	sum := sha256.Sum256(raw)
	if ref == nil || ref.Validate() != nil || ref.Scope.BotID != v.BotID || ref.Kind != store.BotContentSkill || ref.Revision != v.Revision || ref.SizeBytes != int64(len(raw)) || ref.SHA256 != hex.EncodeToString(sum[:]) {
		return nil, store.ErrBotConflict
	}
	record := botSkillRevisionRecord{Format: botDiskRecordFormat, SchemaVersion: v.SchemaVersion, ID: v.ID, BotID: v.BotID, SkillID: v.SkillID, Revision: v.Revision, ParentRevision: v.ParentRevision, ParentDigest: v.ParentDigest, Digest: v.Digest, RevertsRevision: v.RevertsRevision, Content: *ref}
	encoded, err := json.Marshal(record)
	if err != nil {
		return nil, err
	}
	return map[string]any{"bot_record_v2": string(encoded)}, nil
}

func (c *botContentCodec) decodeSkillRevision(ctx context.Context, botID store.BotID, data map[string]any) (_ *store.BotSkillRevision, resultErr error) {
	if c == nil || c.Open == nil {
		return nil, store.ErrBotUnsupported
	}
	var record botSkillRevisionRecord
	if err := decodeBotRecord(data, &record); err != nil {
		return nil, err
	}
	ref := record.Content
	if record.Format != botDiskRecordFormat || record.SchemaVersion != store.BotSkillSchemaVersion || record.ID == uuid.Nil || record.SkillID == uuid.Nil || record.BotID != botID || record.Revision < 1 || record.Revision > store.BotSkillMaxRevisions || ref.Validate() != nil || ref.Scope.BotID != botID || ref.Kind != store.BotContentSkill || ref.Revision != record.Revision || ref.SizeBytes > 2*store.BotSkillMaxRevisionBytes {
		return nil, store.ErrBotInvalid
	}
	disk, err := c.Open(ctx, botID)
	if err != nil {
		return nil, err
	}
	defer func() { resultErr = errors.Join(resultErr, disk.Close()) }()
	stream, err := disk.Open(ctx, ref, store.BotFileRange{})
	if err != nil {
		return nil, err
	}
	if stream == nil {
		return nil, store.ErrBotConflict
	}
	raw, readErr := io.ReadAll(io.LimitReader(stream, ref.SizeBytes+1))
	if err := errors.Join(readErr, stream.Close()); err != nil {
		return nil, err
	}
	sum := sha256.Sum256(raw)
	if int64(len(raw)) != ref.SizeBytes || hex.EncodeToString(sum[:]) != ref.SHA256 {
		return nil, store.ErrBotConflict
	}
	var body botSkillRevisionContent
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		return nil, errors.Join(store.ErrBotInvalid, err)
	}
	if decoder.Decode(new(any)) != io.EOF {
		return nil, store.ErrBotInvalid
	}
	v := body.Revision
	if body.Format != record.Format || v.SchemaVersion != record.SchemaVersion || v.ID != record.ID || v.BotID != botID || v.SkillID != record.SkillID || v.Revision != record.Revision || v.ParentRevision != record.ParentRevision || v.ParentDigest != record.ParentDigest || v.Digest != record.Digest || (v.RevertsRevision == nil) != (record.RevertsRevision == nil) {
		return nil, store.ErrBotConflict
	}
	if v.RevertsRevision != nil && *v.RevertsRevision != *record.RevertsRevision {
		return nil, store.ErrBotConflict
	}
	if err := v.Validate(); err != nil {
		return nil, err
	}
	return &v, nil
}
