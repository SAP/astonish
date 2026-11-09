package k8s

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/SAP/astonish/pkg/sandbox"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func homeReservations(t *testing.T, b *K8sBackend, h *sandbox.BotHomeBinding) map[string]botHomeReservation {
	t.Helper()
	cm, err := b.client.CoreV1().ConfigMaps(b.cfg.Namespace).Get(context.Background(), botHomeFenceName(h), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var rs map[string]botHomeReservation
	if err = json.Unmarshal([]byte(cm.Data["reservations"]), &rs); err != nil {
		t.Fatal(err)
	}
	return rs
}

func sharedHomeBackend(t *testing.T, b *K8sBackend) *K8sBackend {
	t.Helper()
	other, _ := newBackendWithFakeClient(t)
	other.client = b.client
	other.cfg.HomesPVCName = "homes"
	other.sessions = b.sessions
	return other
}

// The default fake tracker does not implement resourceVersion CAS. These
// reactors model that API guarantee, including conflicts on stale updates.
func enforceHomeCAS(b *K8sBackend) {
	client := b.client.(*fake.Clientset)
	var version int
	client.PrependReactor("create", "configmaps", func(a ktesting.Action) (bool, runtime.Object, error) {
		cm := a.(ktesting.CreateAction).GetObject().(*corev1.ConfigMap)
		version++
		cm.ResourceVersion = fmt.Sprint(version)
		return false, nil, nil
	})
	client.PrependReactor("update", "configmaps", func(a ktesting.Action) (bool, runtime.Object, error) {
		cm := a.(ktesting.UpdateAction).GetObject().(*corev1.ConfigMap)
		obj, err := client.Tracker().Get(corev1.SchemeGroupVersion.WithResource("configmaps"), cm.Namespace, cm.Name)
		if err != nil {
			return true, nil, err
		}
		if obj.(*corev1.ConfigMap).ResourceVersion != cm.ResourceVersion {
			return true, nil, apierrors.NewConflict(schema.GroupResource{Resource: "configmaps"}, cm.Name, errors.New("stale revision"))
		}
		version++
		cm.ResourceVersion = fmt.Sprint(version)
		return false, nil, nil
	})
}

func TestBotHomeAttachmentCASRetryBeforeMount(t *testing.T) {
	b, h := homeBackend(t)
	ctx := context.Background()
	client := b.client.(*fake.Clientset)
	if _, err := b.CreateSession(ctx, sandbox.SessionSpec{SessionID: "seed", BotHome: h}); err != nil {
		t.Fatal(err)
	}
	conflicted := false
	client.PrependReactor("update", "configmaps", func(a ktesting.Action) (bool, runtime.Object, error) {
		cm := a.(ktesting.UpdateAction).GetObject().(*corev1.ConfigMap)
		if cm.Name == botHomeFenceName(h) && !conflicted {
			conflicted = true
			return true, nil, apierrors.NewConflict(schema.GroupResource{Resource: "configmaps"}, cm.Name, errors.New("racing attachment"))
		}
		return false, nil, nil
	})
	client.PrependReactor("create", "pods", func(a ktesting.Action) (bool, runtime.Object, error) {
		p := a.(ktesting.CreateAction).GetObject().(*corev1.Pod)
		obj, err := client.Tracker().Get(corev1.SchemeGroupVersion.WithResource("configmaps"), b.cfg.Namespace, botHomeFenceName(h))
		if err != nil {
			t.Fatal(err)
		}
		var rs map[string]botHomeReservation
		if err = json.Unmarshal([]byte(obj.(*corev1.ConfigMap).Data["reservations"]), &rs); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, r := range rs {
			if r.Pod == p.Name && r.Pending {
				found = true
			}
		}
		if !found {
			t.Fatal("pod submitted before durable reservation")
		}
		return false, nil, nil
	})
	if _, err := sharedHomeBackend(t, b).CreateSession(ctx, sandbox.SessionSpec{SessionID: "retry", BotHome: h}); err != nil {
		t.Fatal(err)
	}
	if !conflicted || len(homeReservations(t, b, h)) != 2 {
		t.Fatal("CAS retry lost reservations")
	}
}

func TestBotHomeAttachmentReaderAmbiguityAndDeniedReservation(t *testing.T) {
	for _, mode := range []string{"ambiguous-helper", "denied-reservation"} {
		t.Run(mode, func(t *testing.T) {
			b, h := homeBackend(t)
			client := b.client.(*fake.Clientset)
			created := false
			client.PrependReactor("create", "pods", func(ktesting.Action) (bool, runtime.Object, error) {
				created = true
				return true, nil, errors.New("create outcome unknown")
			})
			if mode == "denied-reservation" {
				client.PrependReactor("create", "configmaps", func(ktesting.Action) (bool, runtime.Object, error) { return true, nil, errors.New("forbidden") })
			}
			if _, err := b.ReadBotHomeArtifact(context.Background(), h, "file", 10); err == nil {
				t.Fatal("accepted failed attachment")
			}
			if mode == "denied-reservation" {
				if created {
					t.Fatal("mounted without reservation")
				}
				return
			}
			rs := homeReservations(t, sharedHomeBackend(t, b), h)
			if len(rs) != 1 {
				t.Fatal("ambiguous helper reservation lost")
			}
			for _, r := range rs {
				if !r.Pending {
					t.Fatal("absent helper incorrectly settled")
				}
			}
		})
	}
}

func TestBotHomeAttachmentTerminalRejectsLifecycle(t *testing.T) {
	for _, state := range []botHomeFenceState{botHomeDeleting, botHomeDeleted} {
		t.Run(string(state), func(t *testing.T) {
			b, h := homeBackend(t)
			ctx := context.Background()
			if _, err := b.CreateSession(ctx, sandbox.SessionSpec{SessionID: "first", BotHome: h}); err != nil {
				t.Fatal(err)
			}
			cms := b.client.CoreV1().ConfigMaps(b.cfg.Namespace)
			cm, err := cms.Get(ctx, botHomeFenceName(h), metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			cm.Data["state"] = string(state)
			if _, err = cms.Update(ctx, cm, metav1.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
			other := sharedHomeBackend(t, b)
			if _, err := other.CreateSession(ctx, sandbox.SessionSpec{SessionID: "second", BotHome: h}); err == nil {
				t.Fatal("attached terminal home")
			}
			if err := other.StartSession(ctx, "first"); err == nil {
				t.Fatal("recovered terminal home")
			}
			if _, err := other.ReadBotHomeArtifact(ctx, h, "file", 10); err == nil {
				t.Fatal("read terminal home")
			}
		})
	}
}

func TestBotHomeAttachmentConcurrentBackends(t *testing.T) {
	b, h := homeBackend(t)
	enforceHomeCAS(b)
	other := sharedHomeBackend(t, b)
	ctx := context.Background()
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for i, backend := range []*K8sBackend{b, other} {
		wg.Add(1)
		go func(i int, backend *K8sBackend) {
			defer wg.Done()
			<-start
			_, err := backend.CreateSession(ctx, sandbox.SessionSpec{SessionID: fmt.Sprintf("concurrent-%d", i), BotHome: h})
			errs <- err
		}(i, backend)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	rs := homeReservations(t, b, h)
	if len(rs) != 2 {
		t.Fatalf("lost cross-instance reservation: %+v", rs)
	}
	for _, r := range rs {
		if r.Pending {
			t.Fatal("successful create not settled")
		}
	}
	for i := 0; i < 2; i++ {
		if err := other.DestroySession(ctx, fmt.Sprintf("concurrent-%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if len(homeReservations(t, b, h)) != 0 {
		t.Fatal("destroy failed to release observed-absent pods")
	}
}

func TestBotHomeAttachmentAmbiguousCreateSurvivesRecovery(t *testing.T) {
	b, h := homeBackend(t)
	ctx := context.Background()
	client := b.client.(*fake.Clientset)
	ambiguous := true
	client.PrependReactor("create", "pods", func(a ktesting.Action) (bool, runtime.Object, error) {
		if ambiguous {
			ambiguous = false
			return true, nil, errors.New("transport lost; server outcome unknown")
		}
		return false, nil, nil
	})
	spec := sandbox.SessionSpec{SessionID: "ambiguous", BotHome: h}
	if _, err := b.CreateSession(ctx, spec); err == nil {
		t.Fatal("accepted ambiguous create")
	}
	other := sharedHomeBackend(t, b)
	if _, err := other.CreateSession(ctx, spec); err != nil {
		t.Fatal(err)
	}
	rs := homeReservations(t, b, h)
	pending := 0
	for _, r := range rs {
		if r.Pending {
			pending++
		}
	}
	if len(rs) != 2 || pending != 1 {
		t.Fatalf("recovery stole paused worker reservation: %+v", rs)
	}
	if err := other.DestroySession(ctx, spec.SessionID); err == nil {
		t.Fatal("destroy released ambiguous creator")
	}
	if len(homeReservations(t, b, h)) != 2 {
		t.Fatal("lost ambiguous exclusion")
	}
}

func TestBotHomeAttachmentDestroyWaitsForAbsence(t *testing.T) {
	b, h := homeBackend(t)
	ctx := context.Background()
	spec := sandbox.SessionSpec{SessionID: "finalizer", BotHome: h}
	if _, err := b.CreateSession(ctx, spec); err != nil {
		t.Fatal(err)
	}
	b.client.(*fake.Clientset).PrependReactor("delete", "pods", func(ktesting.Action) (bool, runtime.Object, error) { return true, nil, nil })
	if err := sharedHomeBackend(t, b).DestroySession(ctx, spec.SessionID); err == nil {
		t.Fatal("delete acknowledgement mistaken for physical absence")
	}
	if len(homeReservations(t, b, h)) != 1 {
		t.Fatal("released existing pod")
	}
}

func TestBotHomeCancelUnsubmittedReservation(t *testing.T) {
	b, h := homeBackend(t)
	ctx := context.Background()
	reserve := func() string {
		token, err := b.reserveBotHome(ctx, h, "same-pod")
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	foreign, settled, own := reserve(), reserve(), reserve()
	if err := b.settleBotHome(ctx, h, settled); err != nil {
		t.Fatal(err)
	}
	if err := b.cancelUnsubmittedBotHome(ctx, h, own); err != nil {
		t.Fatal(err)
	}
	rs := homeReservations(t, b, h)
	if len(rs) != 2 || !rs[foreign].Pending || rs[settled].Pending {
		t.Fatalf("cancellation changed other owners: %+v", rs)
	}
	for _, token := range []string{own, settled, "unknown"} {
		if err := b.cancelUnsubmittedBotHome(ctx, h, token); err == nil {
			t.Fatal("canceled missing or settled token")
		}
	}
	if len(homeReservations(t, b, h)) != 2 {
		t.Fatal("failed cancellation changed reservations")
	}
}

func TestBotHomeAttachmentLocalPreflightCleanupFailure(t *testing.T) {
	b, h := homeBackend(t)
	b.cfg.MaxChainDepth = 1
	denied := errors.New("fence update denied")
	b.client.(*fake.Clientset).PrependReactor("update", "configmaps", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, denied
	})
	_, err := b.CreateSession(context.Background(), sandbox.SessionSpec{
		SessionID: "preflight-cleanup-failure", BotHome: h, LayerChain: []string{"base", "team"},
	})
	var preflight *sessionPodPreflightError
	if !errors.As(err, &preflight) || !errors.Is(err, denied) {
		t.Fatalf("must report both rejection and failed cancellation: %v", err)
	}
	rs := homeReservations(t, b, h)
	if len(rs) != 1 {
		t.Fatalf("failed cancellation lost reservation: %+v", rs)
	}
	for _, r := range rs {
		if !r.Pending {
			t.Fatal("failed cancellation settled reservation")
		}
	}
	if err := sharedHomeBackend(t, b).DeleteBotHome(context.Background(), h); err == nil {
		t.Fatal("purged despite failed cancellation")
	}
}

func TestBotHomeAttachmentLocalPreflightPreservesAmbiguousOwner(t *testing.T) {
	for _, createErr := range []error{errors.New("unknown create outcome"), context.Canceled} {
		t.Run(createErr.Error(), func(t *testing.T) {
			b, h := homeBackend(t)
			ctx := context.Background()
			spec := sandbox.SessionSpec{SessionID: "ambiguous-preflight", BotHome: h}
			b.client.(*fake.Clientset).PrependReactor("create", "pods", func(ktesting.Action) (bool, runtime.Object, error) {
				return true, nil, createErr
			})
			if _, err := b.CreateSession(ctx, spec); !errors.Is(err, createErr) {
				t.Fatalf("expected API failure: %v", err)
			}
			before := homeReservations(t, b, h)
			if len(before) != 1 {
				t.Fatal("lost ambiguous reservation")
			}
			other := sharedHomeBackend(t, b)
			other.cfg.MaxChainDepth = 1
			spec.LayerChain = []string{"base", "team"}
			if _, err := other.CreateSession(ctx, spec); err == nil || !strings.Contains(err.Error(), "layer chain depth") {
				t.Fatalf("expected local rejection: %v", err)
			}
			after := homeReservations(t, b, h)
			if len(after) != 1 {
				t.Fatalf("local failure leaked token: %+v", after)
			}
			for token, r := range before {
				if !r.Pending || after[token] != r {
					t.Fatal("local failure stole older unresolved token")
				}
			}
			if err := other.DeleteBotHome(ctx, h); err == nil {
				t.Fatal("purged with ambiguous creator")
			}
		})
	}
}

func TestBotHomeAttachmentLocalPreflightRetryAndPurge(t *testing.T) {
	for _, mode := range []string{"create", "start", "recover"} {
		t.Run(mode, func(t *testing.T) {
			b, h := homeBackend(t)
			ctx := context.Background()
			spec := sandbox.SessionSpec{SessionID: "preflight", BotHome: h, LayerChain: []string{"base", "team"}}
			b.cfg.MaxChainDepth = 2
			if mode != "create" {
				if _, err := b.CreateSession(ctx, spec); err != nil {
					t.Fatal(err)
				}
				if err := b.client.CoreV1().Pods(b.cfg.Namespace).Delete(ctx, podNameForSession(spec.SessionID), metav1.DeleteOptions{}); err != nil {
					t.Fatal(err)
				}
			} else if err := b.mutateBotHomeFence(ctx, h, func(botHomeFenceState, map[string]botHomeReservation) error { return nil }); err != nil {
				t.Fatal(err)
			}
			before := len(homeReservations(t, b, h))
			other := sharedHomeBackend(t, b)
			other.cfg.MaxChainDepth = 1
			invoke := func() error {
				if mode == "start" {
					return other.StartSession(ctx, spec.SessionID)
				}
				_, err := other.CreateSession(ctx, spec)
				return err
			}
			client := b.client.(*fake.Clientset)
			client.ClearActions()
			if err := invoke(); err == nil || !strings.Contains(err.Error(), "layer chain depth") {
				t.Fatalf("expected local rejection, got %v", err)
			}
			for _, a := range client.Actions() {
				if a.Matches("create", "pods") {
					t.Fatal("local rejection submitted a pod")
				}
			}
			rs := homeReservations(t, b, h)
			if len(rs) != before {
				t.Fatalf("local rejection stranded reservations: %+v", rs)
			}
			for _, r := range rs {
				if r.Pending {
					t.Fatal("local rejection stranded pending admission")
				}
			}
			other.cfg.MaxChainDepth = 2
			if err := invoke(); err != nil {
				t.Fatal(err)
			}
			if err := other.DestroySession(ctx, spec.SessionID); err != nil {
				t.Fatal(err)
			}
			purgeReady(t, b, h, false)
			if err := other.DeleteBotHome(ctx, h); err != nil {
				t.Fatalf("local rejection blocked later purge: %v", err)
			}
		})
	}
}

func TestBotHomeAttachmentRecoveryAndForeignFence(t *testing.T) {
	b, h := homeBackend(t)
	ctx := context.Background()
	spec := sandbox.SessionSpec{SessionID: "recover", BotHome: h}
	if _, err := b.CreateSession(ctx, spec); err != nil {
		t.Fatal(err)
	}
	if err := b.client.CoreV1().Pods(b.cfg.Namespace).Delete(ctx, podNameForSession(spec.SessionID), metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	other := sharedHomeBackend(t, b)
	if err := other.StartSession(ctx, spec.SessionID); err != nil {
		t.Fatal(err)
	}
	if len(homeReservations(t, b, h)) != 1 {
		t.Fatal("recovery leaked settled attempts")
	}
	cms := b.client.CoreV1().ConfigMaps(b.cfg.Namespace)
	cm, err := cms.Get(ctx, botHomeFenceName(h), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	foreign := *h
	foreign.Scope.UserID = "foreign"
	raw, _ := json.Marshal(foreign)
	cm.Data["binding"] = string(raw)
	if _, err = cms.Update(ctx, cm, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := other.StartSession(ctx, spec.SessionID); err == nil {
		t.Fatal("adopted foreign fence")
	}
	if _, err := other.ReadBotHomeArtifact(ctx, h, "file", 10); err == nil {
		t.Fatal("read with foreign fence")
	}
}
