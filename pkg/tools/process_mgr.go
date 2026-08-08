package tools

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"regexp"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"github.com/google/uuid"
)

// --- Ring Buffer ---

// RingBuffer is a fixed-size byte buffer that overwrites oldest data when full.
type RingBuffer struct {
	mu   sync.Mutex
	buf  []byte
	size int
	w    int  // write position
	full bool // whether the buffer has wrapped around
}

// NewRingBuffer creates a ring buffer with the given capacity.
func NewRingBuffer(size int) *RingBuffer {
	return &RingBuffer{
		buf:  make([]byte, size),
		size: size,
	}
}

// Write appends data to the ring buffer, overwriting oldest data if needed.
func (r *RingBuffer) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	n := len(p)
	if n >= r.size {
		// Data larger than buffer — keep only the last `size` bytes
		copy(r.buf, p[n-r.size:])
		r.w = 0
		r.full = true
		return n, nil
	}

	// Write wrapping around if needed
	space := r.size - r.w
	if n <= space {
		copy(r.buf[r.w:], p)
		r.w += n
		if r.w == r.size {
			r.w = 0
			r.full = true
		}
	} else {
		copy(r.buf[r.w:], p[:space])
		copy(r.buf, p[space:])
		r.w = n - space
		r.full = true
	}
	return n, nil
}

// Bytes returns the buffer contents in order (oldest to newest).
func (r *RingBuffer) Bytes() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()

	if !r.full {
		out := make([]byte, r.w)
		copy(out, r.buf[:r.w])
		return out
	}

	out := make([]byte, r.size)
	copy(out, r.buf[r.w:])
	copy(out[r.size-r.w:], r.buf[:r.w])
	return out
}

// Len returns the number of bytes currently in the buffer.
func (r *RingBuffer) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.full {
		return r.size
	}
	return r.w
}

// --- ANSI Stripping ---

// ansiRegexp matches ANSI escape sequences (CSI sequences, OSC sequences, and basic escapes).
var ansiRegexp = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]|\x1b\][^\x07]*\x07|\x1b[()][0-9A-B]|\x1b[=><!]|\x1b\[[0-9;]*[Hf]|\x1b\[[0-9]*[ABCDJK]|\x1b\[\?[0-9;]*[hl]`)

// StripANSI removes ANSI escape sequences from text.
func StripANSI(s string) string {
	return ansiRegexp.ReplaceAllString(s, "")
}

// --- Process Manager ---

const (
	// defaultBufferSize is the default ring buffer size per session (64KB).
	defaultBufferSize = 64 * 1024
	// sessionTTL is how long completed sessions are kept before cleanup.
	sessionTTL = 30 * time.Minute
	// cleanupInterval is how often the background cleaner runs.
	cleanupInterval = 5 * time.Minute
	// idleThreshold is how long a process can be idle (no new output) before
	// being considered "waiting for input" in one-shot mode.
	idleThreshold = 3 * time.Second
)

// ProcessSession represents a running or completed PTY-backed process.
type ProcessSession struct {
	ID        string
	Command   string
	PID       int
	StartedAt time.Time
	EndedAt   *time.Time
	ExitCode  *int
	Output    *RingBuffer
	pty       *os.File // master side of PTY
	cmd       *exec.Cmd
	mu        sync.Mutex
	lastWrite time.Time // last time output was received from the process
	done      chan struct{}
	readDone  chan struct{} // closed when readLoop exits (all output drained)
}

// IsRunning returns true if the process is still alive.
func (s *ProcessSession) IsRunning() bool {
	select {
	case <-s.done:
		return false
	default:
		return true
	}
}

// ProcessManager tracks all PTY-backed process sessions.
type ProcessManager struct {
	mu       sync.RWMutex
	sessions map[string]*ProcessSession
	stopOnce sync.Once
	stopCh   chan struct{}
}

// NewProcessManager creates a ProcessManager and starts the background cleanup goroutine.
func NewProcessManager() *ProcessManager {
	pm := &ProcessManager{
		sessions: make(map[string]*ProcessSession),
		stopCh:   make(chan struct{}),
	}
	go pm.cleanupLoop()
	return pm
}

// Start launches a command in a PTY and returns the session.
func (pm *ProcessManager) Start(command, workDir string, rows, cols uint16) (*ProcessSession, error) {
	if rows == 0 {
		rows = 24
	}
	if cols == 0 {
		cols = 80
	}

	cmd := exec.Command("sh", "-c", command)
	if workDir != "" {
		cmd.Dir = expandPath(workDir)
	}

	// Set environment: inherit parent env and add safe defaults.
	// EDITOR/VISUAL=true prevents commands that spawn a text editor (commit messages,
	// interactive configs, etc.) from hanging — the agent cannot operate a text editor.
	// TERM=xterm-256color ensures consistent terminal behavior regardless of how
	// Astonish is launched.
	//
	// The command still runs in a real PTY, so genuinely interactive programs
	// (REPLs, ssh, y/n prompts, TUIs) keep working and can be driven via the
	// waiting_for_input + process_write loop. What we suppress is the *automatic*
	// pager: because a PTY looks like a terminal, git/less/most CLIs launch a
	// pager (less) that blocks waiting for keypresses the agent has no reason to
	// send — a frequent hang (e.g. `git diff`, `git log`). GIT_PAGER/PAGER=cat
	// make those print instead of paginating, and GIT_TERMINAL_PROMPT=0 makes git
	// fail fast rather than block on an unanswerable credential prompt.
	cmd.Env = append(os.Environ(),
		"EDITOR=true",
		"VISUAL=true",
		"TERM=xterm-256color",
		"PAGER=cat",
		"GIT_PAGER=cat",
		"GIT_TERMINAL_PROMPT=0",
	)

	// Start with PTY
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: rows, Cols: cols})
	if err != nil {
		return nil, err
	}

	sess := &ProcessSession{
		ID:        uuid.New().String()[:8],
		Command:   command,
		PID:       cmd.Process.Pid,
		StartedAt: time.Now(),
		Output:    NewRingBuffer(defaultBufferSize),
		pty:       ptmx,
		cmd:       cmd,
		lastWrite: time.Now(),
		done:      make(chan struct{}),
		readDone:  make(chan struct{}),
	}

	// Start output reader goroutine
	go sess.readLoop()

	// Start waiter goroutine to detect process exit
	go sess.waitLoop()

	pm.mu.Lock()
	pm.sessions[sess.ID] = sess
	pm.mu.Unlock()

	return sess, nil
}

// Get returns a session by ID, or nil if not found.
func (pm *ProcessManager) Get(id string) *ProcessSession {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	return pm.sessions[id]
}

// List returns all sessions.
func (pm *ProcessManager) List() []*ProcessSession {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	list := make([]*ProcessSession, 0, len(pm.sessions))
	for _, s := range pm.sessions {
		list = append(list, s)
	}
	return list
}

// Kill sends a signal to a process. Default is SIGTERM, with SIGKILL fallback after 5s.
func (pm *ProcessManager) Kill(id string) error {
	pm.mu.RLock()
	sess := pm.sessions[id]
	pm.mu.RUnlock()

	if sess == nil {
		return nil
	}

	if !sess.IsRunning() {
		return nil
	}

	// Send SIGTERM
	if sess.cmd.Process != nil {
		_ = sess.cmd.Process.Signal(syscall.SIGTERM)
	}

	// Wait up to 5s for exit, then SIGKILL
	select {
	case <-sess.done:
		return nil
	case <-time.After(5 * time.Second):
		if sess.cmd.Process != nil {
			_ = sess.cmd.Process.Signal(syscall.SIGKILL)
		}
		// Wait for the kill to take effect
		select {
		case <-sess.done:
		case <-time.After(2 * time.Second):
		}
		return nil
	}
}

// Cleanup kills all running sessions and stops the background cleaner.
func (pm *ProcessManager) Cleanup() {
	pm.stopOnce.Do(func() {
		close(pm.stopCh)
	})

	pm.mu.RLock()
	ids := make([]string, 0, len(pm.sessions))
	for id := range pm.sessions {
		ids = append(ids, id)
	}
	pm.mu.RUnlock()

	for _, id := range ids {
		_ = pm.Kill(id)
	}
}

// cleanupLoop removes completed sessions after sessionTTL.
func (pm *ProcessManager) cleanupLoop() {
	ticker := time.NewTicker(cleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-pm.stopCh:
			return
		case <-ticker.C:
			pm.mu.Lock()
			now := time.Now()
			for id, sess := range pm.sessions {
				if !sess.IsRunning() && sess.EndedAt != nil && now.Sub(*sess.EndedAt) > sessionTTL {
					delete(pm.sessions, id)
				}
			}
			pm.mu.Unlock()
		}
	}
}

// readLoop continuously reads from the PTY master and writes to the ring buffer.
func (s *ProcessSession) readLoop() {
	defer close(s.readDone)
	buf := make([]byte, 4096)
	for {
		n, err := s.pty.Read(buf)
		if n > 0 {
			cleaned := StripANSI(string(buf[:n]))
			if len(cleaned) > 0 {
				s.Output.Write([]byte(cleaned))
				s.mu.Lock()
				s.lastWrite = time.Now()
				s.mu.Unlock()
			}
		}
		if err != nil {
			// EOF or EIO — process output stream closed
			return
		}
	}
}

// waitLoop waits for the process to exit and records exit info.
func (s *ProcessSession) waitLoop() {
	err := s.cmd.Wait()
	now := time.Now()
	s.mu.Lock()
	s.EndedAt = &now
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			code := exitErr.ExitCode()
			s.ExitCode = &code
		} else {
			code := -1
			s.ExitCode = &code
		}
	} else {
		code := 0
		s.ExitCode = &code
	}
	s.mu.Unlock()

	// On Linux, when the slave PTY closes (process exits), the master
	// side gets EIO which causes readLoop to exit. We must wait for
	// readLoop to finish draining before we close the PTY fd and
	// signal done. Give readLoop a bounded time to drain; if the
	// process exited, the PTY read will get EIO promptly.
	select {
	case <-s.readDone:
	case <-time.After(2 * time.Second):
	}

	// Close PTY master fd (safe even if readLoop already got EIO)
	s.pty.Close()

	close(s.done)
}

// Write sends data to the process's stdin via the PTY.
// Newlines (\n) are translated to carriage returns (\r) to match real
// terminal behavior — keyboards send \r for Enter, and the PTY line
// discipline normally converts it. Programs in raw mode (like SSH
// password prompts) disable that translation, so sending \r directly
// ensures interactive prompts receive the expected Enter keypress.
func (s *ProcessSession) Write(data []byte) (int, error) {
	if !s.IsRunning() {
		return 0, io.EOF
	}
	translated := bytes.ReplaceAll(data, []byte("\n"), []byte("\r"))
	return s.pty.Write(translated)
}

// IdleDuration returns how long since the last output was received.
func (s *ProcessSession) IdleDuration() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return time.Since(s.lastWrite)
}

// globalProcessManager is the singleton process manager.
var globalProcessManager *ProcessManager
var processManagerOnce sync.Once

// GetProcessManager returns the global process manager, initializing it on first call.
func GetProcessManager() *ProcessManager {
	processManagerOnce.Do(func() {
		globalProcessManager = NewProcessManager()
	})
	return globalProcessManager
}

// CleanupProcessManager shuts down the global process manager if it was initialized.
func CleanupProcessManager() {
	if globalProcessManager != nil {
		globalProcessManager.Cleanup()
	}
}
