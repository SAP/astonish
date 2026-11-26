package entstore

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"entgo.io/ent/dialect/sql"
	pevent "github.com/SAP/astonish/ent/personal/sessionevent"
	tevent "github.com/SAP/astonish/ent/team/sessionevent"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
)

// Exercise the production disk-backed transcript writer and both scoped lookup
// adapters rather than hand-built JSON; the SQL envelope retains only identity.
func TestBotSkillCanonicalEventLookup(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			r, _, worker, run, lease, now := botRunFixture(t, scope)
			event := &session.Event{ID: "canonical-owner-source", Author: "user", Timestamp: *now,
				LLMResponse: model.LLMResponse{Content: genai.NewContentFromText("Observe", "user")}}
			if err := r.appendBotRunEvent(worker, lease, run.SessionID, event); err != nil {
				t.Fatal(err)
			}
			db := r.db.(botSkillDB)
			got, err := db.skillSourceEvent(worker, run.SessionID, event.ID)
			if err != nil || got == nil || got.ID != event.ID || got.Author != "user" || !got.Timestamp.Equal(event.Timestamp) || got.Content.Parts[0].Text != "Observe" {
				t.Fatalf("canonical event not recovered: %+v %v", got, err)
			}
			for _, tc := range []struct {
				sessionID uuid.UUID
				eventID   string
			}{
				{uuid.New(), event.ID}, {run.SessionID, "missing"},
			} {
				if _, err := db.skillSourceEvent(worker, tc.sessionID, tc.eventID); !errors.Is(err, store.ErrBotNotFound) {
					t.Fatalf("foreign/missing source resolved: %v", err)
				}
			}
			// Ambiguous IDs cannot be treated as proof of a unique source event.
			data, _ := skillSourceRowForTest(t, worker, r.db, run.SessionID)
			switch d := r.db.(type) {
			case personalBotDB:
				if _, err := d.client.SessionEvent.Create().SetSessionID(run.SessionID.String()).SetEventData(data).Save(worker); err != nil {
					t.Fatal(err)
				}
			case teamBotDB:
				if _, err := d.client.SessionEvent.Create().SetSessionID(run.SessionID.String()).SetEventData(data).Save(worker); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := db.skillSourceEvent(worker, run.SessionID, event.ID); !errors.Is(err, store.ErrBotNotFound) {
				t.Fatalf("duplicate source resolved: %v", err)
			}
		})
	}
}

func TestBotSkillSourceRejectsInlineAndSubstitutedBinding(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, kind := range []string{"legacy", "foreign-bot", "foreign-session", "changed-id"} {
			t.Run(string(scope)+"/"+kind, func(t *testing.T) {
				r, _, worker, run, lease, now := botRunFixture(t, scope)
				event := &session.Event{ID: "bound-source", Author: "user", Timestamp: *now,
					LLMResponse: model.LLMResponse{Content: genai.NewContentFromText("private-source-sentinel", "user")}}
				if err := r.appendBotRunEvent(worker, lease, run.SessionID, event); err != nil {
					t.Fatal(err)
				}
				data, replace := skillSourceRowForTest(t, worker, r.db, run.SessionID)
				raw, err := json.Marshal(data)
				if err != nil || strings.Contains(string(raw), "private-source-sentinel") {
					t.Fatalf("SQL contains source content: %s %v", raw, err)
				}
				var record botSessionEventRecord
				if err := decodeBotRecord(data, &record); err != nil {
					t.Fatal(err)
				}
				id := event.ID
				switch kind {
				case "legacy":
					data, err = adkEventToMap(event)
				case "foreign-bot":
					record.BotID = uuid.New()
				case "foreign-session":
					record.SessionID = uuid.NewString()
				case "changed-id":
					id = "forged-source"
					record.EventID = id
				}
				if err != nil {
					t.Fatal(err)
				}
				if kind != "legacy" {
					raw, err = json.Marshal(record)
					if err != nil {
						t.Fatal(err)
					}
					data = map[string]any{"bot_record_v2": string(raw)}
				}
				replace(data)
				if _, err := r.db.(botSkillDB).skillSourceEvent(worker, run.SessionID, id); err == nil {
					t.Fatal("invalid source resolved")
				}
			})
		}
	}
}

func TestBotAdmittedMessageDiskSource(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			r, _, worker, run, _, now := botRunFixture(t, scope)
			rec, err := r.db.GetBot(worker, run.BotID)
			if err != nil {
				t.Fatal(err)
			}
			writer := r.db.(interface {
				appendAdmittedMessage(context.Context, store.Bot, *session.Event) (int64, error)
			})
			event := &session.Event{ID: "admitted-exact-id", Author: "user", Timestamp: *now,
				LLMResponse: model.LLMResponse{Content: genai.NewContentFromText("private-admission-sentinel", "user")}}
			cursor, err := writer.appendAdmittedMessage(worker, rec.Bot, event)
			if err != nil || cursor <= 0 {
				t.Fatalf("append: %d %v", cursor, err)
			}
			data, _ := skillSourceRowForTest(t, worker, r.db, rec.Bot.HomeSessionID)
			raw, err := json.Marshal(data)
			if err != nil || strings.Contains(string(raw), "private-admission-sentinel") {
				t.Fatalf("SQL source: %s %v", raw, err)
			}
			got, err := r.db.(botSkillDB).skillSourceEvent(worker, rec.Bot.HomeSessionID, event.ID)
			if err != nil || got == nil || got.ID != event.ID || got.Content.Parts[0].Text != event.Content.Parts[0].Text {
				t.Fatalf("source: %+v %v", got, err)
			}
			event.ID = "admitted-next-id"
			next, err := writer.appendAdmittedMessage(worker, rec.Bot, event)
			if err != nil || next <= cursor {
				t.Fatalf("cursor order: %d %d %v", cursor, next, err)
			}
			forged := rec.Bot
			forged.OwnerID = uuid.New()
			if _, err := writer.appendAdmittedMessage(worker, forged, event); !errors.Is(err, store.ErrBotForbidden) {
				t.Fatalf("foreign owner accepted: %v", err)
			}
		})
	}
}

func skillSourceRowForTest(t *testing.T, ctx context.Context, db botDB, id uuid.UUID) (map[string]any, func(map[string]any)) {
	t.Helper()
	switch d := db.(type) {
	case personalBotDB:
		row, err := d.client.SessionEvent.Query().Where(pevent.SessionIDEQ(id.String())).Order(pevent.ByID(sql.OrderDesc())).First(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return row.EventData, func(data map[string]any) {
			if err := d.client.SessionEvent.UpdateOneID(row.ID).SetEventData(data).Exec(ctx); err != nil {
				t.Fatal(err)
			}
		}
	case teamBotDB:
		row, err := d.client.SessionEvent.Query().Where(tevent.SessionIDEQ(id.String())).Order(tevent.ByID(sql.OrderDesc())).First(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return row.EventData, func(data map[string]any) {
			if err := d.client.SessionEvent.UpdateOneID(row.ID).SetEventData(data).Exec(ctx); err != nil {
				t.Fatal(err)
			}
		}
	default:
		t.Fatal("unexpected adapter")
		return nil, nil
	}
}
