package agent

import (
	"strings"
	"testing"
)

func TestLiveEvidenceSection_SharedAndCodeExtra(t *testing.T) {
	studio := LiveEvidenceSection(false)
	code := LiveEvidenceSection(true)
	for _, want := range []string{
		"## Live Evidence",
		"this turn's tool output",
		"CloakBrowser",
		"which chromium",
		"browser_navigate",
		"Lead with what is true",
		"process_read",
	} {
		if !strings.Contains(studio, want) {
			t.Errorf("Studio Live Evidence missing %q", want)
		}
		if !strings.Contains(code, want) {
			t.Errorf("Code Live Evidence missing %q", want)
		}
	}
	if strings.Contains(studio, "Stop-exploring") {
		t.Error("Studio Live Evidence must not include the code-mode stop-exploring exception")
	}
	if !strings.Contains(code, "Stop-exploring") {
		t.Error("Code Live Evidence must include the stop-exploring exception")
	}
}
