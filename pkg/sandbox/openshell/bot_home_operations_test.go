package openshell

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/sandbox"
)

func TestBotHomeOfflineReadLifecycle(t *testing.T) {
	for _, scenario := range []string{"ok", "mount-mismatch", "exec-error", "cancel", "oversize", "cleanup-error"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var created CreateSandboxRequest
			deleted, executed := false, false
			gw := &mockGateway{}
			b := newTestBackendWithGateway(t, gw)
			binding := homeTestBinding(t, b)
			gw.createFn = func(_ context.Context, req CreateSandboxRequest) (*CreateSandboxResponse, error) {
				created = req
				if req.Env["HOME"] != botHomeMountPath || req.Policy.Process.RunAsUser != "sandbox" {
					t.Fatal("wrong helper identity")
				}
				kube := req.DriverConfig["kubernetes"].(map[string]any)
				mounts := kube["containers"].(map[string]any)["agent"].(map[string]any)["volume_mounts"].([]any)
				mount := mounts[len(mounts)-1].(map[string]any)
				if mount["sub_path"] != "bot-homes/"+binding.Scope.Key() || mount["read_only"] != true {
					t.Fatal("unsafe helper mount")
				}
				return &CreateSandboxResponse{SandboxID: req.Name, GatewayID: "exec-uuid"}, nil
			}
			gw.statusFn = func(_ context.Context, name string) (*SandboxStatus, error) {
				if name != created.Name {
					t.Fatal("status used UUID")
				}
				st := &SandboxStatus{GatewayID: "exec-uuid", State: SandboxStateRunning, Env: created.Env, Labels: created.Labels, DriverConfig: created.DriverConfig}
				if scenario == "mount-mismatch" {
					st.DriverConfig = nil
				}
				return st, nil
			}
			gw.execFn = func(_ context.Context, id string, req ExecRequest) (*ExecResponse, error) {
				executed = true
				if id != "exec-uuid" {
					t.Fatal("exec used name")
				}
				if req.Command[0] != "python3" || !strings.Contains(req.Command[3], "O_NOFOLLOW") || !strings.Contains(req.Command[3], "/proc/self/mountinfo") {
					t.Fatal("missing runtime guards")
				}
				switch scenario {
				case "exec-error":
					return nil, errors.New("exec failed")
				case "cancel":
					cancel()
					return nil, ctx.Err()
				case "oversize":
					return &ExecResponse{Stdout: []byte("too long")}, nil
				}
				return &ExecResponse{Stdout: []byte("ok")}, nil
			}
			gw.deleteFn = func(c context.Context, name string) error {
				deleted = true
				if c.Err() != nil || name != created.Name {
					t.Fatal("cleanup canceled or used UUID")
				}
				if scenario == "cleanup-error" {
					return errors.New("cleanup failed")
				}
				return nil
			}
			data, err := b.ReadBotHomeArtifact(ctx, binding, "report.txt", 4)
			if scenario == "ok" {
				if err != nil || string(data) != "ok" {
					t.Fatalf("%q %v", data, err)
				}
			} else if err == nil || data != nil {
				t.Fatalf("expected fail closed: %q %v", data, err)
			}
			if !deleted {
				t.Fatal("helper leaked")
			}
			if scenario == "mount-mismatch" && executed {
				t.Fatal("exec before ownership proof")
			}
		})
	}
}

func TestBotHomeOfflineAmbiguousCreateCleanup(t *testing.T) {
	for _, owned := range []bool{true, false} {
		t.Run(strconv.FormatBool(owned), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var req CreateSandboxRequest
			deleted := false
			gw := &mockGateway{}
			b := newTestBackendWithGateway(t, gw)
			binding := homeTestBinding(t, b)
			gw.createFn = func(_ context.Context, r CreateSandboxRequest) (*CreateSandboxResponse, error) {
				req = r
				cancel()
				return nil, context.Canceled
			}
			gw.statusFn = func(c context.Context, name string) (*SandboxStatus, error) {
				if c.Err() != nil || name != req.Name {
					t.Fatal("canceled ownership lookup")
				}
				st := &SandboxStatus{State: SandboxStateRunning, GatewayID: "uuid", Env: req.Env, Labels: req.Labels, DriverConfig: req.DriverConfig}
				if !owned {
					st.Labels = nil
				}
				return st, nil
			}
			gw.deleteFn = func(c context.Context, name string) error {
				if c.Err() != nil || name != req.Name {
					t.Fatal("bad cleanup")
				}
				deleted = true
				return nil
			}
			if _, err := b.ReadBotHomeArtifact(ctx, binding, "a", 4); !errors.Is(err, context.Canceled) {
				t.Fatalf("lost cancellation: %v", err)
			}
			if deleted != owned {
				t.Fatal("cleanup without ownership proof or helper leaked")
			}
		})
	}
}

func TestBotHomeOfflineReadFileGuards(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable for isolated reader algorithm test")
	}
	home := t.TempDir()
	home, err = filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "ok"), []byte("data"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("ok", filepath.Join(home, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(home, "dir"), 0700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(home, "escape")); err != nil {
		t.Fatal(err)
	}
	// Exercise the exact file-reading algorithm with a temporary root. Runtime
	// mount/UID checks are deliberately not simulated as gateway proof.
	_, body, ok := strings.Cut(botHomeArtifactReadScript, "resolved =")
	if !ok {
		t.Fatal("missing reader body")
	}
	script := "import os, stat, sys\nhome, relative, limit = sys.argv[1], sys.argv[2], int(sys.argv[3])\nresolved =" + body
	for _, tc := range []struct {
		path    string
		limit   int
		success bool
	}{{"ok", 4, true}, {"ok", 3, false}, {"link", 4, false}, {"escape/secret", 20, false}, {"dir", 4, false}, {"missing", 4, false}} {
		t.Run(tc.path+strconv.Itoa(tc.limit), func(t *testing.T) {
			data, err := exec.Command(python, "-c", script, home, tc.path, strconv.Itoa(tc.limit)).Output()
			if tc.success {
				if err != nil || string(data) != "data" {
					t.Fatalf("%q %v", data, err)
				}
			} else if err == nil || len(data) != 0 {
				t.Fatalf("unsafe read %q %v", data, err)
			}
		})
	}
}

func TestBotHomeOfflineRejectAndPurgeBlocker(t *testing.T) {
	gw := &mockGateway{createFn: func(context.Context, CreateSandboxRequest) (*CreateSandboxResponse, error) {
		t.Fatal("unexpected helper")
		return nil, nil
	}}
	b := newTestBackendWithGateway(t, gw)
	binding := homeTestBinding(t, b)
	for _, p := range []string{"../secret", "/host", "a/../b", ""} {
		if _, err := b.ReadBotHomeArtifact(context.Background(), binding, p, 4); err == nil {
			t.Fatalf("accepted %q", p)
		}
	}
	if _, err := b.ReadBotHomeArtifact(context.Background(), nil, "a", 4); err == nil {
		t.Fatal("nil binding")
	}
	for i := 0; i < 2; i++ {
		if err := b.DeleteBotHome(context.Background(), binding); !errors.Is(err, sandbox.ErrBotHomeUnsupported) {
			t.Fatalf("purge not blocked: %v", err)
		}
	}
}
