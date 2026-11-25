package docker

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBotFileSessionBindingIsLazyAndExact(t *testing.T) {
	db := &DockerBackend{cfg: Config{ContainerRuntimePath: "/must-not-execute", UppersDir: t.TempDir()}}
	h := &sandbox.BotHomeBinding{Scope: sandbox.BotHomeScope{UserID: uuid.NewString(), BotID: uuid.NewString()}, Backend: sandbox.BackendKindDocker}
	h.Ref = botHomeVolume(h.Scope)
	if err := db.BindBotHomeSession(h, "session-one"); err != nil {
		t.Fatal(err)
	}
	if err := db.BindBotHomeSession(h, "session-one"); err != nil {
		t.Fatal(err)
	}
	if err := db.BindBotHomeSession(h, "session-two"); err == nil {
		t.Fatal("binding changed")
	}
	if _, err := db.botFileSession(context.Background(), h); err == nil || !strings.Contains(err.Error(), "authorized launch recipe") {
		t.Fatal(err)
	}
	otherBackend := &DockerBackend{}
	if err := otherBackend.BindBotHomeSession(h, "session-two"); err != nil {
		t.Fatal("binding leaked across backends", err)
	}
	other := h.Clone()
	other.Scope.UserID = uuid.NewString()
	other.Ref = botHomeVolume(other.Scope)
	if _, err := db.botFileSession(context.Background(), other); err == nil || !strings.Contains(err.Error(), "trusted session binding") {
		t.Fatal(err)
	}
	args := strings.Join(botSessionPythonArgs("session-one", "print('ok')"), " ")
	if strings.Contains(args, "container create") || !strings.HasPrefix(args, "exec --interactive astonish-session-") || !strings.Contains(args, "chroot /sandbox/rootfs /usr/bin/python3") {
		t.Fatal(args)
	}
}

func TestBotSessionFilesDockerLive(t *testing.T) {
	if os.Getenv("ASTONISH_TEST_DOCKER") != "1" {
		t.Skip("explicit Docker acceptance required")
	}
	image, layers := os.Getenv("ASTONISH_SANDBOX_IMAGE"), os.Getenv("ASTONISH_BOT_HOME_TEST_LAYERS")
	if image == "" || !filepath.IsAbs(layers) {
		t.Fatal("explicit image and absolute ASTONISH_BOT_HOME_TEST_LAYERS required")
	}
	root, err := os.MkdirTemp(filepath.Dir(layers), "session-file-acceptance-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Error(err)
		}
	})
	st, err := sandbox.NewLocalSessionStore(filepath.Join(root, "registry"))
	if err != nil {
		t.Fatal(err)
	}
	db, err := New(Config{Sessions: sandbox.NewSessionRegistryFromStore(st), SandboxImage: image, LayersDir: layers, UppersDir: filepath.Join(root, "uppers"), OverlayMode: "fuse"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	id := uuid.NewString()
	h, err := db.EnsureBotHome(ctx, sandbox.BotHomeScope{UserID: uuid.NewString(), BotID: uuid.NewString()})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := db.DestroySession(c, id); err != nil {
			t.Error(err)
		}
		if err := db.DeleteBotHome(c, h); err != nil {
			t.Error(err)
		}
	}()
	if err = db.BindBotHomeSession(h, id); err != nil {
		t.Fatal(err)
	}
	template := os.Getenv("ASTONISH_BOT_HOME_TEST_TEMPLATE")
	if template == "" {
		t.Fatal("ASTONISH_BOT_HOME_TEST_TEMPLATE must name the provisioned Python tool layer")
	}
	if _, err := db.CreateSession(ctx, sandbox.SessionSpec{SessionID: id, Type: sandbox.SessionTypeChat, UserID: h.Scope.UserID, BotHome: h.Clone(), TemplateID: template, LayerChain: []string{sandbox.BaseTemplateID, template}}); err != nil {
		t.Fatal(err)
	}
	guard := func(f func() error) error { return f() }
	if _, err = db.WriteBotHomeFile(ctx, h, store.BotFileWrite{Path: "proof.txt", Body: strings.NewReader("persistent"), SizeBytes: 10, CreateOnly: true, BeforeCommit: guard}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := db.PublishBotArtifactSnapshot(ctx, h, "proof.txt", "proof.txt", guard)
	if err != nil {
		t.Fatal(err)
	}
	before, err := runDocker(ctx, db.cfg.ContainerRuntimePath, "inspect", "--format", "{{.Id}}", containerName(id))
	if err != nil {
		t.Fatal(err)
	}
	// Trace the actual Docker CLI after admission: no routine helper creation.
	realRuntime := db.cfg.ContainerRuntimePath
	trace := filepath.Join(root, "docker-trace")
	spy := filepath.Join(root, "docker-spy")
	if err := os.WriteFile(spy, []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" >> "+quoteShell(trace)+"\nexec "+quoteShell(realRuntime)+" \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	db.cfg.ContainerRuntimePath = spy
	started := time.Now()
	var privateRefs []store.BotContentRef
	for i := range 5 {
		if _, err = db.StatBotHomeFile(ctx, h, "proof.txt"); err != nil {
			t.Fatal(err)
		}
		body := []byte(fmt.Sprintf("private-%d\x00\xff", i))
		ref, err := db.PutBotContent(ctx, h, store.BotContentArtifact, int64(len(body)), bytes.NewReader(body), int64(i+1), guard)
		if err != nil {
			t.Fatal(err)
		}
		privateRefs = append(privateRefs, *ref)
		stream, err := db.OpenBotContent(ctx, h, *ref, store.BotFileRange{})
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(stream)
		closeErr := stream.Close()
		if err != nil || closeErr != nil || !bytes.Equal(got, body) {
			t.Fatalf("private bytes %q: %v %v", got, err, closeErr)
		}
		if err := db.WriteBotHomeWorkspace(ctx, h, sandbox.BotHomeWorkspaceFile{Path: "tasks/proof/state/state.json", Data: body}); err != nil {
			t.Fatal(err)
		}
		got, err = db.ReadBotHomeWorkspace(ctx, h, "tasks/proof/state/state.json", 1024)
		if err != nil || !bytes.Equal(got, body) {
			t.Fatalf("workspace bytes %q: %v", got, err)
		}
	}
	t.Logf("five warm stat/private write+read/workspace write+read cycles: %s", time.Since(started))
	after, err := runDocker(ctx, realRuntime, "inspect", "--format", "{{.Id}}", containerName(id))
	if err != nil || string(before) != string(after) {
		t.Fatalf("container changed: %v", err)
	}
	traceBytes, err := os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(traceBytes), "\n") {
		if strings.HasPrefix(line, "container create ") || strings.HasPrefix(line, "run ") {
			t.Fatalf("routine helper creation: %s", line)
		}
	}
	db.cfg.ContainerRuntimePath = realRuntime
	// A blocked remote Python must die without stopping unrelated execs.
	otherCtx, otherCancel := context.WithCancel(ctx)
	otherCommand := db.botSessionCommand(otherCtx, id, "import os,time; print(os.getpid(),flush=True); time.sleep(60)")
	otherOut, err := otherCommand.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := otherCommand.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		otherCancel()
		if err := otherCommand.Wait(); err == nil {
			t.Error("cancelled companion succeeded")
		}
	}()
	otherPID, err := bufio.NewReader(otherOut).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	cancelCtx, cancelExec := context.WithCancel(ctx)
	command := db.botSessionCommand(cancelCtx, id, "import os,time; print(os.getpid(),flush=True); time.sleep(60)")
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	pid, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	cancelAt := time.Now()
	cancelExec()
	if err := command.Wait(); err == nil {
		t.Fatal("cancelled exec succeeded")
	}
	if time.Since(cancelAt) > 7*time.Second {
		t.Fatal("unbounded cancellation")
	}
	if _, err := runDocker(ctx, realRuntime, "exec", containerName(id), "/bin/sh", "-ec", "test ! -e /proc/"+strings.TrimSpace(pid)); err != nil {
		t.Fatalf("remote process survived cancellation: %v", err)
	}
	if _, err := runDocker(ctx, realRuntime, "exec", containerName(id), "/bin/sh", "-ec", "test -e /proc/"+strings.TrimSpace(otherPID)); err != nil {
		t.Fatalf("unrelated exec was killed: %v", err)
	}
	t.Logf("process-only cancellation: %s", time.Since(cancelAt))
	if _, err := db.StatBotHomeFile(ctx, h, "proof.txt"); err != nil {
		t.Fatalf("session lost on cancellation: %v", err)
	}
	entry, err := db.StatBotHomeFile(ctx, h, "proof.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.WriteBotHomeFile(ctx, h, store.BotFileWrite{Path: "proof.txt", Body: strings.NewReader("changed"), SizeBytes: 7, ExpectedSHA256: entry.SHA256, BeforeCommit: guard}); err != nil {
		t.Fatal(err)
	}
	raw, err := runDocker(ctx, db.cfg.ContainerRuntimePath, "ps", "-aq", "--filter=volume="+h.Ref)
	if err != nil || len(strings.Fields(string(raw))) != 1 {
		t.Fatalf("one container: %s %v", raw, err)
	}
	if err = db.DestroySession(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateSession(ctx, sandbox.SessionSpec{SessionID: id, Type: sandbox.SessionTypeChat, UserID: h.Scope.UserID, BotHome: h.Clone(), TemplateID: template, LayerChain: []string{sandbox.BaseTemplateID, template}}); err != nil {
		t.Fatal(err)
	}
	stream, err := db.OpenBotHomeFile(ctx, h, "proof.txt", store.BotFileRange{})
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(stream)
	closeErr := stream.Close()
	if err != nil || closeErr != nil || string(got) != "changed" {
		t.Fatalf("resume bytes %q: %v %v", got, err, closeErr)
	}
	stream, err = db.OpenBotContent(ctx, h, *snapshot, store.BotFileRange{})
	if err != nil {
		t.Fatal(err)
	}
	got, err = io.ReadAll(stream)
	closeErr = stream.Close()
	if err != nil || closeErr != nil || string(got) != "persistent" {
		t.Fatalf("published bytes %q: %v %v", got, err, closeErr)
	}
	for i, ref := range privateRefs {
		stream, err := db.OpenBotContent(ctx, h, ref, store.BotFileRange{})
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(stream)
		closeErr := stream.Close()
		want := []byte(fmt.Sprintf("private-%d\x00\xff", i))
		if err != nil || closeErr != nil || !bytes.Equal(got, want) {
			t.Fatalf("recreated private bytes %q: %v %v", got, err, closeErr)
		}
	}
	got, err = db.ReadBotHomeWorkspace(ctx, h, "tasks/proof/state/state.json", 1024)
	if err != nil || !bytes.Equal(got, []byte("private-4\x00\xff")) {
		t.Fatalf("recreated workspace bytes %q: %v", got, err)
	}
}
