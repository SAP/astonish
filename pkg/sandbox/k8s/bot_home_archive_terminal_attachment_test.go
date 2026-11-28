package k8s

import (
	"bytes"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// A terminal pod is not proof of detached storage: finalizers or a racing
// restart can preserve its mount. Export must not erase it or clear admission.
func TestK8sArchiveTerminalAttachmentStillExcluded(t *testing.T) {
	for _, phase := range []corev1.PodPhase{corev1.PodPending, corev1.PodRunning, corev1.PodSucceeded, corev1.PodFailed, corev1.PodUnknown} {
		t.Run(string(phase), func(t *testing.T) {
			b, h := archiveFixture(t)
			client := workspaceFake(t, b)
			pod := b.botWorkspacePod(h, "retained-attachment")
			created, err := client.CoreV1().Pods(b.cfg.Namespace).Create(t.Context(), pod, metav1.CreateOptions{})
			if err != nil {
				t.Fatal(err)
			}
			created.Status.Phase = phase
			if _, err = client.CoreV1().Pods(b.cfg.Namespace).UpdateStatus(t.Context(), created, metav1.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
			client.ClearActions()
			var out bytes.Buffer
			manifest, err := b.ExportBotHomeArchive(t.Context(), h, &out, archiveAllow)
			if err == nil || manifest != nil || out.Len() != 0 {
				t.Fatalf("attached archive accepted: manifest=%v bytes=%d err=%v", manifest, out.Len(), err)
			}
			for _, action := range client.Actions() {
				if action.GetVerb() != "get" && action.GetVerb() != "list" && !(action.GetVerb() == "update" && action.GetResource().Resource == "configmaps") {
					t.Fatalf("export mutated attached compute: %s %s", action.GetVerb(), action.GetResource().Resource)
				}
			}
			// No reservation was fabricated for this unknown attachment. The archive
			// may temporarily acquire its CAS fence, but must restore it on refusal.
			fence, err := client.CoreV1().ConfigMaps(b.cfg.Namespace).Get(t.Context(), botHomeFenceName(h), metav1.GetOptions{})
			if err != nil || botHomeFenceState(fence.Data["state"]) != botHomeActive || fence.Data[archiveOperationKey] != "" || fence.Data["reservations"] != "{}" {
				t.Fatalf("archive admission not restored after refusal: %+v %v", fence, err)
			}
			current, err := client.CoreV1().Pods(b.cfg.Namespace).Get(t.Context(), created.Name, metav1.GetOptions{})
			if err != nil || current.UID != created.UID {
				t.Fatalf("existing attachment changed: %v", err)
			}
		})
	}
}
