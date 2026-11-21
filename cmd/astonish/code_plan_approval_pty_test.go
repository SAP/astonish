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
			const run = "▸ 1/y  Approve and run autonomously until done"
			const review = "▸ 2    Approve and pause for review after each phase"
			term.wait(run, "Request changes", "Decline", "Enter select")
			term.assertAbsent("Continue through verified phases.")
			term.assertAbsent("Pause for review after each verified phase.")
			term.send("\x1b[B")
			term.wait(review)
			term.assertAbsent(run)
			term.send("\x1b[A")
			term.wait(run)
			term.assertAbsent(review)
			term.send("\x1b[A")
			term.wait("▸ n    Decline")
			term.send("\x1b[B")
			term.wait(run)
			if mode == agent.CodePlanReviewEachPhase {
				term.send("\x1b[B")
				term.wait(review)
			}
			term.send("\r")
			term.wait("CADENCE APPROVAL RECEIVED")
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
			term.wait("CADENCE APPROVAL RECEIVED")
			term.assertAbsent("choose how to proceed")
			term.close()
			t.Logf("72x26 real Code PTY repaints arrows, wraps selection, persists %s and resumes without initial approval", mode)
		})
	}
}
