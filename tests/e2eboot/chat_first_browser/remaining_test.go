package main

import (
	"os"
	"strings"
	"testing"
)

func TestRemainingLifecycleLabels(t *testing.T) {
	for _, c := range []struct{ label, confirm, status string }{{"Pause", "Confirm pause", "paused"}, {"Review activation", "Confirm activate", "active"}} {
		confirm, status, reason := remainingLifecycle(c.label)
		if confirm != c.confirm || status != c.status || reason == "" {
			t.Fatal(c.label, confirm, status, reason)
		}
	}
	defer func() {
		if recover() == nil {
			t.Fatal("unknown lifecycle accepted")
		}
	}()
	remainingLifecycle("Delete Bot")
}

func TestRemainingHarnessScopedAndNoPrerequisiteReplay(t *testing.T) {
	b, err := os.ReadFile("remaining.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	for _, want := range []string{`[aria-label="Bot workspace"] [data-testid="chat-input"]`, `remaining message already attempted`, `remaining confirmation already attempted`, `65 * time.Second`, `defer capture()`} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing guard %q", want)
		}
	}
	for _, bad := range []string{`retainedArtifactReview(`, `reviewedPermissionPrerequisite(`, `api("POST", botPath`} {
		if strings.Contains(text, bad) {
			t.Fatalf("unsafe replay/direct mutation: %s", bad)
		}
	}
}
