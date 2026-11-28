package k8s

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	k8stesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/remotecommand"
)

func TestBotHomeFilesRejectForgedContentMetadata(t *testing.T) {
	b, h, _ := localHomeFilesBackend(t)
	ref, err := b.PutBotContent(t.Context(), h, store.BotContentEvidence, 3, strings.NewReader("abc"), 7, func(f func() error) error { return f() })
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"kind", "revision"} {
		t.Run(field, func(t *testing.T) {
			forged := *ref
			if field == "kind" {
				forged.Kind = store.BotContentArtifact
			} else {
				forged.Revision++
			}
			s, err := b.OpenBotContent(t.Context(), h, forged, store.BotFileRange{})
			if err == nil {
				_ = s.Close()
				t.Fatal("forged immutable metadata accepted")
			}
		})
	}
}

func TestBotHomeFilesPrivateProspectiveQuota(t *testing.T) {
	for _, op := range []string{"put", "snapshot"} {
		t.Run(op, func(t *testing.T) {
			b, h, root := localHomeFilesBackend(t)
			private := filepath.Join(root, "workspace-private")
			if err := os.Mkdir(private, 0700); err != nil {
				t.Fatal(err)
			}
			f, err := os.Create(filepath.Join(private, "prior-task-state"))
			if err != nil {
				t.Fatal(err)
			}
			if err = f.Truncate(512 << 20); err != nil {
				t.Fatal(err)
			}
			_ = f.Close()
			called := false
			barrier := func(f func() error) error { called = true; return f() }
			if op == "put" {
				_, err = b.PutBotContent(t.Context(), h, store.BotContentArtifact, 1, strings.NewReader("x"), 1, barrier)
			} else {
				if err = os.WriteFile(filepath.Join(root, "data/report.txt"), []byte("x"), 0600); err != nil {
					t.Fatal(err)
				}
				_, err = b.PublishBotArtifactSnapshot(t.Context(), h, "report.txt", "report.txt", barrier)
			}
			if err == nil || called {
				t.Fatalf("prospective private quota bypassed: barrier=%v err=%v", called, err)
			}
		})
	}
}

func TestBotHomeFilesPrivateSharedLock(t *testing.T) {
	b, h, root := localHomeFilesBackend(t)
	private := filepath.Join(root, "workspace-private")
	if err := os.Mkdir(private, 0700); err != nil {
		t.Fatal(err)
	}
	lock, err := os.OpenFile(filepath.Join(private, ".workspace-lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	ready := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() {
		_, err := b.PutBotContent(t.Context(), h, store.BotContentArtifact, 1, strings.NewReader("x"), 1, func(f func() error) error { ready <- struct{}{}; return f() })
		done <- err
	}()
	select {
	case <-ready:
		t.Error("immutable write bypassed Task workspace lock")
	case <-time.After(150 * time.Millisecond):
	}
	_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	if err = <-done; err != nil {
		t.Fatal(err)
	}
}

func TestBotHomeFilesCancelBlockedUpload(t *testing.T) {
	b, h, root := localHomeFilesBackend(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	done := make(chan error, 1)
	go func() {
		_, err := b.WriteBotHomeFile(ctx, h, store.BotFileWrite{Path: "upload.bin", SizeBytes: 2, Body: reader, CreateOnly: true, BeforeCommit: func(f func() error) error { return f() }})
		done <- err
	}()
	if _, err := writer.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled upload succeeded")
		}
	case <-time.After(time.Second):
		_ = reader.Close()
		<-done
		t.Fatal("cancelled upload did not interrupt the source reader")
	}
	if _, err := os.Stat(filepath.Join(root, "data/upload.bin")); !os.IsNotExist(err) {
		t.Fatal("cancelled upload published")
	}
}

func TestBotHomeFilesCloseInterruptsPendingJoin(t *testing.T) {
	b, h := homeBackend(t)
	workspaceFake(t, b)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	// A normal EOF join can begin before the exec transport has exited. A
	// concurrent Close must cancel that transport rather than wait behind it.
	p := &homeFileProcess{backend: b, binding: h, cancel: cancel, done: make(chan error, 1)}
	reader, writer := io.Pipe()
	p.input = writer
	defer reader.Close()
	joined := make(chan struct{})
	go func() { _ = p.finish(false); close(joined) }()
	// EOF on stdin proves finish(false) entered its join before Close starts.
	if _, err := reader.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("join did not close input: %v", err)
	}
	closed := make(chan struct{})
	go func() { _ = p.Close(); close(closed) }()
	go func() { <-ctx.Done(); p.done <- ctx.Err() }()
	select {
	case <-closed:
	case <-time.After(time.Second):
		cancel() // release the blocked goroutines before reporting failure
		<-joined
		<-closed
		t.Fatal("Close waited behind the pending join instead of cancelling exec")
	}
	<-joined
}

// Fake API and exec transport are not live Kubernetes/PVC evidence.
func TestBotHomeFilesStreamProtocol(t *testing.T) {
	for _, mode := range []string{"large", "close", "cancel", "short", "overlong", "terminal", "cleanup", "finalizer", "ambiguous", "uid", "mount", "image"} {
		t.Run(mode, func(t *testing.T) {
			b, h := homeBackend(t)
			client := workspaceFake(t, b)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			joined := make(chan struct{})
			b.execExecutorFactory = func(_ *rest.Config, _ string, u *url.URL) (remoteExecutor, error) {
				args := u.Query()["command"]
				if len(args) != 3 || !strings.Contains(args[2], "/usr/local/bin/astonish-host node --internal-scoped-home-files") || strings.Contains(args[2], "python") {
					t.Fatal("incorrect confined program")
				}
				return homeReadExecutor(func(ctx context.Context, o remotecommand.StreamOptions) error {
					defer close(joined)
					var q map[string]any
					if e := json.NewDecoder(o.Stdin).Decode(&q); e != nil {
						return e
					}
					if q["op"] != "open" || q["path"] != "large.bin" {
						return errors.New("incorrect request")
					}
					size := int64(17 << 20)
					if e := json.NewEncoder(o.Stdout).Encode(store.BotFileEntry{Path: "large.bin", SizeBytes: size}); e != nil {
						return e
					}
					if mode == "close" || mode == "cancel" {
						<-ctx.Done()
						return ctx.Err()
					}
					if mode == "short" {
						return nil
					}
					if _, e := io.CopyN(o.Stdout, zeroReader{}, size); e != nil {
						return e
					}
					if mode == "overlong" {
						_, err := o.Stdout.Write([]byte{0xff})
						return err
					}
					if mode == "terminal" {
						return errors.New("file changed")
					}
					return nil
				}), nil
			}
			switch mode {
			case "cleanup":
				client.PrependReactor("delete", "pods", func(k8stesting.Action) (bool, k8sruntime.Object, error) { return true, nil, errors.New("denied") })
			case "finalizer":
				client.PrependReactor("delete", "pods", func(k8stesting.Action) (bool, k8sruntime.Object, error) { return true, nil, nil })
			case "ambiguous":
				client.PrependReactor("create", "pods", func(k8stesting.Action) (bool, k8sruntime.Object, error) {
					return true, nil, errors.New("uncertain create")
				})
			case "uid", "mount", "image":
				client.PrependReactor("get", "pods", func(a k8stesting.Action) (bool, k8sruntime.Object, error) {
					obj, e := client.Tracker().Get(corev1.SchemeGroupVersion.WithResource("pods"), b.cfg.Namespace, a.(k8stesting.GetAction).GetName())
					if e != nil {
						return true, nil, e
					}
					p := obj.(*corev1.Pod).DeepCopy()
					switch mode {
					case "uid":
						p.UID = "foreign"
					case "mount":
						p.Spec.Containers[0].VolumeMounts[0].SubPath = ""
					case "image":
						p.Spec.Containers[0].Image = "foreign"
					}
					return true, p, nil
				})
			}
			stream, e := b.OpenBotHomeFile(ctx, h, "large.bin", store.BotFileRange{})
			if mode == "ambiguous" || mode == "uid" || mode == "mount" || mode == "image" {
				if e == nil {
					t.Fatal("unsafe helper accepted")
				}
			} else {
				if e != nil {
					t.Fatal(e)
				}
				if mode == "close" {
					_ = stream.Close()
				} else if mode == "cancel" {
					cancel()
				} else {
					n, err := io.Copy(io.Discard, stream)
					if mode == "large" {
						if err != nil || n != 17<<20 {
							t.Fatalf("%d %v", n, err)
						}
					} else if err == nil {
						t.Fatal("missing terminal error")
					}
				}
				select {
				case <-joined:
				case <-time.After(5 * time.Second):
					t.Fatal("exec not joined")
				}
				_ = stream.Close()
			}
			rs := homeReservations(t, b, h)
			retained := mode == "cleanup" || mode == "finalizer" || mode == "ambiguous" || mode == "uid"
			if (len(rs) == 1) != retained {
				t.Fatalf("reservation mismatch: %v", rs)
			}
			for _, a := range client.Actions() {
				if a.GetVerb() == "create" && a.GetResource().Resource == "pods" {
					p := a.(k8stesting.CreateAction).GetObject().(*corev1.Pod)
					if p.Spec.ActiveDeadlineSeconds != nil || strings.Join(p.Spec.Containers[0].Command, " ") != "/bin/sleep infinity" || p.Spec.Containers[0].Image != b.cfg.SandboxImage {
						t.Fatal("stream lifetime truncated")
					}
					if !p.Spec.Containers[0].VolumeMounts[0].ReadOnly {
						t.Fatal("download writable")
					}
				}
				if a.GetVerb() == "delete" && a.GetResource().Resource == "pods" {
					opts := a.(k8stesting.DeleteAction).GetDeleteOptions()
					if opts.Preconditions == nil || opts.Preconditions.UID == nil || *opts.Preconditions.UID != "owned-helper" {
						t.Fatal("unfenced delete")
					}
				}
			}
		})
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) { clear(p); return len(p), nil }

func TestBotHomeFilesWriteProtocol(t *testing.T) {
	for _, mode := range []string{"success", "deny", "short", "missing", "double", "cancel", "terminal"} {
		t.Run(mode, func(t *testing.T) {
			b, h := homeBackend(t)
			workspaceFake(t, b)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			committed := false
			dispatches, barriers := 0, 0
			terminalErr := errors.New("transport lost after commit")
			b.execExecutorFactory = func(_ *rest.Config, _ string, _ *url.URL) (remoteExecutor, error) {
				dispatches++
				return homeReadExecutor(func(ctx context.Context, o remotecommand.StreamOptions) error {
					r := bufio.NewReader(o.Stdin)
					line, e := r.ReadBytes('\n')
					if e != nil {
						return e
					}
					var q map[string]any
					if e = json.Unmarshal(line, &q); e != nil {
						return e
					}
					body := make([]byte, 3)
					if _, e = io.ReadFull(r, body); e != nil {
						return e
					}
					if string(body) != "abc" {
						return errors.New("bad body")
					}
					if e = json.NewEncoder(o.Stdout).Encode(map[string]bool{"commit": true}); e != nil {
						return e
					}
					line, e = r.ReadBytes('\n')
					if e != nil {
						return e
					}
					if string(line) != "commit\n" {
						return errors.New("missing approval")
					}
					committed = true
					if err := json.NewEncoder(o.Stdout).Encode(store.BotFileEntry{Path: "x.md", SizeBytes: 3}); err != nil {
						return err
					}
					if mode == "terminal" {
						return terminalErr
					}
					return nil
				}), nil
			}
			barrier := func(commit func() error) error {
				barriers++
				switch mode {
				case "deny":
					return store.ErrBotForbidden
				case "missing":
					return nil
				case "cancel":
					cancel()
				}
				if err := commit(); err != nil {
					return err
				}
				if mode == "double" {
					return commit()
				}
				return nil
			}
			body := "abc"
			if mode == "short" {
				body = "ab"
			}
			entry, e := b.WriteBotHomeFile(ctx, h, store.BotFileWrite{Path: "x.md", Body: strings.NewReader(body), SizeBytes: 3, CreateOnly: true, BeforeCommit: barrier})
			if mode == "success" {
				if e != nil || entry == nil || !committed {
					t.Fatalf("%+v %v", entry, e)
				}
			} else if e == nil || entry != nil {
				t.Fatal("mutation accepted")
			}
			if mode == "terminal" && (!errors.Is(e, terminalErr) || !committed || dispatches != 1 || barriers != 1) {
				t.Fatalf("ambiguous commit was hidden or replayed: committed=%v dispatches=%d barriers=%d err=%v", committed, dispatches, barriers, e)
			}
			if mode != "success" && mode != "double" && mode != "terminal" && committed {
				t.Fatal("unapproved commit")
			}
			if len(homeReservations(t, b, h)) != 0 {
				t.Fatal("attachment leaked")
			}
		})
	}
}

// Exercise the actual compiled node through the adapter using Linux temporary
// roots. This preserves procfs/openat2 checks; it does NOT certify kubelet.
func localHomeFilesBackend(t *testing.T) (*K8sBackend, *sandbox.BotHomeBinding, string) {
	t.Helper()
	binary := os.Getenv("ASTONISH_BOT_FILES_TEST_BINARY")
	if runtime.GOOS != "linux" || !filepath.IsAbs(binary) {
		t.Skip("compiled filesystem fixture requires Linux and ASTONISH_BOT_FILES_TEST_BINARY")
	}
	b, h := homeBackend(t)
	h.Scope.BotID = uuid.NewString()
	// homeBackend already issued a binding; obtain the matching scope reference.
	var err error
	h, err = b.EnsureBotHome(t.Context(), h.Scope)
	if err != nil {
		t.Fatal(err)
	}
	workspaceFake(t, b)
	root := t.TempDir()
	if err = os.Mkdir(filepath.Join(root, "data"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, ".owner"), []byte(h.Ref+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	b.execExecutorFactory = func(_ *rest.Config, _ string, u *url.URL) (remoteExecutor, error) {
		args := append([]string(nil), u.Query()["command"]...)
		if len(args) != 3 || !strings.Contains(args[2], "--internal-scoped-home-files") {
			t.Fatal("not compiled node dispatch")
		}
		args[2] = strings.ReplaceAll(args[2], mountBotHome, root)
		args[2] = strings.ReplaceAll(args[2], "/usr/local/bin/astonish-host", binary)
		return homeReadExecutor(func(ctx context.Context, o remotecommand.StreamOptions) error {
			cmd := exec.CommandContext(ctx, args[0], args[1:]...)
			input, writer, err := os.Pipe()
			if err != nil {
				return err
			}
			defer input.Close()
			cmd.Stdin = input
			cmd.Stdout = o.Stdout
			cmd.Stderr = o.Stderr
			copied := make(chan struct{})
			go func() { _, _ = io.Copy(writer, o.Stdin); _ = writer.Close(); close(copied) }()
			err = cmd.Run()
			// Model remote exec's input closure when the remote process exits.
			_ = o.Stdin.(io.ReadCloser).Close()
			_ = writer.Close()
			<-copied
			return err
		}), nil
	}
	return b, h, root
}
func TestBotHomeFilesFilesystem(t *testing.T) {
	b, h, root := localHomeFilesBackend(t)
	ctx := t.Context()
	barrier := func(f func() error) error { return f() }
	payload := bytes.Repeat([]byte("files-first!\x00"), 1500000)
	sum := sha256.Sum256(payload)
	entry, e := b.WriteBotHomeFile(ctx, h, store.BotFileWrite{Path: "outputs/report.bin", Body: bytes.NewReader(payload), SizeBytes: int64(len(payload)), CreateOnly: true, BeforeCommit: barrier})
	if e != nil || entry.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("%+v %v", entry, e)
	}
	page, e := b.ListBotHomeFiles(ctx, h, ".", "", 1)
	if e != nil || len(page.Entries) != 1 || page.Entries[0].Path != "outputs" {
		t.Fatalf("%+v %v", page, e)
	}
	if _, e = os.Stat(filepath.Join(root, "workspace-private")); !os.IsNotExist(e) {
		t.Fatal("read provisioned private storage")
	}
	stat, e := b.StatBotHomeFile(ctx, h, "outputs/report.bin")
	if e != nil || stat.SizeBytes != int64(len(payload)) {
		t.Fatalf("%+v %v", stat, e)
	}
	stream, e := b.OpenBotHomeFile(ctx, h, "outputs/report.bin", store.BotFileRange{Offset: 12, Length: 131073})
	if e != nil {
		t.Fatal(e)
	}
	got, e := io.ReadAll(stream)
	if e != nil || !bytes.Equal(got, payload[12:12+131073]) {
		t.Fatalf("range %d %v", len(got), e)
	}
	_ = stream.Close()
	ref, e := b.PublishBotArtifactSnapshot(ctx, h, "outputs/report.bin", "report.bin", barrier)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = b.WriteBotHomeFile(ctx, h, store.BotFileWrite{Path: "outputs/report.bin", Body: strings.NewReader("new"), SizeBytes: 3, ExpectedSHA256: entry.SHA256, BeforeCommit: barrier}); e != nil {
		t.Fatal(e)
	}
	immutable, e := b.OpenBotContent(ctx, h, *ref, store.BotFileRange{})
	if e != nil {
		t.Fatal(e)
	}
	hash := sha256.New()
	n, e := io.Copy(hash, immutable)
	if e != nil || n != int64(len(payload)) || !bytes.Equal(hash.Sum(nil), sum[:]) {
		t.Fatalf("snapshot %d %v", n, e)
	}
	_ = immutable.Close()
	put, e := b.PutBotContent(ctx, h, store.BotContentArtifact, 3, strings.NewReader("abc"), 1, barrier)
	if e != nil {
		t.Fatal(e)
	}
	protected, e := b.OpenBotContent(ctx, h, *put, store.BotFileRange{})
	if e != nil {
		t.Fatal(e)
	}
	got, e = io.ReadAll(protected)
	if e != nil || string(got) != "abc" {
		t.Fatalf("%q %v", got, e)
	}
	_ = protected.Close()
	forged := *put
	forged.Scope.BotID = uuid.New()
	if _, e = b.OpenBotContent(ctx, h, forged, store.BotFileRange{}); e == nil {
		t.Fatal("cross-scope accepted")
	}
	for _, mode := range []string{"stale", "denied", "short"} {
		w := store.BotFileWrite{Path: "outputs/report.bin", Body: strings.NewReader("bad"), SizeBytes: 3, ExpectedSHA256: strings.Repeat("0", 64), BeforeCommit: barrier}
		s := sha256.Sum256([]byte("new"))
		if mode != "stale" {
			w.ExpectedSHA256 = hex.EncodeToString(s[:])
		}
		if mode == "denied" {
			w.BeforeCommit = func(func() error) error { return store.ErrBotForbidden }
		}
		if mode == "short" {
			w.SizeBytes = 5
		}
		if _, e = b.WriteBotHomeFile(ctx, h, w); e == nil {
			t.Fatal("invalid write accepted", mode)
		}
		data, e := os.ReadFile(filepath.Join(root, "data/outputs/report.bin"))
		if e != nil || string(data) != "new" {
			t.Fatal("prior file altered", mode)
		}
	}
}
func TestBotHomeFilesConfinement(t *testing.T) {
	b, h, root := localHomeFilesBackend(t)
	if e := os.WriteFile(filepath.Join(root, "data/good.txt"), []byte("ok"), 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.Symlink(root, filepath.Join(root, "data/link")); e != nil {
		t.Fatal(e)
	}
	if e := os.Link(filepath.Join(root, "data/good.txt"), filepath.Join(root, "data/hard.txt")); e != nil {
		t.Fatal(e)
	}
	if e := syscall.Mkfifo(filepath.Join(root, "data/fifo"), 0600); e != nil {
		t.Fatal(e)
	}
	for _, path := range []string{"../.owner", "/etc/passwd", "link/.owner", "good.txt", "hard.txt", "fifo", ".", "a//b"} {
		if stream, e := b.OpenBotHomeFile(t.Context(), h, path, store.BotFileRange{}); e == nil {
			_ = stream.Close()
			t.Fatal("unsafe path accepted", path)
		}
	}
	page, e := b.ListBotHomeFiles(t.Context(), h, ".", "", 0)
	if e != nil || len(page.Entries) != 0 {
		t.Fatalf("unsafe listing %+v %v", page, e)
	}
}
func TestBotHomeFilesRejectBeforeAPI(t *testing.T) {
	b, h := homeBackend(t)
	tests := []func() error{
		func() error { _, e := b.OpenBotHomeFile(t.Context(), nil, "x", store.BotFileRange{}); return e },
		func() error { _, e := b.OpenBotHomeFile(t.Context(), h, "x", store.BotFileRange{Offset: -1}); return e },
		func() error { _, e := b.ListBotHomeFiles(t.Context(), h, ".", "", 501); return e },
		func() error {
			_, e := b.WriteBotHomeFile(t.Context(), h, store.BotFileWrite{Path: "x", SizeBytes: 0, CreateOnly: true})
			return e
		},
		func() error {
			_, e := b.PutBotContent(t.Context(), h, store.BotContentArtifact, 0, nil, 1, nil)
			return e
		},
		func() error {
			_, e := b.StatBotHomeFile(t.Context(), h, "x")
			if !errors.Is(e, sandbox.ErrBotHomeUnsupported) {
				t.Fatalf("not unsupported: %v", e)
			}
			return e
		},
	}
	for i, f := range tests {
		if f() == nil {
			t.Fatalf("accepted %d", i)
		}
	}
}
