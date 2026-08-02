package daemon

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/SAP/astonish/pkg/config"
)

const (
	maxLogSize     = 10 * 1024 * 1024 // 10MB
	maxLogBackups  = 3
	logPermissions = 0644

	logDestinationFile   = "file"
	logDestinationStdout = "stdout"

	logDestinationEnv = "ASTONISH_LOG_DEST"
)

// Logger provides file-based logging with size-based rotation.
type Logger struct {
	mu       sync.Mutex
	file     *os.File
	filePath string
	size     int64
}

// NewLogger creates a logger that writes to the given file path.
// The log directory is created if it doesn't exist.
func NewLogger(logPath string) (*Logger, error) {
	if err := EnsureDir(filepath.Dir(logPath)); err != nil {
		return nil, fmt.Errorf("failed to create log directory: %w", err)
	}

	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, logPermissions)
	if err != nil {
		return nil, fmt.Errorf("failed to open log file: %w", err)
	}

	info, err := f.Stat()
	if err != nil {
		_ = f.Close() // best-effort cleanup; original error takes priority
		return nil, err
	}

	return &Logger{
		file:     f,
		filePath: logPath,
		size:     info.Size(),
	}, nil
}

// NewStdoutLogger creates a logger that writes to stdout.
// Used in containerized modes (api/worker) where file-based logging may not be
// available or desirable (container orchestrators capture stdout natively).
func NewStdoutLogger() *Logger {
	return &Logger{
		file:     os.Stdout,
		filePath: "", // no file rotation for stdout
	}
}

func newLoggerForMode(daemonMode, logDir string) (*Logger, error) {
	destination, explicit := daemonLogDestination(daemonMode)
	if destination == logDestinationStdout {
		return NewStdoutLogger(), nil
	}

	logger, err := NewLogger(filepath.Join(logDir, "daemon.log"))
	if err != nil {
		if daemonMode != config.DaemonModeDefault {
			fallback := NewStdoutLogger()
			if explicit {
				fallback.Printf("Warning: failed to initialize file logger requested by %s=%s: %v; falling back to stdout", logDestinationEnv, destination, err)
			}
			return fallback, nil
		}
		return nil, err
	}
	return logger, nil
}

func daemonLogDestination(daemonMode string) (string, bool) {
	if raw := strings.TrimSpace(os.Getenv(logDestinationEnv)); raw != "" {
		switch strings.ToLower(raw) {
		case logDestinationFile:
			return logDestinationFile, true
		case logDestinationStdout:
			return logDestinationStdout, true
		}
	}

	if daemonMode == config.DaemonModeAPI || daemonMode == config.DaemonModeWorker {
		return logDestinationStdout, false
	}
	return logDestinationFile, false
}

// Write implements io.Writer for use with log.SetOutput or as stdout/stderr redirect.
func (l *Logger) Write(p []byte) (n int, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	// Skip rotation for stdout logger (no file path = no rotation)
	if l.filePath != "" && l.size >= maxLogSize {
		l.rotate()
	}

	n, err = l.file.Write(p)
	l.size += int64(n)
	return
}

// Printf writes a formatted log line with a timestamp.
func (l *Logger) Printf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	timestamp := time.Now().Format("2006-01-02 15:04:05")
	fmt.Fprintf(l, "[%s] %s\n", timestamp, msg)
}

// Close closes the log file. No-op for stdout logger.
func (l *Logger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file != nil && l.filePath != "" {
		return l.file.Close()
	}
	return nil
}

// rotate renames the current log file and opens a new one.
// Must be called with l.mu held.
func (l *Logger) rotate() {
	_ = l.file.Close() // best-effort; file may already be in a bad state

	// Shift existing backups: .3 -> delete, .2 -> .3, .1 -> .2, current -> .1
	for i := maxLogBackups; i >= 1; i-- {
		src := fmt.Sprintf("%s.%d", l.filePath, i)
		dst := fmt.Sprintf("%s.%d", l.filePath, i+1)
		if i == maxLogBackups {
			_ = os.Remove(src) // best-effort cleanup of oldest backup
		} else {
			os.Rename(src, dst)
		}
	}
	os.Rename(l.filePath, l.filePath+".1")

	// Open new log file
	f, err := os.OpenFile(l.filePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, logPermissions)
	if err != nil {
		// If we can't open a new log, write to stderr as fallback
		l.file = os.Stderr
		l.size = 0
		return
	}
	l.file = f
	l.size = 0
}

// TailLog reads the last N lines from the log file and writes them to w.
// If follow is true, it continues tailing new output (blocking).
func TailLog(logPath string, lines int, follow bool, w io.Writer) error {
	f, err := os.Open(logPath)
	if err != nil {
		return fmt.Errorf("failed to open log: %w", err)
	}

	// Read last N lines
	stat, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}

	// Simple approach: read the whole file and output last N lines
	// For daemon logs (10MB max), this is fine
	data := make([]byte, stat.Size())
	if _, err := io.ReadFull(f, data); err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		f.Close()
		return err
	}
	f.Close()

	// Find last N newlines
	count := 0
	start := len(data)
	for i := len(data) - 1; i >= 0; i-- {
		if data[i] == '\n' {
			count++
			if count > lines {
				start = i + 1
				break
			}
		}
	}
	if count <= lines {
		start = 0
	}

	w.Write(data[start:])

	if !follow {
		return nil
	}

	// Follow mode: reopen and poll for new data
	f, err = os.Open(logPath)
	if err != nil {
		return err
	}
	defer f.Close()

	// Seek to end
	f.Seek(0, io.SeekEnd)

	buf := make([]byte, 4096)
	for {
		n, err := f.Read(buf)
		if n > 0 {
			w.Write(buf[:n])
		}
		if err != nil && err != io.EOF {
			return err
		}
		if n == 0 {
			time.Sleep(500 * time.Millisecond)
		}
	}
}
