package entstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/SAP/astonish/pkg/store"
)

func botContentTaskFixture() store.BotTaskRevision {
	id, taskID := uuid.New(), uuid.New()
	asset := []byte("SENTINEL-task-bundle\x00\xff")
	sum := sha256.Sum256(asset)
	v := store.BotTaskRevision{SchemaVersion: store.BotTaskSchemaVersion, ID: uuid.New(), BotID: id,
		Ref:       store.BotTaskRef{TaskID: taskID, Revision: 1<<54 + 3},
		Parent:    &store.BotTaskRef{TaskID: taskID, Revision: 1<<54 + 1, Digest: strings.Repeat("a", 64)},
		CreatedAt: time.Date(2026, 2, 3, 4, 5, 6, 7, time.UTC),
		Manifest: store.BotTaskManifest{SchemaVersion: store.BotTaskSchemaVersion, BotID: id, TaskID: taskID,
			Title: "SENTINEL-task-title", Assignment: "SENTINEL-task-assignment",
			OwnerRequirements: []string{"SENTINEL-task-requirement"}, Assumptions: []string{"SENTINEL-task-assumption"},
			Parameters:   map[string]json.RawMessage{"SENTINEL-task-param": json.RawMessage(` { "n": 18014398509481987, "text": "SENTINEL-task-value" } `)},
			ResearchMode: store.BotTaskResearchProvidedSources,
			Goal:         &store.BotTaskGoalRef{BotID: id, GoalID: uuid.New(), Revision: 1<<54 + 5},
			Skills:       []store.BotTaskSkillRef{{BotID: id, SkillID: uuid.New(), Revision: 1<<54 + 7, Digest: strings.Repeat("b", 64)}},
			OutputRules:  []string{"SENTINEL-task-output"},
			Assets:       []store.BotTaskAsset{{Path: "TASK.md", Kind: store.BotTaskAssetDefinition, Digest: hex.EncodeToString(sum[:]), SizeBytes: int64(len(asset))}}},
		Bundle: map[string][]byte{"TASK.md": asset}}
	v.Ref.Digest = v.ContentDigest()
	return v
}

func botContentTaskEnvelope(t *testing.T, record botTaskRevisionRecord) map[string]any {
	t.Helper()
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	return map[string]any{"bot_record_v2": string(raw)}
}

func TestBotContentTaskRevisionRoundTrip(t *testing.T) {
	dir := t.TempDir()
	scope := store.BotContentScope{OwnerID: uuid.NewString(), OrgID: "org", TeamID: "team"}
	codec := newBotContentCodec(dir, scope)
	v := botContentTaskFixture()
	data, err := codec.encodeTaskRevision(t.Context(), v)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{v.Manifest.Title, v.Manifest.Assignment, v.Manifest.OwnerRequirements[0], v.Manifest.Assumptions[0], v.Manifest.OutputRules[0], "SENTINEL-task-value", "SENTINEL-task-param", string(v.Bundle["TASK.md"])} {
		quoted, _ := json.Marshal(value)
		if bytes.Contains(raw, []byte(value)) || bytes.Contains(raw, quoted) || bytes.Contains(raw, []byte(base64.StdEncoding.EncodeToString([]byte(value)))) {
			t.Fatal("content leaked into SQL envelope")
		}
	}
	var sql map[string]any
	if err := json.Unmarshal(raw, &sql); err != nil {
		t.Fatal(err)
	}
	got, err := newBotContentCodec(dir, scope).decodeTaskRevision(t.Context(), v.BotID, sql)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(v, *got) || v.ContentDigest() != got.ContentDigest() {
		t.Fatalf("hydrated revision changed: %#v", got)
	}
	// Preserve nil versus empty collection shapes, including empty bundle bytes.
	for _, empty := range []bool{false, true} {
		v.Manifest.OwnerRequirements, v.Manifest.Assumptions, v.Manifest.OutputRules, v.Manifest.Skills, v.Manifest.Assets = nil, nil, nil, nil, nil
		v.Manifest.Parameters, v.Manifest.Goal, v.Bundle, v.Parent = nil, nil, nil, nil
		if empty {
			v.Manifest.OwnerRequirements, v.Manifest.Assumptions, v.Manifest.OutputRules = []string{}, []string{}, []string{}
			v.Manifest.Skills, v.Manifest.Assets = []store.BotTaskSkillRef{}, []store.BotTaskAsset{}
			v.Manifest.Parameters, v.Bundle = map[string]json.RawMessage{}, map[string][]byte{}
		}
		v.Ref.Digest = v.ContentDigest()
		data, err := codec.encodeTaskRevision(t.Context(), v)
		if err != nil {
			t.Fatal(err)
		}
		got, err := codec.decodeTaskRevision(t.Context(), v.BotID, data)
		if err != nil || !reflect.DeepEqual(v, *got) {
			t.Fatalf("empty=%v: %#v, %v", empty, got, err)
		}
	}
}

func TestBotContentTaskRevisionRejectsMetadata(t *testing.T) {
	codec := newBotContentCodec(t.TempDir(), store.BotContentScope{OwnerID: uuid.NewString()})
	v := botContentTaskFixture()
	data, err := codec.encodeTaskRevision(t.Context(), v)
	if err != nil {
		t.Fatal(err)
	}
	var original botTaskRevisionRecord
	if err := decodeBotRecord(data, &original); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*botTaskRevisionRecord){
		"format":       func(r *botTaskRevisionRecord) { r.Format++ },
		"schema":       func(r *botTaskRevisionRecord) { r.SchemaVersion++ },
		"id":           func(r *botTaskRevisionRecord) { r.ID = uuid.New() },
		"bot":          func(r *botTaskRevisionRecord) { r.BotID = uuid.New() },
		"task":         func(r *botTaskRevisionRecord) { r.Ref.TaskID = uuid.New() },
		"digest":       func(r *botTaskRevisionRecord) { r.Ref.Digest = strings.Repeat("c", 64) },
		"revision":     func(r *botTaskRevisionRecord) { r.Ref.Revision++ },
		"parent":       func(r *botTaskRevisionRecord) { r.Parent = nil },
		"ref_format":   func(r *botTaskRevisionRecord) { r.Content.Format++ },
		"ref_kind":     func(r *botTaskRevisionRecord) { r.Content.Kind = store.BotContentMemory },
		"ref_revision": func(r *botTaskRevisionRecord) { r.Content.Revision++ },
		"ref_owner":    func(r *botTaskRevisionRecord) { r.Content.Scope.OwnerID = uuid.NewString() },
		"ref_team":     func(r *botTaskRevisionRecord) { r.Content.Scope.OrgID, r.Content.Scope.TeamID = "foreign", "foreign" },
		"ref_bot":      func(r *botTaskRevisionRecord) { r.Content.Scope.BotID = uuid.New() },
		"ref_digest":   func(r *botTaskRevisionRecord) { r.Content.SHA256 = strings.Repeat("0", 64) },
		"ref_size":     func(r *botTaskRevisionRecord) { r.Content.SizeBytes++ },
	} {
		t.Run(name, func(t *testing.T) {
			r := original
			mutate(&r)
			if _, err := codec.decodeTaskRevision(t.Context(), v.BotID, botContentTaskEnvelope(t, r)); err == nil {
				t.Fatal("invalid metadata accepted")
			}
		})
	}
	old, _ := botJSONMap(v)
	if _, err := codec.decodeTaskRevision(t.Context(), v.BotID, old); !errors.Is(err, errBotDevelopmentContentFormat) {
		t.Fatalf("legacy accepted: %v", err)
	}
	for _, bad := range []map[string]any{
		{"bot_record_v2": data["bot_record_v2"], "body": "SENTINEL"},
		{"bot_record_v2": strings.TrimSuffix(data["bot_record_v2"].(string), "}") + `,"manifest":{}}`},
		{"bot_record_v2": data["bot_record_v2"].(string) + `{}`},
	} {
		if _, err := codec.decodeTaskRevision(t.Context(), v.BotID, bad); err == nil {
			t.Fatal("non-strict envelope accepted")
		}
	}
}

type botTaskTestStreamOverride struct {
	store.BotContentStaging
	raw []byte
}

func (d *botTaskTestStreamOverride) Open(context.Context, store.BotContentRef, store.BotFileRange) (*store.BotFileStream, error) {
	return &store.BotFileStream{ReadCloser: io.NopCloser(bytes.NewReader(d.raw))}, nil
}

func TestBotContentTaskRevisionRejectsCorruptMissing(t *testing.T) {
	codec := newBotContentCodec(t.TempDir(), store.BotContentScope{OwnerID: uuid.NewString()})
	v := botContentTaskFixture()
	data, err := codec.encodeTaskRevision(t.Context(), v)
	if err != nil {
		t.Fatal(err)
	}
	var record botTaskRevisionRecord
	if err := decodeBotRecord(data, &record); err != nil {
		t.Fatal(err)
	}
	disk, err := codec.Open(t.Context(), v.BotID)
	if err != nil {
		t.Fatal(err)
	}
	defer disk.Close()
	stream, err := disk.Open(t.Context(), record.Content, store.BotFileRange{})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(stream)
	if closeErr := stream.Close(); err != nil || closeErr != nil {
		t.Fatal(err, closeErr)
	}
	corrupt := &botContentCodec{Open: func(ctx context.Context, id store.BotID) (store.BotContentStaging, error) {
		inner, err := codec.Open(ctx, id)
		return &botTaskTestStreamOverride{BotContentStaging: inner, raw: bytes.Replace(raw, []byte("SENTINEL-task-title"), []byte("CORRUPT!-task-title"), 1)}, err
	}}
	if _, err := corrupt.decodeTaskRevision(t.Context(), v.BotID, data); !errors.Is(err, store.ErrBotConflict) {
		t.Fatalf("corruption accepted: %v", err)
	}
	for name, mutate := range map[string]func(*botTaskRevisionContent){
		"id":             func(b *botTaskRevisionContent) { b.ID = uuid.New() },
		"owner":          func(b *botTaskRevisionContent) { b.BotID = uuid.New() },
		"schema":         func(b *botTaskRevisionContent) { b.SchemaVersion++ },
		"format":         func(b *botTaskRevisionContent) { b.Format++ },
		"logical_digest": func(b *botTaskRevisionContent) { b.Manifest.Assignment = "changed" },
		"asset":          func(b *botTaskRevisionContent) { b.Bundle["TASK.md"] = []byte("changed") },
	} {
		t.Run(name, func(t *testing.T) {
			var body botTaskRevisionContent
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Fatal(err)
			}
			mutate(&body)
			changed, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			ref, err := disk.Put(t.Context(), store.BotContentTask, v.Ref.Revision, int64(len(changed)), bytes.NewReader(changed), func(commit func() error) error { return commit() })
			if err != nil {
				t.Fatal(err)
			}
			r := record
			r.Content = *ref
			if _, err := codec.decodeTaskRevision(t.Context(), v.BotID, botContentTaskEnvelope(t, r)); err == nil {
				t.Fatal("invalid body accepted")
			}
		})
	}
	for name, changed := range map[string][]byte{
		"unknown_body_field":  append(append([]byte(nil), raw[:len(raw)-1]...), []byte(`,"unexpected":"SENTINEL"}`)...),
		"trailing_body_value": append(append([]byte(nil), raw...), []byte(`{}`)...),
	} {
		t.Run(name, func(t *testing.T) {
			ref, err := disk.Put(t.Context(), store.BotContentTask, v.Ref.Revision, int64(len(changed)), bytes.NewReader(changed), func(commit func() error) error { return commit() })
			if err != nil {
				t.Fatal(err)
			}
			r := record
			r.Content = *ref
			if _, err := codec.decodeTaskRevision(t.Context(), v.BotID, botContentTaskEnvelope(t, r)); err == nil {
				t.Fatal("non-strict body accepted")
			}
		})
	}
	if err := disk.Remove(t.Context(), record.Content, func(commit func() error) error { return commit() }); err != nil {
		t.Fatal(err)
	}
	if _, err := codec.decodeTaskRevision(t.Context(), v.BotID, data); err == nil {
		t.Fatal("missing object fallback")
	}
}

func TestBotContentTaskRevisionRequiresStorageAndValidDigest(t *testing.T) {
	v := botContentTaskFixture()
	for _, codec := range []*botContentCodec{nil, {}, {Open: func(context.Context, store.BotID) (store.BotContentStaging, error) { return nil, store.ErrBotNotFound }}} {
		if _, err := codec.encodeTaskRevision(t.Context(), v); err == nil {
			t.Fatal("missing storage fallback")
		}
		if _, err := codec.decodeTaskRevision(t.Context(), v.BotID, nil); err == nil {
			t.Fatal("missing storage fallback")
		}
	}
	codec := newBotContentCodec(t.TempDir(), store.BotContentScope{OwnerID: uuid.NewString()})
	v.Ref.Digest = strings.Repeat("0", 64)
	if _, err := codec.encodeTaskRevision(t.Context(), v); !errors.Is(err, store.ErrBotInvalid) {
		t.Fatalf("invalid digest: %v", err)
	}
}
