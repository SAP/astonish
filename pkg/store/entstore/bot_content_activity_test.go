package entstore

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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

func activityContentFixture() store.BotActivity {
	run := uuid.New()
	return store.BotActivity{BotID: uuid.New(), RunID: &run, Sequence: 1<<54 + 3, Kind: store.BotActivityReport, CreatedAt: time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC), Payload: json.RawMessage(`{"intent":"publish","status":"succeeded","report":{"summary":"SENTINEL-report","artifacts":[{"title":"SENTINEL-artifact"}]},"knowledge_source":{"input":"SENTINEL-knowledge"},"outbox":{"payload":{"event":"SENTINEL-event"}},"integer":18014398509481987}`)}
}

func TestBotDatabaseRuntimeIOProfile(t *testing.T) {
	t.Run("configuration-files-excluded", func(t *testing.T) {
		ctx, profile := store.WithBotRuntimeIOProfiler(t.Context(), nil)
		codec := newBotContentCodec(t.TempDir(), store.BotContentScope{OwnerID: uuid.NewString()})
		v := store.DefaultBotVersion()
		v.BotID = uuid.New()
		data, err := codec.encodeVersion(ctx, v)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := codec.decodeVersion(ctx, v.BotID, v.Version, data); err != nil {
			t.Fatal(err)
		}
		if got := profile.Snapshot(); got != (store.BotRuntimeIOSnapshot{}) {
			t.Fatalf("configuration counted as runtime I/O: %+v", got)
		}
	})
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, mode := range []string{"inline", "empty", "disabled"} {
			t.Run(string(scope)+"/"+mode, func(t *testing.T) {
				ctx := t.Context()
				codec := newBotContentCodec(t.TempDir(), store.BotContentScope{OwnerID: uuid.NewString()})
				a := activityContentFixture()
				sqlDB, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "profile.db"))
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { sqlDB.Close() })
				sqlDB.SetMaxOpenConns(1)
				if _, err := sqlDB.ExecContext(ctx, "PRAGMA foreign_keys=ON"); err != nil {
					t.Fatal(err)
				}
				driver := entsql.OpenDB(dialect.SQLite, sqlDB)
				var db botDB
				if scope == store.BotScopePersonal {
					c := personalent.NewClient(personalent.Driver(driver))
					if err := c.Schema.Create(ctx); err != nil {
						t.Fatal(err)
					}
					if _, err := c.Bot.Create().SetID(a.BotID).SetOwnerID(uuid.New()).SetName("profile").SetHomeSessionID(uuid.NewString()).SetCreateKey(uuid.NewString()).Save(ctx); err != nil {
						t.Fatal(err)
					}
					db = personalBotDB{client: c, content: codec}
				} else {
					c := teament.NewClient(teament.Driver(driver))
					if err := c.Schema.Create(ctx); err != nil {
						t.Fatal(err)
					}
					if _, err := c.Bot.Create().SetID(a.BotID).SetOwnerID(uuid.New()).SetName("profile").SetHomeSessionID(uuid.NewString()).SetCreateKey(uuid.NewString()).Save(ctx); err != nil {
						t.Fatal(err)
					}
					db = teamBotDB{client: c, content: codec}
				}
				if mode != "empty" {
					if err := db.InsertActivity(ctx, a, "profile"); err != nil {
						t.Fatal(err)
					}
				}
				codec.Open = func(ctx context.Context, id store.BotID) (store.BotContentStaging, error) {
					t.Error("database runtime accessed file storage")
					return nil, store.ErrBotUnsupported
				}
				var timings []store.BotRuntimeIOTiming
				profileCtx, profile := store.WithBotRuntimeIOProfiler(ctx, func(v store.BotRuntimeIOTiming) { timings = append(timings, v) })
				if mode != "disabled" {
					ctx = profileCtx
				}
				stages := map[string]int{}
				ctx = store.WithBotActivityProfiler(ctx, func(v store.BotActivityTiming) { stages[v.Stage]++ })
				// A SQL runtime write must never be reported as a file put.
				if mode == "inline" {
					next := a
					next.Sequence++
					if err := db.InsertActivity(ctx, next, "next"); err != nil {
						t.Fatal(err)
					}
				}
				got, err := db.ReadActivity(ctx, a.BotID, 0, 1)
				if err != nil || (mode != "empty" && (len(got) != 1 || !reflect.DeepEqual(got[0], a))) || (mode == "empty" && len(got) != 0) {
					t.Fatalf("read: %+v %v", got, err)
				}
				want := store.BotRuntimeIOSnapshot{Covered: mode != "disabled"}
				if snapshot := profile.Snapshot(); snapshot != want {
					t.Fatalf("runtime I/O: %+v want %+v", snapshot, want)
				}
				if len(timings) != 0 {
					t.Fatalf("timings: %+v", timings)
				}
			})
		}
	}
}

func TestBotContentActivityCodec(t *testing.T) {
	dir, scope := t.TempDir(), store.BotContentScope{OwnerID: uuid.NewString()}
	codec := newBotContentCodec(dir, scope)
	a := activityContentFixture()
	data, err := codec.encodeActivity(t.Context(), a)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(data)
	var fromSQL map[string]any
	if err := json.Unmarshal(raw, &fromSQL); err != nil {
		t.Fatal(err)
	}
	got, err := newBotContentCodec(dir, scope).decodeActivity(t.Context(), a.BotID, a.Sequence, a.Kind, a.CreatedAt, fromSQL)
	if err != nil || !reflect.DeepEqual(*got, a) {
		t.Fatalf("hydrate: %v %#v", err, got)
	}
	var record botActivityRecord
	if err := decodeBotRecord(data, &record); err != nil {
		t.Fatal(err)
	}
	// Corrupt the inline body: integrity check must reject it.
	corrupt := record
	corrupt.Inline = bytes.Replace(append([]byte(nil), record.Inline...), []byte("SENTINEL-report"), []byte("CORRUPT!-report"), 1)
	corruptRaw, _ := json.Marshal(corrupt)
	if _, err := codec.decodeActivity(t.Context(), a.BotID, a.Sequence, a.Kind, a.CreatedAt, map[string]any{"bot_record_v2": string(corruptRaw)}); err == nil {
		t.Fatal("corrupt bytes accepted")
	}
	for name, mutate := range map[string]func(*botActivityRecord){
		"bot":      func(r *botActivityRecord) { r.BotID = uuid.New() },
		"run":      func(r *botActivityRecord) { id := uuid.New(); r.RunID = &id },
		"sequence": func(r *botActivityRecord) { r.Sequence++ },
		"kind":     func(r *botActivityRecord) { r.Kind = store.BotActivityRun },
		"digest":   func(r *botActivityRecord) { r.SHA256 = strings.Repeat("0", 64) },
		"size":     func(r *botActivityRecord) { r.SizeBytes++ },
		"intent":   func(r *botActivityRecord) { r.Intent = store.BotOutboxDispatch },
	} {
		t.Run(name, func(t *testing.T) {
			altered := record
			mutate(&altered)
			raw, _ := json.Marshal(altered)
			if _, err := codec.decodeActivity(t.Context(), a.BotID, a.Sequence, a.Kind, a.CreatedAt, map[string]any{"bot_record_v2": string(raw)}); err == nil {
				t.Fatal("changed identity accepted")
			}
		})
	}
	legacy, _ := botJSONMap(a)
	if _, err := codec.decodeActivity(t.Context(), a.BotID, a.Sequence, a.Kind, a.CreatedAt, legacy); !errors.Is(err, errBotDevelopmentContentFormat) {
		t.Fatalf("legacy: %v", err)
	}
}

func TestBotContentActivityDispatchCoordination(t *testing.T) {
	a := activityContentFixture()
	a.Kind = progressRecordKind
	dispatchID := uuid.New()
	mutation := store.BotProgressMutation{Lease: store.BotLease{BotID: a.BotID, RunID: *a.RunID, WorkerID: "worker", Fence: 1, ExpiresAt: a.CreatedAt.Add(time.Hour)}, Binding: store.BotProgressBinding{BotID: a.BotID, RunID: *a.RunID, SessionID: uuid.New(), Version: 1, WorkRevision: 1}}
	dispatch := botProgressDispatch{Request: store.BotProgressBeginDispatchRequest{Mutation: mutation, DispatchID: dispatchID, Kind: store.BotProgressDispatchTool, OperationDigest: strings.Repeat("a", 64)}, Receipt: store.BotProgressDispatchReceipt{DispatchID: dispatchID, LedgerCursor: 1}}
	a.Payload, _ = json.Marshal(dispatch)
	codec := newBotContentCodec(t.TempDir(), store.BotContentScope{OwnerID: uuid.NewString()})
	data, err := codec.encodeActivity(t.Context(), a)
	if err != nil {
		t.Fatal(err)
	}
	var record botActivityRecord
	if err := decodeBotRecord(data, &record); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(record.Dispatch, &dispatch) {
		t.Fatal("dispatch metadata lost")
	}
	got, err := codec.decodeActivity(t.Context(), a.BotID, a.Sequence, a.Kind, a.CreatedAt, data)
	if err != nil || !reflect.DeepEqual(got, &a) {
		t.Fatalf("dispatch replay: %v", err)
	}
}

func TestBotContentActivityPersistence(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			ctx := t.Context()
			path := filepath.Join(t.TempDir(), "activity.db")
			contentDir := t.TempDir()
			owner := uuid.New()
			a := activityContentFixture()
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
				codec := newBotContentCodec(contentDir, store.BotContentScope{OwnerID: owner.String()})
				if scope == store.BotScopePersonal {
					c := personalent.NewClient(personalent.Driver(driver))
					if err := c.Schema.Create(ctx); err != nil {
						t.Fatal(err)
					}
					db = personalBotDB{client: c, content: codec}
				} else {
					c := teament.NewClient(teament.Driver(driver))
					if err := c.Schema.Create(ctx); err != nil {
						t.Fatal(err)
					}
					db = teamBotDB{client: c, content: codec}
				}
			}
			open()
			t.Cleanup(func() { sqlDB.Close() })
			err := db.Transaction(ctx, func(tx botDB) error {
				switch d := tx.(type) {
				case personalBotDB:
					_, err := d.client.Bot.Create().SetID(a.BotID).SetOwnerID(owner).SetName("Activity fixture").SetHomeSessionID(uuid.NewString()).SetCreateKey(uuid.NewString()).Save(ctx)
					if err != nil {
						return err
					}
				case teamBotDB:
					_, err := d.client.Bot.Create().SetID(a.BotID).SetOwnerID(owner).SetName("Activity fixture").SetHomeSessionID(uuid.NewString()).SetCreateKey(uuid.NewString()).Save(ctx)
					if err != nil {
						return err
					}
				}
				return tx.InsertActivity(ctx, a, "result:exact")
			})
			if err != nil {
				t.Fatal(err)
			}
			var raw []byte
			if err := sqlDB.QueryRowContext(ctx, "SELECT payload FROM bot_outbox WHERE bot_id = ?", a.BotID.String()).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			if err := sqlDB.Close(); err != nil {
				t.Fatal(err)
			}
			open()
			got, err := db.FindActivity(ctx, a.BotID, "result:exact")
			if err != nil || !reflect.DeepEqual(got, &a) {
				t.Fatalf("replay: %v %#v", err, got)
			}
			list, err := db.ReadActivity(ctx, a.BotID, a.Sequence-1, 1)
			if err != nil || len(list) != 1 || !reflect.DeepEqual(list[0], a) {
				t.Fatalf("list: %v %#v", err, list)
			}
			// Duplicate keys still use SQL uniqueness; staged orphan bytes grant no replay.
			if err := db.InsertActivity(ctx, a, "result:exact"); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("duplicate: %v", err)
			}
		})
	}
}

type activityPublicationReadProbe struct {
	botDB
	activity store.BotActivity
	limit    int
}

func (p *activityPublicationReadProbe) ReadRun(context.Context, uuid.UUID, uuid.UUID) (*botStoredRun, error) {
	return &botStoredRun{Run: store.BotRun{Report: store.BotReport{Summary: "newer mutable report"}}}, nil
}
func (p *activityPublicationReadProbe) ReadActivity(_ context.Context, _ uuid.UUID, after int64, limit int) ([]store.BotActivity, error) {
	p.limit = limit
	if after != p.activity.Sequence-1 || limit != 1 {
		return nil, errors.New("would hydrate unrelated corrupt later report")
	}
	return []store.BotActivity{p.activity}, nil
}
func TestBotContentActivityExactPublicationReplay(t *testing.T) {
	a := activityContentFixture()
	p := &activityPublicationReadProbe{activity: a}
	run, sequence, err := committedPublicationReport(t.Context(), p, a.BotID, *a.RunID, a.Sequence)
	if err != nil || sequence != a.Sequence || run.Report.Summary != "SENTINEL-report" || p.limit != 1 {
		t.Fatalf("exact replay: %v %d %#v", err, sequence, run.Report)
	}
}
