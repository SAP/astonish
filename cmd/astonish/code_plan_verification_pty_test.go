package astonish

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Exercise the actual Code subprocess and its synchronous update_plan verifier.
// The first shell invocation sleeps, records its retry prerequisite, and fails;
// the second invocation succeeds without the test changing workspace artifacts.
// Short-deadline process cleanup is tested separately in pkg/agent.
func TestCodePlanVerificationPTYAcceptance(t *testing.T) {
	f := newSidebarPTYFixture(t)
	term := f.start()
	term.wait("Enter send")
	term.send("\x1b[Z")
	term.wait("Plan")
	term.send("Announce the verification visibility fixture.\r")
	f.turn("gplan_gaps", map[string]any{"gaps": []any{map[string]any{"question": "No index in isolated fixture", "why_codegraph_insufficient": "npx deliberately unavailable in temporary HOME"}}})
	f.turn("gplan_finalize", map[string]any{"acceptance": "Observe an advancing verification timer, failure, retry, and recorded completion"})
	var steps []map[string]any
	for _, name := range []string{"one", "two", "three"} {
		marker := filepath.Join(f.work, name+".marker")
		verify := fmt.Sprintf("printf verified-%s > '%s' && test -s '%s'", name, marker, marker)
		if name == "one" {
			verify = fmt.Sprintf("sleep 3; if test -f '%s'; then printf verified-one > '%s'; printf verification-recovered; else printf retry > '%s'; printf verification-first-failure; exit 1; fi", marker, marker, marker)
		}
		steps = append(steps, map[string]any{"name": name, "description": "Phase " + name, "details": "Run the isolated marker verifier for " + name, "summary": "Verify the " + name + " capability", "outcome": "The " + name + " marker is verified", "verify": verify, "verify_kind": "behavior", "files": []any{map[string]any{"path": marker, "kind": "new"}}})
	}
	f.turn("announce_plan", map[string]any{"goal": "Verification visibility fixture", "context": "## Problem\nProve real CLI verification visibility.\n## Root causes\nSynchronous verification needs terminal evidence.\n## Approach\nRun isolated shell marker commands.\n## Boundaries\nTemporary HOME only.", "what_not_to_do": "Never touch user sessions or production files", "verification": "Observe an advancing verification timer, failure, retry, and recorded completion", "steps": steps})
	term.wait("Approve and run")
	waitPlanPTYRegion(t, term, 92, 120, "Awaiting approval", "0/3")
	term.send("\r")
	f.update("one", "running")
	waitPlanPTYRegion(t, term, 92, 120, "● Phase one")
	f.update("one", "complete")
	waitPlanPTYRegion(t, term, 0, 91, "Verifying phase: one · 0s")
	waitPlanPTYRegion(t, term, 92, 120, "Verifying phase: one")
	waitPlanPTYRegion(t, term, 0, 91, "Verifying phase: one · 1s")
	waitPlanPTYRegion(t, term, 0, 91, "Verifying phase: one · 2s")
	waitPlanPTYRegion(t, term, 92, 120, "Phase failed", "0/3", "1 failed")
	if s := f.durable(); s.Steps[0].Status != "failed" || !strings.Contains(s.Steps[0].Evidence, "verification-first-failure") {
		t.Fatalf("real failed verification not recorded: %+v", s.Steps[0])
	}

	// Receiving another model request proves the parent agent resumed after the
	// failed synchronous verifier, rather than remaining wedged in the tool call.
	f.update("one", "running")
	waitPlanPTYRegion(t, term, 92, 120, "● Phase one")
	f.update("one", "complete")
	waitPlanPTYRegion(t, term, 0, 91, "Verifying phase: one · 0s")
	waitPlanPTYRegion(t, term, 92, 120, "Verifying phase: one")
	waitPlanPTYRegion(t, term, 0, 91, "Verifying phase: one · 1s")
	waitPlanPTYRegion(t, term, 92, 120, "1/3", "33%")
	if s := f.durable(); s.Steps[0].Status != "complete" || !strings.Contains(s.Steps[0].Evidence, "verification-recovered") {
		t.Fatalf("real retry verification not recorded: %+v", s.Steps[0])
	}
	for _, name := range []string{"two", "three"} {
		f.update(name, "running")
		f.update(name, "complete")
	}
	waitPlanPTYRegion(t, term, 92, 120, "3/3", "100%", "Awaiting completion record")
	f.turn("announce_completion", map[string]any{"outcome_observed": "Verification failed visibly, retry recovered, and all three marker commands passed", "unverified": "The production ten-minute timeout was not exercised in this PTY"})
	waitPlanPTYRegion(t, term, 92, 120, "COMPLETED", "Results recorded")
	f.turn("", nil)
	term.wait("Enter send")
	term.assertAbsent("Verifying phase:")
	term.close()
	if s := f.durable(); s.Document.Results == "" {
		t.Fatal("completion Results were not persisted")
	}
	if marker, err := os.ReadFile(filepath.Join(f.work, "one.marker")); err != nil || string(marker) != "verified-one" {
		t.Fatalf("shell retry marker = %q, error = %v", marker, err)
	}
}
