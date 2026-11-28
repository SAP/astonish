package docker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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
)

type archiveObservedWriter struct {
	io.Writer
	beforeWrite func() error
}

func (w *archiveObservedWriter) Write(p []byte) (int, error) {
	if w.beforeWrite != nil {
		observe := w.beforeWrite
		w.beforeWrite = nil
		if err := observe(); err != nil {
			return 0, err
		}
	}
	return w.Writer.Write(p)
}

func TestCompiledHomeFilesCommand(t *testing.T) {
	db := &DockerBackend{cfg: Config{ContainerRuntimePath: "docker"}}
	cmd := db.botCompiledFilesCommand(context.Background(), "physical-id")
	args := strings.Join(cmd.Args, " ")
	if !strings.Contains(args, "exec --interactive physical-id") || !strings.Contains(args, "/usr/local/bin/astonish-host node --internal-home-files") || strings.Contains(args, "python") || strings.Contains(args, "chroot") {
		t.Fatal(args)
	}
}

// TestCompiledHomeFilesNoPythonDocker exercises the actual node dispatch and
// transport with the unmodified base tool layer, not a Python-enabled template.
func TestCompiledHomeFilesNoPythonDocker(t *testing.T) {
	if os.Getenv("ASTONISH_TEST_DOCKER") != "1" {
		t.Skip("explicit Docker acceptance required")
	}
	image, layers := os.Getenv("ASTONISH_SANDBOX_IMAGE"), os.Getenv("ASTONISH_BOT_HOME_TEST_LAYERS")
	if image == "" || !filepath.IsAbs(layers) {
		t.Fatal("test-owned image and absolute layer path required")
	}
	root, e := os.MkdirTemp(filepath.Dir(layers), "compiled-storage-acceptance-")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if e := os.RemoveAll(root); e != nil {
			t.Error(e)
		}
	})
	st, e := sandbox.NewLocalSessionStore(filepath.Join(root, "registry"))
	if e != nil {
		t.Fatal(e)
	}
	db, e := New(Config{Sessions: sandbox.NewSessionRegistryFromStore(st), SandboxImage: image, LayersDir: layers, UppersDir: filepath.Join(root, "uppers"), OverlayMode: "fuse"})
	if e != nil {
		t.Fatal(e)
	}
	ctx := t.Context()
	id := uuid.NewString()
	h, e := db.EnsureBotHome(ctx, sandbox.BotHomeScope{UserID: uuid.NewString(), BotID: uuid.NewString()})
	if e != nil {
		t.Fatal(e)
	}
	defer func() {
		c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if e := db.DestroySession(c, id); e != nil {
			t.Error(e)
		}
		if e := db.DeleteBotHome(c, h); e != nil {
			t.Error(e)
		}
	}()
	if e = db.BindBotHomeSession(h, id); e != nil {
		t.Fatal(e)
	}
	if _, e = db.CreateSession(ctx, sandbox.SessionSpec{SessionID: id, Type: sandbox.SessionTypeChat, UserID: h.Scope.UserID, BotHome: h.Clone(), TemplateID: sandbox.BaseTemplateID, LayerChain: []string{sandbox.BaseTemplateID}}); e != nil {
		t.Fatal(e)
	}
	if e = db.WaitForSessionReady(ctx, id); e != nil {
		t.Fatal(e)
	}
	if _, e = runDocker(ctx, db.cfg.ContainerRuntimePath, "exec", containerName(id), "/bin/sh", "-ec", "test ! -x /sandbox/rootfs/usr/bin/python3"); e != nil {
		t.Fatalf("base layer unexpectedly contains Python: %v", e)
	}
	physical, e := runDocker(ctx, db.cfg.ContainerRuntimePath, "inspect", "--format={{.Id}}", containerName(id))
	if e != nil {
		t.Fatal(e)
	}
	checkBytes := func(stream *store.BotFileStream, err error, want []byte) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		got, readErr := io.ReadAll(stream)
		closeErr := stream.Close()
		sum := sha256.Sum256(want)
		if readErr != nil || closeErr != nil || !bytes.Equal(got, want) || stream.Entry.SizeBytes != int64(len(want)) || stream.Entry.SHA256 != hex.EncodeToString(sum[:]) {
			t.Fatalf("retained bytes=%q want=%q entry=%+v read=%v close=%v", got, want, stream.Entry, readErr, closeErr)
		}
	}
	guard := func(f func() error) error { return f() }
	body := []byte("retained\x00\xff\nbytes")
	replacement := bytes.Repeat([]byte("replacement\x00\xff\r\n"), 11) // 165 exact bytes, including non-UTF-8.
	entry, e := db.WriteBotHomeFile(ctx, h, store.BotFileWrite{Path: "proof.bin", SizeBytes: int64(len(body)), Body: bytes.NewReader(body), CreateOnly: true, BeforeCommit: guard})
	if e != nil {
		t.Fatal(e)
	}
	// Nested read in a write's authorization callback must not share a global lock.
	nested := func(f func() error) error {
		if _, e := db.StatBotHomeFile(ctx, h, "proof.bin"); e != nil {
			return e
		}
		return f()
	}
	ref, e := db.PutBotContent(ctx, h, store.BotContentArtifact, int64(len(body)), bytes.NewReader(body), 1, nested)
	if e != nil {
		t.Fatal(e)
	}
	s, e := db.OpenBotContent(ctx, h, *ref, store.BotFileRange{})
	if e != nil {
		t.Fatal(e)
	}
	got, e := io.ReadAll(s)
	closeErr := s.Close()
	if e != nil || closeErr != nil || !bytes.Equal(got, body) {
		t.Fatalf("private roundtrip %q %v %v", got, e, closeErr)
	}
	snap, e := db.PublishBotArtifactSnapshot(ctx, h, "proof.bin", "proof.bin", guard)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = db.WriteBotHomeFile(ctx, h, store.BotFileWrite{Path: "proof.bin", SizeBytes: int64(len(replacement)), Body: bytes.NewReader(replacement), ExpectedSHA256: entry.SHA256, BeforeCommit: guard}); e != nil {
		t.Fatal(e)
	}
	s, e = db.OpenBotContent(ctx, h, *snap, store.BotFileRange{Offset: 1, Length: 3})
	if e != nil {
		t.Fatal(e)
	}
	got, e = io.ReadAll(s)
	closeErr = s.Close()
	if e != nil || closeErr != nil || !bytes.Equal(got, body[1:4]) {
		t.Fatalf("snapshot range %q %v %v", got, e, closeErr)
	}
	targets, e := runDocker(ctx, db.cfg.ContainerRuntimePath, "ps", "-aq", "--filter=volume="+h.Ref)
	if e != nil || len(strings.Fields(string(targets))) != 1 {
		t.Fatalf("routine helper appeared: %s %v", targets, e)
	}
	afterWarm, e := runDocker(ctx, db.cfg.ContainerRuntimePath, "inspect", "--format={{.Id}}", containerName(id))
	if e != nil || !bytes.Equal(physical, afterWarm) {
		t.Fatalf("warm physical session changed: before=%s after=%s err=%v", physical, afterWarm, e)
	}
	t.Logf("warm session=%s physical=%s home=%s: no Python; compiled CAS, nested read, binary content and ranged snapshot", id, strings.TrimSpace(string(physical)), h.Ref)
	verifyCompiledDownloadCancellation(t, db, h, true)
	archiveBarrier := func(c context.Context, exact *sandbox.BotHomeBinding, op func(context.Context) error) error {
		if !exact.Equal(h) {
			return store.ErrBotForbidden
		}
		return op(c)
	}
	if _, err := db.ExportBotHomeArchive(ctx, h, io.Discard, archiveBarrier); err == nil {
		t.Fatal("archive accepted running session")
	}

	// Evict only this fixture's disposable compute, keeping its launch recipe,
	// registry and both retained volumes. No production session is discoverable.
	if e = db.StopSession(ctx, id); e != nil {
		t.Fatal(e)
	}
	// Keep the actual trusted binding for both stopped and removed compute.
	if _, e = db.ListBotHomeFiles(ctx, h, ".", "", 10); e != nil {
		t.Fatalf("bound stopped browse: %v", e)
	}
	stream, err := db.OpenBotHomeFile(ctx, h, "proof.bin", store.BotFileRange{})
	checkBytes(stream, err, replacement)
	stream, err = db.OpenBotContent(ctx, h, *snap, store.BotFileRange{})
	checkBytes(stream, err, body)
	stopped, e := runDocker(ctx, db.cfg.ContainerRuntimePath, "inspect", "--format={{.Id}} {{.State.Running}}", containerName(id))
	if e != nil || strings.TrimSpace(string(stopped)) != strings.TrimSpace(string(physical))+" false" {
		t.Fatalf("offline read resumed or replaced stopped compute: %s %v", stopped, e)
	}
	// A trusted stopped session remains attached during cold export.
	var stoppedArchive bytes.Buffer
	startupBlocked := false
	capture := &archiveObservedWriter{Writer: &stoppedArchive, beforeWrite: func() error {
		attempt, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		err := db.StartSession(attempt, id)
		if err == nil || attempt.Err() == nil {
			return errors.New("session startup was not held by archive admission")
		}
		startupBlocked = true
		return nil
	}}
	manifest, exportErr := db.ExportBotHomeArchive(ctx, h, capture, archiveBarrier)
	if exportErr != nil {
		t.Fatalf("bound stopped archive: %v", exportErr)
	}
	foundProof := false
	for _, archived := range manifest.Entries {
		if archived.Directory {
			continue
		}
		data := make([]byte, archived.Size)
		if _, err := io.ReadFull(&stoppedArchive, data); err != nil {
			t.Fatal(err)
		}
		if archived.Root == sandbox.BotHomeArchiveHome && archived.Path == "proof.bin" {
			foundProof = bytes.Equal(data, replacement)
		}
	}
	if !foundProof || stoppedArchive.Len() != 0 || !startupBlocked {
		t.Fatal("stopped archive lost exact Home bytes or contained trailing data")
	}
	stillStopped, e := runDocker(ctx, db.cfg.ContainerRuntimePath, "inspect", "--format={{.Id}} {{.State.Running}}", containerName(id))
	if e != nil || !bytes.Equal(stopped, stillStopped) {
		t.Fatalf("archive resumed or replaced stopped compute: %s %v", stillStopped, e)
	}
	if _, e = runDocker(ctx, db.cfg.ContainerRuntimePath, "rm", containerName(id)); e != nil {
		t.Fatal(e)
	}
	verifyDetachedCompiledArchive(t, db, h, id, replacement)
	offline := db
	page, e := offline.ListBotHomeFiles(ctx, h, ".", "", 10)
	if e != nil || page == nil || len(page.Entries) != 1 || page.Entries[0].Path != "proof.bin" {
		t.Fatalf("offline browse: page=%+v err=%v", page, e)
	}
	stream, err = offline.OpenBotHomeFile(ctx, h, "proof.bin", store.BotFileRange{})
	checkBytes(stream, err, replacement)
	stream, err = offline.OpenBotContent(ctx, h, *ref, store.BotFileRange{})
	checkBytes(stream, err, body)
	for _, volume := range []string{h.Ref, workspaceVolume(h)} {
		attached, err := runDocker(ctx, db.cfg.ContainerRuntimePath, "ps", "-aq", "--filter=volume="+volume)
		if err != nil || strings.TrimSpace(string(attached)) != "" {
			t.Fatalf("offline access started compute or leaked attachment: %s %v", attached, err)
		}
	}
	t.Log("offline browse and ordinary/private binary download succeeded with no session compute or leaked attachment")
	if e = db.PrepareBotHomeSession(ctx, h, id); e != nil {
		t.Fatal(e)
	}
	recreated, e := runDocker(ctx, db.cfg.ContainerRuntimePath, "inspect", "--format={{.Id}}", containerName(id))
	if e != nil || bytes.Equal(physical, recreated) {
		t.Fatalf("compute was not recreated: before=%s after=%s err=%v", physical, recreated, e)
	}
	stream, err = db.OpenBotHomeFile(ctx, h, "proof.bin", store.BotFileRange{})
	checkBytes(stream, err, replacement)
	stream, err = db.OpenBotContent(ctx, h, *snap, store.BotFileRange{})
	checkBytes(stream, err, body)
	t.Logf("recreated original session=%s physical=%s; retained ordinary and immutable snapshot bytes identical", id, strings.TrimSpace(string(recreated)))
	// Stop the pinned target after staging, at the final authorization barrier.
	// A failed commit must not dispatch again through the offline route.
	commits := 0
	_, e = db.WriteBotHomeFile(ctx, h, store.BotFileWrite{Path: "stale-write", SizeBytes: 1, Body: strings.NewReader("x"), CreateOnly: true, BeforeCommit: func(commit func() error) error {
		commits++
		if err := db.StopSession(ctx, id); err != nil {
			return err
		}
		return commit()
	}})
	if e == nil || commits != 1 {
		t.Fatalf("stopped physical target mutation was retried: commits=%d err=%v", commits, e)
	}
	if _, e = db.StatBotHomeFile(ctx, h, "stale-write"); !errors.Is(e, store.ErrBotNotFound) {
		t.Fatalf("stale physical target published bytes: %v", e)
	}
	t.Log("stopped physical commit target failed terminally with one commit attempt and no published destination; abrupt death may leave uncommitted staging files")
}
