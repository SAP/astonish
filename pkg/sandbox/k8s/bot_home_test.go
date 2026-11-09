package k8s

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/sandbox"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"
)

func homeBackend(t *testing.T) (*K8sBackend, *sandbox.BotHomeBinding) {
	t.Helper()
	b, _ := newBackendWithFakeClient(t, &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "homes", Namespace: "astonish-sandboxes"}})
	b.cfg.HomesPVCName = "homes"
	h, err := b.EnsureBotHome(context.Background(), sandbox.BotHomeScope{OrgID: "org", TeamID: "team", UserID: "user", BotID: "bot"})
	if err != nil {
		t.Fatal(err)
	}
	return b, h
}

func TestBotHomeEnsureScopeAndConfig(t *testing.T) {
	t.Setenv("KUBECONFIG", "")
	b, h := homeBackend(t)
	again, err := b.EnsureBotHome(context.Background(), h.Scope)
	if err != nil || !h.Equal(again) {
		t.Fatalf("idempotency: %v", err)
	}
	for _, field := range []string{"org", "team", "user", "bot"} {
		scope := h.Scope
		switch field {
		case "org":
			scope.OrgID += "2"
		case "team":
			scope.TeamID += "2"
		case "user":
			scope.UserID += "2"
		case "bot":
			scope.BotID += "2"
		}
		other, err := b.EnsureBotHome(context.Background(), scope)
		if err != nil || other.Ref == h.Ref || other.Scope.Key() == h.Scope.Key() {
			t.Fatalf("scope collision %s: %v", field, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := b.EnsureBotHome(ctx, h.Scope); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	b.cfg.HomesPVCName = "missing"
	if _, err := b.EnsureBotHome(context.Background(), h.Scope); err == nil {
		t.Fatal("accepted missing claim")
	}
	b.cfg.HomesPVCName = ""
	if _, err := b.EnsureBotHome(context.Background(), h.Scope); !errors.Is(err, sandbox.ErrBotHomeUnsupported) {
		t.Fatal(err)
	}
	for _, claim := range []string{"../homes", "/homes", "homes:root", "astonish-layers", "astonish-uppers"} {
		if _, err := New(Config{Sessions: newRegistry(t), HomesPVCName: claim}); err == nil {
			t.Fatalf("accepted %q", claim)
		}
	}
	var cfg config.SandboxKubernetesConfig
	if err := yaml.Unmarshal([]byte("homes_pvc_name: retained\n"), &cfg); err != nil || cfg.HomesPVCName != "retained" {
		t.Fatalf("config decode: %+v %v", cfg, err)
	}
	backend, err := sandbox.NewBackend(sandbox.BackendFactoryConfig{Kind: sandbox.BackendKindK8s, Sessions: newRegistry(t), K8s: cfg})
	if err != nil {
		t.Fatal(err)
	}
	if backend.(*K8sBackend).cfg.HomesPVCName != "retained" {
		t.Fatal("factory dropped homes claim")
	}
}

func TestBotHomeManifestIsolationAndNilCompatibility(t *testing.T) {
	b, h := homeBackend(t)
	spec := sandbox.SessionSpec{SessionID: "retained", BotHome: h}
	pod, err := b.buildPodManifest(spec)
	if err != nil {
		t.Fatal(err)
	}
	if len(pod.Spec.InitContainers) != 1 {
		t.Fatal("missing ownership initialization")
	}
	for _, c := range append(pod.Spec.InitContainers, pod.Spec.Containers...) {
		for _, m := range c.VolumeMounts {
			if m.Name == volumeBotHome {
				if m.SubPath == "" || !strings.HasPrefix(m.SubPath, h.Scope.Key()) || m.MountPath != mountBotHome {
					t.Fatalf("unsafe mount: %+v", m)
				}
			}
		}
	}
	m := pod.Spec.Containers[0].VolumeMounts[len(pod.Spec.Containers[0].VolumeMounts)-1]
	if m.SubPath != h.Scope.Key()+"/data" {
		t.Fatal("workload exposes ownership metadata")
	}
	if pod.Spec.AutomountServiceAccountToken == nil || *pod.Spec.AutomountServiceAccountToken {
		t.Fatal("workload can access control-plane metadata")
	}
	plain, err := b.buildPodManifest(sandbox.SessionSpec{SessionID: "plain"})
	if err != nil {
		t.Fatal(err)
	}
	if len(plain.Spec.Volumes) != 3 || plain.Spec.Volumes[2].EmptyDir == nil || len(plain.Spec.InitContainers) != 0 {
		t.Fatal("ordinary session changed")
	}
	disabled, _ := newBackendWithFakeClient(t)
	old, _ := disabled.buildPodManifest(sandbox.SessionSpec{SessionID: "plain"})
	if !reflect.DeepEqual(plain.Spec, old.Spec) {
		t.Fatal("opt-in config changed ordinary pod spec")
	}
	for _, ref := range []string{"../other", "/root", "k8s-home-v1:../../", "homes/other", h.Ref + "x"} {
		bad := h.Clone()
		bad.Ref = ref
		if _, err := b.buildPodManifest(sandbox.SessionSpec{SessionID: "bad", BotHome: bad}); err == nil {
			t.Fatalf("accepted unsafe reference %q", ref)
		}
	}
	bad := h.Clone()
	bad.Backend = sandbox.BackendKindDocker
	if _, err := b.buildPodManifest(sandbox.SessionSpec{SessionID: "bad", BotHome: bad}); err == nil {
		t.Fatal("accepted foreign backend")
	}
	bad = h.Clone()
	bad.Scope.TeamID = "other"
	if _, err := b.buildPodManifest(sandbox.SessionSpec{SessionID: "bad", BotHome: bad}); err == nil {
		t.Fatal("accepted foreign scope")
	}
}

func TestBotHomeSessionRecoveryConflictAndDestroy(t *testing.T) {
	b, h := homeBackend(t)
	ctx := context.Background()
	spec := sandbox.SessionSpec{SessionID: "home-run", BotHome: h}
	if _, err := b.CreateSession(ctx, spec); err != nil {
		t.Fatal(err)
	}
	if _, err := b.CreateSession(ctx, spec); err != nil {
		t.Fatal(err)
	}
	if _, err := b.CreateSession(ctx, sandbox.SessionSpec{SessionID: spec.SessionID}); err == nil {
		t.Fatal("silently dropped retained binding")
	}
	otherScope := h.Scope
	otherScope.BotID = "other"
	other, _ := b.EnsureBotHome(ctx, otherScope)
	if _, err := b.CreateSession(ctx, sandbox.SessionSpec{SessionID: spec.SessionID, BotHome: other}); err == nil {
		t.Fatal("rebound retained home")
	}
	if err := b.client.CoreV1().Pods(b.cfg.Namespace).Delete(ctx, podNameForSession(spec.SessionID), metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	// Rebuild backend to prove recovery is persisted rather than in memory.
	restored, err := New(b.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := restored.StartSession(ctx, spec.SessionID); err != nil {
		t.Fatal(err)
	}
	pod, err := b.client.CoreV1().Pods(b.cfg.Namespace).Get(ctx, podNameForSession(spec.SessionID), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := b.validatePodBotHome(pod, h); err != nil {
		t.Fatal(err)
	}
	if err := restored.DestroySession(ctx, spec.SessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := b.client.CoreV1().PersistentVolumeClaims(b.cfg.Namespace).Get(ctx, "homes", metav1.GetOptions{}); err != nil {
		t.Fatal("destroy deleted retained claim", err)
	}
	if _, err := b.client.CoreV1().ConfigMaps(b.cfg.Namespace).Get(ctx, botHomeSessionName(spec.SessionID), metav1.GetOptions{}); err == nil {
		t.Fatal("session metadata leaked")
	}
	if _, err := b.CreateSession(ctx, sandbox.SessionSpec{SessionID: "next-run", BotHome: h}); err != nil {
		t.Fatal(err)
	}
}

func TestBotHomeExistingPodConflict(t *testing.T) {
	b, h := homeBackend(t)
	ctx := context.Background()
	pod, _ := b.buildPodManifest(sandbox.SessionSpec{SessionID: "collision"})
	if _, err := b.client.CoreV1().Pods(b.cfg.Namespace).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.CreateSession(ctx, sandbox.SessionSpec{SessionID: "collision", BotHome: h}); err == nil {
		t.Fatal("adopted pod with different home")
	}
}

func TestBotHomeOwnershipInit(t *testing.T) {
	for _, scenario := range []string{"fresh", "conflict", "unowned-data", "symlink", "unsafe-owner"} {
		t.Run(scenario, func(t *testing.T) {
			dir := t.TempDir()
			data := filepath.Join(dir, "data")
			if err := os.Mkdir(data, 0700); err != nil {
				t.Fatal(err)
			}
			wantErr := scenario != "fresh"
			switch scenario {
			case "conflict":
				os.WriteFile(filepath.Join(dir, ".owner"), []byte("other"), 0600)
			case "unowned-data":
				os.WriteFile(filepath.Join(data, "file"), []byte("valuable"), 0600)
			case "symlink":
				os.Remove(data)
				os.Symlink(t.TempDir(), data)
			case "unsafe-owner":
				os.Symlink(filepath.Join(dir, "missing"), filepath.Join(dir, ".owner"))
			}
			// Replace only the fixed mount and chown syscall: manifest owns UID 0;
			// developer unit tests run unprivileged. Other filesystem checks are real.
			script := strings.ReplaceAll(botHomeInitScript, mountBotHome, dir)
			script = strings.ReplaceAll(script, "chown 0:0 data", ": ownership")
			run := func() error {
				cmd := exec.Command("/bin/sh", "-c", script)
				cmd.Env = append(os.Environ(), "EXPECTED_OWNER=opaque-owner")
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Log(string(out))
				}
				return err
			}
			err := run()
			if (err != nil) != wantErr {
				t.Fatalf("init error %v wantErr=%v", err, wantErr)
			}
			if !wantErr {
				if err := os.WriteFile(filepath.Join(data, "keep"), []byte("persisted"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(data, 0750); err != nil {
					t.Fatal(err)
				}
				if err := run(); err != nil {
					t.Fatal("repeat init", err)
				}
				info, _ := os.Stat(data)
				if info.Mode().Perm() != 0750 {
					t.Fatal("reinitialized permissions")
				}
				got, _ := os.ReadFile(filepath.Join(data, "keep"))
				if string(got) != "persisted" {
					t.Fatal("lost data")
				}
			}
		})
	}
}

func TestBotHomeEntrypointContract(t *testing.T) {
	script := EntrypointScript(EntrypointScriptOptions{})
	if !strings.Contains(script, botHomeMountScript) {
		t.Fatal("generated entrypoint omits retained home")
	}
	if strings.Index(script, botHomeMountScript) > strings.Index(script, "# --- 3. Hand off") {
		t.Fatal("mount after chroot")
	}
	for _, script := range []string{buildCaptureScript(mountLayers, "builder"), buildPersistUpperScript(mountUppers, "")} {
		if strings.Contains(script, mountBotHome) || strings.Contains(script, "-C /sandbox/rootfs") {
			t.Fatal("home captured in upper snapshot")
		}
	}
	for _, value := range []string{"../root", "/root", "true"} {
		cmd := exec.Command("/bin/sh", "-c", botHomeMountScript)
		cmd.Env = append(os.Environ(), "ASTONISH_BOT_HOME="+value)
		if err := cmd.Run(); err == nil {
			t.Fatalf("accepted unsafe mode %q", value)
		}
	}
	cmd := exec.Command("/bin/sh", "-c", botHomeMountScript)
	cmd.Env = append(os.Environ(), "ASTONISH_BOT_HOME=")
	if err := cmd.Run(); err != nil {
		t.Fatal("ordinary entrypoint changed", err)
	}
}
