package browser

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	// Default container-side directory for session recordings (writable in
	// Incus and OpenShell sandboxes; downloadable via PullFile).
	containerRecordingDir = "/tmp/astonish-recordings"
	// Local (host) directory for session recordings, relative to cwd.
	localRecordingDir = "recordings"
	// Fixed X display used by KasmVNC inside session containers.
	containerDisplay    = ":0"
	defaultRecordingFPS = 30
)

// safeRecordingName matches basename filenames we allow the agent to supply.
var safeRecordingName = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// xdpyinfoDimensionsRe matches "1920x1080" (optionally with surrounding text).
var xdpyinfoDimensionsRe = regexp.MustCompile(`(\d{2,5})x(\d{2,5})`)

// RecordingOptions configures StartRecording.
type RecordingOptions struct {
	// Filename is an optional basename (e.g. "demo.mp4"). Path separators and
	// unsafe characters are rejected. Empty uses a timestamped default.
	Filename string
}

// StartRecordingResult is returned by StartRecording.
type StartRecordingResult struct {
	Path   string `json:"path"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

// RecordingResult is returned by StopRecording.
type RecordingResult struct {
	Path            string  `json:"path"`
	DurationSeconds float64 `json:"duration_seconds"`
	SizeBytes       int64   `json:"size_bytes"`
}

// RecordingStatus is returned by RecordingStatus.
type RecordingStatus struct {
	Recording      bool    `json:"recording"`
	Path           string  `json:"path,omitempty"`
	ElapsedSeconds float64 `json:"elapsed_seconds,omitempty"`
}

// activeRecording tracks an in-progress capture.
type activeRecording struct {
	path      string
	width     int
	height    int
	startedAt time.Time
	stopFn    func() error
}

// ContainerStartRecordingFunc starts ffmpeg x11grab inside the session container.
// The implementation must probe the live X display size and start capture at
// that size (not the configured viewport). Returns the probed dimensions.
type ContainerStartRecordingFunc func(containerName, display, outPath string) (stop func() error, width, height int, err error)

// startLocalRecordingFunc is the local (host) ffmpeg starter. Overridable in tests.
var startLocalRecordingFunc = startLocalRecording

// probeLocalDisplayFunc is overridable in tests.
var probeLocalDisplayFunc = probeLocalDisplaySize

// StartRecording begins capturing the browser display to an MP4 file.
// Capture size is the live X root window (via xdpyinfo), not config viewport.
// The browser must already be launched (headed with a display). Only one
// recording may be active at a time.
func (m *Manager) StartRecording(opts RecordingOptions) (StartRecordingResult, error) {
	if m == nil {
		return StartRecordingResult{}, fmt.Errorf("browser Manager is nil")
	}
	if m.SandboxEnabled && (m.StatArtifactFunc == nil || m.SessionID() == "") {
		return StartRecordingResult{}, fmt.Errorf("sandbox recording metadata transport is not wired")
	}

	// Ensure browser is up before taking the lock for recording state.
	if _, err := m.GetOrLaunch(); err != nil {
		return StartRecordingResult{}, fmt.Errorf("browser must be launched before recording: %w", err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.recording != nil {
		return StartRecordingResult{}, fmt.Errorf("recording already in progress at %s", m.recording.path)
	}

	outPath, err := m.recordingOutputPathLocked(opts.Filename)
	if err != nil {
		return StartRecordingResult{}, err
	}

	var (
		stopFn        func() error
		width, height int
	)
	if m.SandboxEnabled {
		if m.ContainerStartRecordingFunc == nil {
			return StartRecordingResult{}, fmt.Errorf("sandbox recording is not wired (ContainerStartRecordingFunc is nil)")
		}
		if m.containerName == "" {
			return StartRecordingResult{}, fmt.Errorf("sandbox browser is not running in a container")
		}
		stopFn, width, height, err = m.ContainerStartRecordingFunc(m.containerName, containerDisplay, outPath)
		if err != nil {
			return StartRecordingResult{}, fmt.Errorf("failed to start container recording: %w", err)
		}
	} else {
		display := m.displayForRecordingLocked()
		if display == "" {
			return StartRecordingResult{}, fmt.Errorf("recording requires a headed browser with a display (set DISPLAY or run with Xvfb); headless capture is not supported")
		}
		width, height, err = probeLocalDisplayFunc(display)
		if err != nil {
			return StartRecordingResult{}, fmt.Errorf("failed to probe display size: %w", err)
		}
		stopFn, err = startLocalRecordingFunc(display, width, height, outPath)
		if err != nil {
			return StartRecordingResult{}, fmt.Errorf("failed to start local recording: %w", err)
		}
	}

	m.recording = &activeRecording{
		path:      outPath,
		width:     width,
		height:    height,
		startedAt: time.Now(),
		stopFn:    stopFn,
	}
	m.logger.Printf("recording started: %s (%dx%d)", outPath, width, height)
	return StartRecordingResult{Path: outPath, Width: width, Height: height}, nil
}

// StopRecording ends the active capture and returns the finalized file metadata.
func (m *Manager) StopRecording() (RecordingResult, error) {
	if m == nil {
		return RecordingResult{}, fmt.Errorf("browser Manager is nil")
	}

	m.mu.Lock()
	rec := m.recording
	m.recording = nil
	sandboxEnabled, sessionID, stat := m.SandboxEnabled, m.sessionID, m.StatArtifactFunc
	ctx := m.requestCtx
	if ctx == nil {
		ctx = context.Background()
	}
	m.mu.Unlock()

	if rec == nil {
		return RecordingResult{}, fmt.Errorf("no recording in progress")
	}

	if rec.stopFn != nil {
		if err := rec.stopFn(); err != nil {
			return RecordingResult{}, fmt.Errorf("failed to stop recording: %w", err)
		}
	}

	duration := time.Since(rec.startedAt).Seconds()
	var size int64
	if sandboxEnabled {
		if stat == nil || sessionID == "" {
			return RecordingResult{}, fmt.Errorf("sandbox recording metadata transport is not wired")
		}
		var err error
		size, err = stat(ctx, sessionID, rec.path)
		if err != nil {
			return RecordingResult{}, fmt.Errorf("stat sandbox recording: %w", err)
		}
	} else if fi, err := os.Stat(rec.path); err == nil {
		size = fi.Size()
	}

	m.logger.Printf("recording stopped: %s (%.1fs, %d bytes)", rec.path, duration, size)
	return RecordingResult{
		Path:            rec.path,
		DurationSeconds: duration,
		SizeBytes:       size,
	}, nil
}

func (m *Manager) recordingStatusLocked() RecordingStatus {
	if m.recording == nil {
		return RecordingStatus{Recording: false}
	}
	return RecordingStatus{
		Recording:      true,
		Path:           m.recording.path,
		ElapsedSeconds: time.Since(m.recording.startedAt).Seconds(),
	}
}

// RecordingStatus reports whether a capture is in progress.
func (m *Manager) RecordingStatus() RecordingStatus {
	if m == nil {
		return RecordingStatus{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.recordingStatusLocked()
}

// stopRecordingLocked stops an active recording without returning metadata.
// Must be called with m.mu held. Errors are logged and discarded.
func (m *Manager) stopRecordingLocked() {
	if m.recording == nil {
		return
	}
	rec := m.recording
	m.recording = nil
	if rec.stopFn != nil {
		if err := rec.stopFn(); err != nil {
			m.logger.Printf("recording stop during cleanup: %v", err)
		}
	}
}

func (m *Manager) recordingOutputPathLocked(filename string) (string, error) {
	name, err := sanitizeRecordingFilename(filename)
	if err != nil {
		return "", err
	}
	if m.SandboxEnabled {
		return filepath.Join(containerRecordingDir, name), nil
	}
	if err := os.MkdirAll(localRecordingDir, 0o755); err != nil {
		return "", fmt.Errorf("failed to create recordings directory: %w", err)
	}
	abs, err := filepath.Abs(filepath.Join(localRecordingDir, name))
	if err != nil {
		return "", err
	}
	return abs, nil
}

func (m *Manager) displayForRecordingLocked() string {
	if m.xvfb != nil && m.xvfb.display != "" {
		return m.xvfb.display
	}
	return strings.TrimSpace(os.Getenv("DISPLAY"))
}

func sanitizeRecordingFilename(filename string) (string, error) {
	name := strings.TrimSpace(filename)
	if name == "" {
		name = fmt.Sprintf("browser-%s.mp4", time.Now().Format("20060102-150405"))
	}
	name = filepath.Base(name)
	if name == "." || name == ".." || name == "" {
		return "", fmt.Errorf("invalid recording filename")
	}
	if !safeRecordingName.MatchString(name) {
		return "", fmt.Errorf("recording filename may only contain letters, digits, '.', '_', and '-'")
	}
	if !strings.HasSuffix(strings.ToLower(name), ".mp4") {
		name += ".mp4"
	}
	return name, nil
}

// ParseXdpyinfoDimensions extracts WxH from xdpyinfo output (full or awk'd).
func ParseXdpyinfoDimensions(output string) (int, int, error) {
	m := xdpyinfoDimensionsRe.FindStringSubmatch(strings.TrimSpace(output))
	if len(m) != 3 {
		return 0, 0, fmt.Errorf("no dimensions in xdpyinfo output: %q", strings.TrimSpace(output))
	}
	w, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, 0, err
	}
	h, err := strconv.Atoi(m[2])
	if err != nil {
		return 0, 0, err
	}
	if w < 16 || h < 16 || w > 16384 || h > 16384 {
		return 0, 0, fmt.Errorf("implausible display size %dx%d", w, h)
	}
	return w, h, nil
}

// DisplayProbeShellCommand returns a shell one-liner that prints WxH for DISPLAY.
// Used by sandbox recording helpers so Incus/OpenShell stay consistent.
func DisplayProbeShellCommand(display string) string {
	return fmt.Sprintf("DISPLAY=%s xdpyinfo 2>/dev/null | awk '/dimensions:/{print $2; exit}'", shellQuoteForDisplay(display))
}

func shellQuoteForDisplay(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func probeLocalDisplaySize(display string) (int, int, error) {
	bin, err := exec.LookPath("xdpyinfo")
	if err != nil {
		return 0, 0, fmt.Errorf("xdpyinfo not found (install x11-utils): %w", err)
	}
	cmd := exec.Command(bin, "-display", display)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return 0, 0, fmt.Errorf("xdpyinfo %s: %w (%s)", display, err, strings.TrimSpace(string(out)))
	}
	return ParseXdpyinfoDimensions(string(out))
}

// startLocalRecording launches ffmpeg x11grab against the given DISPLAY.
func startLocalRecording(display string, width, height int, outPath string) (func() error, error) {
	bin, err := exec.LookPath("ffmpeg")
	if err != nil {
		return nil, fmt.Errorf("ffmpeg not found in PATH (install ffmpeg to enable browser recording): %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return nil, err
	}

	args := BuildFFmpegX11GrabArgs(display, width, height, outPath)
	cmd := exec.Command(bin, args[1:]...) // skip "ffmpeg" — Command uses bin
	cmd.Env = append(os.Environ(), "DISPLAY="+display)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stdout = nil
	cmd.Stderr = nil

	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return nil, err
	}

	// Brief settle: catch immediate start failures (missing display, etc.).
	time.Sleep(300 * time.Millisecond)
	if cmd.ProcessState != nil && cmd.ProcessState.Exited() {
		_ = stdin.Close()
		return nil, fmt.Errorf("ffmpeg exited immediately after start")
	}

	return func() error {
		// Prefer graceful quit so the MP4 moov atom is written.
		_, _ = stdin.Write([]byte("q"))
		_ = stdin.Close()
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case err := <-done:
			if err != nil {
				// Non-zero exit is common after 'q'; only fail if file missing.
				if _, statErr := os.Stat(outPath); statErr != nil {
					return fmt.Errorf("ffmpeg exited without producing %s: %w", outPath, err)
				}
			}
			return nil
		case <-time.After(30 * time.Second):
			_ = cmd.Process.Kill()
			<-done
			return fmt.Errorf("timed out waiting for ffmpeg to finalize recording")
		}
	}, nil
}

// BuildFFmpegX11GrabArgs returns the ffmpeg argv used for display capture.
// Exported for sandbox wiring and tests so the flags stay in one place.
func BuildFFmpegX11GrabArgs(display string, width, height int, outPath string) []string {
	input := display
	if !strings.Contains(input, ".") {
		input = input + ".0"
	}
	return []string{
		"ffmpeg", "-y",
		"-f", "x11grab",
		"-video_size", fmt.Sprintf("%dx%d", width, height),
		"-framerate", fmt.Sprintf("%d", defaultRecordingFPS),
		"-i", input,
		"-c:v", "libx264",
		"-preset", "veryfast",
		"-crf", "23",
		"-pix_fmt", "yuv420p",
		outPath,
	}
}
