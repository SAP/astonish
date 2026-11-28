package e2eboot

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func retainedHomeTestEnv() map[string]string {
	return map[string]string{"ASTONISH_E2E_DEDICATED": "1", "ASTONISH_E2E_BACKEND": "postgres", "ASTONISH_E2E_SANDBOX_BACKEND": "k8s", "ASTONISH_E2E_SANDBOX_NAMESPACE": "astonish-e2e-homes", "ASTONISH_E2E_CONTROL_PLANE_NAMESPACE": "astonish-e2e-control", "KUBECONFIG": "/dedicated/kubeconfig", "ASTONISH_E2E_KUBE_CONTEXT": "dedicated-context", "ASTONISH_E2E_RESOURCE_OWNER": "astonish-e2e-homes", "ASTONISH_SANDBOX_IMAGE": "test-owned-image:current", "ASTONISH_E2E_HOMES_PVC": "astonish-e2e-homes", "ASTONISH_TEST_DSN": "postgres://fixture:unused@127.0.0.1:55432/postgres?sslmode=disable", "ASTONISH_E2E_POSTGRES_INSTANCE": "127.0.0.1:55432"}
}
func TestRetainedHomeNoModelPreflight(t *testing.T) {
	env := retainedHomeTestEnv()
	get := func(k string) string { return env[k] }
	if err := ValidateRetainedHomeKubernetesInputs(get); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"ASTONISH_E2E_DEDICATED", "ASTONISH_E2E_BACKEND", "ASTONISH_E2E_SANDBOX_BACKEND", "ASTONISH_E2E_SANDBOX_NAMESPACE", "ASTONISH_E2E_CONTROL_PLANE_NAMESPACE", "KUBECONFIG", "ASTONISH_E2E_KUBE_CONTEXT", "ASTONISH_E2E_RESOURCE_OWNER", "ASTONISH_SANDBOX_IMAGE", "ASTONISH_E2E_HOMES_PVC", "ASTONISH_TEST_DSN", "ASTONISH_E2E_POSTGRES_INSTANCE"} {
		t.Run(key, func(t *testing.T) {
			old := env[key]
			env[key] = ""
			defer func() { env[key] = old }()
			if ValidateRetainedHomeKubernetesInputs(get) == nil {
				t.Fatal("accepted ambient or incomplete prerequisite")
			}
		})
	}
	for _, entry := range [][2]string{{"KUBECONFIG", "relative"}, {"KUBECONFIG", "/a:/b"}, {"ASTONISH_E2E_KEEP_ALIVE", "1"}, {"ASTONISH_E2E_HOMES_PVC", "astonish-layers"}, {"ASTONISH_E2E_SANDBOX_NAMESPACE", "default"}, {"ASTONISH_TEST_DSN", "postgres://fixture@127.0.0.1:55432/postgres?sslmode=disable&host=production"}, {"ASTONISH_TEST_DSN", "postgres://fixture@127.0.0.1:55432/production?sslmode=disable"}} {
		old := env[entry[0]]
		env[entry[0]] = entry[1]
		if ValidateRetainedHomeKubernetesInputs(get) == nil {
			t.Errorf("accepted %s override", entry[0])
		}
		env[entry[0]] = old
	}
}
func retainedHomeTestResources(in RetainedHomeKubernetesInputs) ([]*corev1.Namespace, []*corev1.PersistentVolumeClaim) {
	var ns []*corev1.Namespace
	var claims []*corev1.PersistentVolumeClaim
	for _, name := range []string{in.Kubernetes.Namespace, in.Kubernetes.ControlPlaneNamespace} {
		ns = append(ns, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{retainedHomeOwnerLabel: in.Owner}}})
	}
	for _, name := range []string{in.Kubernetes.LayersPVCName, in.Kubernetes.UppersPVCName, in.Kubernetes.HomesPVCName} {
		claims = append(claims, &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: in.Kubernetes.Namespace, Labels: map[string]string{retainedHomeOwnerLabel: in.Owner}}, Spec: corev1.PersistentVolumeClaimSpec{VolumeName: "pv-" + name, AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany}}, Status: corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimBound}})
	}
	return ns, claims
}
func TestRetainedHomeOwnership(t *testing.T) {
	env := retainedHomeTestEnv()
	in, err := retainedHomeKubernetesInputs(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	ns, claims := retainedHomeTestResources(in)
	if err := validateRetainedHomeClaims(in, ns, claims); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(){func() { ns[0].Labels = nil }, func() { claims[2].Labels = nil }, func() { claims[2].Status.Phase = corev1.ClaimPending }, func() { claims[2].Spec.AccessModes = []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce} }, func() { claims[2].Spec.VolumeName = claims[0].Spec.VolumeName }, func() { claims[2].Namespace = "production" }} {
		ns, claims = retainedHomeTestResources(in)
		mutate()
		if validateRetainedHomeClaims(in, ns, claims) == nil {
			t.Fatal("accepted unsafe retained infrastructure")
		}
	}
	if validateRetainedHomeDatabaseReceipt("owned", "owned", "receipt", "receipt", true) != nil {
		t.Fatal("owned receipt rejected")
	}
	for _, q := range []struct {
		name, owner string
		current     bool
	}{{"foreign", "receipt", true}, {"owned", "foreign", true}, {"owned", "receipt", false}} {
		if validateRetainedHomeDatabaseReceipt(q.name, "owned", q.owner, "receipt", q.current) == nil {
			t.Fatal("foreign cleanup accepted")
		}
	}
}
func TestRetainedHomeBootstrapNoProviderOrSweep(t *testing.T) {
	// Inspect only bootstrap/seed entry points: the separate runtime readiness
	// sentinel is not a configured paid provider and cannot generate content.
	forbidden := map[string]bool{"Bootstrap": true, "BootstrapPlatformCore": true, "ValidateSecurityPreflight": true, "resolveAPIKey": true, "ResolveAPIKey": true, "seedProvider": true, "seedProviderCore": true, "writeConfig": true, "Seed": true, "SweepAll": true, "SweepAllPods": true, "DropAllDBsWithSuffix": true, "DropDBs": true, "initEmbedFunc": true, "initEmbedFuncCore": true}
	for _, path := range []string{"sandbox_k8s_home.go", "retained_home_bootstrap.go"} {
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			if fn.Name.Name != "BootstrapRetainedHomeKubernetes" && fn.Name.Name != "NewRetainedHomeBotRuntime" && path == "sandbox_k8s_home.go" {
				continue
			}
			ast.Inspect(fn, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				name := ""
				switch v := call.Fun.(type) {
				case *ast.Ident:
					name = v.Name
				case *ast.SelectorExpr:
					name = v.Sel.Name
				}
				if forbidden[name] {
					t.Errorf("%s calls forbidden %s", fn.Name.Name, name)
				}
				return true
			})
		}
	}
}
