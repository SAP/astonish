package astonish

import (
	"encoding/json"
	"fmt"
	"github.com/SAP/astonish/pkg/client"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/charmbracelet/x/ansi"
	"github.com/creack/pty"
	"github.com/google/uuid"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// This is a real CLI process in a PTY against a controlled authenticated HTTP/SSE
// fixture. It verifies display and owner controls, not provider/worker execution.
func TestBotProgressPTYAcceptance(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) { testBotProgressPTY(t, scope) })
	}
}
func testBotProgressPTY(t *testing.T, scope store.BotScope) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("SUDO_USER", "")
	if err := os.MkdirAll(home+"/astonish", 0700); err != nil {
		t.Fatal(err)
	}
	bot := store.Bot{ID: uuid.New(), HomeSessionID: uuid.New(), Name: "Progress employee", Scope: scope, Status: store.BotPaused, Version: 1, Revision: 4}
	state := store.BotRunState{SchemaVersion: 1, BotID: bot.ID, RunID: uuid.New(), SessionID: uuid.New(), HomeSessionID: bot.HomeSessionID, Presentation: store.BotRunPresentationHomeInteractive, Status: store.RunRunning, StateSequence: 1, CommittedAt: time.Now()}
	state.Progress = &store.BotProgressProjection{SchemaVersion: 1, BotID: bot.ID, RunID: state.RunID, SessionID: state.SessionID, Revision: 7, Phase: store.BotProgressEvaluating}
	base := "/api/studio/bots/" + bot.ID.String()
	var fixtureMu sync.Mutex
	rows := []session.Event{{Actions: session.EventActions{StateDelta: map[string]any{store.BotRunStateKey: state}}}}
	// A raw autonomous tool must not appear in the terminal.
	toolRow := session.Event{Author: "model"}
	toolRow.Content = &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{Name: "private_tool_should_not_render", Args: map[string]any{"secret": "private-argument"}}}}}
	rows = append(rows, toolRow)
	wake := make(chan struct{}, 8)
	var posts atomic.Int32
	snapshot := func() store.BotSnapshot {
		fixtureMu.Lock()
		defer fixtureMu.Unlock()
		return store.BotSnapshot{Bot: bot, RunStates: []store.BotRunState{state}}
	}
	publish := func(phase store.BotProgressPhase, status store.RunStatus, final bool) {
		fixtureMu.Lock()
		state.StateSequence++
		state.Status = status
		state.CommittedAt = time.Now()
		state.Progress = state.Progress.Clone()
		state.Progress.Phase = phase
		if phase == store.BotProgressSuspended {
			state.Progress.Reason = store.BotProgressReasonPaused
		} else {
			state.Progress.Reason = ""
		}
		rows = append(rows, session.Event{Actions: session.EventActions{StateDelta: map[string]any{store.BotRunStateKey: state}}})
		if final {
			e := session.Event{Author: "model"}
			e.Content = genai.NewContentFromText("The reviewed assignment result is ready.", "model")
			e.InvocationID = "bot-report:" + state.RunID.String()
			rows = append(rows, e)
		}
		fixtureMu.Unlock()
		wake <- struct{}{}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/studio/bots") && (r.Header.Get("Authorization") != "Bearer progress-token" || r.URL.Query().Get("scope") != string(scope)) {
			http.Error(w, "forbidden", 403)
			return
		}
		write := func(v any) { _ = json.NewEncoder(w).Encode(v) }
		switch r.URL.Path {
		case "/api/studio/bots":
			write(map[string]any{"bots": []store.Bot{bot}})
		case base:
			write(bot)
		case base + "/snapshot":
			write(snapshot())
		case base + "/runs":
			s := snapshot().RunStates[0]
			write(map[string]any{"runs": []store.BotRun{{ID: s.RunID, BotID: bot.ID, SessionID: s.SessionID, Status: s.Status, ExecutionMode: execution.BotExecutionModeChatInteractive, Progress: s.Progress}}})
		case base + "/runs/" + state.RunID.String() + "/continue-interactive":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if r.Method != "POST" || len(body) != 4 || body["kind"] != "progress_v1" || body["checkpoint_revision"] != float64(7) || body["expected_revision"] != float64(4) || body["idempotency_key"] == "" {
				t.Errorf("unbound progress request: %#v", body)
				http.Error(w, "bad", 400)
				return
			}
			posts.Add(1)
			w.WriteHeader(202)
			write(store.BotRun{ID: state.RunID, BotID: bot.ID, Status: store.RunQueued})
			publish(store.BotProgressWorking, store.RunRunning, false)
		case base + "/sessions/" + bot.HomeSessionID.String() + "/events":
			w.Header().Set("Content-Type", "text/event-stream")
			sent := 0
			for {
				fixtureMu.Lock()
				pending := append([]session.Event(nil), rows[sent:]...)
				fixtureMu.Unlock()
				for _, row := range pending {
					sent++
					raw, _ := json.Marshal(row)
					fmt.Fprintf(w, "id: %s:%d\nevent: transcript\ndata: %s\n\n", bot.HomeSessionID, sent, raw)
				}
				w.(http.Flusher).Flush()
				select {
				case <-r.Context().Done():
					return
				case <-wake:
				}
			}
		case "/api/studio/sessions":
			write(map[string]any{"sessions": []any{}})
		case "/api/settings/providers/effective":
			write(map[string]any{})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	if err := client.SaveRemoteConfig(&client.RemoteConfig{URL: server.URL}); err != nil {
		t.Fatal(err)
	}
	tokens, err := client.NewTokenStore()
	if err != nil {
		t.Fatal(err)
	}
	if err = tokens.Save(&client.Tokens{AccessToken: "progress-token"}); err != nil {
		t.Fatal(err)
	}
	start := func() (*os.File, func(string), func(), func()) {
		cmd := exec.Command(os.Args[0], "-test.run=^TestBotCLIProcess$", "--", "chat", "--bot", bot.ID.String(), "--bot-scope", string(scope))
		cmd.Env = append(os.Environ(), "ASTONISH_BOT_CLI_CHILD=1", "TERM=xterm-256color", "NO_COLOR=1")
		pt, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 40, Cols: 160})
		if err != nil {
			t.Fatal(err)
		}
		var mu sync.Mutex
		var output strings.Builder
		go func() {
			var terminal botPTYTerminal
			buf := make([]byte, 8192)
			for {
				n, e := pt.Read(buf)
				if n > 0 {
					_ = terminal.respond(pt, buf[:n])
					mu.Lock()
					output.Write(buf[:n])
					mu.Unlock()
				}
				if e != nil {
					return
				}
			}
		}()
		wait := func(text string) {
			t.Helper()
			until := time.Now().Add(12 * time.Second)
			for time.Now().Before(until) {
				mu.Lock()
				s := ansi.Strip(output.String())
				mu.Unlock()
				if strings.Contains(s, text) {
					return
				}
				time.Sleep(20 * time.Millisecond)
			}
			mu.Lock()
			defer mu.Unlock()
			t.Fatalf("missing %q: %s", text, ansi.Strip(output.String()))
		}
		private := func() {
			mu.Lock()
			defer mu.Unlock()
			if strings.Contains(output.String(), "private_tool_should_not_render") || strings.Contains(output.String(), "private-argument") {
				t.Fatal("autonomous trace leaked")
			}
		}
		stop := func() { _ = cmd.Process.Kill(); _ = cmd.Wait(); _ = pt.Close() }
		return pt, wait, private, stop
	}
	pt, wait, private, stop := start()
	defer func() {
		if stop != nil {
			stop()
		}
	}()
	wait("Checking progress")
	publish(store.BotProgressSuspended, store.RunInterrupted, false)
	wait("Paused")
	_, _ = io.WriteString(pt, "/bots continue\r")
	wait("Explicit continuation: Enter reviews")
	_, _ = io.WriteString(pt, "\r")
	wait("Continue saved progress checkpoint 7 at Bot revision 4?")
	if posts.Load() != 0 {
		t.Fatal("review mutated")
	}
	_, _ = io.WriteString(pt, "\x1b")
	time.Sleep(300 * time.Millisecond)
	if posts.Load() != 0 {
		t.Fatal("Escape mutated")
	}
	_, _ = io.WriteString(pt, "/bots continue\r")
	time.Sleep(300 * time.Millisecond)
	_, _ = io.WriteString(pt, "\r")
	time.Sleep(150 * time.Millisecond)
	if posts.Load() != 0 {
		t.Fatal("reopen mutated")
	}
	_, _ = io.WriteString(pt, "\r")
	wait("No approvals.")
	_, _ = io.WriteString(pt, "\x1b")
	wait("Working")
	if posts.Load() != 1 {
		t.Fatalf("continuation count %d", posts.Load())
	}
	publish(store.BotProgressSynthesizing, store.RunRunning, false)
	wait("Preparing result")
	publish(store.BotProgressSynthesizing, store.RunSucceeded, true)
	wait("The reviewed assignment result is ready.")
	private()
	stop()
	stop = nil
	_, wait, private, stop = start()
	wait("The reviewed assignment result is ready.")
	private()
	if posts.Load() != 1 {
		t.Fatal("restore resumed work")
	}
}
