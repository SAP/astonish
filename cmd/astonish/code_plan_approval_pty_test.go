package astonish

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/config"
	persistentsession "github.com/SAP/astonish/pkg/session"
	adksession "google.golang.org/adk/session"
)

// Real Code CLI + FileStore + current PTY cells. Only the model response is
// scripted locally; approval dispatch and durable cadence are production code.
func TestCodePlanApprovalPTYAcceptance(t *testing.T) {
	for _, mode := range []agent.CodePlanExecutionMode{agent.CodePlanRunToCompletion, agent.CodePlanReviewEachPhase} {
		t.Run(string(mode), func(t *testing.T) {
			home := t.TempDir()
			work := filepath.Join(home, "project")
			if err := os.Mkdir(work, 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("HOME", home)
			t.Setenv("XDG_CONFIG_HOME", home)
			t.Setenv("SUDO_USER", "")
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/models") {
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprint(w, `{"data":[{"id":"pty-model"}]}`)
					return
				}
				if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
					http.NotFound(w, r)
					return
				}
				var req struct {
					Stream bool `json:"stream"`
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
					return
				}
				message := map[string]any{"role": "assistant", "content": "CADENCE APPROVAL RECEIVED"}
				if req.Stream {
					w.Header().Set("Content-Type", "text/event-stream")
					raw, _ := json.Marshal(map[string]any{"id": "cadence", "object": "chat.completion.chunk", "model": "pty-model", "choices": []any{map[string]any{"index": 0, "delta": message}}})
					fmt.Fprintf(w, "data: %s\n\n", raw)
					fmt.Fprint(w, "data: {\"id\":\"cadence\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
				} else {
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]any{"id": "cadence", "model": "pty-model", "choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": "stop"}}})
				}
			}))
			defer server.Close()
			cfg := &config.AppConfig{General: config.GeneralConfig{DefaultProvider: "fixture", DefaultModel: "pty-model"}, Providers: map[string]config.ProviderConfig{"fixture": {"type": "openai_compat", "base_url": server.URL, "api_key": "fixture"}}}
			if err := config.SaveAppConfig(cfg); err != nil {
				t.Fatal(err)
			}
			base, err := config.GetSessionsDir(&cfg.Sessions)
			if err != nil {
				t.Fatal(err)
			}
			storePath := filepath.Join(base, "code")
			store, err := persistentsession.NewFileStore(storePath)
			if err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256([]byte(work))
			user := fmt.Sprintf("local_user_%x", sum[:8])
			ctx := context.Background()
			created, err := store.Create(ctx, &adksession.CreateRequest{AppName: "astonish_code", UserID: user})
			if err != nil {
				t.Fatal(err)
			}
			snapshot := agent.PlanSnapshot{PlanID: "cadence-plan", Revision: 1, Sequence: 1, Goal: "Cadence approval fixture", Document: agent.PlanDocumentInfo{PlanID: "cadence-plan", Revision: 1, Sequence: 1}, Steps: []agent.PlanStepInfo{{Name: "one", Description: "Verify one marker", Status: "pending", Verify: "true", VerifyKind: "behavior"}}}
			raw, err := json.Marshal(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			var value map[string]any
			if err := json.Unmarshal(raw, &value); err != nil {
				t.Fatal(err)
			}
			err = store.AppendEvent(ctx, created.Session, &adksession.Event{ID: "pending-plan", Author: "system", Timestamp: time.Now(), Actions: adksession.EventActions{StateDelta: map[string]any{agent.PlanSnapshotStateKey: value}}})
			if err != nil {
				t.Fatal(err)
			}
			bin := filepath.Join(home, "bin")
			if err := os.Mkdir(bin, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(bin, "npx"), []byte("#!/bin/sh\nexit 127\n"), 0700); err != nil {
				t.Fatal(err)
			}
			start := func() *handoffPTY {
				cmd := exec.Command(os.Args[0], "-test.run=^TestBotCLIProcess$", "--", "code", "--model", "fixture:pty-model", "--dir", work, "--resume", created.Session.ID())
				cmd.Dir = work
				cmd.Env = append(os.Environ(), "ASTONISH_BOT_CLI_CHILD=1", "TERM=xterm-256color", "NO_COLOR=1", "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
				return newHandoffPTY(t, cmd)
			}
			term := start()
			const run = "▸ 1/y  Approve and run"
			const review = "▸ 3    Approve and review each phase"
			term.wait(run, "Request changes", "Decline", "Enter select", "Folders ○", "Tools ○")
			term.assertAbsent("p change")
			term.send("p")
			term.wait(run)
			term.send("\x1b[B")
			term.wait("▸ 2    Approve and run with full access", "without tool or folder prompts", "Not", "sandboxed.", "Folders ○", "Tools ○")
			term.send("\t")
			term.wait(review, "Pause after each verified phase")
			term.send("\t")
			term.wait("▸ 4    Approve and review with full access", "no tool or folder prompts", "Not", "sandboxed.", "Folders ○", "Tools ○")
			term.assertAbsent("CADENCE APPROVAL RECEIVED")
			term.send("\x1b[Z\x1b[A\x1b[A")
			term.wait(run)
			term.send("\x1b[A")
			term.wait("▸ 6/n  Decline")
			term.send("\x1b[B")
			term.wait(run)
			if mode == agent.CodePlanReviewEachPhase {
				term.send("\x1b[B\x1b[B")
				term.wait(review)
			}
			term.send("\r")
			term.wait("CADENCE APPROVAL RECEIVED")
			// This fixture proves approval/cadence, not phase execution. Stop the
			// still-incomplete execution before reopening so its response is flushed.
			term.send("\x1b")
			term.wait("Enter send")
			term.assertAbsent("choose how to proceed")
			term.close()
			reopened, err := persistentsession.NewFileStore(storePath)
			if err != nil {
				t.Fatal(err)
			}
			got, err := reopened.Get(ctx, &adksession.GetRequest{AppName: "astonish_code", UserID: user, SessionID: created.Session.ID()})
			if err != nil {
				t.Fatal(err)
			}
			state, err := got.Session.State().Get(agent.PlanSnapshotStateKey)
			if err != nil {
				t.Fatal(err)
			}
			raw, err = json.Marshal(state)
			if err != nil {
				t.Fatal(err)
			}
			var durable agent.PlanSnapshot
			if err := json.Unmarshal(raw, &durable); err != nil {
				t.Fatal(err)
			}
			if durable.Document.CodeExecutionMode != mode {
				t.Fatalf("durable cadence=%q want %q", durable.Document.CodeExecutionMode, mode)
			}
			term = start()
			term.wait("CADENCE APPROVAL RECEIVED", "Enter send")
			term.assertAbsent("choose how to proceed")
			term.close()
			t.Logf("72x26 real Code PTY repaints arrows, wraps selection, persists %s and resumes without initial approval", mode)
		})
	}
}

// The permission drills use the same isolated CLI/process/loopback-provider
// fixture as the sidebar acceptance tests. In particular they never use --yolo,
// mutate a running user's session, or inject approval/reducer events.
func TestCodePlanPermissionsPTYAcceptance(t *testing.T) {
	for _, mode := range []agent.CodePlanExecutionMode{agent.CodePlanRunToCompletion, agent.CodePlanReviewEachPhase} {
		for _, full := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/full=%t", mode, full), func(t *testing.T) {
				f := newSidebarPTYFixture(t)
				a, b := codePermissionsPTYOutside(t, f, "outside-a"), codePermissionsPTYOutside(t, f, "outside-b")
				term := f.start()
				codePermissionsPTYAnnounce(t, f, term, a, b)
				term.wait("using current tool and folder approvals", "Folders ○", "Tools ○")
				term.assertAbsent("p change")
				term.send("p\x1b[B")
				term.wait("without tool or folder prompts", "Not", "sandboxed.", "Folders ○", "Tools ○")
				choice := 1
				if mode == agent.CodePlanReviewEachPhase {
					choice = 3
				}
				if full {
					choice++
				}
				term.send(fmt.Sprint(choice))
				f.update("one", "running")
				if full {
					term.wait("Folders ✓  Tools ✓")
					term.send("\x10") // Ctrl+P remains available during active execution.
					term.wait("Revoke Full access for this plan")
					term.send("\x1b")
					term.wait("Folders ✓  Tools ✓")
				} else {
					term.assertAbsent("Folders ✓  Tools ✓")
				}
				f.turn("write_file", map[string]any{"path": a, "content": "main-permission-proof"})
				if !full {
					// Current approvals must not silently inherit the selector's
					// previously highlighted full-access choice.
					// Folder authorization precedes tool authorization. Denying
					// it directly must leave the write unexecuted; tool-negative
					// coverage remains in the session-folder acceptance drill.
					codePermissionsPTYFolderPrompt(term)
					term.send("4")
					codePermissionsPTYReply(t, f, term, sidebarPTYModelTurn{text: "CURRENT APPROVAL DENIED"})
					term.wait("CURRENT APPROVAL DENIED")
					if _, err := os.Stat(a); !os.IsNotExist(err) {
						t.Fatalf("denied write reached outside folder: %v", err)
					}
					term.close()
					return
				}
				// Code exposes concrete main tools; the child has the scoped
				// deferred bridge, which is exercised below.
				codePermissionsPTYReply(t, f, term, sidebarPTYModelTurn{name: "read_file", args: map[string]any{"path": a}})
				codePermissionsPTYReply(t, f, term, sidebarPTYModelTurn{name: "update_plan", args: map[string]any{"step": "one", "status": "complete"}})
				codePermissionsPTYReview(t, f, term, mode, "one")
				codePermissionsPTYReply(t, f, term, sidebarPTYModelTurn{name: "update_plan", args: map[string]any{"step": "two", "status": "running"}})
				codePermissionsPTYReply(t, f, term, sidebarPTYModelTurn{name: "delegate_tasks", args: map[string]any{"tasks": []any{map[string]any{"name": "permission-worker", "task": "Write and read the isolated outside-folder marker", "instructions": "Only use the assigned markers " + a + " and " + b, "tools": []string{"write_file", "read_file"}, "plan_step": "two"}}}})
				codePermissionsPTYReply(t, f, term, sidebarPTYModelTurn{name: "write_file", args: map[string]any{"path": b, "content": "child-permission-proof"}})
				codePermissionsPTYReply(t, f, term, sidebarPTYModelTurn{name: "read_file", args: map[string]any{"path": b}})
				codePermissionsPTYReply(t, f, term, sidebarPTYModelTurn{name: "execute_tool", args: map[string]any{"name": "read_file", "arguments": map[string]any{"path": a, "force": true}}})
				codePermissionsPTYReply(t, f, term, sidebarPTYModelTurn{text: "CHILD PERMISSION VERIFIED"})
				codePermissionsPTYReply(t, f, term, sidebarPTYModelTurn{name: "update_plan", args: map[string]any{"step": "two", "status": "complete"}})
				codePermissionsPTYReview(t, f, term, mode, "two")
				codePermissionsPTYReply(t, f, term, sidebarPTYModelTurn{name: "update_plan", args: map[string]any{"step": "three", "status": "running"}})
				codePermissionsPTYReply(t, f, term, sidebarPTYModelTurn{name: "update_plan", args: map[string]any{"step": "three", "status": "complete"}})
				codePermissionsPTYReview(t, f, term, mode, "three")
				codePermissionsPTYReply(t, f, term, sidebarPTYModelTurn{name: "announce_completion", args: map[string]any{"outcome_observed": "Main, deferred, child and verifier accessed both outside markers", "unverified": ""}})
				// The completion tool records Results but the model still owes
				// its final response before the CLI returns to the composer.
				codePermissionsPTYReply(t, f, term, sidebarPTYModelTurn{text: "PLAN PERMISSIONS COMPLETED"})
				term.wait("PLAN PERMISSIONS COMPLETED", "Enter send")
				term.assertAbsent("Folders ✓  Tools ✓")
				if s := f.durable(); !s.IsFullyAccepted() || s.Document.CodeExecutionMode != mode {
					t.Fatalf("completion/cadence not durable: %+v", s)
				}
				for path, want := range map[string]string{a: "main-permission-proof", b: "child-permission-proof"} {
					if got, err := os.ReadFile(path); err != nil || string(got) != want {
						t.Fatalf("real outside write %s: %q %v", path, got, err)
					}
				}
				term.send("Answer an unrelated normal question.\r")
				codePermissionsPTYReply(t, f, term, sidebarPTYModelTurn{text: "NORMAL QUESTION ANSWERED"})
				term.wait("NORMAL QUESTION ANSWERED", "Enter send")
				term.send("Delegate an unrelated read of the outside marker.\r")
				f.turn("delegate_tasks", map[string]any{"tasks": []any{map[string]any{"name": "normal-worker", "task": "Read " + a, "tools": []string{"read_file"}}}})
				term.wait("Tool authorization required", "delegate_tasks")
				term.send("1")
				codePermissionsPTYReply(t, f, term, sidebarPTYModelTurn{name: "delegate_tasks", args: map[string]any{"tasks": []any{map[string]any{"name": "normal-worker", "task": "Read " + a, "tools": []string{"read_file"}}}}})
				// No plan_step is supplied: reaching a real child read proves
				// the completed plan no longer admits/rejects unrelated work.
				codePermissionsPTYReply(t, f, term, sidebarPTYModelTurn{name: "read_file", args: map[string]any{"path": a}})
				codePermissionsPTYFolderPrompt(term)
				term.send("4")
				codePermissionsPTYReply(t, f, term, sidebarPTYModelTurn{text: "NORMAL CHILD DENIED"})
				codePermissionsPTYReply(t, f, term, sidebarPTYModelTurn{text: "NORMAL DELEGATION FINISHED"})
				term.wait("NORMAL DELEGATION FINISHED", "Enter send")
				term.close()
				codePermissionsPTYReadEvidence(t, f, "execute_tool", "main-permission-proof")
				codePermissionsPTYReadEvidence(t, f, "read_file", "child-permission-proof")
			})
		}
	}
}

func TestCodeSessionFolderPermissionsPTYAcceptance(t *testing.T) {
	f := newSidebarPTYFixture(t)
	a, b := codePermissionsPTYOutside(t, f, "folder-a"), codePermissionsPTYOutside(t, f, "folder-b")
	for _, path := range []string{a, b} {
		if err := os.WriteFile(path, []byte("session-folder-proof"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	term := f.start()
	term.wait("Enter send")
	term.send("Read the isolated external folders.\r")
	f.turn("read_file", map[string]any{"path": a})
	codePermissionsPTYFolderPrompt(term)
	term.send("1")
	codePermissionsPTYReply(t, f, term, sidebarPTYModelTurn{name: "read_file", args: map[string]any{"path": a}}) // retry authorized call
	codePermissionsPTYReply(t, f, term, sidebarPTYModelTurn{name: "read_file", args: map[string]any{"path": a}})
	codePermissionsPTYFolderPrompt(term) // once must be consumed
	term.send("2")
	codePermissionsPTYReply(t, f, term, sidebarPTYModelTurn{name: "read_file", args: map[string]any{"path": a}})
	codePermissionsPTYReply(t, f, term, sidebarPTYModelTurn{name: "read_file", args: map[string]any{"path": a}})
	codePermissionsPTYReply(t, f, term, sidebarPTYModelTurn{name: "read_file", args: map[string]any{"path": b}})
	codePermissionsPTYFolderPrompt(term) // the single-folder grant is not broad
	term.send("3")
	codePermissionsPTYReply(t, f, term, sidebarPTYModelTurn{name: "read_file", args: map[string]any{"path": b}})
	codePermissionsPTYReply(t, f, term, sidebarPTYModelTurn{text: "SESSION FOLDERS GRANTED"})
	term.wait("SESSION FOLDERS GRANTED", "Folders ✓", "Enter send")
	term.close()
	term = f.start() // same durable session, new process; no permission reinjection
	term.wait("Folders ✓", "Enter send")
	c := codePermissionsPTYOutside(t, f, "folder-c")
	if err := os.WriteFile(c, []byte("reopened-folder-proof"), 0600); err != nil {
		t.Fatal(err)
	}
	term.send("Read a new outside folder after reopening.\r")
	f.turn("read_file", map[string]any{"path": c})
	codePermissionsPTYReply(t, f, term, sidebarPTYModelTurn{name: "write_file", args: map[string]any{"path": c, "content": "must-not-write"}})
	term.wait("Tool authorization required", "write_file") // folder != tool grant
	term.send("3")                                         // existing tool prompt still uses 3=Deny
	codePermissionsPTYReply(t, f, term, sidebarPTYModelTurn{text: "FOLDER ONLY RESTORED"})
	term.wait("FOLDER ONLY RESTORED", "Enter send")
	if got, err := os.ReadFile(c); err != nil || string(got) != "reopened-folder-proof" {
		t.Fatalf("folder grant escalated tool access: %q %v", got, err)
	}
	term.send("/permissions\r")
	term.wait("All folders for this session")
	term.send("\r") // selected broad-folder revoke action
	term.wait("Current approvals")
	term.send("\x1b")
	term.wait("Enter send")
	term.assertAbsent("Folders ✓")
	term.send("Read the new outside folder after revocation.\r")
	f.turn("read_file", map[string]any{"path": c})
	codePermissionsPTYFolderPrompt(term)
	term.send("4")
	codePermissionsPTYReply(t, f, term, sidebarPTYModelTurn{text: "FOLDER REVOKED"})
	term.wait("FOLDER REVOKED", "Enter send")
	term.close()
	// Reopen again to prove revocation is durable, not only an overlay repaint.
	term = f.start()
	term.wait("Enter send")
	term.assertAbsent("Folders ✓")
	term.send("Read after reopening the revoked session.\r")
	f.turn("read_file", map[string]any{"path": c})
	codePermissionsPTYFolderPrompt(term)
	term.send("3") // regrant before switching, so isolation cannot pass vacuously
	codePermissionsPTYReply(t, f, term, sidebarPTYModelTurn{text: "REGRANTED FOR ISOLATION"})
	term.wait("REGRANTED FOR ISOLATION", "Folders ✓", "Enter send")
	term.close()
	store, err := persistentsession.NewFileStore(f.storePath)
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.Create(context.Background(), &adksession.CreateRequest{AppName: "astonish_code", UserID: f.user})
	if err != nil {
		t.Fatal(err)
	}
	f.sessionID = created.Session.ID()
	term = f.start()
	term.wait("Enter send")
	term.assertAbsent("Folders ✓")
	term.send("Read the other session's outside folder.\r")
	f.turn("read_file", map[string]any{"path": c})
	codePermissionsPTYFolderPrompt(term)
	term.send("4")
	codePermissionsPTYReply(t, f, term, sidebarPTYModelTurn{text: "SESSION ISOLATION VERIFIED"})
	term.wait("SESSION ISOLATION VERIFIED", "Enter send")
	term.close()
}

func codePermissionsPTYReadEvidence(t *testing.T, f *sidebarPTYFixture, toolName, want string) {
	t.Helper()
	// Delegates may use the factory's app/user namespace rather than the
	// parent Code namespace. Search this isolated fixture store only.
	paths, err := filepath.Glob(filepath.Join(f.storePath, "*", "*", "*.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		events, err := persistentsession.NewTranscript(path).ReadEvents()
		if err != nil {
			t.Fatal(err)
		}
		for _, event := range events {
			if event.Content == nil {
				continue
			}
			for _, part := range event.Content.Parts {
				if part.FunctionResponse != nil && part.FunctionResponse.Name == toolName {
					raw, err := json.Marshal(part.FunctionResponse.Response)
					if err != nil {
						t.Fatal(err)
					}
					if strings.Contains(string(raw), want) {
						return
					}
				}
			}
		}
	}
	t.Fatalf("no durable %s response contains %q; model narration is not read evidence", toolName, want)
}

func codePermissionsPTYOutside(t *testing.T, f *sidebarPTYFixture, name string) string {
	t.Helper()
	dir := filepath.Join(filepath.Dir(f.work), name)
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "proof.txt")
}

func codePermissionsPTYFolderPrompt(term *handoffPTY) {
	term.t.Helper()
	term.wait("Folder access required", "1", "Allow once", "2", "Always allow this folder", "3", "Always allow any folder for this session", "4", "Deny")
}

// Wait at the actual model boundary. Full-access callers supply no approval
// keystrokes, so an unexpected tool/folder prompt blocks this barrier and fails
// with the current terminal frame rather than falsely accepting a final marker.
func codePermissionsPTYReply(t *testing.T, f *sidebarPTYFixture, term *handoffPTY, turn sidebarPTYModelTurn) {
	t.Helper()
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	for {
		select {
		case reply := <-f.turns:
			select {
			case reply <- turn:
				return
			case <-timer.C:
				t.Fatalf("permission provider reply canceled:\n%s", term.frame())
			}
		case <-term.changed:
			frame := term.frame()
			if strings.Contains(frame, "Tool authorization required") || strings.Contains(frame, "Folder access required") {
				// Approval dispatch may still be erasing the preceding prompt.
				// A stable prompt is detected by the timeout below; don't mistake
				// one in-flight paint for newly requested authorization.
				continue
			}
		case <-term.done:
			t.Fatalf("permission CLI exited:\n%s", term.frame())
		case <-timer.C:
			t.Fatalf("permission workflow blocked before %s (unexpected authorization/review or tool failure):\n%s", turn.name, term.frame())
		}
	}
}

func codePermissionsPTYReview(t *testing.T, f *sidebarPTYFixture, term *handoffPTY, mode agent.CodePlanExecutionMode, step string) {
	t.Helper()
	if mode != agent.CodePlanReviewEachPhase {
		return
	}
	if step == "three" {
		// There is no next phase to review before: the runtime permits
		// recording Results directly once the final verifier passes.
		return
	}
	// The narrow sidebar wraps "Paused for phase review" across cells.
	// The phase report and idle composer prove the actual checkpoint.
	term.wait("Paused for phase", "Paused for review. Say continue", "Enter send")
	if s := f.durable(); s.Document.CodeReviewPending != step || s.Steps[map[string]int{"one": 0, "two": 1, "three": 2}[step]].Status != "complete" {
		t.Fatalf("review checkpoint missing for %s: %+v", step, s)
	}
	select {
	case <-f.turns:
		t.Fatalf("full access bypassed intentional phase review:\n%s", term.frame())
	case <-time.After(200 * time.Millisecond):
	}
	term.send("continue\r")
}

func codePermissionsPTYAnnounce(t *testing.T, f *sidebarPTYFixture, term *handoffPTY, a, b string) {
	t.Helper()
	term.wait("Enter send")
	term.send("\x1b[Z")
	term.wait("Plan")
	term.send("Plan the isolated permissions acceptance workflow.\r")
	f.turn("gplan_gaps", map[string]any{"gaps": []any{map[string]any{"question": "No source index in temporary fixture", "why_codegraph_insufficient": "Isolated marker workflow"}}})
	f.turn("gplan_finalize", map[string]any{"acceptance": "Write and read outside markers through main, deferred, child and verifier tools"})
	var steps []map[string]any
	for i, name := range []string{"one", "two", "three"} {
		path := a
		if i == 1 {
			path = b
		}
		verify := fmt.Sprintf("test -s '%s' && printf verified-%s", path, name)
		if i == 2 {
			verify = fmt.Sprintf("test -s '%s' && test -s '%s' && printf verified-both", a, b)
		}
		steps = append(steps, map[string]any{"name": name, "description": "Permission phase " + name, "details": "Exercise actual outside marker access", "summary": "Verify outside access " + name, "outcome": "Outside marker verified", "verify": verify, "verify_kind": "behavior", "parallel_group": "wave-" + name, "files": []any{map[string]any{"path": path, "kind": "new"}}})
	}
	f.turn("announce_plan", map[string]any{"goal": "Plan-scoped permission acceptance", "context": "## Problem\nProve permission scope.\n## Root causes\nIsolated terminal acceptance.\n## Approach\nWrite outside temporary project.\n## Boundaries\nTemporary HOME only.", "what_not_to_do": "Never touch user files", "verification": "Write and read outside markers through main, deferred, child and verifier tools", "steps": steps})
	term.wait("Approve and run")
}
