package entstore

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	personalent "github.com/SAP/astonish/ent/personal"
	personalsession "github.com/SAP/astonish/ent/personal/session"
	teament "github.com/SAP/astonish/ent/team"
	teamsession "github.com/SAP/astonish/ent/team/session"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	adksession "google.golang.org/adk/session"
	"google.golang.org/genai"
)

func TestBotContentSessionContinuationReplaysBeforeFiltering(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			repo, _, ctx, b, lease, _ := botRunFixture(t, scope)
			run, err := repo.GetRun(ctx, b.BotID, lease.RunID)
			if err != nil {
				t.Fatal(err)
			}
			for i := range 205 {
				e := &adksession.Event{ID: fmt.Sprintf("bounded-%03d", i), Author: "bot_runtime"}
				if i == 0 {
					e.Actions.StateDelta = map[string]any{"early-state": "preserved"}
				}
				if err := repo.appendBotRunEvent(ctx, lease, run.SessionID, e); err != nil {
					t.Fatal(err)
				}
			}
			var sessions store.SessionStore
			switch d := repo.db.(type) {
			case personalBotDB:
				sessions = &personalSessionStore{client: d.client, content: d.content}
			case teamBotDB:
				sessions = &teamSessionStore{client: d.client, content: d.content}
			}
			got, err := sessions.Get(ctx, &adksession.GetRequest{SessionID: run.SessionID.String(), NumRecentEvents: 8})
			if err != nil {
				t.Fatal(err)
			}
			var events []*adksession.Event
			for event := range got.Session.Events().All() {
				events = append(events, event)
			}
			if len(events) != 8 || events[0].ID != "bounded-197" || events[8-1].ID != "bounded-204" {
				t.Fatalf("bounded continuation len=%d first=%q last=%q", len(events), events[0].ID, events[len(events)-1].ID)
			}
			if value, err := got.Session.State().Get("early-state"); err != nil || value != "preserved" {
				t.Fatalf("state before history filter lost: %v %v", value, err)
			}
			rows, err := sessions.(store.BotTranscriptReader).ReadBotTranscript(ctx, run.SessionID, 0, 200)
			if err != nil || len(rows) != 200 {
				t.Fatalf("full transcript page changed: len=%d err=%v", len(rows), err)
			}
		})
	}
}

func TestBotContentSessionPersistence(t *testing.T) {
	for _, team := range []bool{false, true} {
		name := "personal"
		if team {
			name = "team"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			dbPath := filepath.Join(t.TempDir(), "sessions.db")
			contentDir := t.TempDir()
			owner, botID, homeID, runID, sessionID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
			var db *sql.DB
			var sessions store.SessionStore
			var adapter botTranscriptDB
			var disable func()
			open := func() {
				var err error
				db, err = sql.Open("sqlite", dbPath)
				if err != nil {
					t.Fatal(err)
				}
				db.SetMaxOpenConns(1)
				if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys=ON"); err != nil {
					t.Fatal(err)
				}
				driver := entsql.OpenDB(dialect.SQLite, db)
				codec := newScopedBotContentCodec(contentDir, "org", "team")
				if team {
					c := teament.NewClient(teament.Driver(driver))
					if err := c.Schema.Create(ctx); err != nil {
						t.Fatal(err)
					}
					s := &teamSessionStore{client: c, content: codec}
					sessions = s
					disable = func() { s.content = nil }
					adapter = teamBotDB{client: c, content: codec}
				} else {
					c := personalent.NewClient(personalent.Driver(driver))
					if err := c.Schema.Create(ctx); err != nil {
						t.Fatal(err)
					}
					s := &personalSessionStore{client: c, content: codec}
					sessions = s
					disable = func() { s.content = nil }
					adapter = personalBotDB{client: c, content: codec}
				}
			}
			open()
			t.Cleanup(func() { _ = db.Close() })
			switch d := adapter.(type) {
			case personalBotDB:
				if _, err := d.client.Bot.Create().SetID(botID).SetOwnerID(owner).SetName("Bot").SetHomeSessionID(homeID.String()).SetCreateKey(uuid.NewString()).Save(ctx); err != nil {
					t.Fatal(err)
				}
				if _, err := d.client.Session.Create().SetID(homeID.String()).SetBotID(botID).SetUserID(owner).SetPurpose(personalsession.PurposeBotHome).SetRetention(personalsession.RetentionBot).Save(ctx); err != nil {
					t.Fatal(err)
				}
				if _, err := d.client.Session.Create().SetID(sessionID.String()).SetBotID(botID).SetBotRunID(runID).SetUserID(owner).SetPurpose(personalsession.PurposeBotRun).SetRetention(personalsession.RetentionBot).Save(ctx); err != nil {
					t.Fatal(err)
				}
			case teamBotDB:
				if _, err := d.client.Bot.Create().SetID(botID).SetOwnerID(owner).SetName("Bot").SetHomeSessionID(homeID.String()).SetCreateKey(uuid.NewString()).Save(ctx); err != nil {
					t.Fatal(err)
				}
				if _, err := d.client.Session.Create().SetID(homeID.String()).SetBotID(botID).SetUserID(owner).SetPurpose(teamsession.PurposeBotHome).SetRetention(teamsession.RetentionBot).Save(ctx); err != nil {
					t.Fatal(err)
				}
				if _, err := d.client.Session.Create().SetID(sessionID.String()).SetBotID(botID).SetBotRunID(runID).SetUserID(owner).SetPurpose(teamsession.PurposeBotRun).SetRetention(teamsession.RetentionBot).Save(ctx); err != nil {
					t.Fatal(err)
				}
			}
			sentinel := "SENTINEL-session-body-\"private\""
			event := &adksession.Event{ID: uuid.NewString(), Author: "assistant", Timestamp: time.Now().UTC(), LLMResponse: model.LLMResponse{Content: genai.NewContentFromText(sentinel, "model")}, Actions: adksession.EventActions{StateDelta: map[string]any{"secret": sentinel}}}
			b := store.Bot{ID: botID, OwnerID: owner, HomeSessionID: homeID}
			r := store.BotRun{ID: runID, BotID: botID, SessionID: sessionID, Intent: store.BotIntentRegular, Presentation: store.BotRunPresentationHomeInteractive}
			for i := 0; i < 2; i++ {
				if err := adapter.appendRunTranscript(ctx, b, r, event); err != nil {
					t.Fatal(err)
				}
			}
			assertClean := func() {
				for _, query := range []string{"SELECT event_data FROM session_events", "SELECT metadata FROM sessions WHERE bot_id IS NOT NULL"} {
					rows, err := db.QueryContext(ctx, query)
					if err != nil {
						t.Fatal(err)
					}
					for rows.Next() {
						var raw []byte
						if err := rows.Scan(&raw); err != nil {
							t.Fatal(err)
						}
						quoted, _ := json.Marshal(sentinel)
						if bytes.Contains(raw, []byte(sentinel)) || bytes.Contains(raw, quoted) || bytes.Contains(raw, []byte(base64.StdEncoding.EncodeToString([]byte(sentinel)))) {
							t.Fatalf("SQL contains body: %s", raw)
						}
					}
					if err := rows.Err(); err != nil {
						t.Fatal(err)
					}
					_ = rows.Close()
				}
			}
			assertClean()
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			open()
			reader := sessions.(store.BotTranscriptReader)
			for _, id := range []uuid.UUID{homeID, sessionID} {
				events, err := reader.ReadBotTranscript(ctx, id, 0, 200)
				if err != nil {
					t.Fatal(err)
				}
				if len(events) != 1 || events[0].Event.Content.Parts[0].Text != sentinel {
					t.Fatalf("bad transcript: %+v", events)
				}
				after, err := reader.ReadBotTranscript(ctx, id, events[0].Cursor, 200)
				if err != nil || len(after) != 0 {
					t.Fatalf("cursor: %v %v", after, err)
				}
			}
			got, err := sessions.Get(ctx, &adksession.GetRequest{SessionID: sessionID.String(), UserID: owner.String()})
			if err != nil {
				t.Fatal(err)
			}
			value, err := got.Session.State().Get("secret")
			if err != nil || value != sentinel {
				t.Fatalf("state: %v %v", value, err)
			}
			event.ID = uuid.NewString()
			if err := sessions.AppendEvent(ctx, got.Session, event); err != nil {
				t.Fatal(err)
			}
			all, err := sessions.ReadTranscriptEvents(ctx, "ignored", "ignored", sessionID.String())
			if err != nil || len(all) != 2 {
				t.Fatalf("events: %v %v", all, err)
			}
			child, err := sessions.Create(ctx, &adksession.CreateRequest{SessionID: uuid.NewString(), UserID: owner.String(), State: map[string]any{"_astonish_parent_id": sessionID.String(), "initial": sentinel}})
			if err != nil {
				t.Fatal(err)
			}
			meta, err := sessions.GetSessionMeta(ctx, child.Session.ID())
			if err != nil || meta.BotID != botID.String() || meta.BotRunID != runID.String() {
				t.Fatalf("child linkage: %+v %v", meta, err)
			}
			loaded, err := sessions.Get(ctx, &adksession.GetRequest{SessionID: child.Session.ID()})
			if err != nil {
				t.Fatal(err)
			}
			value, err = loaded.Session.State().Get("initial")
			if err != nil || value != sentinel {
				t.Fatalf("child state: %v %v", value, err)
			}
			assertClean()
			before, err := reader.ReadBotTranscript(ctx, sessionID, 0, 200)
			if err != nil {
				t.Fatal(err)
			}
			sessions.SetRedactFunc(func(s string) string { return strings.ReplaceAll(s, "SENTINEL", "REDACTED") })
			if err := sessions.RedactSession(ctx, "", "", sessionID.String()); err != nil {
				t.Fatal(err)
			}
			after, err := reader.ReadBotTranscript(ctx, sessionID, 0, 200)
			if err != nil {
				t.Fatal(err)
			}
			if after[0].Cursor != before[0].Cursor || after[0].Event.ID != before[0].Event.ID || !strings.Contains(after[0].Event.Content.Parts[0].Text, "REDACTED") {
				t.Fatal("redaction changed cursor/identity or failed to hydrate")
			}
			// Database runtime records survive workspace loss: removing the
			// per-test content directory must not break conversation reads.
			if err := os.RemoveAll(contentDir); err != nil {
				t.Fatal(err)
			}
			if _, err := reader.ReadBotTranscript(ctx, sessionID, 0, 200); err != nil {
				t.Fatalf("database transcript required workspace files: %v", err)
			}
			loaded, err = sessions.Get(ctx, &adksession.GetRequest{SessionID: child.Session.ID()})
			if err != nil {
				t.Fatalf("database child state read failed: %v", err)
			}
			if value, err := loaded.Session.State().Get("initial"); err != nil || value != sentinel {
				t.Fatalf("database child state lost: %v %v", value, err)
			}
			disable()
			// Without a codec, writing a new event still fails (encode needs scope).
			if err := sessions.AppendEvent(ctx, got.Session, event); !errors.Is(err, store.ErrBotUnsupported) {
				t.Fatalf("missing codec write: %v", err)
			}
			ordinary, err := sessions.Create(ctx, &adksession.CreateRequest{SessionID: uuid.NewString(), State: map[string]any{"initial": sentinel}})
			if err != nil {
				t.Fatal(err)
			}
			if err := sessions.AppendEvent(ctx, ordinary.Session, event); err != nil {
				t.Fatal(err)
			}
			var raw []byte
			if err := db.QueryRowContext(ctx, "SELECT event_data FROM session_events WHERE session_id = ?", ordinary.Session.ID()).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(raw, []byte("SENTINEL")) || bytes.Contains(raw, []byte("bot_record_v2")) {
				t.Fatal("ordinary Chat no longer inline")
			}
		})
	}
}
