package entstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"

	_ "modernc.org/sqlite"
)

func TestBotTranscriptEventIDBackfill(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`CREATE TABLE session_events (id INTEGER PRIMARY KEY, session_id TEXT NOT NULL, event_id TEXT NULL, event_data JSON NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	record, _ := json.Marshal(map[string]any{"format": 2, "event_id": "bot-event-1"})
	botData, _ := json.Marshal(map[string]any{"bot_record_v2": string(record)})
	if _, err = db.Exec(`CREATE UNIQUE INDEX session_events_session_id_event_id_key ON session_events(session_id,event_id)`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO session_events(id,session_id,event_data) VALUES (1,'bot',?),(2,'chat','{}'),(3,'bot',?)`, botData, botData); err != nil {
		t.Fatal(err)
	}
	if err = backfillBotSessionEventIDs(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	var botID string
	if err = db.QueryRow(`SELECT event_id FROM session_events WHERE id=1`).Scan(&botID); err != nil || botID != "bot-event-1" {
		t.Fatalf("Bot identity=%q err=%v", botID, err)
	}
	var duplicate sql.NullString
	if err = db.QueryRow(`SELECT event_id FROM session_events WHERE id=3`).Scan(&duplicate); err != nil || duplicate.Valid {
		t.Fatalf("legacy duplicate identity=%+v err=%v", duplicate, err)
	}
	var chatID sql.NullString
	if err = db.QueryRow(`SELECT event_id FROM session_events WHERE id=2`).Scan(&chatID); err != nil || chatID.Valid {
		t.Fatalf("ordinary Chat identity=%+v err=%v", chatID, err)
	}
}
