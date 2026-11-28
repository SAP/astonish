package astonish

import (
	"context"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/gitutil"
	"github.com/SAP/astonish/pkg/launcher"
	"github.com/SAP/astonish/pkg/tui/backend"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Only the dedicated handoff fixture dispatches positional Code arguments in
// the exec-replaced test executable. Production handoff remains unchanged.
func init() {
	if os.Getenv("ASTONISH_WORKSPACE_HANDOFF_CHILD") == "1" && len(os.Args) > 1 && os.Args[1] == "code" {
		if err := Execute(); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
}

func TestCodeWorkspaceHandoffProcess(t *testing.T) {
	if os.Getenv("ASTONISH_WORKSPACE_HANDOFF_CHILD") != "1" {
		return
	}
	request := backend.WorkspaceLaunchRequest{WorkingDir: os.Getenv("ASTONISH_WORKSPACE_HANDOFF_DIR"), SessionID: os.Getenv("ASTONISH_WORKSPACE_HANDOFF_SESSION")}
	cfg := &launcher.CodeConfig{Provider: "fixture", Model: "pty-model", AutoApprove: true}
	if err := launcher.RunCodeWorkspaceHandoff(context.Background(), cfg, request); err != nil {
		t.Fatal(err)
	}
}

// Real Code CLI process and terminal, controlled loopback model, and native
// shell tool. No injected reducer events or mock checkout operations.
func TestCodeWorkspaceLaunchPTYAcceptance(t *testing.T) {
	f := newSidebarPTYFixture(t)
	git := func(args ...string) {
		t.Helper()
		out, e := exec.Command("git", append([]string{"-C", f.work}, args...)...).CombinedOutput()
		if e != nil {
			t.Fatalf("git %v: %s %v", args, out, e)
		}
	}
	git("init")
	git("config", "user.name", "Fixture")
	git("config", "user.email", "fixture@example.test")
	if e := os.WriteFile(filepath.Join(f.work, "file"), []byte("original\n"), 0600); e != nil {
		t.Fatal(e)
	}
	git("add", "file")
	git("commit", "-m", "base")
	start := func(mode string, extra ...string) *handoffPTY {
		args := append([]string{"-test.run=^TestBotCLIProcess$", "--", "code", "--model", "fixture:pty-model", "--dir", f.work, "--workspace", mode, "--yolo"}, extra...)
		cmd := exec.Command(os.Args[0], args...)
		cmd.Dir = f.work
		cmd.Env = append(os.Environ(), "ASTONISH_BOT_CLI_CHILD=1", "TERM=xterm-256color", "NO_COLOR=1", "PATH="+f.bin+string(os.PathListSeparator)+os.Getenv("PATH"))
		return newHandoffPTY(t, cmd)
	}
	term := start("independent")
	term.wait("Enter send")
	term.send("Write a private marker with the shell tool.\r")
	f.turn("shell_command", map[string]any{"command": "printf 'private\\n' > file; pwd", "timeout": 10})
	codeRevisionPTYReply(t, f, sidebarPTYModelTurn{text: "PRIVATE WRITE FINISHED"})
	term.wait("PRIVATE WRITE FINISHED")
	term.close()
	configDir, _ := config.GetConfigDir()
	configDir, _ = filepath.EvalSymlinks(configDir)
	manager, e := gitutil.NewCodeWorkspaceManager(filepath.Join(configDir, "code-workspaces"))
	if e != nil {
		t.Fatal(e)
	}
	p, e := manager.ResolveProject(context.Background(), f.work)
	if e != nil {
		t.Fatal(e)
	}
	records, e := manager.List(context.Background(), p)
	if e != nil {
		t.Fatal(e)
	}
	if len(records) != 1 {
		t.Fatalf("expected one checkout: %+v", records)
	}
	data, e := os.ReadFile(filepath.Join(records[0].Root, "file"))
	if e != nil || string(data) != "private\n" {
		t.Fatalf("private tool cwd: %q %v", data, e)
	}
	data, _ = os.ReadFile(filepath.Join(f.work, "file"))
	if string(data) != "original\n" {
		t.Fatal("independent launch modified source")
	}
	if len(records[0].Sessions) == 0 {
		t.Fatal("session not durably bound")
	}
	// Resume from the original checkout: initialization must resolve the saved
	// destination before wiring tools. The real shell writes there again.
	term = start("current", "--resume", records[0].Sessions[0].SessionID)
	term.wait("Enter send")
	term.send("Write the resume marker with the shell tool.\r")
	f.turn("shell_command", map[string]any{"command": "printf 'resumed\\n' > resume.marker; pwd", "timeout": 10})
	codeRevisionPTYReply(t, f, sidebarPTYModelTurn{text: "RESUME WRITE FINISHED"})
	term.wait("RESUME WRITE FINISHED")
	data, e = os.ReadFile(filepath.Join(records[0].Root, "resume.marker"))
	if e != nil || string(data) != "resumed\n" {
		t.Fatalf("resumed tool cwd: %q %v", data, e)
	}
	if _, e = os.Stat(filepath.Join(f.work, "resume.marker")); !os.IsNotExist(e) {
		t.Fatalf("resume wrote original: %v", e)
	}
	// A second real process cannot open a checkout with an active writer.
	busy := start("current", "--resume", records[0].Sessions[0].SessionID)
	busy.wait("busy or unavailable")
	busy.close()
	term.close()
	term = start("current")
	term.wait("Enter send")
	term.close()
	records, e = manager.List(context.Background(), p)
	if e != nil || len(records) != 1 {
		t.Fatalf("ordinary launch created checkout: %v %+v", e, records)
	}
	// Exercise the actual exec handoff on this terminal, not a mocked runner.
	cmd := exec.Command(os.Args[0], "-test.run=^TestCodeWorkspaceHandoffProcess$")
	cmd.Dir = f.work
	cmd.Env = append(os.Environ(), "ASTONISH_WORKSPACE_HANDOFF_CHILD=1", "ASTONISH_WORKSPACE_HANDOFF_DIR="+records[0].InvocationDir, "ASTONISH_WORKSPACE_HANDOFF_SESSION="+records[0].Sessions[0].SessionID, "TERM=xterm-256color", "NO_COLOR=1", "PATH="+f.bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	term = newHandoffPTY(t, cmd)
	term.wait("Enter send", "session resumed")
	term.send("Prove the fresh process with a private PID marker.\r")
	f.turn("shell_command", map[string]any{"command": "printf 'handoff\\n' > handoff.marker; pwd", "timeout": 10})
	codeRevisionPTYReply(t, f, sidebarPTYModelTurn{text: "HANDOFF WRITE FINISHED"})
	term.wait("HANDOFF WRITE FINISHED")
	data, e = os.ReadFile(filepath.Join(records[0].Root, "handoff.marker"))
	if e != nil || !strings.Contains(string(data), "handoff") {
		t.Fatalf("handoff tool cwd: %q %v", data, e)
	}
	if _, e = os.Stat(filepath.Join(f.work, "handoff.marker")); !os.IsNotExist(e) {
		t.Fatalf("handoff wrote original: %v", e)
	}
	term.close()
	t.Logf("real Code PTY launch, resume and exec handoff write in %s; active writer is rejected and original remains unchanged", records[0].InvocationDir)
}
