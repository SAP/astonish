package launcher

import (
	"context"
	"errors"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/gitutil"
	persistentsession "github.com/SAP/astonish/pkg/session"
	"github.com/SAP/astonish/pkg/tui/backend"
	adksession "google.golang.org/adk/session"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCodeWorkspaceCloseCurrentRuntime(t *testing.T) {
	var cleaned atomic.Int32
	b := &localAgentBackend{result: &ChatFactoryResult{Cleanup: func() { cleaned.Add(1) }}}
	ctx, finish, err := b.ownCodeWork(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	joined := make(chan struct{})
	go func() { <-ctx.Done(); finish(); close(joined) }()
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-joined:
	case <-time.After(time.Second):
		t.Fatal("work not joined")
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	if cleaned.Load() != 1 {
		t.Fatalf("cleanup count %d", cleaned.Load())
	}
	if _, _, err := b.ownCodeWork(context.Background()); err == nil {
		t.Fatal("admitted work after close")
	}
}

func TestCodeWorkspaceLeaseReleaseAfterShutdown(t *testing.T) {
	dir := workspaceLaunchRepo(t)
	cfg := &CodeConfig{WorkingDir: dir, Workspace: backend.WorkspaceIndependent}
	w, err := resolveCodeWorkspace(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer w.close()
	entered, release := make(chan struct{}), make(chan struct{})
	b := &localAgentBackend{workspace: w, result: &ChatFactoryResult{Cleanup: func() { close(entered); <-release }}}
	done := make(chan error, 1)
	go func() { done <- b.Close() }()
	<-entered
	if _, err := resolveCodeWorkspace(context.Background(), &CodeConfig{WorkingDir: cfg.WorkingDir}); !errors.Is(err, gitutil.ErrWorkspaceBusy) {
		t.Fatalf("released before cleanup: %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	next, err := resolveCodeWorkspace(context.Background(), &CodeConfig{WorkingDir: cfg.WorkingDir})
	if err != nil {
		t.Fatal(err)
	}
	next.close()
}

func TestCodeWorkspaceLateTitleRejected(t *testing.T) {
	b := &localAgentBackend{sessionID: "old", title: "old title"}
	if !b.acceptCodeTitle("old", 0, "refined") {
		t.Fatal("current title rejected")
	}
	b.NewSession()
	if b.acceptCodeTitle("old", 0, "late") {
		t.Fatal("late title accepted")
	}
	b.mu.Lock()
	b.sessionID = "old"
	b.mu.Unlock()
	if b.acceptCodeTitle("old", 0, "same session stale generation") {
		t.Fatal("stale generation accepted")
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	if b.acceptCodeTitle("old", 1, "closed") {
		t.Fatal("closed title accepted")
	}
}

func workspaceLaunchRepo(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("SUDO_USER", "")
	dir := filepath.Join(home, "repo")
	if e := os.Mkdir(dir, 0700); e != nil {
		t.Fatal(e)
	}
	for _, args := range [][]string{{"init"}, {"config", "user.name", "Fixture"}, {"config", "user.email", "fixture@example.test"}} {
		workspaceLaunchGit(t, dir, args...)
	}
	if e := os.WriteFile(filepath.Join(dir, "file"), []byte("base\n"), 0600); e != nil {
		t.Fatal(e)
	}
	workspaceLaunchGit(t, dir, "add", "file")
	workspaceLaunchGit(t, dir, "commit", "-m", "base")
	return dir
}
func workspaceLaunchGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, e := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if e != nil {
		t.Fatalf("git %v: %s %v", args, out, e)
	}
	return strings.TrimSpace(string(out))
}
func TestCodeWorkspaceLaunchIsolationAndResume(t *testing.T) {
	dir := workspaceLaunchRepo(t)
	ctx := context.Background()
	cfg := &CodeConfig{WorkingDir: dir, Workspace: backend.WorkspaceIndependent}
	w, e := resolveCodeWorkspace(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer w.close()
	if cfg.WorkingDir == dir || w.record == nil || w.lease == nil {
		t.Fatalf("not isolated: %+v", cfg)
	}
	if e = os.WriteFile(filepath.Join(cfg.WorkingDir, "file"), []byte("private\n"), 0600); e != nil {
		t.Fatal(e)
	}
	original, _ := os.ReadFile(filepath.Join(dir, "file"))
	if string(original) != "base\n" {
		t.Fatal("original modified")
	}
	_, e = resolveCodeWorkspace(ctx, &CodeConfig{WorkingDir: cfg.WorkingDir})
	if !errors.Is(e, gitutil.ErrWorkspaceBusy) {
		t.Fatalf("second writer: %v", e)
	}
	app := &config.AppConfig{}
	storage, e := codeSessionsDir(app)
	if e != nil {
		t.Fatal(e)
	}
	store, e := persistentsession.NewFileStore(storage)
	if e != nil {
		t.Fatal(e)
	}
	created, e := store.Create(ctx, &adksession.CreateRequest{AppName: codeAppName, UserID: w.owner})
	if e != nil {
		t.Fatal(e)
	}
	b := &localAgentBackend{workspace: w, workingDir: cfg.WorkingDir, userID: w.owner, fileStore: store, sessionSvc: store}
	if e = b.bindCodeWorkspaceSession(ctx, created.Session.ID()); e != nil {
		t.Fatal(e)
	}
	w.close()
	resume := &CodeConfig{WorkingDir: dir, SessionID: created.Session.ID()}
	r, e := resolveCodeWorkspace(ctx, resume)
	if e != nil {
		t.Fatal(e)
	}
	defer r.close()
	if resume.WorkingDir != cfg.WorkingDir || r.owner != w.owner {
		t.Fatalf("wrong resume: %+v", resume)
	}
	child, e := store.Create(ctx, &adksession.CreateRequest{AppName: codeAppName, UserID: w.owner, State: map[string]any{persistentsession.StateKeyParentID: created.Session.ID()}})
	if e != nil {
		t.Fatal(e)
	}
	b.workspace, b.userID, b.workingDir = r, r.owner, resume.WorkingDir
	if _, e = b.ResumeSession(ctx, created.Session.ID()); e != nil {
		t.Fatal(e)
	}
	if b.sessionID != child.Session.ID() {
		t.Fatalf("resume did not select compacted tip: %s", b.sessionID)
	}
	if _, binding, e := r.manager.FindSession(ctx, r.project, child.Session.ID()); e != nil || binding.UserID != w.owner {
		t.Fatalf("compacted tip not bound: %+v %v", binding, e)
	}
	current, e := resolveCodeWorkspace(ctx, &CodeConfig{WorkingDir: dir})
	if e != nil {
		t.Fatal(e)
	}
	b.workspace = current
	b.userID = current.owner
	b.workingDir = dir
	list, e := b.ListSessions(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if len(list) != 1 || list[0].ID != created.Session.ID() {
		t.Fatalf("project discovery: %+v", list)
	}
	r.close() // A cross-workspace resume must not compete with an active writer.
	_, e = b.ResumeSession(ctx, created.Session.ID())
	var handoff *backend.WorkspaceHandoffError
	if !errors.As(e, &handoff) || handoff.Request.WorkingDir != cfg.WorkingDir {
		t.Fatalf("missing fresh handoff: %v", e)
	}
}
func TestCodeWorkspaceDirtyCopyChoices(t *testing.T) {
	dir := workspaceLaunchRepo(t)
	os.WriteFile(filepath.Join(dir, "file"), []byte("staged\n"), 0600)
	workspaceLaunchGit(t, dir, "add", "file")
	os.WriteFile(filepath.Join(dir, "file"), []byte("working\n"), 0600)
	os.WriteFile(filepath.Join(dir, "new"), []byte("untracked"), 0600)
	for _, copy := range []backend.WorkspaceCopyMode{backend.WorkspaceCommitted, backend.WorkspaceCurrentWork} {
		cfg := &CodeConfig{WorkingDir: dir, Workspace: backend.WorkspaceIndependent, WorkspaceCopy: copy}
		w, e := resolveCodeWorkspace(context.Background(), cfg)
		if e != nil {
			t.Fatal(e)
		}
		data, _ := os.ReadFile(filepath.Join(cfg.WorkingDir, "file"))
		want := "base\n"
		if copy == backend.WorkspaceCurrentWork {
			want = "working\n"
		}
		if string(data) != want {
			t.Fatalf("copy %s got %s", copy, data)
		}
		w.close()
	}
	if workspaceLaunchGit(t, dir, "diff", "--cached", "--numstat") == "" {
		t.Fatal("source index changed")
	}
}
func TestCodeWorkspaceNoGitCurrentAndOptions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	cfg := &CodeConfig{WorkingDir: home}
	w, e := resolveCodeWorkspace(context.Background(), cfg)
	if e != nil || w != nil {
		t.Fatalf("no git current: %v", e)
	}
	_, e = resolveCodeWorkspace(context.Background(), &CodeConfig{WorkingDir: home, Workspace: backend.WorkspaceIndependent})
	if !errors.Is(e, gitutil.ErrWorkspaceUnsupported) {
		t.Fatal(e)
	}
	if e = ValidateCodeWorkspaceOptions(backend.WorkspaceCurrent, "HEAD", ""); e == nil {
		t.Fatal("base accepted outside independent")
	}
}
func TestCodeWorkspaceStartupFailureRetainsAndReleases(t *testing.T) {
	dir := workspaceLaunchRepo(t)
	before, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(before) })
	configDir, err := config.GetConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(configDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte("[invalid yaml"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := &CodeConfig{WorkingDir: dir, Workspace: backend.WorkspaceIndependent}
	_, err = buildCodeBackend(context.Background(), cfg)
	if err == nil || !strings.Contains(err.Error(), "workspace retained at") || !strings.Contains(err.Error(), "recover with astonish code") {
		t.Fatalf("missing recovery message: %v", err)
	}
	manager, err := newLocalCodeWorkspaceManager()
	if err != nil {
		t.Fatal(err)
	}
	project, err := manager.ResolveProject(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	records, err := manager.List(context.Background(), project)
	if err != nil || len(records) != 1 || records[0].State != gitutil.WorkspaceRetained || records[0].LastError == "" {
		t.Fatalf("startup state: %+v %v", records, err)
	}
	lease, err := manager.AcquireLease(context.Background(), project, records[0].ID)
	if err != nil {
		t.Fatalf("startup leaked writer lease: %v", err)
	}
	defer lease.Close()
	if _, err := os.Stat(records[0].Root); err != nil {
		t.Fatalf("checkout deleted: %v", err)
	}
}

func TestCodeWorkspaceLegacyResumeAndLazyContract(t *testing.T) {
	dir := workspaceLaunchRepo(t)
	ctx := context.Background()
	cfg := &CodeConfig{WorkingDir: dir}
	w, err := resolveCodeWorkspace(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer w.close()
	storage, err := codeSessionsDir(&config.AppConfig{})
	if err != nil {
		t.Fatal(err)
	}
	store, err := persistentsession.NewFileStore(storage)
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.Create(ctx, &adksession.CreateRequest{AppName: codeAppName, UserID: codeUserIDForDir(dir)})
	if err != nil {
		t.Fatal(err)
	}
	b := &localAgentBackend{workspace: w, workingDir: dir, userID: w.owner, fileStore: store, sessionSvc: store}
	lazy := &lazyCodeBackend{inner: b}
	req, err := lazy.PrepareWorkspaceLaunch(ctx, backend.WorkspaceLaunchRequest{SessionID: created.Session.ID()})
	if err != nil || req.WorkingDir != dir {
		t.Fatalf("legacy request: %+v %v", req, err)
	}
	if _, err = b.ResumeSession(ctx, created.Session.ID()); err != nil {
		t.Fatal(err)
	}
	resumeCfg := &CodeConfig{WorkingDir: dir, SessionID: created.Session.ID()}
	legacy, err := resolveCodeWorkspace(ctx, resumeCfg)
	if err != nil || legacy.owner != codeUserIDForDir(dir) {
		t.Fatalf("legacy identity changed: %+v %v", legacy, err)
	}
	defer legacy.close()
	req, err = lazy.PrepareWorkspaceLaunch(ctx, backend.WorkspaceLaunchRequest{Mode: backend.WorkspaceIndependent})
	if err != nil || req.WorkingDir == dir || req.Mode != backend.WorkspaceCurrent {
		t.Fatalf("lazy independent launch: %+v %v", req, err)
	}
}

func TestCodeWorkspaceManagementAdapter(t *testing.T) {
	dir := workspaceLaunchRepo(t)
	ctx := context.Background()
	cfg := &CodeConfig{WorkingDir: dir}
	w, err := resolveCodeWorkspace(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer w.close()
	local := &localAgentBackend{workspace: w, workingDir: dir}
	lazy := &lazyCodeBackend{inner: local}
	req, err := lazy.PrepareWorkspaceLaunch(ctx, backend.WorkspaceLaunchRequest{Mode: backend.WorkspaceIndependent})
	if err != nil {
		t.Fatal(err)
	}
	opened, err := lazy.PrepareWorkspaceLaunch(ctx, backend.WorkspaceLaunchRequest{Mode: backend.WorkspaceCurrent, WorkspaceID: req.WorkspaceID, WorkingDir: dir})
	if err != nil || opened.WorkingDir != req.WorkingDir {
		t.Fatalf("workspace ID resume: %+v %v", opened, err)
	}
	write := func(root, path, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, path), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(req.WorkingDir, "file", "private\n")
	write(req.WorkingDir, "binary", "\x00\x01private")
	write(dir, "unrelated", "staged\n")
	workspaceLaunchGit(t, dir, "add", "unrelated")
	index := workspaceLaunchGit(t, dir, "write-tree")
	review, err := lazy.ReviewCodeWorkspace(ctx, req.WorkspaceID)
	if err != nil || len(review.Files) != 2 || len(review.Conflicts) != 0 || review.Digest == "" || !strings.Contains(review.Summary, w.project.OriginalRoot) {
		t.Fatalf("review DTO: %+v %v", review, err)
	}
	write(req.WorkingDir, "file", "changed after review\n")
	if err := lazy.ApplyCodeWorkspace(ctx, review); !errors.Is(err, gitutil.ErrWorkspaceStale) {
		t.Fatalf("stale apply: %v", err)
	}
	review, err = lazy.ReviewCodeWorkspace(ctx, req.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if err := lazy.ApplyCodeWorkspace(ctx, review); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"file", "binary"} {
		got, err := os.ReadFile(filepath.Join(dir, path))
		if err != nil {
			t.Fatal(err)
		}
		want, err := os.ReadFile(filepath.Join(req.WorkingDir, path))
		if err != nil || string(got) != string(want) {
			t.Fatalf("apply %s: %q %q %v", path, got, want, err)
		}
	}
	if got := workspaceLaunchGit(t, dir, "write-tree"); got != index {
		t.Fatal("apply changed target staging")
	}
	write(dir, "file", "conflict\n")
	write(req.WorkingDir, "file", "new private\n")
	review, err = lazy.ReviewCodeWorkspace(ctx, req.WorkspaceID)
	if err != nil || len(review.Conflicts) != 1 {
		t.Fatalf("conflict DTO: %+v %v", review, err)
	}
	if err := lazy.ApplyCodeWorkspace(ctx, review); !errors.Is(err, gitutil.ErrWorkspaceConflict) {
		t.Fatalf("conflict apply: %v", err)
	}
	lease, err := w.manager.AcquireLease(ctx, w.project, req.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lazy.PrepareWorkspaceLaunch(ctx, backend.WorkspaceLaunchRequest{Mode: backend.WorkspaceCurrent, WorkspaceID: req.WorkspaceID}); !errors.Is(err, gitutil.ErrWorkspaceBusy) {
		t.Fatalf("busy resume: %v", err)
	}
	list, err := lazy.ListCodeWorkspaces(ctx)
	if err != nil || len(list) != 2 || !list[1].Active {
		t.Fatalf("active listing: %+v %v", list, err)
	}
	if err := lazy.CreateCodeWorkspaceBranch(ctx, req.WorkspaceID, "task"); !errors.Is(err, gitutil.ErrWorkspaceBusy) {
		t.Fatalf("busy branch: %v", err)
	}
	if err := lazy.RemoveCodeWorkspace(ctx, req.WorkspaceID); !errors.Is(err, gitutil.ErrWorkspaceBusy) {
		t.Fatalf("busy remove: %v", err)
	}
	_ = lease.Close()
	if err := lazy.CreateCodeWorkspaceBranch(ctx, req.WorkspaceID, "task"); err != nil {
		t.Fatal(err)
	}
	list, err = lazy.ListCodeWorkspaces(ctx)
	if err != nil || list[1].Label != "task" || list[1].Active {
		t.Fatalf("branch listing: %+v %v", list, err)
	}
	if err := lazy.RemoveCodeWorkspace(ctx, req.WorkspaceID); !errors.Is(err, gitutil.ErrWorkspaceConflict) {
		t.Fatalf("dirty removal: %v", err)
	}
	if err := lazy.RecoverCodeWorkspaceApply(ctx); err != nil {
		t.Fatal(err)
	}
	if err := lazy.ApplyCodeWorkspace(ctx, backend.WorkspaceReview{WorkspaceID: req.WorkspaceID}); err == nil {
		t.Fatal("unreviewed apply accepted")
	}
}

func TestCodeWorkspaceManagementRemovalAdapter(t *testing.T) {
	dir := workspaceLaunchRepo(t)
	ctx := context.Background()
	w, err := resolveCodeWorkspace(ctx, &CodeConfig{WorkingDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer w.close()
	lazy := &lazyCodeBackend{inner: &localAgentBackend{workspace: w, workingDir: dir}}
	req, err := lazy.PrepareWorkspaceLaunch(ctx, backend.WorkspaceLaunchRequest{Mode: backend.WorkspaceIndependent})
	if err != nil {
		t.Fatal(err)
	}
	if err := lazy.RemoveCodeWorkspace(ctx, req.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	list, err := lazy.ListCodeWorkspaces(ctx)
	if err != nil || len(list) != 2 || list[1].State != string(gitutil.WorkspaceRemoved) {
		t.Fatalf("removed registry: %+v %v", list, err)
	}
	if _, err := os.Stat(req.WorkingDir); !os.IsNotExist(err) {
		t.Fatalf("checkout retained: %v", err)
	}
}

func TestCodeWorkspaceManagementUnavailable(t *testing.T) {
	ctx := context.Background()
	for _, b := range []backend.CodeWorkspaceManagementBackend{&localAgentBackend{}, &lazyCodeBackend{}} {
		if _, err := b.ReviewCodeWorkspace(ctx, "id"); err == nil {
			t.Fatal("review unavailable accepted")
		}
		if err := b.ApplyCodeWorkspace(ctx, backend.WorkspaceReview{}); err == nil {
			t.Fatal("apply unavailable accepted")
		}
		if err := b.CreateCodeWorkspaceBranch(ctx, "id", "branch"); err == nil {
			t.Fatal("branch unavailable accepted")
		}
		if err := b.RemoveCodeWorkspace(ctx, "id"); err == nil {
			t.Fatal("remove unavailable accepted")
		}
	}
	if err := (&lazyCodeBackend{}).RecoverCodeWorkspaceApply(ctx); err == nil {
		t.Fatal("recovery unavailable accepted")
	}
}

func TestCodeWorkspaceHandoffAllowlist(t *testing.T) {
	dir := t.TempDir()
	args, e := codeWorkspaceHandoffArgs(&CodeConfig{Model: "model", Provider: "fixture", AutoApprove: true}, backend.WorkspaceLaunchRequest{WorkingDir: dir, SessionID: "session", Mode: backend.WorkspaceIndependent, BaseRef: "ignored"})
	if e != nil {
		t.Fatal(e)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--workspace current") || strings.Contains(joined, "ignored") || !strings.Contains(joined, "--yolo") {
		t.Fatal(args)
	}
}
