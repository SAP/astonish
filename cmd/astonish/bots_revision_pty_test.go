package astonish

import (
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
	"github.com/charmbracelet/x/ansi"
	"github.com/creack/pty"
	"github.com/google/uuid"
	adksession "google.golang.org/adk/session"
)

// Real Execute + Bubble Tea process. A deliberately delayed pre-commit header
// read cannot mask the gap by polling before the next native composer send.
func TestBotComposerRevisionPTYAcceptance(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("SUDO_USER", "")
	if err := os.MkdirAll(filepath.Join(home, "astonish"), 0700); err != nil {
		t.Fatal(err)
	}
	root := store.Bot{ID: uuid.New(), HomeSessionID: uuid.New(), Name: "RevisionBot", Scope: store.BotScopePersonal, Status: store.BotActive, Version: 2, Revision: 5}
	fresh := root
	fresh.Version = 9
	fresh.Revision = 14
	op := store.BotOperationView{SchemaVersion: 1, BotID: root.ID, HomeSessionID: root.HomeSessionID, OperationID: uuid.New(), Revision: 73, StateSequence: 81, Status: store.BotOperationApplied, Decision: store.BotOperationExecute, Summary: "Automatic revision verified."}
	base := "/api/studio/bots/" + root.ID.String()
	applied := make(chan struct{})
	firstRead := make(chan struct{})
	releaseOld := make(chan struct{})
	var release sync.Once
	defer release.Do(func() { close(releaseOld) })
	var reads, posts atomic.Int32
	requests := make(chan int64, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		write := func(v any) { w.Header().Set("Content-Type", "application/json"); _ = json.NewEncoder(w).Encode(v) }
		if r.Header.Get("Authorization") != "Bearer revision-token" {
			http.Error(w, "unauthorized", 401)
			return
		}
		if r.Method == http.MethodPost {
			if r.URL.Path != base+"/messages" {
				t.Errorf("unexpected mutation %s", r.URL)
				http.Error(w, "unexpected", 400)
				return
			}
			var q struct {
				Version int64  `json:"version"`
				Text    string `json:"instructions"`
				Key     string `json:"idempotency_key"`
			}
			if err := json.NewDecoder(r.Body).Decode(&q); err != nil {
				t.Error(err)
			}
			n := posts.Add(1)
			requests <- q.Version
			if q.Key == "" {
				t.Error("missing admission key")
			}
			if q.Version != 9 {
				http.Error(w, "stale composer version", 409)
				return
			}
			if n == 1 {
				if q.Text != "next revision instruction" {
					t.Errorf("wrong text %q", q.Text)
				}
				w.WriteHeader(http.StatusAccepted)
				write(store.BotMessageReceipt{})
			} else {
				// A concurrent owner edit after the client captured version 9: server CAS
				// still rejects it. This controlled HTTP fixture is not a storage test.
				http.Error(w, "Bot changed; reload and review before retrying.", http.StatusConflict)
			}
			return
		}
		switch r.URL.Path {
		case "/api/studio/sessions":
			write(map[string]any{"sessions": []any{}})
		case "/api/settings/providers/effective":
			write(map[string]any{})
		case "/api/studio/bots":
			write(map[string]any{"bots": []store.Bot{root}})
		case base:
			write(root)
		case base + "/snapshot":
			if reads.Add(1) == 1 {
				close(firstRead)
				select {
				case <-releaseOld:
					write(store.BotSnapshot{Bot: root})
				case <-r.Context().Done():
				}
				return
			}
			write(store.BotSnapshot{Bot: fresh, Operations: []store.BotOperationView{op}, HistoryCursor: 90})
		case base + "/sessions/" + root.HomeSessionID.String() + "/events":
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "event: replay_complete\ndata: {}\n\n")
			w.(http.Flusher).Flush()
			select {
			case <-applied:
			case <-r.Context().Done():
				return
			}
			row := adksession.Event{Author: "bot_runtime", Actions: adksession.EventActions{StateDelta: map[string]any{store.BotOperationKey: op}}}
			raw, _ := json.Marshal(row)
			fmt.Fprintf(w, "id: %s:1\nevent: transcript\ndata: %s\n\n", root.HomeSessionID, raw)
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		default:
			t.Errorf("unexpected route %s", r.URL)
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
	if err = tokens.Save(&client.Tokens{AccessToken: "revision-token"}); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestBotCLIProcess$", "--", "chat")
	cmd.Env = append(os.Environ(), "ASTONISH_BOT_CLI_CHILD=1", "TERM=xterm-256color", "NO_COLOR=1")
	pt, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 45, Cols: 140})
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
		until := time.Now().Add(8 * time.Second)
		for time.Now().Before(until) {
			if strings.Contains(read(), s) {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("missing %q: %s", s, read())
	}
	wait("platform")
	send("/bots\r")
	wait("RevisionBot [active]")
	send("\r")
	select {
	case <-firstRead:
	case <-time.After(5 * time.Second):
		t.Fatal("no initial snapshot")
	}
	close(applied)
	wait("Automatic revision verified.")
	send("next revision instruction\r")
	wait("Message accepted. Reply will appear here.")
	select {
	case v := <-requests:
		if v != 9 {
			t.Fatalf("version=%d", v)
		}
	case <-time.After(time.Second):
		t.Fatal("missing admission")
	}
	if reads.Load() != 2 {
		t.Fatalf("expected one poll plus reconciliation; reads=%d", reads.Load())
	}
	// Let the old header response arrive after reconciliation. It must not put
	// version 2 back into the next request.
	release.Do(func() { close(releaseOld) })
	wait("status unavailable")
	send("retain this conflict draft\r")
	wait("I can’t confirm the latest outcome right now. Please refresh before trying again; your work may still be running.")
	if feedback := read(); strings.Contains(feedback, "server error (") || strings.Contains(feedback, "Bot changed; reload and review before retrying.") {
		t.Fatalf("composer exposed raw conflict diagnostic: %s", feedback)
	}
	time.Sleep(200 * time.Millisecond)
	if posts.Load() != 2 {
		t.Fatalf("CAS auto-replayed: %d posts", posts.Load())
	}
	select {
	case v := <-requests:
		if v != 9 {
			t.Fatalf("late poll regressed version=%d", v)
		}
	case <-time.After(time.Second):
		t.Fatal("missing conflict request")
	}
	// Force a fresh render so the input check cannot match the submitted text's
	// earlier keystroke echo or error history.
	mark()
	if err := pty.Setsize(pt, &pty.Winsize{Rows: 45, Cols: 141}); err != nil {
		t.Fatal(err)
	}
	wait("retain this conflict draft")
	send("\x03")
	t.Log("Native Applied -> send version 9 / HTTP 202; late version-2 poll rejected; next HTTP 409 retained draft, exactly two POSTs, no replay.")
}
