package sandbox

import "testing"

func TestBackendContractHelperPresent(t *testing.T) {
	// IncusBackend is removed. Docker/K8s/OpenShell/mock run RunBackendContract
	// in their own packages.
	if ErrBackendKindUnknown == nil {
		t.Fatal("ErrBackendKindUnknown must remain defined")
	}
}
