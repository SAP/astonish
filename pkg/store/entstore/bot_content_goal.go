package entstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/SAP/astonish/pkg/store"
)

// botGoalRecord is the SQL allowlist, not the hydrated domain goal. Evidence,
// completion criteria and descriptive text exist only in the immutable object.
type botGoalRecord struct {
	Format        int                 `json:"format"`
	SchemaVersion int                 `json:"schema_version"`
	ID            uuid.UUID           `json:"id"`
	BotID         store.BotID         `json:"bot_id"`
	Revision      int64               `json:"revision"`
	Title         string              `json:"title"`
	Status        store.BotGoalStatus `json:"status"`
	DueAt         *time.Time          `json:"due_at"`
	CompletedAt   *time.Time          `json:"completed_at"`
	Content       store.BotContentRef `json:"content"`
}

// Identity inside the disk body prevents swapping two goals' references even
// when their Bot, kind and revision match. This type is never sent to Ent.
type botGoalContent struct {
	Format             int                 `json:"format"`
	SchemaVersion      int                 `json:"schema_version"`
	ID                 uuid.UUID           `json:"id"`
	BotID              store.BotID         `json:"bot_id"`
	Revision           int64               `json:"revision"`
	Description        string              `json:"description"`
	CompletionCriteria string              `json:"completion_criteria"`
	WorkspacePath      string              `json:"workspace_path"`
	Evidence           []store.BotEvidence `json:"evidence"`
}

func (r botGoalRecord) valid() bool {
	return r.Format == botDiskRecordFormat && r.SchemaVersion == store.BotSchemaVersion &&
		r.ID != uuid.Nil && r.BotID != uuid.Nil && r.Revision > 0 &&
		len(r.Title) <= 256 && utf8.ValidString(r.Title) &&
		(r.Status == store.BotGoalActive || r.Status == store.BotGoalCompleted)
}

func (c *botContentCodec) encodeGoal(ctx context.Context, goal store.BotGoal) (map[string]any, error) {
	if c == nil || c.Open == nil {
		return nil, store.ErrBotUnsupported
	}
	record := botGoalRecord{Format: botDiskRecordFormat, SchemaVersion: store.BotSchemaVersion,
		ID: goal.ID, BotID: goal.BotID, Revision: goal.Revision, Title: goal.Title,
		Status: goal.Status, DueAt: goal.DueAt, CompletedAt: goal.CompletedAt}
	if !record.valid() {
		return nil, store.ErrBotInvalid
	}
	body := botGoalContent{Format: record.Format, SchemaVersion: record.SchemaVersion,
		ID: goal.ID, BotID: goal.BotID, Revision: goal.Revision, Description: goal.Description,
		CompletionCriteria: goal.CompletionCriteria, WorkspacePath: goal.WorkspacePath, Evidence: goal.Evidence}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	disk, err := c.Open(ctx, goal.BotID)
	if err != nil {
		return nil, err
	}
	ref, putErr := disk.Put(ctx, store.BotContentDocument, goal.Revision, int64(len(raw)), bytes.NewReader(raw), func(commit func() error) error { return commit() })
	if err = errors.Join(putErr, disk.Close()); err != nil {
		return nil, err
	}
	digest := sha256.Sum256(raw)
	if ref == nil || ref.Validate() != nil || ref.Scope.BotID != goal.BotID ||
		ref.Kind != store.BotContentDocument || ref.Revision != goal.Revision ||
		ref.SizeBytes != int64(len(raw)) || ref.SHA256 != hex.EncodeToString(digest[:]) {
		return nil, store.ErrBotConflict
	}
	record.Content = *ref
	encoded, err := json.Marshal(record)
	if err != nil {
		return nil, err
	}
	// A string envelope retains exact int64 revisions through Ent JSON maps.
	return map[string]any{"bot_record_v2": string(encoded)}, nil
}

func (c *botContentCodec) decodeGoal(ctx context.Context, id store.BotID, data map[string]any) (_ *store.BotGoal, resultErr error) {
	if c == nil || c.Open == nil {
		return nil, store.ErrBotUnsupported
	}
	var record botGoalRecord
	if err := decodeBotRecord(data, &record); err != nil {
		return nil, err
	}
	ref := record.Content
	if !record.valid() || record.BotID != id || ref.Validate() != nil ||
		ref.Scope.BotID != id || ref.Kind != store.BotContentDocument || ref.Revision != record.Revision {
		return nil, store.ErrBotInvalid
	}
	// The scoped repository checks the entire owner/org/team scope, not just BotID.
	disk, err := c.Open(ctx, id)
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
	hash := sha256.New()
	reader := &botGoalCountingReader{Reader: io.TeeReader(stream, hash)}
	var body botGoalContent
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	err = decoder.Decode(&body)
	if err == nil && decoder.Decode(new(any)) != io.EOF {
		err = store.ErrBotInvalid
	}
	if err = errors.Join(err, stream.Close()); err != nil {
		return nil, err
	}
	if reader.bytes != ref.SizeBytes || hex.EncodeToString(hash.Sum(nil)) != ref.SHA256 ||
		body.Format != record.Format || body.SchemaVersion != record.SchemaVersion ||
		body.ID != record.ID || body.BotID != id || body.Revision != record.Revision {
		return nil, store.ErrBotConflict
	}
	return &store.BotGoal{ID: record.ID, BotID: id, Revision: record.Revision, Title: record.Title,
		Description: body.Description, CompletionCriteria: body.CompletionCriteria, Status: record.Status,
		DueAt: record.DueAt, WorkspacePath: body.WorkspacePath, CompletedAt: record.CompletedAt, Evidence: body.Evidence}, nil
}

type botGoalCountingReader struct {
	io.Reader
	bytes int64
}

func (r *botGoalCountingReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.bytes += int64(n)
	return n, err
}
