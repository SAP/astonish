package entstore

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	personalent "github.com/SAP/astonish/ent/personal"
	teament "github.com/SAP/astonish/ent/team"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func botContentSkillFixture() store.BotSkillRevision {
	b, goal := uuid.New(), uuid.New()
	p := store.BotSkillProvenance{Level: store.BotSkillOwnerSpecified, BotID: b, GoalID: &goal, GoalRevision: 1<<54 + 3, SourceDigest: strings.Repeat("a", 64), Quote: "SENTINEL-skill-quote", RecordedAt: time.Date(2026, 2, 3, 4, 5, 6, 7, time.UTC)}
	v := store.BotSkillRevision{SchemaVersion: store.BotSkillSchemaVersion, ID: uuid.New(), BotID: b, SkillID: uuid.New(), Revision: 2, ParentRevision: 1, ParentDigest: strings.Repeat("b", 64), Responsibility: "SENTINEL-skill-responsibility", Expectations: []store.BotSkillExpectation{{ID: "SENTINEL-skill-expectation-id", Text: p.Quote, Provenance: p}}, Method: []string{"SENTINEL-skill-method"}, Sources: []store.BotSkillSource{{URL: "https://example.org/SENTINEL-skill-url", Purpose: "SENTINEL-skill-purpose", AccessMethod: "SENTINEL-skill-access", Fallback: "SENTINEL-skill-fallback", Limitation: "SENTINEL-skill-limitation", Provenance: p}}, OutputFormat: "SENTINEL-skill-output", EvaluationRules: []string{"SENTINEL-skill-evaluation"}, Provenance: []store.BotSkillProvenance{p}, ChangeReason: "SENTINEL-skill-change"}
	reverts := int64(1)
	v.RevertsRevision = &reverts
	v.Digest = v.ContentDigest()
	return v
}

func skillRecordEnvelope(t *testing.T, r botSkillRevisionRecord) map[string]any {
	t.Helper()
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	return map[string]any{"bot_record_v2": string(raw)}
}

func assertSkillNoSQLBody(t *testing.T, data []byte) {
	t.Helper()
	for _, text := range []string{"SENTINEL", "SENTINEL-skill-quote", "SENTINEL-skill-method", "SENTINEL-skill-change"} {
		quoted, _ := json.Marshal(text)
		if bytes.Contains(data, []byte(text)) || bytes.Contains(data, quoted) || bytes.Contains(data, []byte(base64.StdEncoding.EncodeToString([]byte(text)))) {
			t.Fatalf("SQL body leaked: %s", text)
		}
	}
}

func TestBotContentSkillRevisionRoundTrip(t *testing.T) {
	dir := t.TempDir()
	scope := store.BotContentScope{OwnerID: uuid.NewString(), OrgID: "org", TeamID: "team"}
	c := newBotContentCodec(dir, scope)
	v := botContentSkillFixture()
	data, err := c.encodeSkillRevision(t.Context(), v)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(data)
	assertSkillNoSQLBody(t, raw)
	got, err := newBotContentCodec(dir, scope).decodeSkillRevision(t.Context(), v.BotID, data)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(v, *got) || got.ContentDigest() != v.Digest {
		t.Fatal("revision changed on reopen")
	}
	scope.OwnerID = uuid.NewString()
	if _, err := newBotContentCodec(dir, scope).decodeSkillRevision(t.Context(), v.BotID, data); err == nil {
		t.Fatal("foreign owner read")
	}
}

func TestBotContentSkillRevisionRejectsMetadata(t *testing.T) {
	c := newBotContentCodec(t.TempDir(), store.BotContentScope{OwnerID: uuid.NewString()})
	v := botContentSkillFixture()
	data, err := c.encodeSkillRevision(t.Context(), v)
	if err != nil {
		t.Fatal(err)
	}
	var original botSkillRevisionRecord
	if err := decodeBotRecord(data, &original); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*botSkillRevisionRecord){
		"format":       func(r *botSkillRevisionRecord) { r.Format++ },
		"schema":       func(r *botSkillRevisionRecord) { r.SchemaVersion++ },
		"bot":          func(r *botSkillRevisionRecord) { r.BotID = uuid.New() },
		"id":           func(r *botSkillRevisionRecord) { r.ID = uuid.New() },
		"skill":        func(r *botSkillRevisionRecord) { r.SkillID = uuid.New() },
		"parent":       func(r *botSkillRevisionRecord) { r.ParentDigest = strings.Repeat("c", 64) },
		"digest":       func(r *botSkillRevisionRecord) { r.Digest = strings.Repeat("c", 64) },
		"ref_kind":     func(r *botSkillRevisionRecord) { r.Content.Kind = store.BotContentTask },
		"ref_revision": func(r *botSkillRevisionRecord) { r.Content.Revision++ },
		"ref_size":     func(r *botSkillRevisionRecord) { r.Content.SizeBytes++ },
		"ref_owner":    func(r *botSkillRevisionRecord) { r.Content.Scope.OwnerID = uuid.NewString() },
	} {
		t.Run(name, func(t *testing.T) {
			r := original
			change(&r)
			if _, err := c.decodeSkillRevision(t.Context(), v.BotID, skillRecordEnvelope(t, r)); err == nil {
				t.Fatal("invalid metadata accepted")
			}
		})
	}
	unknown := map[string]any{"bot_record_v2": strings.TrimSuffix(data["bot_record_v2"].(string), "}") + `,"body":"SENTINEL"}`}
	if _, err := c.decodeSkillRevision(t.Context(), v.BotID, unknown); !errors.Is(err, store.ErrBotInvalid) {
		t.Fatalf("unknown fields: %v", err)
	}
	legacy, _ := botJSONMap(v)
	if _, err := c.decodeSkillRevision(t.Context(), v.BotID, legacy); !errors.Is(err, store.ErrBotUnsupported) {
		t.Fatalf("legacy: %v", err)
	}
	if _, err := (*botContentCodec)(nil).encodeSkillRevision(t.Context(), v); !errors.Is(err, store.ErrBotUnsupported) {
		t.Fatal(err)
	}
}

type botSkillStreamOverride struct {
	store.BotContentStaging
	raw []byte
}

func (s *botSkillStreamOverride) Open(context.Context, store.BotContentRef, store.BotFileRange) (*store.BotFileStream, error) {
	return &store.BotFileStream{ReadCloser: io.NopCloser(bytes.NewReader(s.raw))}, nil
}

func TestBotContentSkillRevisionRejectsCorruptMissing(t *testing.T) {
	c := newBotContentCodec(t.TempDir(), store.BotContentScope{OwnerID: uuid.NewString()})
	v := botContentSkillFixture()
	data, err := c.encodeSkillRevision(t.Context(), v)
	if err != nil {
		t.Fatal(err)
	}
	var r botSkillRevisionRecord
	if err := decodeBotRecord(data, &r); err != nil {
		t.Fatal(err)
	}
	disk, err := c.Open(t.Context(), v.BotID)
	if err != nil {
		t.Fatal(err)
	}
	defer disk.Close()
	s, err := disk.Open(t.Context(), r.Content, store.BotFileRange{})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(s)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	corrupt := &botContentCodec{Open: func(ctx context.Context, id store.BotID) (store.BotContentStaging, error) {
		inner, err := c.Open(ctx, id)
		return &botSkillStreamOverride{BotContentStaging: inner, raw: bytes.Replace(raw, []byte("SENTINEL"), []byte("CORRUPT!"), 1)}, err
	}}
	if _, err := corrupt.decodeSkillRevision(t.Context(), v.BotID, data); !errors.Is(err, store.ErrBotConflict) {
		t.Fatalf("corrupt: %v", err)
	}
	// A valid storage hash must not bypass logical digest or schema validation.
	for _, body := range [][]byte{bytes.Replace(raw, []byte("SENTINEL"), []byte("CORRUPT!"), 1), append(append([]byte{}, raw[:len(raw)-1]...), []byte(`,"unknown":true}`)...)} {
		ref, err := disk.Put(t.Context(), store.BotContentSkill, v.Revision, int64(len(body)), bytes.NewReader(body), func(commit func() error) error { return commit() })
		if err != nil {
			t.Fatal(err)
		}
		bad := r
		bad.Content = *ref
		if _, err := c.decodeSkillRevision(t.Context(), v.BotID, skillRecordEnvelope(t, bad)); err == nil {
			t.Fatal("invalid body accepted")
		}
	}
	if err := disk.Remove(t.Context(), r.Content, func(commit func() error) error { return commit() }); err != nil {
		t.Fatal(err)
	}
	if _, err := c.decodeSkillRevision(t.Context(), v.BotID, data); err == nil {
		t.Fatal("missing content accepted")
	}
}

func TestBotContentSkillPersistence(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			ctx := t.Context()
			path := filepath.Join(t.TempDir(), "skills.db")
			contentDir := t.TempDir()
			owner := uuid.New()
			var sqlDB *sql.DB
			var db botDB
			open := func() {
				var err error
				sqlDB, err = sql.Open("sqlite", path)
				if err != nil {
					t.Fatal(err)
				}
				sqlDB.SetMaxOpenConns(1)
				if _, err := sqlDB.ExecContext(ctx, "PRAGMA foreign_keys=ON"); err != nil {
					t.Fatal(err)
				}
				driver := entsql.OpenDB(dialect.SQLite, sqlDB)
				codec := newScopedBotContentCodec(contentDir, "org", "team")
				if scope == store.BotScopePersonal {
					client := personalent.NewClient(personalent.Driver(driver))
					if err := client.Schema.Create(ctx); err != nil {
						t.Fatal(err)
					}
					db = personalBotDB{client: client, content: codec}
				} else {
					client := teament.NewClient(teament.Driver(driver))
					if err := client.Schema.Create(ctx); err != nil {
						t.Fatal(err)
					}
					db = teamBotDB{client: client, content: codec}
				}
			}
			open()
			t.Cleanup(func() { _ = sqlDB.Close() })
			v := botContentSkillFixture()
			err := db.Transaction(ctx, func(tx botDB) error {
				switch d := tx.(type) {
				case personalBotDB:
					if _, err := d.client.Bot.Create().SetID(v.BotID).SetOwnerID(owner).SetName("Skill fixture").SetHomeSessionID(uuid.NewString()).SetCreateKey(uuid.NewString()).Save(ctx); err != nil {
						return err
					}
				case teamBotDB:
					if _, err := d.client.Bot.Create().SetID(v.BotID).SetOwnerID(owner).SetName("Skill fixture").SetHomeSessionID(uuid.NewString()).SetCreateKey(uuid.NewString()).Save(ctx); err != nil {
						return err
					}
				}
				skills := tx.(botSkillDB)
				now := time.Now()
				if err := skills.writeSkill(ctx, botStoredSkill{Skill: store.BotSkill{ID: v.SkillID, BotID: v.BotID, Key: "skill", Title: "Skill index", Status: store.BotSkillDraft, Revision: 1, HeadRevision: v.Revision, HeadDigest: v.Digest, CreatedAt: now, UpdatedAt: now}}, 0); err != nil {
					return err
				}
				return skills.insertSkillRevision(ctx, v)
			})
			if err != nil {
				t.Fatal(err)
			}
			var raw []byte
			var size int64
			if err := sqlDB.QueryRowContext(ctx, "SELECT data,size_bytes FROM bot_skill_revisions WHERE id = ?", v.ID.String()).Scan(&raw, &size); err != nil {
				t.Fatal(err)
			}
			assertSkillNoSQLBody(t, raw)
			logical, _ := json.Marshal(v)
			if size != int64(len(logical)) {
				t.Fatal("quota changed")
			}
			if err := sqlDB.Close(); err != nil {
				t.Fatal(err)
			}
			open()
			got, err := db.(botSkillDB).readSkillRevision(ctx, v.BotID, v.SkillID, v.Revision)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(v, *got) {
				t.Fatal("reopened revision changed")
			}
			next := *v.Clone()
			next.ID = uuid.New()
			next.Revision++
			next.ParentRevision = v.Revision
			next.ParentDigest = v.Digest
			next.Digest = next.ContentDigest()
			rollback := errors.New("rollback")
			if err := db.Transaction(ctx, func(tx botDB) error {
				if err := tx.(botSkillDB).insertSkillRevision(ctx, next); err != nil {
					return err
				}
				return rollback
			}); !errors.Is(err, rollback) {
				t.Fatal(err)
			}
			if _, err := db.(botSkillDB).readSkillRevision(ctx, v.BotID, v.SkillID, next.Revision); err == nil {
				t.Fatal("rollback revision visible")
			}
			if _, err := sqlDB.ExecContext(ctx, "UPDATE bots SET owner_id = ? WHERE id = ?", uuid.NewString(), v.BotID.String()); err != nil {
				t.Fatal(err)
			}
			if _, err := db.(botSkillDB).readSkillRevision(ctx, v.BotID, v.SkillID, v.Revision); err == nil {
				t.Fatal("cross-owner read")
			}
		})
	}
}
