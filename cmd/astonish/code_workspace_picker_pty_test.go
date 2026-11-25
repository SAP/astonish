package astonish

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Real Code process, real Git project and a loopback provider; current-cell
// terminal assertions exercise the picker rather than a model-only fixture.
func TestCodeWorkspacePickerPTYAcceptance(t *testing.T) {
	goEnv := exec.Command("go", "env", "GOMODCACHE", "GOCACHE", "GOPATH")
	envOut, err := goEnv.Output()
	if err != nil {
		t.Fatal(err)
	}
	values := strings.Fields(string(envOut))
	f := newSidebarPTYFixture(t)
	for i, key := range []string{"GOMODCACHE", "GOCACHE", "GOPATH"} {
		t.Setenv(key, values[i])
	}
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", f.work}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init")
	git("config", "user.name", "Fixture")
	git("config", "user.email", "fixture@example.invalid")
	if err := os.WriteFile(filepath.Join(f.work, "task.txt"), []byte("original\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "task.txt")
	git("commit", "-m", "baseline")
	binary := filepath.Join(t.TempDir(), "astonish")
	build := exec.Command("go", "build", "-o", binary, ".")
	build.Dir = filepath.Join("..", "..")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v %s", err, out)
	}
	cmd := exec.Command(binary, "code", "--model", "fixture:pty-model", "--dir", f.work, "--resume", f.sessionID)
	cmd.Dir = f.work
	cmd.Env = append(os.Environ(), "TERM=xterm-256color", "NO_COLOR=1", "PATH="+f.bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	term := newHandoffPTYSize(t, cmd, 120, 30)
	term.mu.Lock()
	term.screen = newSidebarPTYScreen(120, 30)
	term.screen.p.Parse([]byte(term.raw.String()))
	term.mu.Unlock()
	defer term.close()
	term.wait("Enter send")
	term.send("/new\r")
	term.wait("Code workspaces", "› Current workspace", "Independent workspace")
	term.send("\x1b[B\r")
	term.wait("Committed HEAD", "Include current staged")
	term.send("\x1b")
	term.wait("› Independent workspace")
	term.send("\x1b")
	term.wait("Enter send")
	term.assertAbsent("Code workspaces")
	term.send("/workspaces\r")
	term.wait("Code workspaces", "current")
	term.send("\x1b")
	term.wait("Enter send")
	term.send("/new\r")
	term.wait("› Current workspace")
	term.send("\r")
	term.wait("Message Astonish", "Enter send")
	if got := git("status", "--porcelain"); got != "" {
		t.Fatalf("picker changed original: %s", got)
	}
	term.send("/new\r")
	term.wait("› Current workspace")
	term.send("\x1b[B\r")
	term.wait("Committed HEAD")
	term.send("\r")
	term.wait("Message Astonish", "Enter send")
	if got := git("worktree", "list", "--porcelain"); !strings.Contains(got, "detached") {
		t.Fatalf("independent picker did not create detached workspace: %s", got)
	}
	if got := git("status", "--porcelain"); got != "" {
		t.Fatalf("handoff changed original: %s", got)
	}
	term.send("/workspaces\r")
	term.wait("Code workspaces", "independent")
	term.send("\x1b")
	term.wait("Enter send")
}
