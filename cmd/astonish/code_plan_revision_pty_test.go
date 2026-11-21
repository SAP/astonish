package astonish

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/agent"
)

// Exercise the real Code process, model tool dispatch, durable FileStore, and
// current terminal cells. The loopback model is the only scripted component;
// no reducer events or revised snapshots are injected by this test.
func TestCodePlanRevisionPTYAcceptance(t *testing.T) {
	for _, mode := range []agent.CodePlanExecutionMode{agent.CodePlanRunToCompletion, agent.CodePlanReviewEachPhase} {
		t.Run(string(mode), func(t *testing.T) {
			f := newSidebarPTYFixture(t)
			term := f.start()
			f.announce(term, false)
			initial := f.durable()

			// Start with an approved plan so the revision must clear the old
			// execution lifecycle, rather than merely retain an unapproved one.
			term.send("1")
			codeRevisionPTYReply(t, f, sidebarPTYModelTurn{text: strings.Repeat("Earlier approved-plan discussion before the requested revision.\n", 24) + "INITIAL DISCUSSION FINISHED"})
			term.wait("INITIAL DISCUSSION FINISHED")
			term.send("\x1b") // Stop the autonomous continuation before entering Plan mode.
			term.wait("Enter send")
			term.send("\x1b[Z")
			term.wait("Plan")
			term.send("Revise this plan before doing any implementation.\r")

			for round := 1; round <= 2; round++ {
				before := f.durable()
				definition := before.Definition()
				goal := fmt.Sprintf("Revision %d approval fixture", round)
				for i := range definition.Steps {
					definition.Steps[i].Description = fmt.Sprintf("Revision %d phase %d", round, i+1)
					definition.Steps[i].Summary = fmt.Sprintf("Review revised capability %d", i+1)
				}
				definition.Document.Verification = fmt.Sprintf("Revision %d acceptance: observe phase markers and terminal progress", round)
				f.turn("update_plan", map[string]any{"action": "revise", "revision": agent.PlanRevisionRequest{
					PlanID: before.PlanID, BaseRevision: before.Revision,
					Reason: "Apply the user's requested changes before execution", Goal: &goal, Document: &definition.Document, Steps: definition.Steps,
				}})

				// Both the revised card in the conversation (not merely the
				// sidebar) and the normal Code approval choices must be visible.
				term.wait("1/y  Approve and run autonomously until done", "2    Approve and pause for review after each phase", "Request changes", "Decline", "Enter select")
				waitPlanPTYRegion(t, term, 0, 91, fmt.Sprintf("Revision %d acceptance:", round))
				waitPlanPTYRegion(t, term, 92, 120, "Awaiting approval")
				term.assertAbsent("Enter send")
				codeRevisionPTYAssertStopped(t, f, term)
				after := f.durable()
				if after.PlanID != initial.PlanID || after.Revision != before.Revision+1 || after.Goal != goal {
					t.Fatalf("revision did not preserve identity and advance definition: before=%+v after=%+v", before, after)
				}
				if after.Document.Lifecycle != "" || after.Document.Results != "" {
					t.Fatalf("planning revision retained execution lifecycle: %+v", after.Document)
				}
				for _, step := range after.Steps {
					if step.Status != "pending" || step.Evidence != "" {
						t.Fatalf("phase executed before revised-plan approval: %+v", step)
					}
				}
				if round == 1 {
					term.send("r")
					term.wait("Enter send")
					term.assertAbsent("Enter select")
					// Typing then deleting a character proves the composer is
					// editable, not just a restored hint or a stale screenshot.
					term.send("Please revise once moreX")
					term.wait("Please revise once moreX")
					term.send("\x7f.")
					term.wait("Please revise once more.")
					term.assertAbsent("Please revise once moreX")
					term.send("\r")
				}
			}

			if mode == agent.CodePlanReviewEachPhase {
				term.send("\x1b[B")
				term.wait("▸ 2    Approve and pause for review after each phase")
			} else {
				term.wait("▸ 1/y  Approve and run autonomously until done")
			}
			term.send("\r")
			codeRevisionPTYReply(t, f, sidebarPTYModelTurn{text: "REVISED PLAN APPROVAL RECEIVED"})
			term.wait("REVISED PLAN APPROVAL RECEIVED")
			term.send("\x1b")
			term.wait("Enter send")
			term.assertAbsent("Enter select")
			term.close()
			approved := f.durable()
			if approved.Document.CodeExecutionMode != mode || approved.Document.Lifecycle != agent.PlanLifecycleApproved {
				t.Fatalf("revised plan approval cadence not durable: %+v", approved.Document)
			}
			term = f.start()
			term.wait("REVISED PLAN APPROVAL RECEIVED")
			term.send("\x1b")
			term.wait("Enter send")
			term.assertAbsent("Enter select")
			term.close()
			t.Logf("real 120x30 Code PTY: two planning revisions reopen approval, r edits, no preapproval execution, %s survives resume", mode)
		})
	}
}

func codeRevisionPTYReply(t *testing.T, f *sidebarPTYFixture, turn sidebarPTYModelTurn) {
	t.Helper()
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	select {
	case reply := <-f.turns:
		select {
		case reply <- turn:
		case <-timer.C:
			t.Fatal("Code canceled the scripted revision response")
		}
	case <-timer.C:
		t.Fatal("Code did not request the scripted revision response")
	}
}

func codeRevisionPTYAssertStopped(t *testing.T, f *sidebarPTYFixture, term *handoffPTY) {
	t.Helper()
	// A continued model loop is itself a failure, even if its next tool has not
	// yet run. Hold the controlled provider at the approval boundary to detect it.
	timer := time.NewTimer(300 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-f.turns:
		t.Fatalf("Code continued the model loop before revised-plan approval:\n%s", term.frame())
	case <-term.done:
		t.Fatalf("Code exited at revised-plan approval:\n%s", term.frame())
	case <-timer.C:
	}
	for _, name := range []string{"one", "two", "three"} {
		if _, err := os.Stat(filepath.Join(f.work, name+".marker")); !os.IsNotExist(err) {
			t.Fatalf("verifier marker %s exists before approval (or stat failed): %v", name, err)
		}
	}
}
