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
	"regexp"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/config"
	persistentsession "github.com/SAP/astonish/pkg/session"
	adkmodel "google.golang.org/adk/model"
	adksession "google.golang.org/adk/session"
	"google.golang.org/genai"
)

// Real Code --resume, real FileStore, real terminal input. The loopback provider
// rejects model turns. Large persisted read/search/skill payloads reproduce the
// review-plan-history case that a tiny child edit alone does not exercise.
func TestDelegationLargePayloadCodePTYAcceptance(t *testing.T) {
	for _, mib := range []int{1, 5} {
		t.Run(fmt.Sprintf("%dMiB-per-result", mib), func(t *testing.T) {
			home := t.TempDir()
			work := filepath.Join(home, "project")
			if err := os.Mkdir(work, 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("HOME", home)
			t.Setenv("XDG_CONFIG_HOME", home)
			t.Setenv("SUDO_USER", "")
			var modelCalls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/models") {
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprint(w, `{"data":[{"id":"pty-model"}]}`)
					return
				}
				modelCalls.Add(1)
				http.Error(w, "history acceptance forbids model turns", http.StatusServiceUnavailable)
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
			store, err := persistentsession.NewFileStore(filepath.Join(base, "code"))
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			sum := sha256.Sum256([]byte(work))
			owner := fmt.Sprintf("local_user_%x", sum[:8])
			parent, err := store.Create(ctx, &adksession.CreateRequest{AppName: "astonish_code", UserID: owner})
			if err != nil {
				t.Fatal(err)
			}
			appendEvent := func(session adksession.Session, event *adksession.Event) {
				t.Helper()
				event.Timestamp = time.Now()
				if err := store.AppendEvent(ctx, session, event); err != nil {
					t.Fatal(err)
				}
			}
			partEvent := func(id string, part *genai.Part) *adksession.Event {
				return &adksession.Event{ID: id, Author: "agent", LLMResponse: adkmodel.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{part}}}}
			}
			const taskName = "review-plan-history"
			const pendingName = "pending-review"
			const finalText = "REVIEW COMPLETE: chronology and diff verified."
			const parentText = "PARENT READY: all review tasks completed."
			const assignment = "Review plan history and preserve recorded edits."
			const rawMarker = "RAW-PAYLOAD-MUST-NOT-FLOOD"
			const scaffold = "Execute the immutable delegation handoff in your instructions."
			payloadLine := rawMarker + " source line: func historicalPlan() { return completed }\n"
			payload := strings.Repeat(payloadLine, (mib<<20)/len(payloadLine)+1)
			var tasks, results []any
			var pendingPath, reviewedPath string
			for i, name := range []string{taskName, pendingName, "verify-chronology", "review-tests"} {
				child, err := store.Create(ctx, &adksession.CreateRequest{AppName: "astonish_code", UserID: owner, State: map[string]any{persistentsession.StateKeyParentID: parent.Session.ID()}})
				if err != nil {
					t.Fatal(err)
				}
				if err := store.SetSessionTitle(ctx, child.Session.ID(), name); err != nil {
					t.Fatal(err)
				}
				taskID := fmt.Sprintf("large-task-%d", i)
				h := agent.DelegationHandoff{Version: agent.DelegationHandoffVersion, ParentSessionID: parent.Session.ID(), ChildSessionID: child.Session.ID(), Name: name, BatchID: "large-batch", TaskID: taskID, Attempt: 1, OriginalTask: assignment, EffectiveTask: assignment, OriginalInstructions: "Preserve the signed diff.", EffectiveInstructions: "Preserve the signed diff.", WorkDir: work, Plan: agent.DelegationPlanContext{Status: agent.DelegationPlanMissing}}
				raw, err := json.Marshal(h)
				if err != nil {
					t.Fatal(err)
				}
				appendEvent(child.Session, &adksession.Event{ID: "handoff", Author: "system", Actions: adksession.EventActions{StateDelta: map[string]any{agent.DelegationHandoffStateKey: string(raw), "batch_id": "large-batch", "task_id": taskID, "attempt": 1}}})
				appendEvent(child.Session, &adksession.Event{ID: "trigger", Author: "user", LLMResponse: adkmodel.LLMResponse{Content: genai.NewContentFromText(scaffold, genai.RoleUser)}})
				if i < 2 {
					for j, tool := range []string{"read_file", "grep_search", "skill_lookup"} {
						id := fmt.Sprintf("large-result-%d", j)
						args := map[string]any{"path": "history.go", "pattern": "historicalPlan", "name": "debug-regression"}
						appendEvent(child.Session, partEvent(id+"-call", &genai.Part{FunctionCall: &genai.FunctionCall{ID: id, Name: tool, Args: args}}))
						appendEvent(child.Session, partEvent(id+"-response", &genai.Part{FunctionResponse: &genai.FunctionResponse{ID: id, Name: tool, Response: map[string]any{"content": payload, "success": true}}}))
					}
					// Arguments intentionally disagree with the persisted verification.
					// The viewer must use recorded evidence, not args or today's file.
					appendEvent(child.Session, partEvent("edit-call", &genai.Part{FunctionCall: &genai.FunctionCall{ID: "edit", Name: "edit_file", Args: map[string]any{"path": filepath.Join(work, "history.go"), "old_string": "WRONG-ARG-OLD", "new_string": "WRONG-ARG-NEW"}}}))
					appendEvent(child.Session, partEvent("edit-result", &genai.Part{FunctionResponse: &genai.FunctionResponse{ID: "edit", Name: "edit_file", Response: map[string]any{"success": true, "verification_context": "@@ history.go:42\n- 42| before-recorded-edit\n+ 42| after-recorded-edit\n"}}}))
				}
				appendEvent(child.Session, partEvent("final", &genai.Part{Text: finalText}))
				tasks = append(tasks, map[string]any{"name": name, "task": assignment, "instructions": "Preserve the signed diff."})
				// No duplicated child final text in the parent summary: detail has to
				// finish loading to expose the child's result and edit evidence.
				results = append(results, map[string]any{"name": name, "task_id": taskID, "child_session_id": child.Session.ID(), "attempts": 1, "status": "success", "duration": "1m42s"})
				path := filepath.Join(base, "code", "astonish_code", owner, child.Session.ID()+".jsonl")
				if i == 0 {
					reviewedPath = path
				}
				if i == 1 {
					pendingPath = path
				}
			}
			appendEvent(parent.Session, partEvent("delegate-call", &genai.Part{FunctionCall: &genai.FunctionCall{ID: "large-batch", Name: "delegate_tasks", Args: map[string]any{"tasks": tasks}}}))
			appendEvent(parent.Session, partEvent("delegate-result", &genai.Part{FunctionResponse: &genai.FunctionResponse{ID: "large-batch", Name: "delegate_tasks", Response: map[string]any{"batch_id": "large-batch", "status": "success", "results": results}}}))
			appendEvent(parent.Session, partEvent("parent-final", &genai.Part{Text: parentText}))
			if err := os.WriteFile(filepath.Join(work, "history.go"), []byte("UNRELATED CURRENT FILE\n"), 0600); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(reviewedPath)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("seeded %d-byte child transcript with three %d-byte tool results", len(before), len(payload))
			bin := filepath.Join(home, "bin")
			if err := os.Mkdir(bin, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(bin, "npx"), []byte("#!/bin/sh\nexit 127\n"), 0700); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(os.Args[0], "-test.run=^TestBotCLIProcess$", "--", "code", "--model", "fixture:pty-model", "--dir", work, "--resume", parent.Session.ID())
			cmd.Dir = work
			cmd.Env = append(os.Environ(), "ASTONISH_BOT_CLI_CHILD=1", "TERM=xterm-256color", "COLORTERM=truecolor", "CLICOLOR_FORCE=1", "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			cleanEnv := cmd.Env[:0]
			for _, entry := range cmd.Env {
				if !strings.HasPrefix(entry, "NO_COLOR=") {
					cleanEnv = append(cleanEnv, entry)
				}
			}
			cmd.Env = cleanEnv
			term := newHandoffPTY(t, cmd)
			term.wait(parentText, taskName, pendingName, "Enter send")
			for attempt := 0; attempt < 3; attempt++ {
				// Separate clicks beyond the UI double-click window.
				time.Sleep(450 * time.Millisecond)
				started := time.Now()
				term.click(taskName)
				term.waitWithin(2*time.Second, "[Activity]", "Read file", "Search", "skill lookup")
				term.send("\x1b[F")
				term.waitWithin(2*time.Second, "− before-recorded-edit", "+ after-recorded-edit", "+1 −1")
				t.Logf("open %d (%d MiB/result): %s", attempt, mib, time.Since(started))
				for _, absent := range []string{rawMarker, scaffold, "WRONG-ARG", "UNRELATED CURRENT FILE"} {
					term.assertAbsent(absent)
				}
				// Scroll to the child's final answer. This checks compactness across
				// the full activity, rather than only hiding JSON above the fold.
				term.send("\x1b[F")
				term.waitWithin(2*time.Second, finalText, "[Activity]")
				term.assertAbsent(rawMarker)
				term.send("\t")
				term.waitWithin(2*time.Second, "[Handoff]", "Immutable handoff", assignment)
				term.send("h")
				term.waitWithin(2*time.Second, "[Activity]", finalText)
				// A deliberate idle interval catches repeated View/render work, not
				// merely an Esc queued in the same batch as a tab transition.
				time.Sleep(250 * time.Millisecond)
				started = time.Now()
				term.send("\x1b")
				term.waitWithin(2*time.Second, parentText, "Enter send", taskName)
				t.Logf("idle Esc %d (%d MiB/result): %s", attempt, mib, time.Since(started))
			}
			term.mu.Lock()
			ansiOutput := term.raw.String()
			term.mu.Unlock()
			// Terminal diff updates may interleave cursor movements inside words.
			// Assert visible signed lines above and SGR output here; exact diff
			// color preservation is checked by the renderer regression test.
			if !regexp.MustCompile(`\x1b\[[0-9;:]*m`).MatchString(ansiOutput) {
				t.Error("missing styled terminal output")
			}

			// Deterministically hold a never-loaded child's real transcript read.
			// FIFO is test-owned, in private HOME, and contains no production data.
			// This tests Esc during genuine pending FileStore I/O without a fake backend.
			pendingBytes, err := os.ReadFile(pendingPath)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(pendingPath, pendingPath+".saved"); err != nil {
				t.Fatal(err)
			}
			if err := syscall.Mkfifo(pendingPath, 0600); err != nil {
				t.Fatal(err)
			}
			fd, err := syscall.Open(pendingPath, syscall.O_RDWR|syscall.O_NONBLOCK, 0600)
			if err != nil {
				t.Fatal(err)
			}
			fifo := os.NewFile(uintptr(fd), pendingPath)
			defer fifo.Close()
			time.Sleep(450 * time.Millisecond)
			term.click(pendingName)
			term.waitWithin(2*time.Second, "[Activity]", "Loading attempt detail")
			started := time.Now()
			term.send("\x1b")
			term.waitWithin(2*time.Second, parentText, "Enter send", pendingName)
			t.Logf("pending-load Esc (%d MiB/result): %s", mib, time.Since(started))
			// Complete the old request after navigating away; late results must
			// not reopen its viewer or freeze interaction with another task.
			written := make(chan error, 1)
			go func() { _, err := fifo.Write(pendingBytes); _ = fifo.Close(); written <- err }()
			select {
			case err := <-written:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("pending FileStore read did not drain fixture")
			}
			time.Sleep(450 * time.Millisecond)
			term.click("review-tests")
			term.waitWithin(2*time.Second, "[Activity]", finalText)
			term.send("\x1b")
			term.waitWithin(2*time.Second, parentText, "Enter send")
			term.close()
			after, err := os.ReadFile(reviewedPath)
			if err != nil || sha256.Sum256(after) != sha256.Sum256(before) {
				t.Fatalf("detail inspection mutated child transcript: %v", err)
			}
			t.Logf("loopback provider rejected %d background requests; no external provider was used", modelCalls.Load())
		})
	}
}

func (p *handoffPTY) waitWithin(budget time.Duration, wants ...string) string {
	p.t.Helper()
	timer := time.NewTimer(budget)
	defer timer.Stop()
	for {
		frame, ok := p.frame(), true
		for _, want := range wants {
			if !strings.Contains(frame, want) {
				ok = false
			}
		}
		if ok {
			return frame
		}
		select {
		case <-p.changed:
		case <-p.done:
			p.t.Fatalf("Code exited before %q:\n%s", wants, p.frame())
		case <-timer.C:
			p.t.Fatalf("Code exceeded %s waiting for %q:\n%s", budget, wants, p.frame())
		}
	}
}
