package entstore

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	personalent "github.com/SAP/astonish/ent/personal"
	teament "github.com/SAP/astonish/ent/team"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// Real SQLite + independently reopenable content storage, with scoped adapters.
func trackedRootDB(t *testing.T, scope store.BotScope) (botDraftDB, *sql.DB, store.BotID, uuid.UUID, func() botDraftDB) {
	t.Helper()
	path, dir := filepath.Join(t.TempDir(), "content.db"), t.TempDir()
	var sqlDB *sql.DB
	open := func() botDraftDB {
		var err error
		sqlDB, err = sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		sqlDB.SetMaxOpenConns(1)
		if _, err = sqlDB.Exec("PRAGMA foreign_keys=ON"); err != nil {
			t.Fatal(err)
		}
		driver := entsql.OpenDB(dialect.SQLite, sqlDB)
		codec := newScopedBotContentCodec(dir, "org", "team")
		if scope == store.BotScopePersonal {
			client := personalent.NewClient(personalent.Driver(driver))
			if err = client.Schema.Create(t.Context()); err != nil {
				t.Fatal(err)
			}
			return personalBotDB{client: client, content: codec}
		}
		client := teament.NewClient(teament.Driver(driver))
		if err = client.Schema.Create(t.Context()); err != nil {
			t.Fatal(err)
		}
		return teamBotDB{client: client, content: codec}
	}
	db := open()
	t.Cleanup(func() { sqlDB.Close() })
	b, g, owner := uuid.New(), uuid.New(), uuid.New()
	err := db.Transaction(t.Context(), func(tx botDraftDB) error {
		switch d := tx.(type) {
		case personalBotDB:
			if _, err := d.client.Bot.Create().SetID(b).SetOwnerID(owner).SetName("fixture").SetHomeSessionID(uuid.NewString()).SetCreateKey(uuid.NewString()).Save(t.Context()); err != nil {
				return err
			}
			_, err := d.client.BotGoal.Create().SetID(g).SetBotID(b).SetTitle("Goal").SetData(map[string]any{}).Save(t.Context())
			return err
		case teamBotDB:
			if _, err := d.client.Bot.Create().SetID(b).SetOwnerID(owner).SetName("fixture").SetHomeSessionID(uuid.NewString()).SetCreateKey(uuid.NewString()).Save(t.Context()); err != nil {
				return err
			}
			_, err := d.client.BotGoal.Create().SetID(g).SetBotID(b).SetTitle("Goal").SetData(map[string]any{}).Save(t.Context())
			return err
		}
		return store.ErrBotInvalid
	})
	if err != nil {
		t.Fatal(err)
	}
	return db, sqlDB, b, g, func() botDraftDB {
		if err := sqlDB.Close(); err != nil {
			t.Fatal(err)
		}
		return open()
	}
}
func assertTrackedRootNoBody(t *testing.T, raw []byte, sentinel string) {
	t.Helper()
	for _, bad := range []string{sentinel, base64.StdEncoding.EncodeToString([]byte(sentinel))} {
		if strings.Contains(string(raw), bad) {
			t.Fatalf("SQL content leak: %s", raw)
		}
	}
	escaped, _ := json.Marshal(sentinel)
	if strings.Contains(string(raw), string(escaped[1:len(escaped)-1])) {
		t.Fatal("escaped SQL content leak")
	}
}
func TestBotContentTrackedPersistence(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			db, sqlDB, _, g, reopen := trackedRootDB(t, scope)
			item := store.BotTrackedItem{ID: uuid.New(), GoalID: g, Title: "Tracked", Status: store.BotTrackedOpen, Reason: "SENTINEL-tracked-body-\"prose\"", Evidence: []store.BotEvidence{{Summary: "SENTINEL-tracked-evidence", Source: "SENTINEL-tracked-source"}}}
			if err := db.Transaction(t.Context(), func(tx botDraftDB) error { return tx.WriteTrackedItem(t.Context(), item, 1) }); err != nil {
				t.Fatal(err)
			}
			var raw []byte
			if err := sqlDB.QueryRow("SELECT data FROM bot_tracked_items WHERE id = ?", item.ID.String()).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			assertTrackedRootNoBody(t, raw, item.Reason)
			assertTrackedRootNoBody(t, raw, item.Evidence[0].Summary)
			assertTrackedRootNoBody(t, raw, item.Evidence[0].Source)
			if strings.Contains(string(raw), "evidence") || strings.Contains(string(raw), "reason") {
				t.Fatal("body fields in SQL")
			}
			db = reopen()
			got, err := db.ReadTrackedItems(t.Context(), g)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 1 || !reflect.DeepEqual(got[0], item) {
				t.Fatalf("roundtrip changed: %#v", got)
			}
			rollback := errors.New("rollback")
			changed := item
			changed.Reason = "not committed"
			if err = db.Transaction(t.Context(), func(tx botDraftDB) error {
				if err := tx.WriteTrackedItem(t.Context(), changed, 2); err != nil {
					return err
				}
				return rollback
			}); !errors.Is(err, rollback) {
				t.Fatal(err)
			}
			got, err = db.ReadTrackedItems(t.Context(), g)
			if err != nil || len(got) != 1 || !reflect.DeepEqual(got[0], item) {
				t.Fatal("rollback changed item", err)
			}
			changed.Title = strings.Repeat("界", 86)
			if err = db.WriteTrackedItem(t.Context(), changed, 2); !errors.Is(err, store.ErrBotInvalid) {
				t.Fatal("oversize UTF8 title accepted", err)
			}
		})
	}
}
func TestBotContentTrackedRejectsInvalidReferences(t *testing.T) {
	dir := t.TempDir()
	scope := store.BotContentScope{OwnerID: uuid.NewString()}
	c := newBotContentCodec(dir, scope)
	b := uuid.New()
	item := store.BotTrackedItem{ID: uuid.New(), GoalID: uuid.New(), Title: "ok", Status: store.BotTrackedOpen, Reason: "private"}
	data, err := c.encodeTracked(t.Context(), b, item, 1)
	if err != nil {
		t.Fatal(err)
	}
	var original botTrackedRecord
	if err = decodeBotRecord(data, &original); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*botTrackedRecord){"id": func(r *botTrackedRecord) { r.ID = uuid.New() }, "goal": func(r *botTrackedRecord) { r.GoalID = uuid.New() }, "title": func(r *botTrackedRecord) { r.Title = "changed" }, "owner": func(r *botTrackedRecord) { r.Content.Scope.OwnerID = uuid.NewString() }, "kind": func(r *botTrackedRecord) { r.Content.Kind = store.BotContentSkill }, "revision": func(r *botTrackedRecord) { r.Content.Revision++ }, "size": func(r *botTrackedRecord) { r.Content.SizeBytes++ }, "digest": func(r *botTrackedRecord) { r.Content.SHA256 = strings.Repeat("0", 64) }} {
		t.Run(name, func(t *testing.T) {
			r := original
			mutate(&r)
			raw, _ := json.Marshal(r)
			if _, err := c.decodeTracked(t.Context(), b, 1, map[string]any{"bot_record_v2": string(raw)}); err == nil {
				t.Fatal("accepted corrupt reference")
			}
		})
	}
	for _, status := range []store.BotTrackedStatus{store.BotTrackedOpen, store.BotTrackedBlocked, store.BotTrackedCompleted, store.BotTrackedCancelled} {
		candidate := item
		candidate.Status = status
		candidate.Title = strings.Repeat("界", 85) + "a"
		encoded, err := c.encodeTracked(t.Context(), b, candidate, 1)
		if err != nil {
			t.Fatal(err)
		}
		got, err := c.decodeTracked(t.Context(), b, 1, encoded)
		if err != nil || !reflect.DeepEqual(candidate, *got) {
			t.Fatal("status or 256-byte title changed", err)
		}
	}
	old, _ := botJSONMap(item)
	if _, err = c.decodeTracked(t.Context(), b, 1, old); !errors.Is(err, store.ErrBotUnsupported) {
		t.Fatal("legacy body accepted", err)
	}
	for _, title := range []string{strings.Repeat("a", 257), string([]byte{0xff})} {
		item.Title = title
		if _, err = c.encodeTracked(t.Context(), b, item, 1); !errors.Is(err, store.ErrBotInvalid) {
			t.Fatal("invalid title accepted", err)
		}
	}
	if err = os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if _, err = c.decodeTracked(t.Context(), b, 1, data); err == nil {
		t.Fatal("missing disk body accepted")
	}
}
