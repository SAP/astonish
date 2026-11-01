// Package openshell provides the OpenShell-backed implementation of the
// sandbox.Backend interface.
//
// The OpenShell backend creates sandboxes via the NVIDIA OpenShell Gateway,
// which manages pod lifecycle through the kubernetes-sigs/agent-sandbox CRD.
// The OpenShell supervisor (sideloaded by the gateway into each sandbox pod)
// enforces Landlock, seccomp, network namespace, and OCSF audit policies.
//
// Astonish is a *client* of the OpenShell gateway — it does NOT manage the
// gateway, supervisor, or Agent Sandbox controller. Those are deployed
// independently using NVIDIA's official Helm chart.
//
// Reference: docs/architecture/openshell-sandbox-backend.md

package openshell

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/sandbox"
)

// ErrNotImplementedYet is returned by methods whose implementation has
// not yet landed. Callers MAY check for this error with errors.Is to
// gate feature rollout.
var ErrNotImplementedYet = errors.New("sandbox/openshell: not implemented yet")

// Config bundles the OpenShellBackend dependencies.
type Config struct {
	// Sessions is the session registry. Required.
	Sessions *sandbox.SessionRegistry

	// Gateway is the OpenShell Gateway client. When nil, session
	// lifecycle and exec methods return ErrNotImplementedYet (useful
	// for skeleton/test construction where no gateway is available).
	Gateway GatewayClient

	// GatewayAddr is the gRPC address of the OpenShell Gateway.
	// Default: "openshell.openshell.svc.cluster.local:8080"
	GatewayAddr string

	// GatewayTLS enables TLS for the gRPC connection to the gateway.
	GatewayTLS bool

	// ClientCertPath / ClientKeyPath / CACertPath configure mTLS.
	ClientCertPath string
	ClientKeyPath  string
	CACertPath     string

	// AuthToken is a static bearer token for development setups.
	AuthToken string

	// SandboxImage is the container image for OpenShell sandbox pods.
	// Default: "ghcr.io/sap/astonish-sandbox-openshell:latest"
	SandboxImage string

	// AppConfig stores the full OpenShell config from app_config.yaml.
	AppConfig config.SandboxOpenShellConfig
}

func (c *Config) applyDefaults() {
	if c.GatewayAddr == "" {
		c.GatewayAddr = "openshell.openshell.svc.cluster.local:8080"
	}
	if c.SandboxImage == "" {
		c.SandboxImage = "ghcr.io/sap/astonish-sandbox-openshell:latest"
	}
}

// OpenShellBackend satisfies sandbox.Backend by routing session lifecycle
// and exec operations through the OpenShell Gateway.
type OpenShellBackend struct {
	cfg       Config
	sessions  *sandbox.SessionRegistry
	gateway   GatewayClient
	startedAt time.Time
}

// New creates a new OpenShellBackend. Returns an error if required
// configuration is missing.
func New(cfg Config) (*OpenShellBackend, error) {
	if cfg.Sessions == nil {
		return nil, errors.New("sandbox/openshell: Sessions registry is required")
	}
	cfg.applyDefaults()
	return &OpenShellBackend{
		cfg:       cfg,
		sessions:  cfg.Sessions,
		gateway:   cfg.Gateway,
		startedAt: time.Now().UTC(),
	}, nil
}

// init registers OpenShellBackend with sandbox.NewBackend.
func init() {
	sandbox.RegisterBackendFactory(sandbox.BackendKindOpenShell, func(fc sandbox.BackendFactoryConfig) (sandbox.Backend, error) {
		if fc.Sessions == nil {
			return nil, errors.New("sandbox/openshell: BackendFactoryConfig.Sessions is required")
		}
		osCfg := fc.OpenShell
		cfg := Config{
			Sessions:       fc.Sessions,
			GatewayAddr:    osCfg.GatewayAddr,
			GatewayTLS:     osCfg.OpenShellGatewayTLS(),
			ClientCertPath: osCfg.ClientCertPath,
			ClientKeyPath:  osCfg.ClientKeyPath,
			CACertPath:     osCfg.CACertPath,
			AuthToken:      osCfg.AuthToken,
			SandboxImage:   osCfg.SandboxImage,
			AppConfig:      osCfg,
		}
		// Apply defaults before creating the gRPC client.
		cfg.applyDefaults()

		// Create the real gRPC gateway client.
		gatewayClient, err := NewGRPCGatewayClient(GRPCClientConfig{
			Addr:           cfg.GatewayAddr,
			TLS:            cfg.GatewayTLS,
			ClientCertPath: cfg.ClientCertPath,
			ClientKeyPath:  cfg.ClientKeyPath,
			CACertPath:     cfg.CACertPath,
			AuthToken:      cfg.AuthToken,
		})
		if err != nil {
			return nil, fmt.Errorf("sandbox/openshell: create gateway client: %w", err)
		}
		cfg.Gateway = gatewayClient

		return New(cfg)
	})
}

// ---------------------------------------------------------------------------
// Diagnostics
// ---------------------------------------------------------------------------

// Kind returns BackendKindOpenShell.
func (b *OpenShellBackend) Kind() sandbox.BackendKind {
	return sandbox.BackendKindOpenShell
}

func (b *OpenShellBackend) ArtifactReadsConfined() bool { return b != nil && b.sessions != nil }
func (b *OpenShellBackend) PullArtifact(ctx context.Context, sessionID, path string) (io.ReadCloser, error) {
	return b.PullFile(ctx, sessionID, path)
}

// ServerArchitecture returns "amd64" (conservative default).
func (b *OpenShellBackend) ServerArchitecture() string {
	return "amd64"
}

// Capabilities advertises the designed feature set.
func (b *OpenShellBackend) Capabilities() sandbox.BackendCapabilities {
	return sandbox.BackendCapabilities{
		Kind:                 sandbox.BackendKindOpenShell,
		SupportsLiveEvict:    true,
		SupportsFastClone:    true,
		SupportsPortExpose:   true,
		SupportsOrgIsolation: true,
	}
}

// Health reports readiness. Currently returns healthy if the config is
// valid; future slices will probe Gateway connectivity.
func (b *OpenShellBackend) Health(ctx context.Context) (*sandbox.BackendHealth, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	details := map[string]string{
		"gateway_addr":  b.cfg.GatewayAddr,
		"gateway_tls":   fmt.Sprintf("%t", b.cfg.GatewayTLS),
		"sandbox_image": b.cfg.SandboxImage,
		"started_at":    b.startedAt.Format(time.RFC3339),
	}
	return &sandbox.BackendHealth{
		Healthy:   true,
		Reason:    "openshell backend configured (gateway connectivity check pending implementation)",
		CheckedAt: time.Now().UTC(),
		Details:   details,
	}, nil
}

// ---------------------------------------------------------------------------
// Accessors — used by chat_factory to wire browser-in-sandbox callbacks
// ---------------------------------------------------------------------------

// Gateway returns the underlying GatewayClient for direct operations
// (browser startup, CDP tunnel, network policy).
func (b *OpenShellBackend) Gateway() GatewayClient { return b.gateway }

// Sessions returns the session registry for session lookups and activity tracking.
func (b *OpenShellBackend) Sessions() *sandbox.SessionRegistry { return b.sessions }

// ---------------------------------------------------------------------------
// Session lifecycle — implemented in session.go
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Exec and file I/O — implemented in exec.go
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Templates — implemented in template.go
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Networking — implemented in network.go
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Fleet — implemented in fleet.go
// ---------------------------------------------------------------------------

// Ensure compile-time interface compliance.
var _ sandbox.Backend = (*OpenShellBackend)(nil)
