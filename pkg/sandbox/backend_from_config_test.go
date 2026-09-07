package sandbox

import (
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/config"
)

// TestBackendFromAppConfig_NilAppConfig pins the nil-input error path.
func TestBackendFromAppConfig_NilAppConfig(t *testing.T) {
	_, _, err := BackendFromAppConfig(nil)
	if err == nil {
		t.Fatal("expected error for nil AppConfig")
	}
	if !strings.Contains(err.Error(), "nil app config") {
		t.Errorf("error = %v, want nil-app-config wording", err)
	}
}

// TestBackendFromAppConfig_K8sBranchTakesKubernetesSubConfig is covered
// from the k8s package's test suite (it blank-imports its own init), not
// here — pkg/sandbox deliberately does not link pkg/sandbox/k8s. See
// pkg/sandbox/k8s/backend_test.go:TestBackendFromAppConfig_K8s.

// TestBackendFromAppConfig_DefaultsToDocker: empty Backend field routes
// through the docker factory. pkg/sandbox does not import pkg/sandbox/docker,
// so the error names docker as unavailable.
func TestBackendFromAppConfig_DefaultsToDocker(t *testing.T) {
	appCfg := &config.AppConfig{}
	_, _, err := BackendFromAppConfig(appCfg)
	if err == nil {
		return
	}
	if !strings.Contains(err.Error(), "docker") {
		t.Errorf("expected docker path error, got: %v", err)
	}
}

// TestBackendFromAppConfig_UnknownKind surfaces a clear error for typos.
func TestBackendFromAppConfig_UnknownKind(t *testing.T) {
	appCfg := &config.AppConfig{}
	appCfg.Sandbox.Backend = "bogus"

	_, _, err := BackendFromAppConfig(appCfg)
	if err == nil {
		t.Fatal("expected error for unknown kind")
	}
	if !strings.Contains(err.Error(), "bogus") {
		t.Errorf("error should name the unknown kind; got: %v", err)
	}
}
