package entstore

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/SAP/astonish/pkg/backup"
	"github.com/SAP/astonish/pkg/store"
)

// Checksums alone prove bytes, not closure. Build internally checksummed archives
// independently of the exporter so its inventory cannot mask validator defects.
func TestBotFileArchiveStrictClosure(t *testing.T) {
	for _, name := range []string{"valid", "missing-child", "missing-file", "orphan", "inverted", "foreign", "wrong-codec", "duplicate-object", "wrong-roots", "malformed-json", "unknown-field", "trailing-json", "wrong-format", "wrong-owner", "wrong-size", "wrong-digest"} {
		t.Run(name, func(t *testing.T) {
			scope := botFileTestScope()
			childRaw := []byte("exact artifact bytes\x00\xff")
			child := botBackupObject{Ref: botFileTestRef(scope, store.BotContentArtifact, childRaw, true), Codec: botBackupLeaf}
			if name == "foreign" {
				child.Ref.Scope = botFileTestScope()
			}
			body := botRunContent{Format: botRuntimeRecordFormat, Stored: botStoredRun{Run: store.BotRun{BotID: scope.BotID, Report: store.BotReport{Artifacts: []store.BotArtifact{{ContentRef: &child.Ref, SHA256: child.Ref.SHA256, SizeBytes: child.Ref.SizeBytes}}}}}}
			if name == "wrong-format" {
				body.Format = botDiskRecordFormat
			}
			if name == "wrong-owner" {
				body.Stored.Run.BotID = botFileTestScope().BotID
			}
			rootRaw, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			switch name {
			case "malformed-json":
				rootRaw = []byte("{")
			case "unknown-field":
				rootRaw = append([]byte(`{"unrecognized_ref":{},`), rootRaw[1:]...)
			case "trailing-json":
				rootRaw = append(rootRaw, []byte(` {}`)...)
			}
			root := botBackupObject{Ref: botFileTestRef(scope, store.BotContentCheckpoint, rootRaw, false), Codec: botBackupRun}
			roots := []botBackupObject{root}
			index := botBackupFileIndex{Format: 1, Roots: roots, Objects: []botBackupObject{root, child}}
			objects := []botBackupObject{root, child}
			bodies := map[string][]byte{root.Ref.Key: rootRaw, child.Ref.Key: childRaw}
			switch name {
			case "missing-child":
				index.Objects = index.Objects[:1]
				objects = objects[:1]
			case "missing-file":
				objects = objects[:1]
			case "orphan":
				orphan := botBackupObject{Ref: botFileTestRef(scope, store.BotContentArtifact, childRaw, true), Codec: botBackupLeaf}
				index.Objects = append(index.Objects, orphan)
				objects = append(objects, orphan)
				bodies[orphan.Ref.Key] = childRaw
			case "inverted":
				objects[0], objects[1] = objects[1], objects[0]
			case "wrong-codec":
				index.Objects[0].Codec = botBackupLeaf
			case "duplicate-object":
				index.Objects = append(index.Objects, child)
			case "wrong-roots":
				index.Roots = []botBackupObject{child}
			case "wrong-size":
				index.Objects[1].Ref.SizeBytes++
			case "wrong-digest":
				index.Objects[1].Ref.SHA256 = root.Ref.SHA256
			}
			path := filepath.Join(t.TempDir(), "adversarial.tar")
			w, err := backup.Create(path, backup.WriterOptions{Compression: backup.CompressionNone})
			if err != nil {
				t.Fatal(err)
			}
			defer w.Close()
			manifest := backup.NewManifest("file-content", botBackupFilesMode, []backup.Scope{{Kind: "platform"}})
			for _, object := range objects {
				p := botBackupObjectPath(object)
				if _, err := w.AddFileSized(t.Context(), p, int64(len(bodies[object.Ref.Key])), bytes.NewReader(bodies[object.Ref.Key])); err != nil {
					t.Fatal(err)
				}
				manifest.Entries = append(manifest.Entries, backup.Entry{Path: p, Kind: "file", Entity: "bot_content_object", Scope: backup.Scope{Kind: "platform"}})
			}
			raw, err := json.Marshal(index)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = w.AddFileSized(t.Context(), botBackupIndexPath, int64(len(raw)), bytes.NewReader(raw)); err != nil {
				t.Fatal(err)
			}
			manifest.Entries = append(manifest.Entries, backup.Entry{Path: botBackupIndexPath, Kind: "file", Entity: "bot_content_index", Scope: backup.Scope{Kind: "platform"}})
			if err = w.CloseWithManifest(manifest); err != nil {
				t.Fatal(err)
			}
			if _, err = backup.Verify(path); err != nil {
				t.Fatalf("fixture must have valid transport checksums: %v", err)
			}
			err = validateBotFileContentArchive(t.Context(), path, backup.ReaderOptions{}, []store.BotContentScope{scope}, roots)
			if name == "valid" && err != nil {
				t.Fatal(err)
			}
			if name != "valid" && err == nil {
				t.Fatal("accepted invalid reference closure")
			}
			// Every adversarial archive must also be rejected by the installer
			// before any destination writes. The valid retained-child fixture is
			// intentionally unsupported by the exact staged-identity installer.
			target, err := newBotDiskContent(t.TempDir(), scope)
			if err != nil {
				t.Fatal(err)
			}
			defer target.Close()
			mapping, err := restoreBotFileContent(t.Context(), path, botFileBackupRestoreOptions{
				SpoolDirectory: t.TempDir(), Scopes: []store.BotContentScope{scope}, ExpectedRoots: roots,
				Targets: map[store.BotContentScope]*botDiskContent{scope: target},
			})
			if err == nil || mapping != nil {
				t.Fatal("accepted unsupported or malformed closure")
			}
			entries, err := os.ReadDir(target.root.Name())
			if err != nil || len(entries) != 0 {
				t.Fatal("restore changed destination before closure validation", err)
			}
		})
	}
}
