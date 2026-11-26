package astonish

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/gitutil"
	persistentsession "github.com/SAP/astonish/pkg/session"
)

// This acceptance drives the actual Code CLI, native shell, Git worktree and
// exec-replaced terminal. No reducer events or checkout deletion are injected.
func TestCodeWorkspaceSessionDeletePTYAcceptance(t *testing.T) {
	f := newSidebarPTYFixture(t)
	ctx := context.Background()
	git := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", f.work}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s: %v", args, out, err)
		}
		return strings.TrimSpace(string(out))
	}
	git("init")
	git("config", "user.name", "Fixture")
	git("config", "user.email", "fixture@example.test")
	for name, data := range map[string]string{"file": "original\n", ".gitignore": "ignored.marker\n"} {
		if err := os.WriteFile(filepath.Join(f.work, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	git("add", ".")
	git("commit", "-m", "base")
	git("branch", "preserved-feature")
	branchesBefore := git("show-ref", "--heads")
	originalHEAD := git("rev-parse", "HEAD")

	cmd := exec.Command(os.Args[0], "-test.run=^TestBotCLIProcess$", "--", "code", "--model", "fixture:pty-model", "--dir", f.work, "--workspace", "independent", "--yolo")
	cmd.Dir = f.work
	cmd.Env = append(os.Environ(), "ASTONISH_BOT_CLI_CHILD=1", "ASTONISH_WORKSPACE_HANDOFF_CHILD=1", "TERM=xterm-256color", "NO_COLOR=1", "PATH="+f.bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	term := newHandoffPTYSize(t, cmd, 180, 32)
	term.mu.Lock()
	term.screen = newSidebarPTYScreen(180, 32)
	term.screen.p.Parse([]byte(term.raw.String()))
	term.mu.Unlock()
	term.wait("Enter send")
	term.send("Create private workspace changes using the shell tool.\r")
	f.turn("shell_command", map[string]any{"command": "printf 'private committed\\n' > file; git add file; git -c user.name=Fixture -c user.email=fixture@example.test commit -m private; printf 'dirty\\n' > file; printf 'untracked\\n' > untracked.marker; printf 'ignored\\n' > ignored.marker; pwd", "timeout": 10})
	codeRevisionPTYReply(t, f, sidebarPTYModelTurn{text: "PRIVATE DELETION FIXTURE READY"})
	term.wait("PRIVATE DELETION FIXTURE READY")

	configDir, err := config.GetConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	configDir, err = filepath.EvalSymlinks(configDir)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := gitutil.NewCodeWorkspaceManager(filepath.Join(configDir, "code-workspaces"))
	if err != nil {
		t.Fatal(err)
	}
	project, err := manager.ResolveProject(ctx, f.work)
	if err != nil {
		t.Fatal(err)
	}
	records, err := manager.List(ctx, project)
	if err != nil || len(records) != 1 || len(records[0].Sessions) != 1 {
		t.Fatalf("expected one durably bound workspace: %+v: %v", records, err)
	}
	record := records[0]
	binding := record.Sessions[0]
	store, err := persistentsession.NewFileStore(f.storePath)
	if err != nil {
		t.Fatal(err)
	}
	sessionBase := filepath.Join(f.storePath, binding.AppName, binding.UserID, binding.SessionID)
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	// Seed the persisted plan through the real shell, not a mock deletion path.
	term.send("Persist the deletion fixture plan using the shell tool.\r")
	f.turn("shell_command", map[string]any{"command": "printf '# Deletion fixture plan\\n' > " + quote(sessionBase+".PLAN.md"), "timeout": 10})
	codeRevisionPTYReply(t, f, sidebarPTYModelTurn{text: "DELETION PLAN READY"})
	term.wait("DELETION PLAN READY")
	assertPresent := func() {
		t.Helper()
		for _, path := range []string{record.Root, filepath.Join(record.Root, "file"), filepath.Join(record.Root, "untracked.marker"), filepath.Join(record.Root, "ignored.marker"), sessionBase + ".jsonl", sessionBase + ".PLAN.md"} {
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("cancel must preserve %s: %v", path, err)
			}
		}
		for name, want := range map[string]string{"file": "dirty\n", "untracked.marker": "untracked\n", "ignored.marker": "ignored\n"} {
			data, err := os.ReadFile(filepath.Join(record.Root, name))
			if err != nil || string(data) != want {
				t.Fatalf("cancel changed private file %s: %q: %v", name, data, err)
			}
		}
		meta, err := store.GetSessionMeta(binding.SessionID)
		if err != nil || meta == nil {
			t.Fatalf("cancel lost session metadata: %+v: %v", meta, err)
		}
		got, err := manager.List(ctx, project)
		if err != nil || len(got) != 1 || len(got[0].Sessions) != 1 || got[0].Sessions[0].SessionID != binding.SessionID {
			t.Fatalf("cancel lost workspace binding: %+v: %v", got, err)
		}
	}
	assertPresent()
	if out := git("worktree", "list", "--porcelain"); !strings.Contains(out, record.Root) {
		t.Fatalf("private checkout missing from Git: %s", out)
	}
	term.send("/sessions\r")
	term.wait("Sessions", binding.SessionID[:8], "independent", "d delete")
	term.send("d")
	warning := []string{"Delete this session and its dedicated workspace?", "permanently discards workspace files", "uncommitted changes", "detached commits", "original checkout and named branches are preserved", "Shared workspaces are retained", "Code returns to the original checkout", "Esc to cancel"}
	term.wait(warning...)
	term.send("\x1b")
	term.wait("Sessions", binding.SessionID[:8], "d delete")
	term.assertAbsent("Delete this session and its dedicated workspace?")
	assertPresent()
	term.send("d")
	term.wait(warning...)
	term.send("y")
	// Waiting for a fresh-model request below also proves the new process is
	// interactive; a stale pre-handoff Enter-send frame is not sufficient.
	term.wait("Enter send")
	term.send("Prove the fresh original checkout with the shell tool.\r")
	f.turn("shell_command", map[string]any{"command": "printf 'fresh original\\n' > fresh-original.marker; pwd", "timeout": 10})
	codeRevisionPTYReply(t, f, sidebarPTYModelTurn{text: "FRESH ORIGINAL WRITE FINISHED"})
	term.wait("FRESH ORIGINAL WRITE FINISHED")
	data, err := os.ReadFile(filepath.Join(f.work, "fresh-original.marker"))
	if err != nil || string(data) != "fresh original\n" {
		t.Fatalf("handoff shell did not write original checkout: %q: %v", data, err)
	}
	for _, path := range []string{record.Root, sessionBase + ".jsonl", sessionBase + ".PLAN.md"} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("confirmed deletion retained %s: %v", path, err)
		}
	}
	// Reopen stores so in-memory index caches cannot masquerade as deletion.
	store, err = persistentsession.NewFileStore(f.storePath)
	if err != nil {
		t.Fatal(err)
	}
	meta, err := store.GetSessionMeta(binding.SessionID)
	if meta != nil || err == nil || err.Error() != "session "+binding.SessionID+" not found" {
		t.Fatalf("deleted session metadata retained: %+v: %v", meta, err)
	}
	records, err = manager.List(ctx, project)
	if err != nil || len(records) != 0 {
		t.Fatalf("deleted workspace metadata retained: %+v: %v", records, err)
	}
	if out := git("worktree", "list", "--porcelain"); strings.Contains(out, record.Root) {
		t.Fatalf("Git retained deleted checkout: %s", out)
	}
	if after := git("show-ref", "--heads"); after != branchesBefore {
		t.Fatalf("named branches changed: before %s; after %s", branchesBefore, after)
	}
	if after := git("rev-parse", "HEAD"); after != originalHEAD {
		t.Fatalf("original HEAD changed: %s -> %s", originalHEAD, after)
	}
	data, err = os.ReadFile(filepath.Join(f.work, "file"))
	if err != nil || string(data) != "original\n" {
		t.Fatalf("original checkout changed: %q: %v", data, err)
	}
	for _, name := range []string{"ignored.marker", "untracked.marker"} {
		if _, err := os.Stat(filepath.Join(f.work, name)); !os.IsNotExist(err) {
			t.Fatalf("private file escaped into original: %s: %v", name, err)
		}
	}
	term.close()
	t.Log(fmt.Sprintf("Real Code PTY canceled safely, deleted active dirty/ignored checkout %s and session %s, preserved branches and handed shell tools to the original checkout", record.Root, binding.SessionID))
}
