package entstore

import (
	"context"
	"errors"
	"fmt"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"reflect"
	"strings"
	"testing"
	"time"
)

func permanentTestVersion() store.BotVersion {
	v := store.DefaultBotVersion()
	v.Charter = "Remain idle"
	return v
}

func TestBotPermanentVerifiedPurge(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			repo, owner, _, run, lease, now := botRunFixture(t, scope)
			reservedKeys := []string{"mandate:test:1", "mandate-revoked:test:1", "mandate-operation:test:1", "mandate-reservation:test", "mandate-quota:test:1"}
			if err := repo.db.Transaction(owner, func(tx botDraftDB) error {
				rec, err := tx.GetBot(owner, run.BotID)
				if err != nil {
					return err
				}
				for _, key := range reservedKeys {
					if err := appendMandateRecord(owner, tx, rec, key, map[string]any{"reserved": true}, *now); err != nil {
						return err
					}
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			soft, teardown := cleanupDeleteFixture(t, repo, owner, run, lease)
			other, err := repo.CreateDraft(owner, store.BotCreateRequest{Name: "Unrelated", OwnerID: soft.OwnerID, Version: permanentTestVersion(), IdempotencyKey: "other"})
			if err != nil {
				t.Fatal(err)
			}
			q := store.BotPermanentDeleteRequest{BotID: soft.ID, ActorID: soft.OwnerID, ExpectedRevision: soft.Revision, Confirm: true}
			deleted, err := repo.BeginPermanentDelete(owner, q)
			if err != nil {
				t.Fatal(err)
			}
			if !deleted.PermanentDelete || !deleted.RestoreUntil.Equal(*now) {
				t.Fatal("not immediate irreversible intent")
			}
			restarted := *repo
			retry, err := restarted.BeginPermanentDelete(owner, q)
			if err != nil || retry.Revision != deleted.Revision {
				t.Fatalf("retry: %+v %v", retry, err)
			}
			*now = now.Add(-time.Hour)
			if _, err = repo.Restore(owner, store.BotLifecycleRequest{BotID: deleted.ID, ActorID: deleted.OwnerID, ExpectedRevision: deleted.Revision, Reason: "rollback"}); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("restore: %v", err)
			}
			*now = *deleted.RestoreUntil
			if err = repo.PurgeDeleted(owner, deleted.ID, deleted.Revision); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("unverified purge: %v", err)
			}
			rec, err := repo.db.GetBot(owner, deleted.ID)
			if err != nil {
				t.Fatal(err)
			}
			teardown.ExpectedRevision = deleted.Revision
			teardown.Fence = rec.Fence
			runtime := cleanupRuntimeContext(t, func(context.Context, store.BotCleanupRequest) error { return nil })
			if err = repo.ConfirmCleanup(runtime, teardown); err != nil {
				t.Fatal(err)
			}
			claim, err := repo.ReserveHomeCleanup(runtime, deleted.Home, deleted.Revision)
			if err != nil {
				t.Fatal(err)
			}
			if err = repo.ConfirmCleanup(runtime, claim.Request); err != nil {
				t.Fatal(err)
			}
			// Bot-owned memory is deleted alongside sessions, not orphaned.
			switch d := repo.db.(type) {
			case personalBotDB:
				_, err = d.client.Memory.Create().SetChunkText("private bot memory").SetSessionID(deleted.HomeSessionID).Save(owner)
			case teamBotDB:
				_, err = d.client.Memory.Create().SetChunkText("private bot memory").SetSessionID(deleted.HomeSessionID).Save(owner)
			}
			if err != nil {
				t.Fatal(err)
			}
			if n, err := repo.PurgeDue(owner, *now, 10); err != nil || n != 0 {
				t.Fatalf("batch consumed permanent completion: %d %v", n, err)
			}
			if err = repo.PurgeDeleted(owner, deleted.ID, deleted.Revision); err != nil {
				t.Fatal(err)
			}
			for _, key := range reservedKeys {
				if _, err = repo.db.FindActivity(owner, deleted.ID, key); !errors.Is(err, store.ErrBotNotFound) {
					t.Fatalf("reserved row survived purge %s: %v", key, err)
				}
			}
			if _, err = repo.Get(owner, deleted.ID); !errors.Is(err, store.ErrBotNotFound) {
				t.Fatal(err)
			}
			if _, err = repo.Get(owner, other.ID); err != nil {
				t.Fatal("unrelated Bot removed", err)
			}
			var count int
			switch d := repo.db.(type) {
			case personalBotDB:
				count, err = d.client.Memory.Query().Count(owner)
			case teamBotDB:
				count, err = d.client.Memory.Query().Count(owner)
			}
			if err != nil || count != 0 {
				t.Fatalf("orphaned memory %d %v", count, err)
			}
		})
	}
}

func TestBotPermanentScopedRowsAndRollback(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			db, bots, memories, ctx, target, other := botMemorySearchFixture(t, scope, "vector")
			mustExec := func(query string, args ...any) {
				t.Helper()
				if _, err := db.ExecContext(ctx, query, args...); err != nil {
					t.Fatal(err)
				}
			}
			// SQLite FTS deletion mirrors production's external-content delete trigger.
			mustExec("CREATE VIRTUAL TABLE memories_fts USING fts5(chunk_text, content='memories', content_rowid='rowid')")
			mustExec("CREATE TRIGGER memory_fts_insert AFTER INSERT ON memories BEGIN INSERT INTO memories_fts(rowid,chunk_text) VALUES (new.rowid,new.chunk_text); END")
			mustExec("CREATE TRIGGER memory_fts_delete AFTER DELETE ON memories BEGIN INSERT INTO memories_fts(memories_fts,rowid,chunk_text) VALUES ('delete',old.rowid,old.chunk_text); END")
			for _, b := range []*store.Bot{target, other} {
				if err := memories.Add(ctx, store.MemoryEntry{Content: "needle", SessionID: b.HomeSessionID.String(), Embedding: []float32{1, 0}}); err != nil {
					t.Fatal(err)
				}
				mustExec("INSERT INTO session_events (session_id,event_data) VALUES (?,?)", b.HomeSessionID.String(), `{"content":"conversation"}`)
			}
			if scope == store.BotScopeTeam {
				mustExec("INSERT INTO sandbox_sessions (id,chat_session_id,template_id,state,exposed_ports) VALUES (?,?,?,?,?)", uuid.NewString(), target.HomeSessionID.String(), uuid.NewString(), "terminated", "[]")
				mustExec("INSERT INTO sandbox_sessions (id,chat_session_id,template_id,state,exposed_ports) VALUES (?,?,?,?,?)", uuid.NewString(), other.HomeSessionID.String(), uuid.NewString(), "running", "[]")
			}
			child, grandchild := uuid.NewString(), uuid.NewString()
			mustExec("INSERT INTO sessions (id,parent_id) VALUES (?,?)", child, target.HomeSessionID.String())
			mustExec("INSERT INTO sessions (id,parent_id) VALUES (?,?)", grandchild, child)
			for _, sid := range []string{child, grandchild} {
				mustExec("INSERT INTO session_events (session_id,event_data) VALUES (?,?)", sid, `{"content":"child"}`)
				mustExec("INSERT INTO cache_diagnostics (session_id,round,cache_stable_path,system_hash,system_changed,system_changed_session,tool_hash,tool_count,tools_changed,tools_changed_session) VALUES (?,0,0,'x',0,0,'x',0,0,0)", sid)
				if err := memories.Add(ctx, store.MemoryEntry{Content: "needle", SessionID: sid, Embedding: []float32{1, 0}}); err != nil {
					t.Fatal(err)
				}
			}
			credentialID := uuid.NewString()
			mustExec("INSERT INTO credentials (id,name,cred_type,encrypted) VALUES (?,?,?,?)", credentialID, "shared", "api_key", []byte("keep"))
			// Snapshot every unrelated root/session/memory row byte-for-byte.
			queries := map[[2]string]string{
				{"bots", "id"}:                          "SELECT * FROM bots WHERE id=?",
				{"sessions", "id"}:                      "SELECT * FROM sessions WHERE id=?",
				{"sessions", "bot_id"}:                  "SELECT * FROM sessions WHERE bot_id=?",
				{"memories", "session_id"}:              "SELECT * FROM memories WHERE session_id=?",
				{"session_events", "session_id"}:        "SELECT * FROM session_events WHERE session_id=?",
				{"cache_diagnostics", "session_id"}:     "SELECT * FROM cache_diagnostics WHERE session_id=?",
				{"credentials", "id"}:                   "SELECT * FROM credentials WHERE id=?",
				{"bot_versions", "bot_id"}:              "SELECT * FROM bot_versions WHERE bot_id=?",
				{"bot_runs", "bot_id"}:                  "SELECT * FROM bot_runs WHERE bot_id=?",
				{"bot_approvals", "bot_id"}:             "SELECT * FROM bot_approvals WHERE bot_id=?",
				{"bot_goals", "bot_id"}:                 "SELECT * FROM bot_goals WHERE bot_id=?",
				{"bot_outbox", "bot_id"}:                "SELECT * FROM bot_outbox WHERE bot_id=?",
				{"sandbox_sessions", "chat_session_id"}: "SELECT * FROM sandbox_sessions WHERE chat_session_id=?",
			}
			snapshot := func(table, field, id string) string {
				t.Helper()
				query, ok := queries[[2]string{table, field}]
				if !ok {
					t.Fatalf("unsupported snapshot query: %s.%s", table, field)
				}
				rows, err := db.QueryContext(ctx, query, id)
				if err != nil {
					t.Fatal(err)
				}
				defer rows.Close()
				columns, _ := rows.Columns()
				var result []string
				for rows.Next() {
					values := make([]any, len(columns))
					dest := make([]any, len(columns))
					for i := range values {
						dest[i] = &values[i]
					}
					if err := rows.Scan(dest...); err != nil {
						t.Fatal(err)
					}
					result = append(result, fmt.Sprint(values))
				}
				if err := rows.Err(); err != nil {
					t.Fatal(err)
				}
				return strings.Join(result, "\n")
			}
			before := []string{snapshot("bots", "id", other.ID.String()), snapshot("sessions", "bot_id", other.ID.String()), snapshot("memories", "session_id", other.HomeSessionID.String()), snapshot("session_events", "session_id", other.HomeSessionID.String())}
			credentialBefore := snapshot("credentials", "id", credentialID)
			deletion := bots.(store.BotPermanentDeleteStore)
			deleted, err := deletion.BeginPermanentDelete(ctx, store.BotPermanentDeleteRequest{BotID: target.ID, ActorID: target.OwnerID, ExpectedRevision: target.Revision, Confirm: true})
			if err != nil {
				t.Fatal(err)
			}
			// Explicit foreign attribution must fail closed and leave every child.
			mustExec("UPDATE sessions SET bot_id=? WHERE id=?", other.ID.String(), grandchild)
			if err := deletion.PurgeDeleted(ctx, target.ID, deleted.Revision); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("foreign descendant: %v", err)
			}
			mustExec("UPDATE sessions SET bot_id=NULL WHERE id=?", grandchild)
			// Fail after children have been deleted: transaction must restore all of them.
			mustExec("CREATE TRIGGER fail_purge BEFORE DELETE ON bots BEGIN SELECT RAISE(ABORT,'injected purge failure'); END")
			if err := deletion.PurgeDeleted(ctx, target.ID, deleted.Revision); err == nil {
				t.Fatal("injected failure succeeded")
			}
			if snapshot("memories", "session_id", target.HomeSessionID.String()) == "" || snapshot("session_events", "session_id", target.HomeSessionID.String()) == "" {
				t.Fatal("failed purge lost children")
			}
			for _, sid := range []string{child, grandchild} {
				if snapshot("sessions", "id", sid) == "" || snapshot("cache_diagnostics", "session_id", sid) == "" || snapshot("memories", "session_id", sid) == "" {
					t.Fatal("rollback lost descendant")
				}
			}
			mustExec("DROP TRIGGER fail_purge")
			if err := deletion.PurgeDeleted(ctx, target.ID, deleted.Revision); err != nil {
				t.Fatal(err)
			}
			for _, sid := range []string{child, grandchild} {
				for _, pair := range [][2]string{{"sessions", "id"}, {"session_events", "session_id"}, {"cache_diagnostics", "session_id"}, {"memories", "session_id"}} {
					if snapshot(pair[0], pair[1], sid) != "" {
						t.Fatalf("orphaned descendant %s", pair[0])
					}
				}
			}
			if snapshot("credentials", "id", credentialID) != credentialBefore {
				t.Fatal("shared credential changed")
			}
			// Root and all direct Bot/session-owned tables are gone, never unrelated rows.
			for _, pair := range [][2]string{{"bots", "id"}, {"sessions", "bot_id"}, {"bot_versions", "bot_id"}, {"bot_runs", "bot_id"}, {"bot_approvals", "bot_id"}, {"bot_goals", "bot_id"}, {"bot_outbox", "bot_id"}, {"memories", "session_id"}, {"session_events", "session_id"}} {
				id := target.ID.String()
				if pair[1] == "session_id" {
					id = target.HomeSessionID.String()
				}
				if snapshot(pair[0], pair[1], id) != "" {
					t.Fatalf("orphaned %s", pair[0])
				}
			}
			after := []string{snapshot("bots", "id", other.ID.String()), snapshot("sessions", "bot_id", other.ID.String()), snapshot("memories", "session_id", other.HomeSessionID.String()), snapshot("session_events", "session_id", other.HomeSessionID.String())}
			if !reflect.DeepEqual(before, after) {
				t.Fatal("unrelated data changed")
			}
			if scope == store.BotScopeTeam {
				if snapshot("sandbox_sessions", "chat_session_id", target.HomeSessionID.String()) != "" || snapshot("sandbox_sessions", "chat_session_id", other.HomeSessionID.String()) == "" {
					t.Fatal("sandbox registry purge not scoped")
				}
			}
			var count int
			if err := db.QueryRowContext(ctx, "SELECT count(*) FROM memories_fts WHERE memories_fts MATCH 'needle'").Scan(&count); err != nil || count != 1 {
				t.Fatalf("stale fulltext: %d %v", count, err)
			}
			results, err := memories.Search(ctx, "needle", 10, 0)
			if err != nil || len(results) != 1 || results[0].SessionID != other.HomeSessionID.String() {
				t.Fatalf("retrieval leak: %+v %v", results, err)
			}
		})
	}
}

func TestBotPermanentUnprovisioned(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			repo, owner, _, _, _, _ := botRunFixture(t, scope)
			ownerID := uuid.MustParse(repo.auth.userID)
			for _, attempted := range []bool{false, true} {
				b, err := repo.CreateDraft(owner, store.BotCreateRequest{Name: "Draft", OwnerID: ownerID, Version: permanentTestVersion(), IdempotencyKey: uuid.NewString()})
				if err != nil {
					t.Fatal(err)
				}
				if attempted {
					err = repo.db.Transaction(owner, func(tx botDraftDB) error {
						rec, e := tx.GetBot(owner, b.ID)
						if e != nil {
							return e
						}
						return tx.(botProvisioningDB).markProvisioning(owner, *rec)
					})
					if err != nil {
						t.Fatal(err)
					}
				}
				deleted, err := repo.BeginPermanentDelete(owner, store.BotPermanentDeleteRequest{BotID: b.ID, ActorID: ownerID, ExpectedRevision: b.Revision, Confirm: true})
				if err != nil {
					t.Fatal(err)
				}
				err = repo.PurgeDeleted(owner, b.ID, deleted.Revision)
				if attempted && !errors.Is(err, store.ErrBotConflict) {
					t.Fatalf("ambiguous allocation purged: %v", err)
				}
				if !attempted && err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
