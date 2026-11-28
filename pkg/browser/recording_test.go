package browser

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDefaultConfig_Viewport1080p(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.ViewportWidth != 1920 || cfg.ViewportHeight != 1080 {
		t.Fatalf("DefaultConfig viewport = %dx%d, want 1920x1080", cfg.ViewportWidth, cfg.ViewportHeight)
	}
}

func TestNewManager_ZeroViewportClampsTo1080p(t *testing.T) {
	m := NewManager(BrowserConfig{})
	if m.config.ViewportWidth != 1920 || m.config.ViewportHeight != 1080 {
		t.Fatalf("clamped viewport = %dx%d, want 1920x1080", m.config.ViewportWidth, m.config.ViewportHeight)
	}
}

func TestSanitizeRecordingFilename(t *testing.T) {
	got, err := sanitizeRecordingFilename("")
	if err != nil {
		t.Fatalf("empty: %v", err)
	}
	if !strings.HasPrefix(got, "browser-") || !strings.HasSuffix(got, ".mp4") {
		t.Fatalf("empty default = %q", got)
	}

	got, err = sanitizeRecordingFilename("demo")
	if err != nil || got != "demo.mp4" {
		t.Fatalf("demo: got %q err %v", got, err)
	}

	got, err = sanitizeRecordingFilename("../evil.mp4")
	if err != nil || got != "evil.mp4" {
		t.Fatalf("traversal: got %q err %v", got, err)
	}

	if _, err := sanitizeRecordingFilename("bad name.mp4"); err == nil {
		t.Fatal("expected error for spaces")
	}
}

func TestParseXdpyinfoDimensions(t *testing.T) {
	tests := []struct {
		in      string
		wantW   int
		wantH   int
		wantErr bool
	}{
		{"1728x996", 1728, 996, false},
		{"  dimensions:    1920x1080 pixels (508x286 millimeters)", 1920, 1080, false},
		{"", 0, 0, true},
		{"no size here", 0, 0, true},
		{"1x1", 0, 0, true}, // implausible
	}
	for _, tt := range tests {
		w, h, err := ParseXdpyinfoDimensions(tt.in)
		if tt.wantErr {
			if err == nil {
				t.Errorf("ParseXdpyinfoDimensions(%q) expected error", tt.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseXdpyinfoDimensions(%q): %v", tt.in, err)
			continue
		}
		if w != tt.wantW || h != tt.wantH {
			t.Errorf("ParseXdpyinfoDimensions(%q) = %dx%d, want %dx%d", tt.in, w, h, tt.wantW, tt.wantH)
		}
	}
}

func TestBuildFFmpegX11GrabArgs(t *testing.T) {
	args := BuildFFmpegX11GrabArgs(":0", 1728, 996, "/tmp/out.mp4")
	joined := strings.Join(args, " ")
	for _, want := range []string{"ffmpeg", "x11grab", "1728x996", ":0.0", "libx264", "/tmp/out.mp4"} {
		if !strings.Contains(joined, want) {
			t.Errorf("args missing %q: %v", want, args)
		}
	}
}

func TestStopRecording_HappyPathAndIdleError(t *testing.T) {
	dir := t.TempDir()
	outFile := filepath.Join(dir, "test.mp4")
	if err := os.WriteFile(outFile, []byte("fake-mp4"), 0o644); err != nil {
		t.Fatal(err)
	}

	var stopped bool
	m := NewManager(DefaultConfig())
	m.mu.Lock()
	m.recording = &activeRecording{
		path:      outFile,
		startedAt: time.Now().Add(-2 * time.Second),
		stopFn: func() error {
			stopped = true
			return nil
		},
	}
	m.mu.Unlock()

	st := m.RecordingStatus()
	if !st.Recording || st.Path != outFile {
		t.Fatalf("status = %+v", st)
	}

	res, err := m.StopRecording()
	if err != nil {
		t.Fatalf("StopRecording: %v", err)
	}
	if !stopped {
		t.Fatal("expected stopFn to run")
	}
	if res.Path != outFile {
		t.Fatalf("path = %q, want %q", res.Path, outFile)
	}
	if res.DurationSeconds < 1.5 {
		t.Fatalf("duration = %v, want >= 1.5s", res.DurationSeconds)
	}
	if res.SizeBytes != 8 {
		t.Fatalf("size = %d, want 8", res.SizeBytes)
	}

	if _, err := m.StopRecording(); err == nil {
		t.Fatal("second StopRecording should error")
	}
}

func TestCleanup_StopsOrphanRecording(t *testing.T) {
	var stopped bool
	m := NewManager(DefaultConfig())
	m.mu.Lock()
	m.recording = &activeRecording{
		path:      "/tmp/orphan.mp4",
		startedAt: time.Now(),
		stopFn: func() error {
			stopped = true
			return nil
		},
	}
	m.mu.Unlock()

	m.Cleanup()
	if !stopped {
		t.Fatal("expected Cleanup to stop recording")
	}
	if m.RecordingStatus().Recording {
		t.Fatal("expected no active recording after Cleanup")
	}
}

func TestStartRecording_SandboxPathViaHook(t *testing.T) {
	m := NewManager(DefaultConfig())
	m.SandboxEnabled = true
	m.containerName = "sess-ctr"
	m.SetSessionID("session")
	m.StatArtifactFunc = func(_ context.Context, id, path string) (int64, error) {
		if id != "session" || path != "/tmp/astonish-recordings/demo.mp4" {
			t.Fatalf("incorrect artifact scope %s %s", id, path)
		}
		return 42, nil
	}

	var gotDisplay, gotPath string
	var gotW, gotH int
	var stopped bool

	m.ContainerStartRecordingFunc = func(containerName, display, outPath string) (func() error, int, int, error) {
		gotDisplay = display
		gotPath = outPath
		gotW, gotH = 1728, 996
		return func() error {
			stopped = true
			return nil
		}, gotW, gotH, nil
	}

	// Exercise the sandbox start branch without GetOrLaunch / rod.
	m.mu.Lock()
	if m.recording != nil {
		m.mu.Unlock()
		t.Fatal("unexpected existing recording")
	}
	outPath, err := m.recordingOutputPathLocked("demo.mp4")
	if err != nil {
		m.mu.Unlock()
		t.Fatal(err)
	}
	stopFn, width, height, err := m.ContainerStartRecordingFunc(m.containerName, containerDisplay, outPath)
	if err != nil {
		m.mu.Unlock()
		t.Fatal(err)
	}
	m.recording = &activeRecording{path: outPath, width: width, height: height, startedAt: time.Now(), stopFn: stopFn}
	m.mu.Unlock()

	if gotDisplay != ":0" || gotW != 1728 || gotH != 996 {
		t.Fatalf("start args display=%q size=%dx%d", gotDisplay, gotW, gotH)
	}
	if gotPath != "/tmp/astonish-recordings/demo.mp4" {
		t.Fatalf("path = %q", gotPath)
	}

	res, err := m.StopRecording()
	if err != nil {
		t.Fatal(err)
	}
	if !stopped || res.Path != gotPath {
		t.Fatalf("stop stopped=%v res=%+v", stopped, res)
	}
}

func TestRecordingOutputPath_LocalCreatesDir(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })

	m := NewManager(DefaultConfig())
	m.mu.Lock()
	path, err := m.recordingOutputPathLocked("local.mp4")
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(path, filepath.Join("recordings", "local.mp4")) {
		t.Fatalf("path = %q", path)
	}
	if _, err := os.Stat(filepath.Join(tmp, "recordings")); err != nil {
		t.Fatalf("recordings dir not created: %v", err)
	}
}

func TestDisplayProbeShellCommand(t *testing.T) {
	cmd := DisplayProbeShellCommand(":0")
	if !strings.Contains(cmd, "xdpyinfo") || !strings.Contains(cmd, "DISPLAY=':0'") {
		t.Fatalf("unexpected probe command: %s", cmd)
	}
}
