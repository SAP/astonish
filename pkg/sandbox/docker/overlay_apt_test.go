package docker

import (
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

func TestOverlayAptPrepScript_ChecksFreeSpace(t *testing.T) {
	script := overlayAptPrepScript()
	if !strings.Contains(script, "df -Pm") {
		t.Fatal("expected a df free-space check")
	}
	if !strings.Contains(script, "docker system prune") {
		t.Fatal("expected a prune hint when the overlay is too small")
	}
}
