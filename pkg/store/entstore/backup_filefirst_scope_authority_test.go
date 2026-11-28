package entstore

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestBackupFileFirstRestoreRejectsScopedBotIDCollisionBeforeProvision(t *testing.T) {
	src, sourcePath, sourceDB, backend := fileFirstExportFixture(t, "team")
	org, err := src.ForOrg("archive")
	if err != nil {
		t.Fatal(err)
	}
	team := &store.Team{ID: uuid.NewString(), Name: "Second", Slug: "second", SchemaName: "second"}
	if err = org.Teams().CreateTeam(t.Context(), team); err != nil {
		t.Fatal(err)
	}
	// VACUUM produces a consistent, standalone copy including WAL-backed rows.
	secondPath := filepath.Join(filepath.Dir(sourcePath), "second.db")
	if _, err = sourceDB.Exec("VACUUM INTO ?", secondPath); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", secondPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	owner := uuid.New()
	if err = src.Users().Create(t.Context(), &store.User{ID: owner.String(), Email: owner.String() + "@example.test", DisplayName: "Second", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	var idText, raw string
	if err = db.QueryRow("SELECT id,data FROM bots").Scan(&idText, &raw); err != nil {
		t.Fatal(err)
	}
	id := uuid.MustParse(idText)
	var metadata map[string]any
	if err = json.Unmarshal([]byte(raw), &metadata); err != nil {
		t.Fatal(err)
	}
	var selection botContentHomeSelection
	if err = botBackupDecodeValue(metadata[botContentHomeSelectionKey], &selection); err != nil {
		t.Fatal(err)
	}
	selection.Binding.Scope.TeamID = "second"
	selection.Binding.Scope.UserID = owner.String()
	selection.Binding.Ref = "second-home"
	selection.Home.StorageKey = selection.Binding.Ref
	selection.Home.ID = uuid.New()
	metadata[botContentHomeSelectionKey] = selection
	data, _ := json.Marshal(metadata)
	home, _ := json.Marshal(selection.Home)
	if _, err = db.Exec("UPDATE bots SET owner_id=?,data=?,home_binding=?", owner.String(), string(data), string(home)); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("UPDATE sessions SET user_id=?", owner.String()); err != nil {
		t.Fatal(err)
	}
	scope := store.BotContentScope{OrgID: "archive", TeamID: "second", OwnerID: owner.String(), BotID: id}
	version := store.DefaultBotVersion()
	version.BotID, version.Version, version.Charter, version.CreatedBy = id, 1, "second-owner-distinct-body", owner
	record, err := newBotContentCodec(filepath.Join(src.dataDir, "bot-content"), scope).encodeVersion(t.Context(), version)
	if err != nil {
		t.Fatal(err)
	}
	definition, _ := json.Marshal(record)
	if _, err = db.Exec("UPDATE bot_versions SET definition=?,created_by=?", string(definition), owner.String()); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "backup")
	if err = src.ExportPlatformBackup(t.Context(), archive, PlatformBackupExportOptions{FileBackend: func(context.Context) (BotBackupBackend, error) { return backend, nil }}); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	_, target, err := NewPlatformServices(t.Context(), Config{DSN: filepath.Join(dir, "platform.db"), DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	called := false
	_, err = target.RestorePlatformBackup(t.Context(), archive, PlatformRestoreOptions{FileBackend: func(context.Context) (BotRestoreBackend, error) {
		called = true
		return &fileFirstRestoreTestBackend{fileFirstTestBackend: backend}, nil
	}})
	if err == nil || !strings.Contains(err.Error(), "duplicate Bot identity") {
		t.Fatalf("expected explicit scoped identity rejection, got %v", err)
	}
	if called {
		t.Fatal("backend touched before scoped collision rejection")
	}
	entries, err := os.ReadDir(filepath.Join(dir, "bot-content"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("target content changed: %v (%d entries)", err, len(entries))
	}
}

func TestBackupFileFirstRestoreAuthorityWithoutMapping(t *testing.T) {
	for _, mapped := range []bool{false, true} {
		name := "unmapped"
		if mapped {
			name = "mapped"
		}
		t.Run(name, func(t *testing.T) {
			scope := botFileTestScope()
			root := t.TempDir()
			owner := uuid.New()
			v := botStoredRun{Run: store.BotRun{ID: uuid.New(), BotID: scope.BotID, Version: 1, Status: store.RunPartial, Lease: store.BotLease{WorkerID: "source-worker", ExpiresAt: time.Now().Add(time.Hour)}, MandateRef: &store.BotMandateRef{}, CallerCeiling: &execution.BotCallerCeiling{}, Checkpoint: json.RawMessage(`{ "opaque": "org/team", "arguments": { "owner": "source" } }`)}, ExternalAdmissionValidated: true, SkillAdmissionOwner: &owner, ProgressOwnerContinuation: &botProgressOwnerContinuation{BotRevision: 3}, ProgressQuota: &mandateRunUsage{Tools: 2}}
			row, err := newBotContentCodec(root, scope).encodeRun(t.Context(), v)
			if err != nil {
				t.Fatal(err)
			}
			var original botRunRecord
			if err = decodeBotRecord(row, &original); err != nil {
				t.Fatal(err)
			}
			// Exercise the same restoration mapping with an identity or real mapping.
			next := scope
			if mapped {
				next.OrgID = "mapped"
			}
			state := &fileFirstRestoreState{mapping: newFileFirstMapping()}
			state.mapping.scopes[scope] = next
			targetRoot := t.TempDir()
			sqlRow := map[string]any{"data": row}
			if err = mapFileFirstRow("bot_runs", sqlRow, state); err != nil {
				t.Fatal(err)
			}
			data, err := backupDocument(sqlRow, "data")
			if err != nil {
				t.Fatal(err)
			}
			var restored botRunRecord
			if err = decodeBotRecord(data, &restored); err != nil {
				t.Fatal(err)
			}
			if restored.SHA256 == original.SHA256 || restored.LogicalDigest == original.LogicalDigest {
				t.Fatal("authority changes did not rehash typed parent and logical envelope")
			}
			var body botRunContent
			if err = json.Unmarshal(restored.Inline, &body); err != nil {
				t.Fatal(err)
			}
			if body.Stored.Run.Lease.WorkerID != "" || !body.Stored.Run.Lease.ExpiresAt.IsZero() {
				t.Fatal("embedded content retained lease authority")
			}
			got, err := newBotContentCodec(targetRoot, next).decodeRun(t.Context(), scope.BotID, data)
			if err != nil {
				t.Fatal(err)
			}
			if got.Run.MandateRef != nil || got.Run.CallerCeiling != nil || got.ExternalAdmissionValidated || got.SkillAdmissionOwner != nil || got.ProgressOwnerContinuation != nil || got.ProgressQuota != nil || got.Run.Lease.WorkerID != "" || !got.Run.Lease.ExpiresAt.IsZero() {
				t.Fatal("restored live authority")
			}
			if !bytes.Equal(got.Run.Checkpoint, v.Run.Checkpoint) {
				t.Fatal("opaque evidence rewritten")
			}
		})
	}
}
