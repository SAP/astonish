package entstore

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	adksession "google.golang.org/adk/session"
	"google.golang.org/genai"
)

const botSessionSentinel = "SESSION-SENTINEL-\"private\"-\n-body"

func botSessionEventFixture() *adksession.Event {
	return &adksession.Event{
		ID: uuid.NewString(), Timestamp: time.Date(2026, 2, 3, 4, 5, 6, 7, time.UTC),
		InvocationID: botSessionSentinel, Branch: botSessionSentinel, Author: botSessionSentinel,
		LLMResponse: model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{
			{Text: botSessionSentinel},
			{FunctionCall: &genai.FunctionCall{Name: "tool", Args: map[string]any{"input": botSessionSentinel}}},
			{FunctionResponse: &genai.FunctionResponse{Name: "tool", Response: map[string]any{"output": botSessionSentinel}}},
		}}},
		Actions:            adksession.EventActions{StateDelta: map[string]any{"state": map[string]any{"body": botSessionSentinel, "count": float64(7)}}, ArtifactDelta: map[string]int64{botSessionSentinel: 1<<54 + 3}},
		LongRunningToolIDs: []string{botSessionSentinel},
	}
}

func botSessionEnvelope(t *testing.T, record botSessionEventRecord) map[string]any {
	t.Helper()
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	return map[string]any{"bot_record_v2": string(raw)}
}

func TestBotDatabaseSessionRuntimeIOProfile(t *testing.T) {
	for _, mode := range []string{"inline", "disabled"} {
		t.Run(mode, func(t *testing.T) {
			ctx := t.Context()
			codec := newBotContentCodec(t.TempDir(), store.BotContentScope{OwnerID: uuid.NewString()})
			id, sid, event := uuid.New(), uuid.NewString(), botSessionEventFixture()
			codec.Open = func(ctx context.Context, id store.BotID) (store.BotContentStaging, error) {
				t.Error("inline session accessed file storage")
				return nil, store.ErrBotUnsupported
			}
			var timings []store.BotRuntimeIOTiming
			profileCtx, profile := store.WithBotRuntimeIOProfiler(ctx, func(v store.BotRuntimeIOTiming) { timings = append(timings, v) })
			if mode != "disabled" {
				ctx = profileCtx
			}
			data, err := codec.encodeSessionEvent(ctx, id, sid, event)
			if err != nil {
				t.Fatal(err)
			}
			got, err := codec.decodeSessionEvent(ctx, id, sid, data)
			if err != nil || got == nil || got.ID != event.ID {
				t.Fatalf("session: %+v %v", got, err)
			}
			if got := profile.Snapshot(); got != (store.BotRuntimeIOSnapshot{}) {
				t.Fatalf("runtime I/O: %+v", got)
			}
			if len(timings) != 0 {
				t.Fatalf("timings: %+v", timings)
			}
		})
	}
}

func TestBotContentSessionEventCodec(t *testing.T) {
	dir := t.TempDir()
	scope := store.BotContentScope{OrgID: "org", TeamID: "team", OwnerID: uuid.NewString()}
	codec := newBotContentCodec(dir, scope)
	botID, sessionID := uuid.New(), uuid.NewString()
	event := botSessionEventFixture()
	data, err := codec.encodeSessionEvent(t.Context(), botID, sessionID, event)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	var fromSQL map[string]any
	if err := json.Unmarshal(raw, &fromSQL); err != nil {
		t.Fatal(err)
	}
	got, err := newBotContentCodec(dir, scope).decodeSessionEvent(t.Context(), botID, sessionID, fromSQL)
	if err != nil {
		t.Fatal(err)
	}
	// The event round-trips through Chat's map serializer; compare to that.
	mapped, err := adkEventToMap(event)
	if err != nil {
		t.Fatal(err)
	}
	want, err := eventDataToADKEvent(mapped)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("roundtrip changed event/state: got %#v", got)
	}
	id, err := botSessionEventID(botID, sessionID, fromSQL)
	if err != nil || id != event.ID {
		t.Fatalf("metadata ID: %q, %v", id, err)
	}
	_ = base64.StdEncoding
}

func TestBotContentSessionEventRejectsMetadata(t *testing.T) {
	codec := newBotContentCodec(t.TempDir(), store.BotContentScope{OwnerID: uuid.NewString()})
	botID, sessionID := uuid.New(), uuid.NewString()
	data, err := codec.encodeSessionEvent(t.Context(), botID, sessionID, botSessionEventFixture())
	if err != nil {
		t.Fatal(err)
	}
	var original botSessionEventRecord
	if err := decodeBotRecord(data, &original); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*botSessionEventRecord){
		"bot":         func(r *botSessionEventRecord) { r.BotID = uuid.New() },
		"session":     func(r *botSessionEventRecord) { r.SessionID = uuid.NewString() },
		"event":       func(r *botSessionEventRecord) { r.EventID = uuid.NewString() },
		"empty_event": func(r *botSessionEventRecord) { r.EventID = "" },
		"digest":      func(r *botSessionEventRecord) { r.SHA256 = strings.Repeat("0", 64) },
		"size":        func(r *botSessionEventRecord) { r.SizeBytes++ },
	} {
		t.Run(name, func(t *testing.T) {
			record := original
			mutate(&record)
			if _, err := codec.decodeSessionEvent(t.Context(), botID, sessionID, botSessionEnvelope(t, record)); err == nil {
				t.Fatal("invalid metadata accepted")
			}
		})
	}
	for name, invalid := range map[string]map[string]any{
		"old":      {"ID": original.EventID, "Actions": map[string]any{"StateDelta": botSessionSentinel}},
		"missing":  nil,
		"extra":    {"bot_record_v2": data["bot_record_v2"], "body": botSessionSentinel},
		"unknown":  {"bot_record_v2": strings.TrimSuffix(data["bot_record_v2"].(string), "}") + `,"body":"forbidden"}`},
		"trailing": {"bot_record_v2": data["bot_record_v2"].(string) + `{}`},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := codec.decodeSessionEvent(t.Context(), botID, sessionID, invalid); err == nil {
				t.Fatal("invalid envelope accepted")
			}
			if _, err := botSessionEventID(botID, sessionID, invalid); err == nil {
				t.Fatal("metadata helper accepted invalid envelope")
			}
		})
	}
}

func TestBotContentSessionEventRejectsSubstitutionAndMissing(t *testing.T) {
	codec := newBotContentCodec(t.TempDir(), store.BotContentScope{OwnerID: uuid.NewString()})
	botID, sessionID := uuid.New(), uuid.NewString()
	event := botSessionEventFixture()
	data, err := codec.encodeSessionEvent(t.Context(), botID, sessionID, event)
	if err != nil {
		t.Fatal(err)
	}
	var original botSessionEventRecord
	if err := decodeBotRecord(data, &original); err != nil {
		t.Fatal(err)
	}
	// Corrupting the inline body breaks the digest and must be rejected.
	corrupt := original
	corrupt.Inline = bytes.ReplaceAll(append([]byte(nil), original.Inline...), []byte("SESSION-SENTINEL"), []byte("CORRUPT-CONTENT!"))
	if _, err := codec.decodeSessionEvent(t.Context(), botID, sessionID, botSessionEnvelope(t, corrupt)); !errors.Is(err, store.ErrBotConflict) {
		t.Fatalf("corrupt body accepted: %v", err)
	}
	// A different event's body cannot be substituted under this identity:
	// the decoded event ID must match the record's EventID.
	otherEvent := botSessionEventFixture()
	other, err := codec.encodeSessionEvent(t.Context(), botID, sessionID, otherEvent)
	if err != nil {
		t.Fatal(err)
	}
	var record botSessionEventRecord
	if err := decodeBotRecord(other, &record); err != nil {
		t.Fatal(err)
	}
	changed := original
	changed.botRuntimeInline = record.botRuntimeInline
	if _, err := codec.decodeSessionEvent(t.Context(), botID, sessionID, botSessionEnvelope(t, changed)); !errors.Is(err, store.ErrBotConflict) {
		t.Fatalf("body substitution accepted: %v", err)
	}
	if id, err := botSessionEventID(botID, sessionID, data); err != nil || id != event.ID {
		t.Fatalf("metadata query failed: %q, %v", id, err)
	}
}

func TestBotContentSessionEventMissingStorageAndInvalidInput(t *testing.T) {
	botID, sessionID := uuid.New(), uuid.NewString()
	codec := newBotContentCodec(t.TempDir(), store.BotContentScope{OwnerID: uuid.NewString()})
	for _, event := range []*adksession.Event{nil, {}, {ID: strings.Repeat("x", 257)}} {
		if _, err := codec.encodeSessionEvent(t.Context(), botID, sessionID, event); !errors.Is(err, store.ErrBotInvalid) {
			t.Fatalf("invalid event accepted: %v", err)
		}
	}
	event := botSessionEventFixture()
	event.Actions.StateDelta["unserializable"] = make(chan int)
	if _, err := codec.encodeSessionEvent(t.Context(), botID, sessionID, event); err == nil {
		t.Fatal("unserializable state silently lost")
	}
}
