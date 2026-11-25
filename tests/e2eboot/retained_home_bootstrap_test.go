//go:build e2e

package e2eboot

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
)

func TestRetainedHomeDedicatedFakeClient(t *testing.T) {
	env := retainedHomeTestEnv()
	in, err := retainedHomeKubernetesInputs(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	for _, foreign := range []bool{false, true} {
		ns, claims := retainedHomeTestResources(in)
		if foreign {
			claims[2].Labels = nil
		}
		var objects []runtime.Object
		for _, v := range ns {
			objects = append(objects, v)
		}
		for _, v := range claims {
			objects = append(objects, v)
		}
		client := fake.NewSimpleClientset(objects...)
		err := checkRetainedHomeCluster(context.Background(), in, client)
		if foreign && err == nil {
			t.Fatal("accepted unowned fake claim")
		}
		if !foreign && err != nil {
			t.Fatal(err)
		}
		for _, action := range client.Actions() {
			if action.GetVerb() != "get" {
				t.Fatalf("preflight mutated or enumerated resources: %v", action)
			}
		}
	}
}

func TestRetainedHomeDedicatedContextBeforeNetwork(t *testing.T) {
	env := retainedHomeTestEnv()
	path := filepath.Join(t.TempDir(), "kubeconfig")
	env["KUBECONFIG"] = path
	in, err := retainedHomeKubernetesInputs(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	raw := `apiVersion: v1
kind: Config
current-context: ambient
contexts:
- name: dedicated-context
  context:
    cluster: dedicated
    user: dedicated
clusters:
- name: dedicated
  cluster:
    server: https://example.invalid
users:
- name: dedicated
  user:
    token: fake-not-used
`
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := retainedHomeDedicatedClient(in); err == nil {
		t.Fatal("ambient current context accepted")
	}
}

func TestRetainedHomeGenericProviderGateUnchanged(t *testing.T) {
	env := retainedHomeTestEnv()
	if ValidateRetainedHomeKubernetesInputs(func(k string) string { return env[k] }) != nil {
		t.Fatal("no-model gate requires provider")
	}
	if ValidateSecurityPreflight(func(k string) string { return env[k] }) == nil {
		t.Fatal("generic paid-provider gate weakened")
	}
}
