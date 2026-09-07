// BackendFromAppConfig builds a sandbox.Backend directly from an
// *config.AppConfig. It is the one-stop helper used by tools that want to
// talk to a sandbox backend without carrying around Docker / kubeconfig wiring.
//
// Precedence for the kind selector:
//
//  1. Explicit cfg.Sandbox.Backend ("docker" / "k8s" / "openshell" / "mock").
//  2. Default: "docker" (empty and legacy "incus" alias here).
//
// Required registries are constructed via NewSessionRegistry /
// NewTemplateRegistry; both honour platform/personal mode automatically.

package sandbox

import (
	"errors"
	"fmt"

	"github.com/SAP/astonish/pkg/config"
)

// BackendFromAppConfig returns a Backend plus a cleanup func. Cleanup may
// be nil; callers should defer it when non-nil.
//
// The returned Backend is ready for production use: for the k8s kind it
// has a real clientset and rest.Config attached, loaded via the standard
// kubeconfig ladder (see pkg/sandbox/k8s.LoadConfigOptions).
//
// Errors are actionable: missing kubeconfig, Docker engine down,
// invalid backend kind, etc. Callers should surface them verbatim to the
// operator.
func BackendFromAppConfig(appCfg *config.AppConfig) (Backend, func(), error) {
	return BackendFromAppConfigWithSessions(appCfg, nil)
}

// BackendFromAppConfigWithSessions is like BackendFromAppConfig but allows
// the caller to inject a pre-built SessionRegistry. This is critical for
// platform-mode K8s deployments with multiple API replicas: each request
// should use a pgstore-backed registry so that session records are shared
// across all replicas rather than being siloed in pod-local JSON files.
//
// If sessRegistry is nil, falls back to NewSessionRegistry() (local JSON).
func BackendFromAppConfigWithSessions(appCfg *config.AppConfig, sessRegistry *SessionRegistry) (Backend, func(), error) {
	if appCfg == nil {
		return nil, nil, errors.New("sandbox: nil app config")
	}

	kind := BackendKind(appCfg.Sandbox.BackendKind())

	if sessRegistry == nil {
		var err error
		sessRegistry, err = NewSessionRegistry()
		if err != nil {
			return nil, nil, fmt.Errorf("sandbox: session registry: %w", err)
		}
	}
	tplRegistry, err := NewTemplateRegistry()
	if err != nil {
		return nil, nil, fmt.Errorf("sandbox: template registry: %w", err)
	}

	limits := EffectiveLimits(&appCfg.Sandbox)

	switch kind {
	case BackendKindDocker:
		b, err := NewBackend(BackendFactoryConfig{
			Kind:       BackendKindDocker,
			Sessions:   sessRegistry,
			Templates:  tplRegistry,
			DefaultLim: &limits,
		})
		if err != nil {
			return nil, nil, err
		}
		return b, nil, nil

	case BackendKindIncus:
		SetSandboxConfig(&appCfg.Sandbox)
		client, err := SetupSandboxRuntime()
		if err != nil {
			return nil, nil, fmt.Errorf("sandbox: incus runtime: %w", err)
		}
		b, err := NewBackend(BackendFactoryConfig{
			Kind:       BackendKindIncus,
			Client:     client,
			Sessions:   sessRegistry,
			Templates:  tplRegistry,
			DefaultLim: &limits,
		})
		if err != nil {
			return nil, nil, err
		}
		return b, nil, nil

	case BackendKindK8s:
		b, err := NewBackend(BackendFactoryConfig{
			Kind:       BackendKindK8s,
			Sessions:   sessRegistry,
			Templates:  tplRegistry,
			DefaultLim: &limits,
			K8s:        appCfg.Sandbox.Kubernetes,
		})
		if err != nil {
			return nil, nil, err
		}
		return b, nil, nil

	case BackendKindMock:
		b, err := NewBackend(BackendFactoryConfig{
			Kind:       BackendKindMock,
			Sessions:   sessRegistry,
			Templates:  tplRegistry,
			DefaultLim: &limits,
		})
		if err != nil {
			return nil, nil, err
		}
		return b, nil, nil

	case BackendKindOpenShell:
		b, err := NewBackend(BackendFactoryConfig{
			Kind:       BackendKindOpenShell,
			Sessions:   sessRegistry,
			Templates:  tplRegistry,
			DefaultLim: &limits,
			OpenShell:  appCfg.Sandbox.OpenShell,
		})
		if err != nil {
			return nil, nil, err
		}
		return b, nil, nil

	default:
		return nil, nil, fmt.Errorf("sandbox: unsupported backend kind %q", kind)
	}
}
