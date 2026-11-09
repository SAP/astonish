package k8s

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestVerifySessionDestroyedRequiresPhysicalAbsence(t *testing.T) {
	client := fake.NewClientset()
	b := &K8sBackend{client: client}
	sid := "exact-run"
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: podNameForSession(sid), Namespace: b.cfg.Namespace}}
	if _, err := client.CoreV1().Pods(b.cfg.Namespace).Create(context.Background(), pod, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := b.VerifySessionDestroyed(ctx, sid); err == nil {
		t.Fatal("live pod accepted with no registry")
	}
	if err := client.CoreV1().Pods(b.cfg.Namespace).Delete(context.Background(), pod.Name, metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := b.VerifySessionDestroyed(context.Background(), sid); err != nil {
		t.Fatal(err)
	}
	if err := (&K8sBackend{}).VerifySessionDestroyed(context.Background(), sid); err == nil {
		t.Fatal("missing backend attested")
	}
}
