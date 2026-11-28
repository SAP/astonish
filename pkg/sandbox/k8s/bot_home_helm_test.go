package k8s

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBotHomeHelmOptIn(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm required for manifest rendering")
	}
	root := chartDir(t)
	for _, tc := range []struct {
		name                         string
		set                          []string
		wantPVC, wantConfig, wantErr bool
	}{
		{name: "disabled"},
		{name: "retained", set: []string{"sandbox.storage.homes.enabled=true"}, wantPVC: true, wantConfig: true},
		{name: "existing", set: []string{"sandbox.storage.homes.enabled=true", "sandbox.storage.homes.existingClaim=existing-homes"}, wantConfig: true},
		{name: "aliased", set: []string{"sandbox.storage.homes.enabled=true", "sandbox.storage.homes.pvcName=astonish-uppers"}, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := []string{"template", "bot-home-test", root, "-n", "astonishdev", "-f", filepath.Join(root, "values-dev-proxmox.yaml")}
			for _, set := range tc.set {
				args = append(args, "--set", set)
			}
			out, err := exec.Command("helm", args...).CombinedOutput()
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected rejected manifest")
				}
				return
			}
			if err != nil {
				t.Fatalf("helm: %v: %s", err, out)
			}
			manifest := string(out)
			if strings.Contains(manifest, "app.kubernetes.io/component: bot-homes") != tc.wantPVC {
				t.Fatal("home claim opt-in mismatch")
			}
			if strings.Contains(manifest, "homes_pvc_name:") != tc.wantConfig {
				t.Fatal("runtime config opt-in mismatch")
			}
			if tc.wantPVC && !strings.Contains(manifest, "helm.sh/resource-policy: keep") {
				t.Fatal("claim not retained on uninstall")
			}
			if tc.name == "existing" && !strings.Contains(manifest, `homes_pvc_name: "existing-homes"`) {
				t.Fatal("existing claim not wired")
			}
			if !strings.Contains(manifest, "resources: [\"configmaps\"]\n    verbs: [\"get\", \"create\", \"update\", \"delete\"]") {
				t.Fatal("home attachment CAS RBAC absent")
			}
		})
	}
}
