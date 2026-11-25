package entstore

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	personalent "github.com/SAP/astonish/ent/personal"
	teament "github.com/SAP/astonish/ent/team"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// Exercise the actual SQL adapters, including transaction-local Bot lookup and
// closing/reopening SQLite and the disk codec. No mock content store is used.
func TestBotContentTaskPersistence(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			path := filepath.Join(dir, "tasks.db")
			contentDir := t.TempDir()
			var sqlDB *sql.DB
			var db botDraftDB
			open := func() {
				var err error
				sqlDB, err = sql.Open("sqlite", path)
				if err != nil {
					t.Fatal(err)
				}
				sqlDB.SetMaxOpenConns(1) // outer-DB lookup inside a transaction would deadlock
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
			// Task repositories must forward the scoped store's codec.
			owner := uuid.New()
			auth := botRepositoryAuth{orgSlug: "org", teamSlug: "team", userID: owner.String(), scope: scope, check: func(context.Context, string) (string, error) { return "member", nil }}
			var repository *botRepository
			switch d := db.(type) {
			case personalBotDB:
				repository = (&personalBotStore{client: d.client, content: d.content, auth: auth}).taskRepository()
			case teamBotDB:
				repository = (&teamBotStore{client: d.client, content: d.content, auth: auth}).taskRepository()
			}
			// The lower-level adapter fixture does not require an authenticated API
			// caller; owner metadata is inserted in the same transaction as the task.
			candidate := botContentTaskFixture()
			candidate.Parent = nil
			candidate.Ref.Revision = 1
			candidate.Manifest.Goal, candidate.Manifest.Skills = nil, nil
			candidate.Ref.Digest = candidate.ContentDigest()
			task := store.BotTask{ID: candidate.Ref.TaskID, BotID: candidate.BotID, Key: candidate.Ref.TaskID.String(), HomePath: "tasks/" + candidate.Ref.TaskID.String(), Title: "Task index", Status: store.BotTaskReady, Revision: 1}
			env := taskPublicationEnvelope{Publication: store.BotTaskPublication{ID: candidate.ID, BotID: candidate.BotID, TaskID: task.ID, State: store.BotTaskPublicationPrepared, Candidate: candidate, AuthorityConstraintRef: "original"}, Task: task, Mutation: store.BotTaskMutation{BotID: candidate.BotID, ActorID: owner, IdempotencyKey: "once", AuthorityConstraintRef: "original"}}
			err := repository.db.Transaction(ctx, func(tx botDraftDB) error {
				switch d := tx.(type) {
				case personalBotDB:
					_, err := d.client.Bot.Create().SetID(candidate.BotID).SetOwnerID(owner).SetName("Task fixture").SetHomeSessionID(uuid.NewString()).SetCreateKey(uuid.NewString()).Save(ctx)
					if err != nil {
						return err
					}
				case teamBotDB:
					_, err := d.client.Bot.Create().SetID(candidate.BotID).SetOwnerID(owner).SetName("Task fixture").SetHomeSessionID(uuid.NewString()).SetCreateKey(uuid.NewString()).Save(ctx)
					if err != nil {
						return err
					}
				}
				tasks := tx.(botTaskDB)
				if err := tasks.writeTask(ctx, task, 0); err != nil {
					return err
				}
				return tasks.insertTaskPublication(ctx, env)
			})
			if err != nil {
				t.Fatal(err)
			}
			var raw []byte
			if err := sqlDB.QueryRowContext(ctx, "SELECT data FROM bot_task_revisions WHERE id = ?", candidate.ID.String()).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			for _, sentinel := range []string{candidate.Manifest.Title, candidate.Manifest.Assignment, candidate.Manifest.OwnerRequirements[0], candidate.Manifest.Assumptions[0], candidate.Manifest.OutputRules[0], "SENTINEL-task-value", "SENTINEL-task-param", string(candidate.Bundle["TASK.md"])} {
				quoted, _ := json.Marshal(sentinel)
				if bytes.Contains(raw, []byte(sentinel)) || bytes.Contains(raw, quoted) || bytes.Contains(raw, []byte(base64.StdEncoding.EncodeToString([]byte(sentinel)))) {
					t.Fatalf("SQL contains task body %q", sentinel)
				}
			}
			if err := sqlDB.Close(); err != nil {
				t.Fatal(err)
			}
			open()
			got, err := db.(botTaskDB).readTaskPublication(ctx, candidate.BotID, candidate.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(env, *got) || got.Publication.Candidate.ContentDigest() != candidate.Ref.Digest {
				t.Fatalf("reopened task changed: %#v", got)
			}
			// A swapped owner must not open another owner's immutable task.
			if _, err := sqlDB.ExecContext(ctx, "UPDATE bots SET owner_id = ? WHERE id = ?", uuid.NewString(), candidate.BotID.String()); err != nil {
				t.Fatal(err)
			}
			if _, err := db.(botTaskDB).readTaskPublication(ctx, candidate.BotID, candidate.ID); err == nil {
				t.Fatal("cross-owner reference accepted")
			}
			if _, err := sqlDB.ExecContext(ctx, "UPDATE bots SET owner_id = ? WHERE id = ?", owner.String(), candidate.BotID.String()); err != nil {
				t.Fatal(err)
			}
			// A SQL rollback cannot publish a staged candidate.
			rollback := errors.New("rollback candidate")
			next := env
			next.Publication.Candidate = *candidate.Clone()
			next.Publication.ID = uuid.New()
			next.Publication.Candidate.ID = next.Publication.ID
			next.Publication.Candidate.Ref.Revision++
			next.Publication.Candidate.Ref.Digest = next.Publication.Candidate.ContentDigest()
			err = db.Transaction(ctx, func(tx botDraftDB) error {
				if err := tx.(botTaskDB).insertTaskPublication(ctx, next); err != nil {
					return err
				}
				return rollback
			})
			if !errors.Is(err, rollback) {
				t.Fatal(err)
			}
			var count int
			if err := sqlDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM bot_task_revisions WHERE id = ?", next.Publication.ID.String()).Scan(&count); err != nil || count != 0 {
				t.Fatalf("rolled-back candidate visible: %d %v", count, err)
			}
			// Missing immutable storage must fail; no current-file/inline fallback.
			var data map[string]any
			if err := json.Unmarshal(raw, &data); err != nil {
				t.Fatal(err)
			}
			var record botTaskPublicationRecord
			if err := decodeBotRecord(data, &record); err != nil {
				t.Fatal(err)
			}
			var revision botTaskRevisionRecord
			if err := decodeBotRecord(record.Candidate, &revision); err != nil {
				t.Fatal(err)
			}
			switch d := db.(type) {
			case personalBotDB:
				d.content = newScopedBotContentCodec(t.TempDir(), "org", "team")
				db = d
			case teamBotDB:
				d.content = newScopedBotContentCodec(t.TempDir(), "org", "team")
				db = d
			}
			if _, err := db.(botTaskDB).readTaskPublication(ctx, candidate.BotID, candidate.ID); err == nil {
				t.Fatal("missing bytes accepted")
			}
			// Old development envelopes are explicitly rejected, even with complete body.
			legacy, err := botJSONMap(env)
			if err != nil {
				t.Fatal(err)
			}
			legacyRaw, _ := json.Marshal(legacy)
			if _, err := sqlDB.ExecContext(ctx, "UPDATE bot_task_revisions SET data = ? WHERE id = ?", string(legacyRaw), candidate.ID.String()); err != nil {
				t.Fatal(err)
			}
			if _, err := db.(botTaskDB).readTaskPublication(ctx, candidate.BotID, candidate.ID); !errors.Is(err, store.ErrBotUnsupported) {
				t.Fatalf("legacy fallback: %v", err)
			}
		})
	}
}
