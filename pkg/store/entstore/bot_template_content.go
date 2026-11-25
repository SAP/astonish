package entstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"

	teament "github.com/SAP/astonish/ent/team"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// Independently owned documents never use a source Bot's namespace.
type botIndependentContent struct {
	Open  func(context.Context) (store.BotContentStaging, error)
	Scope store.BotContentScope
}

type botIndependentRecord struct {
	Format  int                 `json:"format"`
	ID      uuid.UUID           `json:"id"`
	Content store.BotContentRef `json:"content"`
}

func newBotTemplateContent(directory, org, team string) *botIndependentContent {
	scope := store.BotContentScope{OrgID: org, TeamID: team}
	return &botIndependentContent{Scope: scope, Open: func(ctx context.Context) (store.BotContentStaging, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if directory == "" {
			return nil, store.ErrBotUnsupported
		}
		return newBotDiskContent(directory, scope)
	}}
}

func (c *botIndependentContent) encode(ctx context.Context, id uuid.UUID, kind store.BotContentKind, revision int64, body any) (map[string]any, error) {
	if c == nil || c.Open == nil {
		return nil, store.ErrBotUnsupported
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	disk, err := c.Open(ctx)
	if err != nil {
		return nil, err
	}
	ref, putErr := disk.Put(ctx, kind, revision, int64(len(raw)), bytes.NewReader(raw), func(commit func() error) error { return commit() })
	if err := errors.Join(putErr, disk.Close()); err != nil {
		return nil, err
	}
	if ref == nil || ref.Validate() != nil || ref.Scope != c.Scope || ref.Kind != kind || ref.Revision != revision {
		return nil, store.ErrBotConflict
	}
	encoded, err := json.Marshal(botIndependentRecord{Format: botDiskRecordFormat, ID: id, Content: *ref})
	if err != nil {
		return nil, err
	}
	return map[string]any{"bot_record_v2": string(encoded)}, nil
}

func (c *botIndependentContent) decode(ctx context.Context, id uuid.UUID, kind store.BotContentKind, revision int64, data map[string]any, out any) error {
	var record botIndependentRecord
	if err := decodeBotRecord(data, &record); err != nil {
		return err
	}
	if c == nil || c.Open == nil {
		return store.ErrBotUnsupported
	}
	ref := record.Content
	if record.Format != botDiskRecordFormat || record.ID != id || ref.Validate() != nil || ref.Scope != c.Scope || ref.Kind != kind || ref.Revision != revision {
		return store.ErrBotInvalid
	}
	disk, err := c.Open(ctx)
	if err != nil {
		return err
	}
	defer disk.Close()
	stream, err := disk.Open(ctx, ref, store.BotFileRange{})
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(stream)
	decoder.DisallowUnknownFields()
	err = decoder.Decode(out)
	if err == nil && decoder.Decode(new(any)) != io.EOF {
		err = store.ErrBotInvalid
	}
	return errors.Join(err, stream.Close())
}

func (s *teamBotTemplateStore) decodeBotTemplate(ctx context.Context, row *teament.BotTemplate) (*store.BotTemplate, error) {
	var def store.BotTemplateDefinition
	if err := s.content.decode(ctx, row.ID, store.BotContentTemplate, row.Revision, row.Definition, &def); err != nil {
		return nil, err
	}
	if err := validateBotTemplate(def); err != nil {
		return nil, err
	}
	if def.Name != row.Name {
		return nil, store.ErrBotInvalid
	}
	return &store.BotTemplate{ID: row.ID, Revision: row.Revision, Definition: def, PublishedBy: row.PublishedBy, PublishedAt: row.PublishedAt}, nil
}
