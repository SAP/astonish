package sandbox

import (
	"errors"
	"testing"
)

func TestNewBackend_EmptyKindUsesDocker(t *testing.T) {
	_, err := NewBackend(BackendFactoryConfig{
		Sessions:  newTestRegistry(t),
		Templates: &TemplateRegistry{},
	})
	if !errors.Is(err, ErrBackendKindUnavailable) {
		t.Errorf("empty kind: got %v, want ErrBackendKindUnavailable (docker not linked)", err)
	}
}

func TestNewBackend_IncusAliasesToDocker(t *testing.T) {
	_, err := NewBackend(BackendFactoryConfig{
		Kind:      BackendKindIncus,
		Sessions:  newTestRegistry(t),
		Templates: &TemplateRegistry{},
	})
	if !errors.Is(err, ErrBackendKindUnavailable) {
		t.Errorf("incus alias: got %v, want ErrBackendKindUnavailable (docker not linked)", err)
	}
}

func TestNewBackend_DockerRequiresImport(t *testing.T) {
	_, err := NewBackend(BackendFactoryConfig{Kind: BackendKindDocker})
	if !errors.Is(err, ErrBackendKindUnavailable) {
		t.Errorf("docker: got %v, want ErrBackendKindUnavailable", err)
	}
}

func TestNewBackend_K8sRequiresImport(t *testing.T) {
	_, err := NewBackend(BackendFactoryConfig{Kind: BackendKindK8s})
	if !errors.Is(err, ErrBackendKindUnavailable) {
		t.Errorf("k8s: got %v, want ErrBackendKindUnavailable", err)
	}
}

func TestNewBackend_MockRequiresImport(t *testing.T) {
	_, err := NewBackend(BackendFactoryConfig{Kind: BackendKindMock})
	if !errors.Is(err, ErrBackendKindUnavailable) {
		t.Errorf("mock: got %v, want ErrBackendKindUnavailable", err)
	}
}

func TestNewBackend_UnknownKind(t *testing.T) {
	_, err := NewBackend(BackendFactoryConfig{Kind: BackendKind("bogus")})
	if !errors.Is(err, ErrBackendKindUnknown) {
		t.Errorf("bogus: got %v, want ErrBackendKindUnknown", err)
	}
}

func TestNodeClientPool_GetBackend_MissingDeps(t *testing.T) {
	pool := NewNodeClientPool(nil, nil, "", nil)
	if pool.GetBackend() != nil {
		t.Error("GetBackend() should be nil when no backend was attached")
	}
}
