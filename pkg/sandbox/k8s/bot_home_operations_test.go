package k8s

import (
	"context"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/sandbox"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	k8stesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/remotecommand"
	k8sexec "k8s.io/client-go/util/exec"
)

type homeReadExecutor func(context.Context, remotecommand.StreamOptions) error

func (f homeReadExecutor) StreamWithContext(ctx context.Context, o remotecommand.StreamOptions) error {
	return f(ctx, o)
}

func TestBotHomeOfflineCapability(t *testing.T) {
	b, h := homeBackend(t)
	reader, ok := any(b).(sandbox.BotHomeArtifactBackend)
	if !ok {
		t.Fatal("missing offline artifact capability")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := reader.ReadBotHomeArtifact(ctx, h, "artifact.txt", 12); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func readyHomeHelper(t *testing.T, b *K8sBackend) *fake.Clientset {
	t.Helper()
	client := b.client.(*fake.Clientset)
	b.restConfig = &rest.Config{Host: "https://fake.invalid"}
	client.PrependReactor("create", "pods", func(a k8stesting.Action) (bool, k8sruntime.Object, error) {
		p := a.(k8stesting.CreateAction).GetObject().(*corev1.Pod)
		if len(p.Spec.InitContainers) != 0 || len(p.Spec.Volumes) != 1 || !p.Spec.Volumes[0].PersistentVolumeClaim.ReadOnly || !p.Spec.Containers[0].VolumeMounts[0].ReadOnly || p.Spec.Containers[0].VolumeMounts[0].SubPath == "" || *p.Spec.AutomountServiceAccountToken {
			t.Fatal("unsafe helper manifest")
		}
		p.Status.Phase = corev1.PodRunning
		p.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: containerName, Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}
		return false, nil, nil
	})
	return client
}

func TestBotHomeOfflineReadCleanup(t *testing.T) {
	for _, mode := range []string{"success", "oversized", "exec-error", "cancel", "foreign-mount", "cleanup-error"} {
		t.Run(mode, func(t *testing.T) {
			b, h := homeBackend(t)
			client := readyHomeHelper(t, b)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			called := false
			b.execExecutorFactory = func(_ *rest.Config, _ string, u *url.URL) (remoteExecutor, error) {
				called = true
				args := u.Query()["command"]
				if len(args) != 7 || args[4] != h.Ref || args[5] != "artifact.txt" || args[6] != "12" {
					t.Fatalf("command: %q", args)
				}
				return homeReadExecutor(func(ctx context.Context, o remotecommand.StreamOptions) error {
					switch mode {
					case "cancel":
						cancel()
						<-ctx.Done()
						return ctx.Err()
					case "oversized":
						_, err := o.Stdout.Write([]byte(strings.Repeat("x", 30)))
						return err
					case "exec-error":
						return k8sexec.CodeExitError{Err: errors.New("owner mismatch"), Code: 1}
					default:
						_, err := o.Stdout.Write([]byte("artifact"))
						return err
					}
				}), nil
			}
			if mode == "foreign-mount" {
				client.PrependReactor("get", "pods", func(a k8stesting.Action) (bool, k8sruntime.Object, error) {
					obj, err := client.Tracker().Get(corev1.SchemeGroupVersion.WithResource("pods"), b.cfg.Namespace, a.(k8stesting.GetAction).GetName())
					if err != nil {
						return true, nil, err
					}
					p := obj.(*corev1.Pod).DeepCopy()
					p.Spec.Containers[0].VolumeMounts[0].SubPath = ""
					return true, p, nil
				})
			}
			if mode == "cleanup-error" {
				client.PrependReactor("delete", "pods", func(k8stesting.Action) (bool, k8sruntime.Object, error) { return true, nil, errors.New("denied") })
			}
			data, err := b.ReadBotHomeArtifact(ctx, h, "artifact.txt", 12)
			if mode == "success" {
				if err != nil || string(data) != "artifact" {
					t.Fatalf("%q %v", data, err)
				}
			} else if err == nil || data != nil {
				t.Fatalf("accepted %s", mode)
			}
			if mode == "foreign-mount" && called {
				t.Fatal("executed foreign pod")
			}
			pods, err := client.CoreV1().Pods(b.cfg.Namespace).List(context.Background(), metav1.ListOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if mode != "cleanup-error" && len(pods.Items) != 0 {
				t.Fatal("leaked helper")
			}
			cms, err := client.CoreV1().ConfigMaps(b.cfg.Namespace).List(context.Background(), metav1.ListOptions{})
			if err != nil {
				t.Fatal(err)
			}
			for _, cm := range cms.Items {
				if cm.Name != botHomeFenceName(h) {
					t.Fatal("created session metadata")
				}
			}
			rs := homeReservations(t, b, h)
			if mode == "cleanup-error" {
				if len(rs) != 1 {
					t.Fatal("cleanup failure released exclusion")
				}
			} else if len(rs) != 0 {
				t.Fatal("successful helper cleanup retained reservation")
			}
		})
	}
}

func TestBotHomeOfflineReadRejectsBeforeAPI(t *testing.T) {
	for _, mode := range []string{"nil", "foreign", "path", "limit", "unsupported"} {
		t.Run(mode, func(t *testing.T) {
			b, h := homeBackend(t)
			client := b.client.(*fake.Clientset)
			client.ClearActions()
			p := "artifact"
			limit := int64(12)
			switch mode {
			case "nil":
				h = nil
			case "foreign":
				h.Scope.UserID = "other"
			case "path":
				p = "../outside"
			case "limit":
				limit = sandbox.MaxBotHomeArtifactBytes + 1
			case "unsupported":
				b.cfg.HomesPVCName = ""
			}
			if _, err := b.ReadBotHomeArtifact(context.Background(), h, p, limit); err == nil {
				t.Fatal("accepted invalid request")
			}
			if len(client.Actions()) != 0 {
				t.Fatal("invalid request reached API")
			}
		})
	}
}

// Executes the exact production command, substituting only the mount root.
// Linux procfs and GNU coreutils are supplied by the production base image.
func TestBotHomeOfflineReadFilesystem(t *testing.T) {
	image := os.Getenv("ASTONISH_BOT_HOME_READER_TEST_IMAGE")
	if runtime.GOOS != "linux" && image == "" {
		t.Skip("requires Linux procfs or explicit ASTONISH_BOT_HOME_READER_TEST_IMAGE")
	}
	if image != "" {
		wd, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		t.Setenv("TMPDIR", wd)
	}
	for _, mode := range []string{"success", "owner", "missing-owner", "symlink", "directory-symlink", "oversized", "directory", "fifo"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			outside := t.TempDir()
			ref := "k8s-home-v1:owner"
			if err := os.Mkdir(filepath.Join(root, "data"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, ".owner"), []byte(ref+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(root, "data", "artifact")
			if err := os.WriteFile(file, []byte("artifact"), 0600); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "owner":
				os.WriteFile(filepath.Join(root, ".owner"), []byte("foreign\n"), 0600)
			case "missing-owner":
				os.Remove(filepath.Join(root, ".owner"))
			case "symlink":
				os.Remove(file)
				os.Symlink(filepath.Join(root, ".owner"), file)
			case "directory-symlink":
				os.RemoveAll(filepath.Join(root, "data"))
				os.WriteFile(filepath.Join(outside, "artifact"), []byte("foreign"), 0600)
				os.Symlink(outside, filepath.Join(root, "data"))
			case "oversized":
				os.WriteFile(file, []byte(strings.Repeat("x", 30)), 0600)
			case "directory":
				os.Remove(file)
				os.Mkdir(file, 0700)
			case "fifo":
				if err := os.Remove(file); err != nil {
					t.Fatal(err)
				}
				if out, err := exec.Command("mkfifo", file).CombinedOutput(); err != nil {
					t.Fatalf("mkfifo: %s %v", out, err)
				}
			}
			script := strings.ReplaceAll(botHomeReadScript, mountBotHome, root)
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "/bin/sh", "-c", script, "home-read", ref, "artifact", "12")
			if image != "" {
				cmd = exec.CommandContext(ctx, "docker", "container", "run", "--rm", "--network=none", "--entrypoint=/bin/sh", "--mount=type=bind,src="+root+",dst="+root+",readonly", "--mount=type=bind,src="+outside+",dst="+outside+",readonly", image, "-c", script, "home-read", ref, "artifact", "12")
			}
			out, err := cmd.Output()
			if ctx.Err() != nil {
				t.Fatalf("reader blocked: %v", ctx.Err())
			}
			if mode == "success" {
				if err != nil || string(out) != "artifact" {
					t.Fatalf("%q %v", out, err)
				}
			} else if err == nil || len(out) != 0 {
				t.Fatalf("accepted %s: %q", mode, out)
			}
		})
	}
}
