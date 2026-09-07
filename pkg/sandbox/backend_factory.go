// Package sandbox — Backend factory.
//
// This file provides sandbox.NewBackend, the central factory that picks a
// Backend implementation based on configuration. Docker OverlayFS is the
// local default via BackendFromAppConfig; K8s, OpenShell, and mock register
// through RegisterBackendFactory.

package sandbox

import (
	"errors"
	"fmt"
	"sync"

	"github.com/SAP/astonish/pkg/config"
)

// BackendFactoryFunc constructs a Backend from a BackendFactoryConfig. It
// is the registration hook used by out-of-tree backend implementations
// (e.g., pkg/sandbox/mock, pkg/sandbox/k8s) to plug themselves into
// NewBackend without forcing pkg/sandbox to import them — which would
// create an import cycle because those packages already import
// pkg/sandbox.
type BackendFactoryFunc func(cfg BackendFactoryConfig) (Backend, error)

var (
	registeredFactoriesMu sync.RWMutex
	registeredFactories   = map[BackendKind]BackendFactoryFunc{}
)

// RegisterBackendFactory installs fn as the constructor for backends of
// the given kind. It is intended to be called from an init() function in
// a backend's implementation package. Passing a nil fn unregisters the
// kind. Re-registering an existing kind replaces the prior factory; this
// lets tests substitute a mock or fake.
//
// RegisterBackendFactory is safe for concurrent use, but callers typically
// invoke it once during process startup.
func RegisterBackendFactory(kind BackendKind, fn BackendFactoryFunc) {
	registeredFactoriesMu.Lock()
	defer registeredFactoriesMu.Unlock()
	if fn == nil {
		delete(registeredFactories, kind)
		return
	}
	registeredFactories[kind] = fn
}

func lookupBackendFactory(kind BackendKind) (BackendFactoryFunc, bool) {
	registeredFactoriesMu.RLock()
	defer registeredFactoriesMu.RUnlock()
	fn, ok := registeredFactories[kind]
	return fn, ok
}

// BackendFactoryConfig bundles the dependencies NewBackend needs. All fields
// are validated; missing required fields produce a clear error.
type BackendFactoryConfig struct {
	// Kind selects the backend implementation. Accepted values:
	//   - "" or "docker" → DockerBackend (local OverlayFS sessions)
	//   - "incus"        → treated as docker by config.Sandbox.BackendKind
	//   - "k8s"          → K8sSandboxBackend
	//   - "openshell"    → OpenShellBackend
	Kind BackendKind

	// Client is the Incus daemon client. Required for Kind == "incus".
	Client *IncusClient

	// Sessions is the session registry. Required for all kinds.
	Sessions *SessionRegistry

	// Templates is the template registry. Required for all kinds.
	Templates *TemplateRegistry

	// DefaultLim supplies default resource limits when a caller does not
	// specify them in a SessionSpec. MAY be nil.
	DefaultLim *config.SandboxLimits

	// Docker carries local Docker-backend configuration. Consulted only
	// when Kind == "docker".
	Docker DockerRuntimeConfig

	// K8s carries Kubernetes-specific configuration. Consulted only when
	// Kind == "k8s"; ignored otherwise. The struct is YAML-friendly
	// (lives in pkg/config) and is translated to a k8s.Config by the
	// registered factory in pkg/sandbox/k8s.
	K8s config.SandboxKubernetesConfig

	// OpenShell carries OpenShell-specific configuration. Consulted only
	// when Kind == "openshell"; ignored otherwise. Contains gateway
	// connection details and sandbox image configuration.
	OpenShell config.SandboxOpenShellConfig
}

// DockerRuntimeConfig is the factory payload for BackendKindDocker.
type DockerRuntimeConfig struct {
	SandboxImage         string
	OverlayMode          string
	LayersDir            string
	UppersDir            string
	ContainerRuntimePath string
}

// NewBackend constructs a Backend implementation from configuration. The
// returned Backend satisfies the full interface and may be used by any
// caller that wants to be backend-agnostic.
//
// Kinds other than the leftover in-tree Incus adapter are resolved via
// the RegisterBackendFactory hook. This keeps out-of-tree implementations
// (mock, k8s, docker) from forcing an import cycle back into pkg/sandbox.
// A backend becomes available by importing its package (which registers
// itself in its init()).
//
// Errors:
//   - ErrBackendKindUnknown: Kind is not recognized.
//   - ErrBackendKindUnavailable: Kind is recognized but not built into this
//     binary (e.g., "k8s" on a build that has not yet shipped Phase C, or
//     "mock" when pkg/sandbox/mock was not imported).
//   - Plus any validation error from the underlying constructor.
func NewBackend(cfg BackendFactoryConfig) (Backend, error) {
	kind := cfg.Kind
	if kind == "" {
		kind = BackendKindIncus
	}

	// Built-in kinds first. Incus lives in pkg/sandbox itself and cannot
	// go through the registry (it would cause a self-reference at init).
	switch kind {
	case BackendKindIncus:
		return NewIncusBackend(IncusBackendConfig{
			Client:     cfg.Client,
			Sessions:   cfg.Sessions,
			Templates:  cfg.Templates,
			DefaultLim: cfg.DefaultLim,
		})
	case BackendKindDocker, BackendKindK8s, BackendKindOpenShell, BackendKindMock:
		// Fall through to registry lookup.
	default:
		// Still try the registry so tests can register exotic kinds.
		if fn, ok := lookupBackendFactory(kind); ok {
			return fn(cfg)
		}
		return nil, fmt.Errorf("%w: %q", ErrBackendKindUnknown, kind)
	}

	if fn, ok := lookupBackendFactory(kind); ok {
		return fn(cfg)
	}

	switch kind {
	case BackendKindDocker:
		return nil, fmt.Errorf("%w: docker backend requires importing pkg/sandbox/docker", ErrBackendKindUnavailable)
	case BackendKindK8s:
		return nil, fmt.Errorf("%w: k8s backend requires importing pkg/sandbox/k8s", ErrBackendKindUnavailable)
	case BackendKindOpenShell:
		return nil, fmt.Errorf("%w: openshell backend requires importing pkg/sandbox/openshell", ErrBackendKindUnavailable)
	case BackendKindMock:
		return nil, fmt.Errorf("%w: mock backend requires importing pkg/sandbox/mock", ErrBackendKindUnavailable)
	default:
		return nil, fmt.Errorf("%w: %q", ErrBackendKindUnknown, kind)
	}
}

// ErrBackendKindUnknown is returned by NewBackend when the requested kind
// is not one of the recognized constants.
var ErrBackendKindUnknown = errors.New("sandbox: unknown backend kind")

// ErrBackendKindUnavailable is returned by NewBackend when the requested
// kind is recognized but its implementation is not compiled into this
// binary.
var ErrBackendKindUnavailable = errors.New("sandbox: backend kind not available in this build")
