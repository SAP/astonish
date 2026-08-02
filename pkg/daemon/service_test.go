package daemon

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestWriteAndReadPID(t *testing.T) {
	dir := t.TempDir()
	pidPath := filepath.Join(dir, "test.pid")

	if err := WritePID(pidPath); err != nil {
		t.Fatalf("WritePID failed: %v", err)
	}

	pid, err := ReadPID(pidPath)
	if err != nil {
		t.Fatalf("ReadPID failed: %v", err)
	}

	if pid != os.Getpid() {
		t.Errorf("expected pid %d, got %d", os.Getpid(), pid)
	}
}

func TestReadPID_NotExist(t *testing.T) {
	pid, err := ReadPID("/nonexistent/path/test.pid")
	if err != nil {
		t.Fatalf("expected no error for missing file, got: %v", err)
	}
	if pid != 0 {
		t.Errorf("expected pid 0 for missing file, got %d", pid)
	}
}

func TestReadPID_Invalid(t *testing.T) {
	dir := t.TempDir()
	pidPath := filepath.Join(dir, "bad.pid")
	os.WriteFile(pidPath, []byte("notanumber"), 0644)

	_, err := ReadPID(pidPath)
	if err == nil {
		t.Fatal("expected error for invalid PID file")
	}
}

func TestRemovePID(t *testing.T) {
	dir := t.TempDir()
	pidPath := filepath.Join(dir, "test.pid")
	os.WriteFile(pidPath, []byte("123"), 0644)

	RemovePID(pidPath)

	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Error("expected PID file to be removed")
	}
}

func TestRemovePID_NotExist(t *testing.T) {
	// Should not panic
	RemovePID("/nonexistent/path/test.pid")
}

func TestIsProcessRunning_Self(t *testing.T) {
	if !IsProcessRunning(os.Getpid()) {
		t.Error("expected own process to be running")
	}
}

func TestIsProcessRunning_Invalid(t *testing.T) {
	if IsProcessRunning(0) {
		t.Error("expected pid 0 to not be running")
	}
	if IsProcessRunning(-1) {
		t.Error("expected pid -1 to not be running")
	}
}

func TestDefaultLogDir(t *testing.T) {
	dir, err := DefaultLogDir()
	if err != nil {
		t.Fatalf("DefaultLogDir failed: %v", err)
	}
	if dir == "" {
		t.Error("expected non-empty log dir")
	}
	if filepath.Base(dir) != "logs" {
		t.Errorf("expected log dir to end with 'logs', got %q", filepath.Base(dir))
	}
}

func TestDefaultPIDPath(t *testing.T) {
	path, err := DefaultPIDPath()
	if err != nil {
		t.Fatalf("DefaultPIDPath failed: %v", err)
	}
	if filepath.Base(path) != "daemon.pid" {
		t.Errorf("expected 'daemon.pid', got %q", filepath.Base(path))
	}
}

func TestNewLogger(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "test.log")

	logger, err := NewLogger(logPath)
	if err != nil {
		t.Fatalf("NewLogger failed: %v", err)
	}
	defer logger.Close()

	logger.Printf("test message %d", 42)

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("failed to read log: %v", err)
	}

	if !bytes.Contains(data, []byte("test message 42")) {
		t.Errorf("log should contain 'test message 42', got: %s", string(data))
	}
}

func TestLoggerWrite(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "test.log")

	logger, err := NewLogger(logPath)
	if err != nil {
		t.Fatalf("NewLogger failed: %v", err)
	}
	defer logger.Close()

	n, err := logger.Write([]byte("hello world\n"))
	if err != nil {
		t.Fatalf("Write failed: %v", err)
	}
	if n != 12 {
		t.Errorf("expected 12 bytes written, got %d", n)
	}

	data, _ := os.ReadFile(logPath)
	if string(data) != "hello world\n" {
		t.Errorf("unexpected content: %q", string(data))
	}
}

func TestLoggerRotation(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "test.log")

	logger, err := NewLogger(logPath)
	if err != nil {
		t.Fatalf("NewLogger failed: %v", err)
	}
	defer logger.Close()

	// Artificially set size to trigger rotation
	logger.mu.Lock()
	logger.size = maxLogSize
	logger.mu.Unlock()

	logger.Write([]byte("after rotation\n"))

	// Check that .1 backup exists
	if _, err := os.Stat(logPath + ".1"); os.IsNotExist(err) {
		t.Error("expected backup file .1 to exist after rotation")
	}

	// Check the new log has our message
	data, _ := os.ReadFile(logPath)
	if !bytes.Contains(data, []byte("after rotation")) {
		t.Errorf("new log should contain 'after rotation', got: %q", string(data))
	}
}

func TestDaemonLogDestination(t *testing.T) {
	tests := []struct {
		name         string
		daemonMode   string
		envValue     string
		want         string
		wantExplicit bool
	}{
		{"default mode uses file", "default", "", logDestinationFile, false},
		{"api mode uses stdout", "api", "", logDestinationStdout, false},
		{"worker mode uses stdout", "worker", "", logDestinationStdout, false},
		{"env file overrides api", "api", "file", logDestinationFile, true},
		{"env stdout overrides default", "default", "stdout", logDestinationStdout, true},
		{"env is case insensitive", "default", "STDOUT", logDestinationStdout, true},
		{"invalid env falls back to mode default", "api", "syslog", logDestinationStdout, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(logDestinationEnv, tt.envValue)
			got, explicit := daemonLogDestination(tt.daemonMode)
			if got != tt.want {
				t.Fatalf("destination = %q, want %q", got, tt.want)
			}
			if explicit != tt.wantExplicit {
				t.Fatalf("explicit = %v, want %v", explicit, tt.wantExplicit)
			}
		})
	}
}

func TestNewLoggerForMode(t *testing.T) {
	t.Run("api mode returns stdout logger without creating files", func(t *testing.T) {
		dir := t.TempDir()
		logger, err := newLoggerForMode("api", dir)
		if err != nil {
			t.Fatalf("newLoggerForMode failed: %v", err)
		}
		defer logger.Close()

		if logger.file != os.Stdout {
			t.Fatal("expected api mode to use stdout logger")
		}
		if logger.filePath != "" {
			t.Fatalf("stdout logger should not have filePath, got %q", logger.filePath)
		}
		if _, err := os.Stat(filepath.Join(dir, "daemon.log")); !os.IsNotExist(err) {
			t.Fatalf("api mode should not create daemon.log, stat err = %v", err)
		}
	})

	t.Run("default mode returns file logger", func(t *testing.T) {
		dir := t.TempDir()
		logger, err := newLoggerForMode("default", dir)
		if err != nil {
			t.Fatalf("newLoggerForMode failed: %v", err)
		}
		defer logger.Close()

		wantPath := filepath.Join(dir, "daemon.log")
		if logger.filePath != wantPath {
			t.Fatalf("filePath = %q, want %q", logger.filePath, wantPath)
		}
		if _, err := os.Stat(wantPath); err != nil {
			t.Fatalf("expected daemon.log to exist: %v", err)
		}
	})

	t.Run("stdout env overrides default mode", func(t *testing.T) {
		t.Setenv(logDestinationEnv, logDestinationStdout)
		dir := t.TempDir()
		logger, err := newLoggerForMode("default", dir)
		if err != nil {
			t.Fatalf("newLoggerForMode failed: %v", err)
		}
		defer logger.Close()

		if logger.file != os.Stdout {
			t.Fatal("expected stdout override to use stdout logger")
		}
	})
}

func TestTailLog(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "test.log")

	// Write some lines
	var content string
	for i := 1; i <= 20; i++ {
		content += "line " + strconv.Itoa(i) + "\n"
	}
	os.WriteFile(logPath, []byte(content), 0644)

	var buf bytes.Buffer
	err := TailLog(logPath, 5, false, &buf)
	if err != nil {
		t.Fatalf("TailLog failed: %v", err)
	}

	output := buf.String()
	// Should contain lines 16-20
	if !bytes.Contains([]byte(output), []byte("line 16")) {
		t.Errorf("expected output to contain 'line 16', got: %q", output)
	}
	if !bytes.Contains([]byte(output), []byte("line 20")) {
		t.Errorf("expected output to contain 'line 20', got: %q", output)
	}
	// Should not contain line 14
	if bytes.Contains([]byte(output), []byte("line 14\n")) {
		t.Errorf("expected output to NOT contain 'line 14', got: %q", output)
	}
}

func TestTailLog_NotExist(t *testing.T) {
	var buf bytes.Buffer
	err := TailLog("/nonexistent/path", 10, false, &buf)
	if err == nil {
		t.Error("expected error for nonexistent file")
	}
}

func TestNewService(t *testing.T) {
	svc, err := NewService()
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}
	if svc == nil {
		t.Fatal("expected non-nil service")
	}
	if svc.Label() == "" {
		t.Error("expected non-empty label")
	}
	if svc.LogPath() == "" {
		t.Error("expected non-empty log path")
	}
}

func TestWritePID_CreatesSubdirs(t *testing.T) {
	dir := t.TempDir()
	pidPath := filepath.Join(dir, "sub", "deep", "test.pid")

	if err := WritePID(pidPath); err != nil {
		t.Fatalf("WritePID should create subdirs, got: %v", err)
	}

	pid, _ := ReadPID(pidPath)
	if pid != os.Getpid() {
		t.Errorf("expected pid %d, got %d", os.Getpid(), pid)
	}
}
