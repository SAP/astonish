package docker

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// Exercises the public retained-file backend with no session registry, binding,
// launch recipe or tool layer. Every Docker resource is a disposable UUID Home.
func TestDetachedCompiledHomeFilesDockerLive(t *testing.T) {
	if os.Getenv("ASTONISH_TEST_DOCKER") != "1" {
		t.Skip("explicit Docker acceptance required")
	}
	image := os.Getenv("ASTONISH_SANDBOX_IMAGE")
	if image == "" {
		t.Fatal("test-owned current binary image required")
	}
	db := &DockerBackend{cfg: Config{ContainerRuntimePath: "docker", SandboxImage: image}}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	h, err := db.EnsureBotHome(ctx, sandbox.BotHomeScope{UserID: uuid.NewString(), BotID: uuid.NewString()})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		c, stop := context.WithTimeout(context.Background(), 20*time.Second)
		defer stop()
		if e := db.DeleteBotHome(c, h); e != nil {
			t.Error(e)
		}
	}()
	// Browsing a brand-new retained Home never provisions its private half.
	for i := 0; i < 2; i++ {
		if _, err = db.ListBotHomeFiles(ctx, h, ".", "", 10); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = runDocker(ctx, "docker", "volume", "inspect", workspaceVolume(h)); err == nil {
		t.Fatal("read allocated private storage")
	}
	guard := func(f func() error) error { return f() }
	body := []byte("detached\x00\xff\ncontent")
	if _, err = db.WriteBotHomeFile(ctx, h, store.BotFileWrite{Path: "proof.bin", SizeBytes: int64(len(body)), Body: bytes.NewReader(body), CreateOnly: true, BeforeCommit: guard}); err != nil {
		t.Fatal(err)
	}
	ref, err := db.PutBotContent(ctx, h, store.BotContentDocument, int64(len(body)), bytes.NewReader(body), 1, func(f func() error) error {
		// Reading during a held private writer lock must neither deadlock nor busy.
		if _, e := db.StatBotHomeFile(ctx, h, "proof.bin"); e != nil {
			return e
		}
		return f()
	})
	if err != nil {
		t.Fatal(err)
	}
	operation, end := sandbox.WithBotHomeFileOperation(ctx)
	defer end()
	var attached string
	for i := 0; i < 4; i++ {
		stream, e := db.OpenBotContent(operation, h, *ref, store.BotFileRange{})
		if e != nil {
			t.Fatal(e)
		}
		got, e := io.ReadAll(stream)
		closeErr := stream.Close()
		if e != nil || closeErr != nil || !bytes.Equal(got, body) {
			t.Fatalf("content %q %v %v", got, e, closeErr)
		}
		raw, e := runDocker(ctx, "docker", "ps", "-aq", "--filter=volume="+h.Ref)
		ids := strings.Fields(string(raw))
		if e != nil || len(ids) != 1 {
			t.Fatalf("attachment count %s %v", raw, e)
		}
		if i > 0 && attached != ids[0] {
			t.Fatal("one helper per hydrated object")
		}
		attached = ids[0]
	}
	if err = end(); err != nil {
		t.Fatal(err)
	}
	if err = db.archiveDetached(ctx, h, ""); err != nil {
		t.Fatal(err)
	}
	denied := errors.New("revoked")
	if _, err = db.WriteBotHomeFile(ctx, h, store.BotFileWrite{Path: "denied", SizeBytes: 1, Body: strings.NewReader("x"), CreateOnly: true, BeforeCommit: func(func() error) error { return denied }}); !errors.Is(err, denied) {
		t.Fatalf("denied commit: %v", err)
	}
	if _, err = db.StatBotHomeFile(ctx, h, "denied"); !errors.Is(err, store.ErrBotNotFound) {
		t.Fatalf("denied bytes published: %v", err)
	}
	// A trusted but never launched session identity is still a storage-only read.
	if err = db.BindBotHomeSession(h, uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	if _, err = db.StatBotHomeFile(ctx, h, "proof.bin"); err != nil {
		t.Fatal(err)
	}
	if err = db.archiveDetached(ctx, h, ""); err != nil {
		t.Fatal(err)
	}
	verifyCompiledDownloadCancellation(t, db, h, false)
	t.Log("detached compiled reads/writes, no private allocation on reads, nested authorization, four-object attachment reuse, denied commit and absent compute binding verified")
}
