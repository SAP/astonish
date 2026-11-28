package openshell

import (
	"context"
	pb "github.com/SAP/astonish/pkg/sandbox/openshell/gen/openshellv1"
	"testing"
)

func TestBotHomeGRPCTemplateEvidenceRoundTrip(t *testing.T) {
	b := newTestBackendWithGateway(t, nil)
	binding := homeTestBinding(t, b)
	env := map[string]string{}
	driver, err := b.composeBotHome(context.Background(), binding, env, nil)
	if err != nil {
		t.Fatal(err)
	}
	labels := map[string]string{}
	botHomeLabels(labels, "run", binding)
	var saved *pb.SandboxSpec
	server := &fakeOpenShellServer{
		createSandboxFn: func(req *pb.CreateSandboxRequest) (*pb.SandboxResponse, error) {
			saved = req.Spec
			if saved.Policy.Process.RunAsUser != "sandbox" {
				t.Fatal("missing sandbox identity")
			}
			return &pb.SandboxResponse{Sandbox: &pb.Sandbox{Metadata: &pb.ObjectMeta{Name: req.Name, Id: "exec-uuid"}, Spec: saved}}, nil
		},
		getSandboxFn: func(req *pb.GetSandboxRequest) (*pb.SandboxResponse, error) {
			if req.Name != "sandbox-name" {
				t.Fatal("Get must use sandbox name")
			}
			return &pb.SandboxResponse{Sandbox: &pb.Sandbox{Metadata: &pb.ObjectMeta{Name: req.Name, Id: "exec-uuid"}, Spec: saved, Status: &pb.SandboxStatus{Phase: pb.SandboxPhase_SANDBOX_PHASE_READY}}}, nil
		},
	}
	client, cleanup := startFakeServer(t, server)
	defer cleanup()
	_, err = client.CreateSandbox(context.Background(), CreateSandboxRequest{Name: "sandbox-name", Env: env, Labels: labels, DriverConfig: driver, Policy: botHomePolicy(defaultSandboxPolicy(b.cfg.AppConfig), binding)})
	if err != nil {
		t.Fatal(err)
	}
	st, err := client.GetSandboxStatus(context.Background(), "sandbox-name")
	if err != nil {
		t.Fatal(err)
	}
	if err := validateBotHomeAdoption(st, driver, labels); err != nil {
		t.Fatal(err)
	}
}
