package astonish

import (
	"encoding/json"
	"fmt"
	"github.com/SAP/astonish/pkg/client"
	"github.com/SAP/astonish/pkg/store"
	"github.com/charmbracelet/x/ansi"
	"github.com/creack/pty"
	"github.com/google/uuid"
	adksession "google.golang.org/adk/session"
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
)

func TestBotEmployeePTYAcceptance(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("SUDO_USER", "")
	if err := os.MkdirAll(filepath.Join(home, "astonish"), 0700); err != nil {
		t.Fatal(err)
	}
	bot := store.Bot{ID: uuid.New(), HomeSessionID: uuid.New(), Name: "Employee", Scope: store.BotScopePersonal, Status: store.BotActive, Version: 1, Revision: 1}
	op := store.BotOperationView{SchemaVersion: 1, BotID: bot.ID, HomeSessionID: bot.HomeSessionID, OperationID: uuid.New(), Revision: 1, StateSequence: 1, Status: store.BotOperationPending, Decision: store.BotOperationExecute, Summary: "Employee change queued."}
	exception := op
	exception.OperationID = uuid.New()
	exception.Status = store.BotOperationBlocked
	exception.Decision = store.BotOperationRequireAuthorization
	exception.Summary = "Employee exception needs review."
	exception.Exception = &store.BotOperationExceptionView{OperationID: exception.OperationID, PayloadDigest: "exact-digest", ExpiresAt: time.Now().Add(time.Hour)}
	base := "/api/studio/bots/" + bot.ID.String()
	var stage, decisions atomic.Int32
	current := func() []store.BotOperationView {
		a := op
		if stage.Load() > 0 {
			a.Status = store.BotOperationApplied
			a.Revision = 2
			a.StateSequence = 2
			a.Summary = "Employee change verified."
			a.VerifiedSchedule = &store.BotOperationScheduleView{Enabled: true}
		}
		out := []store.BotOperationView{a}
		if stage.Load() > 1 {
			e := exception
			if decisions.Load() > 0 {
				e.Status = store.BotOperationCancelled
				e.Decision = store.BotOperationDeny
				e.Revision = 2
				e.StateSequence = 2
				e.Exception = nil
				e.Summary = "Employee exception denied."
			}
			out = append(out, e)
		}
		return out
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		write := func(v any) { w.Header().Set("Content-Type", "application/json"); _ = json.NewEncoder(w).Encode(v) }
		if r.Header.Get("Authorization") != "Bearer employee-token" {
			http.Error(w, "unauthorized", 401)
			return
		}
		if r.Method == "POST" {
			if r.URL.Path != base+"/operations/"+exception.OperationID.String()+"/decision" {
				t.Errorf("unexpected mutation %s", r.URL)
				http.Error(w, "unexpected", 400)
				return
			}
			var q map[string]any
			_ = json.NewDecoder(r.Body).Decode(&q)
			if len(q) != 4 || q["payload_digest"] != "exact-digest" || q["expected_revision"] != float64(1) || q["approve"] != false {
				t.Errorf("unbound decision %#v", q)
			}
			decisions.Add(1)
			write(current()[1])
			return
		}
		switch r.URL.Path {
		case "/api/studio/sessions":
			write(map[string]any{"sessions": []any{}})
		case "/api/settings/providers/effective":
			write(map[string]any{})
		case "/api/studio/bots":
			write(map[string]any{"bots": []store.Bot{bot}})
		case base:
			write(bot)
		case base + "/snapshot":
			write(store.BotSnapshot{Bot: bot, Operations: current()})
		case base + "/sessions/" + bot.HomeSessionID.String() + "/events":
			w.Header().Set("Content-Type", "text/event-stream")
			seq := 0
			emit := func() {
				for _, v := range current() {
					seq++
					row := adksession.Event{Author: "bot_runtime", Actions: adksession.EventActions{StateDelta: map[string]any{store.BotOperationKey: v}}}
					raw, _ := json.Marshal(row)
					fmt.Fprintf(w, "id: %s:%d\nevent: transcript\ndata: %s\n\n", bot.HomeSessionID, seq, raw)
				}
				w.(http.Flusher).Flush()
			}
			emit()
			fmt.Fprintf(w, "event: replay_complete\ndata: {}\n\n")
			w.(http.Flusher).Flush()
			ticker := time.NewTicker(100 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-r.Context().Done():
					return
				case <-ticker.C:
					emit()
				}
			}
		default:
			t.Errorf("unexpected endpoint %s", r.URL)
			http.Error(w, "unexpected", 404)
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
	if err = tokens.Save(&client.Tokens{AccessToken: "employee-token"}); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestBotCLIProcess$", "--", "chat")
	cmd.Env = append(os.Environ(), "ASTONISH_BOT_CLI_CHILD=1", "TERM=xterm-256color", "NO_COLOR=1")
	pt, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 45, Cols: 140})
	if err != nil {
		t.Fatal(err)
	}
	defer pt.Close()
	defer cmd.Process.Kill()
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
	start := 0
	read := func() string { mu.Lock(); defer mu.Unlock(); return ansi.Strip(output.String()[start:]) }
	mark := func() { mu.Lock(); start = output.Len(); mu.Unlock() }
	send := func(s string) {
		mark()
		if _, e := io.WriteString(pt, s); e != nil {
			t.Fatal(e)
		}
	}
	wait := func(s string) {
		t.Helper()
		until := time.Now().Add(10 * time.Second)
		for time.Now().Before(until) {
			if strings.Contains(read(), s) {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("missing %q: %s", s, read())
	}
	frame := func(cols uint16) string {
		time.Sleep(150 * time.Millisecond)
		mark()
		_ = pty.Setsize(pt, &pty.Winsize{Rows: 45, Cols: cols})
		time.Sleep(200 * time.Millisecond)
		return read()
	}
	wait("platform")
	send("/bots\r")
	wait("Employee [active]")
	send("\r")
	wait("Employee change queued.")
	if s := frame(141); strings.Contains(s, "approve once") || strings.Contains(s, "Confirm approved") {
		t.Fatalf("routine review: %s", s)
	}
	stage.Store(1)
	wait("Employee change verified.")
	s := frame(142)
	if strings.Count(s, "Employee change verified.") != 1 || strings.Contains(s, "Employee change queued.") || !strings.Contains(s, "Schedule enabled.") {
		t.Fatalf("receipt mismatch %s", s)
	}
	stage.Store(2)
	wait("Employee exception needs review.")
	send("/bots exceptions\r")
	wait("[Cancel]")
	send("\r")
	time.Sleep(150 * time.Millisecond)
	if decisions.Load() != 0 {
		t.Fatal("default enter mutated")
	}
	send("/bots exceptions\r")
	wait("[Cancel]")
	send("y")
	wait("Confirm approved")
	send("\x1b")
	time.Sleep(200 * time.Millisecond)
	if decisions.Load() != 0 {
		t.Fatal("escape mutated")
	}
	send("/bots exceptions\r")
	wait("[Cancel]")
	send("n")
	wait("Confirm denied")
	send("\r")
	wait("No pending authorization exceptions.")
	send("\x1b")
	time.Sleep(200 * time.Millisecond)
	send("/bots home\r")
	wait("Employee exception denied.")
	s = frame(143)
	if strings.Count(s, "Employee exception denied.") != 1 || strings.Contains(s, "Authorization exception:") {
		t.Fatalf("reload resurrected exception %s", s)
	}
	send("/bots exceptions\r")
	wait("No pending authorization exceptions.")
	send("n\r")
	time.Sleep(150 * time.Millisecond)
	if decisions.Load() != 1 {
		t.Fatalf("repeated decision %d", decisions.Load())
	}
}
