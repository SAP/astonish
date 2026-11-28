package entstore

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	adksession "google.golang.org/adk/session"
)

func TestBotDatabaseRuntimeFormatMatrix(t *testing.T) {
	ctx := t.Context()
	opens := 0
	codec := &botContentCodec{Open: func(context.Context, store.BotID) (store.BotContentStaging, error) {
		opens++
		return nil, fmt.Errorf("runtime content open forbidden")
	}}
	run := botContentRunFixture()
	approval := botContentApprovalFixture(t)
	activity := activityContentFixture()
	event := botSessionEventFixture()
	id, sid := uuid.New(), uuid.NewString()
	cases := []struct {
		name   string
		encode func(*botContentCodec) (map[string]any, error)
		decode func(*botContentCodec, map[string]any) (any, error)
		want   any
	}{
		{"run", func(c *botContentCodec) (map[string]any, error) { return c.encodeRun(ctx, run) }, func(c *botContentCodec, d map[string]any) (any, error) { return c.decodeRun(ctx, run.Run.BotID, d) }, &run},
		{"approval", func(c *botContentCodec) (map[string]any, error) { return c.encodeApproval(ctx, approval) }, func(c *botContentCodec, d map[string]any) (any, error) {
			return c.decodeApproval(ctx, approval.BotID, d)
		}, &approval},
		{"activity", func(c *botContentCodec) (map[string]any, error) { return c.encodeActivity(ctx, activity) }, func(c *botContentCodec, d map[string]any) (any, error) {
			return c.decodeActivity(ctx, activity.BotID, activity.Sequence, activity.Kind, activity.CreatedAt, d)
		}, &activity},
		{"session", func(c *botContentCodec) (map[string]any, error) { return c.encodeSessionEvent(ctx, id, sid, event) }, func(c *botContentCodec, d map[string]any) (any, error) { return c.decodeSessionEvent(ctx, id, sid, d) }, event},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data, err := tc.encode(codec)
			if err != nil {
				t.Fatal(err)
			}
			expected := tc.want
			if tc.name == "session" {
				mapped, err := adkEventToMap(event)
				if err != nil {
					t.Fatal(err)
				}
				expected, err = eventDataToADKEvent(mapped)
				if err != nil {
					t.Fatal(err)
				}
			}
			got, err := tc.decode(codec, data)
			if err != nil || !reflect.DeepEqual(got, expected) {
				t.Fatalf("database roundtrip: %+v %v", got, err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal([]byte(data["bot_record_v2"].(string)), &fields); err != nil {
				t.Fatal(err)
			}
			if fields["content"] != nil || fields["inline"] == nil {
				t.Fatal("not an inline database body")
			}
			for _, bad := range []string{"missing-inline", "null-inline", "bad-size", "bad-digest"} {
				t.Run(bad, func(t *testing.T) {
					mutated := make(map[string]json.RawMessage, len(fields))
					for k, v := range fields {
						mutated[k] = v
					}
					switch bad {
					case "missing-inline":
						delete(mutated, "inline")
					case "null-inline":
						mutated["inline"] = json.RawMessage(`null`)
					case "bad-size":
						mutated["size_bytes"] = json.RawMessage(`1`)
					case "bad-digest":
						mutated["sha256"] = json.RawMessage(`"bad"`)
					}
					raw, err := json.Marshal(mutated)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := tc.decode(codec, map[string]any{"bot_record_v2": string(raw)}); err == nil {
						t.Fatal("malformed payload accepted")
					}
				})
			}
			t.Run("nil-opener", func(t *testing.T) {
				noOpen := &botContentCodec{}
				inline, err := tc.encode(noOpen)
				if err != nil {
					t.Fatalf("database encode needs no opener: %v", err)
				}
				got, err := tc.decode(noOpen, inline)
				if err != nil || !reflect.DeepEqual(got, expected) {
					t.Fatalf("database decode needs no opener: %+v %v", got, err)
				}
			})
		})
	}
	if opens != 0 {
		t.Fatalf("runtime opens: %d", opens)
	}
}

func TestBotDatabaseSessionChatParity(t *testing.T) {
	ctx := t.Context()
	opens := 0
	codec := &botContentCodec{Open: func(context.Context, store.BotID) (store.BotContentStaging, error) {
		opens++
		return nil, fmt.Errorf("runtime content open forbidden")
	}}
	id := uuid.New()
	event := botSessionEventFixture()
	event.Timestamp = time.Now().UTC()
	data, err := codec.encodeSessionEvent(ctx, id, "session", event)
	if err != nil {
		t.Fatal(err)
	}
	got, err := codec.decodeSessionEvent(ctx, id, "session", data)
	if err != nil {
		t.Fatal(err)
	}
	chatMap, err := adkEventToMap(event)
	if err != nil {
		t.Fatal(err)
	}
	chat, err := eventDataToADKEvent(chatMap)
	if err != nil || !reflect.DeepEqual(got, chat) {
		t.Fatalf("Chat serializer mismatch: %+v %v", got, err)
	}
	var record botSessionEventRecord
	if err := decodeBotRecord(data, &record); err != nil {
		t.Fatal(err)
	}
	chatBytes, _ := json.Marshal(chatMap)
	if !bytes.Equal(record.Inline, chatBytes) {
		t.Fatal("Bot introduced another event serializer")
	}
	for _, mismatch := range []struct {
		bot     store.BotID
		session string
	}{{uuid.New(), "session"}, {id, "other"}} {
		if _, err := codec.decodeSessionEvent(ctx, mismatch.bot, mismatch.session, data); err == nil {
			t.Fatal("physical linkage mismatch accepted")
		}
	}
	if gotID, err := botSessionEventID(id, "session", data); err != nil || gotID != event.ID {
		t.Fatal(gotID, err)
	}
	if opens != 0 {
		t.Fatal("database event used workspace")
	}
	_ = adksession.Event{}
}
