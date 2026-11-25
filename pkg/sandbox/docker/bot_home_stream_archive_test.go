package docker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestArchiveStoppedTargetBinding(t *testing.T) {
	for _, tc := range []string{"valid", "running", "paused", "foreign-label", "foreign-mount", "missing-recipe", "changed-physical"} {
		t.Run(tc, func(t *testing.T) {
			h := &sandbox.BotHomeBinding{Backend: sandbox.BackendKindDocker, Scope: sandbox.BotHomeScope{UserID: uuid.NewString(), BotID: uuid.NewString()}}
			h.Ref = botHomeVolume(h.Scope)
			id := uuid.NewString()
			state := "exited"
			label, home := id, h.Ref
			if tc == "running" || tc == "paused" {
				state = tc
			}
			if tc == "foreign-label" {
				label = "foreign"
			}
			if tc == "foreign-mount" {
				home = "foreign"
			}
			mounts := []map[string]any{
				{"Type": "volume", "Name": home, "Destination": botHomeMount, "RW": true},
				{"Type": "volume", "Name": workspaceVolume(h), "Destination": botPrivateMount, "RW": true},
			}
			raw, err := json.Marshal(map[string]any{"Id": "physical", "State": map[string]any{"Status": state, "Running": state == "running", "Paused": state == "paused"}, "Config": map[string]any{"Labels": map[string]string{"astonish.session_id": label}}})
			if err != nil {
				t.Fatal(err)
			}
			mountJSON, err := json.Marshal(mounts)
			if err != nil {
				t.Fatal(err)
			}
			log := filepath.Join(t.TempDir(), "docker.log")
			script := writeDockerTestScript(t, log, fmt.Sprintf(`case "$1" in
  ps) printf '%%s\n' %s ;;
  inspect) case "$3" in *Mounts*) printf '%%s\n' %s ;; *) printf '%%s\n' %s ;; esac ;;
  *) exit 23 ;;
esac`, quoteShell(containerName(id)), quoteShell(string(mountJSON)), quoteShell(string(raw))))
			db := &DockerBackend{cfg: Config{ContainerRuntimePath: script, UppersDir: t.TempDir()}}
			if err := db.BindBotHomeSession(h, id); err != nil {
				t.Fatal(err)
			}
			if tc != "missing-recipe" {
				if err := os.MkdirAll(db.upperPath(id), 0700); err != nil {
					t.Fatal(err)
				}
				if err := db.writeSessionMetadata(newDockerSessionMetadata(sandbox.SessionSpec{SessionID: id, BotHome: h, TemplateID: sandbox.BaseTemplateID, LayerChain: []string{sandbox.BaseTemplateID}, Image: "fixture:local"})); err != nil {
					t.Fatal(err)
				}
			}
			target, err := db.archiveStoppedTarget(t.Context(), h)
			if tc == "valid" || tc == "changed-physical" {
				if err != nil || target != "physical" {
					t.Fatalf("target=%q err=%v", target, err)
				}
				if tc == "changed-physical" {
					target = "previous-physical"
				}
				err = db.archiveAttachments(t.Context(), h, "", target)
				if (err == nil) != (tc == "valid") {
					t.Fatalf("attachment validation: %v", err)
				}
			} else if err == nil {
				t.Fatal("invalid stopped target accepted")
			}
		})
	}
}

func TestArchiveDetachedWithoutRecipe(t *testing.T) {
	h := &sandbox.BotHomeBinding{Backend: sandbox.BackendKindDocker, Scope: sandbox.BotHomeScope{UserID: uuid.NewString(), BotID: uuid.NewString()}}
	h.Ref = botHomeVolume(h.Scope)
	log := filepath.Join(t.TempDir(), "docker.log")
	script := writeDockerTestScript(t, log, `case "$1" in
  inspect) echo 'Error: No such object: missing-session' >&2; exit 1 ;;
  ps) exit 0 ;;
  *) exit 23 ;;
esac`)
	db := &DockerBackend{cfg: Config{ContainerRuntimePath: script, UppersDir: t.TempDir()}}
	if err := db.BindBotHomeSession(h, uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	target, err := db.archiveStoppedTarget(t.Context(), h)
	if err != nil || target != "" {
		t.Fatalf("detached storage requires compute recipe: target=%q err=%v", target, err)
	}
	if err := db.archiveAttachments(t.Context(), h, "", target); err != nil {
		t.Fatal(err)
	}
}

func TestArchiveBarrier(t *testing.T) {
	b := &sandbox.BotHomeBinding{Backend: sandbox.BackendKindDocker, Scope: sandbox.BotHomeScope{UserID: uuid.NewString(), BotID: uuid.NewString()}}
	b.Ref = botHomeVolume(b.Scope)
	fail := errors.New("denied")
	for _, barrier := range []sandbox.BotHomeArchiveBarrier{nil,
		func(context.Context, *sandbox.BotHomeBinding, func(context.Context) error) error { return nil },
		func(c context.Context, _ *sandbox.BotHomeBinding, op func(context.Context) error) error {
			_ = op(c)
			return nil
		},
		func(c context.Context, _ *sandbox.BotHomeBinding, op func(context.Context) error) error {
			_ = op(c)
			_ = op(c)
			return nil
		},
	} {
		if archiveBarrier(t.Context(), b, barrier, func(context.Context) error { return fail }) == nil {
			t.Fatal("barrier swallowed failure")
		}
	}
}

func TestBotHomeStreamArchiveDockerLive(t *testing.T) {
	if os.Getenv("ASTONISH_TEST_DOCKER") != "1" {
		t.Skip("explicit physical Docker opt-in required")
	}
	image := os.Getenv("ASTONISH_SANDBOX_IMAGE")
	if image == "" {
		t.Fatal("ASTONISH_SANDBOX_IMAGE required")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()
	if _, err := runDocker(ctx, "docker", "image", "inspect", image); err != nil {
		t.Fatal(err)
	}
	db := &DockerBackend{cfg: Config{ContainerRuntimePath: "docker", SandboxImage: image}}
	fresh := func() *sandbox.BotHomeBinding {
		b, e := db.EnsureBotHome(ctx, sandbox.BotHomeScope{UserID: uuid.NewString(), BotID: uuid.NewString()})
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(func() {
			c, stop := context.WithTimeout(context.Background(), 30*time.Second)
			defer stop()
			if e := db.DeleteBotHome(c, b); e != nil {
				t.Error(e)
			}
		})
		return b
	}
	source, target := fresh(), fresh()
	corruptTarget := fresh()
	// The fixture owns every admission to these unique never-registered homes.
	// No daemon can discover or attach them; this mutex is the test's full writer
	// and attachment barrier, not a production paused-status approximation.
	var mu sync.Mutex
	barrier := func(c context.Context, b *sandbox.BotHomeBinding, op func(context.Context) error) error {
		mu.Lock()
		defer mu.Unlock()
		if !b.Equal(source) && !b.Equal(target) && !b.Equal(corruptTarget) {
			return errors.New("fixture binding denied")
		}
		return op(c)
	}
	body := bytes.Repeat([]byte("streaming-retained-byte-"), 1<<20)
	entry, e := db.WriteBotHomeFile(ctx, source, store.BotFileWrite{Path: "reports/large.bin", SizeBytes: int64(len(body)), Body: bytes.NewReader(body), CreateOnly: true, BeforeCommit: func(op func() error) error { return op() }})
	if e != nil {
		t.Fatal(e)
	}
	ref, e := db.PublishBotArtifactSnapshot(ctx, source, "reports/large.bin", "large.bin", func(op func() error) error { return op() })
	if e != nil {
		t.Fatal(e)
	}
	archive, e := os.Create(filepath.Join(t.TempDir(), "home.stream"))
	if e != nil {
		t.Fatal(e)
	}
	defer archive.Close()
	manifest, e := db.ExportBotHomeArchive(ctx, source, archive, barrier)
	if e != nil {
		t.Fatal(e)
	}
	if manifest.SizeBytes() != 2*int64(len(body)) {
		t.Fatalf("missing ordinary or protected bytes: %+v", manifest)
	}
	if _, e = archive.Seek(0, 0); e != nil {
		t.Fatal(e)
	}
	if e = db.RestoreBotHomeArchive(ctx, target, manifest, archive, barrier); e != nil {
		t.Fatal(e)
	}
	want := fmt.Sprintf("%x", sha256.Sum256(body))
	check := func(stream *store.BotFileStream, e error) {
		t.Helper()
		if e != nil {
			t.Fatal(e)
		}
		hash := sha256.New()
		n, e := io.Copy(hash, stream)
		closeErr := stream.Close()
		if e != nil || closeErr != nil || n != int64(len(body)) || fmt.Sprintf("%x", hash.Sum(nil)) != want {
			t.Fatalf("restored bytes mismatch n=%d err=%v close=%v", n, e, closeErr)
		}
	}
	check(db.OpenBotHomeFile(ctx, target, "reports/large.bin", store.BotFileRange{}))
	// Mapping belongs to platform recovery, not physical byte extraction. Retain
	// immutable key/revision/digest but bind the verified reference to target scope.
	restored, e := contentRef(target, ref.Kind, ref.Revision, &store.BotFileEntry{Path: ref.Key, SHA256: ref.SHA256, SizeBytes: ref.SizeBytes})
	if e != nil {
		t.Fatal(e)
	}
	check(db.OpenBotContent(ctx, target, *restored, store.BotFileRange{}))
	if s, e := db.OpenBotHomeFile(ctx, target, ref.Key, store.BotFileRange{}); e == nil {
		_ = s.Close()
		t.Fatal("private bytes exposed through Home")
	}
	if _, e = archive.Seek(0, 0); e != nil {
		t.Fatal(e)
	}
	if e = db.RestoreBotHomeArchive(ctx, target, manifest, archive, barrier); e == nil {
		t.Fatal("nonempty target accepted")
	}
	if _, e = db.ExportBotHomeArchive(ctx, source, io.Discard, nil); e == nil {
		t.Fatal("missing fence accepted")
	}
	// A truncated restore fails and its partial target is not exportable.
	if _, e = archive.Seek(0, 0); e != nil {
		t.Fatal(e)
	}
	if e = db.RestoreBotHomeArchive(ctx, corruptTarget, manifest, io.LimitReader(archive, 1024), barrier); e == nil {
		t.Fatal("truncated restore accepted")
	}
	if _, e = db.ExportBotHomeArchive(ctx, corruptTarget, io.Discard, barrier); e == nil {
		t.Fatal("partial restore became exportable")
	}
	// Even stopped unknown attachments block capture; never stop/detach them.
	name := "astonish-archive-test-" + uuid.NewString()
	if _, e = runDocker(ctx, "docker", "container", "create", "--name="+name, "--pull=never", "--network=none", "--mount=type=volume,source="+source.Ref+",target=/fixture,volume-nocopy", image); e != nil {
		t.Fatal(e)
	}
	if _, e = db.ExportBotHomeArchive(ctx, source, io.Discard, barrier); e == nil {
		t.Error("attached source accepted")
	}
	if _, e = runDocker(ctx, "docker", "rm", name); e != nil {
		t.Fatal(e)
	}
	for _, b := range []*sandbox.BotHomeBinding{source, target} {
		raw, e := runDocker(ctx, "docker", "ps", "-aq", "--filter=label="+botHomeScopeLabel+"="+b.Scope.Key(), "--filter=label="+botHomeReaderLabel)
		if e != nil || strings.TrimSpace(string(raw)) != "" {
			t.Fatalf("helper leak %s %v", raw, e)
		}
	}
	t.Logf("physical Docker archive: ordinary=%d protected=%d digest=%s source=%s target=%s", entry.SizeBytes, ref.SizeBytes, want, source.Ref, target.Ref)
}
