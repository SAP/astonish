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
	adkmodel "google.golang.org/adk/model"
	adksession "google.golang.org/adk/session"
	"google.golang.org/genai"
)

// Exercise the real Code command, FileStore restoration, ANSI rendering and
// /rollback picker. No model turn is needed: the fixture matches a compacted
// transcript whose original announce_plan is gone but whose completed snapshot
// precedes the implementation answer. Looking only at captured output would
// miss a plan appended beneath that answer, so assertions use current PTY cells.
func TestPlanHistoryOrderCodePTYAcceptance(t *testing.T) {
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
		if strings.HasSuffix(r.URL.Path, "/chat/completions") {
			modelCalls.Add(1)
		}
		http.Error(w, "history acceptance must not invoke a model", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	cfg := &config.AppConfig{
		General:   config.GeneralConfig{DefaultProvider: "fixture", DefaultModel: "pty-model"},
		Providers: map[string]config.ProviderConfig{"fixture": {"type": "openai_compat", "base_url": server.URL, "api_key": "fixture"}},
	}
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
	sum := sha256.Sum256([]byte(work))
	ctx := context.Background()
	created, err := store.Create(ctx, &adksession.CreateRequest{AppName: "astonish_code", UserID: fmt.Sprintf("local_user_%x", sum[:8])})
	if err != nil {
		t.Fatal(err)
	}
	const goal = "Historical completed plan"
	const implementation = "IMPLEMENTATION FINISHED: verified and ready."
	const laterUser = "LATER USER: explain the implementation"
	const laterAnswer = "LATER ANSWER: explanation delivered."
	snapshot := agent.PlanSnapshot{
		PlanID: "history-order-plan", Revision: 1, Sequence: 20, Goal: goal,
		Document: agent.PlanDocumentInfo{PlanID: "history-order-plan", Revision: 1, Sequence: 20, Lifecycle: agent.PlanLifecycleCompleted, Results: "All acceptance checks passed."},
		Steps: []agent.PlanStepInfo{
			{Name: "implement", Description: "Implement the requested change", Status: "complete", Evidence: "exit 0"},
			{Name: "verify", Description: "Verify the requested behavior", Status: "complete", Evidence: "exit 0"},
			{Name: "accept", Description: "Accept the finished implementation", Status: "complete", Evidence: "exit 0"},
		},
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	appendEvent := func(event *adksession.Event) {
		t.Helper()
		event.Timestamp = time.Now()
		if err := store.AppendEvent(ctx, created.Session, event); err != nil {
			t.Fatal(err)
		}
	}
	textEvent := func(id, author, text string) *adksession.Event {
		role := genai.Role(genai.RoleModel)
		if author == "user" {
			role = genai.RoleUser
		}
		return &adksession.Event{ID: id, Author: author, LLMResponse: adkmodel.LLMResponse{Content: genai.NewContentFromText(text, role)}}
	}
	appendEvent(textEvent("initial-request", "user", "Implement and verify the change."))
	// No announce_plan call or response and no PLAN.md: the state-only event
	// is the sole surviving chronological anchor for the completed plan.
	appendEvent(&adksession.Event{ID: "plan-state-20", Author: "system", Actions: adksession.EventActions{StateDelta: map[string]any{agent.PlanSnapshotStateKey: value}}})
	var answer strings.Builder
	for i := 1; i <= 22; i++ {
		fmt.Fprintf(&answer, "Verified implementation detail %02d.\n\n", i)
	}
	answer.WriteString(implementation)
	appendEvent(textEvent("implementation-answer", "agent", answer.String()))
	appendEvent(textEvent("later-user", "user", laterUser))
	appendEvent(textEvent("later-answer", "agent", laterAnswer))

	// Avoid optional CodeGraph package downloads, confined to this test HOME.
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
	term.wait("Enter send", laterAnswer)
	term.assertAbsent(goal)
	t.Log("resume bottom retains the later answer, not an appended plan")

	// Scroll upward through real terminal input until the original completed
	// plan is visible. This rules out fixing chronology by hiding all plans.
	scrollToPlan := func() {
		t.Helper()
		for i := 0; i < 50 && !strings.Contains(term.frame(), goal); i++ {
			before := term.frame()
			term.send("\x1b[<64;35;10M") // Mouse wheel up in the transcript.
			deadline := time.NewTimer(2 * time.Second)
			for term.frame() == before {
				select {
				case <-term.changed:
				case <-term.done:
					t.Fatalf("Code exited while scrolling:\n%s", term.frame())
				case <-deadline.C:
					t.Fatalf("Code did not scroll toward the earlier plan:\n%s", term.frame())
				}
			}
			deadline.Stop()
		}
		term.wait(goal)
	}
	scrollToPlan()
	term.send(strings.Repeat("\x1b[<65;35;10M", 100))
	term.wait(laterAnswer, "Enter send")

	// The picker defaults to the latest user turn. Confirm using actual keys;
	// rollback should retain implementation-answer and remove later-answer.
	term.send("/rollback\r")
	term.wait("Rollback", laterUser, "enter select")
	term.send("\r")
	term.wait("Revert to this message?", "y revert")
	term.send("y")
	term.wait("Rolled back.", implementation, "Enter send")
	term.assertAbsent(goal)
	term.assertAbsent(laterAnswer)
	t.Log("rollback bottom retains the implementation answer, not an appended plan")

	scrollToPlan()
	term.send(strings.Repeat("\x1b[<65;35;10M", 100)) // Mouse wheel down to the retained answer.
	term.wait(implementation, "Rolled back.")
	term.assertAbsent(goal)
	term.assertAbsent(laterAnswer)
	term.close()
	if calls := modelCalls.Load(); calls != 0 {
		t.Fatalf("history-only resume/rollback unexpectedly made %d model calls", calls)
	}
	t.Log("completed plan remains earlier in scrollback; resume and rollback preserve chronology at 72x26 with zero model calls")
}
