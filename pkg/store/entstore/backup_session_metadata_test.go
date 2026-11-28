package entstore

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/backup"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	adksession "google.golang.org/adk/session"
)

// Rebuild checksums as well as the session JSON: rejection must come from the
// semantic SQL gate, not an accidentally broken transport archive.
func rewriteSessionMetadataArchive(t *testing.T, source string, metadata map[string]any) string {
	t.Helper()
	reader, err := backup.OpenReader(source)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	summary, err := reader.Summary()
	if err != nil {
		t.Fatal(err)
	}
	sizes := map[string]int64{}
	entities := map[string]string{}
	for _, c := range summary.Checksums {
		sizes[c.Path] = c.Size
	}
	for _, e := range summary.Manifest.Entries {
		entities[e.Path] = e.Entity
	}
	path := filepath.Join(t.TempDir(), "modified.archive")
	writer, err := backup.Create(path, backup.WriterOptions{Compression: backup.CompressionNone})
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	changed := false
	err = reader.ForEachFile(func(p string, r io.Reader) error {
		if p == backup.ManifestPath || p == backup.ChecksumsPath {
			return nil
		}
		size := sizes[p]
		if entities[p] == "sessions" {
			scanner := backup.NewRecordScanner(r, "sessions")
			var out bytes.Buffer
			rows := backup.NewRecordWriter(&out, "sessions")
			for scanner.Next() {
				record, err := scanner.Record()
				if err != nil {
					return err
				}
				row, err := backup.DecodeRecordData(record)
				if err != nil {
					return err
				}
				if row["bot_id"] != nil {
					row["metadata"] = metadata
					changed = true
				}
				if err = rows.Write(record.ID, row); err != nil {
					return err
				}
			}
			if err := scanner.Err(); err != nil {
				return err
			}
			r, size = bytes.NewReader(out.Bytes()), int64(out.Len())
		}
		_, err := writer.AddFileSized(t.Context(), p, size, r)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("no Bot session mutated")
	}
	if err = writer.CloseWithManifest(summary.Manifest); err != nil {
		t.Fatal(err)
	}
	if _, err = backup.Verify(path); err != nil {
		t.Fatalf("invalid transport fixture: %v", err)
	}
	return path
}

func TestBackupFileFirstSessionMetadataGate(t *testing.T) {
	const sentinel = "unexpected-session-body-sentinel"
	for _, kind := range []string{"personal", "team"} {
		t.Run(kind, func(t *testing.T) {
			s, _, db, backend := fileFirstExportFixture(t, kind)
			backend.home = []byte("retained Home")
			var botText, ownerText, sessionID string
			if err := db.QueryRow(`SELECT bot_id,user_id,id FROM sessions WHERE bot_id IS NOT NULL`).Scan(&botText, &ownerText, &sessionID); err != nil {
				t.Fatal(err)
			}
			stage := store.BotContentScope{OrgID: "archive", TeamID: "original", OwnerID: ownerText, BotID: uuid.MustParse(botText)}
			if kind == "personal" {
				stage.TeamID = personalSchemaName(ownerText)
			}
			initial, err := newBotContentCodec(filepath.Join(s.dataDir, "bot-content"), stage).encodeSessionEvent(t.Context(), stage.BotID, sessionID, &adksession.Event{ID: "initial-state", Actions: adksession.EventActions{StateDelta: map[string]any{"body": "disk-only-initial-state"}}})
			if err != nil {
				t.Fatal(err)
			}
			validMetadata := map[string]any{"app_name": "astonish", "bot_initial_state": initial}
			validRaw, err := json.Marshal(validMetadata)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = db.Exec(`UPDATE sessions SET metadata=?`, string(validRaw)); err != nil {
				t.Fatal(err)
			}
			// Chat metadata is intentionally open-ended, even in a scope with Bots.
			if _, err := db.Exec(`INSERT INTO sessions (id,purpose,retention,metadata,created_at,updated_at) VALUES ('ordinary-chat','chat','default',?,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`, `{"unexpected_body":"`+sentinel+`"}`); err != nil {
				t.Fatal(err)
			}
			archive := filepath.Join(t.TempDir(), "good.archive")
			opts := PlatformBackupExportOptions{FileBackend: func(context.Context) (BotBackupBackend, error) { return backend, nil }}
			if err := s.ExportPlatformBackup(t.Context(), archive, opts); err != nil {
				t.Fatal(err)
			}
			if _, err := VerifyPlatformBackup(t.Context(), archive, backup.ReaderOptions{}); err != nil {
				t.Fatal(err)
			}
			prior, err := os.ReadFile(archive)
			if err != nil {
				t.Fatal(err)
			}
			for _, mode := range []string{"literal", "nested", "escaped", "base64", "wrong-type", "oversized", "null", "foreign-user", "foreign-team"} {
				t.Run(mode, func(t *testing.T) {
					metadata := map[string]any{"app_name": "astonish", "bot_initial_state": initial}
					switch mode {
					case "literal":
						metadata["unexpected_body"] = sentinel
					case "nested":
						metadata["unexpected_body"] = map[string]any{"a": []any{map[string]any{"b": sentinel}}}
					case "escaped":
						metadata["unexpected_body"] = `{"body":"\u0075nexpected-session-body-sentinel"}`
					case "base64":
						metadata["unexpected_body"] = base64.StdEncoding.EncodeToString([]byte(sentinel))
					case "wrong-type":
						metadata["app_name"] = map[string]any{"unexpected_body": sentinel}
					case "oversized":
						metadata["app_name"] = strings.Repeat("a", 257)
					case "null":
						metadata["app_name"] = nil
					case "foreign-user":
						metadata["user_id"] = "someone-else"
					case "foreign-team":
						metadata["team_id"] = "another-team"
					}
					modified := rewriteSessionMetadataArchive(t, archive, metadata)
					if _, err := VerifyPlatformBackup(t.Context(), modified, backup.ReaderOptions{}); err == nil {
						t.Error("offline verifier accepted non-allowlisted Bot session metadata")
					}
					raw, err := json.Marshal(metadata)
					if err != nil {
						t.Fatal(err)
					}
					if _, err = db.Exec(`UPDATE sessions SET metadata=? WHERE bot_id IS NOT NULL`, string(raw)); err != nil {
						t.Fatal(err)
					}
					calls := backend.calls
					if err = s.ExportPlatformBackup(t.Context(), archive, opts); err == nil {
						t.Error("export accepted non-allowlisted Bot session metadata")
					}
					if backend.calls != calls {
						t.Error("Home export started before metadata rejection")
					}
					after, err := os.ReadFile(archive)
					if err != nil || !bytes.Equal(prior, after) {
						t.Error("rejected export replaced prior archive")
					}
				})
			}
		})
	}
}

func TestBotBackupSessionCollectorOwnership(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			repo, ctx, bot, _, _ := selectionFixture(t, scope)
			var scopes []store.BotContentScope
			runtimeScope, err := repo.db.(botHomeConfigurationDB).contentCodec().RuntimeScope(ctx, bot.ID)
			if err != nil {
				t.Fatal(err)
			}
			scopes = append(scopes, runtimeScope)
			for _, row := range configurationRecords(t, repo, ctx, bot.ID) {
				_, ref, _, err := homeRecordEnvelope(row)
				if err != nil {
					t.Fatal(err)
				}
				if ref != nil {
					scopes = append(scopes, ref.Scope)
				}
			}
			for _, mode := range []string{"valid", "unknown-metadata", "foreign-owner"} {
				t.Run(mode, func(t *testing.T) {
					owner := bot.OwnerID
					metadata := map[string]any{"app_name": "astonish"}
					if mode == "unknown-metadata" {
						metadata["unexpected_body"] = "sentinel"
					}
					if mode == "foreign-owner" {
						owner = uuid.New()
					}
					var err error
					switch db := repo.db.(type) {
					case personalBotDB:
						err = db.client.Session.UpdateOneID(bot.HomeSessionID.String()).SetUserID(owner).SetMetadata(metadata).Exec(ctx)
					case teamBotDB:
						err = db.client.Session.UpdateOneID(bot.HomeSessionID.String()).SetUserID(owner).SetMetadata(metadata).Exec(ctx)
					}
					if err != nil {
						t.Fatal(err)
					}
					inv, err := newBotBackupInventory(scopes)
					if err != nil {
						t.Fatal(err)
					}
					err = collectBotBackupRoots(ctx, repo.db, bot.ID, inv)
					if (err == nil) != (mode == "valid") {
						t.Fatalf("collector %s: %v", mode, err)
					}
				})
			}
		})
	}
}

func TestBotBackupSessionMetadataAllowlist(t *testing.T) {
	scope := botFileTestScope()
	codec := newBotContentCodec(t.TempDir(), scope)
	event := adksession.NewEvent("initial-state")
	event.ID = "initial-state"
	initial, err := codec.encodeSessionEvent(context.Background(), scope.BotID, "child", event)
	if err != nil {
		t.Fatal(err)
	}
	for _, metadata := range []map[string]any{nil, {}, {"app_name": "astonish"}, {"app_name": "astonish", "bot_initial_state": initial}} {
		inv, err := newBotBackupInventory([]store.BotContentScope{scope})
		if err != nil {
			t.Fatal(err)
		}
		if err = inv.rootsFromSession(scope.BotID, "child", metadata); err != nil {
			t.Fatal(err)
		}
	}
	// A record whose embedded runtime scope does not match the owning scope must be rejected.
	foreign := newBotContentCodec(t.TempDir(), store.BotContentScope{OrgID: scope.OrgID, TeamID: "foreign", OwnerID: scope.OwnerID, BotID: scope.BotID})
	foreignEvent := adksession.NewEvent("initial-state")
	foreignEvent.ID = "initial-state"
	badInitial, err := foreign.encodeSessionEvent(context.Background(), scope.BotID, "child", foreignEvent)
	if err != nil {
		t.Fatal(err)
	}
	inv, err := newBotBackupInventory([]store.BotContentScope{scope})
	if err != nil {
		t.Fatal(err)
	}
	if err = inv.rootsFromSession(scope.BotID, "child", map[string]any{"bot_initial_state": badInitial}); err == nil {
		t.Error("accepted foreign-scope session event")
	}
	for _, metadata := range []map[string]any{
		{"unexpected_body": "sentinel"}, {"app_name": true}, {"app_name": nil}, {"app_name": strings.Repeat("a", 257)},
		{"APP_NAME": "astonish"}, {"app_name": "body\ntext"},
		{"bot_initial_state": map[string]any{"BOT_RECORD_V2": initial["bot_record_v2"]}},
		{"bot_initial_state": nil}, {"bot_initial_state": map[string]any{"bot_record_v2": initial["bot_record_v2"], "unexpected_body": "sentinel"}},
	} {
		inv, err := newBotBackupInventory([]store.BotContentScope{scope})
		if err != nil {
			t.Fatal(err)
		}
		if err = inv.rootsFromSession(scope.BotID, "child", metadata); err == nil {
			t.Errorf("accepted %#v", metadata)
		}
	}
}
