package astonish

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/client"
	"github.com/SAP/astonish/pkg/store"
	"github.com/creack/pty"
	"github.com/google/uuid"
	adksession "google.golang.org/adk/session"
	"google.golang.org/genai"
)

// This helper executes the real command dispatcher in a separate process. PTY
// acceptance below drives its real Bubble Tea input and rendered terminal bytes.
func TestBotCLIProcess(t *testing.T) {
	if os.Getenv("ASTONISH_BOT_CLI_CHILD") != "1" {
		return
	}
	for i, a := range os.Args {
		if a == "--" {
			os.Args = append([]string{"astonish"}, os.Args[i+1:]...)
			break
		}
	}
	if err := Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}
func TestBotChatFlagBoundaries(t *testing.T) {
	for _, args := range [][]string{{"--bot", "x", "--resume", "s"}, {"--bot", "x", "--auto-approve"}, {"--bot", "x", "--model", "m"}, {"--bot", "x", "--bot-scope", "bad"}} {
		if _, err := parseChatFlags(args); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	f, err := parseChatFlags([]string{"--bot", "Monitor", "--bot-scope", "team"})
	if err != nil || f.Bot != "Monitor" || f.BotScope != "team" {
		t.Fatalf("%+v %v", f, err)
	}
	if !shouldLaunchBareCodeWith(true, true) || shouldLaunchBareCodeWith(false, true) {
		t.Fatal("bare code boundary changed")
	}
}
func TestBotCLIAndPTYAcceptance(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("SUDO_USER", "")
	if err := os.MkdirAll(filepath.Join(home, "astonish"), 0700); err != nil {
		t.Fatal(err)
	}
	id, sid, rid, aid := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	runSID := uuid.New()
	var runWatched atomic.Int32
	var cursorLeaks atomic.Int32
	var typedAnswer atomic.Bool
	var mu sync.Mutex
	bot := store.Bot{ID: id, Name: "Monitor", Scope: store.BotScopePersonal, Status: store.BotActive, Revision: 1, Version: 1, HomeSessionID: sid}
	approval := store.BotApproval{ID: aid, BotID: id, RunID: rid, Version: 1, ActionDigest: "reviewed-digest", Status: store.ApprovalPending, ExpiresAt: time.Now().Add(time.Hour)}
	var mutations atomic.Int32
	var watched atomic.Int32
	var authFailures atomic.Int32
	var listState atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer acceptance-token" {
			authFailures.Add(1)
			http.Error(w, "unauthorized", 401)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/studio/bots") && r.URL.Query().Get("scope") != "personal" {
			http.Error(w, "forbidden", 403)
			return
		}
		if r.Method != "GET" {
			mutations.Add(1)
		}
		write := func(v any) { w.Header().Set("Content-Type", "application/json"); _ = json.NewEncoder(w).Encode(v) }
		if strings.HasSuffix(r.URL.Path, "/events") {
			watched.Add(1)
			w.Header().Set("Content-Type", "text/event-stream")
			e := adksession.Event{Author: "agent"}
			e.Partial = true // observer commands must work while daemon text streams
			e.Content = &genai.Content{Role: "model", Parts: []*genai.Part{{Text: "Durable terminal report"}}}
			feedSID := sid
			if strings.Contains(r.URL.Path, runSID.String()) {
				feedSID = runSID
				runWatched.Add(1)
				e.Content.Parts[0].Text = "Selected run trace report"
			}
			if r.URL.Query().Get("after") != "" {
				cursorLeaks.Add(1)
			}
			raw, _ := json.Marshal(e)
			fmt.Fprintf(w, "id: %s:1\nevent: transcript\ndata: %s\n\nid: %s:1\nevent: replay_complete\ndata: {}\n\n", feedSID, raw, feedSID)
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.URL.Path == "/api/studio/bots" && r.Method == "GET":
			if listState.Load() == 1 {
				write(map[string]any{"bots": []store.Bot{}})
			} else if listState.Load() == 2 {
				http.Error(w, "forbidden", 403)
			} else {
				write(map[string]any{"bots": []store.Bot{bot}})
			}
		case r.URL.Path == "/api/studio/bots" && r.Method == "POST":
			var q client.BotDraft
			if json.NewDecoder(r.Body).Decode(&q) != nil || q.Definition.Charter == "" || q.IdempotencyKey == "" {
				http.Error(w, "bad draft", 400)
				return
			}
			draft := bot
			draft.Name = q.Name
			draft.Status = store.BotDraft
			write(draft)
		case r.URL.Path == "/api/studio/bots/"+id.String()+"/snapshot" && r.Method == "GET":
			snap := store.BotSnapshot{Bot: bot}
			if approval.Status == store.ApprovalPending {
				snap.PendingApprovals = []store.BotApproval{approval}
			}
			write(snap)
		case r.URL.Path == "/api/studio/bots/"+id.String() && r.Method == "GET":
			write(bot)
		case strings.HasSuffix(r.URL.Path, "/approvals") && r.Method == "GET":
			write(map[string]any{"approvals": []store.BotApproval{approval}})
		case strings.HasSuffix(r.URL.Path, "/decision"), strings.HasSuffix(r.URL.Path, "/approve-and-continue"):
			atomicContinue := strings.HasSuffix(r.URL.Path, "/approve-and-continue")
			var q client.BotDecision
			_ = json.NewDecoder(r.Body).Decode(&q)
			if atomicContinue {
				q.Status = store.ApprovalApproved
			}
			if q.ActionDigest != approval.ActionDigest || approval.Status != store.ApprovalPending {
				http.Error(w, "Bot changed; reload and review", 409)
				return
			}
			if approval.Interaction != nil {
				if approval.Interaction.ValidateResponse(q.Response) != nil {
					http.Error(w, "invalid answer", 400)
					return
				}
				typedAnswer.Store(true)
			}
			approval.Status = q.Status
			if atomicContinue {
				write(store.BotApproveContinueResult{Approval: approval, RunID: rid, Continuation: store.BotContinuationBlockedByPause})
			} else {
				write(approval)
			}
		case strings.HasSuffix(r.URL.Path, "/runs") && r.Method == "GET":
			write(map[string]any{"runs": []store.BotRun{{ID: rid, BotID: id, SessionID: runSID, Status: store.RunQueued}}})
		case strings.HasSuffix(r.URL.Path, "/runs") && r.Method == "POST":
			var q map[string]any
			_ = json.NewDecoder(r.Body).Decode(&q)
			if q["version"] != float64(1) || q["idempotency_key"] == "" {
				http.Error(w, "bad run", 400)
				return
			}
			write(store.BotRun{ID: rid, BotID: id, SessionID: sid, Status: store.RunQueued})
		case strings.HasSuffix(r.URL.Path, "/resume"):
			var q map[string]string
			_ = json.NewDecoder(r.Body).Decode(&q)
			if q["approval_id"] != aid.String() {
				http.Error(w, "wrong approval", 400)
				return
			}
			w.WriteHeader(202)
		case r.Method == "POST" || r.Method == "DELETE":
			var q struct {
				Revision int64  `json:"expected_revision"`
				Reason   string `json:"reason"`
			}
			_ = json.NewDecoder(r.Body).Decode(&q)
			if q.Revision != bot.Revision || q.Reason == "" {
				http.Error(w, "Bot changed; reload and review", 409)
				return
			}
			bot.Revision++
			switch {
			case strings.HasSuffix(r.URL.Path, "/pause"):
				bot.Status = store.BotPaused
			case strings.HasSuffix(r.URL.Path, "/activate"):
				bot.Status = store.BotActive
			case strings.HasSuffix(r.URL.Path, "/stop"):
				bot.Status = store.BotStopped
			case strings.HasSuffix(r.URL.Path, "/restore"):
				bot.Status = store.BotPaused
			case r.Method == "DELETE":
				bot.Status = store.BotDeleted
			}
			write(bot)
		case r.Method == "GET" && r.URL.Path == "/api/studio/sessions":
			write(map[string]any{"sessions": []any{}})
		case r.Method == "GET" && r.URL.Path == "/api/settings/providers/effective":
			write(map[string]any{})
		default:
			t.Errorf("unexpected fixture endpoint: %s %s", r.Method, r.URL)
			http.Error(w, "unexpected endpoint", http.StatusNotFound)
		}
	}))
	defer server.Close()
	if err := client.SaveRemoteConfig(&client.RemoteConfig{URL: server.URL}); err != nil {
		t.Fatal(err)
	}
	ts, err := client.NewTokenStore()
	if err != nil {
		t.Fatal(err)
	}
	if err = ts.Save(&client.Tokens{AccessToken: "acceptance-token"}); err != nil {
		t.Fatal(err)
	}
	child := func(args ...string) *exec.Cmd {
		cmd := exec.Command(os.Args[0], append([]string{"-test.run=^TestBotCLIProcess$", "--"}, args...)...)
		cmd.Env = append(os.Environ(), "ASTONISH_BOT_CLI_CHILD=1", "TERM=xterm-256color", "NO_COLOR=1")
		return cmd
	}
	run := func(want string, args ...string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		cmd := child(args...)
		bounded := exec.CommandContext(ctx, cmd.Path, cmd.Args[1:]...)
		bounded.Env = cmd.Env
		out, err := bounded.CombinedOutput()
		if err != nil || !strings.Contains(string(out), want) {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}
	run("Monitor", "bots", "list", "--json")
	run("draft", "bots", "create", "draft", "Draft", "--charter", "Observe only", "--json")
	run(id.String(), "bots", "show", "Monitor", "--json")
	run("queued", "bots", "run", "Monitor", "--json")
	run("pending", "bots", "approvals", "Monitor", "--json")
	run("Durable terminal report", "bots", "logs", "Monitor", "--json")
	for i, action := range []string{"pause", "resume", "stop", "delete", "restore"} {
		want := []string{"paused", "active", "stopped", "deleted", "paused"}[i]
		run(want, "bots", action, "Monitor", "--revision", fmt.Sprint(i+1), "--reason", "reviewed", "--json")
	}
	bad := child("bots", "pause", "Monitor", "--revision", "1", "--reason", "stale")
	if out, err := bad.CombinedOutput(); err == nil || !strings.Contains(string(out), "409") {
		t.Fatalf("stale mutation %v %s", err, out)
	}
	denied := child("bots", "list", "--scope", "team")
	if out, err := denied.CombinedOutput(); err == nil || !strings.Contains(string(out), "403") {
		t.Fatalf("scope denial %v %s", err, out)
	}
	before := mutations.Load()
	cmd := child("chat", "--bot", "Monitor")
	pt, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 45, Cols: 130})
	if err != nil {
		t.Fatal(err)
	}
	defer pt.Close()
	defer func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	}()
	var outputMu sync.Mutex
	var output strings.Builder
	go func() {
		var terminal botPTYTerminal
		buf := make([]byte, 8192)
		for {
			n, err := pt.Read(buf)
			if n > 0 {
				if responseErr := terminal.respond(pt, buf[:n]); responseErr != nil {
					t.Errorf("PTY terminal response: %v", responseErr)
					return
				}
				outputMu.Lock()
				output.Write(buf[:n])
				outputMu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()
	// Match only output produced after the latest input, so repeated overlays
	// cannot satisfy waits with stale frames from a previous visit.
	outputStart := 0
	wait := func(text string) {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			outputMu.Lock()
			s := output.String()[outputStart:]
			outputMu.Unlock()
			if strings.Contains(s, text) {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		outputMu.Lock()
		defer outputMu.Unlock()
		t.Fatalf("PTY missing %q:\n%s", text, output.String())
	}
	send := func(text string) {
		t.Helper()
		outputMu.Lock()
		outputStart = output.Len()
		outputMu.Unlock()
		if _, err := io.WriteString(pt, text); err != nil {
			t.Fatal(err)
		}
	}
	wait("Durable terminal report")
	send("/bots runs\r")
	wait("read-only traces")
	wait(runSID.String())
	send("\r")
	wait("Selected run trace report")
	homeWatchBefore := watched.Load()
	send("/bots home\r")
	homeDeadline := time.Now().Add(10 * time.Second)
	for watched.Load() <= homeWatchBefore && time.Now().Before(homeDeadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if watched.Load() <= homeWatchBefore || runWatched.Load() != 1 || cursorLeaks.Load() != 0 {
		t.Fatal("run/home subscription not fresh")
	}
	time.Sleep(150 * time.Millisecond)
	send("/bots approvals\r")
	wait("reviewed-digest")
	send("y")
	wait("Confirm approved")
	send("\r")
	deadline := time.Now().Add(10 * time.Second)
	for {
		mu.Lock()
		approved := approval.Status == store.ApprovalApproved
		mu.Unlock()
		if approved {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("PTY did not persist the exact approval")
		}
		time.Sleep(20 * time.Millisecond)
	}
	send("\x1b")
	time.Sleep(150 * time.Millisecond)
	mu.Lock()
	approval.Status = store.ApprovalPending
	approval.Interaction = &store.BotInteraction{Kind: store.BotInteractionQuestion, QuestionID: "pty-q", Payload: json.RawMessage(`{"questionId":"pty-q","kind":"yesno","prompt":"Continue safely?"}`)}
	mu.Unlock()
	send("/bots approvals\r")
	wait("Continue safely?")
	send("y")
	wait("choose answer")
	send("\r")
	wait("Confirm answer")
	send("\x1b")
	time.Sleep(150 * time.Millisecond)
	if typedAnswer.Load() {
		t.Fatal("Escape submitted answer")
	}
	send("/bots approvals\r")
	wait("Continue safely?")
	send("y")
	wait("choose answer")
	send("\r")
	wait("Confirm answer")
	send("\r")
	answerDeadline := time.Now().Add(10 * time.Second)
	for !typedAnswer.Load() && time.Now().Before(answerDeadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !typedAnswer.Load() {
		t.Fatal("typed answer not persisted")
	}
	time.Sleep(150 * time.Millisecond)
	send("\x1b")
	time.Sleep(150 * time.Millisecond)
	send("/bots\r")
	wait("separate from chat sessions")
	// The title is also rendered during Loading; only the populated row proves
	// Enter can select rather than being deliberately ignored by the UI.
	wait("Monitor [paused]")
	watchBefore := watched.Load()
	send("\r")
	deadline = time.Now().Add(10 * time.Second)
	for watched.Load() == watchBefore {
		if time.Now().After(deadline) {
			t.Fatal("PTY picker did not select/watch Bot")
		}
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(150 * time.Millisecond)
	listState.Store(1)
	send("/bots\r")
	wait("No Bots.")
	send("\x1b")
	time.Sleep(150 * time.Millisecond)
	listState.Store(2)
	send("/bots\r")
	// The picker shows safe uncertainty feedback, not the raw HTTP denial.
	wait("I can’t confirm the latest outcome right now. Please refresh before trying again;")
	outputMu.Lock()
	denialOutput := output.String()[outputStart:]
	outputMu.Unlock()
	if strings.Contains(denialOutput, "forbidden") {
		t.Fatal("PTY leaked raw permission-denial diagnostics")
	}
	send("\x1b")
	time.Sleep(150 * time.Millisecond)
	listState.Store(0)
	send("/bots detach\r")
	wait("Bot work continues")
	send("/exit\r")
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("PTY did not exit")
	}
	if mutations.Load() != before+2 {
		t.Fatalf("detach/watch issued mutation: before=%d after=%d", before, mutations.Load())
	}
	if watched.Load() < 2 {
		t.Fatal("CLI and PTY did not request feed")
	}
	run("accepted", "bots", "resume", "Monitor", "--run", rid.String(), "--approval", aid.String(), "--json")
	mu.Lock()
	approval.Status = store.ApprovalPending
	approval.Interaction = nil
	mu.Unlock()
	run("denied", "bots", "approval", "deny", "Monitor", aid.String(), "--digest", "reviewed-digest", "--json")
	if err = ts.Save(&client.Tokens{AccessToken: "invalid"}); err != nil {
		t.Fatal(err)
	}
	bad = child("bots", "list")
	if out, err := bad.CombinedOutput(); err == nil || !strings.Contains(string(out), "login") {
		t.Fatalf("auth failure %v %s", err, out)
	}
	if authFailures.Load() == 0 {
		t.Fatal("auth not exercised")
	}
	if err = client.RemoveRemoteConfig(); err != nil {
		t.Fatal(err)
	}
	bad = child("bots", "list")
	if out, err := bad.CombinedOutput(); err == nil || !strings.Contains(string(out), "login") {
		t.Fatalf("local gating %v %s", err, out)
	}
	run("code", "code", "--help")
}
