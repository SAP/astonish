package browser

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSandboxRecordingNeverStatsHost(t *testing.T) {
	p := filepath.Join(t.TempDir(), "host.mp4")
	if err := os.WriteFile(p, []byte("host-only-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	m := NewManager(DefaultConfig())
	m.SandboxEnabled = true
	m.recording = &activeRecording{path: p, startedAt: time.Now()}
	_, err := m.StopRecording()
	if err == nil {
		t.Fatal("sandbox recording must require session metadata transport, not stat a host path")
	}
}

func TestSandboxRecordingMetadataUsesSession(t *testing.T) {
	for _, fail := range []bool{false, true} {
		m := NewManager(DefaultConfig())
		m.SandboxEnabled = true
		m.SetSessionID("owner")
		stopped := false
		m.recording = &activeRecording{path: "/workspace/recordings/demo.mp4", startedAt: time.Now(), stopFn: func() error { stopped = true; return nil }}
		m.StatArtifactFunc = func(ctx context.Context, id, p string) (int64, error) {
			if !stopped || id != "owner" || p != "/workspace/recordings/demo.mp4" {
				t.Fatal("incorrect session metadata request")
			}
			if fail {
				return 0, errors.New("missing sandbox artifact")
			}
			return 123, nil
		}
		res, err := m.StopRecording()
		if fail {
			if err == nil {
				t.Fatal("metadata failure hidden")
			}
		} else if err != nil || res.SizeBytes != 123 {
			t.Fatalf("metadata %+v %v", res, err)
		}
	}
}

func TestSessionRebindStopsRecording(t *testing.T) {
	m := NewManager(DefaultConfig())
	m.SetSessionID("one")
	stopped := false
	m.recording = &activeRecording{path: "/workspace/one.mp4", startedAt: time.Now(), stopFn: func() error { stopped = true; return nil }}
	m.EnsureSessionID("two")
	if !stopped || m.RecordingStatus().Recording {
		t.Fatal("recording crossed session rebind")
	}
}
