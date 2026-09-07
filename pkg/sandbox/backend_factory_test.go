package sandbox

import (
	"errors"
	"testing"
)

func TestNewBackend_DefaultsToIncus(t *testing.T) {
	b, err := NewBackend(BackendFactoryConfig{
		Client:    &IncusClient{},
		Sessions:  newTestRegistry(t),
		Templates: &TemplateRegistry{},
	})
	if err != nil {
		t.Fatalf("NewBackend: %v", err)
	}
	if b.Kind() != BackendKindIncus {
		t.Errorf("default kind = %q, want %q", b.Kind(), BackendKindIncus)
	}
}

func TestNewBackend_ExplicitIncus(t *testing.T) {
	b, err := NewBackend(BackendFactoryConfig{
		Kind:      BackendKindIncus,
		Client:    &IncusClient{},
		Sessions:  newTestRegistry(t),
		Templates: &TemplateRegistry{},
	})
	if err != nil {
		t.Fatalf("NewBackend: %v", err)
	}
	if b.Kind() != BackendKindIncus {
		t.Errorf("kind = %q, want %q", b.Kind(), BackendKindIncus)
	}
}

// TestNewBackend_K8sRequiresImport pins the contract that pkg/sandbox on its
// own does NOT link the k8s backend: callers who want it must import
// pkg/sandbox/k8s so that its init() registers with the factory. This is
// intentional isolation — pkg/sandbox/k8s pulls in k8s.io/client-go and its
// SPDY stack, which we don't want to force on every consumer.
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

// TestNewBackend_MockRequiresImport is the analogous contract for the mock
// backend: it's registered via pkg/sandbox/mock's init() and pkg/sandbox
// deliberately does not import it.
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

func TestNewBackend_PropagatesConstructorError(t *testing.T) {
	// Missing client → IncusBackend constructor returns an error; factory
	// must surface it rather than silently succeeding.
	_, err := NewBackend(BackendFactoryConfig{
		Sessions:  newTestRegistry(t),
		Templates: &TemplateRegistry{},
	})
	if err == nil {
		t.Fatal("expected error from underlying constructor, got nil")
	}
}

func TestNodeClientPool_GetBackend(t *testing.T) {
	// With full wiring the pool constructs a backend.
	pool := NewNodeClientPool(
		&IncusClient{},
		newTestRegistry(t),
		&TemplateRegistry{},
		"",
		nil,
	)
	if pool.GetBackend() == nil {
		t.Fatal("NodeClientPool.GetBackend() = nil, want non-nil Backend")
	}
	if pool.GetBackend().Kind() != BackendKindIncus {
		t.Errorf("GetBackend().Kind() = %q, want %q",
			pool.GetBackend().Kind(), BackendKindIncus)
	}
}

func TestNodeClientPool_GetBackend_MissingDeps(t *testing.T) {
	// Without the client the pool cannot build a backend; GetBackend()
	// returns nil rather than panicking.
	pool := NewNodeClientPool(nil, nil, nil, "", nil)
	if pool.GetBackend() != nil {
		t.Error("GetBackend() should be nil when dependencies are missing")
	}
}
