package astonish

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/gitutil"
)

// Real Code processes, native background PTY tool, graceful /quit (not the
// harness's SIGKILL fallback). Another instance must keep writing throughout.
func TestCodeWorkspaceLifecyclePTYAcceptance(t *testing.T) {
	f := newSidebarPTYFixture(t)
	for _, args := range [][]string{{"init"}, {"config", "user.name", "Fixture"}, {"config", "user.email", "fixture@example.test"}} {
		if out, err := exec.Command("git", append([]string{"-C", f.work}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git: %s %v", out, err)
		}
	}
	if err := os.WriteFile(filepath.Join(f.work, "base"), []byte("base\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "base"}, {"commit", "-m", "base"}} {
		if out, err := exec.Command("git", append([]string{"-C", f.work}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git: %s %v", out, err)
		}
	}
	start := func() *handoffPTY {
		cmd := exec.Command(os.Args[0], "-test.run=^TestBotCLIProcess$", "--", "code", "--model", "fixture:pty-model", "--dir", f.work, "--workspace", "independent", "--yolo")
		cmd.Dir = f.work
		cmd.Env = append(os.Environ(), "ASTONISH_BOT_CLI_CHILD=1", "TERM=xterm-256color", "NO_COLOR=1", "PATH="+f.bin+string(os.PathListSeparator)+os.Getenv("PATH"))
		p := newHandoffPTY(t, cmd)
		p.wait("Enter send")
		return p
	}
	a := start()
	a.send("Start heartbeat A with shell command in background.\r")
	f.turn("shell_command", map[string]any{"command": "while :; do printf x >> heartbeat; sleep 0.1; done", "background": true, "timeout": 10})
	codeRevisionPTYReply(t, f, sidebarPTYModelTurn{text: "HEARTBEAT A STARTED"})
	a.wait("HEARTBEAT A STARTED")
	b := start()
	b.send("Start heartbeat B with shell command in background.\r")
	f.turn("shell_command", map[string]any{"command": "while :; do printf x >> heartbeat; sleep 0.1; done", "background": true, "timeout": 10})
	codeRevisionPTYReply(t, f, sidebarPTYModelTurn{text: "HEARTBEAT B STARTED"})
	b.wait("HEARTBEAT B STARTED")
	dir, err := config.GetConfigDir()
	if err == nil {
		dir, err = filepath.EvalSymlinks(dir)
	}
	if err != nil {
		t.Fatal(err)
	}
	manager, err := gitutil.NewCodeWorkspaceManager(filepath.Join(dir, "code-workspaces"))
	if err != nil {
		t.Fatal(err)
	}
	project, err := manager.ResolveProject(context.Background(), f.work)
	if err != nil {
		t.Fatal(err)
	}
	records, err := manager.List(context.Background(), project)
	if err != nil || len(records) != 2 {
		t.Fatalf("records: %+v %v", records, err)
	}
	heartbeat := func(root string) int {
		data, err := os.ReadFile(filepath.Join(root, "heartbeat"))
		if err != nil {
			t.Fatal(err)
		}
		return len(data)
	}
	// Both are growing before shutdown.
	before := []int{heartbeat(records[0].Root), heartbeat(records[1].Root)}
	time.Sleep(300 * time.Millisecond)
	for i := range records {
		if heartbeat(records[i].Root) <= before[i] {
			t.Fatal("heartbeat not running")
		}
	}
	graceful := func(p *handoffPTY) {
		p.send("/quit\r")
		waited := make(chan error, 1)
		go func() { waited <- p.cmd.Wait() }()
		select {
		case err := <-waited:
			if err != nil {
				t.Fatalf("graceful Code exit: %v", err)
			}
		case <-time.After(15 * time.Second):
			t.Fatal("Code shutdown did not complete")
		}
		p.once.Do(func() { _ = p.pt.Close(); <-p.done })
	}
	graceful(a)
	sizes := []int{heartbeat(records[0].Root), heartbeat(records[1].Root)}
	time.Sleep(500 * time.Millisecond)
	stopped, running := -1, -1
	for i := range records {
		if heartbeat(records[i].Root) == sizes[i] {
			stopped = i
		} else {
			running = i
		}
	}
	if stopped < 0 || running < 0 {
		t.Fatal("closing one Code instance must stop exactly one heartbeat")
	}
	lease, err := manager.AcquireLease(context.Background(), project, records[stopped].ID)
	if err != nil {
		t.Fatalf("closed workspace lease not released: %v", err)
	}
	lease.Close()
	if _, err := manager.AcquireLease(context.Background(), project, records[running].ID); err == nil {
		t.Fatal("other process lease released")
	}
	graceful(b)
	t.Log(fmt.Sprintf("graceful Code shutdown stops owned heartbeat only; other checkout %s remains active until its own quit", records[running].Root))
}
