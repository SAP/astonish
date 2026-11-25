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

	"github.com/google/uuid"

	"github.com/SAP/astonish/pkg/store"
)

// botTaskRevisionRecord is the SQL coordination allowlist. The manifest and
// exact bundle bytes used for replay are never included in the SQL envelope.
type botTaskRevisionRecord struct {
	Format        int                 `json:"format"`
	SchemaVersion int                 `json:"schema_version"`
	ID            uuid.UUID           `json:"id"`
	BotID         store.BotID         `json:"bot_id"`
	Ref           store.BotTaskRef    `json:"ref"`
	Parent        *store.BotTaskRef   `json:"parent"`
	CreatedAt     time.Time           `json:"created_at"`
	Content       store.BotContentRef `json:"content"`
}

// No omitempty fields: nil and empty collections are distinct hydrated values.
// Parameter bytes are encoded as bytes rather than JSON values so even original
// whitespace and integers above 2^53 survive the archival boundary unchanged.
type botTaskManifestContent struct {
	SchemaVersion     int                       `json:"schema_version"`
	BotID             store.BotID               `json:"bot_id"`
	TaskID            uuid.UUID                 `json:"task_id"`
	Title             string                    `json:"title"`
	Assignment        string                    `json:"assignment"`
	OwnerRequirements []string                  `json:"owner_requirements"`
	Assumptions       []string                  `json:"assumptions"`
	Parameters        map[string][]byte         `json:"parameters"`
	ResearchMode      store.BotTaskResearchMode `json:"research_mode"`
	Goal              *store.BotTaskGoalRef     `json:"goal"`
	Skills            []store.BotTaskSkillRef   `json:"skills"`
	OutputRules       []string                  `json:"output_rules"`
	Assets            []store.BotTaskAsset      `json:"assets"`
}

type botTaskRevisionContent struct {
	Format        int                    `json:"format"`
	SchemaVersion int                    `json:"schema_version"`
	ID            uuid.UUID              `json:"id"`
	BotID         store.BotID            `json:"bot_id"`
	Ref           store.BotTaskRef       `json:"ref"`
	Manifest      botTaskManifestContent `json:"manifest"`
	Bundle        map[string][]byte      `json:"bundle"`
}

func (r botTaskRevisionRecord) valid() bool {
	return r.Format == botDiskRecordFormat && r.SchemaVersion == store.BotTaskSchemaVersion &&
		r.ID != uuid.Nil && r.BotID != uuid.Nil && r.Ref.Validate() == nil &&
		(r.Parent == nil || (r.Parent.Validate() == nil && r.Parent.TaskID == r.Ref.TaskID && r.Parent.Revision < r.Ref.Revision))
}

func (c *botContentCodec) encodeTaskRevision(ctx context.Context, v store.BotTaskRevision) (map[string]any, error) {
	if c == nil || c.Open == nil {
		return nil, store.ErrBotUnsupported
	}
	// Draft validation verifies identity, parent, limits, assets and the logical
	// content digest without imposing a lifecycle state on this immutable codec.
	if err := v.Validate(store.BotTaskDraft); err != nil {
		return nil, err
	}
	record := botTaskRevisionRecord{Format: botDiskRecordFormat, SchemaVersion: v.SchemaVersion,
		ID: v.ID, BotID: v.BotID, Ref: v.Ref, Parent: v.Parent, CreatedAt: v.CreatedAt}
	m := v.Manifest
	manifest := botTaskManifestContent{SchemaVersion: m.SchemaVersion, BotID: m.BotID, TaskID: m.TaskID,
		Title: m.Title, Assignment: m.Assignment, OwnerRequirements: m.OwnerRequirements,
		Assumptions: m.Assumptions, ResearchMode: m.ResearchMode, Goal: m.Goal,
		Skills: m.Skills, OutputRules: m.OutputRules, Assets: m.Assets}
	if m.Parameters != nil {
		manifest.Parameters = make(map[string][]byte, len(m.Parameters))
		for key, value := range m.Parameters {
			manifest.Parameters[key] = value
		}
	}
	body := botTaskRevisionContent{Format: record.Format, SchemaVersion: v.SchemaVersion,
		ID: v.ID, BotID: v.BotID, Ref: v.Ref, Manifest: manifest, Bundle: v.Bundle}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	disk, err := c.Open(ctx, v.BotID)
	if err != nil {
		return nil, err
	}
	ref, putErr := disk.Put(ctx, store.BotContentTask, v.Ref.Revision, int64(len(raw)), bytes.NewReader(raw), func(commit func() error) error { return commit() })
	if err = errors.Join(putErr, disk.Close()); err != nil {
		return nil, err
	}
	digest := sha256.Sum256(raw)
	if ref == nil || ref.Validate() != nil || ref.Scope.BotID != v.BotID || ref.Kind != store.BotContentTask ||
		ref.Revision != v.Ref.Revision || ref.SizeBytes != int64(len(raw)) || ref.SHA256 != hex.EncodeToString(digest[:]) {
		return nil, store.ErrBotConflict
	}
	record.Content = *ref
	encoded, err := json.Marshal(record)
	if err != nil {
		return nil, err
	}
	return map[string]any{"bot_record_v2": string(encoded)}, nil
}

func (c *botContentCodec) decodeTaskRevision(ctx context.Context, botID store.BotID, data map[string]any) (_ *store.BotTaskRevision, resultErr error) {
	if c == nil || c.Open == nil {
		return nil, store.ErrBotUnsupported
	}
	var record botTaskRevisionRecord
	if err := decodeBotRecord(data, &record); err != nil {
		return nil, err
	}
	ref := record.Content
	if !record.valid() || record.BotID != botID || ref.Validate() != nil || ref.Scope.BotID != botID ||
		ref.Kind != store.BotContentTask || ref.Revision != record.Ref.Revision {
		return nil, store.ErrBotInvalid
	}
	// The repository validates the full owner/org/team scope on every read.
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
	reader := &botTaskContentReader{Reader: io.TeeReader(stream, hash)}
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	var body botTaskRevisionContent
	err = decoder.Decode(&body)
	if err == nil && decoder.Decode(new(any)) != io.EOF {
		err = store.ErrBotInvalid
	}
	if err = errors.Join(err, stream.Close()); err != nil {
		return nil, err
	}
	if reader.bytes != ref.SizeBytes || hex.EncodeToString(hash.Sum(nil)) != ref.SHA256 ||
		body.Format != record.Format || body.SchemaVersion != record.SchemaVersion ||
		body.ID != record.ID || body.BotID != botID || body.Ref != record.Ref {
		return nil, store.ErrBotConflict
	}
	m := body.Manifest
	manifest := store.BotTaskManifest{SchemaVersion: m.SchemaVersion, BotID: m.BotID, TaskID: m.TaskID,
		Title: m.Title, Assignment: m.Assignment, OwnerRequirements: m.OwnerRequirements,
		Assumptions: m.Assumptions, ResearchMode: m.ResearchMode, Goal: m.Goal,
		Skills: m.Skills, OutputRules: m.OutputRules, Assets: m.Assets}
	if m.Parameters != nil {
		manifest.Parameters = make(map[string]json.RawMessage, len(m.Parameters))
		for key, value := range m.Parameters {
			manifest.Parameters[key] = value
		}
	}
	v := &store.BotTaskRevision{SchemaVersion: record.SchemaVersion, ID: record.ID, BotID: botID,
		Ref: record.Ref, Parent: record.Parent, CreatedAt: record.CreatedAt, Manifest: manifest, Bundle: body.Bundle}
	// Disk object hashes alone cannot prove the pinned logical revision digest.
	if err := v.Validate(store.BotTaskDraft); err != nil {
		return nil, err
	}
	return v, nil
}

type botTaskContentReader struct {
	io.Reader
	bytes int64
}

func (r *botTaskContentReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.bytes += int64(n)
	return n, err
}
