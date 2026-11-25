package docker

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

// Reproduce the actual lock order: PutBotContent holds the volume flock before
// requesting SQL, while export enters its callback with BEGIN IMMEDIATE held.
func TestBotHomeArchivePrivateWriterContentionDockerLive(t *testing.T) {
	if os.Getenv("ASTONISH_TEST_DOCKER") != "1" {
		t.Skip("explicit physical Docker opt-in required")
	}
	image := os.Getenv("ASTONISH_SANDBOX_IMAGE")
	if image == "" {
		t.Fatal("ASTONISH_SANDBOX_IMAGE required")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	if _, err := runDocker(ctx, "docker", "image", "inspect", image); err != nil {
		t.Fatal(err)
	}
	db := &DockerBackend{cfg: Config{ContainerRuntimePath: "docker", SandboxImage: image}}
	fresh := func() *sandbox.BotHomeBinding {
		t.Helper()
		b, err := db.EnsureBotHome(ctx, sandbox.BotHomeScope{UserID: uuid.NewString(), BotID: uuid.NewString()})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			c, stop := context.WithTimeout(context.Background(), 20*time.Second)
			defer stop()
			if err := db.DeleteBotHome(c, b); err != nil {
				t.Error(err)
			}
		})
		if _, err := db.ensureWorkspaceVolume(ctx, b); err != nil {
			t.Fatal(err)
		}
		return b
	}
	b, unrelated := fresh(), fresh()
	sqldb, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "writer.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer sqldb.Close()
	exporter, err := sqldb.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer exporter.Close()
	writer, err := sqldb.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if _, err = writer.ExecContext(ctx, "PRAGMA busy_timeout=30000"); err != nil {
		t.Fatal(err)
	}
	if _, err = exporter.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	rollback := func() { _, _ = exporter.ExecContext(context.Background(), "ROLLBACK") }
	defer rollback()
	entered := make(chan struct{})
	type result struct {
		ref *store.BotContentRef
		err error
	}
	done := make(chan result, 1)
	go func() {
		ref, e := db.PutBotContent(ctx, b, store.BotContentDocument, 5, strings.NewReader("hello"), 1, func(commit func() error) error {
			close(entered)
			if _, e := writer.ExecContext(ctx, "BEGIN IMMEDIATE"); e != nil {
				return e
			}
			defer func() { _, _ = writer.ExecContext(context.Background(), "ROLLBACK") }()
			return commit()
		})
		done <- result{ref, e}
	}()
	defer func() {
		rollback()
		select {
		case <-done:
		case <-time.After(35 * time.Second):
			t.Error("writer cleanup did not finish")
		}
	}()
	select {
	case <-entered:
	case r := <-done:
		done <- r
		t.Fatalf("writer failed before commit barrier: %v", r.err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	attachments, err := runDocker(ctx, "docker", "ps", "-a", "--format={{.Names}}", "--filter=volume="+b.Ref)
	if err != nil || !strings.Contains(string(attachments), "astonish-home-files-") {
		t.Fatalf("writer helper not attached: %q %v", attachments, err)
	}
	barrier := func(c context.Context, h *sandbox.BotHomeBinding, op func(context.Context) error) error {
		if !h.Equal(b) && !h.Equal(unrelated) {
			return store.ErrBotForbidden
		}
		return op(c) // exporter already owns the real SQL writer transaction
	}
	attempt, stop := context.WithTimeout(ctx, 8*time.Second)
	var output bytes.Buffer
	manifest, archiveErr := db.ExportBotHomeArchive(attempt, b, &output, barrier)
	attemptErr := attempt.Err()
	stop()
	if !errors.Is(archiveErr, store.ErrBotBusy) || attemptErr != nil || manifest != nil || output.Len() != 0 {
		t.Errorf("contention must refuse busy before timeout or bytes: err=%v context=%v manifest=%v bytes=%d", archiveErr, attemptErr, manifest, output.Len())
	}
	// Refusal must not force-detach the existing private writer or retain its own
	// failed admission helper, and must not serialize unrelated Home volumes.
	after, err := runDocker(ctx, "docker", "ps", "-a", "--format={{.Names}}", "--filter=volume="+b.Ref)
	if err != nil || string(after) != string(attachments) {
		t.Errorf("writer attachment changed: before=%q after=%q err=%v", attachments, after, err)
	}
	if err := db.archiveDetached(ctx, unrelated, ""); err != nil {
		t.Fatal(err)
	}
	unrelatedManifest, err := db.ExportBotHomeArchive(ctx, unrelated, io.Discard, barrier)
	if err != nil {
		t.Fatalf("unrelated Home blocked: %v", err)
	}
	// Restore enters the same SQL-first order and must use nonblocking admission
	// too, even though it may provision private storage for a fresh target.
	restoreCtx, stopRestore := context.WithTimeout(ctx, 8*time.Second)
	restoreErr := db.RestoreBotHomeArchive(restoreCtx, b, unrelatedManifest, strings.NewReader(""), barrier)
	if !errors.Is(restoreErr, store.ErrBotBusy) || restoreCtx.Err() != nil {
		t.Errorf("restore contention must refuse busy: %v context=%v", restoreErr, restoreCtx.Err())
	}
	stopRestore()
	rollback()
	var written result
	select {
	case written = <-done:
		done <- written // retain cleanup acknowledgement
	case <-time.After(10 * time.Second):
		t.Fatal("writer did not resume after SQL lock release")
	}
	if written.err != nil || written.ref == nil {
		t.Fatalf("writer failed: %v", written.err)
	}
	if err := db.archiveDetached(ctx, b, ""); err != nil {
		t.Fatalf("helper leaked after refusal/writer cleanup: %v", err)
	}
	privateAttachments, err := runDocker(ctx, "docker", "ps", "-a", "--format={{.Names}}", "--filter=volume="+workspaceVolume(b))
	if err != nil || strings.TrimSpace(string(privateAttachments)) != "" {
		t.Fatalf("admission helper leaked after contention: %q %v", privateAttachments, err)
	}
	manifest, err = db.ExportBotHomeArchive(ctx, b, io.Discard, barrier)
	if err != nil || manifest == nil || manifest.SizeBytes() != 5 {
		t.Fatalf("archive retry after writer completion: manifest=%v err=%v", manifest, err)
	}
}
