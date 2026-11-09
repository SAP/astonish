package k8s

import (
	"context"
	"testing"

	"github.com/SAP/astonish/pkg/sandbox"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestBotHomeRejectUnsafeRecoveredPod(t *testing.T) {
	mutations := map[string]func(*corev1.Pod){
		"claim-root":          func(p *corev1.Pod) { p.Spec.Containers[0].VolumeMounts[3].SubPath = "" },
		"traversal":           func(p *corev1.Pod) { p.Spec.Containers[0].VolumeMounts[3].SubPath = "../other/data" },
		"expression":          func(p *corev1.Pod) { p.Spec.Containers[0].VolumeMounts[3].SubPathExpr = "$(OTHER)" },
		"target":              func(p *corev1.Pod) { p.Spec.Containers[0].VolumeMounts[3].MountPath = "/root" },
		"initializer-root":    func(p *corev1.Pod) { p.Spec.InitContainers[0].VolumeMounts[0].SubPath = "" },
		"initializer-missing": func(p *corev1.Pod) { p.Spec.InitContainers = nil },
		"token":               func(p *corev1.Pod) { p.Spec.AutomountServiceAccountToken = botHomeBool(true) },
		"home-env": func(p *corev1.Pod) {
			p.Spec.Containers[0].Env = append(p.Spec.Containers[0].Env, corev1.EnvVar{Name: "HOME", Value: "/tmp"})
		},
		"missing-mode": func(p *corev1.Pod) {
			env := p.Spec.Containers[0].Env
			for i, e := range env {
				if e.Name == "ASTONISH_BOT_HOME" {
					p.Spec.Containers[0].Env = append(env[:i], env[i+1:]...)
					break
				}
			}
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			b, h := homeBackend(t)
			ctx := context.Background()
			spec := sandbox.SessionSpec{SessionID: "unsafe", BotHome: h}
			if _, err := b.CreateSession(ctx, spec); err != nil {
				t.Fatal(err)
			}
			pods := b.client.CoreV1().Pods(b.cfg.Namespace)
			pod, err := pods.Get(ctx, podNameForSession(spec.SessionID), metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			mutate(pod)
			if _, err := pods.Update(ctx, pod, metav1.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
			if _, err := b.CreateSession(ctx, spec); err == nil {
				t.Fatal("adopted unsafe retained pod")
			}
			// A running pod must be checked on Start as well as idempotent Create.
			pod.Status.Phase = corev1.PodRunning
			if _, err := pods.UpdateStatus(ctx, pod, metav1.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
			if err := b.StartSession(ctx, spec.SessionID); err == nil {
				t.Fatal("started unsafe retained pod")
			}
		})
	}
}

func TestBotHomeCannotUpgradeOrdinarySession(t *testing.T) {
	b, h := homeBackend(t)
	ctx := context.Background()
	if _, err := b.CreateSession(ctx, sandbox.SessionSpec{SessionID: "ordinary"}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.CreateSession(ctx, sandbox.SessionSpec{SessionID: "ordinary", BotHome: h}); err == nil {
		t.Fatal("upgraded ordinary session")
	}
	if _, err := b.client.CoreV1().ConfigMaps(b.cfg.Namespace).Get(ctx, botHomeSessionName("ordinary"), metav1.GetOptions{}); err == nil {
		t.Fatal("ordinary session acquired home metadata")
	}
}

func TestBotHomeCorruptMetadataFailsClosed(t *testing.T) {
	b, h := homeBackend(t)
	ctx := context.Background()
	spec := sandbox.SessionSpec{SessionID: "corrupt", BotHome: h}
	if _, err := b.CreateSession(ctx, spec); err != nil {
		t.Fatal(err)
	}
	cms := b.client.CoreV1().ConfigMaps(b.cfg.Namespace)
	cm, err := cms.Get(ctx, botHomeSessionName(spec.SessionID), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if cm.Immutable == nil || !*cm.Immutable {
		t.Fatal("mutable binding")
	}
	// Fake API allows simulating external backup corruption of immutable metadata.
	cm.Data["binding"] = "{invalid"
	if _, err := cms.Update(ctx, cm, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := b.client.CoreV1().Pods(b.cfg.Namespace).Delete(ctx, podNameForSession(spec.SessionID), metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := b.StartSession(ctx, spec.SessionID); err == nil {
		t.Fatal("recovered corrupt binding")
	}
}
