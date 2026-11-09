//go:build e2e

package e2eboot

import "testing"

func TestRetainedHomeKubernetesInputs(t *testing.T) {
	valid := map[string]string{
		"ASTONISH_E2E_DEDICATED": "1", "ASTONISH_E2E_SANDBOX_NAMESPACE": "astonish-e2e-home", "ASTONISH_E2E_CONTROL_PLANE_NAMESPACE": "astonish-e2e-control",
		"KUBECONFIG": "/explicit/disposable/config", "ASTONISH_E2E_BACKEND": "sqlite", "OPENAI_API_KEY": "test-placeholder",
		"ASTONISH_SANDBOX_IMAGE": "example.invalid/acceptance@sha256:explicit", "ASTONISH_E2E_HOMES_PVC": "retained-homes",
	}
	if err := ValidateRetainedHomeKubernetesInputs(func(k string) string { return valid[k] }); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"ASTONISH_E2E_DEDICATED", "ASTONISH_E2E_SANDBOX_NAMESPACE", "ASTONISH_E2E_CONTROL_PLANE_NAMESPACE", "KUBECONFIG", "OPENAI_API_KEY", "ASTONISH_SANDBOX_IMAGE", "ASTONISH_E2E_HOMES_PVC"} {
		t.Run("missing-"+key, func(t *testing.T) {
			if err := ValidateRetainedHomeKubernetesInputs(func(k string) string {
				if k == key {
					return ""
				}
				return valid[k]
			}); err == nil {
				t.Fatal("missing release infrastructure accepted")
			}
		})
	}
	for _, pvc := range []string{"astonish-layers", "astonish-uppers"} {
		t.Run(pvc, func(t *testing.T) {
			if err := ValidateRetainedHomeKubernetesInputs(func(k string) string {
				if k == "ASTONISH_E2E_HOMES_PVC" {
					return pvc
				}
				return valid[k]
			}); err == nil {
				t.Fatal("shared claim accepted")
			}
		})
	}
}
