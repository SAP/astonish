//go:build linux

package sandbox

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestCompiledAdmissionChild(t *testing.T) {
	root := os.Getenv("ASTONISH_ADMISSION_ROOT")
	if root == "" {
		return
	}
	f, err := os.Open(root)
	if err == nil {
		defer f.Close()
		err = runBotHomeAdmission(f, BotHomeAdmissionMode(os.Getenv("ASTONISH_ADMISSION_MODE")), os.Stdin, os.Stdout)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

func admissionCommand(t *testing.T, root string, mode BotHomeAdmissionMode) *exec.Cmd {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCompiledAdmissionChild$")
	cmd.Env = append(os.Environ(), "ASTONISH_ADMISSION_ROOT="+root, "ASTONISH_ADMISSION_MODE="+string(mode))
	return cmd
}

func TestCompiledAdmissionContention(t *testing.T) {
	root := t.TempDir()
	start := func() (*exec.Cmd, io.WriteCloser, *bufio.Reader) {
		t.Helper()
		cmd := admissionCommand(t, root, BotHomeAdmissionWait)
		in, err := cmd.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		out, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err = cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = in.Close(); _ = cmd.Process.Kill() })
		return cmd, in, bufio.NewReader(out)
	}
	holder, input, reader := start()
	if line, err := reader.ReadString('\n'); err != nil || line != "{\"ready\":true}\n" {
		t.Fatalf("ready %q %v", line, err)
	}
	info, err := os.Stat(filepath.Join(root, ".workspace-lock"))
	if err != nil {
		t.Fatal(err)
	}
	if raw, err := admissionCommand(t, root, BotHomeAdmissionTry).Output(); err != nil || string(raw) != "{\"busy\":true}\n" {
		t.Fatalf("busy %q %v", raw, err)
	}
	// Input EOF cancels a blocked waiter without admission.
	cancelled, cancelInput, cancelReader := start()
	_ = cancelInput.Close()
	if raw, _ := io.ReadAll(cancelReader); len(raw) != 0 {
		t.Fatalf("cancelled waiter emitted %q", raw)
	}
	if err := cancelled.Wait(); err == nil {
		t.Fatal("cancelled waiter succeeded")
	}
	waiter, waitingInput, waitingReader := start()
	arrived := make(chan error, 1)
	go func() {
		line, err := waitingReader.ReadString('\n')
		if err == nil && line != "{\"ready\":true}\n" {
			err = fmt.Errorf("ready %q", line)
		}
		arrived <- err
	}()
	select {
	case err := <-arrived:
		t.Fatalf("bypassed flock: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	_ = holder.Process.Kill()
	_ = holder.Wait()
	_ = input.Close()
	select {
	case err := <-arrived:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("death retained lock")
	}
	_, _ = io.WriteString(waitingInput, "release\n")
	if err := waiter.Wait(); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(filepath.Join(root, ".workspace-lock"))
	if err != nil || !os.SameFile(info, after) {
		t.Fatalf("lock inode replaced: %v", err)
	}
	// Acquired stdin EOF fails and frees the descriptor too.
	acquired, acquiredInput, acquiredReader := start()
	if _, err := acquiredReader.ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	_ = acquiredInput.Close()
	if err := acquired.Wait(); err == nil {
		t.Fatal("EOF accepted as release")
	}
	cmd := admissionCommand(t, root, BotHomeAdmissionTry)
	cmd.Stdin = bytes.NewBufferString("release\n")
	if raw, err := cmd.Output(); err != nil || string(raw) != "{\"ready\":true}\n" {
		t.Fatalf("reacquire %q %v", raw, err)
	}
}

func TestCompiledAdmissionUnsafeLock(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink", "fifo", "directory", "invalid-mode"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			lock := filepath.Join(root, ".workspace-lock")
			var err error
			switch kind {
			case "symlink":
				err = os.Symlink(filepath.Join(t.TempDir(), "foreign"), lock)
			case "hardlink":
				other := filepath.Join(root, "other")
				err = os.WriteFile(other, nil, 0600)
				if err == nil {
					err = os.Link(other, lock)
				}
			case "fifo":
				err = unix.Mkfifo(lock, 0600)
			case "directory":
				err = os.Mkdir(lock, 0700)
			}
			if err != nil {
				t.Fatal(err)
			}
			mode := BotHomeAdmissionWait
			if kind == "invalid-mode" {
				mode = "invalid"
			}
			if raw, err := admissionCommand(t, root, mode).Output(); err == nil || len(raw) != 0 {
				t.Fatalf("unsafe lock %q %v", raw, err)
			}
		})
	}
}
