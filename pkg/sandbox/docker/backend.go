// Package docker implements the sandbox.Backend interface for plain Docker
// containers with OverlayFS template layering (unified Linux/macOS sandbox).
//
// The Docker backend replicates the overlay-based approach proven in the K8s
// backend, using Docker containers instead of Kubernetes pods. Template layers
// are stored as directories on the host filesystem (or Docker bind-mounts),
// and the sandbox-base entrypoint composes the overlay rootfs inside each
// container using the same fuse-overlayfs mechanism as K8s.
//
// This backend:
//   - Manages containers via docker run / exec / cp / stop / rm
//   - Stores template layers as directories in LayersDir (host filesystem)
//   - Uses OverlayFS (fuse or kernel) inside the container rootfs
//   - Provides org isolation via Docker networks
//   - Works identically on Linux, macOS (Docker Desktop), and Windows (WSL2)
//
// Registration: importing this package (directly or via a blank _ import)
// calls init(), which registers the factory for BackendKindDocker. After that
// sandbox.NewBackend({Kind: sandbox.BackendKindDocker, ...}) works.
package docker

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/SAP/astonish/pkg/sandbox"
)

// OverlayMode selects how the entrypoint composes the rootfs overlay.
// Mirrors k8s.OverlayMode — defined separately to avoid an import cycle
// back into pkg/sandbox/k8s.
type OverlayMode string

const (
	// OverlayModeFuse runs fuse-overlayfs inside the container.
	// Portable; works without kernel CAP_SYS_ADMIN.
	OverlayModeFuse OverlayMode = "fuse"

	// OverlayModeKernel runs native `mount -t overlay`. Faster, but
	// requires either privileged mode or a supporting kernel + runtime.
	OverlayModeKernel OverlayMode = "kernel"

	// OverlayModeAuto tries kernel first and falls back to fuse.
	OverlayModeAuto OverlayMode = "auto"
)

// Config bundles the DockerBackend dependencies. Fields are validated by New.
type Config struct {
	// Sessions is the session registry. Required.
	Sessions *sandbox.SessionRegistry

	// SandboxImage is the OCI image used for new containers.
	// Default: ghcr.io/sap/astonish-sandbox-base:latest
	SandboxImage string

	// OverlayMode selects the overlay driver: "fuse", "kernel", or "auto".
	// Default: OverlayModeFuse (most portable; works on macOS via Docker Desktop).
	OverlayMode OverlayMode

	// LayersDir is the host path where template layers are stored as directories.
	// Each layer subdirectory is named by the content-addressed SHA-256 of its rootfs.
	// Required.
	LayersDir string

	// UppersDir is the host path where session upper layers are stored.
	// Each session gets a subdirectory named by its SessionID.
	// Required.
	UppersDir string

	// ContainerRuntimePath is the absolute path to the docker binary.
	// Default: "docker" (resolved via PATH).
	ContainerRuntimePath string

	// Labels are extra labels attached to all containers created by this backend.
	Labels map[string]string
}

func (c *Config) applyDefaults() {
	if c.SandboxImage == "" {
		if img := os.Getenv("ASTONISH_SANDBOX_IMAGE"); img != "" {
			c.SandboxImage = img
		} else {
			c.SandboxImage = "ghcr.io/sap/astonish-sandbox-base:latest"
		}
	}
	if c.OverlayMode == "" {
		c.OverlayMode = OverlayModeFuse
	}
	if c.ContainerRuntimePath == "" {
		c.ContainerRuntimePath = "docker"
	}
	// Default storage directories under the Astonish config directory.
	// This mirrors the convention used by the daemon's layer migration
	// (pkg/daemon/run.go) which looks for layers at <configDir>/sandbox/layers.
	if c.LayersDir == "" || c.UppersDir == "" {
		configDir, err := os.UserConfigDir()
		if err == nil {
			base := filepath.Join(configDir, "astonish", "sandbox")
			if c.LayersDir == "" {
				c.LayersDir = filepath.Join(base, "layers")
			}
			if c.UppersDir == "" {
				c.UppersDir = filepath.Join(base, "uppers")
			}
		}
	}
}

// DockerBackend is the Backend implementation backed by plain Docker containers
// with OverlayFS rootfs composition. Zero value is not usable; construct via New.
type DockerBackend struct {
	cfg Config

	// mu protects networkRegistry.
	mu sync.RWMutex

	// networkRegistry tracks org Docker networks that have been created,
	// for idempotency of EnsureOrgNetwork.
	networkRegistry map[string]bool

	// startedAt records construction time for Health reporting.
	startedAt time.Time
}

// New constructs a DockerBackend from a Config. Returns an error if required
// fields are missing or the storage directories cannot be created.
func New(cfg Config) (*DockerBackend, error) {
	cfg.applyDefaults()

	if cfg.Sessions == nil {
		return nil, errors.New("sandbox/docker: Sessions registry is required")
	}
	if cfg.LayersDir == "" {
		return nil, errors.New("sandbox/docker: LayersDir is required")
	}
	if cfg.UppersDir == "" {
		return nil, errors.New("sandbox/docker: UppersDir is required")
	}

	// Ensure storage directories exist so later operations never race against
	// first-use creation.
	if err := os.MkdirAll(cfg.LayersDir, 0o755); err != nil {
		return nil, fmt.Errorf("sandbox/docker: create layers dir %q: %w", cfg.LayersDir, err)
	}
	if err := os.MkdirAll(cfg.UppersDir, 0o755); err != nil {
		return nil, fmt.Errorf("sandbox/docker: create uppers dir %q: %w", cfg.UppersDir, err)
	}

	return &DockerBackend{
		cfg:             cfg,
		networkRegistry: make(map[string]bool),
		startedAt:       time.Now().UTC(),
	}, nil
}

// init registers DockerBackend with sandbox.NewBackend. Importing
// pkg/sandbox/docker makes BackendKindDocker available to the factory.
//
// Production path:
//
//	sandbox.NewBackend(sandbox.BackendFactoryConfig{
//	    Kind:     sandbox.BackendKindDocker,
//	    Sessions: sessRegistry,
//	    Docker:   cfg.Sandbox.Docker,  // YAML sub-config (added in Phase C)
//	})
func init() {
	sandbox.RegisterBackendFactory(sandbox.BackendKindDocker, func(fc sandbox.BackendFactoryConfig) (sandbox.Backend, error) {
		if fc.Sessions == nil {
			return nil, errors.New("sandbox/docker: BackendFactoryConfig.Sessions is required")
		}
		cfg := Config{
			Sessions:             fc.Sessions,
			SandboxImage:         fc.Docker.SandboxImage,
			OverlayMode:          OverlayMode(fc.Docker.OverlayMode),
			LayersDir:            fc.Docker.LayersDir,
			UppersDir:            fc.Docker.UppersDir,
			ContainerRuntimePath: fc.Docker.ContainerRuntimePath,
		}
		return New(cfg)
	})
}

var _ sandbox.Backend = (*DockerBackend)(nil)

// ---------------------------------------------------------------------------
// Diagnostics (§3.6)
// ---------------------------------------------------------------------------

// Kind returns the backend kind identifier.
func (db *DockerBackend) Kind() sandbox.BackendKind {
	return sandbox.BackendKindDocker
}

// Capabilities reports Docker backend feature flags.
func (db *DockerBackend) Capabilities() sandbox.BackendCapabilities {
	return sandbox.BackendCapabilities{
		Kind:                 sandbox.BackendKindDocker,
		SupportsFastClone:    true,  // OverlayFS upper-layer copy is fast
		SupportsPortExpose:   true,  // docker port mapping
		SupportsOrgIsolation: true,  // per-org Docker networks
		SupportsLiveEvict:    false, // not implemented yet
	}
}

// ServerArchitecture returns the architecture of Docker-managed containers.
// On macOS with Apple Silicon, Docker Desktop uses arm64 natively.
// Falls back to amd64 as the most common server architecture.
func (db *DockerBackend) ServerArchitecture() string {
	switch runtime.GOARCH {
	case "arm64":
		return "arm64"
	default:
		return "amd64"
	}
}

// Health performs a connectivity check against the Docker daemon.
func (db *DockerBackend) Health(ctx context.Context) (*sandbox.BackendHealth, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	out, err := runDocker(ctx, db.cfg.ContainerRuntimePath, "version", "--format", "{{.Server.Version}}")
	if err != nil {
		return &sandbox.BackendHealth{
			Healthy:   false,
			Reason:    fmt.Sprintf("docker version failed: %v", err),
			CheckedAt: time.Now().UTC(),
		}, nil
	}

	version := strings.TrimSpace(string(out))
	return &sandbox.BackendHealth{
		Healthy:   true,
		CheckedAt: time.Now().UTC(),
		Details: map[string]string{
			"docker_version": version,
			"uptime_seconds": fmt.Sprintf("%.0f", time.Since(db.startedAt).Seconds()),
		},
	}, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// containerName returns a collision-resistant Docker name for a session.
func containerName(sessionID string) string {
	const prefix = "astonish-session-"
	sum := sha256.Sum256([]byte(sessionID))
	suffix := fmt.Sprintf("%x", sum[:6])
	clean := make([]byte, 0, len(sessionID))
	for i := 0; i < len(sessionID); i++ {
		c := sessionID[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
			clean = append(clean, c)
		default:
			clean = append(clean, '-')
		}
	}
	label := strings.Trim(string(clean), "-")
	const maxLabel = 20
	if len(label) > maxLabel {
		label = label[:maxLabel]
	}
	label = strings.Trim(label, "-")
	if label == "" {
		label = "s"
	}
	return prefix + label + "-" + suffix
}

// layerDir is the host directory for a content-addressed layer (contains rootfs/).
func (db *DockerBackend) layerDir(layerID string) string {
	return filepath.Join(db.cfg.LayersDir, layerID)
}

func (db *DockerBackend) layerRootfs(layerID string) string {
	return filepath.Join(db.layerDir(layerID), "rootfs")
}

// upperPath is the host persist directory for a session (upper.tar.zst).
func (db *DockerBackend) upperPath(sessionID string) string {
	return filepath.Join(db.cfg.UppersDir, sessionID)
}
