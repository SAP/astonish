//go:build unix

package agent

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestPlanVerifyTimeoutKillsProcessGroup(t *testing.T) {
	testPlanVerifyPipeChild(t, false, 0)
}

func TestPlanVerifyExitedShellPipeBounded(t *testing.T) {
	testPlanVerifyPipeChild(t, true, 0)
}

func TestPlanVerifyFailedShellPipeBounded(t *testing.T) {
	testPlanVerifyPipeChild(t, true, 7)
}

func testPlanVerifyPipeChild(t *testing.T, exitShell bool, shellCode int) {
	t.Helper()
	dir := t.TempDir()
	pidPath := filepath.Join(dir, "child.pid")
	heartbeat := filepath.Join(dir, "heartbeat")
	// The background shell retains the output pipe and keeps mutating a file.
	// It ignores TERM to require reliable process-group SIGKILL cleanup.
	command := `echo ready; bash -c 'trap "" TERM; echo $$ > child.pid; while :; do echo tick >> heartbeat; sleep 0.02; done' & `
	if exitShell {
		command += fmt.Sprintf("exit %d", shellCode)
	} else {
		command += "wait"
	}
	t.Cleanup(func() {
		data, err := os.ReadFile(pidPath)
		if err == nil {
			pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
			if pid > 0 {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	})
	timeout := 500 * time.Millisecond
	if exitShell {
		timeout = 5 * time.Second
	}
	started := time.Now()
	code, output, err := defaultPlanVerify(dir, command, timeout)
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("verification took %s, expected prompt pipe cleanup", elapsed)
	}
	wantCode := -1
	if exitShell && shellCode != 0 {
		wantCode = shellCode
	}
	if !strings.HasPrefix(output, "ready") || code != wantCode {
		t.Fatalf("got (%d, %q, %v)", code, output, err)
	}
	if exitShell && shellCode != 0 {
		if err != nil {
			t.Fatalf("failed shell should preserve exit code without error: %v", err)
		}
	} else if exitShell {
		if !errors.Is(err, exec.ErrWaitDelay) {
			t.Fatalf("expected bounded inherited-pipe error, got %v", err)
		}
	} else if err == nil || err.Error() != fmt.Sprintf("verify timed out after %s", timeout) {
		t.Fatalf("wrong timeout error: %v", err)
	}
	before, err := os.ReadFile(heartbeat)
	if err != nil || len(before) == 0 {
		t.Fatalf("child did not start: %v", err)
	}
	time.Sleep(150 * time.Millisecond)
	after, err := os.ReadFile(heartbeat)
	if err != nil || string(before) != string(after) {
		t.Fatalf("descendant still running after verify returned: %v", err)
	}
}

func TestPlanVerifyCommandOutputBounded(t *testing.T) {
	code, output, err := defaultPlanVerify(t.TempDir(), `printf prefix; yes x | head -c 1048576`, 5*time.Second)
	if err != nil || code != 0 || len(output) > planVerifyMaxOutput || !strings.HasPrefix(output, "prefix") {
		t.Fatalf("got code=%d output bytes=%d err=%v", code, len(output), err)
	}
	if len(output) < planVerifyMaxOutput-2 {
		t.Fatalf("output was not retained up to cap: %d", len(output))
	}
}
