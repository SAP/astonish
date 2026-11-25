package entstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
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

func botContentGoalFixture() store.BotGoal {
	now := time.Date(2026, 2, 3, 4, 5, 6, 7, time.UTC)
	return store.BotGoal{ID: uuid.New(), BotID: uuid.New(), Revision: 1<<54 + 3,
		Title: "Report goal", Description: "SENTINEL-description", CompletionCriteria: "SENTINEL-outcome",
		Status: store.BotGoalCompleted, DueAt: &now, CompletedAt: &now, WorkspacePath: "SENTINEL-workspace",
		Evidence: []store.BotEvidence{{Kind: store.BotEvidenceObservation, Summary: "SENTINEL-evidence",
			Source: "SENTINEL-source", ObservedAt: now, ActorID: uuid.New()}}}
}

func botContentGoalEnvelope(t *testing.T, record botGoalRecord) map[string]any {
	t.Helper()
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	return map[string]any{"bot_record_v2": string(raw)}
}

func TestBotContentGoalCodec(t *testing.T) {
	dir := t.TempDir()
	scope := store.BotContentScope{OrgID: "org", TeamID: "team", OwnerID: uuid.NewString()}
	codec := newBotContentCodec(dir, scope)
	goal := botContentGoalFixture()
	data, err := codec.encodeGoal(t.Context(), goal)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	for _, content := range []string{goal.Description, goal.CompletionCriteria, goal.WorkspacePath, goal.Evidence[0].Summary, goal.Evidence[0].Source} {
		if strings.Contains(string(raw), content) || strings.Contains(string(raw), base64.StdEncoding.EncodeToString([]byte(content))) {
			t.Fatal("body persisted in SQL envelope")
		}
	}
	// Simulate the actual database JSON boundary as well as reopening storage.
	var fromSQL map[string]any
	if err := json.Unmarshal(raw, &fromSQL); err != nil {
		t.Fatal(err)
	}
	got, err := newBotContentCodec(dir, scope).decodeGoal(t.Context(), goal.BotID, fromSQL)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(goal, *got) {
		t.Fatalf("roundtrip differs: %#v != %#v", goal, *got)
	}
	before, _ := json.Marshal(goal)
	after, _ := json.Marshal(got)
	if sha256.Sum256(before) != sha256.Sum256(after) {
		t.Fatal("hydrated logical hash changed")
	}

	for _, evidence := range [][]store.BotEvidence{nil, {}} {
		goal.Evidence = evidence
		goal.DueAt, goal.CompletedAt = nil, nil
		goal.Status = store.BotGoalActive
		data, err = codec.encodeGoal(t.Context(), goal)
		if err != nil {
			t.Fatal(err)
		}
		got, err = codec.decodeGoal(t.Context(), goal.BotID, data)
		if err != nil || !reflect.DeepEqual(goal, *got) {
			t.Fatalf("nil/empty changed: %#v, %v", got, err)
		}
	}
}

func TestBotContentGoalCodecRejectsMetadata(t *testing.T) {
	codec := newBotContentCodec(t.TempDir(), store.BotContentScope{OwnerID: uuid.NewString()})
	goal := botContentGoalFixture()
	data, err := codec.encodeGoal(t.Context(), goal)
	if err != nil {
		t.Fatal(err)
	}
	var original botGoalRecord
	if err := decodeBotRecord(data, &original); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*botGoalRecord){
		"format":         func(r *botGoalRecord) { r.Format-- },
		"schema":         func(r *botGoalRecord) { r.SchemaVersion++ },
		"id":             func(r *botGoalRecord) { r.ID = uuid.New() },
		"bot":            func(r *botGoalRecord) { r.BotID = uuid.New() },
		"revision":       func(r *botGoalRecord) { r.Revision++ },
		"title":          func(r *botGoalRecord) { r.Title = strings.Repeat("é", 129) },
		"status":         func(r *botGoalRecord) { r.Status = "unknown" },
		"ref_format":     func(r *botGoalRecord) { r.Content.Format++ },
		"ref_bot":        func(r *botGoalRecord) { r.Content.Scope.BotID = uuid.New() },
		"ref_owner":      func(r *botGoalRecord) { r.Content.Scope.OwnerID = uuid.NewString() },
		"ref_team":       func(r *botGoalRecord) { r.Content.Scope.OrgID, r.Content.Scope.TeamID = "other", "other" },
		"ref_kind":       func(r *botGoalRecord) { r.Content.Kind = store.BotContentMemory },
		"ref_revision":   func(r *botGoalRecord) { r.Content.Revision++ },
		"ref_size":       func(r *botGoalRecord) { r.Content.SizeBytes++ },
		"ref_digest":     func(r *botGoalRecord) { r.Content.SHA256 = strings.Repeat("0", 64) },
		"ref_bad_digest": func(r *botGoalRecord) { r.Content.SHA256 = "bad" },
	} {
		t.Run(name, func(t *testing.T) {
			record := original
			mutate(&record)
			if _, err := codec.decodeGoal(t.Context(), goal.BotID, botContentGoalEnvelope(t, record)); err == nil {
				t.Fatal("invalid metadata accepted")
			}
		})
	}
	old, _ := botJSONMap(goal)
	if _, err := codec.decodeGoal(t.Context(), goal.BotID, old); !errors.Is(err, errBotDevelopmentContentFormat) {
		t.Fatalf("old format accepted: %v", err)
	}
	for name, envelope := range map[string]map[string]any{
		"extra_envelope": {"bot_record_v2": data["bot_record_v2"], "body": "SENTINEL"},
		"unknown_field":  {"bot_record_v2": strings.TrimSuffix(data["bot_record_v2"].(string), "}") + `,"description":"SENTINEL"}`},
		"trailing_value": {"bot_record_v2": data["bot_record_v2"].(string) + `{}`},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := codec.decodeGoal(t.Context(), goal.BotID, envelope); err == nil {
				t.Fatal("non-strict envelope accepted")
			}
		})
	}
}

func TestBotContentGoalCodecRejectsMissingAndCorrupt(t *testing.T) {
	codec := newBotContentCodec(t.TempDir(), store.BotContentScope{OwnerID: uuid.NewString()})
	goal := botContentGoalFixture()
	data, err := codec.encodeGoal(t.Context(), goal)
	if err != nil {
		t.Fatal(err)
	}
	var record botGoalRecord
	if err := decodeBotRecord(data, &record); err != nil {
		t.Fatal(err)
	}
	disk, err := codec.Open(t.Context(), goal.BotID)
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

	// Exercise decoder integrity independently of the storage implementation.
	corrupt := &botContentCodec{Open: func(ctx context.Context, id store.BotID) (store.BotContentStaging, error) {
		inner, err := codec.Open(ctx, id)
		return &botGoalStreamOverride{BotContentStaging: inner, raw: bytes.Replace(raw, []byte("SENTINEL-description"), []byte("CORRUPT!-description"), 1)}, err
	}}
	if _, err := corrupt.decodeGoal(t.Context(), goal.BotID, data); !errors.Is(err, store.ErrBotConflict) {
		t.Fatalf("corrupt content accepted: %v", err)
	}
	for name, modify := range map[string]func(map[string]any){
		"unknown":       func(body map[string]any) { body["extra"] = "SENTINEL" },
		"body_schema":   func(body map[string]any) { body["schema_version"] = -1 },
		"body_kind":     func(body map[string]any) { body["kind"] = "other" },
		"body_owner":    func(body map[string]any) { body["bot_id"] = uuid.NewString() },
		"body_goal":     func(body map[string]any) { body["id"] = uuid.NewString() },
		"body_revision": func(body map[string]any) { body["revision"] = 1 },
	} {
		t.Run(name, func(t *testing.T) {
			var body map[string]any
			decoder := json.NewDecoder(bytes.NewReader(raw))
			decoder.UseNumber()
			if err := decoder.Decode(&body); err != nil {
				t.Fatal(err)
			}
			modify(body)
			changed, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			ref, err := disk.Put(t.Context(), store.BotContentDocument, goal.Revision, int64(len(changed)), bytes.NewReader(changed), func(commit func() error) error { return commit() })
			if err != nil {
				t.Fatal(err)
			}
			altered := record
			altered.Content = *ref
			if _, err := codec.decodeGoal(t.Context(), goal.BotID, botContentGoalEnvelope(t, altered)); err == nil {
				t.Fatal("malformed disk body accepted")
			}
		})
	}
	if err := disk.Remove(t.Context(), record.Content, func(commit func() error) error { return commit() }); err != nil {
		t.Fatal(err)
	}
	if _, err := codec.decodeGoal(t.Context(), goal.BotID, data); err == nil {
		t.Fatal("missing object fallback")
	}
}

type botGoalStreamOverride struct {
	store.BotContentStaging
	raw []byte
}

func (d *botGoalStreamOverride) Open(context.Context, store.BotContentRef, store.BotFileRange) (*store.BotFileStream, error) {
	return &store.BotFileStream{ReadCloser: io.NopCloser(bytes.NewReader(d.raw))}, nil
}

func TestBotContentGoalCodecMissingStorageAndInvalidInput(t *testing.T) {
	goal := botContentGoalFixture()
	for _, codec := range []*botContentCodec{nil, {}, {Open: func(context.Context, store.BotID) (store.BotContentStaging, error) { return nil, store.ErrBotNotFound }}} {
		if _, err := codec.encodeGoal(t.Context(), goal); err == nil {
			t.Fatal("missing storage fallback")
		}
	}
	codec := newBotContentCodec(t.TempDir(), store.BotContentScope{OwnerID: uuid.NewString()})
	goal.Title = strings.Repeat("x", 257)
	if _, err := codec.encodeGoal(t.Context(), goal); !errors.Is(err, store.ErrBotInvalid) {
		t.Fatal("unbounded SQL title", err)
	}
	goal.Title = "\xff"
	if _, err := codec.encodeGoal(t.Context(), goal); !errors.Is(err, store.ErrBotInvalid) {
		t.Fatal("invalid UTF-8 SQL title", err)
	}
	goal = botContentGoalFixture()
	goal.Revision = 0
	if _, err := codec.encodeGoal(t.Context(), goal); !errors.Is(err, store.ErrBotInvalid) {
		t.Fatal("invalid revision", err)
	}
}
