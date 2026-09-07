package docker

import (
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/sandbox"
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
	// Live overlay upperdir must be the anonymous volume, not the host persist bind.
	for i, a := range args {
		if a == "-v" && i+1 < len(args) && strings.HasSuffix(args[i+1], ":"+mountUpper) {
			t.Errorf("must not bind host path onto live overlay upperdir: %s", args[i+1])
		}
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
