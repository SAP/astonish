package astonish

import (
	"encoding/json"
	"fmt"
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

	"github.com/SAP/astonish/pkg/client"
	"github.com/SAP/astonish/pkg/store"
	"github.com/charmbracelet/x/ansi"
	"github.com/creack/pty"
	"github.com/google/uuid"
)

// Real remote command child and PTY; HTTP responses are controlled contract
// fixtures, not proof of daemon scheduling or model quality.
func TestBotSkillsPTYAcceptance(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("SUDO_USER", "")
	if err := os.MkdirAll(home+"/astonish", 0700); err != nil {
		t.Fatal(err)
	}
	bot := store.Bot{ID: uuid.New(), HomeSessionID: uuid.New(), Name: "Skills PTY", Scope: store.BotScopePersonal, Status: store.BotDraft, Version: 1, Revision: 1}
	skill := store.BotSkill{ID: uuid.New(), BotID: bot.ID, Title: "Verified research", Status: store.BotSkillActive, HeadRevision: 2, HeadDigest: "skill-digest"}
	base := "/api/studio/bots/" + bot.ID.String()
	var empty, unavailable, unsupported atomic.Bool
	empty.Store(true)
	var writes atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer skill-token" {
			http.Error(w, "unauthorized", 401)
			return
		}
		if r.Method != http.MethodGet {
			writes.Add(1)
			http.Error(w, "read-only", 405)
			return
		}
		write := func(v any) { _ = json.NewEncoder(w).Encode(v) }
		switch r.URL.Path {
		case "/api/studio/bots":
			write(map[string]any{"bots": []store.Bot{bot}})
		case base:
			write(bot)
		case base + "/snapshot":
			write(store.BotSnapshot{Bot: bot})
		case base + "/runs":
			write(map[string]any{"runs": []store.BotRun{}})
		case base + "/skills":
			if unavailable.Load() {
				http.Error(w, "offline", 503)
				return
			}
			list := []store.BotSkill{}
			if !empty.Load() {
				list = append(list, skill)
			}
			write(store.BotSkillList{Skills: list})
		case base + "/skills/" + skill.ID.String() + "/revisions":
			if unsupported.Load() {
				write(store.BotSkillRevisionList{Revisions: []store.BotSkillRevision{{SchemaVersion: 99, Responsibility: "UNTRUSTED-SCHEMA-CONTENT"}}})
				return
			}
			write(store.BotSkillRevisionList{Revisions: []store.BotSkillRevision{{SchemaVersion: 1, Revision: 2, Digest: "skill-digest", Responsibility: "Inspect original sources", ChangeReason: "Owner feedback"}}})
		case base + "/skills/" + skill.ID.String() + "/runs":
			write(store.BotSkillRunList{Runs: []store.BotRun{{ID: uuid.New(), Report: store.BotReport{Skill: &store.BotSkillReceipt{SchemaVersion: 1, Kind: store.BotSkillUnchanged, UsedRevision: 2}}}}})
		case base + "/sessions/" + bot.HomeSessionID.String() + "/events":
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, ": connected\n\n")
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
	if err = tokens.Save(&client.Tokens{AccessToken: "skill-token"}); err != nil {
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
		until := time.Now().Add(15 * time.Second)
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
	send := func(text string) {
		t.Helper()
		mu.Lock()
		output.Reset()
		mu.Unlock()
		if _, err := io.WriteString(pt, text); err != nil {
			t.Fatal(err)
		}
	}
	wait("Skills PTY")
	send("/bots skills\r")
	wait("No committed skills")
	t.Log("SKILL_SURFACE pty/empty PASS")
	empty.Store(false)
	send("r")
	wait("Verified research")
	send("\r")
	wait("Inspect original sources")
	t.Log("SKILL_SURFACE pty/history PASS")
	unsupported.Store(true)
	send("r")
	wait("Unsupported skill schema 99")
	mu.Lock()
	leaked := strings.Contains(output.String(), "UNTRUSTED-SCHEMA-CONTENT")
	mu.Unlock()
	if leaked {
		t.Fatal("unknown schema interpreted")
	}
	t.Log("SKILL_SURFACE pty/schema PASS")
	unsupported.Store(false)
	send("u")
	wait("unchanged")
	t.Log("SKILL_SURFACE pty/receipts PASS")
	send("\x1b")
	wait("Verified research")
	unavailable.Store(true)
	send("r")
	wait("Unavailable or stale")
	unavailable.Store(false)
	send("r")
	time.Sleep(200 * time.Millisecond)
	_ = pty.Setsize(pt, &pty.Winsize{Rows: 40, Cols: 141})
	wait("Verified research")
	t.Log("SKILL_SURFACE pty/stale-retry PASS")
	send("\x1b")
	time.Sleep(200 * time.Millisecond)
	send("/bots skills\r")
	wait("Verified research")
	if writes.Load() != 0 {
		t.Fatalf("inspection made %d writes", writes.Load())
	}
	t.Log("SKILL_SURFACE pty/escape-readonly PASS")
}
