package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/config"
)

func TestShellCommand_Echo(t *testing.T) {
	result, err := ShellCommand(nil, ShellCommandArgs{
		Command: "echo hello world",
	})
	if err != nil {
		t.Fatalf("ShellCommand() error = %v", err)
	}
	if got := strings.TrimSpace(result.Stdout); got != "hello world" {
		t.Errorf("Stdout = %q, want %q", got, "hello world")
	}
	if result.TimedOut {
		t.Error("TimedOut = true, want false")
	}
}

func TestShellCommand_WorkingDir(t *testing.T) {
	dir := t.TempDir()

	// Create a file in the temp dir
	if err := os.WriteFile(filepath.Join(dir, "marker.txt"), []byte("found"), 0644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	result, err := ShellCommand(nil, ShellCommandArgs{
		Command:    "cat marker.txt",
		WorkingDir: dir,
	})
	if err != nil {
		t.Fatalf("ShellCommand() error = %v", err)
	}
	if got := strings.TrimSpace(result.Stdout); got != "found" {
		t.Errorf("Stdout = %q, want %q", got, "found")
	}
}

func TestShellCommand_ExitNonZero(t *testing.T) {
	result, err := ShellCommand(nil, ShellCommandArgs{
		Command: "exit 1",
	})
	if err != nil {
		t.Fatalf("ShellCommand() error = %v (PTY reports exit code in result, not error)", err)
	}
	if result.ExitCode == nil {
		t.Fatal("expected ExitCode to be set for non-zero exit")
	}
	if *result.ExitCode != 1 {
		t.Errorf("ExitCode = %d, want 1", *result.ExitCode)
	}
}

func TestShellCommand_StderrInOutput(t *testing.T) {
	// PTY merges stdout and stderr into a single stream
	result, err := ShellCommand(nil, ShellCommandArgs{
		Command: "echo error_msg >&2 && exit 1",
	})
	if err != nil {
		t.Fatalf("ShellCommand() error = %v (PTY reports exit code in result, not error)", err)
	}
	// stderr should appear in the output stream (PTY merges stdout/stderr)
	if !strings.Contains(result.Stdout, "error_msg") {
		t.Errorf("Stdout = %q, want it to contain 'error_msg'", result.Stdout)
	}
	if result.ExitCode == nil {
		t.Fatal("expected ExitCode to be set for non-zero exit")
	}
	if *result.ExitCode != 1 {
		t.Errorf("ExitCode = %d, want 1", *result.ExitCode)
	}
}

func TestShellCommand_TimeoutDefault(t *testing.T) {
	// Verify that a quick command with default timeout works fine
	result, err := ShellCommand(nil, ShellCommandArgs{
		Command: "echo quick",
		Timeout: 0, // should default to 120
	})
	if err != nil {
		t.Fatalf("ShellCommand() error = %v", err)
	}
	if got := strings.TrimSpace(result.Stdout); got != "quick" {
		t.Errorf("Stdout = %q, want %q", got, "quick")
	}
}

func TestShellCommand_TimeoutExceeded(t *testing.T) {
	result, err := ShellCommand(nil, ShellCommandArgs{
		Command: "sleep 10",
		Timeout: 1,
	})
	if err != nil {
		t.Fatalf("ShellCommand() error = %v (expected nil with TimedOut=true)", err)
	}
	if !result.TimedOut {
		t.Error("TimedOut = false, want true")
	}
}

func TestShellCommand_TimeoutClamped(t *testing.T) {
	// Timeout > 3600 should be clamped to 3600, but command should still run
	result, err := ShellCommand(nil, ShellCommandArgs{
		Command: "echo clamped",
		Timeout: 9999,
	})
	if err != nil {
		t.Fatalf("ShellCommand() error = %v", err)
	}
	if got := strings.TrimSpace(result.Stdout); got != "clamped" {
		t.Errorf("Stdout = %q, want %q", got, "clamped")
	}
}

func TestShellCommand_MultilineOutput(t *testing.T) {
	result, err := ShellCommand(nil, ShellCommandArgs{
		Command: "printf 'line1\nline2\nline3\n'",
	})
	if err != nil {
		t.Fatalf("ShellCommand() error = %v", err)
	}
	lines := strings.Split(strings.TrimSpace(result.Stdout), "\n")
	if len(lines) != 3 {
		t.Errorf("output lines = %d, want 3", len(lines))
	}
}

// --- Security: file access blocking tests ---

func TestShellCommand_BlocksStoreKey(t *testing.T) {
	configDir, err := config.GetConfigDir()
	if err != nil {
		t.Skip("config dir not resolvable, skipping")
	}

	fullPath := filepath.Join(configDir, ".store_key")

	// Direct cat
	_, err = ShellCommand(nil, ShellCommandArgs{
		Command: "cat " + fullPath,
	})
	if err == nil {
		t.Fatal("expected error when reading .store_key via shell_command")
	}
	if !strings.Contains(err.Error(), "access denied") {
		t.Errorf("error should mention 'access denied', got: %v", err)
	}

	// With pipes
	_, err = ShellCommand(nil, ShellCommandArgs{
		Command: "cat " + fullPath + " | base64",
	})
	if err == nil {
		t.Fatal("expected error when reading .store_key via piped command")
	}

	// xxd variant
	_, err = ShellCommand(nil, ShellCommandArgs{
		Command: "xxd " + fullPath,
	})
	if err == nil {
		t.Fatal("expected error when reading .store_key via xxd")
	}
}

func TestShellCommand_BlocksCredentialsEnc(t *testing.T) {
	configDir, err := config.GetConfigDir()
	if err != nil {
		t.Skip("config dir not resolvable, skipping")
	}

	fullPath := filepath.Join(configDir, "credentials.enc")

	_, err = ShellCommand(nil, ShellCommandArgs{
		Command: "cat " + fullPath,
	})
	if err == nil {
		t.Fatal("expected error when reading credentials.enc via shell_command")
	}
	if !strings.Contains(err.Error(), "access denied") {
		t.Errorf("error should mention 'access denied', got: %v", err)
	}
}

func TestShellCommand_BlocksBareFilename(t *testing.T) {
	// Even bare filenames (without path) should be blocked
	_, err := ShellCommand(nil, ShellCommandArgs{
		Command: "cat .store_key",
	})
	if err == nil {
		t.Fatal("expected error when command references .store_key by bare name")
	}
	if !strings.Contains(err.Error(), "access denied") {
		t.Errorf("error should mention 'access denied', got: %v", err)
	}
}

func TestShellCommand_AllowsNormalCommands(t *testing.T) {
	// Normal commands that don't reference protected files should work
	result, err := ShellCommand(nil, ShellCommandArgs{
		Command: "echo 'credentials are fine to mention in text'",
	})
	if err != nil {
		t.Fatalf("normal command should not be blocked: %v", err)
	}
	if !strings.Contains(result.Stdout, "credentials are fine") {
		t.Errorf("unexpected output: %s", result.Stdout)
	}
}

// --- Filesystem scope confinement tests ---

func TestShellCommand_ScopeRoot_BlocksOutside(t *testing.T) {
	root := t.TempDir()
	SetScopeRoot(root)
	defer SetScopeRoot("") // reset for other tests

	_, err := ShellCommand(nil, ShellCommandArgs{Command: "cat /etc/hosts"})
	if err == nil {
		t.Fatal("expected out-of-scope command to be blocked")
	}
	if !strings.Contains(err.Error(), "outside the project directory") {
		t.Errorf("error should mention scope, got: %v", err)
	}
}

func TestShellCommand_ScopeRoot_AllowsInside(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "marker.txt"), []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	SetScopeRoot(root)
	defer SetScopeRoot("")

	// An absolute path inside root must be allowed.
	result, err := ShellCommand(nil, ShellCommandArgs{
		Command: "cat " + filepath.Join(root, "marker.txt"),
	})
	if err != nil {
		t.Fatalf("in-scope command should not be blocked: %v", err)
	}
	if !strings.Contains(result.Stdout, "ok") {
		t.Errorf("unexpected output: %s", result.Stdout)
	}
}

func TestShellCommand_ScopeRoot_DisabledByDefault(t *testing.T) {
	// With no scope root configured, out-of-project paths are NOT blocked by
	// the scope guard (preserves personal/CLI behavior). The command still runs.
	SetScopeRoot("")
	result, err := ShellCommand(nil, ShellCommandArgs{Command: "echo /etc/hosts"})
	if err != nil {
		t.Fatalf("with scoping disabled, command should run: %v", err)
	}
	if !strings.Contains(result.Stdout, "/etc/hosts") {
		t.Errorf("unexpected output: %s", result.Stdout)
	}
}

func TestReadFile_BlocksStoreKey(t *testing.T) {
	configDir, err := config.GetConfigDir()
	if err != nil {
		t.Skip("config dir not resolvable, skipping")
	}

	fullPath := filepath.Join(configDir, ".store_key")

	_, err = ReadFile(nil, ReadFileArgs{Path: fullPath})
	if err == nil {
		t.Fatal("expected error when reading .store_key via read_file")
	}
	if !strings.Contains(err.Error(), "access denied") {
		t.Errorf("error should mention 'access denied', got: %v", err)
	}
}

func TestReadFile_BlocksCredentialsEnc(t *testing.T) {
	configDir, err := config.GetConfigDir()
	if err != nil {
		t.Skip("config dir not resolvable, skipping")
	}

	fullPath := filepath.Join(configDir, "credentials.enc")

	_, err = ReadFile(nil, ReadFileArgs{Path: fullPath})
	if err == nil {
		t.Fatal("expected error when reading credentials.enc via read_file")
	}
	if !strings.Contains(err.Error(), "access denied") {
		t.Errorf("error should mention 'access denied', got: %v", err)
	}
}

func TestReadFile_AllowsNormalFiles(t *testing.T) {
	resetTestCache(t)
	dir := t.TempDir()
	testFile := filepath.Join(dir, "normal.txt")
	os.WriteFile(testFile, []byte("hello"), 0644)

	result, err := ReadFile(nil, ReadFileArgs{Path: testFile})
	if err != nil {
		t.Fatalf("reading normal file should work: %v", err)
	}
	// read_file now returns line-numbered content
	if result.Content != "1: hello" {
		t.Errorf("content = %q, want %q", result.Content, "1: hello")
	}
	if result.TotalLines != 1 {
		t.Errorf("total_lines = %d, want 1", result.TotalLines)
	}
}

func TestWriteFile_BlocksStoreKey(t *testing.T) {
	configDir, err := config.GetConfigDir()
	if err != nil {
		t.Skip("config dir not resolvable, skipping")
	}

	fullPath := filepath.Join(configDir, ".store_key")

	_, err = WriteFile(nil, WriteFileArgs{FilePath: fullPath, Content: "malicious"})
	if err == nil {
		t.Fatal("expected error when writing to .store_key via write_file")
	}
	if !strings.Contains(err.Error(), "access denied") {
		t.Errorf("error should mention 'access denied', got: %v", err)
	}
}
