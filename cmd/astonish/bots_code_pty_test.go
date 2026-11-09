package astonish

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
)

// No arguments and no remote config exercise the real bare-code composition root,
// rather than only asserting shouldLaunchBareCodeWith or printing code --help.
func TestBotBareCodePTYAcceptance(t *testing.T) {
	home := t.TempDir()
	// A fresh HOME has no MCP cache. The default, optional CodeGraph server
	// otherwise runs npx and downloads packages before the first UI frame.
	// Exercise the supported unavailable-tool path, not the public npm registry:
	// no provider, remote config, cache, or rendered UI is manufactured here.
	bin := filepath.Join(home, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "npx"), []byte("#!/bin/sh\necho 'PTY fixture: optional npx unavailable' >&2\nexit 127\n"), 0700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestBotCLIProcess$", "--")
	cmd.Dir = home
	cmd.Env = append(os.Environ(), "ASTONISH_BOT_CLI_CHILD=1", "HOME="+home, "XDG_CONFIG_HOME="+home, "SUDO_USER=", "TERM=xterm-256color", "NO_COLOR=1", "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	pt, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 40, Cols: 120})
	if err != nil {
		t.Fatal(err)
	}
	// Reap on EVERY path, including a missing startup frame. Killing only the
	// CLI can leave an npm descendant writing into HOME during TempDir cleanup.
	// StartWithSize gives the child its own session/process group.
	exited := make(chan struct{})
	var exitErr error
	go func() {
		exitErr = cmd.Wait()
		close(exited)
	}()
	stop := make(chan struct{})
	readerDone := make(chan struct{})
	output := make(chan string, 64)
	readErrors := make(chan error, 1)
	t.Cleanup(func() {
		close(stop) // release the reader even if its output queue is full
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-exited       // do not remove HOME before the direct child has exited
		_ = pt.Close() // unblock Read after the process is gone
		<-readerDone   // no goroutine may use the PTY or testing.T after cleanup
	})
	go func() {
		defer close(readerDone)
		defer close(output)
		var terminal botPTYTerminal
		buf := make([]byte, 8192)
		for {
			n, err := pt.Read(buf)
			if n > 0 {
				if responseErr := terminal.respond(pt, buf[:n]); responseErr != nil {
					readErrors <- responseErr
					return
				}
				select {
				case output <- string(buf[:n]):
				case <-stop:
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	var rendered strings.Builder
	wait := func(text string) {
		t.Helper()
		started := time.Now()
		timer := time.NewTimer(30 * time.Second)
		defer timer.Stop()
		for !strings.Contains(rendered.String(), text) {
			select {
			case s, ok := <-output:
				if !ok {
					t.Fatalf("bare code closed before %q: %q", text, rendered.String())
				}
				rendered.WriteString(s)
			case err := <-readErrors:
				t.Fatalf("bare code terminal response: %v; output: %q", err, rendered.String())
			case <-timer.C:
				t.Fatalf("bare code missing %q after %s: %q", text, time.Since(started), rendered.String())
			}
		}
		t.Logf("bare code rendered %q after %s", text, time.Since(started))
	}
	wait("/model")
	if _, err := io.WriteString(pt, "/bots\r"); err != nil {
		t.Fatal(err)
	}
	wait("requires remote platform chat")
	if _, err := io.WriteString(pt, "/exit\r"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-exited:
		if exitErr != nil {
			t.Fatal(exitErr)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("bare code did not exit; output: %q", rendered.String())
	}
}
