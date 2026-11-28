package openshell

import (
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestBotHomeHelmConfiguration(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm required")
	}
	chart, err := filepath.Abs("../../../deploy/helm/astonish")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name           string
		set            []string
		claim, wantErr string
		pvc            bool
	}{
		{name: "default-off"},
		{name: "retained", set: []string{"sandbox.openshell.botHomes.enabled=true"}, claim: "astonish-openshell-bot-homes", pvc: true},
		{name: "existing", set: []string{"sandbox.openshell.botHomes.enabled=true", "sandbox.openshell.botHomes.existingClaim=existing-homes"}, claim: "existing-homes"},
		{name: "single-node-rwo", set: []string{"sandbox.openshell.botHomes.enabled=true", "sandbox.openshell.botHomes.accessMode=ReadWriteOnce", "sandbox.openshell.botHomes.storageClassName=local-csi", "sandbox.openshell.botHomes.namespace=astonish-sandbox"}, claim: "astonish-openshell-bot-homes", pvc: true},
		{name: "wrong-backend", set: []string{"sandbox.openshell.botHomes.enabled=true", "sandbox.backend=k8s"}, wantErr: "requires sandbox.enabled"},
		{name: "sandbox-disabled", set: []string{"sandbox.openshell.botHomes.enabled=true", "sandbox.enabled=false"}, wantErr: "requires sandbox.enabled"},
		{name: "cross-namespace", set: []string{"sandbox.openshell.botHomes.enabled=true", "sandbox.openshell.botHomes.namespace=elsewhere"}, wantErr: "cannot mount across namespaces"},
		{name: "invalid-namespace", set: []string{"sandbox.openshell.botHomes.enabled=true", "namespaces.sandbox=Invalid"}, wantErr: "DNS-1123"},
		{name: "invalid-name", set: []string{"sandbox.openshell.botHomes.enabled=true", "sandbox.openshell.botHomes.pvcName=bad..name"}, wantErr: "valid DNS-1123 PVC"},
		{name: "empty-name", set: []string{"sandbox.openshell.botHomes.enabled=true", "sandbox.openshell.botHomes.pvcName="}, wantErr: "valid DNS-1123 PVC"},
		{name: "aliased-uppers", set: []string{"sandbox.openshell.botHomes.enabled=true", "sandbox.openshell.botHomes.existingClaim=astonish-uppers"}, wantErr: "must be independent"},
		{name: "aliased-workspace", set: []string{"sandbox.openshell.botHomes.enabled=true", "sandbox.openshell.botHomes.pvcName=workspace"}, wantErr: "must be independent"},
		{name: "aliased-cert", set: []string{"sandbox.openshell.botHomes.enabled=true", "sandbox.openshell.certBundles[0].name=corp", "sandbox.openshell.certBundles[0].claimName=astonish-openshell-bot-homes", "sandbox.openshell.certBundles[0].mountPath=/etc/corp.pem"}, wantErr: "collide with cert"},
		{name: "read-only", set: []string{"sandbox.openshell.botHomes.enabled=true", "sandbox.openshell.botHomes.accessMode=ReadOnlyMany"}, wantErr: "accessMode"},
		{name: "single-pod", set: []string{"sandbox.openshell.botHomes.enabled=true", "sandbox.openshell.botHomes.accessMode=ReadWriteOncePod"}, wantErr: "accessMode"},
		{name: "bad-storage-class", set: []string{"sandbox.openshell.botHomes.enabled=true", "sandbox.openshell.botHomes.storageClassName=Bad/Class"}, wantErr: "valid StorageClass"},
		{name: "zero-size", set: []string{"sandbox.openshell.botHomes.enabled=true", "sandbox.openshell.botHomes.size=0Gi"}, wantErr: "positive whole storage"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := []string{"template", "bot-home-test", chart, "-n", "astonish", "--set", "sandbox.enabled=true,sandbox.backend=openshell,sandbox.storage.storageClassName=nfs-client"}
			for _, s := range tc.set {
				args = append(args, "--set", s)
			}
			out, err := exec.Command("helm", args...).CombinedOutput()
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(string(out), tc.wantErr) {
					t.Fatalf("expected %q error, got %v: %s", tc.wantErr, err, out)
				}
				return
			}
			if err != nil {
				t.Fatalf("helm: %v: %s", err, out)
			}
			if tc.claim == "" && strings.Contains(string(out), "app.kubernetes.io/component: openshell-bot-homes") {
				t.Fatal("default-off rendered a Bot home PVC")
			}
			dec := yaml.NewDecoder(strings.NewReader(string(out)))
			var runtimeConfig string
			var foundPVC bool
			for {
				var doc struct {
					Kind     string `yaml:"kind"`
					Metadata struct {
						Name, Namespace string
						Annotations     map[string]string
						OwnerReferences []any `yaml:"ownerReferences"`
					} `yaml:"metadata"`
					Data map[string]string `yaml:"data"`
					Spec struct {
						AccessModes      []string `yaml:"accessModes"`
						StorageClassName string   `yaml:"storageClassName"`
					} `yaml:"spec"`
				}
				if err := dec.Decode(&doc); err == io.EOF {
					break
				} else if err != nil {
					t.Fatal(err)
				}
				if doc.Kind == "ConfigMap" && doc.Data["config.yaml"] != "" {
					runtimeConfig = doc.Data["config.yaml"]
				}
				if doc.Kind == "PersistentVolumeClaim" && doc.Metadata.Name == tc.claim && tc.claim != "" {
					foundPVC = true
					if doc.Metadata.Namespace != "astonish-sandbox" || doc.Metadata.Annotations["helm.sh/resource-policy"] != "keep" || len(doc.Metadata.OwnerReferences) != 0 {
						t.Fatalf("home is not independent and retained: %+v", doc.Metadata)
					}
					wantClass, wantMode := "nfs-client", "ReadWriteMany"
					if tc.name == "single-node-rwo" {
						wantClass, wantMode = "local-csi", "ReadWriteOnce"
					}
					if doc.Spec.StorageClassName != wantClass || len(doc.Spec.AccessModes) != 1 || doc.Spec.AccessModes[0] != wantMode {
						t.Fatalf("storage config lost: %+v", doc.Spec)
					}
				}
				if doc.Kind == "StatefulSet" {
					t.Fatal("Bot homes must not opt into the gateway subchart")
				}
			}
			if foundPVC != tc.pvc {
				t.Fatalf("claim created=%t want %t", foundPVC, tc.pvc)
			}
			if runtimeConfig == "" {
				t.Fatal("missing application config")
			}
			if strings.Contains(runtimeConfig, "password:") || strings.Contains(runtimeConfig, "auth_token:") {
				t.Fatal("secret embedded in runtime ConfigMap")
			}
			b := loadBotHomeBackend(t, runtimeConfig)
			if b.cfg.BotHomeClaimName != tc.claim || b.cfg.BotHomeMountsEnabled != (tc.claim != "") {
				t.Fatalf("render -> loader -> factory lost bot homes: enabled=%t claim=%q", b.cfg.BotHomeMountsEnabled, b.cfg.BotHomeClaimName)
			}
		})
	}
}
