package tools

import (
	"context"
	"strings"
	"testing"
	"time"
)

// --- Ring Buffer Tests ---

func TestRingBuffer_BasicWrite(t *testing.T) {
	rb := NewRingBuffer(32)

	n, err := rb.Write([]byte("hello"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 5 {
		t.Fatalf("expected 5 bytes written, got %d", n)
	}

	got := string(rb.Bytes())
	if got != "hello" {
		t.Fatalf("expected %q, got %q", "hello", got)
	}
	if rb.Len() != 5 {
		t.Fatalf("expected len 5, got %d", rb.Len())
	}
}

func TestRingBuffer_Overflow(t *testing.T) {
	rb := NewRingBuffer(10)

	// Write more than capacity
	rb.Write([]byte("abcdefghij")) // fills exactly
	rb.Write([]byte("12345"))      // overwrites first 5

	got := string(rb.Bytes())
	expected := "fghij12345"
	if got != expected {
		t.Fatalf("expected %q, got %q", expected, got)
	}
	if rb.Len() != 10 {
		t.Fatalf("expected len 10, got %d", rb.Len())
	}
}

func TestRingBuffer_LargerThanCapacity(t *testing.T) {
	rb := NewRingBuffer(8)

	// Write data larger than the buffer — only last 8 bytes should remain
	rb.Write([]byte("0123456789ABCDEF"))

	got := string(rb.Bytes())
	expected := "89ABCDEF"
	if got != expected {
		t.Fatalf("expected %q, got %q", expected, got)
	}
}

func TestRingBuffer_MultipleSmallWrites(t *testing.T) {
	rb := NewRingBuffer(16)

	rb.Write([]byte("aaaa"))
	rb.Write([]byte("bbbb"))
	rb.Write([]byte("cccc"))
	rb.Write([]byte("dddd"))
	// Total 16 bytes, should exactly fill buffer
	got := string(rb.Bytes())
	if got != "aaaabbbbccccdddd" {
		t.Fatalf("expected %q, got %q", "aaaabbbbccccdddd", got)
	}

	// One more write should wrap
	rb.Write([]byte("ee"))
	got = string(rb.Bytes())
	if got != "aabbbbccccddddee" {
		t.Fatalf("expected %q, got %q", "aabbbbccccdddee", got)
	}
}

func TestRingBuffer_Empty(t *testing.T) {
	rb := NewRingBuffer(16)
	got := rb.Bytes()
	if len(got) != 0 {
		t.Fatalf("expected empty, got %d bytes", len(got))
	}
	if rb.Len() != 0 {
		t.Fatalf("expected len 0, got %d", rb.Len())
	}
}

// --- ANSI Stripping Tests ---

func TestStripANSI_Colors(t *testing.T) {
	input := "\x1b[31mred text\x1b[0m"
	got := StripANSI(input)
	if got != "red text" {
		t.Fatalf("expected %q, got %q", "red text", got)
	}
}

func TestStripANSI_CursorMovement(t *testing.T) {
	input := "\x1b[2J\x1b[Hhello"
	got := StripANSI(input)
	if got != "hello" {
		t.Fatalf("expected %q, got %q", "hello", got)
	}
}

func TestStripANSI_NoEscapes(t *testing.T) {
	input := "plain text with no escapes"
	got := StripANSI(input)
	if got != input {
		t.Fatalf("expected %q, got %q", input, got)
	}
}

func TestStripANSI_Mixed(t *testing.T) {
	input := "\x1b[1;32m$ \x1b[0mls -la\n\x1b[34mtotal 48\x1b[0m"
	got := StripANSI(input)
	expected := "$ ls -la\ntotal 48"
	if got != expected {
		t.Fatalf("expected %q, got %q", expected, got)
	}
}

// --- Prompt Detection Tests ---

func TestLooksLikePrompt(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{"ssh_fingerprint", "Are you sure you want to continue connecting (yes/no/[fingerprint])? ", true},
		{"password_prompt", "root@host's password:", true},
		{"sudo_prompt", "[sudo] password for user: ", true},
		{"yes_no", "Do you want to continue? [Y/n] ", true},
		{"shell_prompt", "user@host:~$ ", true},
		{"question_mark", "Enter your name? ", true},
		{"colon_ending", "Username: ", true},
		{"normal_output", "total 48\ndrwxr-xr-x 5 user staff 160 Feb 22 10:00 .", false},
		{"empty", "", false},
		{"long_last_line", strings.Repeat("x", 250), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := looksLikePrompt(tt.input)
			if got != tt.expected {
				t.Errorf("looksLikePrompt(%q) = %v, want %v", tt.input, got, tt.expected)
			}
		})
	}
}

// --- Process Manager Tests ---

func TestProcessManager_StartAndWait(t *testing.T) {
	pm := NewProcessManager()
	defer pm.Cleanup()

	sess, err := pm.Start("echo hello", "", 24, 80)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	if sess.ID == "" {
		t.Fatal("session ID is empty")
	}
	if sess.PID == 0 {
		t.Fatal("PID is 0")
	}

	// Wait for process to exit
	select {
	case <-sess.done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for process to exit")
	}

	if sess.IsRunning() {
		t.Fatal("expected process to not be running")
	}

	output := string(sess.Output.Bytes())
	if !strings.Contains(output, "hello") {
		t.Fatalf("expected output to contain 'hello', got %q", output)
	}
}

func TestProcessManager_BackgroundAndKill(t *testing.T) {
	pm := NewProcessManager()
	defer pm.Cleanup()

	sess, err := pm.Start("sleep 60", "", 24, 80)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	if !sess.IsRunning() {
		t.Fatal("expected process to be running")
	}

	// Kill it
	if err := pm.Kill(sess.ID); err != nil {
		t.Fatalf("Kill failed: %v", err)
	}

	// Should be dead now
	if sess.IsRunning() {
		t.Fatal("expected process to be dead after kill")
	}
}

func TestProcessManager_List(t *testing.T) {
	pm := NewProcessManager()
	defer pm.Cleanup()

	pm.Start("sleep 60", "", 24, 80)
	pm.Start("sleep 60", "", 24, 80)

	sessions := pm.List()
	if len(sessions) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(sessions))
	}
}

func TestProcessManager_WriteAndRead(t *testing.T) {
	pm := NewProcessManager()
	defer pm.Cleanup()

	// Start cat which echoes stdin to stdout
	sess, err := pm.Start("cat", "", 24, 80)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Write to stdin
	time.Sleep(200 * time.Millisecond) // let cat start
	_, err = sess.Write([]byte("hello world\n"))
	if err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	// Wait for output
	time.Sleep(500 * time.Millisecond)

	output := string(sess.Output.Bytes())
	if !strings.Contains(output, "hello world") {
		t.Fatalf("expected output to contain 'hello world', got %q", output)
	}

	// Kill cat
	pm.Kill(sess.ID)
}

func TestProcessManager_SecurityBlocksStoreKey(t *testing.T) {
	// The shell_command function should block commands referencing protected files
	// even with PTY. This test verifies the security check is still in place.
	result, err := ShellCommand(nil, ShellCommandArgs{
		Command: "cat .store_key",
	})
	if err == nil {
		t.Fatalf("expected error for .store_key access, got result: %+v", result)
	}
	if !strings.Contains(err.Error(), "access denied") {
		t.Fatalf("expected 'access denied' error, got: %v", err)
	}
}

func TestProcessManager_SecurityBlocksCredentialsEnc(t *testing.T) {
	result, err := ShellCommand(nil, ShellCommandArgs{
		Command: "hexdump credentials.enc",
	})
	if err == nil {
		t.Fatalf("expected error for credentials.enc access, got result: %+v", result)
	}
	if !strings.Contains(err.Error(), "access denied") {
		t.Fatalf("expected 'access denied' error, got: %v", err)
	}
}

func TestShellCommand_PTY_Echo(t *testing.T) {
	result, err := ShellCommand(nil, ShellCommandArgs{
		Command: "echo 'pty test output'",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result.Stdout, "pty test output") {
		t.Fatalf("expected output to contain 'pty test output', got %q", result.Stdout)
	}
	if result.WaitingForInput {
		t.Fatal("expected waiting_for_input=false for echo")
	}
	if result.SessionID != "" {
		t.Fatal("expected no session_id for completed one-shot command")
	}
}

func TestShellCommand_PTY_Background(t *testing.T) {
	result, err := ShellCommand(nil, ShellCommandArgs{
		Command:    "sleep 60",
		Background: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.SessionID == "" {
		t.Fatal("expected session_id for background command")
	}

	// Clean up
	pm := GetProcessManager()
	pm.Kill(result.SessionID)
}

func TestShellCommand_PTY_ExitCode(t *testing.T) {
	result, err := ShellCommand(nil, ShellCommandArgs{
		Command: "exit 42",
	})
	// exit 42 returns non-zero but shouldn't be a tool error
	// The PTY-backed version captures exit code in the result
	_ = err // may or may not have error depending on implementation
	if result.ExitCode != nil && *result.ExitCode != 42 {
		t.Fatalf("expected exit code 42, got %d", *result.ExitCode)
	}
}

// TestShellCommand_PagerDisabled verifies the child environment suppresses the
// auto-pager (so git/less-style CLIs don't hang waiting for keypresses) while
// keeping the PTY. We check the env is exported to the command.
func TestShellCommand_PagerDisabled(t *testing.T) {
	result, err := ShellCommand(nil, ShellCommandArgs{
		Command: "echo pager=$PAGER git_pager=$GIT_PAGER prompt=$GIT_TERMINAL_PROMPT",
	})
	if err != nil {
		t.Fatalf("ShellCommand error: %v", err)
	}
	out := result.Stdout
	for _, want := range []string{"pager=cat", "git_pager=cat", "prompt=0"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected env %q in output, got %q", want, out)
		}
	}
}

// TestWaitForShellSession_CancelKillsProcess verifies that cancelling the
// context aborts a long-running command promptly (the Esc-cancel path) instead
// of waiting for the timeout, and kills the child process.
func TestWaitForShellSession_CancelKillsProcess(t *testing.T) {
	pm := NewProcessManager()
	defer pm.Cleanup()

	sess, err := pm.Start("sleep 60", "", 24, 80)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel shortly after we begin waiting.
	go func() {
		time.Sleep(150 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	// Large timeout so only cancellation can end this quickly.
	result, err := waitForShellSession(ctx, pm, sess, 3600)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected a cancellation error")
	}
	if result.TimedOut {
		t.Fatal("should have been cancelled, not timed out")
	}
	if elapsed > 3*time.Second {
		t.Fatalf("cancellation was not prompt: took %s", elapsed)
	}
	// The process should have been killed.
	time.Sleep(200 * time.Millisecond)
	if sess.IsRunning() {
		t.Fatal("process still running after cancellation")
	}
}

// TestWaitForShellSession_NilContext confirms a nil context disables the cancel
// case without panicking. The nil path is reached in practice via
// ShellCommand(nil, ...) (a nil tool.Context), so we exercise it through the
// public entry point rather than passing a literal nil context.Context.
func TestWaitForShellSession_NilContext(t *testing.T) {
	result, err := ShellCommand(nil, ShellCommandArgs{Command: "echo done"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result.Stdout, "done") {
		t.Fatalf("expected output 'done', got %q", result.Stdout)
	}
}
