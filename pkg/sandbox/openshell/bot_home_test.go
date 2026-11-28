package openshell

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"reflect"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/sandbox"
	pb "github.com/SAP/astonish/pkg/sandbox/openshell/gen/openshellv1"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func homeTestBinding(t *testing.T, b *OpenShellBackend) *sandbox.BotHomeBinding {
	t.Helper()
	b.cfg.BotHomeMountsEnabled = true
	b.cfg.BotHomeClaimName = "retained-homes"
	binding, err := b.EnsureBotHome(context.Background(), sandbox.BotHomeScope{OrgID: "org", TeamID: "team", UserID: "user", BotID: "bot"})
	if err != nil {
		t.Fatal(err)
	}
	return binding
}

func TestBotHomeConfigAndScope(t *testing.T) {
	b := newTestBackendWithGateway(t, &mockGateway{})
	scope := sandbox.BotHomeScope{UserID: "user", BotID: "bot"}
	for _, cfg := range []Config{{}, {BotHomeMountsEnabled: true}, {BotHomeClaimName: "retained-homes"}} {
		b.cfg = cfg
		if _, err := b.EnsureBotHome(context.Background(), scope); !errors.Is(err, sandbox.ErrBotHomeUnsupported) {
			t.Fatalf("expected unsupported, got %v", err)
		}
	}
	binding := homeTestBinding(t, b)
	again, err := b.EnsureBotHome(context.Background(), binding.Scope)
	if err != nil || !binding.Equal(again) {
		t.Fatalf("not idempotent: %v", err)
	}
	for _, claim := range []string{"../root", "/root", "UPPER", "a..b", "a/b", strings.Repeat("a", 254)} {
		b.cfg.BotHomeClaimName = claim
		if _, err := b.EnsureBotHome(context.Background(), scope); err == nil {
			t.Fatalf("accepted claim %q", claim)
		}
	}
	b.cfg.BotHomeClaimName = "retained-homes"
	scope.BotID = "../../sibling"
	safe, err := b.EnsureBotHome(context.Background(), scope)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(safe.Ref, "..") || path.Clean(safe.Ref) != safe.Ref || safe.Equal(binding) {
		t.Fatalf("unsafe scope: %+v", safe)
	}
	for _, ref := range []string{"retained-homes", "retained-homes/", "retained-homes/bot-homes/../other", "/host/path"} {
		forged := binding.Clone()
		forged.Ref = ref
		if err := b.validateBotHome(context.Background(), forged); err == nil {
			t.Fatalf("accepted forged ref %q", ref)
		}
	}
	forged := binding.Clone()
	forged.Backend = sandbox.BackendKindDocker
	if err := b.validateBotHome(context.Background(), forged); err == nil {
		t.Fatal("accepted foreign backend")
	}
}

func TestBotHomeComposeCertsAndNil(t *testing.T) {
	b := newTestBackendWithGateway(t, &mockGateway{})
	binding := homeTestBinding(t, b)
	b.cfg.AppConfig.CertBundles = []config.CertBundleConfig{{Name: "corp", ClaimName: "corp-ca", MountPath: "/etc/corp/ca.crt", SubPath: "ca.crt"}}
	env := map[string]string{"HOME": "/sandbox"}
	driver, err := applyCertBundles(b.cfg.AppConfig, env)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(driver)
	unchanged, err := b.composeBotHome(context.Background(), nil, env, driver)
	after, _ := json.Marshal(unchanged)
	if err != nil || string(before) != string(after) || env["HOME"] != "/sandbox" {
		t.Fatal("nil binding changed ordinary session")
	}
	driver, err = b.composeBotHome(context.Background(), binding, env, driver)
	if err != nil {
		t.Fatal(err)
	}
	kube := driver["kubernetes"].(map[string]any)
	vols := kube["volumes"].([]any)
	mounts := kube["containers"].(map[string]any)["agent"].(map[string]any)["volume_mounts"].([]any)
	if len(vols) != 2 || len(mounts) != 3 {
		t.Fatalf("lost cert composition: %#v", driver)
	}
	home := mounts[2].(map[string]any)
	if home["mount_path"] != botHomeMountPath || home["sub_path"] != "bot-homes/"+binding.Scope.Key() || home["read_only"] != false {
		t.Fatalf("unsafe mount: %v", home)
	}
	if vols[1].(map[string]any)["persistent_volume_claim"].(map[string]any)["claim_name"] != "retained-homes" {
		t.Fatal("wrong claim")
	}
	for _, m := range mounts[:2] {
		if m.(map[string]any)["read_only"] != true {
			t.Fatal("cert is writable")
		}
	}
	if env["HOME"] != botHomeMountPath || env["SSL_CERT_FILE"] != "/etc/corp/ca.crt" {
		t.Fatalf("env: %v", env)
	}
	plain := defaultSandboxPolicy(b.cfg.AppConfig)
	if !reflect.DeepEqual(plain, botHomePolicy(defaultSandboxPolicy(b.cfg.AppConfig), nil)) {
		t.Fatal("nil policy changed")
	}
	policy := botHomePolicy(defaultSandboxPolicy(b.cfg.AppConfig), binding)
	if policy.Process.RunAsUser != "sandbox" || policy.Process.RunAsGroup != "sandbox" || policy.Filesystem.ReadWrite[len(policy.Filesystem.ReadWrite)-1] != botHomeMountPath {
		t.Fatalf("home policy: %+v", policy)
	}
}

func TestBotHomeRejectCertCollisions(t *testing.T) {
	for _, cert := range []config.CertBundleConfig{
		{Name: botHomeVolumeName, ClaimName: "ca", MountPath: "/etc/ca.crt"},
		{Name: "ca", ClaimName: "retained-homes", MountPath: "/etc/ca.crt"},
		{Name: "ca", ClaimName: "ca", MountPath: "/home"},
		{Name: "ca", ClaimName: "ca", MountPath: botHomeMountPath},
		{Name: "ca", ClaimName: "ca", MountPath: botHomeMountPath + "/ca.crt"},
		{Name: "ca", ClaimName: "ca", MountPath: "/etc/../home/astonish/ca.crt"},
		{Name: "ca", Source: config.CertBundleSourceConfigMap, ConfigMapName: "ca", MountPath: botHomeMountPath + "/ca.crt"},
	} {
		t.Run(cert.Name+cert.MountPath+cert.ClaimName, func(t *testing.T) {
			b := newTestBackendWithGateway(t, &mockGateway{})
			binding := homeTestBinding(t, b)
			b.cfg.AppConfig.CertBundles = []config.CertBundleConfig{cert}
			if _, err := b.composeBotHome(context.Background(), binding, map[string]string{}, nil); err == nil {
				t.Fatal("accepted collision")
			}
		})
	}
}

func TestBotHomeGRPCRoundTrip(t *testing.T) {
	var saved *pb.SandboxSpec
	srv := &fakeOpenShellServer{
		createSandboxFn: func(req *pb.CreateSandboxRequest) (*pb.SandboxResponse, error) {
			saved = req.GetSpec()
			return &pb.SandboxResponse{Sandbox: &pb.Sandbox{Metadata: &pb.ObjectMeta{Name: req.GetName(), Id: "exec-uuid"}, Spec: saved}}, nil
		},
		getSandboxFn: func(req *pb.GetSandboxRequest) (*pb.SandboxResponse, error) {
			return &pb.SandboxResponse{Sandbox: &pb.Sandbox{Metadata: &pb.ObjectMeta{Name: req.GetName(), Id: "exec-uuid"}, Spec: saved, Status: &pb.SandboxStatus{Phase: pb.SandboxPhase_SANDBOX_PHASE_READY}}}, nil
		},
	}
	gw, cleanup := startFakeServer(t, srv)
	defer cleanup()
	b := newTestBackendWithGateway(t, gw)
	binding := homeTestBinding(t, b)
	env := map[string]string{}
	driver, err := b.composeBotHome(context.Background(), binding, env, nil)
	if err != nil {
		t.Fatal(err)
	}
	labels := map[string]string{}
	botHomeLabels(labels, "roundtrip", binding)
	resp, err := gw.CreateSandbox(context.Background(), CreateSandboxRequest{Name: "roundtrip", DriverConfig: driver, Env: env, Labels: labels, Policy: botHomePolicy(defaultSandboxPolicy(b.cfg.AppConfig), binding)})
	if err != nil {
		t.Fatal(err)
	}
	st, err := gw.GetSandboxStatus(context.Background(), resp.SandboxID)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateBotHomeAdoption(st, driver, labels); err != nil {
		t.Fatal(err)
	}
	if resp.SandboxID != "roundtrip" || resp.GatewayID != "exec-uuid" {
		t.Fatalf("name/UUID conflated: %+v", resp)
	}
	if saved.GetPolicy().GetProcess().GetRunAsUser() != "sandbox" {
		t.Fatal("process policy not serialized")
	}
}

func TestBotHomeAdoptionRecovery(t *testing.T) {
	ctx := context.Background()
	var captured CreateSandboxRequest
	uuid := "old-exec-uuid"
	gw := &mockGateway{createFn: func(_ context.Context, req CreateSandboxRequest) (*CreateSandboxResponse, error) {
		captured = req
		return nil, status.Error(codes.AlreadyExists, "exists")
	}}
	b := newTestBackendWithGateway(t, gw)
	binding := homeTestBinding(t, b)
	id := fmt.Sprintf("home-recovery-%s", t.Name())
	gw.statusFn = func(_ context.Context, name string) (*SandboxStatus, error) {
		if name != sandboxName(id) {
			t.Fatalf("status used UUID not name: %s", name)
		}
		return &SandboxStatus{State: SandboxStateRunning, GatewayID: uuid, DriverConfig: captured.DriverConfig, Env: captured.Env, Labels: captured.Labels}, nil
	}
	defer b.sessions.Remove(id)
	if _, err := b.CreateSession(ctx, sandbox.SessionSpec{SessionID: id, BotHome: binding, Image: "custom-image"}); err != nil {
		t.Fatal(err)
	}
	rec, err := b.sessions.GetSession(id)
	if err != nil {
		t.Fatal(err)
	}
	roundtrip, err := botHomeFromStore(rec)
	if err != nil || !roundtrip.Equal(binding) || rec.ContainerName != sandboxName(id) || rec.PodName != uuid {
		t.Fatalf("lost metadata: %+v, %v", rec, err)
	}
	// Reconstruct a backend with only persisted registry/config, then refresh exec ID.
	restored, err := New(b.cfg)
	if err != nil {
		t.Fatal(err)
	}
	uuid = "new-exec-uuid"
	if err := restored.StartSession(ctx, id); err != nil {
		t.Fatal(err)
	}
	rec, _ = b.sessions.GetSession(id)
	if rec.PodName != uuid {
		t.Fatal("stale exec UUID after recovery")
	}
	gw.deleteFn = func(_ context.Context, name string) error {
		if name != sandboxName(id) {
			t.Fatalf("delete used UUID: %s", name)
		}
		return nil
	}
	if err := restored.StopSession(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := restored.StartSession(ctx, id); err != nil {
		t.Fatal(err)
	}
	if captured.Image != "custom-image" || captured.Env["HOME"] != botHomeMountPath {
		t.Fatal("recreate lost image/home")
	}
	other := binding.Clone()
	other.Scope.BotID = "other"
	other, err = b.EnsureBotHome(ctx, other.Scope)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.CreateSession(ctx, sandbox.SessionSpec{SessionID: id, BotHome: other}); err == nil {
		t.Fatal("rebound immutable home")
	}
	if err := restored.DestroySession(ctx, id); err != nil {
		t.Fatal(err)
	}
}

func TestBotHomeRejectUnprovenAdoption(t *testing.T) {
	b := newTestBackendWithGateway(t, &mockGateway{})
	binding := homeTestBinding(t, b)
	env := map[string]string{}
	driver, err := b.composeBotHome(context.Background(), binding, env, nil)
	if err != nil {
		t.Fatal(err)
	}
	labels := map[string]string{}
	botHomeLabels(labels, "session", binding)
	for _, mutation := range []string{"driver", "home", "scope", "session", "uuid", "gone", "failed"} {
		t.Run(mutation, func(t *testing.T) {
			st := &SandboxStatus{State: SandboxStateRunning, GatewayID: "uuid", DriverConfig: driver, Env: map[string]string{"HOME": botHomeMountPath}, Labels: map[string]string{botHomeScopeLabel: labels[botHomeScopeLabel], botHomeSessionLabel: labels[botHomeSessionLabel]}}
			switch mutation {
			case "driver":
				st.DriverConfig = nil
			case "home":
				st.Env = nil
			case "scope":
				st.Labels[botHomeScopeLabel] = "wrong"
			case "session":
				st.Labels[botHomeSessionLabel] = "wrong"
			case "uuid":
				st.GatewayID = ""
			case "gone":
				st.State = SandboxStateGone
			case "failed":
				st.State = SandboxStateFailed
			}
			if err := validateBotHomeAdoption(st, driver, labels); err == nil {
				t.Fatal("accepted unproven home")
			}
		})
	}
	if err := validateBotHomeAdoption(nil, driver, labels); err == nil {
		t.Fatal("accepted nil status")
	}
	rec := &store.SandboxSession{BotHomeBinding: botHomeToStore(binding)}
	rec.BotHomeBinding.Backend = "docker"
	if _, err := botHomeFromStore(rec); err == nil {
		t.Fatal("accepted mismatched stored backend")
	}
	rec.BotHomeBinding.StorageKey = "invalid-json"
	if _, err := botHomeFromStore(rec); err == nil {
		t.Fatal("accepted corrupt binding")
	}
}
