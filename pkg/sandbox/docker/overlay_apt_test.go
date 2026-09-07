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

func TestFormatExecOutput_IncludesStdoutWhenStderrIsWarning(t *testing.T) {
	got := formatExecOutput(
		[]byte("dpkg: error processing archive docker.io: subprocess returned error code 2"),
		[]byte("debconf: delaying package configuration, since apt-utils is not installed"),
	)
	if !strings.Contains(got, "apt-utils") {
		t.Errorf("missing stderr: %s", got)
	}
	if !strings.Contains(got, "docker.io") {
		t.Errorf("missing stdout: %s", got)
	}
}
