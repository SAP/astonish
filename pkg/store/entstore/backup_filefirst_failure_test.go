package entstore

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestBackupFileFirstFailureReleasesFence(t *testing.T) {
	for _, mode := range []string{"cancel", "unknown-root", "cleanup", "lease", "missing-stage"} {
		t.Run(mode, func(t *testing.T) {
			s, _, db, backend := fileFirstExportFixture(t, "personal")
			archive := filepath.Join(t.TempDir(), "backup.archive")
			prior := []byte("last-good-backup")
			if err := os.WriteFile(archive, prior, 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch mode {
			case "cancel":
				backend.check = cancel
			case "unknown-root":
				if _, err := db.Exec(`UPDATE bots SET data=json_set(data,'$.unknown_pending_operation','pending')`); err != nil {
					t.Fatal(err)
				}
			case "cleanup":
				if _, err := db.Exec(`UPDATE bots SET data=json_set(data,'$.home_cleanup_claim','claimed')`); err != nil {
					t.Fatal(err)
				}
			case "lease":
				if _, err := db.Exec(`UPDATE bots SET lease_expires_at=CURRENT_TIMESTAMP`); err != nil {
					t.Fatal(err)
				}
			case "missing-stage":
				if err := os.RemoveAll(filepath.Join(s.dataDir, "bot-content")); err != nil {
					t.Fatal(err)
				}
			}
			err := s.ExportPlatformBackup(ctx, archive, PlatformBackupExportOptions{FileBackend: func(context.Context) (BotBackupBackend, error) { return backend, nil }})
			if err == nil {
				t.Fatal("unsafe export succeeded")
			}
			if _, err := db.Exec(`UPDATE bots SET revision=revision+1`); err != nil {
				t.Fatalf("writer fence leaked: %v", err)
			}
			after, err := os.ReadFile(archive)
			if err != nil || !bytes.Equal(prior, after) {
				t.Fatalf("prior archive changed: %v", err)
			}
		})
	}
}

func TestBackupFileFirstIndependentTemplate(t *testing.T) {
	s, _, db, backend := fileFirstExportFixture(t, "team")
	id := uuid.New()
	def := store.BotTemplateDefinition{SchemaVersion: store.BotSchemaVersion, Name: "Independent", Charter: "independent-template-file-sentinel"}
	data, err := newBotTemplateContent(filepath.Join(s.dataDir, "bot-content"), "archive", "original").encode(t.Context(), id, store.BotContentTemplate, 1, def)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO bot_templates (id,name,revision,definition,published_by,published_at) VALUES (?,?,1,?,?,CURRENT_TIMESTAMP)`, id.String(), def.Name, string(raw), uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	detail, err := json.Marshal(map[string]any{"revision": "1", "definition": data})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO team_audit_log (timestamp,user_id,action,resource,detail) VALUES (CURRENT_TIMESTAMP,?,'bot_template.publish',?,?)`, uuid.NewString(), id.String(), string(detail)); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "backup.archive")
	opts := PlatformBackupExportOptions{FileBackend: func(context.Context) (BotBackupBackend, error) { return backend, nil }}
	if err = s.ExportPlatformBackup(t.Context(), archive, opts); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE bot_templates SET name='mismatched-name'`); err != nil {
		t.Fatal(err)
	}
	if err = s.ExportPlatformBackup(t.Context(), archive, opts); err == nil {
		t.Fatal("template logical identity was not validated")
	}
}

func TestBackupFileFirstRootMetadata(t *testing.T) {
	for _, data := range []map[string]any{
		{"charter": "inline"}, {"operation_cleanup:unknown": "pending"},
		{"permanent_delete": true}, {"read_cursors": map[string]string{"invalid": "1"}},
		{"create_hash": "body"}, {"previous_status": "unknown"},
	} {
		if err := validateFileFirstRootMetadata(data); err == nil {
			t.Fatalf("accepted %#v", data)
		}
	}
	if err := validateFileFirstRootMetadata(map[string]any{"previous_status": string(store.BotActive), "permanent_delete": false}); err != nil {
		t.Fatal(err)
	}
}
