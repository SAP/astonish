package k8s

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/sandbox"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	k8stesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/remotecommand"
)

func archiveAllow(ctx context.Context, _ *sandbox.BotHomeBinding, op func(context.Context) error) error {
	return op(ctx)
}
func archiveFixture(t *testing.T) (*K8sBackend, *sandbox.BotHomeBinding) {
	t.Helper()
	b, h := homeBackend(t)
	workspaceFake(t, b)
	if err := b.mutateBotHomeFence(t.Context(), h, func(botHomeFenceState, map[string]botHomeReservation) error { return nil }); err != nil {
		t.Fatal(err)
	}
	return b, h
}
func TestK8sArchiveAdmission(t *testing.T) {
	b, h := archiveFixture(t)
	if err := b.archiveFence(t.Context(), h, "owned", true); err != nil {
		t.Fatal(err)
	}
	if _, err := b.reserveBotHome(t.Context(), h, "late-session"); err == nil {
		t.Fatal("archive admitted writer")
	}
	if err := b.DeleteBotHome(t.Context(), h); err == nil {
		t.Fatal("archive admitted purge")
	}
	if err := b.archiveFence(t.Context(), h, "other", true); err == nil {
		t.Fatal("archive admitted competitor")
	}
	if err := b.archiveFence(t.Context(), h, "other", false); err == nil {
		t.Fatal("foreign release")
	}
	if err := b.archiveFence(t.Context(), h, "owned", false); err != nil {
		t.Fatal(err)
	}
	token, err := b.reserveBotHome(t.Context(), h, "pending")
	if err != nil || token == "" {
		t.Fatal(err)
	}
	if err := b.archiveFence(t.Context(), h, "archive", true); err == nil {
		t.Fatal("pending attachment ignored")
	}
}
func TestK8sArchiveBarrier(t *testing.T) {
	for _, mode := range []string{"missing", "swallow", "repeat", "cancel", "escape"} {
		t.Run(mode, func(t *testing.T) {
			b, h := archiveFixture(t)
			b.execExecutorFactory = func(*rest.Config, string, *url.URL) (remoteExecutor, error) { return nil, errors.New("unused") }
			var escaped func(context.Context) error
			barrier := func(c context.Context, _ *sandbox.BotHomeBinding, op func(context.Context) error) error {
				switch mode {
				case "missing":
					return nil
				case "swallow":
					_ = op(c)
					return nil
				case "repeat":
					_ = op(c)
					return op(c)
				case "cancel":
					ctx, cancel := context.WithCancel(c)
					cancel()
					return op(ctx)
				case "escape":
					escaped = op
					return nil
				}
				return nil
			}
			if err := b.archiveBarrier(t.Context(), h, barrier, func(context.Context) error { return errors.New("operation rejected") }); err == nil {
				t.Fatal("unsafe barrier accepted")
			}
			if escaped != nil && escaped(t.Context()) == nil {
				t.Fatal("escaped callback accepted")
			}
		})
	}
}
func TestK8sArchiveCompiledCLI(t *testing.T) {
	binary := os.Getenv("ASTONISH_BOT_ARCHIVE_TEST_BINARY")
	if runtime.GOOS != "linux" || binary == "" {
		t.Skip("requires explicit Linux compiled root CLI fixture; not Kubernetes evidence")
	}
	for _, mode := range []string{"roundtrip", "generation", "owner", "missing-private", "mount", "truncated", "fence", "race", "uid", "ambiguous", "cleanup"} {
		t.Run(mode, func(t *testing.T) {
			b, h := archiveFixture(t)
			client := workspaceFake(t, b)
			root := t.TempDir()
			for _, dir := range []string{"data", "workspace-private"} {
				if err := os.Mkdir(filepath.Join(root, dir), 0700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(root, ".owner"), []byte(h.Ref+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			data := bytes.Repeat([]byte("exact-large-file\x00\xff\n"), 1<<20)
			if err := os.WriteFile(filepath.Join(root, "data", "large.bin"), data, 0600); err != nil {
				t.Fatal(err)
			}
			private := []byte("private retained memory")
			if err := os.WriteFile(filepath.Join(root, "workspace-private", "memory.md"), private, 0600); err != nil {
				t.Fatal(err)
			}
			called := 0
			b.execExecutorFactory = func(_ *rest.Config, _ string, u *url.URL) (remoteExecutor, error) {
				command := u.Query()["command"]
				if len(command) != 3 || command[0] != "/bin/sh" || !strings.Contains(command[2], "/usr/local/bin/astonish-host node --internal-scoped-home-archive") || strings.Contains(command[2], "python") {
					t.Fatalf("unsafe command %v", command)
				}
				called++
				return homeReadExecutor(func(ctx context.Context, o remotecommand.StreamOptions) error {
					cmdline := strings.ReplaceAll(command[2], mountBotHome, root)
					cmdline = strings.ReplaceAll(cmdline, "/usr/local/bin/astonish-host", shellQuote(binary))
					cmd := exec.CommandContext(ctx, "/bin/sh", "-ec", cmdline)
					// Give exec a real descriptor: os/exec otherwise waits forever for
					// its stdin copier when the CLI rejects roots before reading JSON.
					input, writer, err := os.Pipe()
					if err != nil {
						return err
					}
					defer input.Close()
					copied := make(chan struct{})
					go func() {
						defer close(copied)
						_, _ = io.Copy(writer, o.Stdin)
						_ = writer.Close()
					}()
					cmd.Stdin, cmd.Stdout, cmd.Stderr = input, o.Stdout, o.Stderr
					err = cmd.Run()
					_ = writer.Close()
					_ = o.Stdin.(io.Closer).Close()
					<-copied
					return err
				}), nil
			}
			switch mode {
			case "owner":
				if err := os.WriteFile(filepath.Join(root, ".owner"), []byte("foreign\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "missing-private":
				if err := os.RemoveAll(filepath.Join(root, "workspace-private")); err != nil {
					t.Fatal(err)
				}
			case "mount", "uid":
				client.PrependReactor("get", "pods", func(a k8stesting.Action) (bool, k8sruntime.Object, error) {
					name := a.(k8stesting.GetAction).GetName()
					obj, err := client.Tracker().Get(corev1.SchemeGroupVersion.WithResource("pods"), b.cfg.Namespace, name)
					if err != nil {
						return false, nil, nil
					}
					p := obj.(*corev1.Pod).DeepCopy()
					if mode == "mount" {
						p.Spec.Containers[0].VolumeMounts[0].SubPath = "foreign"
					} else {
						p.UID = "foreign"
					}
					return true, p, nil
				})
			case "ambiguous":
				client.PrependReactor("create", "pods", func(k8stesting.Action) (bool, k8sruntime.Object, error) {
					return true, nil, errors.New("ambiguous create")
				})
			case "cleanup":
				client.PrependReactor("delete", "pods", func(k8stesting.Action) (bool, k8sruntime.Object, error) {
					return true, nil, errors.New("delete denied")
				})
			}
			var output bytes.Buffer
			m, err := b.ExportBotHomeArchive(t.Context(), h, &output, archiveAllow)
			if mode == "owner" || mode == "missing-private" || mode == "mount" || mode == "uid" || mode == "ambiguous" || mode == "cleanup" {
				if err == nil || m != nil {
					t.Fatal("invalid export accepted")
				}
				if (mode == "mount" || mode == "uid" || mode == "ambiguous") && called != 0 {
					t.Fatal("invalid target executed")
				}
				if mode == "ambiguous" || mode == "cleanup" {
					cm, e := client.CoreV1().ConfigMaps(b.cfg.Namespace).Get(t.Context(), botHomeFenceName(h), metav1.GetOptions{})
					if e != nil || cm.Data["state"] != string(botHomeArchiving) {
						t.Fatal("lost durable exclusion", e)
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(output.Bytes(), append(append([]byte{}, data...), private...)) {
				t.Fatal("archive bytes differ")
			}
			scope := h.Scope
			scope.BotID = "restored"
			var dest *sandbox.BotHomeBinding
			if mode == "generation" {
				dest, err = b.CreateBotHomeRestoreTarget(t.Context(), h.Scope)
			} else {
				dest, err = b.EnsureBotHome(t.Context(), scope)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = b.mutateBotHomeFence(t.Context(), dest, func(botHomeFenceState, map[string]botHomeReservation) error { return nil }); err != nil {
				t.Fatal(err)
			}
			sourceRoot := root
			root = t.TempDir()
			if mode != "generation" {
				if err = os.Mkdir(filepath.Join(root, "data"), 0700); err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(filepath.Join(root, ".owner"), []byte(dest.Ref+"\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			defer func() {
				got, e := os.ReadFile(filepath.Join(sourceRoot, "data", "large.bin"))
				if e != nil || !bytes.Equal(got, data) {
					t.Error("source data modified", e)
				}
				got, e = os.ReadFile(filepath.Join(sourceRoot, "workspace-private", "memory.md"))
				if e != nil || !bytes.Equal(got, private) {
					t.Error("source private data modified", e)
				}
			}()
			input := output.Bytes()
			if mode == "truncated" {
				input = input[:len(input)-1]
			}
			var reader io.Reader = bytes.NewReader(input)
			if mode == "fence" || mode == "race" {
				reader = &archiveOnceReader{Reader: reader, once: func() {
					if mode == "race" {
						if _, e := b.reserveBotHome(t.Context(), dest, "racing-writer"); e == nil {
							t.Error("racing writer admitted")
						}
						return
					}
					cm, e := client.CoreV1().ConfigMaps(b.cfg.Namespace).Get(t.Context(), botHomeFenceName(dest), metav1.GetOptions{})
					if e != nil {
						t.Fatal(e)
					}
					cm.Data[archiveOperationKey] = "lost"
					if _, e = client.CoreV1().ConfigMaps(b.cfg.Namespace).Update(t.Context(), cm, metav1.UpdateOptions{}); e != nil {
						t.Fatal(e)
					}
				}}
			}
			err = b.RestoreBotHomeArchive(t.Context(), dest, m, reader, archiveAllow)
			if mode == "truncated" || mode == "fence" {
				if err == nil {
					t.Fatal("bad restore accepted")
				}
				state, e := os.ReadFile(filepath.Join(root, "workspace-private", ".archive-state"))
				if e != nil || string(state) != "restoring\n" {
					t.Fatalf("failed target selectable: %q %v", state, e)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(filepath.Join(root, "data", "large.bin"))
			if err != nil || !bytes.Equal(got, data) {
				t.Fatal("restore bytes", err)
			}
			var again bytes.Buffer
			restored, err := b.ExportBotHomeArchive(t.Context(), dest, &again, archiveAllow)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(again.Bytes(), output.Bytes()) {
				t.Fatal("roundtrip bytes changed")
			}
			restored.Binding = m.Binding
			a, _ := json.Marshal(restored)
			z, _ := json.Marshal(m)
			if !bytes.Equal(a, z) {
				t.Fatal("roundtrip manifest changed")
			}
			if err = b.RestoreBotHomeArchive(t.Context(), dest, m, bytes.NewReader(input), archiveAllow); err == nil {
				t.Fatal("nonempty target adopted")
			}
			for _, a := range client.Actions() {
				if a.GetVerb() == "create" && a.GetResource().Resource == "pods" {
					p := a.(k8stesting.CreateAction).GetObject().(*corev1.Pod)
					if len(p.Spec.InitContainers) != 0 || !strings.HasPrefix(p.Name, "astn-home-archive-") {
						t.Fatal("started run or provisioned Home")
					}
				}
			}
		})
	}
}

type archiveOnceReader struct {
	io.Reader
	once func()
}

func (r *archiveOnceReader) Read(p []byte) (int, error) {
	if r.once != nil {
		f := r.once
		r.once = nil
		f()
	}
	return r.Reader.Read(p)
}
