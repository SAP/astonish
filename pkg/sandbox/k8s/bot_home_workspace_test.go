package k8s

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/sandbox"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	k8stesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/remotecommand"
	k8sexec "k8s.io/client-go/util/exec"
)

// These are fake API/exec protocol tests, NOT live Kubernetes certification.
func workspaceFake(t *testing.T, b *K8sBackend) *fake.Clientset {
	t.Helper()
	client := b.client.(*fake.Clientset)
	b.restConfig = &rest.Config{Host: "https://fake.invalid"}
	client.PrependReactor("create", "pods", func(a k8stesting.Action) (bool, k8sruntime.Object, error) {
		p := a.(k8stesting.CreateAction).GetObject().(*corev1.Pod)
		if !safeBotWorkspacePod(p, p) || p.Spec.Containers[0].VolumeMounts[0].SubPath == "" || p.Spec.Containers[0].Image != BotWorkspaceHelperImage {
			t.Fatal("unsafe helper manifest")
		}
		p.UID = types.UID("owned-helper")
		p.Status.Phase = corev1.PodRunning
		p.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: containerName, Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}
		return false, nil, nil
	})
	return client
}

func TestBotHomeWorkspaceProtocol(t *testing.T) {
	for _, mode := range []string{"success", "oversized", "exec-error", "cancel", "mount", "uid", "token", "image", "cleanup", "ambiguous", "finalizer", "bad-json"} {
		t.Run(mode, func(t *testing.T) {
			b, h := homeBackend(t)
			client := workspaceFake(t, b)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			called := false
			b.execExecutorFactory = func(_ *rest.Config, _ string, u *url.URL) (remoteExecutor, error) {
				called = true
				args := u.Query()["command"]
				if len(args) != 4 || args[0] != "python3" || args[3] != botWorkspaceProgram || u.Query().Get("stdin") != "true" {
					t.Fatalf("unsafe exec %v", u)
				}
				return homeReadExecutor(func(ctx context.Context, o remotecommand.StreamOptions) error {
					var q map[string]any
					if e := json.NewDecoder(o.Stdin).Decode(&q); e != nil {
						return e
					}
					if q["owner"] != h.Ref || q["binding"] != h.Scope.Key() || q["path"] != "tasks/task/state/x" {
						t.Fatal(q)
					}
					switch mode {
					case "cancel":
						cancel()
						return ctx.Err()
					case "exec-error":
						return k8sexec.CodeExitError{Err: errors.New("rejected"), Code: 1}
					case "oversized":
						_, e := o.Stdout.Write(make([]byte, (24<<20)+1))
						return e
					case "bad-json":
						_, e := o.Stdout.Write([]byte("not-json"))
						return e
					}
					return json.NewEncoder(o.Stdout).Encode([]byte("value"))
				}), nil
			}
			switch mode {
			case "mount", "uid", "token", "image":
				client.PrependReactor("get", "pods", func(a k8stesting.Action) (bool, k8sruntime.Object, error) {
					obj, e := client.Tracker().Get(corev1.SchemeGroupVersion.WithResource("pods"), b.cfg.Namespace, a.(k8stesting.GetAction).GetName())
					if e != nil {
						return true, nil, e
					}
					p := obj.(*corev1.Pod).DeepCopy()
					switch mode {
					case "mount":
						p.Spec.Containers[0].VolumeMounts[0].SubPath = ""
					case "uid":
						p.UID = "replacement"
					case "token":
						p.Spec.AutomountServiceAccountToken = botHomeBool(true)
					case "image":
						p.Spec.Containers[0].Image = "foreign"
					}
					return true, p, nil
				})
			case "cleanup":
				client.PrependReactor("delete", "pods", func(k8stesting.Action) (bool, k8sruntime.Object, error) { return true, nil, errors.New("denied") })
			case "finalizer":
				client.PrependReactor("delete", "pods", func(k8stesting.Action) (bool, k8sruntime.Object, error) { return true, nil, nil })
			case "ambiguous":
				client.PrependReactor("create", "pods", func(k8stesting.Action) (bool, k8sruntime.Object, error) {
					return true, nil, errors.New("transport uncertain")
				})
			}
			data, e := b.ReadBotHomeWorkspace(ctx, h, "tasks/task/state/x", 12)
			if mode == "success" {
				if e != nil || string(data) != "value" {
					t.Fatalf("%q %v", data, e)
				}
			} else if e == nil || data != nil {
				t.Fatalf("accepted %s %q %v", mode, data, e)
			}
			if (mode == "mount" || mode == "uid" || mode == "token" || mode == "image" || mode == "ambiguous") && called {
				t.Fatal("executed unsafe pod")
			}
			rs := homeReservations(t, b, h)
			if mode == "cleanup" || mode == "finalizer" || mode == "ambiguous" || mode == "uid" {
				if len(rs) != 1 {
					t.Fatal("lost exclusion")
				}
			} else if len(rs) != 0 {
				t.Fatalf("leaked reservation %v", rs)
			}
			for _, a := range client.Actions() {
				if a.GetVerb() == "delete" && a.GetResource().Resource == "pods" {
					d := a.(k8stesting.DeleteAction).GetDeleteOptions()
					if d.Preconditions == nil || d.Preconditions.UID == nil || *d.Preconditions.UID != "owned-helper" {
						t.Fatal("unfenced cleanup")
					}
				}
			}
		})
	}
}

func TestBotHomeWorkspaceRejectBeforeAPI(t *testing.T) {
	b, h := homeBackend(t)
	client := b.client.(*fake.Clientset)
	client.ClearActions()
	ctx := context.Background()
	tests := []func() error{
		func() error { _, e := b.ReadBotHomeWorkspace(ctx, nil, "x", 12); return e },
		func() error { _, e := b.ReadBotHomeWorkspace(ctx, h, "../x", 12); return e },
		func() error {
			_, e := b.ReadBotHomeWorkspace(ctx, h, "x", sandbox.MaxBotHomeWorkspaceStateBytes+1)
			return e
		},
		func() error {
			return b.WriteBotHomeWorkspace(ctx, h, sandbox.BotHomeWorkspaceFile{Path: "tasks/task/TASK.md"})
		},
		func() error {
			return b.WriteBotHomeWorkspace(ctx, h, sandbox.BotHomeWorkspaceFile{Path: "tasks/task/state/x", Executable: true})
		},
		func() error { _, e := b.ResolveBotHomeWorkspaceReceipt(ctx, h, "../receipt"); return e },
		func() error { _, _, e := b.ListBotHomeWorkspace(ctx, h, "tasks/task", "", 101); return e },
		func() error { _, e := b.SnapshotBotHomeWorkspace(ctx, h, "tasks/task", 0); return e },
		func() error {
			_, e := b.PublishBotHomeWorkspace(ctx, h, sandbox.BotHomeWorkspacePublishRequest{PublicationID: "../pub", TaskPath: "tasks/task", Files: []sandbox.BotHomeWorkspaceFile{{Path: "TASK.md"}}})
			return e
		},
		func() error { _, e := b.ReadBotHomeWorkspace(ctx, h, "x", 12); return e }, // unavailable executor fails closed
	}
	for i, f := range tests {
		if f() == nil {
			t.Fatalf("accepted %d", i)
		}
	}
	if len(client.Actions()) != 0 {
		t.Fatal("invalid requests reached API")
	}
}

// Local Python filesystem fixtures exercise the server-owned program with a
// substituted mount path and empty mountinfo fixture. They do not prove
// PVC/kubelet confinement or the Linux nested-mount check.
func workspaceLocal(t *testing.T, root string, q map[string]any, out any) error {
	t.Helper()
	raw, e := json.Marshal(q)
	if e != nil {
		return e
	}
	mountinfo := filepath.Join(root, "mountinfo-fixture")
	if e := os.WriteFile(mountinfo, nil, 0600); e != nil {
		return e
	}
	program := strings.ReplaceAll(botWorkspaceProgram, "/proc/self/mountinfo", mountinfo)
	program = strings.Replace(program, "def ready():\n", "def ready():\n return\n", 1)
	cmd := exec.CommandContext(t.Context(), "python3", "-I", "-c", strings.ReplaceAll(program, mountBotHome, root))
	cmd.Stdin = bytes.NewReader(append(raw, '\n'))
	data, e := cmd.Output()
	if e != nil {
		return e
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}
func workspaceFixture(t *testing.T) (string, map[string]any) {
	t.Helper()
	root := t.TempDir()
	if e := os.Mkdir(filepath.Join(root, "data"), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(root, ".owner"), []byte("owner\n"), 0600); e != nil {
		t.Fatal(e)
	}
	return root, map[string]any{"owner": "owner", "binding": "scope"}
}
func TestBotHomeWorkspaceFilesystem(t *testing.T) {
	root, q := workspaceFixture(t)
	_, binding := homeBackend(t)
	q["binding"] = binding.Scope.Key()
	request := sandbox.BotHomeWorkspacePublishRequest{TaskPath: "tasks/task", PublicationID: "pub", Files: []sandbox.BotHomeWorkspaceFile{{Path: "TASK.md", Data: []byte("assignment")}, {Path: "scripts/helper.py", Data: []byte("print('ok')"), Executable: true}}}
	q["op"] = "publish"
	q["request"] = request
	var receipt sandbox.BotHomeWorkspaceReceipt
	if e := workspaceLocal(t, root, q, &receipt); e != nil {
		t.Fatal(e)
	}
	if e := validateWorkspaceReceipt(binding, &receipt); e != nil {
		t.Fatal(e)
	}
	// Retry reads existing immutable revision, never overwrites it.
	if e := workspaceLocal(t, root, q, &receipt); e != nil {
		t.Fatal(e)
	}
	edited := request
	edited.Files = append([]sandbox.BotHomeWorkspaceFile(nil), request.Files...)
	edited.Files[0].Data = []byte("edited")
	q["request"] = edited
	if workspaceLocal(t, root, q, nil) == nil {
		t.Fatal("edited retry accepted")
	}
	q["op"] = "snapshot"
	q["path"] = "tasks/task"
	q["limit"] = int64(1024)
	var snap sandbox.BotHomeWorkspaceSnapshot
	if e := workspaceLocal(t, root, q, &snap); e != nil || len(snap.Files) != 2 || string(snap.Files[0].Data) != "assignment" {
		t.Fatalf("%v %+v", e, snap)
	}
	q["op"] = "resolve"
	q["path"] = "pub"
	if e := workspaceLocal(t, root, q, &receipt); e != nil {
		t.Fatal(e)
	}
	q["op"] = "list"
	q["path"] = "tasks/task"
	q["limit"] = 1
	q["after"] = ""
	var page struct {
		Entries []sandbox.BotHomeWorkspaceEntry
		Next    string
	}
	if e := workspaceLocal(t, root, q, &page); e != nil || len(page.Entries) != 1 || page.Next != "TASK.md" {
		t.Fatalf("%v %+v", e, page)
	}
	q["op"] = "write"
	q["path"] = "tasks/task/state/value"
	q["data"] = []byte("state")
	if e := workspaceLocal(t, root, q, nil); e != nil {
		t.Fatal(e)
	}
	q["op"] = "read"
	q["limit"] = 1024
	var data []byte
	if e := workspaceLocal(t, root, q, &data); e != nil || string(data) != "state" {
		t.Fatalf("%q %v", data, e)
	}
	q["path"] = "tasks/task/TASK.md"
	if e := workspaceLocal(t, root, q, &data); e != nil || string(data) != "assignment" {
		t.Fatalf("%q %v", data, e)
	}
	// New revision preserves mutable state; stale CAS cannot select another head.
	q["op"] = "publish"
	request.PublicationID = "pub2"
	request.ExpectedDigest = receipt.Digest
	q["request"] = request
	if e := workspaceLocal(t, root, q, &receipt); e != nil {
		t.Fatal(e)
	}
	request.PublicationID = "pub3"
	request.ExpectedDigest = ""
	q["request"] = request
	if workspaceLocal(t, root, q, nil) == nil {
		t.Fatal("stale CAS accepted")
	}
	q["op"] = "read"
	q["path"] = "tasks/task/state/value"
	if e := workspaceLocal(t, root, q, &data); e != nil || string(data) != "state" {
		t.Fatalf("%q %v", data, e)
	}
	// Exercise all public adapters against local byte I/O through fake exec.
	backend, home := homeBackend(t)
	workspaceFake(t, backend)
	backend.execExecutorFactory = func(_ *rest.Config, _ string, _ *url.URL) (remoteExecutor, error) {
		return homeReadExecutor(func(_ context.Context, o remotecommand.StreamOptions) error {
			var request map[string]any
			if err := json.NewDecoder(o.Stdin).Decode(&request); err != nil {
				return err
			}
			request["owner"] = "owner"
			var result json.RawMessage
			if err := workspaceLocal(t, root, request, &result); err != nil {
				return err
			}
			_, err := o.Stdout.Write(append(result, '\n'))
			return err
		}), nil
	}
	if _, err := backend.ResolveBotHomeWorkspaceReceipt(t.Context(), home, "pub2"); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.SnapshotBotHomeWorkspace(t.Context(), home, "tasks/task", 1024); err != nil {
		t.Fatal(err)
	}
	if entries, next, err := backend.ListBotHomeWorkspace(t.Context(), home, "tasks/task", "", 1); err != nil || len(entries) != 1 || next != "TASK.md" {
		t.Fatalf("list: %v %q %v", entries, next, err)
	}
	if err := backend.WriteBotHomeWorkspace(t.Context(), home, sandbox.BotHomeWorkspaceFile{Path: "tasks/task/state/adapter", Data: []byte("adapter")}); err != nil {
		t.Fatal(err)
	}
	if data, err := backend.ReadBotHomeWorkspace(t.Context(), home, "tasks/task/state/adapter", 1024); err != nil || string(data) != "adapter" {
		t.Fatalf("read: %q %v", data, err)
	}
	request.PublicationID = "adapterpub"
	request.ExpectedDigest = receipt.Digest
	if _, err := backend.PublishBotHomeWorkspace(t.Context(), home, request); err != nil {
		t.Fatal(err)
	}
}
func TestBotHomeWorkspaceFilesystemRejects(t *testing.T) {
	for _, mode := range []string{"owner", "symlink", "ancestor", "hardlink", "tamper", "undeclared", "limit", "private", "quota"} {
		t.Run(mode, func(t *testing.T) {
			root, q := workspaceFixture(t)
			q["op"] = "publish"
			q["request"] = sandbox.BotHomeWorkspacePublishRequest{TaskPath: "tasks/task", PublicationID: "pub", Files: []sandbox.BotHomeWorkspaceFile{{Path: "TASK.md", Data: []byte("assignment")}}}
			if e := workspaceLocal(t, root, q, nil); e != nil {
				t.Fatal(e)
			}
			file := filepath.Join(root, "workspace-private/tasks/task/.revisions/pub/TASK.md")
			must := func(e error) {
				t.Helper()
				if e != nil {
					t.Fatal(e)
				}
			}
			q["op"] = "snapshot"
			q["path"] = "tasks/task"
			q["limit"] = 1024
			switch mode {
			case "owner":
				must(os.WriteFile(filepath.Join(root, ".owner"), []byte("foreign\n"), 0600))
			case "symlink":
				must(os.Remove(file))
				must(os.Symlink(filepath.Join(root, ".owner"), file))
			case "ancestor":
				must(os.Rename(filepath.Join(root, "workspace-private/tasks"), filepath.Join(root, "other")))
				must(os.Symlink(filepath.Join(root, "other"), filepath.Join(root, "workspace-private/tasks")))
			case "hardlink":
				must(os.Link(file, filepath.Join(root, "linked")))
			case "tamper":
				must(os.WriteFile(file, []byte("changed"), 0600))
			case "undeclared":
				must(os.WriteFile(filepath.Join(filepath.Dir(file), "extra"), []byte("extra"), 0600))
			case "limit":
				q["limit"] = 1
			case "private":
				q["op"] = "read"
				q["path"] = "tasks/task/.head"
			case "quota":
				f, e := os.Create(filepath.Join(root, "workspace-private/large"))
				must(e)
				must(f.Truncate(sandbox.MaxBotHomeWorkspaceBytes))
				must(f.Close())
				q["op"] = "write"
				q["path"] = "tasks/task/state/x"
				q["data"] = []byte("x")
			}
			if workspaceLocal(t, root, q, nil) == nil {
				t.Fatalf("accepted %s", mode)
			}
		})
	}
}

func TestBotHomeWorkspaceManifestReadback(t *testing.T) {
	b, h := homeBackend(t)
	workspaceFake(t, b)
	b.execExecutorFactory = func(_ *rest.Config, _ string, _ *url.URL) (remoteExecutor, error) {
		return homeReadExecutor(func(_ context.Context, o remotecommand.StreamOptions) error {
			return json.NewEncoder(o.Stdout).Encode(sandbox.BotHomeWorkspaceReceipt{BindingKey: "foreign"})
		}), nil
	}
	if r, e := b.ResolveBotHomeWorkspaceReceipt(context.Background(), h, "pub"); e == nil || r != nil {
		t.Fatal("accepted foreign receipt")
	}
	if r, e := b.SnapshotBotHomeWorkspace(context.Background(), h, "tasks/task", 1024); e == nil || r != nil {
		t.Fatal("accepted invalid snapshot")
	}
	pods, e := b.client.CoreV1().Pods(b.cfg.Namespace).List(context.Background(), metav1.ListOptions{})
	if e != nil || len(pods.Items) != 0 {
		t.Fatal("leaked helper")
	}
}
