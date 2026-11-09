//go:build e2e

package bots_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"errors"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/launcher"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/tests/e2eboot"
	corev1 "k8s.io/api/core/v1"
)

// TestBotsLiveKubernetesRetainedHome is intentionally strict: selecting this
// release test without its dedicated infrastructure fails instead of skipping.
// No fake Kubernetes client or host-file fallback participates in this proof.
func TestBotsLiveKubernetesRetainedHome(t *testing.T) {
	h, open := e2eboot.BootstrapRetainedHomeKubernetes(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Minute)
	defer cancel()
	b, reg := open()
	pool := sandbox.NewBackendPool(b, sandbox.ResourceLimits{}, reg)
	runtime := e2eboot.NewRetainedHomeBotRuntime(t, ctx, h, b, pool)
	var ids []string
	runs := map[string]*store.BotRun{}
	runContexts := map[string]context.Context{}
	destroySession := func(c context.Context, id string) error {
		var last error
		for c.Err() == nil {
			last = b.DestroySession(c, id)
			if last == nil {
				return nil
			}
			// Kubernetes DELETE acknowledgement precedes physical termination.
			// Retry the product's conservative fence release, never bypass it.
			select {
			case <-c.Done():
				return last
			case <-time.After(time.Second):
			}
		}
		return last
	}
	var homes []*sandbox.BotHomeBinding
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 2*time.Minute)
		defer stop()
		// Product destruction first: never delete PVCs, scope roots, fence records,
		// or namespaces to force past a failed attachment fence.
		blockedHomes := map[string]bool{}
		for _, id := range ids {
			if err := destroySession(cleanup, id); err != nil {
				t.Errorf("cleanup %s: %v", id, err)
				if run := runs[id]; run != nil {
					blockedHomes[run.BotID.String()] = true
				}
				continue // retain binding and root lease barrier on uncertain physical teardown
			}
			pool.Remove(id)
			if run := runs[id]; run != nil {
				commitCtx, cancel := context.WithTimeout(context.WithoutCancel(runContexts[id]), 15*time.Second)
				err := runtime.Bots.CommitRun(commitCtx, store.BotRunCommit{Lease: run.Lease, Status: store.RunCancelled, Report: store.BotReport{Summary: "Home-only test cleanup after physical destruction"}})
				cancel()
				if err != nil {
					blockedHomes[run.BotID.String()] = true
					t.Errorf("cleanup root lease retained for %s: %v", id, err)
				}
			}
		}
		for _, home := range homes {
			if blockedHomes[home.Scope.BotID] {
				continue
			}
			if err := b.(sandbox.BotHomeDeleteBackend).DeleteBotHome(cleanup, home); err != nil {
				t.Errorf("safe cleanup home %s (retain fence for operator repair): %v", home.Scope.Key(), err)
			}
		}
	})
	kubectl := func(args ...string) string {
		t.Helper()
		out, err := exec.CommandContext(ctx, "kubectl", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("kubectl %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	check := func(id, script string) {
		t.Helper()
		out, err := b.Exec(ctx, id, sandbox.ExecSpec{Command: []string{"/bin/sh", "-c", script}})
		if err != nil || out == nil || out.ExitCode != 0 {
			t.Fatalf("exec %s: %+v %v", id, out, err)
		}
	}
	var uids []string
	create := func(owner *store.Bot, home *sandbox.BotHomeBinding) {
		t.Helper()
		runCtx, run := runtime.Claim(t, owner.ID)
		id := run.SessionID.String()
		ids = append(ids, id)
		runs[id], runContexts[id] = run, runCtx
		registered, err := runtime.Manager.Ensure(runCtx, bot.HomeRequest{BotID: owner.ID, Scope: owner.Scope})
		if err != nil {
			t.Fatal(err)
		}
		binding := bot.HomeRunBinding{Home: registered, PhysicalSessionID: run.SessionID, Lease: run.Lease}
		stale := binding
		stale.Lease.Fence--
		if err := runtime.Manager.BindRun(runCtx, stale); !errors.Is(err, store.ErrBotConflict) {
			t.Fatalf("stale lease accepted: %v", err)
		}
		if err := runtime.Manager.BindRun(context.Background(), binding); !errors.Is(err, store.ErrBotForbidden) {
			t.Fatalf("unauthenticated bind accepted: %v", err)
		}
		if err := runtime.Manager.BindRun(runCtx, binding); err != nil {
			t.Fatal(err)
		}
		if err := sandbox.EnsureToolClientReady(ctx, pool.GetOrCreate(id), id); err != nil {
			t.Fatal(err)
		}
		if reg.Get(id) == nil {
			t.Fatal("missing production registry entry")
		}
		raw := kubectl("get", "pod", "-n", e2eboot.SandboxNamespace(), e2eboot.DerivePodName(id), "-o", "json")
		var pod corev1.Pod
		if err := json.Unmarshal([]byte(raw), &pod); err != nil {
			t.Fatal(err)
		}
		uid := string(pod.UID)
		if uid == "" {
			t.Fatal("missing physical pod UID")
		}
		for _, prior := range uids {
			if uid == prior {
				t.Fatal("reused physical pod")
			}
		}
		uids = append(uids, uid)
		mounted := false
		for _, c := range pod.Spec.Containers {
			for _, m := range c.VolumeMounts {
				if m.MountPath == "/mnt/astonish-bot-home" {
					mounted = m.SubPath == home.Scope.Key()+"/data" && m.SubPathExpr == "" && !m.ReadOnly
				}
			}
		}
		if !mounted || pod.Spec.AutomountServiceAccountToken == nil || *pod.Spec.AutomountServiceAccountToken {
			t.Fatal("retained workload isolation manifest mismatch")
		}
		check(id, `test "$HOME" = /root && test "$PWD" = /root && mountpoint -q /root`)
		t.Logf("physical session=%s pod=%s UID=%s scoped home=%s", id, pod.Name, uid, home.Scope.Key())
	}
	destroy := func(id string) {
		t.Helper()
		if err := destroySession(ctx, id); err != nil {
			t.Fatal(err)
		}
		if got := kubectl("get", "pod", "-n", e2eboot.SandboxNamespace(), e2eboot.DerivePodName(id), "--ignore-not-found", "-o", "name"); got != "" {
			t.Fatalf("physical pod remains: %s", got)
		}
		if reg.Get(id) != nil {
			t.Fatal("destroyed registry entry remains")
		}
		pool.Remove(id)
		if err := runtime.Bots.CommitRun(runContexts[id], store.BotRunCommit{Lease: runs[id].Lease, Status: store.RunSucceeded, Report: store.BotReport{Summary: "Physical retained-home invocation verified and destroyed"}}); err != nil {
			t.Fatal(err)
		}
		delete(runs, id)
	}
	owner, home := runtime.Create(t, "Retained home owner")
	scope := home.Scope
	homes = append(homes, home)
	otherOwner, other := runtime.Create(t, "Isolated other Bot")
	homes = append(homes, other)
	if home.Ref == other.Ref {
		t.Fatal("different Bots share retained storage")
	}
	create(owner, home)
	check(ids[0], `printf durable-marker > marker && printf scratch > /tmp/run-only && ln -s /etc/passwd escape && ln -s marker local-link && mkdir folder`)
	destroy(ids[0])
	// Reopen registry and factory, discarding the old pool and in-memory binding.
	b, reg = open()
	pool = sandbox.NewBackendPool(b, sandbox.ResourceLimits{}, reg)
	runtime.Manager = &launcher.BotHomeManager{Backend: b.(sandbox.BotHomeBackend), Pool: pool, Bots: runtime.Bots}
	registered, err := runtime.Manager.Ensure(runtime.Context, bot.HomeRequest{BotID: owner.ID, Scope: owner.Scope})
	if err != nil || registered != owner.Home {
		t.Fatalf("reopened registered home: %+v %v", registered, err)
	}
	reopened := &sandbox.BotHomeBinding{Scope: scope, Backend: sandbox.BackendKind(registered.Backend), Ref: registered.StorageKey}
	if !home.Equal(reopened) {
		t.Fatal("home changed after backend/registry reopen")
	}
	create(owner, reopened)
	check(ids[1], `test "$(cat marker)" = durable-marker && test ! -e /tmp/run-only`)
	create(otherOwner, other)
	check(ids[2], `test ! -e marker && test ! -e /tmp/run-only && printf other-bot > marker`)
	deleter := b.(sandbox.BotHomeDeleteBackend)
	if err := deleter.DeleteBotHome(ctx, home); err == nil {
		t.Fatal("attached home purge accepted")
	}
	check(ids[1], `test "$(cat marker)" = durable-marker`)
	destroy(ids[1])
	reader := b.(sandbox.BotHomeArtifactBackend)
	for _, path := range []string{"marker", "local-link"} {
		data, err := reader.ReadBotHomeArtifact(ctx, home, path, 14)
		if err != nil || string(data) != "durable-marker" {
			t.Fatalf("offline %s: %q %v", path, data, err)
		}
	}
	for _, path := range []string{"../etc/passwd", "/etc/passwd", "escape", "folder", "missing"} {
		data, err := reader.ReadBotHomeArtifact(ctx, home, path, 1024)
		if err == nil || len(data) != 0 {
			t.Fatalf("unsafe offline read %s: %q %v", path, data, err)
		}
	}
	if _, err := reader.ReadBotHomeArtifact(ctx, home, "marker", 3); err == nil {
		t.Fatal("oversized artifact accepted")
	}
	foreign := home.Clone()
	foreign.Ref = other.Ref
	if err := deleter.DeleteBotHome(ctx, foreign); err == nil {
		t.Fatal("foreign binding purge accepted")
	}
	if err := deleter.DeleteBotHome(ctx, home); err != nil {
		t.Fatal(err)
	}
	if err := deleter.DeleteBotHome(ctx, home); err != nil {
		t.Fatalf("idempotent purge: %v", err)
	}
	if _, err := reader.ReadBotHomeArtifact(ctx, home, "marker", 1024); err == nil {
		t.Fatal("purged artifact readable")
	}
	if _, err := b.(sandbox.BotHomeBackend).EnsureBotHome(ctx, scope); err == nil {
		t.Fatal("purged home reopened")
	}
	check(ids[2], `test "$(cat marker)" = other-bot`)
	// Shared retained claim survives run teardown and authorized scoped purge.
	kubectl("get", "pvc", "-n", e2eboot.SandboxNamespace(), os.Getenv("ASTONISH_E2E_HOMES_PVC"))
	t.Log("real Kubernetes: distinct pod UIDs; backend/registry reopened; HOME persisted without scratch; offline artifacts bounded; attached/foreign purge denied; scoped purge leaves other Bot and PVC intact")
}
