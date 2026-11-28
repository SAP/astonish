package entstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"time"
	"unicode/utf8"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// Only bounded display text and coordination metadata may enter SQL.
type botTrackedRecord struct {
	Format   int                    `json:"format"`
	BotID    store.BotID            `json:"bot_id"`
	ID       uuid.UUID              `json:"id"`
	GoalID   uuid.UUID              `json:"goal_id"`
	Revision int64                  `json:"revision"`
	Title    string                 `json:"title"`
	Status   store.BotTrackedStatus `json:"status"`
	DueAt    *time.Time             `json:"due_at"`
	Content  store.BotContentRef    `json:"content"`
}

type botTrackedContent struct {
	Format   int                  `json:"format"`
	BotID    store.BotID          `json:"bot_id"`
	Revision int64                `json:"revision"`
	Item     store.BotTrackedItem `json:"item"`
}

func trackedRecord(botID store.BotID, item store.BotTrackedItem, revision int64) botTrackedRecord {
	return botTrackedRecord{Format: botDiskRecordFormat, BotID: botID, ID: item.ID, GoalID: item.GoalID, Revision: revision, Title: item.Title, Status: item.Status, DueAt: item.DueAt}
}
func (r botTrackedRecord) valid() bool {
	if r.Format != botDiskRecordFormat || r.BotID == uuid.Nil || r.ID == uuid.Nil || r.GoalID == uuid.Nil || r.Revision < 1 || len(r.Title) > 256 || !utf8.ValidString(r.Title) {
		return false
	}
	switch r.Status {
	case store.BotTrackedOpen, store.BotTrackedBlocked, store.BotTrackedCompleted, store.BotTrackedCancelled:
		return true
	}
	return false
}
func (c *botContentCodec) encodeTracked(ctx context.Context, botID store.BotID, item store.BotTrackedItem, revision int64) (map[string]any, error) {
	if c == nil || c.Open == nil {
		return nil, store.ErrBotUnsupported
	}
	record := trackedRecord(botID, item, revision)
	if !record.valid() {
		return nil, store.ErrBotInvalid
	}
	raw, err := json.Marshal(botTrackedContent{Format: record.Format, BotID: botID, Revision: revision, Item: item})
	if err != nil {
		return nil, err
	}
	disk, err := c.Open(ctx, botID)
	if err != nil {
		return nil, err
	}
	ref, putErr := disk.Put(ctx, store.BotContentDocument, revision, int64(len(raw)), bytes.NewReader(raw), func(commit func() error) error { return commit() })
	if err = errors.Join(putErr, disk.Close()); err != nil {
		return nil, err
	}
	hash := sha256.Sum256(raw)
	if ref == nil || ref.Validate() != nil || ref.Scope.BotID != botID || ref.Kind != store.BotContentDocument || ref.Revision != revision || ref.SizeBytes != int64(len(raw)) || ref.SHA256 != hex.EncodeToString(hash[:]) {
		return nil, store.ErrBotConflict
	}
	record.Content = *ref
	encoded, err := json.Marshal(record)
	if err != nil {
		return nil, err
	}
	return map[string]any{"bot_record_v2": string(encoded)}, nil
}
func (c *botContentCodec) decodeTracked(ctx context.Context, botID store.BotID, revision int64, data map[string]any) (_ *store.BotTrackedItem, resultErr error) {
	if c == nil || c.Open == nil {
		return nil, store.ErrBotUnsupported
	}
	var record botTrackedRecord
	if err := decodeBotRecord(data, &record); err != nil {
		return nil, err
	}
	ref := record.Content
	if !record.valid() || record.BotID != botID || record.Revision != revision || ref.Validate() != nil || ref.Scope.BotID != botID || ref.Kind != store.BotContentDocument || ref.Revision != revision {
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
	hash := sha256.New()
	reader := &botGoalCountingReader{Reader: io.TeeReader(io.LimitReader(stream, ref.SizeBytes+1), hash)}
	var body botTrackedContent
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	err = decoder.Decode(&body)
	if err == nil && decoder.Decode(new(any)) != io.EOF {
		err = store.ErrBotInvalid
	}
	if err = errors.Join(err, stream.Close()); err != nil {
		return nil, err
	}
	expected := trackedRecord(body.BotID, body.Item, body.Revision)
	expected.Content = ref
	if reader.bytes != ref.SizeBytes || hex.EncodeToString(hash.Sum(nil)) != ref.SHA256 || body.Format != record.Format || !reflect.DeepEqual(record, expected) {
		return nil, store.ErrBotConflict
	}
	return &body.Item, nil
}
