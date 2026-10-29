package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/sandbox/mock"
)

func TestCleanupCyclePreservesLiveSession(t *testing.T) {
	mb, registry, deps := cleanupTestDeps(t, "live-session")
	logger, logPath := cleanupTestLogger(t)

	runCleanupCycle(context.Background(), cleanupTestConfig(), func(context.Context) (map[string]bool, error) {
		return map[string]bool{"live-session": true}, nil
	}, nil, logger, deps)

	if calls := mb.DestroySessionCalls(); len(calls) != 0 {
		t.Fatalf("DestroySession calls = %v, want none", calls)
	}
	assertCleanupLogContains(t, logger, logPath, "live=1 pruned=0")
	if got := registry.Get("live-session"); got == nil {
		t.Fatal("live registry entry was removed")
	}
}

func TestCleanupCycleAuthoritativeEmptyPrunesOrphan(t *testing.T) {
	mb, _, deps := cleanupTestDeps(t, "orphan-session")
	logger, logPath := cleanupTestLogger(t)

	runCleanupCycle(context.Background(), cleanupTestConfig(), func(context.Context) (map[string]bool, error) {
		return map[string]bool{}, nil
	}, nil, logger, deps)

	if calls := mb.DestroySessionCalls(); len(calls) != 1 || calls[0] != "orphan-session" {
		t.Fatalf("DestroySession calls = %v, want [orphan-session]", calls)
	}
	assertCleanupLogContains(t, logger, logPath, "live=0 pruned=1")
}

func TestCleanupCycleEnumerationFailureSkipsThenHealthyCyclePrunes(t *testing.T) {
	mb, _, deps := cleanupTestDeps(t, "retry-orphan")
	logger, logPath := cleanupTestLogger(t)

	runCleanupCycle(context.Background(), cleanupTestConfig(), func(context.Context) (map[string]bool, error) {
		return nil, errors.New("tenant scan failed")
	}, nil, logger, deps)
	if calls := mb.DestroySessionCalls(); len(calls) != 0 {
		t.Fatalf("DestroySession calls after failed enumeration = %v, want none", calls)
	}
	assertCleanupLogContains(t, logger, logPath, "Skipped orphan pruning")

	runCleanupCycle(context.Background(), cleanupTestConfig(), func(context.Context) (map[string]bool, error) {
		return map[string]bool{}, nil
	}, nil, logger, deps)
	if calls := mb.DestroySessionCalls(); len(calls) != 1 || calls[0] != "retry-orphan" {
		t.Fatalf("DestroySession calls after healthy retry = %v, want [retry-orphan]", calls)
	}
	assertCleanupLogContains(t, logger, logPath, "pruned=1")
}

func cleanupTestDeps(t *testing.T, sessionID string) (*mock.MockBackend, *sandbox.SessionRegistry, cleanupCycleDeps) {
	t.Helper()
	st, err := sandbox.NewLocalSessionStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewLocalSessionStore: %v", err)
	}
	registry := sandbox.NewSessionRegistryFromStore(st)
	if err := registry.Put(sessionID, "pod-"+sessionID, "@base"); err != nil {
		t.Fatalf("registry.Put: %v", err)
	}
	mb := mock.New()
	if _, err := mb.CreateSession(context.Background(), sandbox.SessionSpec{SessionID: sessionID}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	return mb, registry, cleanupCycleDeps{
		loadConfig: func() (*config.AppConfig, error) { return nil, errors.New("use supplied config") },
		backend: func(*config.AppConfig) (sandbox.Backend, func(), error) {
			return mb, nil, nil
		},
		registry: func() (*sandbox.SessionRegistry, error) { return registry, nil },
	}
}

func cleanupTestConfig() *config.AppConfig {
	enabled := true
	return &config.AppConfig{Sandbox: config.SandboxConfig{Enabled: &enabled}}
}

func cleanupTestLogger(t *testing.T) (*Logger, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cleanup.log")
	logger, err := NewLogger(path)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	t.Cleanup(func() { _ = logger.Close() })
	return logger, path
}

func assertCleanupLogContains(t *testing.T, logger *Logger, path, want string) {
	t.Helper()
	logger.mu.Lock()
	if logger.file != nil {
		_ = logger.file.Sync()
	}
	logger.mu.Unlock()
	data, err := readLogFile(path)
	if err != nil {
		t.Fatalf("read cleanup log: %v", err)
	}
	if !strings.Contains(data, want) {
		t.Fatalf("cleanup log %q does not contain %q", data, want)
	}
}

func readLogFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	return string(data), err
}
