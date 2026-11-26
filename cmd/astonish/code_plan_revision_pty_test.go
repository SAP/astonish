package astonish

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/config"
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
				term.wait("1/y  Approve and run", "2    Approve and run with full access", "3    Approve and review each phase", "4    Approve and review with full access", "5/r  Request changes", "6/n  Decline", "Enter select")
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
				term.send("\x1b[B\x1b[B")
				term.wait("▸ 3    Approve and review each phase")
			} else {
				term.wait("▸ 1/y  Approve and run")
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

// Narrow revisions use the same real CLI/provider barriers as the ordinary
// planning-revision regression above. All artifacts belong to the temporary HOME.
func TestCodePlanNarrowRevisionPTYAcceptance(t *testing.T) {
	for _, mode := range []agent.CodePlanExecutionMode{agent.CodePlanRunToCompletion, agent.CodePlanReviewEachPhase} {
		t.Run(string(mode), func(t *testing.T) {
			f := newSidebarPTYFixture(t)
			requests := codeNarrowPTYCaptureRequests(t)
			term := f.start()
			codeNarrowPTYAnnounce(t, f, term)
			if mode == agent.CodePlanReviewEachPhase {
				term.send("\x1b[B\x1b[B")
				term.wait("▸ 3    Approve and review each phase")
			}
			term.send("\r")
			f.update("one", "running")
			f.update("one", "complete")
			waitPlanPTYRegion(t, term, 92, 120, "1/3", "33%")
			if mode == agent.CodePlanReviewEachPhase {
				term.wait("Paused for review", "Enter send")
				term.send("Change only the final verifier, without starting the next phase.\r")
			}
			before := f.durable()
			verify := codeNarrowPTYVerify(f, "three", "revised-final")
			f.turn("update_plan", map[string]any{"action": "revise", "revision": map[string]any{
				"plan_id": before.PlanID, "base_revision": before.Revision,
				"reason":       "Use the focused final marker check; earlier acceptance is unchanged",
				"step_patches": []any{map[string]any{"name": "three", "verify": verify}},
			}})
			codeNarrowPTYStop(t, f, term, "NARROW REVISION APPLIED")
			codeNarrowPTYAssertPreserved(t, f, term, before, mode, verify)
			term.close()
			term = f.start() // Explicit --resume through the existing fixture.
			term.wait("Enter send")
			codeNarrowPTYAssertPreserved(t, f, term, before, mode, verify)
			term.send("continue\r") // Also clears the durable review checkpoint.
			for _, name := range []string{"two", "three"} {
				f.update(name, "running")
				f.update(name, "complete")
				if name == "two" && mode == agent.CodePlanReviewEachPhase {
					term.wait("Paused for review", "Enter send")
					term.send("continue\r")
				}
			}
			waitPlanPTYRegion(t, term, 92, 120, "3/3", "Awaiting completion record")
			if f.durable().Document.Results != "" {
				t.Fatal("Results existed before announce_completion")
			}
			f.turn("announce_completion", map[string]any{"outcome_observed": "All three real marker checks passed without repeating phase one", "unverified": ""})
			waitPlanPTYRegion(t, term, 92, 120, "COMPLETED", "Results recorded")
			codeRevisionPTYReply(t, f, sidebarPTYModelTurn{text: "NARROW EXECUTION FINISHED"})
			term.wait("NARROW EXECUTION FINISHED", "Enter send")
			term.close()
			after := f.durable()
			if !after.IsFullyAccepted() || after.Steps[0].Evidence != before.Steps[0].Evidence {
				t.Fatalf("missing Results or preserved evidence: %+v", after)
			}
			sidecar, err := os.ReadFile(filepath.Join(f.storePath, "astonish_code", f.user, f.sessionID+".PLAN.md"))
			if err != nil || !strings.Contains(string(sidecar), "## Results") || !strings.Contains(string(sidecar), "revised-final") {
				t.Fatalf("durable PLAN.md omitted Results/revised acceptance: %v\n%s", err, sidecar)
			}
			for _, name := range []string{"one", "two", "three"} {
				data, err := os.ReadFile(filepath.Join(f.work, name+".count"))
				if err != nil || string(data) != "run\n" {
					t.Fatalf("%s verifier should run exactly once: %q (%v)", name, data, err)
				}
			}
			data, err := os.ReadFile(filepath.Join(f.work, "three.marker"))
			if err != nil || string(data) != "revised-final" {
				t.Fatalf("final revised check did not run: %q (%v)", data, err)
			}
			select {
			case messages := <-requests:
				for _, guidance := range []string{"Use the smallest relevant tests while editing", "authoritative recorded phase evidence", "Keep a stable acceptance bar"} {
					if !strings.Contains(messages, guidance) {
						t.Fatalf("generated Code request omitted focused validation guidance %q", guidance)
					}
				}
			default:
				t.Fatal("no generated Code request captured")
			}
		})
	}

	t.Run("changed_earlier_contract", func(t *testing.T) {
		f := newSidebarPTYFixture(t)
		term := f.start()
		codeNarrowPTYAnnounce(t, f, term)
		term.send("\r")
		for _, name := range []string{"one", "two"} {
			f.update(name, "running")
			f.update(name, "complete")
		}
		waitPlanPTYRegion(t, term, 92, 120, "2/3", "66%")
		before := f.durable()
		verify := codeNarrowPTYVerify(f, "one", "strengthened-first")
		f.turn("update_plan", map[string]any{"action": "revise", "revision": map[string]any{
			"plan_id": before.PlanID, "base_revision": before.Revision, "reason": "Strengthen the earlier contract",
			"step_patches": []any{map[string]any{"name": "one", "verify": verify}},
		}})
		codeNarrowPTYStop(t, f, term, "EARLIER CONTRACT CHANGED")
		waitPlanPTYRegion(t, term, 92, 120, "0/3", "0%")
		for _, step := range f.durable().Steps {
			if step.Status != "pending" || step.Evidence != "" {
				t.Fatalf("changed earlier contract retained dependent evidence: %+v", step)
			}
		}
		if f.durable().Document.Results != "" {
			t.Fatal("invalidated plan falsely recorded Results")
		}
		term.close()
	})
}

func codeNarrowPTYVerify(f *sidebarPTYFixture, name, marker string) string {
	return fmt.Sprintf("printf 'run\\n' >> '%s' && printf '%s' > '%s' && test -s '%s'", filepath.Join(f.work, name+".count"), marker, filepath.Join(f.work, name+".marker"), filepath.Join(f.work, name+".marker"))
}

func codeNarrowPTYAnnounce(t *testing.T, f *sidebarPTYFixture, term *handoffPTY) {
	t.Helper()
	term.wait("Enter send")
	term.send("\x1b[Z")
	term.wait("Plan")
	term.send("Announce a three phase focused verification plan.\r")
	f.turn("gplan_gaps", map[string]any{"gaps": []any{map[string]any{"question": "No index in isolated fixture", "why_codegraph_insufficient": "npx unavailable in temporary HOME"}}})
	f.turn("gplan_finalize", map[string]any{"acceptance": "Observe phase markers and terminal progress"})
	var steps []map[string]any
	for i, name := range []string{"one", "two", "three"} {
		steps = append(steps, map[string]any{"name": name, "description": "Phase " + name, "summary": "Verify the " + name + " capability", "details": "Run the isolated marker verifier", "outcome": "The " + name + " marker is verified", "verify": codeNarrowPTYVerify(f, name, "verified-"+name), "verify_kind": "behavior", "parallel_group": fmt.Sprintf("wave-%d", i+1), "files": []any{map[string]any{"path": filepath.Join(f.work, name+".marker"), "kind": "new"}, map[string]any{"path": filepath.Join(f.work, name+".count"), "kind": "new"}}})
	}
	f.turn("announce_plan", map[string]any{"goal": "Focused verification fixture", "context": "## Problem\nProve preserved terminal progress.\n## Root causes\nFixture needs real evidence.\n## Approach\nUse isolated marker commands.\n## Boundaries\nTemporary HOME only.", "what_not_to_do": "Never touch user sessions or production files", "verification": "Observe phase markers and terminal progress", "steps": steps})
	term.wait("Approve and run")
}

func codeNarrowPTYStop(t *testing.T, f *sidebarPTYFixture, term *handoffPTY, text string) {
	t.Helper()
	codeRevisionPTYReply(t, f, sidebarPTYModelTurn{text: text})
	term.wait(text)
	term.send("\x1b") // Cancel only after dispatch/result reached a safe model boundary.
	term.wait("Enter send")
	term.assertAbsent("Enter select")
	term.assertAbsent("Awaiting approval")
}

func codeNarrowPTYAssertPreserved(t *testing.T, f *sidebarPTYFixture, term *handoffPTY, before agent.PlanSnapshot, mode agent.CodePlanExecutionMode, verify string) {
	t.Helper()
	waitPlanPTYRegion(t, term, 92, 120, "1/3", "33%")
	// Scroll the actual panel to expose phase one's completed card, not history.
	term.send(strings.Repeat("\x1b[<64;110;15M", 20))
	waitPlanPTYRegion(t, term, 92, 120, "✓ Phase one")
	term.assertAbsent("Enter select")
	term.assertAbsent("Awaiting approval")
	after := f.durable()
	if after.PlanID != before.PlanID || after.Revision != before.Revision+1 || after.Document.CodeExecutionMode != mode || after.Document.Lifecycle != agent.PlanLifecycleApproved {
		t.Fatalf("narrow revision changed approval/identity/cadence: %+v", after)
	}
	if !reflect.DeepEqual(after.Steps[0], before.Steps[0]) || !reflect.DeepEqual(after.Steps[1], before.Steps[1]) || after.Steps[0].Status != "complete" || after.Steps[0].Evidence == "" || after.Steps[2].Verify != verify {
		t.Fatalf("narrow revision lost earlier evidence or final verifier: before=%+v after=%+v", before, after)
	}
}

// Capture actual generated requests without changing the shared scripted provider.
// A loopback reverse proxy forwards their unmodified bytes to that fixture.
func codeNarrowPTYCaptureRequests(t *testing.T) <-chan string {
	t.Helper()
	cfg, err := config.LoadAppConfig()
	if err != nil {
		t.Fatal(err)
	}
	target, err := url.Parse(cfg.Providers["fixture"]["base_url"])
	if err != nil {
		t.Fatal(err)
	}
	requests := make(chan string, 1)
	proxy := httputil.NewSingleHostReverseProxy(target)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/chat/completions") {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
				return
			}
			_ = r.Body.Close()
			r.Body = io.NopCloser(bytes.NewReader(body))
			var req struct {
				Messages json.RawMessage `json:"messages"`
			}
			if err := json.Unmarshal(body, &req); err != nil {
				t.Error(err)
			}
			// Decode JSON escapes so assertions inspect the generated prompt text.
			var messages []map[string]any
			_ = json.Unmarshal(req.Messages, &messages)
			select {
			case requests <- fmt.Sprint(messages):
			default:
			}
		}
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	cfg.Providers["fixture"]["base_url"] = server.URL
	if err := config.SaveAppConfig(cfg); err != nil {
		t.Fatal(err)
	}
	return requests
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
