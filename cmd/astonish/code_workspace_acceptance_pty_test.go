package astonish

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/gitutil"
)

// This is a composed real-terminal acceptance, not a reducer simulation. The
// loopback provider controls native shell calls; all checkouts, indexes, commits,
// session discovery and management actions go through production Code/Git.
func TestCodeWorkspaceIndependentPTYAcceptance(t *testing.T) {
	cache := exec.Command("go", "env", "GOMODCACHE", "GOCACHE", "GOPATH")
	out, err := cache.Output()
	if err != nil {
		t.Fatal(err)
	}
	values := strings.Fields(string(out))
	f := newSidebarPTYFixture(t)
	for i, key := range []string{"GOMODCACHE", "GOCACHE", "GOPATH"} {
		t.Setenv(key, values[i])
	}
	binary := filepath.Join(t.TempDir(), "astonish")
	build := exec.Command("go", "build", "-o", binary, ".")
	build.Dir = filepath.Join("..", "..")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %s %v", out, err)
	}
	git := func(root string, args ...string) []byte {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s %v", args, out, err)
		}
		return out
	}
	write := func(root, name string, data []byte) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	check := func(root, name string, want []byte) {
		t.Helper()
		got, err := os.ReadFile(filepath.Join(root, name))
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("%s/%s = %q, want %q: %v", root, name, got, want, err)
		}
	}
	git(f.work, "init")
	git(f.work, "config", "user.name", "Fixture")
	git(f.work, "config", "user.email", "fixture@example.invalid")
	for _, name := range []string{"task.txt", "unrelated.txt", "unstaged.txt"} {
		write(f.work, name, []byte("original\n"))
	}
	write(f.work, "data.bin", []byte{0, 1, 2, 255})
	write(f.work, "AGENTS.md", []byte("WORKSPACE GUIDANCE SENTINEL: keep task edits local.\n"))
	git(f.work, "add", ".")
	git(f.work, "commit", "-m", "baseline")
	// Both clean-base workspaces must leave staged, unstaged and untracked source
	// work alone. Compare raw index bytes, not just the human status rendering.
	write(f.work, "unrelated.txt", []byte("staged source\n"))
	git(f.work, "add", "unrelated.txt")
	write(f.work, "unstaged.txt", []byte("unstaged source\n"))
	write(f.work, "untracked.txt", []byte("untracked source\n"))
	sourceHead := git(f.work, "rev-parse", "HEAD")
	sourceStatus := git(f.work, "status", "--porcelain=v1", "-z")
	indexPath := filepath.Join(f.work, ".git", "index")
	sourceIndex, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	start := func(mode string, extra ...string) *handoffPTY {
		t.Helper()
		args := append([]string{"code", "--model", "fixture:pty-model", "--dir", f.work, "--workspace", mode, "--yolo"}, extra...)
		cmd := exec.Command(binary, args...)
		cmd.Dir = f.work
		cmd.Env = append(os.Environ(), "TERM=xterm-256color", "NO_COLOR=1", "PATH="+f.bin+string(os.PathListSeparator)+os.Getenv("PATH"))
		term := newHandoffPTYSize(t, cmd, 120, 30)
		term.mu.Lock()
		term.screen = newSidebarPTYScreen(120, 30)
		term.screen.p.Parse([]byte(term.raw.String()))
		term.mu.Unlock()
		return term
	}
	shell := func(term *handoffPTY, marker, command string) {
		t.Helper()
		term.send(marker + " using shell command.\r")
		f.turn("shell_command", map[string]any{"command": command, "timeout": 10})
		codeRevisionPTYReply(t, f, sidebarPTYModelTurn{text: marker + " FINISHED"})
		term.wait(marker+" FINISHED", "Enter send")
	}
	ctx := context.Background()
	dir, err := config.GetConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := gitutil.NewCodeWorkspaceManager(filepath.Join(dir, "code-workspaces"))
	if err != nil {
		t.Fatal(err)
	}
	project, err := manager.ResolveProject(ctx, f.work)
	if err != nil {
		t.Fatal(err)
	}
	records := func(count int) []gitutil.WorkspaceRecord {
		t.Helper()
		rows, err := manager.List(ctx, project)
		if err != nil || len(rows) != count {
			t.Fatalf("registry rows=%+v: %v", rows, err)
		}
		return rows
	}
	a := start("independent")
	a.wait("Enter send")
	shell(a, "TASK A", "printf 'task A\\n' > task.txt; printf '\\000\\003\\376' > data.bin; git add task.txt data.bin; git -c core.hooksPath=/dev/null commit -m task-A; pwd")
	first := records(1)[0]
	b := start("independent")
	b.wait("Enter send")
	shell(b, "TASK B", "printf 'task B\\n' > task.txt; git add task.txt; git -c core.hooksPath=/dev/null commit -m task-B; pwd")
	rows := records(2)
	var second gitutil.WorkspaceRecord
	for _, r := range rows {
		if r.ID != first.ID {
			second = r
		}
	}
	if first.Root == second.Root || a.cmd.Process.Pid == b.cmd.Process.Pid {
		t.Fatal("workspaces/processes are not distinct")
	}
	for _, r := range rows {
		if string(bytes.TrimSpace(git(r.Root, "rev-parse", "--abbrev-ref", "HEAD"))) != "HEAD" {
			t.Fatal("workspace not detached")
		}
		check(r.Root, "unrelated.txt", []byte("original\n"))
		check(r.Root, "unstaged.txt", []byte("original\n"))
		if _, err := os.Stat(filepath.Join(r.Root, "untracked.txt")); !os.IsNotExist(err) {
			t.Fatalf("clean base copied untracked data: %v", err)
		}
		if len(r.Sessions) != 1 {
			t.Fatalf("session binding: %+v", r)
		}
	}
	check(first.Root, "task.txt", []byte("task A\n"))
	check(second.Root, "task.txt", []byte("task B\n"))
	check(f.work, "task.txt", []byte("original\n"))
	check(f.work, "data.bin", []byte{0, 1, 2, 255})
	if !bytes.Equal(sourceHead, git(f.work, "rev-parse", "HEAD")) || !bytes.Equal(sourceStatus, git(f.work, "status", "--porcelain=v1", "-z")) {
		t.Fatal("private commits changed original HEAD/status")
	}
	currentIndex, err := os.ReadFile(indexPath)
	if err != nil || !bytes.Equal(sourceIndex, currentIndex) {
		t.Fatalf("private commits changed original index: %v", err)
	}
	// Original-project picker sees BOTH independently owned sessions while active.
	original := start("current")
	original.wait("Enter send")
	original.send("/sessions\r")
	original.wait("Sessions", first.Sessions[0].SessionID[:8], second.Sessions[0].SessionID[:8], "independent")
	original.send("\x1b")
	original.wait("Enter send")
	busy := start("current", "--resume", first.Sessions[0].SessionID)
	busy.wait("busy or unavailable")
	busy.close()
	if _, err := manager.ReviewRemoval(ctx, project, first.ID); !errors.Is(err, gitutil.ErrWorkspaceBusy) {
		t.Fatalf("active removal review: %v", err)
	}
	a.close()
	b.close()
	// Abrupt process death releases leases, and explicit resume rehydrates the
	// saved transcript BEFORE a native tool writes in its recorded physical root.
	for i, r := range []gitutil.WorkspaceRecord{first, second} {
		resumed := start("current", "--resume", r.Sessions[0].SessionID)
		resumed.wait("Enter send", fmt.Sprintf("TASK %c FINISHED", 'A'+i))
		shell(resumed, fmt.Sprintf("RESUME %d", i), "pwd; test -f AGENTS.md && printf 'resumed\\n' > resume.marker")
		check(r.Root, "resume.marker", []byte("resumed\n"))
		if _, err := os.Stat(filepath.Join(f.work, "resume.marker")); !os.IsNotExist(err) {
			t.Fatal("resume wrote original")
		}
		resumed.close()
		// Remove only the acceptance marker, not task work or user data.
		if err := os.Remove(filepath.Join(r.Root, "resume.marker")); err != nil {
			t.Fatal(err)
		}
	}
	// Management uses real picker confirmations. First apply A; unrelated staged
	// and unstaged source edits and binary bytes must survive intact.
	openDetail := func(id string) {
		t.Helper()
		original.send("/workspaces\r")
		original.wait("Code workspaces", "detached · independent · ready")
		list := records(2)
		for i, r := range list {
			if r.ID == id {
				for j := 0; j <= i; j++ {
					original.send("\x1b[B")
				}
				original.send("\r")
				original.wait("Review changes", "Create branch", "Remove checkout")
				return
			}
		}
		t.Fatal("workspace absent")
	}
	openDetail(first.ID)
	original.send("\x1b[B\r")
	original.wait("2 paths", "0 conflicts", "task.txt", "data.bin")
	original.send("\r")
	original.wait("Preserve current staging")
	original.send("\r")
	original.wait("Changes applied to original")
	check(f.work, "task.txt", []byte("task A\n"))
	check(f.work, "data.bin", []byte{0, 3, 254})
	check(f.work, "unrelated.txt", []byte("staged source\n"))
	check(f.work, "unstaged.txt", []byte("unstaged source\n"))
	check(f.work, "untracked.txt", []byte("untracked source\n"))
	currentIndex, err = os.ReadFile(indexPath)
	if err != nil || !bytes.Equal(sourceIndex, currentIndex) {
		t.Fatalf("apply changed original index: %v", err)
	}
	if !bytes.Equal(sourceHead, git(f.work, "rev-parse", "HEAD")) {
		t.Fatal("apply created original commit")
	}
	original.send("\x1b")
	original.wait("Remove checkout")
	original.send("\x1b")
	original.wait("independent · ready")
	original.send("\x1b")
	original.wait("Enter send")
	// B now conflicts on the same path: confirm must not write even a subset.
	openDetail(second.ID)
	original.send("\x1b[B\r")
	original.wait("1 conflicts", "Conflict: task.txt")
	original.send("\r")
	original.wait("Conflicts must be resolved")
	check(f.work, "task.txt", []byte("task A\n"))
	check(second.Root, "task.txt", []byte("task B\n"))
	original.send("\x1b")
	original.wait("Remove checkout")
	original.send("\x1b")
	original.wait("independent · ready")
	original.send("\x1b")
	original.wait("Enter send")
	// An unmerged detached commit is retained. A named local branch plus the
	// apply receipt makes A explicitly removable without deleting its history.
	review, err := manager.ReviewRemoval(ctx, project, second.ID)
	if err != nil || review.Eligible {
		t.Fatalf("unmerged B should be retained: %+v %v", review, err)
	}
	openDetail(first.ID)
	original.send("\x1b[B\x1b[B\r")
	original.wait("Branch name:")
	for _, ch := range "accepted-task-a" {
		original.send(string(ch))
	}
	original.send("\r")
	original.wait("Branch created")
	aHead := git(first.Root, "rev-parse", "HEAD")
	if !bytes.Equal(aHead, git(f.work, "rev-parse", "accepted-task-a")) {
		t.Fatal("branch lost detached commit")
	}
	original.send("\x1b[B\r")
	original.wait("Transcript and branch retained")
	original.send("\x1b")
	original.wait("Remove checkout")
	if _, err := os.Stat(first.Root); err != nil {
		t.Fatal("cancel removed workspace")
	}
	original.send("\x1b[B\x1b[B\x1b[B\r")
	original.wait("Transcript and branch retained")
	original.send("\r")
	original.wait("Checkout removed")
	if _, err := os.Stat(first.Root); !os.IsNotExist(err) {
		t.Fatalf("checkout not removed: %v", err)
	}
	if !bytes.Equal(aHead, git(f.work, "rev-parse", "accepted-task-a")) {
		t.Fatal("remove deleted branch")
	}
	original.send("\x1b")
	original.wait("independent")
	original.send("\x1b")
	original.wait("Enter send")
	original.send("/sessions\r")
	original.wait("Sessions", first.Sessions[0].SessionID[:8], second.Sessions[0].SessionID[:8])
	original.close()
	// Capture-current is explicit and copies source staging separately from the
	// working tree. Rollback in that physical checkout must restore only it.
	captured := start("independent", "--workspace-copy", "current-work")
	captured.wait("Enter send")
	all := records(3)
	var capture gitutil.WorkspaceRecord
	for _, r := range all {
		if r.ID != first.ID && r.ID != second.ID {
			capture = r
		}
	}
	check(capture.Root, "unrelated.txt", []byte("staged source\n"))
	check(capture.Root, "unstaged.txt", []byte("unstaged source\n"))
	check(capture.Root, "untracked.txt", []byte("untracked source\n"))
	if !bytes.Equal(git(f.work, "show", ":unrelated.txt"), git(capture.Root, "show", ":unrelated.txt")) {
		t.Fatal("capture lost source staging")
	}
	captured.send("ROLLBACK PRIVATE using write_file.\r")
	f.turn("write_file", map[string]any{"path": filepath.Join(capture.Root, "task.txt"), "content": "rollback-only\n"})
	codeRevisionPTYReply(t, f, sidebarPTYModelTurn{text: "ROLLBACK PRIVATE FINISHED"})
	captured.wait("ROLLBACK PRIVATE FINISHED")
	captured.wait("Enter send")
	check(capture.Root, "task.txt", []byte("rollback-only\n"))
	captured.send("/rollback\r")
	captured.wait("Rollback", "ROLLBACK PRIVATE")
	captured.send("\r")
	captured.wait("Revert to this message?")
	captured.send("y")
	captured.wait("Rolled back.")
	check(capture.Root, "task.txt", []byte("task A\n"))
	check(f.work, "task.txt", []byte("task A\n"))
	check(second.Root, "task.txt", []byte("task B\n"))
	captured.close()
	// Explicit resume without the CLI override exposes project-relative
	// approvals instead of restoring the previous process's --yolo authority.
	// Registry enumeration is not an ordering contract; locate the capture.
	for _, r := range records(3) {
		if r.ID != first.ID && r.ID != second.ID {
			capture = r
		}
	}
	standard := start("current", "--resume", capture.Sessions[0].SessionID, "--yolo=false")
	standard.wait("Enter send")
	standard.send("/permissions\r")
	standard.wait("Permissions", "Ask outside project", "Ask when needed", "No active plan")
	standard.assertAbsent("CLI override")
	standard.close()
	t.Log("Two live Code processes committed different edits to one path; original HEAD/index/dirty files unchanged; project discovery, writer exclusion, death/reopen, binary/text confirmed apply, conflict refusal, branch retention and confirmed/cancelled removal proven.")
}
