package k8s

import (
	"context"
	"io"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	k8stesting "k8s.io/client-go/testing"
)

func TestK8sArchiveMissingEvidenceNeverProvisions(t *testing.T) {
	for _, mode := range []string{"claim", "admission", "attachment"} {
		t.Run(mode, func(t *testing.T) {
			b, h := archiveFixture(t)
			client := workspaceFake(t, b)
			switch mode {
			case "claim":
				if err := client.CoreV1().PersistentVolumeClaims(b.cfg.Namespace).Delete(t.Context(), b.cfg.HomesPVCName, metav1.DeleteOptions{}); err != nil {
					t.Fatal(err)
				}
			case "admission":
				if err := client.CoreV1().ConfigMaps(b.cfg.Namespace).Delete(t.Context(), botHomeFenceName(h), metav1.DeleteOptions{}); err != nil {
					t.Fatal(err)
				}
			case "attachment":
				pod := b.botWorkspacePod(h, "existing-writer")
				if _, err := client.CoreV1().Pods(b.cfg.Namespace).Create(t.Context(), pod, metav1.CreateOptions{}); err != nil {
					t.Fatal(err)
				}
			}
			client.ClearActions()
			if m, err := b.ExportBotHomeArchive(t.Context(), h, io.Discard, archiveAllow); err == nil || m != nil {
				t.Fatal("missing evidence accepted")
			}
			for _, action := range client.Actions() {
				if action.GetVerb() == "create" {
					t.Fatalf("read provisioned %s", action.GetResource().Resource)
				}
			}
		})
	}
}

func TestK8sArchiveCleanupWaitsForAbsence(t *testing.T) {
	b, h := archiveFixture(t)
	client := workspaceFake(t, b)
	name := "archive-cleanup"
	if err := b.archiveFence(t.Context(), h, name, true); err != nil {
		t.Fatal(err)
	}
	pod, err := client.CoreV1().Pods(b.cfg.Namespace).Create(t.Context(), b.botWorkspacePod(h, name), metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	deleting, gets := false, 0
	client.PrependReactor("delete", "pods", func(k8stesting.Action) (bool, k8sruntime.Object, error) { deleting = true; return true, nil, nil })
	client.PrependReactor("get", "pods", func(k8stesting.Action) (bool, k8sruntime.Object, error) {
		if !deleting {
			return false, nil, nil
		}
		gets++
		cm, e := client.Tracker().Get(corev1.SchemeGroupVersion.WithResource("configmaps"), b.cfg.Namespace, botHomeFenceName(h))
		if e != nil || cm.(*corev1.ConfigMap).Data["state"] != string(botHomeArchiving) {
			t.Fatal("released before absence", e)
		}
		if gets == 2 {
			return true, nil, apierrors.NewNotFound(corev1.Resource("pods"), name)
		}
		return false, nil, nil
	})
	_, cancel := context.WithCancel(t.Context())
	p := &homeArchiveProcess{backend: b, binding: h, name: name, uid: pod.UID, submitted: true, cancel: cancel}
	if err := p.finish(false); err != nil {
		t.Fatal(err)
	}
	if gets != 2 {
		t.Fatal("did not wait for absence")
	}
}

func TestK8sArchiveReservationWinsCAS(t *testing.T) {
	b, h := archiveFixture(t)
	client := workspaceFake(t, b)
	// Emulate an API-server CAS conflict after an attachment reserved the Home.
	first := true
	client.PrependReactor("update", "configmaps", func(a k8stesting.Action) (bool, k8sruntime.Object, error) {
		if !first {
			return false, nil, nil
		}
		first = false
		obj, err := client.Tracker().Get(corev1.SchemeGroupVersion.WithResource("configmaps"), b.cfg.Namespace, botHomeFenceName(h))
		if err != nil {
			t.Fatal(err)
		}
		cm := obj.(*corev1.ConfigMap).DeepCopy()
		cm.Data["reservations"] = `{"racer":{"token":"owned"}}`
		if err := client.Tracker().Update(corev1.SchemeGroupVersion.WithResource("configmaps"), cm, b.cfg.Namespace); err != nil {
			t.Fatal(err)
		}
		return true, nil, apierrors.NewConflict(corev1.Resource("configmaps"), cm.Name, io.ErrUnexpectedEOF)
	})
	if err := b.archiveFence(context.Background(), h, "archive", true); err == nil {
		t.Fatal("lost CAS accepted")
	}
	if err := b.archiveFence(context.Background(), h, "archive", true); err == nil {
		t.Fatal("racing attachment ignored")
	}
}
