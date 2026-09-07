// Package docker_test — unit tests for the Docker sandbox backend.
//
// These tests exercise construction, factory registration, config validation,
// and the pure-accessor methods (Kind, Capabilities, ServerArchitecture).
// Tests that require a live Docker daemon are guarded with t.Skip and only
// run when ASTONISH_TEST_DOCKER=1 is set.
package docker_test

import (
	"context"
	"os"
	"testing"

	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/sandbox/docker"
)

// newTestRegistry returns a minimal session registry for tests.
func newTestRegistry(t *testing.T) *sandbox.SessionRegistry {
	t.Helper()
	r, err := sandbox.NewSessionRegistry()
	if err != nil {
		t.Fatalf("NewSessionRegistry: %v", err)
	}
	return r
}

// newTestConfig returns a Config wired to t.TempDir() directories.
func newTestConfig(t *testing.T, r *sandbox.SessionRegistry) docker.Config {
	t.Helper()
	return docker.Config{
		Sessions:     r,
		LayersDir:    t.TempDir(),
		UppersDir:    t.TempDir(),
		SandboxImage: "ghcr.io/sap/astonish-sandbox-base:latest",
	}
}

// ---------------------------------------------------------------------------
// Construction
// ---------------------------------------------------------------------------

func TestNew_RequiresSessions(t *testing.T) {
	_, err := docker.New(docker.Config{
		LayersDir: t.TempDir(),
		UppersDir: t.TempDir(),
	})
	if err == nil {
		t.Fatal("expected error when Sessions is nil, got nil")
	}
}

func TestNew_RequiresLayersDir(t *testing.T) {
	// LayersDir now defaults to <UserConfigDir>/astonish/sandbox/layers via
	// applyDefaults(). Verify that passing an empty LayersDir succeeds (defaults kick in).
	r, err := sandbox.NewSessionRegistry()
	if err != nil {
		t.Fatal(err)
	}
	b, err := docker.New(docker.Config{
		Sessions:  r,
		UppersDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("expected success with default LayersDir, got error: %v", err)
	}
	if b == nil {
		t.Fatal("expected non-nil backend")
	}
}

func TestNew_RequiresUppersDir(t *testing.T) {
	// UppersDir now defaults to <UserConfigDir>/astonish/sandbox/uppers via
	// applyDefaults(). Verify that passing an empty UppersDir succeeds (defaults kick in).
	r, err := sandbox.NewSessionRegistry()
	if err != nil {
		t.Fatal(err)
	}
	b, err := docker.New(docker.Config{
		Sessions:  r,
		LayersDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("expected success with default UppersDir, got error: %v", err)
	}
	if b == nil {
		t.Fatal("expected non-nil backend")
	}
}

func TestNew_CreatesDirectories(t *testing.T) {
	r, err := sandbox.NewSessionRegistry()
	if err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	layers := base + "/layers"
	uppers := base + "/uppers"

	_, err = docker.New(docker.Config{
		Sessions:  r,
		LayersDir: layers,
		UppersDir: uppers,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for _, dir := range []string{layers, uppers} {
		if _, err := os.Stat(dir); err != nil {
			t.Errorf("directory %q was not created: %v", dir, err)
		}
	}
}

func TestNew_Success(t *testing.T) {
	r := newTestRegistry(t)
	cfg := newTestConfig(t, r)
	b, err := docker.New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if b == nil {
		t.Fatal("expected non-nil DockerBackend")
	}
}

// ---------------------------------------------------------------------------
// Kind and Capabilities accessors
// ---------------------------------------------------------------------------

func TestKind(t *testing.T) {
	r := newTestRegistry(t)
	b, err := docker.New(newTestConfig(t, r))
	if err != nil {
		t.Fatal(err)
	}
	if b.Kind() != sandbox.BackendKindDocker {
		t.Errorf("Kind() = %q, want %q", b.Kind(), sandbox.BackendKindDocker)
	}
}

func TestCapabilities(t *testing.T) {
	r := newTestRegistry(t)
	b, err := docker.New(newTestConfig(t, r))
	if err != nil {
		t.Fatal(err)
	}
	caps := b.Capabilities()
	if caps.Kind != sandbox.BackendKindDocker {
		t.Errorf("Capabilities.Kind = %q, want %q", caps.Kind, sandbox.BackendKindDocker)
	}
	if !caps.SupportsFastClone {
		t.Error("expected SupportsFastClone = true")
	}
	if !caps.SupportsPortExpose {
		t.Error("expected SupportsPortExpose = true")
	}
	if !caps.SupportsOrgIsolation {
		t.Error("expected SupportsOrgIsolation = true")
	}
}

func TestServerArchitecture(t *testing.T) {
	r := newTestRegistry(t)
	b, err := docker.New(newTestConfig(t, r))
	if err != nil {
		t.Fatal(err)
	}
	arch := b.ServerArchitecture()
	if arch != "amd64" && arch != "arm64" {
		t.Errorf("ServerArchitecture() = %q, want amd64 or arm64", arch)
	}
}

// ---------------------------------------------------------------------------
// Factory registration (init())
// ---------------------------------------------------------------------------

func TestFactoryRegistration(t *testing.T) {
	// Importing pkg/sandbox/docker triggers init(), which calls
	// sandbox.RegisterBackendFactory(BackendKindDocker, ...).
	// Verify the factory is callable without a live Docker daemon.
	r := newTestRegistry(t)
	b, err := sandbox.NewBackend(sandbox.BackendFactoryConfig{
		Kind:     sandbox.BackendKindDocker,
		Sessions: r,
		Docker: sandbox.DockerRuntimeConfig{
			LayersDir: t.TempDir(),
			UppersDir: t.TempDir(),
		},
	})
	if err != nil {
		t.Fatalf("NewBackend(docker): %v", err)
	}
	if b.Kind() != sandbox.BackendKindDocker {
		t.Errorf("factory produced backend with Kind()=%q, want %q", b.Kind(), sandbox.BackendKindDocker)
	}
}

// ---------------------------------------------------------------------------
// Context cancellation (contract requirement)
// ---------------------------------------------------------------------------

func TestDockerBackendContract(t *testing.T) {
	sandbox.RunBackendContract(t, func(t *testing.T) (sandbox.Backend, string) {
		r := newTestRegistry(t)
		b, err := docker.New(newTestConfig(t, r))
		if err != nil {
			t.Fatal(err)
		}
		return b, ""
	})
}

func TestCreateSession_RespectsContextCancel(t *testing.T) {
	r := newTestRegistry(t)
	b, err := docker.New(newTestConfig(t, r))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel

	_, err = b.CreateSession(ctx, sandbox.SessionSpec{
		SessionID: "test-ctx",
		Type:      sandbox.SessionTypeChat,
	})
	if err == nil {
		t.Error("expected error for cancelled context, got nil")
	}
}

// ---------------------------------------------------------------------------
// applyDefaults — ASTONISH_SANDBOX_IMAGE env var override
// ---------------------------------------------------------------------------

func TestApplyDefaults_SandboxImageEnvVar(t *testing.T) {
	const customImage = "my.registry.io/custom-sandbox:v2"
	t.Setenv("ASTONISH_SANDBOX_IMAGE", customImage)

	r := newTestRegistry(t)
	// Pass empty SandboxImage so applyDefaults must fill it from the env var.
	b, err := docker.New(docker.Config{
		Sessions:  r,
		LayersDir: t.TempDir(),
		UppersDir: t.TempDir(),
		// SandboxImage intentionally omitted.
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if b == nil {
		t.Fatal("expected non-nil backend")
	}
	// We cannot inspect cfg.SandboxImage directly (unexported), but the backend
	// must have been constructed without error when the env var is set.
	// The live-docker suite validates actual container creation with the image.
}

// ---------------------------------------------------------------------------
// Live Docker tests (require ASTONISH_TEST_DOCKER=1)
// ---------------------------------------------------------------------------

func TestHealth_LiveDocker(t *testing.T) {
	if os.Getenv("ASTONISH_TEST_DOCKER") == "" {
		t.Skip("set ASTONISH_TEST_DOCKER=1 to run live Docker tests")
	}
	r := newTestRegistry(t)
	b, err := docker.New(newTestConfig(t, r))
	if err != nil {
		t.Fatal(err)
	}
	health, err := b.Health(context.Background())
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if !health.Healthy {
		t.Errorf("Health.Healthy = false, reason: %s", health.Reason)
	}
}
