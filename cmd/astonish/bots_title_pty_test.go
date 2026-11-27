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
	"testing"
	"time"
)

// Exercises the real command process and terminal; no model/provider credentials.
func TestBotTitleChatPTYAcceptance(t *testing.T) {
	for _, mode := range []string{"option", "custom"} {
		t.Run(mode, func(t *testing.T) { testBotTitleChatPTYAnswer(t, mode) })
	}
}

// The HTTP fixture supplies a persisted ask_user interaction, not a fabricated
// completed assignment. Production worker execution is covered by HTTP acceptance.
func testBotTitleChatPTYAnswer(t *testing.T, mode string) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("SUDO_USER", "")
	if err := os.MkdirAll(home+"/astonish", 0700); err != nil {
		t.Fatal(err)
	}
	bot := store.Bot{ID: uuid.New(), HomeSessionID: uuid.New(), Name: "Title draft", Scope: store.BotScopePersonal, Status: store.BotActive, Version: 1, Revision: 1}
	base := "/api/studio/bots/" + bot.ID.String()
	state := store.BotRunState{SchemaVersion: 1, BotID: bot.ID, RunID: uuid.New(), HomeSessionID: bot.HomeSessionID, SessionID: uuid.New(), Presentation: store.BotRunPresentationHomeInteractive, Status: store.RunWaitingApproval, StateSequence: 3}
	answers := make(chan struct{}, 2)
	wantAnswer := "forecast"
	if mode == "custom" {
		wantAnswer = "Weekend rain"
	}
	approval := store.BotApproval{ID: uuid.New(), BotID: bot.ID, RunID: state.RunID, ActionDigest: "title-action", Status: store.ApprovalPending, ExpiresAt: time.Now().Add(time.Hour), Interaction: &store.BotInteraction{Kind: store.BotInteractionQuestion, QuestionID: "title-q", Payload: json.RawMessage(`{"questionId":"title-q","kind":"select","prompt":"What should I do about Boston weather?","options":[{"id":"forecast","label":"Compare Boston forecasts"},{"id":"climate","label":"Explain Boston seasonal weather"},{"id":"outing","label":"Plan a Boston outing"}],"allow_custom_answer":true}`)}}
	if err := approval.Interaction.Validate(); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer title-token" {
			http.Error(w, "unauthorized", 401)
			return
		}
		write := func(v any) { _ = json.NewEncoder(w).Encode(v) }
		switch r.URL.Path {
		case "/api/studio/bots":
			write(map[string]any{"bots": []store.Bot{bot}})
		case base:
			write(bot)
		case base + "/snapshot":
			write(store.BotSnapshot{Bot: bot, RunStates: []store.BotRunState{state}, PendingApprovals: []store.BotApproval{approval}})
		case base + "/runs":
			write(map[string]any{"runs": []store.BotRun{{ID: state.RunID, BotID: bot.ID, ExecutionMode: execution.BotExecutionModeChatInteractive}}})
		case base + "/approvals/" + approval.ID.String() + "/approve-and-continue":
			var request struct {
				ActionDigest string                        `json:"action_digest"`
				Response     *store.BotInteractionResponse `json:"response"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
			}
			if r.Method != "POST" || request.ActionDigest != approval.ActionDigest || request.Response == nil || request.Response.QuestionID != "title-q" || request.Response.Answer != wantAnswer {
				t.Errorf("unbound answer: %+v", request)
			}
			write(store.BotApproveContinueResult{RunID: state.RunID, Continuation: store.BotContinuationQueued})
			answers <- struct{}{}
		case base + "/sessions/" + bot.HomeSessionID.String() + "/events":
			w.Header().Set("Content-Type", "text/event-stream")
			rows := []session.Event{{Actions: session.EventActions{StateDelta: map[string]any{store.BotRunStateKey: state}}}, {Author: "model"}}
			var args map[string]any
			_ = json.Unmarshal(approval.Interaction.Payload, &args)
			rows[1].Content = &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: "title-call", Name: "ask_user", Args: args}}}}
			for i, row := range rows {
				raw, _ := json.Marshal(row)
				fmt.Fprintf(w, "id: %s:%d\nevent: transcript\ndata: %s\n\n", bot.HomeSessionID, i+1, raw)
			}
			w.(http.Flusher).Flush()
			<-r.Context().Done()
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
	if err = tokens.Save(&client.Tokens{AccessToken: "title-token"}); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestBotCLIProcess$", "--", "chat", "--bot", bot.ID.String())
	cmd.Env = append(os.Environ(), "ASTONISH_BOT_CLI_CHILD=1", "TERM=xterm-256color", "NO_COLOR=1")
	pt, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 40, Cols: 140})
	if err != nil {
		t.Fatal(err)
	}
	defer pt.Close()
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
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
	// Open and reload the actual durable decision, rather than inventing a final
	// report while the title is still waiting for an owner answer.
	wait("Waiting for answer or review")
	_, _ = io.WriteString(pt, "/bots approvals\r")
	wait("What should I do about Boston weather?")
	wait("Compare Boston forecasts")
	wait("Custom answer:")
	_, _ = io.WriteString(pt, "\x1b")
	time.Sleep(300 * time.Millisecond)
	mu.Lock()
	output.Reset()
	mu.Unlock()
	_, _ = io.WriteString(pt, "/bots approvals\r")
	wait("What should I do about Boston weather?")
	wait("Explain Boston seasonal weather")
	_, _ = io.WriteString(pt, "y")
	if mode == "custom" {
		_, _ = io.WriteString(pt, "\x1b[B\x1b[B\x1b[B"+wantAnswer)
	}
	wait("Enter submits answer")
	select {
	case <-answers:
		t.Fatal("answer posted before Enter")
	default:
	}
	_, _ = io.WriteString(pt, "\r")
	select {
	case <-answers:
	case <-time.After(5 * time.Second):
		t.Fatal("answer did not use original-authority approval continuation")
	}
}
