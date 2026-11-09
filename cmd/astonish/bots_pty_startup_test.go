package astonish

import (
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/muesli/termenv"
)

// Exercise the emulator against the actual startup color-query reader, not just
// a string writer. In particular, termenv needs the OSC reply before the cursor
// reply and must consume both before restoring the terminal line discipline.
func TestBotPTYTerminalStartup(t *testing.T) {
	if os.Getenv("ASTONISH_PTY_QUERY_CHILD") == "1" {
		output := termenv.NewOutput(os.Stdout)
		for i := 0; i < 100; i++ {
			_ = output.BackgroundColor()
		}
		_, _ = io.WriteString(os.Stdout, "astonish terminal ready\n")
		return
	}
	for i := 0; i < 10; i++ {
		cmd := exec.Command(os.Args[0], "-test.run=^TestBotPTYTerminalStartup$")
		cmd.Env = append(os.Environ(), "ASTONISH_PTY_QUERY_CHILD=1", "TERM=xterm-256color", "NO_COLOR=1", "CI=")
		pt, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 50, Cols: 140})
		if err != nil {
			t.Fatal(err)
		}
		var output strings.Builder
		readerDone := make(chan struct{})
		var responseErr error
		go func() {
			defer close(readerDone)
			var terminal botPTYTerminal
			buf := make([]byte, 8192)
			for {
				n, err := pt.Read(buf)
				if n > 0 {
					output.Write(buf[:n])
					if responseErr = terminal.respond(pt, buf[:n]); responseErr != nil {
						return
					}
				}
				if err != nil {
					return
				}
			}
		}()
		exited := make(chan error, 1)
		go func() { exited <- cmd.Wait() }()
		timer := time.NewTimer(10 * time.Second)
		select {
		case err := <-exited:
			timer.Stop()
			_ = pt.Close()
			<-readerDone
			if err != nil {
				t.Fatalf("startup %d: %v; %q", i, err, output.String())
			}
		case <-timer.C:
			// Capture where initialization stopped before reaping the owned child.
			_ = cmd.Process.Signal(syscall.SIGQUIT)
			select {
			case <-exited:
			case <-time.After(time.Second):
				_ = cmd.Process.Kill()
				<-exited
			}
			_ = pt.Close()
			<-readerDone
			t.Fatalf("startup %d stalled: %s", i, output.String())
		}
		if responseErr != nil {
			t.Fatalf("startup %d terminal response: %v", i, responseErr)
		}
		if !strings.Contains(output.String(), "astonish terminal ready") {
			t.Fatalf("startup %d missing readiness: %q", i, output.String())
		}
	}
}
