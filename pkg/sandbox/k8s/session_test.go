// Package k8s — session lifecycle tests.
//
// These tests use k8s.io/client-go/kubernetes/fake to stand up an in-
// memory API server and exercise the K8sBackend lifecycle methods end-
// to-end. They cover:
//
//   - CreateSession: pod manifest shape (labels, annotations, volumes,
//                    runtime class, env vars), registry persistence
//                    (backend tag, pod_name, node_name), idempotency.
//   - SessionState:  pending/running/ready/absent mapping, evicted row.
//   - StopSession:   pod deletion + state=evicted bookkeeping.
//   - DestroySession: pod deletion + registry drain; idempotent on
//                     absent sessions.
//   - ListSessions:  label-selector dispatch + filter-side state match.
//
// The fake clientset does not run admission/defaulting logic, so these
// tests deliberately assert on the shape we submit (the pod the backend
// sent to Create) rather than on any server-applied fields.

package k8s

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	clientgotesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/remotecommand"
	k8sexec "k8s.io/client-go/util/exec"

	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
)

// newBackendWithFakeClient returns a K8sBackend wired to a fresh in-
// memory API server and the standard test registry.
func newBackendWithFakeClient(t *testing.T, seed ...runtime.Object) (*K8sBackend, kubernetes.Interface) {
	t.Helper()
	cs := fake.NewSimpleClientset(seed...)
	b, err := New(Config{
		Client:   cs,
		Sessions: newRegistry(t),
	})
	if err != nil {
		t.Fatalf("k8s.New: %v", err)
	}
	return b, cs
}

// ---------------------------------------------------------------------------
// buildPodManifest
// ---------------------------------------------------------------------------

func TestBuildPodManifest_LabelsAndAnnotations(t *testing.T) {
	b, _ := newBackendWithFakeClient(t)
	pod, err := b.buildPodManifest(sandbox.SessionSpec{
		SessionID:  "sess-abc-001",
		TemplateID: "python-dev",
		OrgSlug:    "acme",
		TeamSlug:   "data",
		UserID:     "user-42",
		LayerChain: []string{"layer-a", "layer-b", "layer-c"},
		Limits:     sandbox.ResourceLimits{CPUs: 2, MemoryMiB: 1024, DiskMiB: 4096},
	})
	if err != nil {
		t.Fatalf("buildPodManifest: %v", err)
	}

	want := map[string]string{
		labelType:      typeSession,
		labelSessionID: "sess-abc-001",
		labelTemplate:  "python-dev",
		labelOrg:       "acme",
		labelTeam:      "data",
	}
	for k, v := range want {
		if got := pod.Labels[k]; got != v {
			t.Errorf("label[%s] = %q, want %q", k, got, v)
		}
	}

	if pod.Annotations[annotationCreatedBy] != "user-42" {
		t.Errorf("annotation %s = %q, want %q", annotationCreatedBy, pod.Annotations[annotationCreatedBy], "user-42")
	}
	if pod.Annotations[annotationLayerChain] != "layer-a,layer-b,layer-c" {
		t.Errorf("annotation %s = %q", annotationLayerChain, pod.Annotations[annotationLayerChain])
	}
	if pod.Annotations[annotationCreatedAt] == "" {
		t.Error("annotation created-at should not be empty")
	}
}

func TestBuildPodManifest_RuntimeClassAndVolumes(t *testing.T) {
	b, _ := newBackendWithFakeClient(t)
	pod, err := b.buildPodManifest(sandbox.SessionSpec{
		SessionID:  "s1",
		TemplateID: "t1",
	})
	if err != nil {
		t.Fatalf("buildPodManifest: %v", err)
	}

	// Phase F: default backend has RuntimeClassName=="" so the pod's
	// RuntimeClassName must be nil (cluster default). Operators who
	// need a specialised runtime set Config.RuntimeClassName
	// explicitly; see fleet_test.go / a dedicated test for that path.
	if pod.Spec.RuntimeClassName != nil {
		t.Errorf("RuntimeClassName = %v, want nil (empty config → cluster default)", pod.Spec.RuntimeClassName)
	}

	volNames := map[string]bool{}
	for _, v := range pod.Spec.Volumes {
		volNames[v.Name] = true
	}
	for _, want := range []string{volumeLayers, volumeUppers, volumeOverlay} {
		if !volNames[want] {
			t.Errorf("volume %q missing from pod spec", want)
		}
	}

	// The layers volume MUST be RO PVC-backed (§5.3 step 3).
	for _, v := range pod.Spec.Volumes {
		if v.Name == volumeLayers {
			if v.PersistentVolumeClaim == nil {
				t.Errorf("volume %q should be PVC-backed", volumeLayers)
				break
			}
			if !v.PersistentVolumeClaim.ReadOnly {
				t.Errorf("volume %q should be read-only", volumeLayers)
			}
			if v.PersistentVolumeClaim.ClaimName != "astonish-layers" {
				t.Errorf("volume %q ClaimName = %q, want %q", volumeLayers, v.PersistentVolumeClaim.ClaimName, "astonish-layers")
			}
		}
	}
}

func TestBuildPodManifest_EnvAndMounts(t *testing.T) {
	b, _ := newBackendWithFakeClient(t)
	pod, err := b.buildPodManifest(sandbox.SessionSpec{
		SessionID:  "s1",
		TemplateID: "t1",
		LayerChain: []string{"a", "b"},
	})
	if err != nil {
		t.Fatalf("buildPodManifest: %v", err)
	}
	if len(pod.Spec.Containers) != 1 {
		t.Fatalf("expected 1 container, got %d", len(pod.Spec.Containers))
	}
	c := pod.Spec.Containers[0]

	// ASTONISH_LAYER_CHAIN env var must match the annotation.
	env := map[string]string{}
	for _, e := range c.Env {
		env[e.Name] = e.Value
	}
	if env["ASTONISH_SESSION_ID"] != "s1" {
		t.Errorf("env ASTONISH_SESSION_ID = %q, want s1", env["ASTONISH_SESSION_ID"])
	}
	if env["ASTONISH_LAYER_CHAIN"] != "a,b" {
		t.Errorf("env ASTONISH_LAYER_CHAIN = %q", env["ASTONISH_LAYER_CHAIN"])
	}
	if env["ASTONISH_UPPER_DIR"] != mountUpper {
		t.Errorf("env ASTONISH_UPPER_DIR = %q", env["ASTONISH_UPPER_DIR"])
	}

	// All four volume mounts must be present on the container.
	mounts := map[string]corev1.VolumeMount{}
	for _, m := range c.VolumeMounts {
		mounts[m.Name] = m
	}
	for _, want := range []string{volumeLayers, volumeUppers, volumeOverlay} {
		if _, ok := mounts[want]; !ok {
			t.Errorf("VolumeMount %q missing", want)
		}
	}
	if !mounts[volumeLayers].ReadOnly {
		t.Errorf("VolumeMount %q should be read-only", volumeLayers)
	}
	if mounts[volumeUppers].SubPath != "s1" {
		t.Errorf("VolumeMount %q SubPath = %q, want session-specific subpath", volumeUppers, mounts[volumeUppers].SubPath)
	}
}

func TestBuildPodManifest_Rejects(t *testing.T) {
	b, _ := newBackendWithFakeClient(t)

	if _, err := b.buildPodManifest(sandbox.SessionSpec{}); err == nil {
		t.Error("empty SessionID should error")
	}
	// Empty TemplateID now defaults to BaseTemplateID ("@base") — verify
	// it succeeds and applies the default rather than erroring.
	pod, err := b.buildPodManifest(sandbox.SessionSpec{SessionID: "s"})
	if err != nil {
		t.Fatalf("empty TemplateID should default to @base, got error: %v", err)
	}
	// Verify the default was applied in the pod's env/labels.
	found := false
	for _, env := range pod.Spec.Containers[0].Env {
		if env.Name == "ASTONISH_LAYER_CHAIN" && env.Value == sandbox.BaseTemplateID {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected ASTONISH_LAYER_CHAIN=%q in pod env", sandbox.BaseTemplateID)
	}

	// Chain depth exceeds MaxChainDepth (default 20).
	chain := make([]string, 25)
	for i := range chain {
		chain[i] = "l"
	}
	if _, err := b.buildPodManifest(sandbox.SessionSpec{SessionID: "s", TemplateID: "t", LayerChain: chain}); err == nil {
		t.Error("over-deep chain should error")
	}
}

// TestBuildPodManifest_TeamTemplateEditorMountsLayersRW verifies that when a
// session spec has the team-template-editor purpose label, the layers PVC is
// mounted read-write (required for SaveSessionAsTemplate to stage artifacts).
func TestBuildPodManifest_TeamTemplateEditorMountsLayersRW(t *testing.T) {
	b, _ := newBackendWithFakeClient(t)
	pod, err := b.buildPodManifest(sandbox.SessionSpec{
		SessionID:  "team-template-general",
		TemplateID: "@base",
		Labels: map[string]string{
			"astonish.io/purpose": "team-template-editor",
			"astonish.io/team":    "general",
		},
	})
	if err != nil {
		t.Fatalf("buildPodManifest: %v", err)
	}

	// Volume source should be RW (ReadOnly=false).
	for _, v := range pod.Spec.Volumes {
		if v.Name == volumeLayers {
			if v.PersistentVolumeClaim == nil {
				t.Fatalf("volume %q should be PVC-backed", volumeLayers)
			}
			if v.PersistentVolumeClaim.ReadOnly {
				t.Errorf("team-template-editor volume %q should be read-write, got read-only", volumeLayers)
			}
			break
		}
	}

	// VolumeMount should be RW (ReadOnly=false).
	c := pod.Spec.Containers[0]
	for _, vm := range c.VolumeMounts {
		if vm.Name == volumeLayers {
			if vm.ReadOnly {
				t.Errorf("team-template-editor mount %q should be read-write, got read-only", volumeLayers)
			}
			break
		}
	}
}

// ---------------------------------------------------------------------------
// buildResourceRequirements
// ---------------------------------------------------------------------------

func TestBuildResourceRequirements_ExplicitRequests(t *testing.T) {
	// When RequestCPUMillis and RequestMemoryMiB are explicitly set, they
	// should appear as-is in the pod spec Requests.
	lim := sandbox.ResourceLimits{
		CPUs:             2,
		MemoryMiB:        2048,
		DiskMiB:          4096,
		RequestCPUMillis: 200,
		RequestMemoryMiB: 512,
	}
	reqs := buildResourceRequirements(lim)

	// Limits should be unchanged.
	if got := reqs.Limits.Cpu().Value(); got != 2 {
		t.Errorf("Limits.CPU = %d, want 2", got)
	}
	if got := reqs.Limits.Memory().Value(); got != 2048*1024*1024 {
		t.Errorf("Limits.Memory = %d, want %d", got, 2048*1024*1024)
	}

	// Requests should reflect the explicit values.
	if got := reqs.Requests.Cpu().MilliValue(); got != 200 {
		t.Errorf("Requests.CPU = %dm, want 200m", got)
	}
	if got := reqs.Requests.Memory().Value(); got != 512*1024*1024 {
		t.Errorf("Requests.Memory = %d, want %d", got, 512*1024*1024)
	}
}

func TestBuildResourceRequirements_AutoDerive(t *testing.T) {
	// When request fields are zero, auto-derive from limits.
	lim := sandbox.ResourceLimits{
		CPUs:      2,
		MemoryMiB: 2048,
	}
	reqs := buildResourceRequirements(lim)

	// Auto-derived: CPU = max(50, 2*1000/20) = max(50, 100) = 100m
	if got := reqs.Requests.Cpu().MilliValue(); got != 100 {
		t.Errorf("Requests.CPU = %dm, want 100m (auto-derived)", got)
	}
	// Auto-derived: Memory = max(128, 2048/8) = max(128, 256) = 256 MiB
	if got := reqs.Requests.Memory().Value(); got != 256*1024*1024 {
		t.Errorf("Requests.Memory = %d, want %d (auto-derived)", got, 256*1024*1024)
	}
}

func TestBuildResourceRequirements_AutoDeriveMinimums(t *testing.T) {
	// When limits are tiny, auto-derivation applies minimums.
	lim := sandbox.ResourceLimits{
		CPUs:      1,   // 1*1000/20 = 50 → exactly the min
		MemoryMiB: 512, // 512/8 = 64 → below min 128
	}
	reqs := buildResourceRequirements(lim)

	// CPU: max(50, 50) = 50m
	if got := reqs.Requests.Cpu().MilliValue(); got != 50 {
		t.Errorf("Requests.CPU = %dm, want 50m (min)", got)
	}
	// Memory: max(128, 64) = 128 MiB
	if got := reqs.Requests.Memory().Value(); got != 128*1024*1024 {
		t.Errorf("Requests.Memory = %d, want %d (min 128Mi)", got, 128*1024*1024)
	}
}

func TestBuildResourceRequirements_ZeroLimits(t *testing.T) {
	// When limits are all zero, no requests should be set (no resource
	// requirements means the pod is BestEffort QoS — not typical, but
	// should not panic).
	reqs := buildResourceRequirements(sandbox.ResourceLimits{})
	if reqs.Limits != nil {
		t.Errorf("expected nil Limits, got %v", reqs.Limits)
	}
	if reqs.Requests != nil {
		t.Errorf("expected nil Requests, got %v", reqs.Requests)
	}
}

// ---------------------------------------------------------------------------
// CreateSession
// ---------------------------------------------------------------------------

func TestCreateSession_CreatesPodAndPersistsRegistry(t *testing.T) {
	b, cs := newBackendWithFakeClient(t)
	ctx := context.Background()

	sess, err := b.CreateSession(ctx, sandbox.SessionSpec{
		SessionID:  "sess-create-1",
		TemplateID: "python-dev",
		UserID:     "u1",
		OrgSlug:    "acme",
		Type:       sandbox.SessionTypeChat,
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if sess.SessionID != "sess-create-1" {
		t.Errorf("Session.SessionID = %q", sess.SessionID)
	}
	if sess.Type != sandbox.SessionTypeChat {
		t.Errorf("Session.Type = %q, want chat", sess.Type)
	}
	if sess.BackendRef == "" {
		t.Error("Session.BackendRef should be populated with pod name")
	}

	// Pod must exist.
	pod, err := cs.CoreV1().Pods("astonish-sandboxes").Get(ctx, podNameForSession("sess-create-1"), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("pod not created: %v", err)
	}
	if pod.Labels[labelSessionID] != "sess-create-1" {
		t.Errorf("pod label session-id = %q", pod.Labels[labelSessionID])
	}

	// Registry must record the k8s backend tag and pod name.
	rec, err := b.sessions.GetSession("sess-create-1")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if rec == nil {
		t.Fatal("registry record not found")
	}
	if rec.Backend != string(sandbox.BackendKindK8s) {
		t.Errorf("registry Backend = %q, want %q", rec.Backend, sandbox.BackendKindK8s)
	}
	if rec.PodName != pod.Name {
		t.Errorf("registry PodName = %q, want %q", rec.PodName, pod.Name)
	}
	if rec.CreatedBy != "u1" {
		t.Errorf("registry CreatedBy = %q", rec.CreatedBy)
	}
	if rec.UpperLayerID != "" {
		t.Errorf("registry UpperLayerID = %q, want empty until eviction", rec.UpperLayerID)
	}
}

func TestCreateSession_IdempotentOnSessionID(t *testing.T) {
	b, cs := newBackendWithFakeClient(t)
	ctx := context.Background()

	spec := sandbox.SessionSpec{SessionID: "s1", TemplateID: "t1"}
	if _, err := b.CreateSession(ctx, spec); err != nil {
		t.Fatalf("first CreateSession: %v", err)
	}
	// Second call with same ID must succeed (registry hit) and not
	// error out.
	sess, err := b.CreateSession(ctx, spec)
	if err != nil {
		t.Fatalf("second CreateSession: %v", err)
	}
	if sess.SessionID != "s1" {
		t.Errorf("Session.SessionID = %q", sess.SessionID)
	}

	// Exactly one pod should exist.
	list, _ := cs.CoreV1().Pods("astonish-sandboxes").List(ctx, metav1.ListOptions{})
	if len(list.Items) != 1 {
		t.Errorf("expected 1 pod, got %d", len(list.Items))
	}
}

// TestCreateSession_RecreatesAfterPodDeleted verifies that when the
// registry holds a stale record (pod was deleted out-of-band) a subsequent
// CreateSession call recreates the pod instead of returning the stale entry.
func TestCreateSession_RecreatesAfterPodDeleted(t *testing.T) {
	b, cs := newBackendWithFakeClient(t)
	ctx := context.Background()

	spec := sandbox.SessionSpec{SessionID: "s-stale", TemplateID: "t1"}

	// First create — pod is created, registry populated.
	sess1, err := b.CreateSession(ctx, spec)
	if err != nil {
		t.Fatalf("first CreateSession: %v", err)
	}
	podName := podNameForSession("s-stale")
	if _, getErr := cs.CoreV1().Pods("astonish-sandboxes").Get(ctx, podName, metav1.GetOptions{}); getErr != nil {
		t.Fatalf("pod should exist after first create: %v", getErr)
	}

	// Delete the pod out-of-band (simulates kubectl delete or node eviction).
	if err := cs.CoreV1().Pods("astonish-sandboxes").Delete(ctx, podName, metav1.DeleteOptions{}); err != nil {
		t.Fatalf("delete pod: %v", err)
	}
	// Verify pod is gone from API server.
	_, getErr := cs.CoreV1().Pods("astonish-sandboxes").Get(ctx, podName, metav1.GetOptions{})
	if !apierrors.IsNotFound(getErr) {
		t.Fatalf("expected NotFound after delete, got: %v", getErr)
	}

	// Second CreateSession with same spec — should recreate the pod.
	sess2, err := b.CreateSession(ctx, spec)
	if err != nil {
		t.Fatalf("second CreateSession after pod-deleted: %v", err)
	}
	if sess2.SessionID != sess1.SessionID {
		t.Errorf("session ID mismatch: %q vs %q", sess2.SessionID, sess1.SessionID)
	}

	// Pod must exist again.
	pod, getErr := cs.CoreV1().Pods("astonish-sandboxes").Get(ctx, podName, metav1.GetOptions{})
	if getErr != nil {
		t.Fatalf("pod should be recreated: %v", getErr)
	}
	if pod.Name != podName {
		t.Errorf("recreated pod name = %q, want %q", pod.Name, podName)
	}

	// Registry should reflect the new record.
	rec, _ := b.sessions.GetSession("s-stale")
	if rec == nil {
		t.Fatal("registry record missing after recreate")
	}
	if rec.State != store.SandboxSessionStateCreating {
		t.Errorf("registry state = %q, want %q", rec.State, store.SandboxSessionStateCreating)
	}
}

// ---------------------------------------------------------------------------
// SessionState
// ---------------------------------------------------------------------------

func TestSessionState_Absent(t *testing.T) {
	b, _ := newBackendWithFakeClient(t)
	st, err := b.SessionState(context.Background(), "nope")
	if err != nil {
		t.Fatalf("SessionState: %v", err)
	}
	if st != sandbox.SessionStateGone {
		t.Errorf("state for absent session = %q, want %q", st, sandbox.SessionStateGone)
	}
}

func TestSessionState_PodPhaseMapping(t *testing.T) {
	b, cs := newBackendWithFakeClient(t)
	ctx := context.Background()

	if _, err := b.CreateSession(ctx, sandbox.SessionSpec{SessionID: "s1", TemplateID: "t1"}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Patch the pod phase to Running+Ready and verify mapping.
	name := podNameForSession("s1")
	pod, _ := cs.CoreV1().Pods("astonish-sandboxes").Get(ctx, name, metav1.GetOptions{})
	pod.Status.Phase = corev1.PodRunning
	pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
	if _, err := cs.CoreV1().Pods("astonish-sandboxes").UpdateStatus(ctx, pod, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}
	if st, _ := b.SessionState(ctx, "s1"); st != sandbox.SessionStateRunning {
		t.Errorf("running+ready pod = %q, want running", st)
	}

	// Flip to Pending and verify it maps to creating.
	pod, _ = cs.CoreV1().Pods("astonish-sandboxes").Get(ctx, name, metav1.GetOptions{})
	pod.Status.Phase = corev1.PodPending
	pod.Status.Conditions = nil
	if _, err := cs.CoreV1().Pods("astonish-sandboxes").UpdateStatus(ctx, pod, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}
	if st, _ := b.SessionState(ctx, "s1"); st != sandbox.SessionStateCreating {
		t.Errorf("pending pod = %q, want creating", st)
	}
}

func TestSessionState_EvictedRecord(t *testing.T) {
	b, _ := newBackendWithFakeClient(t)
	_ = b.sessions.PutSession(&store.SandboxSession{
		SessionID: "s-evict",
		Backend:   string(sandbox.BackendKindK8s),
		State:     store.SandboxSessionStateEvicted,
	})
	st, err := b.SessionState(context.Background(), "s-evict")
	if err != nil {
		t.Fatalf("SessionState: %v", err)
	}
	if st != sandbox.SessionStateStopped {
		t.Errorf("evicted record = %q, want stopped", st)
	}
}

// ---------------------------------------------------------------------------
// StartSession
// ---------------------------------------------------------------------------

func TestStartSession_IdempotentOnPendingPod(t *testing.T) {
	b, _ := newBackendWithFakeClient(t)
	ctx := context.Background()

	// Create a session. The fake client creates the pod in Pending phase
	// by default (no status update from kubelet).
	_, err := b.CreateSession(ctx, sandbox.SessionSpec{SessionID: "s1", TemplateID: "t1"})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// StartSession on a Pending pod (SessionStateCreating) should succeed
	// — it's idempotent, meaning "ensure the session is running or will be".
	if err := b.StartSession(ctx, "s1"); err != nil {
		t.Errorf("StartSession on pending pod: got %v, want nil", err)
	}
}

func TestStartSession_IdempotentOnRunningPod(t *testing.T) {
	b, cs := newBackendWithFakeClient(t)
	ctx := context.Background()

	_, err := b.CreateSession(ctx, sandbox.SessionSpec{SessionID: "s1", TemplateID: "t1"})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Simulate kubelet marking the pod as Running+Ready.
	pod, _ := cs.CoreV1().Pods(b.cfg.Namespace).Get(ctx, podNameForSession("s1"), metav1.GetOptions{})
	pod.Status.Phase = corev1.PodRunning
	pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
	cs.CoreV1().Pods(b.cfg.Namespace).Update(ctx, pod, metav1.UpdateOptions{})

	if err := b.StartSession(ctx, "s1"); err != nil {
		t.Errorf("StartSession on running pod: got %v, want nil", err)
	}
}

func TestCreateSessionResumesEvictedSessionWithOriginalLayerChain(t *testing.T) {
	b, cs := newBackendWithFakeClient(t)
	ctx := context.Background()

	if _, err := b.CreateSession(ctx, sandbox.SessionSpec{SessionID: "s-evicted", TemplateID: "old-template", LayerChain: []string{"@base", "old-layer"}}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := cs.CoreV1().Pods(b.cfg.Namespace).Delete(ctx, podNameForSession("s-evicted"), metav1.DeleteOptions{}); err != nil {
		t.Fatalf("delete pod: %v", err)
	}
	rec, err := b.sessions.GetSession("s-evicted")
	if err != nil || rec == nil {
		t.Fatalf("GetSession: rec=%+v err=%v", rec, err)
	}
	rec.State = store.SandboxSessionStateEvicted
	rec.PodName = ""
	rec.UpperLayerID = encodePersistedLayerChain("@base,old-layer")
	if err := b.sessions.PutSession(rec); err != nil {
		t.Fatalf("PutSession: %v", err)
	}

	if _, err := b.CreateSession(ctx, sandbox.SessionSpec{SessionID: "s-evicted", TemplateID: "new-template", LayerChain: []string{"@base", "new-layer"}}); err != nil {
		t.Fatalf("CreateSession resume: %v", err)
	}
	pod, err := cs.CoreV1().Pods(b.cfg.Namespace).Get(ctx, podNameForSession("s-evicted"), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("pod should be recreated: %v", err)
	}
	if got := pod.Annotations[annotationLayerChain]; got != "@base,old-layer" {
		t.Fatalf("resumed pod layer chain = %q, want original chain", got)
	}
}

func TestStartSession_RecreatesCompletedPod(t *testing.T) {
	b, cs := newBackendWithFakeClient(t)
	ctx := context.Background()

	_, err := b.CreateSession(ctx, sandbox.SessionSpec{SessionID: "s1", TemplateID: "t1", LayerChain: []string{"@base", "team-layer"}})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	podName := podNameForSession("s1")
	pod, _ := cs.CoreV1().Pods(b.cfg.Namespace).Get(ctx, podName, metav1.GetOptions{})
	pod.Status.Phase = corev1.PodSucceeded
	if _, err := cs.CoreV1().Pods(b.cfg.Namespace).UpdateStatus(ctx, pod, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}
	rec, err := b.sessions.GetSession("s1")
	if err != nil || rec == nil {
		t.Fatalf("GetSession: rec=%+v err=%v", rec, err)
	}
	rec.UpperLayerID = encodePersistedLayerChain("@base,team-layer")
	if err := b.sessions.PutSession(rec); err != nil {
		t.Fatalf("PutSession: %v", err)
	}

	if err := b.StartSession(ctx, "s1"); err != nil {
		t.Fatalf("StartSession on completed/gone pod: %v", err)
	}
	resumedPod, err := cs.CoreV1().Pods(b.cfg.Namespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("pod should be recreated: %v", err)
	}
	if got := resumedPod.Annotations[annotationLayerChain]; got != "@base,team-layer" {
		t.Fatalf("resumed pod layer chain = %q, want original chain", got)
	}
	rec, _ = b.sessions.GetSession("s1")
	if rec == nil || rec.PodName != podName || rec.State != store.SandboxSessionStateCreating {
		t.Fatalf("registry after resume = %+v, want pod %q creating", rec, podName)
	}
}

// ---------------------------------------------------------------------------
// StopSession
// ---------------------------------------------------------------------------

func TestStopSession_DeletesPodAndMarksEvicted(t *testing.T) {
	b, cs := newBackendWithFakeClient(t)
	ctx := context.Background()
	if _, err := b.CreateSession(ctx, sandbox.SessionSpec{SessionID: "s1", TemplateID: "t1"}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	se := stubFactory(t, b, func(_ context.Context, opts remotecommand.StreamOptions) error {
		_, _ = opts.Stdout.Write([]byte("persisted"))
		return nil
	})

	if err := b.StopSession(ctx, "s1"); err != nil {
		t.Fatalf("StopSession: %v", err)
	}
	cmds := se.url.Query()["command"]
	if len(cmds) < 3 || cmds[0] != "/bin/sh" || cmds[1] != "-c" {
		t.Fatalf("persist command = %v, want /bin/sh -c script", cmds)
	}
	script := cmds[2]
	for _, want := range []string{"/var/astonish/overlay/upper", "SESSION_DIR=\"/mnt/astonish-uppers\"", "FINAL=\"$SESSION_DIR/upper.tar.zst\"", "upper.tar.zst.tmp", "--exclude=/var/astonish/overlay/upper/mnt/astonish-uppers", "mv \"$TMP\" \"$FINAL\""} {
		if !strings.Contains(script, want) {
			t.Errorf("persist script missing %q:\n%s", want, script)
		}
	}

	// Pod should be gone.
	if _, err := cs.CoreV1().Pods("astonish-sandboxes").Get(ctx, podNameForSession("s1"), metav1.GetOptions{}); err == nil {
		t.Error("pod should have been deleted")
	} else if !apierrors.IsNotFound(err) {
		t.Errorf("unexpected error checking deleted pod: %v", err)
	}

	// Registry row should remain with state=evicted and no PodName.
	rec, _ := b.sessions.GetSession("s1")
	if rec == nil {
		t.Fatal("registry record should persist after stop")
	}
	if rec.State != store.SandboxSessionStateEvicted {
		t.Errorf("registry State = %q, want evicted", rec.State)
	}
	if rec.PodName != "" {
		t.Errorf("registry PodName should be cleared, got %q", rec.PodName)
	}
}

func TestStopSession_DoesNotDeletePodWhenPersistFails(t *testing.T) {
	b, cs := newBackendWithFakeClient(t)
	ctx := context.Background()
	if _, err := b.CreateSession(ctx, sandbox.SessionSpec{SessionID: "s-persist-fail", TemplateID: "t1"}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	stubFactory(t, b, func(_ context.Context, opts remotecommand.StreamOptions) error {
		_, _ = opts.Stderr.Write([]byte("tar failed"))
		return k8sexec.CodeExitError{Err: fmt.Errorf("exit 2"), Code: 2}
	})

	err := b.StopSession(ctx, "s-persist-fail")
	if err == nil || !strings.Contains(err.Error(), "persist upper exited") {
		t.Fatalf("StopSession error = %v, want persist failure", err)
	}
	if _, err := cs.CoreV1().Pods(b.cfg.Namespace).Get(ctx, podNameForSession("s-persist-fail"), metav1.GetOptions{}); err != nil {
		t.Fatalf("pod should remain after failed persist: %v", err)
	}
	rec, _ := b.sessions.GetSession("s-persist-fail")
	if rec == nil || rec.State == store.SandboxSessionStateEvicted {
		t.Fatalf("session should not be final evicted after failed persist: %+v", rec)
	}
}

func TestUpperDirNameForSessionRejectsUnsafeIDs(t *testing.T) {
	bad := []string{"", ".", "..", "../other", "abc;rm -rf /", "$(cat /secret)", "abc/def", "team session"}
	for _, id := range bad {
		if got, err := upperDirNameForSession(id); err == nil {
			t.Fatalf("upperDirNameForSession(%q) = %q, want error", id, got)
		}
	}
	if got, err := upperDirNameForSession("aa4a9422-75d1-4073-b238-b0e9f4e94462"); err != nil || got == "" {
		t.Fatalf("valid session ID rejected: got=%q err=%v", got, err)
	}
}

func TestCreateSessionRejectsUnsafeSessionIDForUpperIsolation(t *testing.T) {
	b, _ := newBackendWithFakeClient(t)
	_, err := b.CreateSession(context.Background(), sandbox.SessionSpec{SessionID: "../other", TemplateID: "t1"})
	if err == nil || !strings.Contains(err.Error(), "not safe for upper persistence") {
		t.Fatalf("CreateSession unsafe ID error = %v, want upper isolation validation", err)
	}
}

func TestStopSession_AbsentIsNoOp(t *testing.T) {
	b, _ := newBackendWithFakeClient(t)
	if err := b.StopSession(context.Background(), "never-existed"); err != nil {
		t.Errorf("StopSession on absent session should be no-op, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// DestroySession
// ---------------------------------------------------------------------------

func TestDestroySession_DeletesPodAndRegistry(t *testing.T) {
	b, cs := newBackendWithFakeClient(t)
	ctx := context.Background()
	if _, err := b.CreateSession(ctx, sandbox.SessionSpec{SessionID: "s1", TemplateID: "t1"}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	if err := b.DestroySession(ctx, "s1"); err != nil {
		t.Fatalf("DestroySession: %v", err)
	}

	if _, err := cs.CoreV1().Pods("astonish-sandboxes").Get(ctx, podNameForSession("s1"), metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("pod not deleted: err=%v", err)
	}
	if rec, _ := b.sessions.GetSession("s1"); rec != nil {
		t.Errorf("registry record should be gone, still present: %+v", rec)
	}
}

func TestDestroySession_Idempotent(t *testing.T) {
	b, _ := newBackendWithFakeClient(t)
	if err := b.DestroySession(context.Background(), "never-existed"); err != nil {
		t.Errorf("DestroySession on absent session should succeed, got %v", err)
	}
}

func TestDestroySession_CleansUpEvictedUpper(t *testing.T) {
	b, _ := newBackendWithFakeClient(t)
	ctx := context.Background()

	// Install reactor to auto-complete upper-gc pods.
	autoCompleteUpperGCPods(t, b)

	// Create and then stop (evict) a session.
	if _, err := b.CreateSession(ctx, sandbox.SessionSpec{SessionID: "s-evict", TemplateID: "t1"}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	stubFactory(t, b, func(_ context.Context, _ remotecommand.StreamOptions) error { return nil })
	if err := b.StopSession(ctx, "s-evict"); err != nil {
		t.Fatalf("StopSession: %v", err)
	}

	// Verify session is evicted.
	rec, _ := b.sessions.GetSession("s-evict")
	if rec == nil || rec.State != store.SandboxSessionStateEvicted {
		t.Fatalf("expected evicted state, got %+v", rec)
	}

	// DestroySession should spawn an upper-gc pod.
	if err := b.DestroySession(ctx, "s-evict"); err != nil {
		t.Fatalf("DestroySession: %v", err)
	}

	// Verify registry row is gone.
	if rec, _ := b.sessions.GetSession("s-evict"); rec != nil {
		t.Errorf("registry record should be gone, still present: %+v", rec)
	}
}

// autoCompleteUpperGCPods installs a reactor that transitions upper-gc pods
// to Succeeded on create.
func autoCompleteUpperGCPods(t *testing.T, b *K8sBackend) {
	t.Helper()
	fakeCS, ok := b.client.(interface {
		PrependReactor(verb, resource string, r clientgotesting.ReactionFunc)
	})
	if !ok {
		t.Fatalf("client does not support PrependReactor")
	}
	fakeCS.PrependReactor("create", "pods", func(action clientgotesting.Action) (bool, runtime.Object, error) {
		ca, ok := action.(clientgotesting.CreateAction)
		if !ok {
			return false, nil, nil
		}
		pod, ok := ca.GetObject().(*corev1.Pod)
		if !ok {
			return false, nil, nil
		}
		if pod.Labels[labelType] != "upper-gc" {
			return false, nil, nil
		}
		pod.Status.Phase = corev1.PodSucceeded
		return false, nil, nil
	})
}

// ---------------------------------------------------------------------------
// ListSessions
// ---------------------------------------------------------------------------

func TestListSessions_FiltersByLabelAndState(t *testing.T) {
	b, _ := newBackendWithFakeClient(t)
	ctx := context.Background()

	// Seed three sessions: one running (acme), one pending (acme),
	// and one in another org.
	create := func(id, org string, phase corev1.PodPhase, ready bool) {
		if _, err := b.CreateSession(ctx, sandbox.SessionSpec{
			SessionID:  id,
			TemplateID: "t1",
			OrgSlug:    org,
		}); err != nil {
			t.Fatalf("CreateSession %s: %v", id, err)
		}
		pod, _ := b.client.CoreV1().Pods("astonish-sandboxes").Get(ctx, podNameForSession(id), metav1.GetOptions{})
		pod.Status.Phase = phase
		if ready {
			pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
		}
		if _, err := b.client.CoreV1().Pods("astonish-sandboxes").UpdateStatus(ctx, pod, metav1.UpdateOptions{}); err != nil {
			t.Fatalf("UpdateStatus %s: %v", id, err)
		}
	}
	create("run-a", "acme", corev1.PodRunning, true)
	create("pending-a", "acme", corev1.PodPending, false)
	create("run-b", "other", corev1.PodRunning, true)

	// No filter: all three session-typed pods.
	got, err := b.ListSessions(ctx, sandbox.SessionFilter{})
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(got) != 3 {
		t.Errorf("ListSessions all = %d, want 3", len(got))
	}

	// Org filter: only acme sessions.
	got, err = b.ListSessions(ctx, sandbox.SessionFilter{OrgSlug: "acme"})
	if err != nil {
		t.Fatalf("ListSessions acme: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("ListSessions acme = %d, want 2", len(got))
	}

	// State filter: only running.
	got, err = b.ListSessions(ctx, sandbox.SessionFilter{State: sandbox.SessionStateRunning})
	if err != nil {
		t.Fatalf("ListSessions running: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("ListSessions running = %d, want 2", len(got))
	}
}

// ---------------------------------------------------------------------------
// Start/Stop when no Client configured — legacy skeleton paths.
// ---------------------------------------------------------------------------

func TestLifecycle_NoClient_StubsStillApply(t *testing.T) {
	// Without a Client, the real lifecycle paths fall back to
	// ErrNotImplementedYet (except DestroySession which must drain the
	// registry idempotently). This matches the skeleton behaviour.
	b, err := New(Config{Sessions: newRegistry(t)})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()

	if _, err := b.CreateSession(ctx, sandbox.SessionSpec{SessionID: "s", TemplateID: "t"}); !errors.Is(err, ErrNotImplementedYet) {
		t.Errorf("CreateSession without Client: got %v, want ErrNotImplementedYet", err)
	}
	if err := b.StartSession(ctx, "s"); !errors.Is(err, ErrNotImplementedYet) {
		t.Errorf("StartSession without Client: got %v, want ErrNotImplementedYet", err)
	}
	if err := b.StopSession(ctx, "s"); !errors.Is(err, ErrNotImplementedYet) {
		t.Errorf("StopSession without Client: got %v, want ErrNotImplementedYet", err)
	}
	if _, err := b.SessionState(ctx, "s"); !errors.Is(err, ErrNotImplementedYet) {
		t.Errorf("SessionState without Client: got %v, want ErrNotImplementedYet", err)
	}
	if _, err := b.ListSessions(ctx, sandbox.SessionFilter{}); !errors.Is(err, ErrNotImplementedYet) {
		t.Errorf("ListSessions without Client: got %v, want ErrNotImplementedYet", err)
	}
	// DestroySession must still succeed on an absent session.
	if err := b.DestroySession(ctx, "never-existed"); err != nil {
		t.Errorf("DestroySession without Client on absent session: got %v, want nil", err)
	}
}

// TestStateMapping isolates the helpers (no client required).
func TestStateMapping(t *testing.T) {
	cases := []struct {
		phase corev1.PodPhase
		ready bool
		want  sandbox.SessionState
	}{
		{corev1.PodPending, false, sandbox.SessionStateCreating},
		{corev1.PodRunning, false, sandbox.SessionStateCreating},
		{corev1.PodRunning, true, sandbox.SessionStateRunning},
		{corev1.PodSucceeded, false, sandbox.SessionStateStopped},
		{corev1.PodFailed, false, sandbox.SessionStateStopped},
	}
	for _, c := range cases {
		pod := &corev1.Pod{Status: corev1.PodStatus{Phase: c.phase}}
		if c.ready {
			pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
		}
		if got := podPhaseToSessionState(pod); got != c.want {
			t.Errorf("phase=%v ready=%v -> %q, want %q", c.phase, c.ready, got, c.want)
		}
	}
}

// TestLabelSelector is a targeted unit check of the selector helper.
func TestLabelSelector(t *testing.T) {
	cases := []struct {
		in   sandbox.SessionFilter
		want string
	}{
		{sandbox.SessionFilter{}, "astonish.io/type=session"},
		{sandbox.SessionFilter{Type: sandbox.SessionTypeFleet}, "astonish.io/type=fleet"},
		{sandbox.SessionFilter{OrgSlug: "acme"}, "astonish.io/type=session,astonish.io/org=acme"},
		{sandbox.SessionFilter{TeamSlug: "data"}, "astonish.io/type=session,astonish.io/team=data"},
	}
	for _, c := range cases {
		if got := labelSelectorFor(c.in); got != c.want {
			t.Errorf("labelSelectorFor(%+v) = %q, want %q", c.in, got, c.want)
		}
	}
}
