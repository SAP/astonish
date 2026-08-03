package k8s

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
)

func TestOrphanUpperCandidatesHonorsGraceAndSafety(t *testing.T) {
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	known := map[string]bool{"known-session": true}
	dirs := []gcDirInfo{
		{Name: "known-session", ModTime: now.Add(-48 * time.Hour)},
		{Name: "old-orphan", ModTime: now.Add(-2 * time.Hour)},
		{Name: "young-orphan", ModTime: now.Add(-10 * time.Minute)},
		{Name: "../unsafe", ModTime: now.Add(-2 * time.Hour)},
	}

	got := orphanUpperCandidates(dirs, known, now, time.Hour, 0)
	if len(got) != 1 || got[0] != "old-orphan" {
		t.Fatalf("orphanUpperCandidates() = %#v, want [old-orphan]", got)
	}
}

func TestStaleEvictedUpperCandidates(t *testing.T) {
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	old := now.Add(-15 * 24 * time.Hour)
	young := now.Add(-2 * time.Hour)
	sessions := []store.SandboxSessionGCInfo{
		{SandboxSession: store.SandboxSession{SessionID: "old-evicted", Backend: string(sandbox.BackendKindK8s), State: store.SandboxSessionStateEvicted, LastActiveAt: old}},
		{SandboxSession: store.SandboxSession{SessionID: "young-evicted", Backend: string(sandbox.BackendKindK8s), State: store.SandboxSessionStateEvicted, LastActiveAt: young}},
		{SandboxSession: store.SandboxSession{SessionID: "pinned-evicted", Backend: string(sandbox.BackendKindK8s), State: store.SandboxSessionStateEvicted, LastActiveAt: old, Pinned: true}},
		{SandboxSession: store.SandboxSession{SessionID: "running-session", Backend: string(sandbox.BackendKindK8s), State: store.SandboxSessionStateRunning, LastActiveAt: old}},
		{SandboxSession: store.SandboxSession{SessionID: "with-pod", Backend: string(sandbox.BackendKindK8s), State: store.SandboxSessionStateEvicted, LastActiveAt: old, PodName: "astn-sess-with-pod"}},
		{SandboxSession: store.SandboxSession{SessionID: "openshell-session", Backend: string(sandbox.BackendKindOpenShell), State: store.SandboxSessionStateEvicted, LastActiveAt: old}},
		{SandboxSession: store.SandboxSession{SessionID: "../unsafe", Backend: string(sandbox.BackendKindK8s), State: store.SandboxSessionStateEvicted, LastActiveAt: old}},
		{SandboxSession: store.SandboxSession{SessionID: "live-pod", Backend: string(sandbox.BackendKindK8s), State: store.SandboxSessionStateEvicted, LastActiveAt: old}},
	}

	got := staleEvictedUpperCandidates(sessions, map[string]bool{"live-pod": true}, now, 14*24*time.Hour, 0)
	if len(got) != 1 || got[0].SessionID != "old-evicted" {
		t.Fatalf("staleEvictedUpperCandidates() = %#v, want only old-evicted", got)
	}
}

func TestStaleEvictedUpperCandidatesUsesUpdatedAtFallback(t *testing.T) {
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	sessions := []store.SandboxSessionGCInfo{
		{SandboxSession: store.SandboxSession{SessionID: "updated-old", Backend: string(sandbox.BackendKindK8s), State: store.SandboxSessionStateEvicted, UpdatedAt: now.Add(-20 * 24 * time.Hour)}},
	}

	got := staleEvictedUpperCandidates(sessions, nil, now, 14*24*time.Hour, 0)
	if len(got) != 1 || got[0].SessionID != "updated-old" {
		t.Fatalf("staleEvictedUpperCandidates() = %#v, want updated-old", got)
	}
}

func TestStaleEvictedUpperCandidatesHonorsLimit(t *testing.T) {
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	old := now.Add(-20 * 24 * time.Hour)
	sessions := []store.SandboxSessionGCInfo{
		{SandboxSession: store.SandboxSession{SessionID: "old-1", Backend: string(sandbox.BackendKindK8s), State: store.SandboxSessionStateEvicted, LastActiveAt: old}},
		{SandboxSession: store.SandboxSession{SessionID: "old-2", Backend: string(sandbox.BackendKindK8s), State: store.SandboxSessionStateEvicted, LastActiveAt: old}},
		{SandboxSession: store.SandboxSession{SessionID: "old-3", Backend: string(sandbox.BackendKindK8s), State: store.SandboxSessionStateEvicted, LastActiveAt: old}},
	}

	got := staleEvictedUpperCandidates(sessions, nil, now, 14*24*time.Hour, 2)
	if len(got) != 2 || got[0].SessionID != "old-1" || got[1].SessionID != "old-2" {
		t.Fatalf("staleEvictedUpperCandidates() = %#v, want old-1 and old-2", got)
	}
}

func TestChunkSandboxSessionGCInfo(t *testing.T) {
	items := []store.SandboxSessionGCInfo{
		{SandboxSession: store.SandboxSession{SessionID: "1"}},
		{SandboxSession: store.SandboxSession{SessionID: "2"}},
		{SandboxSession: store.SandboxSession{SessionID: "3"}},
	}

	got := chunkSandboxSessionGCInfo(items, 2)
	if len(got) != 2 || len(got[0]) != 2 || len(got[1]) != 1 {
		t.Fatalf("chunkSandboxSessionGCInfo() = %#v, want chunk lengths 2 and 1", got)
	}
}

func TestGCLiveSandboxSessions(t *testing.T) {
	client := fake.NewSimpleClientset(
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "running", Namespace: "ns", Labels: map[string]string{labelType: typeSession, labelSessionID: "running-session"}}, Status: corev1.PodStatus{Phase: corev1.PodRunning}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pending", Namespace: "ns", Labels: map[string]string{labelType: typeFleet, labelSessionID: "pending-session"}}, Status: corev1.PodStatus{Phase: corev1.PodPending}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "succeeded", Namespace: "ns", Labels: map[string]string{labelType: typeSession, labelSessionID: "done-session"}}, Status: corev1.PodStatus{Phase: corev1.PodSucceeded}},
	)

	got := gcLiveSandboxSessions(t.Context(), GCReconcilerConfig{Client: client, Namespace: "ns"})
	if !got["running-session"] || !got["pending-session"] {
		t.Fatalf("gcLiveSandboxSessions() = %#v, want running and pending sessions", got)
	}
	if got["done-session"] {
		t.Fatalf("gcLiveSandboxSessions() included succeeded pod: %#v", got)
	}
}
