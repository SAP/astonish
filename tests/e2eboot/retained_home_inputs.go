package e2eboot

import (
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/SAP/astonish/pkg/config"
	corev1 "k8s.io/api/core/v1"
)

const retainedHomeOwnerLabel = "astonish.sap.com/e2e-owner"

// RetainedHomeKubernetesInputs contains only explicit dedicated fixture inputs.
// Acknowledgements and labels do not prove physical isolation; operators must
// provision and authorize the disposable cluster/storage and entire PG instance.
type RetainedHomeKubernetesInputs struct {
	Kubernetes config.SandboxKubernetesConfig
	DSN        string
	Owner      string
}

func retainedHomeKubernetesInputs(getenv func(string) string) (RetainedHomeKubernetesInputs, error) {
	var out RetainedHomeKubernetesInputs
	if getenv("ASTONISH_E2E_DEDICATED") != "1" {
		return out, fmt.Errorf("ASTONISH_E2E_DEDICATED=1 required")
	}
	if getenv("ASTONISH_E2E_BACKEND") != "postgres" || getenv("ASTONISH_E2E_SANDBOX_BACKEND") != "k8s" {
		return out, fmt.Errorf("explicit postgres and k8s backends required")
	}
	if getenv("ASTONISH_E2E_KEEP_ALIVE") != "" {
		return out, fmt.Errorf("retained-home acceptance cannot attach to an inspector")
	}
	dedicated := regexp.MustCompile(`^(astonishe2e|astonish-e2e)-[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
	for _, key := range []string{"ASTONISH_E2E_SANDBOX_NAMESPACE", "ASTONISH_E2E_CONTROL_PLANE_NAMESPACE"} {
		ns := getenv(key)
		if !dedicated.MatchString(ns) || len(ns) > 63 {
			return out, fmt.Errorf("%s must name a dedicated test namespace", key)
		}
	}
	if getenv("ASTONISH_E2E_SANDBOX_NAMESPACE") == getenv("ASTONISH_E2E_CONTROL_PLANE_NAMESPACE") {
		return out, fmt.Errorf("namespaces must be distinct")
	}
	kubeconfig := getenv("KUBECONFIG")
	if !filepath.IsAbs(kubeconfig) || strings.ContainsRune(kubeconfig, filepath.ListSeparator) || filepath.Clean(kubeconfig) != kubeconfig {
		return out, fmt.Errorf("KUBECONFIG must be one explicit absolute file")
	}
	contextName := getenv("ASTONISH_E2E_KUBE_CONTEXT")
	if strings.TrimSpace(contextName) == "" {
		return out, fmt.Errorf("ASTONISH_E2E_KUBE_CONTEXT required")
	}
	out.Owner = getenv("ASTONISH_E2E_RESOURCE_OWNER")
	if !dedicated.MatchString(out.Owner) || len(out.Owner) > 63 {
		return out, fmt.Errorf("ASTONISH_E2E_RESOURCE_OWNER must name the dedicated fixture ownership label")
	}
	image := getenv("ASTONISH_SANDBOX_IMAGE")
	if strings.TrimSpace(image) == "" {
		return out, fmt.Errorf("ASTONISH_SANDBOX_IMAGE must identify the current test image")
	}
	homes := getenv("ASTONISH_E2E_HOMES_PVC")
	if !regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`).MatchString(homes) || len(homes) > 63 || homes == "astonish-layers" || homes == "astonish-uppers" {
		return out, fmt.Errorf("ASTONISH_E2E_HOMES_PVC must name an independent RWX claim")
	}
	out.DSN = getenv("ASTONISH_TEST_DSN")
	u, err := url.Parse(out.DSN)
	if err != nil || u == nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.User == nil || u.User.Username() == "" || u.Hostname() == "" || u.Port() == "" || u.Path != "/postgres" || u.Fragment != "" {
		return out, fmt.Errorf("dedicated DSN requires explicit PostgreSQL user, host, port and /postgres")
	}
	password, hasPassword := u.User.Password()
	if !hasPassword || password == "" {
		return out, fmt.Errorf("dedicated DSN requires an explicit password; ambient PG credentials forbidden")
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 1 || port > 65535 || strings.ContainsAny(u.Hostname(), ",/\\") {
		return out, fmt.Errorf("invalid dedicated PostgreSQL endpoint")
	}
	if getenv("ASTONISH_E2E_POSTGRES_INSTANCE") != net.JoinHostPort(u.Hostname(), u.Port()) {
		return out, fmt.Errorf("ASTONISH_E2E_POSTGRES_INSTANCE must match approved dedicated DSN host:port")
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(q) != 1 || len(q["sslmode"]) != 1 {
		return out, fmt.Errorf("DSN allows only one explicit sslmode; routing overrides forbidden")
	}
	switch q.Get("sslmode") {
	case "disable", "require", "verify-ca", "verify-full":
	default:
		return out, fmt.Errorf("explicit non-fallback sslmode required")
	}
	out.Kubernetes = config.SandboxKubernetesConfig{KubeconfigPath: kubeconfig, Context: contextName, Namespace: getenv("ASTONISH_E2E_SANDBOX_NAMESPACE"), ControlPlaneNamespace: getenv("ASTONISH_E2E_CONTROL_PLANE_NAMESPACE"), OverlayMode: "fuse", PrivilegedPods: true, SandboxImage: image, LayersPVCName: "astonish-layers", UppersPVCName: "astonish-uppers", HomesPVCName: homes}
	return out, nil
}

// ValidateRetainedHomeKubernetesInputs is pure and intentionally separate from
// provider-required generic security preflight. No provider fallback is allowed.
func ValidateRetainedHomeKubernetesInputs(getenv func(string) string) error {
	_, err := retainedHomeKubernetesInputs(getenv)
	return err
}

func validateRetainedHomeClaims(in RetainedHomeKubernetesInputs, namespaces []*corev1.Namespace, claims []*corev1.PersistentVolumeClaim) error {
	if len(namespaces) != 2 || len(claims) != 3 {
		return fmt.Errorf("missing dedicated namespaces or claims")
	}
	for i, name := range []string{in.Kubernetes.Namespace, in.Kubernetes.ControlPlaneNamespace} {
		ns := namespaces[i]
		if ns == nil || ns.Name != name || ns.Labels[retainedHomeOwnerLabel] != in.Owner || ns.DeletionTimestamp != nil {
			return fmt.Errorf("namespace %s is not positively fixture-owned", name)
		}
	}
	volumes := map[string]bool{}
	for i, name := range []string{in.Kubernetes.LayersPVCName, in.Kubernetes.UppersPVCName, in.Kubernetes.HomesPVCName} {
		pvc := claims[i]
		if pvc == nil || pvc.Name != name || pvc.Namespace != in.Kubernetes.Namespace || pvc.Labels[retainedHomeOwnerLabel] != in.Owner || pvc.DeletionTimestamp != nil || pvc.Status.Phase != corev1.ClaimBound || pvc.Spec.VolumeName == "" || volumes[pvc.Spec.VolumeName] {
			return fmt.Errorf("claim %s must be fixture-owned, Bound and independently backed", name)
		}
		rwx := false
		for _, mode := range pvc.Spec.AccessModes {
			rwx = rwx || mode == corev1.ReadWriteMany
		}
		if !rwx {
			return fmt.Errorf("claim %s requires RWX", name)
		}
		volumes[pvc.Spec.VolumeName] = true
	}
	return nil
}

// Exact database receipts, never suffix scans, authorize test cleanup.
func validateRetainedHomeDatabaseReceipt(name, expected, owner, expectedOwner string, ownedByCurrentUser bool) error {
	if name != expected || expected == "" || expectedOwner == "" || owner != expectedOwner || !ownedByCurrentUser {
		return fmt.Errorf("database is not positively test-owned; retain for operator review")
	}
	return nil
}
