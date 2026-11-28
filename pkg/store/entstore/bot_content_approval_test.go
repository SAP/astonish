package entstore

import (
	"bytes"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"errors"
	personalent "github.com/SAP/astonish/ent/personal"
	teament "github.com/SAP/astonish/ent/team"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func botContentApprovalFixture(t *testing.T) store.BotApproval {
	t.Helper()
	now := time.Date(2026, 11, 17, 20, 0, 0, 0, time.UTC)
	owner := uuid.New()
	a := store.BotApproval{ID: uuid.New(), BotID: uuid.New(), RunID: uuid.New(), Version: 1<<60 + 37, Status: store.ApprovalApproved, CreatedAt: now, ExpiresAt: now.Add(time.Hour), DecidedAt: &now, DecidedBy: &owner,
		Action:      execution.ActionDescriptor{Tool: "send", Effect: execution.EffectSend, Arguments: json.RawMessage(`{ "body": "APPROVAL-SENTINEL", "id": 1152921504606847013 }`), TargetSnapshot: json.RawMessage(`{ "target": "APPROVAL-SENTINEL-snapshot" }`), Recipient: "APPROVAL-SENTINEL-recipient", Merchant: "APPROVAL-SENTINEL-merchant", TargetFingerprint: strings.Repeat("f", 64), Currency: "USD", SpendMinor: 1<<60 + 9, ConstraintsVerified: true},
		Interaction: &store.BotInteraction{Kind: store.BotInteractionQuestion, QuestionID: "question", Payload: json.RawMessage(`{ "prompt": "APPROVAL-SENTINEL-question" }`)}, Response: &store.BotInteractionResponse{QuestionID: "question", Answer: "APPROVAL-SENTINEL-answer"}}
	var err error
	a.ActionDigest, err = a.Action.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func approvalRecordEnvelope(t *testing.T, r botApprovalRecord) map[string]any {
	t.Helper()
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	return map[string]any{"bot_record_v2": string(raw)}
}

func TestBotContentApprovalSQLAllowlist(t *testing.T) {
	forbidden := map[reflect.Type]bool{reflect.TypeOf(store.BotApproval{}): true, reflect.TypeOf(execution.ActionDescriptor{}): true, reflect.TypeOf(store.BotInteraction{}): true, reflect.TypeOf(json.RawMessage{}): true}
	seen := map[reflect.Type]bool{}
	var visit func(reflect.Type)
	visit = func(typ reflect.Type) {
		if forbidden[typ] || typ.Kind() == reflect.Interface {
			t.Fatalf("unallowlisted SQL type: %v", typ)
		}
		if seen[typ] {
			return
		}
		seen[typ] = true
		switch typ.Kind() {
		case reflect.Pointer, reflect.Slice, reflect.Array:
			visit(typ.Elem())
		case reflect.Map:
			visit(typ.Key())
			visit(typ.Elem())
		case reflect.Struct:
			for i := 0; i < typ.NumField(); i++ {
				f := typ.Field(i)
				if f.IsExported() && f.Tag.Get("json") != "-" {
					visit(f.Type)
				}
			}
		}
	}
	visit(reflect.TypeOf(botApprovalRecord{}))
}

func TestBotContentApprovalCodec(t *testing.T) {
	dir := t.TempDir()
	scope := store.BotContentScope{OrgID: "org", TeamID: "team", OwnerID: uuid.NewString()}
	c := newBotContentCodec(dir, scope)
	a := botContentApprovalFixture(t)
	data, err := c.encodeApproval(t.Context(), a)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	var persisted map[string]any
	if err := json.Unmarshal(raw, &persisted); err != nil {
		t.Fatal(err)
	}
	got, err := newBotContentCodec(dir, scope).decodeApproval(t.Context(), a.BotID, persisted)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a, *got) {
		t.Fatalf("roundtrip differs: %#v / %#v", a, *got)
	}
	var r botApprovalRecord
	if err := decodeBotRecord(data, &r); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*botApprovalRecord){
		"bot": func(r *botApprovalRecord) { r.BotID = uuid.New() }, "approval": func(r *botApprovalRecord) { r.ID = uuid.New() }, "run": func(r *botApprovalRecord) { r.RunID = uuid.New() }, "version": func(r *botApprovalRecord) { r.Version++ },
		"digest": func(r *botApprovalRecord) { r.ActionDigest = strings.Repeat("0", 64) }, "tool": func(r *botApprovalRecord) { r.Tool = "delete" }, "effect": func(r *botApprovalRecord) { r.Effect = execution.EffectDelete }, "target": func(r *botApprovalRecord) { r.TargetFingerprint = "different" }, "spend": func(r *botApprovalRecord) { r.SpendMinor++ },
		"size": func(r *botApprovalRecord) { r.SizeBytes++ }, "bodydigest": func(r *botApprovalRecord) { r.SHA256 = strings.Repeat("0", 64) },
	} {
		t.Run(name, func(t *testing.T) {
			bad := r
			mutate(&bad)
			if _, err := c.decodeApproval(t.Context(), a.BotID, approvalRecordEnvelope(t, bad)); err == nil {
				t.Fatal("accepted invalid metadata")
			}
		})
	}
	if _, err := c.decodeApproval(t.Context(), uuid.New(), data); err == nil {
		t.Fatal("cross bot accepted")
	}
	a.ActionDigest = ""
	if _, err := c.encodeApproval(t.Context(), a); err == nil {
		t.Fatal("missing exact action digest accepted")
	}
	if _, err := c.decodeApproval(t.Context(), a.BotID, map[string]any{"action": a.Action}); err == nil {
		t.Fatal("inline fallback accepted")
	}
}

func TestBotContentApprovalCorruptMissing(t *testing.T) {
	c := newBotContentCodec(t.TempDir(), store.BotContentScope{OwnerID: uuid.NewString()})
	a := botContentApprovalFixture(t)
	data, err := c.encodeApproval(t.Context(), a)
	if err != nil {
		t.Fatal(err)
	}
	var r botApprovalRecord
	if err := decodeBotRecord(data, &r); err != nil {
		t.Fatal(err)
	}
	// Corrupt the inline body: integrity check must reject it.
	corrupt := r
	corrupt.Inline = bytes.Replace(append([]byte(nil), r.Inline...), []byte("APPROVAL-SENTINEL"), []byte("CORRUPT-SENTINEL!"), 1)
	if _, err := c.decodeApproval(t.Context(), a.BotID, approvalRecordEnvelope(t, corrupt)); err == nil {
		t.Fatal("corrupt body accepted")
	}
	// A different trusted action's body cannot be substituted: the SQL digest
	// still pins the original descriptor independently of the body hash.
	changed := a
	changed.Action.Arguments = json.RawMessage(`{"body":"different action"}`)
	changed.ActionDigest, err = changed.Action.Digest()
	if err != nil {
		t.Fatal(err)
	}
	other, err := c.encodeApproval(t.Context(), changed)
	if err != nil {
		t.Fatal(err)
	}
	var otherRecord botApprovalRecord
	if err := decodeBotRecord(other, &otherRecord); err != nil {
		t.Fatal(err)
	}
	substituted := r
	substituted.botRuntimeInline = otherRecord.botRuntimeInline
	if _, err := c.decodeApproval(t.Context(), a.BotID, approvalRecordEnvelope(t, substituted)); err == nil {
		t.Fatal("valid body substituted a different action")
	}
}

func TestBotContentApprovalSQLiteReopen(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			ctx := t.Context()
			path := filepath.Join(t.TempDir(), "approvals.db")
			contentDir := t.TempDir()
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
			a := botContentApprovalFixture(t)
			a.Status = store.ApprovalPending
			a.DecidedAt = nil
			a.DecidedBy = nil
			a.Response = nil
			owner := uuid.New()
			err := db.Transaction(ctx, func(tx botDB) error {
				switch d := tx.(type) {
				case personalBotDB:
					if _, err := d.client.Bot.Create().SetID(a.BotID).SetOwnerID(owner).SetName("Approval fixture").SetHomeSessionID(uuid.NewString()).SetCreateKey(uuid.NewString()).Save(ctx); err != nil {
						return err
					}
					if _, err := d.client.BotRun.Create().SetID(a.RunID).SetBotID(a.BotID).SetVersion(a.Version).SetSessionID(uuid.NewString()).SetIdempotencyKey("once").SetRequestHash("hash").SetData(map[string]any{}).Save(ctx); err != nil {
						return err
					}
				case teamBotDB:
					if _, err := d.client.Bot.Create().SetID(a.BotID).SetOwnerID(owner).SetName("Approval fixture").SetHomeSessionID(uuid.NewString()).SetCreateKey(uuid.NewString()).Save(ctx); err != nil {
						return err
					}
					if _, err := d.client.BotRun.Create().SetID(a.RunID).SetBotID(a.BotID).SetVersion(a.Version).SetSessionID(uuid.NewString()).SetIdempotencyKey("once").SetRequestHash("hash").SetData(map[string]any{}).Save(ctx); err != nil {
						return err
					}
				}
				return tx.InsertApproval(ctx, a)
			})
			if err != nil {
				t.Fatal(err)
			}
			var raw []byte
			if err := sqlDB.QueryRowContext(ctx, "SELECT data FROM bot_approvals WHERE id = ?", a.ID.String()).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			for _, s := range []string{"APPROVAL-SENTINEL", string(a.Action.Arguments), string(a.Interaction.Payload)} {
				quoted, _ := json.Marshal(s)
				if bytes.Contains(raw, []byte(s)) || bytes.Contains(raw, quoted) || bytes.Contains(raw, []byte(base64.StdEncoding.EncodeToString([]byte(s)))) {
					t.Fatal("SQL contains approval body")
				}
			}
			if err := sqlDB.Close(); err != nil {
				t.Fatal(err)
			}
			open()
			got, err := db.ReadApproval(ctx, a.BotID, a.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(a, *got) {
				t.Fatalf("reopen changed approval: %#v", got)
			}
			a.Status = store.ApprovalApproved
			a.DecidedBy = &owner
			a.DecidedAt = &a.CreatedAt
			a.Response = &store.BotInteractionResponse{QuestionID: a.Interaction.QuestionID, Answer: "APPROVAL-SENTINEL-answer"}
			if err := db.UpdateApproval(ctx, a, store.ApprovalPending, a.CreatedAt); err != nil {
				t.Fatal(err)
			}
			a.Status = store.ApprovalConsumed
			if err := db.UpdateApproval(ctx, a, store.ApprovalApproved, a.CreatedAt); err != nil {
				t.Fatal(err)
			}
			if err := sqlDB.Close(); err != nil {
				t.Fatal(err)
			}
			open()
			got, err = db.ReadApproval(ctx, a.BotID, a.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(a, *got) {
				t.Fatal("consumption lost on reopen")
			}
			if err := db.UpdateApproval(ctx, a, store.ApprovalApproved, a.CreatedAt); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("replay after reopen: %v", err)
			}
			if _, err := sqlDB.ExecContext(ctx, "UPDATE bots SET owner_id = ? WHERE id = ?", uuid.NewString(), a.BotID.String()); err != nil {
				t.Fatal(err)
			}
			if _, err := db.ReadApproval(ctx, a.BotID, a.ID); err == nil {
				t.Fatal("cross owner accepted")
			}
		})
	}
}

func TestBotContentApprovalSQLConsumption(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			repo, ctx, worker, run, lease, now := botRunFixture(t, scope)
			action := execution.ActionDescriptor{Tool: "send", Effect: execution.EffectSend, Arguments: []byte(`{"body":"APPROVAL-SENTINEL-SQL"}`), Recipient: "APPROVAL-SENTINEL-recipient"}
			digest, err := action.Digest()
			if err != nil {
				t.Fatal(err)
			}
			a, err := repo.RequestApproval(worker, lease, store.BotApproval{BotID: run.BotID, RunID: run.ID, Version: run.Version, Action: action, ActionDigest: digest, ExpiresAt: now.Add(time.Hour)})
			if err != nil {
				t.Fatal(err)
			}
			var data map[string]any
			switch d := repo.db.(type) {
			case personalBotDB:
				row, err := d.client.BotApproval.Get(ctx, a.ID)
				if err != nil {
					t.Fatal(err)
				}
				data = row.Data
			case teamBotDB:
				row, err := d.client.BotApproval.Get(ctx, a.ID)
				if err != nil {
					t.Fatal(err)
				}
				data = row.Data
			}
			raw, err := json.Marshal(data)
			if err != nil {
				t.Fatal(err)
			}
			for _, s := range []string{"APPROVAL-SENTINEL", base64.StdEncoding.EncodeToString(action.Arguments)} {
				if bytes.Contains(raw, []byte(s)) {
					t.Fatal("real SQL leaked content")
				}
			}
			bot, err := repo.Get(ctx, run.BotID)
			if err != nil {
				t.Fatal(err)
			}
			a, err = repo.DecideApproval(ctx, store.BotApprovalDecision{BotID: run.BotID, ApprovalID: a.ID, ActorID: bot.OwnerID, ActionDigest: digest, Status: store.ApprovalApproved})
			if err != nil {
				t.Fatal(err)
			}
			q := store.BotApprovalConsumption{Lease: lease, ApprovalID: a.ID, Version: a.Version, ActionDigest: digest}
			if err := repo.ConsumeApproval(worker, q); err != nil {
				t.Fatal(err)
			}
			if err := repo.ConsumeApproval(worker, q); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("consumed approval replay: %v", err)
			}
			saved, err := repo.db.ReadApproval(ctx, run.BotID, a.ID)
			if err != nil {
				t.Fatal(err)
			}
			if saved.Status != store.ApprovalConsumed || saved.DecidedBy == nil || *saved.DecidedBy != bot.OwnerID || saved.ActionDigest != digest || !bytes.Equal(saved.Action.Arguments, action.Arguments) {
				t.Fatalf("consumed state lost: %#v", saved)
			}
			if _, err := repo.db.ReadApproval(ctx, uuid.New(), a.ID); !errors.Is(err, store.ErrBotNotFound) {
				t.Fatalf("scope: %v", err)
			}
		})
	}
}
