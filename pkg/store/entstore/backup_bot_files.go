package entstore

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	personalsession "github.com/SAP/astonish/ent/personal/session"
	teamsession "github.com/SAP/astonish/ent/team/session"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// These codecs describe content, not authority. Adding a content-bearing DTO
// requires an explicit codec/edge here; never discover refs by walking raw JSON.
type botBackupCodec string

const (
	botBackupLeaf     botBackupCodec = "bytes"
	botBackupRun      botBackupCodec = "run-v2"
	botBackupActivity botBackupCodec = "activity-v2"
	botBackupSession  botBackupCodec = "session-v2"
	botBackupMemory   botBackupCodec = "memory-v1"
	botBackupApproval botBackupCodec = "approval-v2"
)
const botBackupMaxObjects = 100000
const botBackupMaxTypedBytes int64 = 64 << 20

type botBackupObject struct {
	Ref   store.BotContentRef `json:"ref"`
	Codec botBackupCodec      `json:"codec"`
}

// Scope membership is supplied by trusted inventory callers, never derived from
// archive refs. Both staging and retained scopes may be listed for the same Bot.
type botBackupInventory struct {
	allowed map[store.BotContentScope]bool
	objects []botBackupObject
	seen    map[string]botBackupObject
}

func newBotBackupInventory(scopes []store.BotContentScope) (*botBackupInventory, error) {
	i := &botBackupInventory{allowed: make(map[store.BotContentScope]bool), seen: make(map[string]botBackupObject)}
	for _, scope := range scopes {
		if scope.Validate() != nil {
			return nil, store.ErrBotInvalid
		}
		i.allowed[scope] = true
	}
	if len(i.allowed) == 0 {
		return nil, store.ErrBotInvalid
	}
	return i, nil
}
func botBackupIdentity(ref store.BotContentRef) string {
	raw, _ := json.Marshal(ref.Scope)
	return string(raw) + "/" + ref.Key
}
func validateBotBackupObject(o botBackupObject) error {
	r := o.Ref
	if r.Validate() != nil || len(r.Key) > 512 || r.SHA256 != strings.ToLower(r.SHA256) {
		return store.ErrBotInvalid
	}
	if strings.HasPrefix(r.Key, "staged-v1/") {
		if _, err := diskContentName(r); err != nil {
			return err
		}
	} else {
		prefix := fmt.Sprintf("content-v1/%s-%d-", r.Kind, r.Revision)
		suffix := strings.TrimPrefix(r.Key, prefix)
		raw, err := hex.DecodeString(suffix)
		if suffix == r.Key || len(raw) != 16 || err != nil || suffix != strings.ToLower(suffix) {
			return store.ErrBotInvalid
		}
	}
	switch o.Codec {
	case botBackupLeaf:
	case botBackupRun, botBackupApproval:
		if r.Kind != store.BotContentCheckpoint {
			return store.ErrBotInvalid
		}
	case botBackupActivity:
		if r.Kind != store.BotContentEvidence && r.Kind != store.BotContentArtifact {
			return store.ErrBotInvalid
		}
	case botBackupSession:
		if r.Kind != store.BotContentEvidence {
			return store.ErrBotInvalid
		}
	case botBackupMemory:
		if r.Kind != store.BotContentMemory {
			return store.ErrBotInvalid
		}
	default:
		return store.ErrBotUnsupported
	}
	if o.Codec != botBackupLeaf && r.SizeBytes > botBackupMaxTypedBytes {
		return fmt.Errorf("typed Bot backup object exceeds %d bytes: %w", botBackupMaxTypedBytes, store.ErrBotInvalid)
	}
	return nil
}
func (i *botBackupInventory) add(o botBackupObject) error {
	if err := validateBotBackupObject(o); err != nil {
		return err
	}
	if !i.allowed[o.Ref.Scope] {
		return store.ErrBotForbidden
	}
	key := botBackupIdentity(o.Ref)
	if prior, ok := i.seen[key]; ok {
		if prior != o {
			return store.ErrBotConflict
		}
		return nil
	}
	if len(i.objects) >= botBackupMaxObjects {
		return store.ErrBotInvalid
	}
	i.seen[key] = o
	i.objects = append(i.objects, o)
	return nil
}

// rootsFromHome uses only the explicit SQL envelope allowlist. The caller must
// collect every physical row under its SQL/writer fence, including session rows.
func (i *botBackupInventory) rootsFromHome(botID store.BotID, rows []botHomeConfigurationRecord) error {
	for _, row := range rows {
		record, ref, _, err := homeRecordEnvelope(row)
		if err != nil {
			return err
		}
		encoded, err := json.Marshal(record)
		if err != nil {
			return err
		}
		var header struct {
			Format int `json:"format"`
		}
		if json.Unmarshal(encoded, &header) != nil {
			return store.ErrBotInvalid
		}
		if ref == nil {
			if header.Format == botDiskRecordFormat {
				continue
			}
			if err := i.inlineRoots(botID, row, record); err != nil {
				return err
			}
			continue
		}
		if header.Format != botDiskRecordFormat {
			return store.ErrBotInvalid
		}
		if ref.Scope.BotID != botID {
			return store.ErrBotForbidden
		}
		codec := botBackupLeaf
		switch row.kind {
		case botHomeApproval:
			codec = botBackupApproval
		case botHomeRun:
			codec = botBackupRun
		case botHomeActivity:
			codec = botBackupActivity
		case botHomeSessionEvent:
			codec = botBackupSession
		}
		if err := i.add(botBackupObject{Ref: *ref, Codec: codec}); err != nil {
			return err
		}
	}
	return nil
}
func (i *botBackupInventory) inlineRoots(id store.BotID, row botHomeConfigurationRecord, record any) error {
	var in botRuntimeInline
	var codec botBackupCodec
	switch v := record.(type) {
	case *botRunRecord:
		in, codec = v.botRuntimeInline, botBackupRun
	case *botActivityRecord:
		in, codec = v.botRuntimeInline, botBackupActivity
	case *botApprovalRecord:
		in = v.botRuntimeInline
	case *botSessionEventRecord:
		in = v.botRuntimeInline
	default:
		return store.ErrBotUnsupported
	}
	if !i.allowed[in.Scope] || in.Scope.BotID != id {
		return store.ErrBotForbidden
	}
	if in.SizeBytes > botBackupMaxTypedBytes {
		return store.ErrBotInvalid
	}
	// Logical readers validate the full union, digest and identity without disk IO.
	c := &botContentCodec{RuntimeScope: func(context.Context, store.BotID) (store.BotContentScope, error) { return in.Scope, nil }}
	if err := validateHomeRecord(context.Background(), c, id, row); err != nil {
		return err
	}
	if codec == "" {
		return nil
	}
	// Reuse only the explicit report/candidate artifact edge decoder. This is
	// not an external root and its synthetic ref is never added to inventory.
	return i.children(botBackupObject{Ref: store.BotContentRef{Scope: in.Scope}, Codec: codec}, bytes.NewReader(in.Inline))
}

func (i *botBackupInventory) rootsFromMemory(botID store.BotID, metadata map[string]any) error {
	value, ok := metadata[botMemoryHeadKey]
	if !ok {
		return nil
	}
	var ref store.BotContentRef
	if err := botBackupDecodeValue(value, &ref); err != nil {
		return err
	}
	if ref.Scope.BotID != botID {
		return store.ErrBotForbidden
	}
	return i.add(botBackupObject{Ref: ref, Codec: botBackupMemory})
}

// Home/run writers persist app_name; child-session creation additionally writes
// bot_initial_state. User/team ownership lives in the row and trusted Bot scope,
// not metadata. Ordinary Chat metadata does not pass through this allowlist.
type botBackupSessionMetadata struct {
	AppName      string `json:"app_name,omitempty"`
	InitialState *struct {
		Record string `json:"bot_record_v2"`
	} `json:"bot_initial_state,omitempty"`
}

func (i *botBackupInventory) rootsFromSession(botID store.BotID, sessionID string, metadata map[string]any) error {
	var typed botBackupSessionMetadata
	if err := botBackupDecodeValue(metadata, &typed); err != nil {
		return fmt.Errorf("Bot session metadata: %w", store.ErrBotInvalid)
	}
	// Reject explicit nulls instead of treating malformed fields as absent.
	for key, value := range metadata {
		if (key != "app_name" && key != "bot_initial_state") || value == nil {
			return store.ErrBotInvalid
		}
	}
	if !validBotSessionEventIdentity(botID, sessionID, "initial-state") || len(typed.AppName) > 256 {
		return store.ErrBotInvalid
	}
	for _, c := range typed.AppName {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' || c == '.') {
			return store.ErrBotInvalid
		}
	}
	if typed.InitialState == nil {
		return nil
	}
	// Bound inline metadata separately from legacy reference-only envelopes.
	if len(typed.InitialState.Record) > int(botBackupMaxTypedBytes)*2 {
		return store.ErrBotInvalid
	}
	var envelope map[string]any
	if err := botBackupDecodeValue(metadata["bot_initial_state"], &envelope); err != nil {
		return store.ErrBotInvalid
	}
	record, err := readBotSessionEventRecord(botID, sessionID, envelope)
	if err != nil {
		return err
	}
	if record.EventID != "initial-state" {
		return store.ErrBotInvalid
	}
	return i.inlineRoots(botID, botHomeConfigurationRecord{kind: botHomeSessionEvent, sessionID: sessionID, data: envelope}, record)
}
func (i *botBackupInventory) rootFromIndependent(id uuid.UUID, scope store.BotContentScope, kind store.BotContentKind, revision int64, data map[string]any) error {
	if kind != store.BotContentTemplate && kind != store.BotContentIntake {
		return store.ErrBotInvalid
	}
	var record botIndependentRecord
	if err := decodeBotRecord(data, &record); err != nil {
		return err
	}
	if record.Format != botDiskRecordFormat || id == uuid.Nil || record.ID != id || record.Content.Scope != scope || record.Content.Kind != kind || record.Content.Revision != revision {
		return store.ErrBotInvalid
	}
	return i.add(botBackupObject{Ref: record.Content, Codec: botBackupLeaf})
}
func botBackupDecodeValue(value, out any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return botBackupDecode(bytes.NewReader(raw), out)
}
func botBackupDecode(r io.Reader, out any) error {
	d := json.NewDecoder(r)
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return err
	}
	if d.Decode(new(any)) != io.EOF {
		return store.ErrBotInvalid
	}
	return nil
}

// children decodes named content DTOs. Arguments, checkpoint JSON, transcript
// tool results, private receipts, mandate refs and source paths are data, never
// interpreted as object addresses. BotArtifact.ContentRef is the typed edge.
func (i *botBackupInventory) children(o botBackupObject, r io.Reader) error {
	var artifacts []store.BotArtifact
	switch o.Codec {
	case botBackupLeaf:
		return nil
	case botBackupRun:
		var body botRunContent
		if err := botBackupDecode(r, &body); err != nil {
			return err
		}
		// Runtime run records are database-only, decoded from inlineRoots'
		// synthetic (non-object) descriptor.
		if body.Format != botRuntimeRecordFormat || body.Stored.Run.BotID != o.Ref.Scope.BotID {
			return store.ErrBotInvalid
		}
		artifacts = append(artifacts, body.Stored.Run.Report.Artifacts...)
		if w := body.Stored.Work; w != nil && w.Candidate != nil {
			artifacts = append(artifacts, w.Candidate.Artifacts...)
		}
	case botBackupActivity:
		var body botActivityContent
		if err := botBackupDecode(r, &body); err != nil {
			return err
		}
		if body.Activity.BotID != o.Ref.Scope.BotID {
			return store.ErrBotInvalid
		}
		// Outcomes and publication receipts share ActivityReport; these are named
		// payload projections, not recursive untyped JSON reference searches.
		if body.Activity.Kind == store.BotActivityReport {
			var payload struct {
				Report    store.BotReport     `json:"report"`
				Artifacts []store.BotArtifact `json:"artifacts"`
			}
			if err := json.Unmarshal(body.Activity.Payload, &payload); err != nil {
				return err
			}
			artifacts = append(artifacts, payload.Report.Artifacts...)
			artifacts = append(artifacts, payload.Artifacts...)
		}
	case botBackupApproval:
		var body botApprovalContent
		if err := botBackupDecode(r, &body); err != nil {
			return err
		}
		if body.BotID != o.Ref.Scope.BotID || body.Version != o.Ref.Revision {
			return store.ErrBotInvalid
		}
		// Action arguments and target snapshots are exact opaque bytes, not a
		// registry of content refs; interpreting model JSON here grants authority.
	case botBackupSession:
		var body botSessionEventContent
		if err := botBackupDecode(r, &body); err != nil {
			return err
		}
		if body.BotID != o.Ref.Scope.BotID || body.Event == nil {
			return store.ErrBotInvalid
		}
		// FilePublication state is a public projection with artifact IDs, not refs.
	case botBackupMemory:
		var body botDiskMemoryCatalogue
		if err := botBackupDecode(r, &body); err != nil {
			return err
		}
		if body.Format != 1 || body.BotID != o.Ref.Scope.BotID {
			return store.ErrBotInvalid
		}
	default:
		return store.ErrBotUnsupported
	}
	for _, a := range artifacts {
		if a.ContentRef == nil {
			return fmt.Errorf("artifact has no immutable content: %w", store.ErrBotUnsupported)
		}
		ref := *a.ContentRef
		if ref.Scope.BotID != o.Ref.Scope.BotID || ref.Scope.OwnerID != o.Ref.Scope.OwnerID || ref.Kind != store.BotContentArtifact || ref.SHA256 != a.SHA256 || ref.SizeBytes != a.SizeBytes {
			return store.ErrBotInvalid
		}
		if err := i.add(botBackupObject{Ref: ref, Codec: botBackupLeaf}); err != nil {
			return err
		}
	}
	return nil
}

// collectBotBackupRoots reads through the existing scoped DB adapters. It does
// not acquire a fence itself: the export options' Collect callback must invoke it
// inside the same writer/attachment fence as export. Independent template/intake
// roots are added separately, since they have no Bot aggregate ownership.
func collectBotBackupRoots(ctx context.Context, db botDraftDB, botID store.BotID, inv *botBackupInventory) error {
	adapter, ok := db.(botHomeConfigurationDB)
	if !ok {
		return store.ErrBotUnsupported
	}
	root, err := db.GetBot(ctx, botID)
	if err != nil {
		return err
	}
	rows, err := adapter.homeConfigurationRecords(ctx, botID)
	if err != nil {
		return err
	}
	if err = inv.rootsFromHome(botID, rows); err != nil {
		return err
	}
	if err = inv.rootsFromMemory(botID, root.Metadata); err != nil {
		return err
	}
	switch d := db.(type) {
	case personalBotDB:
		sessions, err := d.client.Session.Query().Where(personalsession.BotIDEQ(botID)).All(ctx)
		if err != nil {
			return err
		}
		for _, session := range sessions {
			if session.BotID != nil && *session.BotID == botID {
				if session.UserID == nil || *session.UserID != root.Bot.OwnerID {
					return store.ErrBotForbidden
				}
				if err = inv.rootsFromSession(botID, session.ID, session.Metadata); err != nil {
					return err
				}
			}
		}
	case teamBotDB:
		sessions, err := d.client.Session.Query().Where(teamsession.BotIDEQ(botID)).All(ctx)
		if err != nil {
			return err
		}
		for _, session := range sessions {
			if session.BotID != nil && *session.BotID == botID {
				if session.UserID == nil || *session.UserID != root.Bot.OwnerID {
					return store.ErrBotForbidden
				}
				if err = inv.rootsFromSession(botID, session.ID, session.Metadata); err != nil {
					return err
				}
			}
		}
	default:
		return store.ErrBotUnsupported
	}
	return nil
}

// String form is useful only for diagnostics; no path is resolved from it.
func (o botBackupObject) label() string {
	return string(o.Ref.Kind) + " revision " + strconv.FormatInt(o.Ref.Revision, 10)
}
