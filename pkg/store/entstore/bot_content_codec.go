package entstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

var errBotDevelopmentContentFormat = fmt.Errorf("%w: unsupported development Bot content format; delete the development Bot explicitly and create a new one", store.ErrBotUnsupported)

// botRuntimeRecordFormat is the single on-record format marker for database-backed
// runtime records (session events, runs, activities, approvals). Their bodies live
// in SQL; workspace/configuration documents remain file-based (botDiskRecordFormat).
const botRuntimeRecordFormat = 3

// botContentCodec is installed on concrete scoped database adapters, including
// transactions. It never changes generic botJSONMap or ordinary Chat storage.
type botContentCodec struct {
	RuntimeScope func(context.Context, store.BotID) (store.BotContentScope, error)
	Open         func(context.Context, store.BotID) (store.BotContentStaging, error)
	OpenIntake   func(context.Context, uuid.UUID, uuid.UUID, store.BotScope) (store.BotContentStaging, error)
}

func (c *botContentCodec) encodeVersion(ctx context.Context, v store.BotVersion) (map[string]any, error) {
	if c == nil || c.Open == nil {
		return nil, store.ErrBotUnsupported
	}
	body := botVersionContent{Charter: v.Charter, Soul: v.Persona.Soul, Identity: v.Persona.Identity, User: v.Persona.User, Avatar: v.Persona.Avatar, Preferences: v.Delivery.Preferences, ChangeReason: v.ChangeReason}
	record := botVersionRecord{Format: botDiskRecordFormat, BotID: v.BotID, Version: v.Version, SchemaVersion: v.SchemaVersion, Autonomy: v.Autonomy, Budget: v.Budget, Notify: v.Delivery.Notify, Bindings: v.Delivery.Bindings, SandboxTemplate: v.SandboxTemplate, Language: v.Persona.Language, Timezone: v.Persona.Timezone, CreatedBy: v.CreatedBy, CreatedAt: v.CreatedAt}
	for _, t := range v.Triggers {
		record.Triggers = append(record.Triggers, botTriggerRecord{ID: t.ID, TaskID: t.TaskID, SkillID: t.SkillID, Kind: t.Kind, ScheduleID: t.ScheduleID, GoalID: t.GoalID, Cron: t.Cron, Timezone: t.Timezone, Enabled: t.Enabled, HookStatus: t.HookStatus, CredentialRef: t.CredentialRef, Fingerprint: t.Fingerprint, CatchUpOnce: t.CatchUpOnce, Poll: t.Poll, HookWatermark: t.HookWatermark, ChannelBinding: t.ChannelBinding})
		body.Triggers = append(body.Triggers, botTriggerContent{ID: t.ID, Instructions: t.Instructions, Invocation: t.Invocation})
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	disk, err := c.Open(ctx, v.BotID)
	if err != nil {
		return nil, err
	}
	ref, putErr := disk.Put(ctx, store.BotContentDocument, v.Version, int64(len(raw)), bytes.NewReader(raw), func(commit func() error) error { return commit() })
	closeErr := disk.Close()
	if err = errors.Join(putErr, closeErr); err != nil {
		return nil, err
	}
	if ref == nil || ref.Validate() != nil || ref.Scope.BotID != v.BotID || ref.Kind != store.BotContentDocument || ref.Revision != v.Version {
		return nil, store.ErrBotConflict
	}
	record.Content = *ref
	// Preserve exact integers via a typed metadata envelope, not float64 maps.
	encoded, err := json.Marshal(record)
	if err != nil {
		return nil, err
	}
	return map[string]any{"bot_record_v2": string(encoded)}, nil
}

func decodeBotRecord(data map[string]any, out any) error {
	if len(data) != 1 {
		return errBotDevelopmentContentFormat
	}
	value, ok := data["bot_record_v2"].(string)
	if !ok {
		return errBotDevelopmentContentFormat
	}
	decoder := json.NewDecoder(bytes.NewBufferString(value))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return fmt.Errorf("%w: %v", store.ErrBotInvalid, err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return store.ErrBotInvalid
	}
	return nil
}
func (c *botContentCodec) decodeVersion(ctx context.Context, id store.BotID, version int64, data map[string]any) (*store.BotVersion, error) {
	if c == nil || c.Open == nil {
		return nil, store.ErrBotUnsupported
	}
	var record botVersionRecord
	if err := decodeBotRecord(data, &record); err != nil {
		return nil, err
	}
	ref := record.Content
	if record.Format != botDiskRecordFormat || record.BotID != id || record.Version != version || record.SchemaVersion != store.BotSchemaVersion || ref.Validate() != nil || ref.Scope.BotID != id || ref.Kind != store.BotContentDocument || ref.Revision != version {
		return nil, store.ErrBotInvalid
	}
	disk, err := c.Open(ctx, id)
	if err != nil {
		return nil, err
	}
	defer disk.Close()
	stream, err := disk.Open(ctx, ref, store.BotFileRange{})
	if err != nil {
		return nil, err
	}
	// Version bodies are configuration documents. Ref size is checked by storage;
	// decoding streams them directly and never substitutes newer working files.
	var body botVersionContent
	decoder := json.NewDecoder(stream)
	decoder.DisallowUnknownFields()
	err = decoder.Decode(&body)
	if err == nil && decoder.Decode(new(any)) != io.EOF {
		err = store.ErrBotInvalid
	}
	closeErr := stream.Close()
	if err = errors.Join(err, closeErr); err != nil {
		return nil, err
	}
	if len(body.Triggers) != len(record.Triggers) {
		return nil, store.ErrBotConflict
	}
	v := &store.BotVersion{BotID: id, Version: version, SchemaVersion: record.SchemaVersion, Charter: body.Charter, Persona: store.BotPersona{Language: record.Language, Timezone: record.Timezone, Soul: body.Soul, Identity: body.Identity, User: body.User, Avatar: body.Avatar}, Autonomy: record.Autonomy, Budget: record.Budget, Delivery: store.DeliveryPolicy{Notify: record.Notify, Bindings: record.Bindings, Preferences: body.Preferences}, SandboxTemplate: record.SandboxTemplate, CreatedBy: record.CreatedBy, CreatedAt: record.CreatedAt, ChangeReason: body.ChangeReason}
	for i, t := range record.Triggers {
		text := body.Triggers[i]
		if text.ID != t.ID {
			return nil, store.ErrBotConflict
		}
		v.Triggers = append(v.Triggers, store.TriggerSpec{ID: t.ID, TaskID: t.TaskID, SkillID: t.SkillID, Kind: t.Kind, ScheduleID: t.ScheduleID, GoalID: t.GoalID, Cron: t.Cron, Timezone: t.Timezone, Enabled: t.Enabled, HookStatus: t.HookStatus, CredentialRef: t.CredentialRef, Fingerprint: t.Fingerprint, CatchUpOnce: t.CatchUpOnce, Poll: t.Poll, HookWatermark: t.HookWatermark, ChannelBinding: t.ChannelBinding, Instructions: text.Instructions, Invocation: text.Invocation})
	}
	return v, nil
}

func newBotContentCodec(directory string, scope store.BotContentScope) *botContentCodec {
	return &botContentCodec{RuntimeScope: func(ctx context.Context, id store.BotID) (store.BotContentScope, error) {
		if err := ctx.Err(); err != nil {
			return store.BotContentScope{}, err
		}
		exact := scope
		exact.BotID = id
		return exact, nil
	}, OpenIntake: newBotIntakeOpener(directory, scope.OrgID, scope.TeamID), Open: func(ctx context.Context, id store.BotID) (store.BotContentStaging, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if id == uuid.Nil {
			return nil, store.ErrBotInvalid
		}
		exact := scope
		exact.BotID = id
		return newBotDiskContent(directory, exact)
	}}
}

// Runtime records (session events, runs, activities, approvals) are stored as
// database bytes. Workspace/configuration documents remain file-based on disk.
type botRuntimeInline struct {
	Scope     store.BotContentScope `json:"runtime_scope,omitempty"`
	Inline    []byte                `json:"inline,omitempty"`
	SizeBytes int64                 `json:"size_bytes,omitempty"`
	SHA256    string                `json:"sha256,omitempty"`
}

func runtimeInline(raw []byte) botRuntimeInline {
	sum := sha256.Sum256(raw)
	return botRuntimeInline{Inline: raw, SizeBytes: int64(len(raw)), SHA256: hex.EncodeToString(sum[:])}
}
func validateRuntimePayload(in botRuntimeInline) error {
	if in.Inline == nil || in.SizeBytes != int64(len(in.Inline)) || in.SizeBytes < 1 {
		return store.ErrBotInvalid
	}
	sum := sha256.Sum256(in.Inline)
	if in.SHA256 != hex.EncodeToString(sum[:]) {
		return store.ErrBotConflict
	}
	return nil
}
func (c *botContentCodec) encodeRuntimeRecord(ctx context.Context, id store.BotID, record any) (map[string]any, error) {
	var inline *botRuntimeInline
	switch v := record.(type) {
	case *botSessionEventRecord:
		inline = &v.botRuntimeInline
	case *botActivityRecord:
		inline = &v.botRuntimeInline
	case *botRunRecord:
		inline = &v.botRuntimeInline
	case *botApprovalRecord:
		inline = &v.botRuntimeInline
	default:
		return nil, store.ErrBotInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c != nil && c.RuntimeScope != nil {
		scope, err := c.RuntimeScope(ctx, id)
		if err != nil {
			return nil, err
		}
		inline.Scope = scope
	}
	return encodeRuntimeRecord(record)
}
func encodeRuntimeRecord(record any) (map[string]any, error) {
	raw, err := json.Marshal(record)
	if err != nil {
		return nil, err
	}
	return map[string]any{"bot_record_v2": string(raw)}, nil
}
func (c *botContentCodec) readRuntimeBody(ctx context.Context, id store.BotID, in botRuntimeInline) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c != nil && c.RuntimeScope != nil {
		scope, err := c.RuntimeScope(ctx, id)
		if err != nil {
			return nil, err
		}
		if scope != in.Scope {
			return nil, store.ErrBotForbidden
		}
	}
	return bytes.Clone(in.Inline), nil
}
