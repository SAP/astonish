package entstore

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	personalent "github.com/SAP/astonish/ent/personal"
	teament "github.com/SAP/astonish/ent/team"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestBotContentProgressControlPersistence(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			ctx := t.Context()
			path, contentDir := filepath.Join(t.TempDir(), "private.db"), t.TempDir()
			owner, botID, runID := uuid.New(), uuid.New(), uuid.New()
			var sqlDB *sql.DB
			var db botDraftDB
			var codec *botContentCodec
			contentOpens := 0
			open := func() {
				var err error
				sqlDB, err = sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)")
				if err != nil {
					t.Fatal(err)
				}
				sqlDB.SetMaxOpenConns(1)
				driver := entsql.OpenDB(dialect.SQLite, sqlDB)
				contentScope := store.BotContentScope{OwnerID: owner.String()}
				if scope == store.BotScopeTeam {
					contentScope.OrgID, contentScope.TeamID = "private-org", "private-team"
				}
				codec = newBotContentCodec(contentDir, contentScope)
				opener := codec.Open
				codec.Open = func(ctx context.Context, id store.BotID) (store.BotContentStaging, error) {
					contentOpens++
					return opener(ctx, id)
				}
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
			now := time.Now().UTC().Truncate(time.Microsecond)
			sentinel := `PRIVATE-SENTINEL-"context-control"-body`
			binding := store.BotProgressBinding{BotID: botID, RunID: runID, SessionID: uuid.New(), Version: 1, WorkRevision: 1}
			contextRecord := botProgressContextRecord{Binding: binding, Data: []byte(sentinel), Digest: store.BotProgressContextDigest([]byte(sentinel))}
			proposal := store.BotControlProposal{ID: uuid.New(), BotID: botID, SourceRunID: runID, SourceUserID: owner, Version: 1, ExpectedRevision: 1, Payload: store.BotControlPayload{Operation: store.BotControlResponsibility, Reason: sentinel, Responsibility: &sentinel}, Status: store.BotControlProposed, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
			proposal.Digest, _ = botHash(proposal)
			decision := proposal
			decision.Status, decision.DecidedAt, decision.DecidedBy = store.BotControlRejected, &now, &owner
			values := []any{contextRecord, controlProposalEnvelope{Record: controlProposalRecord, Proposal: proposal, RequestHash: store.BotProgressContextDigest([]byte("request"))}, controlProposalEnvelope{Record: controlProposalRecord, Proposal: decision}}
			keys := []string{"context", controlProposalKey(proposal.ID), controlProposalKey(proposal.ID) + ":decision"}
			activities := make([]store.BotActivity, len(values))
			err := db.Transaction(ctx, func(tx botDraftDB) error {
				switch d := tx.(type) {
				case personalBotDB:
					if _, err := d.client.Bot.Create().SetID(botID).SetOwnerID(owner).SetName("private fixture").SetHomeSessionID(uuid.NewString()).SetCreateKey(uuid.NewString()).Save(ctx); err != nil {
						return err
					}
				case teamBotDB:
					if _, err := d.client.Bot.Create().SetID(botID).SetOwnerID(owner).SetName("private fixture").SetHomeSessionID(uuid.NewString()).SetCreateKey(uuid.NewString()).Save(ctx); err != nil {
						return err
					}
				}
				for i, v := range values {
					payload, err := json.Marshal(v)
					if err != nil {
						return err
					}
					kind := store.BotActivityLifecycle
					if i == 0 {
						kind = progressRecordKind
					}
					activities[i] = store.BotActivity{BotID: botID, RunID: &runID, Sequence: int64(i + 1), Kind: kind, CreatedAt: now, Payload: payload}
					if err := tx.InsertActivity(ctx, activities[i], keys[i]); err != nil {
						return err
					}
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			rows, err := sqlDB.QueryContext(ctx, "SELECT payload FROM bot_outbox WHERE bot_id = ? ORDER BY sequence", botID.String())
			if err != nil {
				t.Fatal(err)
			}
			var records []botActivityRecord
			for rows.Next() {
				var raw []byte
				if err := rows.Scan(&raw); err != nil {
					t.Fatal(err)
				}
				quoted, _ := json.Marshal(sentinel)
				for _, secret := range [][]byte{[]byte("PRIVATE-SENTINEL"), []byte(sentinel), quoted, []byte(base64.StdEncoding.EncodeToString([]byte(sentinel)))} {
					if bytes.Contains(raw, secret) {
						t.Fatal("SQL contains private body")
					}
				}
				var data map[string]any
				if err := json.Unmarshal(raw, &data); err != nil {
					t.Fatal(err)
				}
				var record botActivityRecord
				if err := decodeBotRecord(data, &record); err != nil {
					t.Fatal(err)
				}
				records = append(records, record)
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			rows.Close()
			if len(records) != 3 || records[0].ProgressContext == nil || records[0].ProgressContext.Digest != contextRecord.Digest || records[1].ControlProposal == nil || records[1].ControlProposal.Status != store.BotControlProposed || records[2].ControlProposal == nil || records[2].ControlProposal.Status != store.BotControlRejected {
				t.Fatal("SQL authority missing")
			}
			if err := sqlDB.Close(); err != nil {
				t.Fatal(err)
			}
			open()
			var restored botProgressContextRecord
			if err := progressRead(ctx, db, botID, keys[0], &restored); err != nil || !reflect.DeepEqual(restored, contextRecord) {
				t.Fatalf("context replay: %v", err)
			}
			got, err := readControlProposal(ctx, db, botID, proposal.ID)
			if err != nil || !reflect.DeepEqual(got, &decision) {
				t.Fatalf("decision replay: %v %#v", err, got)
			}
			contentOpens = 0
			list, err := listControlProposals(ctx, db, botID)
			// Runtime bodies are database-inline; listing never opens workspace content.
			if contentOpens != 0 {
				t.Errorf("control listing opened %d workspace objects; want zero", contentOpens)
			}
			if err != nil || len(list) != 1 || !reflect.DeepEqual(list[0], decision) {
				t.Fatalf("proposal list: %v", err)
			}
			for i, record := range records {
				a := activities[i]
				// File contents cannot override the SQL-selected digest or decision.
				changed := record
				if changed.ProgressContext != nil {
					copy := *changed.ProgressContext
					copy.Digest = store.BotProgressContextDigest([]byte("wrong"))
					changed.ProgressContext = &copy
				} else {
					copy := *changed.ControlProposal
					copy.ExpectedRevision++
					changed.ControlProposal = &copy
				}
				raw, _ := json.Marshal(changed)
				if _, err := codec.decodeActivity(ctx, botID, a.Sequence, a.Kind, a.CreatedAt, map[string]any{"bot_record_v2": string(raw)}); err == nil {
					t.Fatal("altered SQL authority accepted")
				}
				_ = record
			}
		})
	}
}
