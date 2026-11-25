package k8s

import (
	"context"
	"encoding/json"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"io"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	k8stesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/remotecommand"
	"net/url"
	"strings"
	"testing"
)

func warmHomeFilesFixture(t *testing.T) (*K8sBackend, *sandbox.BotHomeBinding, *fake.Clientset) {
	t.Helper()
	b, h := homeBackend(t)
	id := "canonical"
	if _, err := b.CreateSession(t.Context(), sandbox.SessionSpec{SessionID: id, BotHome: h, TemplateID: sandbox.BaseTemplateID}); err != nil {
		t.Fatal(err)
	}
	client := b.client.(*fake.Clientset)
	pod, err := client.CoreV1().Pods(b.cfg.Namespace).Get(t.Context(), podNameForSession(id), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	pod.UID = "physical-session"
	pod.Status.Phase = corev1.PodRunning
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: containerName, Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}
	if _, err = client.CoreV1().Pods(b.cfg.Namespace).Update(t.Context(), pod, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err = b.BindBotHomeSession(h, id); err != nil {
		t.Fatal(err)
	}
	b.restConfig = &rest.Config{Host: "https://fake.invalid"}
	client.ClearActions()
	return b, h, client
}

func TestBotHomeFilesWarmNoHelperOrSessionDelete(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(map[bool]string{false: "read", true: "cancel"}[cancelled], func(t *testing.T) {
			b, h, client := warmHomeFilesFixture(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			b.execExecutorFactory = func(_ *rest.Config, _ string, u *url.URL) (remoteExecutor, error) {
				if !strings.Contains(u.Path, podNameForSession("canonical")) || !strings.Contains(strings.Join(u.Query()["command"], " "), mountBotHomeScope) {
					t.Fatal("not canonical scoped dispatch")
				}
				return homeReadExecutor(func(ctx context.Context, o remotecommand.StreamOptions) error {
					var q map[string]any
					if err := json.NewDecoder(o.Stdin).Decode(&q); err != nil {
						return err
					}
					if err := json.NewEncoder(o.Stdout).Encode(store.BotFileEntry{Path: "x", SizeBytes: 2}); err != nil {
						return err
					}
					if cancelled {
						<-ctx.Done()
						return ctx.Err()
					}
					_, err := io.WriteString(o.Stdout, "ok")
					return err
				}), nil
			}
			stream, err := b.OpenBotHomeFile(ctx, h, "x", store.BotFileRange{})
			if err != nil {
				t.Fatal(err)
			}
			if cancelled {
				cancel()
			} else {
				got, err := io.ReadAll(stream)
				if err != nil || string(got) != "ok" {
					t.Fatalf("%q %v", got, err)
				}
			}
			_ = stream.Close()
			for _, a := range client.Actions() {
				if a.GetVerb() != "get" {
					t.Fatalf("warm storage mutated Kubernetes: %s %s", a.GetVerb(), a.GetResource().Resource)
				}
			}
			if len(homeReservations(t, b, h)) != 1 {
				t.Fatal("session admission released")
			}
		})
	}
}

func TestBotHomeFilesWarmRejectChangedTarget(t *testing.T) {
	for _, mode := range []string{"uid", "scope", "duplicate", "fence", "missing", "uid-env", "scope-shadow"} {
		t.Run(mode, func(t *testing.T) {
			b, h, client := warmHomeFilesFixture(t)
			gets := 0
			client.PrependReactor("get", "pods", func(a k8stesting.Action) (bool, k8sruntime.Object, error) {
				obj, err := client.Tracker().Get(corev1.SchemeGroupVersion.WithResource("pods"), b.cfg.Namespace, a.(k8stesting.GetAction).GetName())
				if err != nil {
					return true, nil, err
				}
				p := obj.(*corev1.Pod).DeepCopy()
				gets++
				switch mode {
				case "uid":
					if gets > 1 {
						p.UID = "replacement"
					}
				case "scope":
					p.Spec.Containers[0].VolumeMounts[len(p.Spec.Containers[0].VolumeMounts)-1].SubPath = "foreign"
				case "duplicate":
					p.Spec.Containers[0].VolumeMounts = append(p.Spec.Containers[0].VolumeMounts, p.Spec.Containers[0].VolumeMounts[len(p.Spec.Containers[0].VolumeMounts)-1])
				case "uid-env":
					for i := range p.Spec.Containers[0].Env {
						if p.Spec.Containers[0].Env[i].Name == "ASTONISH_POD_UID" {
							p.Spec.Containers[0].Env[i].ValueFrom = nil
							p.Spec.Containers[0].Env[i].Value = "spoof"
						}
					}
				case "scope-shadow":
					p.Spec.Containers[0].VolumeMounts = append(p.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{Name: "foreign", MountPath: mountBotHomeScope})
				case "missing":
					p.Status.Phase = corev1.PodPending
				}
				return true, p, nil
			})
			if mode == "fence" {
				cm, err := client.CoreV1().ConfigMaps(b.cfg.Namespace).Get(t.Context(), botHomeFenceName(h), metav1.GetOptions{})
				if err != nil {
					t.Fatal(err)
				}
				cm.Data["state"] = string(botHomeDeleting)
				if _, err = client.CoreV1().ConfigMaps(b.cfg.Namespace).Update(t.Context(), cm, metav1.UpdateOptions{}); err != nil {
					t.Fatal(err)
				}
			}
			b.execExecutorFactory = func(*rest.Config, string, *url.URL) (remoteExecutor, error) {
				t.Fatal("unsafe target dispatched")
				return nil, nil
			}
			if _, err := b.StatBotHomeFile(t.Context(), h, "x"); err == nil {
				t.Fatal("unsafe target accepted")
			}
			for _, a := range client.Actions() {
				if a.GetResource().Resource == "pods" && (a.GetVerb() == "create" || a.GetVerb() == "delete") {
					t.Fatal("warm failure created or deleted pod")
				}
			}
		})
	}
}

func TestBotHomeFilesWarmBindingCapability(t *testing.T) {
	b, h := homeBackend(t)
	binder, ok := any(b).(interface {
		BindBotHomeSession(*sandbox.BotHomeBinding, string) error
	})
	if !ok {
		t.Fatal("missing trusted canonical Home session binding")
	}
	if err := binder.BindBotHomeSession(h, "canonical"); err != nil {
		t.Fatal(err)
	}
	if err := binder.BindBotHomeSession(h, "canonical"); err != nil {
		t.Fatal(err)
	}
	if err := binder.BindBotHomeSession(h, "other"); err == nil {
		t.Fatal("binding changed")
	}
}
