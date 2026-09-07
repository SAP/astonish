package docker

import (
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
)

func TestDockerRunArgs_OverlayContract(t *testing.T) {
	db := &DockerBackend{cfg: Config{
		LayersDir:    "/tmp/layers",
		UppersDir:    "/tmp/uppers",
		SandboxImage: "ghcr.io/sap/astonish-sandbox-base:latest",
	}}
	spec := sandbox.SessionSpec{
		SessionID:  "sess-1",
		Type:       sandbox.SessionTypeChat,
		LayerChain: []string{"seed"},
	}
	args := db.dockerRunArgs(spec, containerName(spec.SessionID), "seed", "/tmp/uppers/sess-1")
	joined := strings.Join(args, "\n")

	mustContain := []string{
		mountLayers,
		mountUppers,
		mountOverlay,
		envLayerChain + "=seed",
		envUpperDir + "=" + mountUpper,
		envWorkDir + "=" + mountWork,
		envHandoff + "=/bin/sleep",
		"--privileged",
		"--device",
		"/dev/fuse",
	}
	for _, want := range mustContain {
		if !strings.Contains(joined, want) {
			t.Errorf("docker run args missing %q\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "--entrypoint") {
		t.Error("must not skip the sandbox-base entrypoint")
	}
	if strings.Contains(joined, "/mnt/astonish-uppers/sess-1") && strings.Contains(joined, mountOverlay) {
		// host persist dir is bound at mountUppers, not as overlay upperdir
	}
	// Live overlay upperdir must be the named overlay volume, not the host persist bind.
	wantVol := overlayVolumeName(spec.SessionID) + ":" + mountOverlay
	if !strings.Contains(joined, wantVol) {
		t.Errorf("docker run args missing overlay volume %q\n%s", wantVol, joined)
	}
	for i, a := range args {
		if a == "-v" && i+1 < len(args) && strings.HasSuffix(args[i+1], ":"+mountUpper) {
			t.Errorf("must not bind host path onto live overlay upperdir: %s", args[i+1])
		}
	}
}

func TestIsPrunableOverlayVolume(t *testing.T) {
	if !isPrunableOverlayVolume("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa") {
		t.Error("64-hex dangling anonymous volume should be prunable")
	}
	if !isPrunableOverlayVolume("astonish-session-build-base-config-abcdef-overlay") {
		t.Error("named session overlay volume should be prunable")
	}
	if isPrunableOverlayVolume("postgres_data") {
		t.Error("must not prune unrelated named volumes")
	}
	if isPrunableOverlayVolume("not-hex") {
		t.Error("short names must not be treated as anonymous overlay volumes")
	}
}

func TestContainerName_DoesNotTruncateTo12(t *testing.T) {
	a := containerName("aaaaaaaaaaaa-one")
	b := containerName("aaaaaaaaaaaa-two")
	if a == b {
		t.Fatalf("names collided after truncation: %s", a)
	}
	if !strings.HasPrefix(a, "astonish-session-") {
		t.Errorf("name = %q", a)
	}
}

func TestResolveLayerChain_RequiresRootfs(t *testing.T) {
	db := &DockerBackend{cfg: Config{LayersDir: t.TempDir(), UppersDir: t.TempDir()}}
	if _, err := db.resolveLayerChain(nil, sandbox.BaseTemplateID); err == nil {
		t.Fatal("expected error when no layers exist")
	}
}

func TestRecordSession_SetsContainerNameAndPodName(t *testing.T) {
	st, err := sandbox.NewLocalSessionStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewLocalSessionStore: %v", err)
	}
	reg := sandbox.NewSessionRegistryFromStore(st)
	db := &DockerBackend{cfg: Config{Sessions: reg}}
	spec := sandbox.SessionSpec{SessionID: "c1b27dfb-f828-40f2-b510-c8e85df3cda7"}
	cname := containerName(spec.SessionID)
	db.recordSession(spec, cname, sandbox.BaseTemplateID)

	rec, err := reg.GetSession(spec.SessionID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if rec == nil {
		t.Fatal("GetSession returned nil")
	}
	if rec.ContainerName != cname {
		t.Errorf("ContainerName = %q, want %q", rec.ContainerName, cname)
	}
	if rec.PodName != cname {
		t.Errorf("PodName = %q, want %q (browser resolve historically required PodName)", rec.PodName, cname)
	}
	if rec.Backend != string(sandbox.BackendKindDocker) {
		t.Errorf("Backend = %q, want docker", rec.Backend)
	}
	if rec.State != store.SandboxSessionStateRunning {
		t.Errorf("State = %q, want running", rec.State)
	}
}
