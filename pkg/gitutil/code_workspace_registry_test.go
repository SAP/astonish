package gitutil

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestCodeWorkspaceConcurrentRegistry(t *testing.T) {
	if os.Getenv("ASTONISH_WORKSPACE_REGISTRY_CHILD") == "1" {
		m, e := NewCodeWorkspaceManager(os.Getenv("ASTONISH_WORKSPACE_ROOT"))
		if e != nil {
			t.Fatal(e)
		}
		r, e := m.Create(context.Background(), WorkspaceCreateOptions{SourceDir: os.Getenv("ASTONISH_WORKSPACE_SOURCE")})
		if e != nil {
			t.Fatal(e)
		}
		if e = m.BindSession(context.Background(), r.Project, r.ID, SessionBinding{SessionID: r.ID, UserID: "user", AppName: "code"}); e != nil {
			t.Fatal(e)
		}
		return
	}
	dir, m := workspaceTestRepo(t)
	var wg sync.WaitGroup
	errCh := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cmd := exec.Command(os.Args[0], "-test.run=^TestCodeWorkspaceConcurrentRegistry$")
			cmd.Env = append(os.Environ(), "ASTONISH_WORKSPACE_REGISTRY_CHILD=1", "ASTONISH_WORKSPACE_ROOT="+m.Root(), "ASTONISH_WORKSPACE_SOURCE="+dir)
			out, e := cmd.CombinedOutput()
			if e != nil {
				errCh <- errors.New(string(out))
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for e := range errCh {
		t.Fatal(e)
	}
	p, e := ResolveCodeProject(context.Background(), dir)
	if e != nil {
		t.Fatal(e)
	}
	rs, e := m.List(context.Background(), p)
	if e != nil || len(rs) != 4 {
		t.Fatal("lost records", len(rs), e)
	}
	for _, r := range rs {
		if r.State != WorkspaceReady || len(r.Sessions) != 1 {
			t.Fatal("lost bindings", r)
		}
	}
	st, e := os.Stat(filepath.Join(m.Root(), p.ID, "registry.json"))
	if e != nil || st.Mode().Perm() != 0600 {
		t.Fatal("registry permission", e)
	}
}
func TestCodeWorkspaceLeaseProcessDeath(t *testing.T) {
	if os.Getenv("ASTONISH_WORKSPACE_LEASE_CHILD") == "1" {
		ctx := context.Background()
		m, e := NewCodeWorkspaceManager(os.Getenv("ASTONISH_WORKSPACE_ROOT"))
		if e != nil {
			t.Fatal(e)
		}
		p, e := ResolveCodeProject(ctx, os.Getenv("ASTONISH_WORKSPACE_SOURCE"))
		if e != nil {
			t.Fatal(e)
		}
		l, e := m.AcquireLease(ctx, p, os.Getenv("ASTONISH_WORKSPACE_ID"))
		if e != nil {
			t.Fatal(e)
		}
		defer l.Close()
		if e = os.WriteFile(os.Getenv("ASTONISH_WORKSPACE_SIGNAL"), []byte("ready"), 0600); e != nil {
			t.Fatal(e)
		}
		time.Sleep(time.Minute)
		return
	}
	ctx := context.Background()
	dir, m := workspaceTestRepo(t)
	r, e := m.Create(ctx, WorkspaceCreateOptions{SourceDir: dir})
	if e != nil {
		t.Fatal(e)
	}
	signal := filepath.Join(workspaceTestDir(t), "ready")
	cmd := exec.Command(os.Args[0], "-test.run=^TestCodeWorkspaceLeaseProcessDeath$")
	cmd.Env = append(os.Environ(), "ASTONISH_WORKSPACE_LEASE_CHILD=1", "ASTONISH_WORKSPACE_ROOT="+m.Root(), "ASTONISH_WORKSPACE_SOURCE="+dir, "ASTONISH_WORKSPACE_ID="+r.ID, "ASTONISH_WORKSPACE_SIGNAL="+signal)
	if e = cmd.Start(); e != nil {
		t.Fatal(e)
	}
	defer cmd.Process.Kill()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, e = os.Stat(signal); e == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("child did not acquire lease")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, e = m.AcquireLease(ctx, r.Project, r.ID); !errors.Is(e, ErrWorkspaceBusy) {
		t.Fatal("second writer not blocked", e)
	}
	if e = cmd.Process.Kill(); e != nil {
		t.Fatal(e)
	}
	_ = cmd.Wait()
	lease, e := m.AcquireLease(ctx, r.Project, r.ID)
	if e != nil {
		t.Fatal("lease not released on death", e)
	}
	if e = lease.Close(); e != nil {
		t.Fatal(e)
	}
}
func TestCodeWorkspaceRegistryBindingsAndStates(t *testing.T) {
	ctx := context.Background()
	dir, m := workspaceTestRepo(t)
	r, e := m.Create(ctx, WorkspaceCreateOptions{SourceDir: dir})
	if e != nil {
		t.Fatal(e)
	}
	b := SessionBinding{SessionID: "session", UserID: "user", AppName: "code", StorageDir: "legacy"}
	if e = m.BindSession(ctx, r.Project, r.ID, b); e != nil {
		t.Fatal(e)
	}
	b.StorageDir = "updated"
	if e = m.BindSession(ctx, r.Project, r.ID, b); e != nil {
		t.Fatal(e)
	}
	found, binding, e := m.FindSession(ctx, r.Project, "session")
	if e != nil || found.ID != r.ID || binding.StorageDir != "updated" || len(found.Sessions) != 1 {
		t.Fatal("binding update failed", e)
	}
	found, e = m.LookupDirectory(ctx, filepath.Join(r.Root, "sub"))
	if e != nil || found.ID != r.ID {
		t.Fatal("lookup failed", e)
	}
	if e = m.SetState(ctx, r.Project, r.ID, WorkspacePreparing, ""); e == nil {
		t.Fatal("invalid transition allowed")
	}
	if e = m.SetState(ctx, r.Project, r.ID, WorkspaceRetained, ""); e != nil {
		t.Fatal(e)
	}
	if e = m.SetState(ctx, r.Project, r.ID, WorkspaceRemoved, ""); e != nil {
		t.Fatal(e)
	}
	if _, e = m.AcquireLease(ctx, r.Project, r.ID); !errors.Is(e, ErrWorkspaceOwnership) {
		t.Fatal(e)
	}
	if _, e = os.Stat(r.Root); e != nil {
		t.Fatal("state removed checkout")
	}
}
