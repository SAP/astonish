package entstore

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/SAP/astonish/pkg/backup"
	"github.com/SAP/astonish/pkg/store"
)

func botRestoreFixture(t *testing.T) (string, botFileBackupRestoreOptions, []store.BotContentRef) {
	t.Helper()
	scope := botFileTestScope()
	sourceDir := t.TempDir()
	disk, err := newBotDiskContent(sourceDir, scope)
	if err != nil {
		t.Fatal(err)
	}
	put := func(kind store.BotContentKind, raw []byte) store.BotContentRef {
		t.Helper()
		ref, e := disk.Put(t.Context(), kind, 1, int64(len(raw)), bytes.NewReader(raw), func(f func() error) error { return f() })
		if e != nil {
			t.Fatal(e)
		}
		return *ref
	}
	// An actual disk-backed binary object beyond the old buffered artifact ceiling.
	artifact := put(store.BotContentArtifact, bytes.Repeat([]byte("binary-restore-sentinel\x00"), 850000))
	if artifact.SizeBytes <= 16<<20 {
		t.Fatal("fixture too small")
	}
	a := store.BotArtifact{ContentRef: &artifact, SHA256: artifact.SHA256, SizeBytes: artifact.SizeBytes}
	marshal := func(v any) []byte {
		t.Helper()
		raw, e := json.Marshal(v)
		if e != nil {
			t.Fatal(e)
		}
		return raw
	}
	run := put(store.BotContentCheckpoint, marshal(botRunContent{Format: botRuntimeRecordFormat, Stored: botStoredRun{Run: store.BotRun{BotID: scope.BotID, Report: store.BotReport{Artifacts: []store.BotArtifact{a}}}}}))
	activity := put(store.BotContentEvidence, marshal(botActivityContent{Activity: store.BotActivity{BotID: scope.BotID, Kind: store.BotActivityReport, Payload: marshal(struct {
		Artifacts []store.BotArtifact `json:"artifacts"`
	}{[]store.BotArtifact{a}})}}))
	document := put(store.BotContentDocument, []byte("critical instructions\n"))
	roots := []botBackupObject{{Ref: run, Codec: botBackupRun}, {Ref: activity, Codec: botBackupActivity}, {Ref: document, Codec: botBackupLeaf}}
	archive := filepath.Join(t.TempDir(), "files.tar")
	err = exportBotFileContent(t.Context(), archive, botFileBackupExportOptions{
		Scopes: []store.BotContentScope{scope}, Compression: backup.CompressionNone,
		Fence: func(_ context.Context, f func() error) error { return f() },
		Collect: func(_ context.Context, inv *botBackupInventory) error {
			for _, o := range roots {
				if e := inv.add(o); e != nil {
					return e
				}
			}
			return nil
		},
		Open: func(c context.Context, ref store.BotContentRef) (*store.BotFileStream, error) {
			return disk.Open(c, ref, store.BotFileRange{})
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = disk.Close(); err != nil {
		t.Fatal(err)
	}
	// Delete only this test-owned source, after closing it. No source fallback can
	// make the restore/reopen acceptance pass.
	if err = os.RemoveAll(sourceDir); err != nil {
		t.Fatal(err)
	}
	target, err := newBotDiskContent(t.TempDir(), scope)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = target.Close() })
	opts := botFileBackupRestoreOptions{SpoolDirectory: t.TempDir(), Scopes: []store.BotContentScope{scope}, ExpectedRoots: roots, Targets: map[store.BotContentScope]*botDiskContent{scope: target}}
	return archive, opts, []store.BotContentRef{run, activity, document, artifact}
}

func TestBotFileRestoreDiskRoundtrip(t *testing.T) {
	archive, opts, refs := botRestoreFixture(t)
	mapping, err := restoreBotFileContent(t.Context(), archive, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(mapping) != len(refs) {
		t.Fatalf("mapping size %d", len(mapping))
	}
	target := opts.Targets[refs[0].Scope]
	directory := target.root.Name()
	if err = target.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := newBotDiskContent(directory, refs[0].Scope)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	inv, err := newBotBackupInventory(opts.Scopes)
	if err != nil {
		t.Fatal(err)
	}
	for _, root := range opts.ExpectedRoots {
		if err = inv.add(root); err != nil {
			t.Fatal(err)
		}
	}
	for n := 0; n < len(inv.objects); n++ {
		o := inv.objects[n]
		if mapping[o.Ref] != o.Ref {
			t.Fatal("identity changed without typed rewrite")
		}
		stream, e := reopened.Open(t.Context(), mapping[o.Ref], store.BotFileRange{})
		if e != nil {
			t.Fatal(e)
		}
		h := sha256.New()
		count := &botContentCountingReader{reader: io.TeeReader(stream, h)}
		if e = inv.children(o, count); e != nil {
			t.Fatal(e)
		}
		if _, e = io.CopyBuffer(io.Discard, count, make([]byte, 32<<10)); e != nil {
			t.Fatal(e)
		}
		if e = stream.Close(); e != nil {
			t.Fatal(e)
		}
		if count.n != o.Ref.SizeBytes || hex.EncodeToString(h.Sum(nil)) != o.Ref.SHA256 {
			t.Fatal("changed disk bytes")
		}
	}
	if len(inv.objects) != len(refs) {
		t.Fatal("nested artifact not reopened")
	}
	// Fresh-namespace requirement is no-overwrite even for identical objects.
	opts.Targets[refs[0].Scope] = reopened
	if m, e := restoreBotFileContent(t.Context(), archive, opts); e == nil || m != nil {
		t.Fatal("overwrote existing namespace")
	}
}

// Rebuild a raw test archive by streaming entries, without buffering its payloads.
func rewriteBotRestoreArchive(t *testing.T, source, mode string) string {
	t.Helper()
	target := filepath.Join(t.TempDir(), "bad.tar")
	in, err := os.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.Create(target)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	tr, tw := tar.NewReader(in), tar.NewWriter(out)
	first := true
	for {
		hdr, e := tr.Next()
		if errors.Is(e, io.EOF) {
			break
		}
		if e != nil {
			t.Fatal(e)
		}
		if first {
			first = false
			if mode == "missing" {
				continue
			}
			if mode == "corrupt" {
				if e = tw.WriteHeader(hdr); e != nil {
					t.Fatal(e)
				}
				var b [1]byte
				if _, e = io.ReadFull(tr, b[:]); e != nil {
					t.Fatal(e)
				}
				b[0] ^= 1
				if _, e = tw.Write(b[:]); e != nil {
					t.Fatal(e)
				}
				if _, e = io.CopyN(tw, tr, hdr.Size-1); e != nil {
					t.Fatal(e)
				}
				continue
			}
			if mode == "duplicate" {
				raw, e := io.ReadAll(tr)
				if e != nil {
					t.Fatal(e)
				} // first typed root only
				for range 2 {
					if e = tw.WriteHeader(hdr); e != nil {
						t.Fatal(e)
					}
					if _, e = tw.Write(raw); e != nil {
						t.Fatal(e)
					}
				}
				continue
			}
		}
		if e = tw.WriteHeader(hdr); e != nil {
			t.Fatal(e)
		}
		if _, e = io.CopyN(tw, tr, hdr.Size); e != nil {
			t.Fatal(e)
		}
	}
	if err = tw.Close(); err != nil {
		t.Fatal(err)
	}
	return target
}

func TestBotFileRestoreRejectsBeforeDestinationWrites(t *testing.T) {
	archive, original, _ := botRestoreFixture(t)
	for _, mode := range []string{"corrupt", "duplicate", "missing", "foreign", "roots", "wrong-target", "old", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			opts := original
			scope := original.Scopes[0]
			target, err := newBotDiskContent(t.TempDir(), scope)
			if err != nil {
				t.Fatal(err)
			}
			defer target.Close()
			opts.Targets = map[store.BotContentScope]*botDiskContent{scope: target}
			source := archive
			ctx := t.Context()
			switch mode {
			case "corrupt", "duplicate", "missing":
				source = rewriteBotRestoreArchive(t, archive, mode)
			case "foreign":
				opts.Scopes = []store.BotContentScope{botFileTestScope()}
			case "roots":
				opts.ExpectedRoots = original.ExpectedRoots[:1]
			case "wrong-target":
				target.scope = botFileTestScope()
			case "cancel":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "old":
				source = filepath.Join(t.TempDir(), "old.tar")
				w, e := backup.Create(source, backup.WriterOptions{Compression: backup.CompressionNone})
				if e != nil {
					t.Fatal(e)
				}
				if e = w.CloseWithManifest(backup.NewManifest("old", "platform", []backup.Scope{{Kind: "platform"}})); e != nil {
					t.Fatal(e)
				}
			}
			mapping, err := restoreBotFileContent(ctx, source, opts)
			if err == nil || mapping != nil {
				t.Fatal("accepted invalid restore")
			}
			entries, e := os.ReadDir(target.root.Name())
			if e != nil || len(entries) != 0 {
				t.Fatalf("destination changed: %v %v", entries, e)
			}
			entries, e = os.ReadDir(opts.SpoolDirectory)
			if e != nil || len(entries) != 0 {
				t.Fatalf("spool leaked: %v %v", entries, e)
			}
		})
	}
}
