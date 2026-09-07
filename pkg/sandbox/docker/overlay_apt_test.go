package docker

import (
	"fmt"
	"strings"
	"testing"
)

func TestOverlayAptPrepScript_DisablesAptSandboxAndServiceStarts(t *testing.T) {
	script := overlayAptPrepScript()
	for _, want := range []string{
		`APT::Sandbox::User "root"`,
		"force-unsafe-io",
		"policy-rc.d",
		"exit 101",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("overlayAptPrepScript missing %q\n%s", want, script)
		}
	}
}

func TestFormatExecOutput_PrefersAptErrorLinesOverPackageList(t *testing.T) {
	stdout := []byte("The following NEW packages will be installed:\n  git curl docker.io libavcodec ... Need to get 324 MB of archives.")
	stderr := []byte("debconf: delaying package configuration, since apt-utils is not installed\nE: You don't have enough free space in /var/cache/apt/archives/.")
	got := formatExecOutput(stdout, stderr)
	if !strings.Contains(got, "enough free space") {
		t.Fatalf("missing apt error: %s", got)
	}
	if strings.Contains(got, "libavcodec") {
		t.Fatalf("should not dump the package list when an E: line exists: %s", got)
	}
}

func TestBuildCaptureScript_DoesNotStageTmpTar(t *testing.T) {
	s := buildCaptureScript("bid-1")
	if strings.Contains(s, "/tmp/astn-layer.tar") {
		t.Fatal("capture must not write /tmp/astn-layer.tar")
	}
	if !strings.Contains(s, "mkfifo") {
		t.Fatal("expected posix fifo streaming capture")
	}
}

func TestCaptureLayerError_NoSpace(t *testing.T) {
	err := captureLayerError(fmt.Errorf("exit status 2\nstderr: tar: /tmp/astn-layer.tar: Cannot write: No space left on device"))
	if err == nil || !strings.Contains(err.Error(), "docker volume prune") {
		t.Fatalf("expected prune hint, got %v", err)
	}
}

func TestTruncateStep(t *testing.T) {
	if got := truncateStep("apt-get update"); got != "apt-get update" {
		t.Fatalf("short step: %q", got)
	}
	long := strings.Repeat("a", 100)
	got := truncateStep(long)
	if len(got) != 80 || !strings.HasSuffix(got, "...") {
		t.Fatalf("long step: %q", got)
	}
}

func TestOverlayAptPrepScript_ChecksFreeSpace(t *testing.T) {
	script := overlayAptPrepScript()
	if !strings.Contains(script, "df -Pm") {
		t.Fatal("expected a df free-space check")
	}
	if !strings.Contains(script, "docker system prune") {
		t.Fatal("expected a prune hint when the overlay is too small")
	}
}
