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
	"sync/atomic"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/config"
	persistentsession "github.com/SAP/astonish/pkg/session"
	adksession "google.golang.org/adk/session"
)

// Exercise the real Code composition root, terminal approval, tool execution,
// verifier and persisted completion. Only the remote model is replaced. In
// particular, the fixture never calls PlanRuntime or creates the marker files.
func TestCodePlanExecutionPTYAcceptance(t *testing.T) {
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
			var calls atomic.Int32
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
					Stream   bool              `json:"stream"`
					Tools    []json.RawMessage `json:"tools"`
					Messages json.RawMessage   `json:"messages"`
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
					return
				}
				name, text := "", "Verified cadence execution finished"
				var args any
				n := int32(0)
				if len(req.Tools) > 0 {
					n = calls.Add(1)
					// More than the former three-continuation limit: these are genuine
					// assistant finals, not tool responses or injected user messages.
					switch {
					case n <= 5:
						text = fmt.Sprintf("Execution progress %d; continuing the approved work.", n)
					case n == 6:
						name, args = "update_plan", map[string]any{"step": "one", "status": "running"}
					case n == 7:
						name, args = "update_plan", map[string]any{"step": "one", "status": "complete"}
					case n == 8:
						name, args = "update_plan", map[string]any{"step": "two", "status": "running"}
					case n == 9:
						name, args = "update_plan", map[string]any{"step": "two", "status": "complete"}
					case n == 10:
						name, args = "announce_completion", map[string]any{"outcome_observed": "Both phase marker commands executed successfully", "unverified": ""}
					}
				}
				t.Logf("local provider call=%d tool=%s", n, name)
				message := map[string]any{"role": "assistant", "content": text}
				finish := "stop"
				if name != "" {
					raw, _ := json.Marshal(args)
					message = map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"index": 0, "id": fmt.Sprintf("cadence-%d", n), "type": "function", "function": map[string]any{"name": name, "arguments": string(raw)}}}}
					finish = "tool_calls"
				}
				if req.Stream {
					w.Header().Set("Content-Type", "text/event-stream")
					raw, _ := json.Marshal(map[string]any{"id": "cadence", "object": "chat.completion.chunk", "model": "pty-model", "choices": []any{map[string]any{"index": 0, "delta": message}}})
					fmt.Fprintf(w, "data: %s\n\n", raw)
					raw, _ = json.Marshal(map[string]any{"id": "cadence", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": finish}}})
					fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", raw)
				} else {
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(map[string]any{"id": "cadence", "model": "pty-model", "choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": finish}}})
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
			snapshot := agent.PlanSnapshot{PlanID: "execution-plan", Revision: 1, Sequence: 1, Goal: "Verify two execution phases", Document: agent.PlanDocumentInfo{PlanID: "execution-plan", Revision: 1, Sequence: 1}}
			for _, name := range []string{"one", "two"} {
				marker := filepath.Join(work, name+".marker")
				snapshot.Steps = append(snapshot.Steps, agent.PlanStepInfo{Name: name, Description: "Verify phase " + name, Status: "pending", Outcome: "Phase " + name + " marker exists", Verify: fmt.Sprintf("printf 'verified-%s\\n' > '%s' && test -s '%s'", name, marker, marker), VerifyKind: "behavior"})
			}
			raw, err := json.Marshal(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			var value map[string]any
			if err := json.Unmarshal(raw, &value); err != nil {
				t.Fatal(err)
			}
			if err := store.AppendEvent(ctx, created.Session, &adksession.Event{ID: "pending-plan", Author: "system", Timestamp: time.Now(), Actions: adksession.EventActions{StateDelta: map[string]any{agent.PlanSnapshotStateKey: value}}}); err != nil {
				t.Fatal(err)
			}
			bin := filepath.Join(home, "bin")
			if err := os.Mkdir(bin, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(bin, "npx"), []byte("#!/bin/sh\nexit 127\n"), 0700); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(os.Args[0], "-test.run=^TestBotCLIProcess$", "--", "code", "--model", "fixture:pty-model", "--dir", work, "--resume", created.Session.ID())
			cmd.Dir = work
			cmd.Env = append(os.Environ(), "ASTONISH_BOT_CLI_CHILD=1", "TERM=xterm-256color", "NO_COLOR=1", "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			term := newHandoffPTY(t, cmd)
			defer term.close()
			term.wait("▸ 1/y  Approve and run", "3    Approve and review each phase")
			if mode == agent.CodePlanReviewEachPhase {
				// Full-access run is now option 2; choose review with current approvals.
				term.send("\x1b[B")
				term.wait("▸ 2    Approve and run with full access")
				term.send("\x1b[B")
				term.wait("▸ 3    Approve and review each phase")
			}
			term.send("\r")
			assertMarker := func(name string) {
				t.Helper()
				got, err := os.ReadFile(filepath.Join(work, name+".marker"))
				if err != nil || string(got) != "verified-"+name+"\n" {
					t.Fatalf("real verify marker %s: %q, %v", name, got, err)
				}
			}
			if mode == agent.CodePlanReviewEachPhase {
				term.wait("Paused for review", "Enter send")
				assertMarker("one")
				// Observe a quiescent input boundary, rather than catching a transient
				// paint before the next phase is automatically started.
				before := calls.Load()
				time.Sleep(300 * time.Millisecond)
				if got := calls.Load(); got != before || got != 7 {
					t.Fatalf("review did not stop after first verified phase: calls %d -> %d", before, got)
				}
				if _, err := os.Stat(filepath.Join(work, "two.marker")); !os.IsNotExist(err) {
					t.Fatalf("second phase ran before review: %v", err)
				}
				term.send("continue\r")
			}
			term.wait("Verified cadence execution finished", "Enter send")
			assertMarker("one")
			assertMarker("two")
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
			if durable.Document.CodeExecutionMode != mode || durable.Document.Results == "" {
				t.Fatalf("missing durable cadence/completion: %+v", durable.Document)
			}
			for _, step := range durable.Steps {
				if step.Status != "complete" || step.Evidence == "" {
					t.Fatalf("missing verified completion: %+v", step)
				}
			}
			t.Logf("real Code PTY: %s crossed five assistant finals, verified both markers and persisted Results", mode)
		})
	}
}
