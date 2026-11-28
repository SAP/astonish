//go:build e2e

package e2eboot

import (
	"context"
	"crypto/sha256"
	"fmt"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	"strings"
	"testing"
)

func TestRetainedHomeKubernetesInputs(t *testing.T) {
	valid := retainedHomeTestEnv()
	if err := ValidateRetainedHomeKubernetesInputs(func(k string) string { return valid[k] }); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"ASTONISH_E2E_DEDICATED", "ASTONISH_E2E_SANDBOX_NAMESPACE", "ASTONISH_E2E_CONTROL_PLANE_NAMESPACE", "KUBECONFIG", "ASTONISH_E2E_KUBE_CONTEXT", "ASTONISH_SANDBOX_IMAGE", "ASTONISH_E2E_HOMES_PVC"} {
		t.Run("missing-"+key, func(t *testing.T) {
			if err := ValidateRetainedHomeKubernetesInputs(func(k string) string {
				if k == key {
					return ""
				}
				return valid[k]
			}); err == nil {
				t.Fatal("missing release infrastructure accepted")
			}
		})
	}
	for _, pvc := range []string{valid["ASTONISH_E2E_LAYERS_PVC"], valid["ASTONISH_E2E_UPPERS_PVC"]} {
		t.Run(pvc, func(t *testing.T) {
			if err := ValidateRetainedHomeKubernetesInputs(func(k string) string {
				if k == "ASTONISH_E2E_HOMES_PVC" {
					return pvc
				}
				return valid[k]
			}); err == nil {
				t.Fatal("shared claim accepted")
			}
		})
	}
}

func TestRetainedHomeOwnedPodRejectsAmbientCleanup(t *testing.T) {
	id := uuid.NewString()
	home := &sandbox.BotHomeBinding{Scope: sandbox.BotHomeScope{OrgID: "test-org", TeamID: "test-team", UserID: uuid.NewString(), BotID: uuid.NewString()}, Backend: sandbox.BackendKindK8s, Ref: "test-home"}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: DerivePodName(id), Namespace: "dedicated", UID: types.UID("owned-uid")}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "session", VolumeMounts: []corev1.VolumeMount{{MountPath: "/mnt/astonish-bot-home", SubPath: home.Scope.Key() + "/data"}}}}}}
	if err := retainedHomeOwnedPod(pod, id, "dedicated", home); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*corev1.Pod){func(p *corev1.Pod) { p.Namespace = "ambient" }, func(p *corev1.Pod) { p.Name = "unrelated" }, func(p *corev1.Pod) { p.UID = "" }, func(p *corev1.Pod) { p.Spec.Containers[0].VolumeMounts[0].SubPath = "foreign/data" }, func(p *corev1.Pod) { p.Spec.Containers[0].VolumeMounts[0].ReadOnly = true }} {
		changed := pod.DeepCopy()
		mutate(changed)
		client := fake.NewSimpleClientset(changed)
		if err := deleteRetainedHomeOwnedPod(context.Background(), client, changed, id, "dedicated", home); err == nil {
			t.Fatal("ambient pod authorized")
		}
		for _, a := range client.Actions() {
			if a.GetVerb() == "delete" {
				t.Fatal("rejected resource reached cleanup")
			}
		}
	}
}

func TestRetainedHomeLifecycleModelRejectsMarkerOnly(t *testing.T) {
	m := &retainedHomeLifecycleModel{calls: 1}
	req := &model.LLMRequest{Contents: []*genai.Content{{Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{ID: "retained-read-1", Response: map[string]any{"content": "LOCAL_SEARCH_RESULT_42"}}}}}}}
	rejected := false
	for _, err := range m.GenerateContent(context.Background(), req, false) {
		rejected = rejected || err != nil
	}
	if !rejected {
		t.Fatal("marker-only fake result accepted")
	}
	if len(RetainedHomeFixtureBytes) != 165 {
		t.Fatal("exact fixture changed")
	}
}

func TestRetainedHomeLifecycleModelAcceptsActualCompleteRead(t *testing.T) {
	m := &retainedHomeLifecycleModel{calls: 1}
	req := &model.LLMRequest{Contents: []*genai.Content{{Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{ID: "retained-read-1", Response: map[string]any{"content": "1: " + RetainedHomeFixtureBytes}}}}}}}
	accepted := false
	for response, err := range m.GenerateContent(context.Background(), req, false) {
		if err != nil {
			t.Fatal(err)
		}
		accepted = response != nil && response.Content != nil
	}
	if !accepted {
		t.Fatal("actual complete read did not advance to typed question")
	}
}

func TestRetainedHomeLifecycleGuardMissingAuthority(t *testing.T) {
	g := retainedHomeLifecycleGuard{}
	if err := g.check(context.Background()); err != store.ErrBotForbidden {
		t.Fatalf("missing budget/authority accepted: %v", err)
	}
}

func TestRetainedHomeArchiveFixtureExactDigest(t *testing.T) {
	m := &sandbox.BotHomeArchiveManifest{Version: 1, Binding: strings.Repeat("a", 64), Entries: []sandbox.BotHomeArchiveEntry{
		{Root: sandbox.BotHomeArchiveHome, Path: "outputs", Directory: true, Mode: 0700},
		{Root: sandbox.BotHomeArchiveHome, Path: retainedHomeFixturePath, Size: int64(len(RetainedHomeFixtureBytes)), SHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(RetainedHomeFixtureBytes))), Mode: 0600},
	}}
	if err := retainedHomeArchiveFixture(m); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*sandbox.BotHomeArchiveManifest){
		func(m *sandbox.BotHomeArchiveManifest) { m.Entries[1].Size-- },
		func(m *sandbox.BotHomeArchiveManifest) { m.Entries[1].SHA256 = strings.Repeat("b", 64) },
		func(m *sandbox.BotHomeArchiveManifest) { m.Entries[1].Path = "outputs/other.txt" },
	} {
		changed := *m
		changed.Entries = append([]sandbox.BotHomeArchiveEntry(nil), m.Entries...)
		mutate(&changed)
		if err := retainedHomeArchiveFixture(&changed); err == nil {
			t.Fatal("incomplete/foreign fixture accepted")
		}
	}
}

func TestRetainedHomeOwnedPodDeleteUIDPrecondition(t *testing.T) {
	id := uuid.NewString()
	home := &sandbox.BotHomeBinding{Scope: sandbox.BotHomeScope{UserID: uuid.NewString(), BotID: uuid.NewString()}, Backend: sandbox.BackendKindK8s, Ref: "owned"}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: DerivePodName(id), Namespace: "dedicated", UID: types.UID("owned-uid")}, Spec: corev1.PodSpec{Containers: []corev1.Container{{VolumeMounts: []corev1.VolumeMount{{MountPath: "/mnt/astonish-bot-home", SubPath: home.Scope.Key() + "/data"}}}}}}
	client := fake.NewSimpleClientset(pod)
	if err := deleteRetainedHomeOwnedPod(context.Background(), client, pod, id, "dedicated", home); err != nil {
		t.Fatal(err)
	}
	if len(client.Actions()) != 1 {
		t.Fatalf("unexpected cleanup actions: %+v", client.Actions())
	}
	a := client.Actions()[0].(k8stesting.DeleteAction)
	o := a.GetDeleteOptions()
	if a.GetNamespace() != "dedicated" || a.GetName() != pod.Name || o.Preconditions == nil || o.Preconditions.UID == nil || *o.Preconditions.UID != pod.UID {
		t.Fatal("delete lacked exact namespace/name/UID fencing")
	}
}

func TestRetainedHomeLifecycleNoGenerationHasNoFallback(t *testing.T) {
	for _, err := range (retainedHomeNoGeneration{}).GenerateContent(context.Background(), &model.LLMRequest{}, false) {
		if err == nil || !strings.Contains(err.Error(), "must not generate") {
			t.Fatalf("unexpected generation: %v", err)
		}
	}
}
