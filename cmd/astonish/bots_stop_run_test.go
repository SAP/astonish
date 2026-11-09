package astonish

import (
	"context"
	"encoding/json"
	"github.com/SAP/astonish/pkg/client"
	"github.com/SAP/astonish/pkg/store"
	"github.com/creack/pty"
	"github.com/google/uuid"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBotsStopRun(t *testing.T) {
	id, rid := uuid.New(), uuid.New()
	bot := store.Bot{ID: id, Scope: store.BotScopeTeam}
	run := store.BotRun{ID: rid, BotID: id, Status: store.RunRunning}
	posts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("scope") != "team" {
			t.Errorf("wrong scope: %s", r.URL)
		}
		base := "/api/studio/bots/" + id.String()
		switch {
		case r.Method == "GET" && r.URL.Path == base:
			json.NewEncoder(w).Encode(bot)
		case r.Method == "GET" && r.URL.Path == base+"/runs":
			json.NewEncoder(w).Encode(map[string]any{"runs": []store.BotRun{run}})
		case r.Method == "POST" && r.URL.Path == base+"/runs/"+rid.String()+"/cancel":
			posts++
			json.NewEncoder(w).Encode(run)
		default:
			t.Errorf("unexpected mutation/request: %s %s", r.Method, r.URL)
			http.Error(w, "unexpected", 500)
		}
	}))
	defer srv.Close()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("SUDO_USER", "")
	if err := os.MkdirAll(filepath.Join(home, "astonish"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := client.SaveRemoteConfig(&client.RemoteConfig{URL: srv.URL}); err != nil {
		t.Fatal(err)
	}
	ts, err := client.NewTokenStore()
	if err != nil {
		t.Fatal(err)
	}
	if err = ts.Save(&client.Tokens{AccessToken: "test-token"}); err != nil {
		t.Fatal(err)
	}
	c, err := client.New()
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"stop-run", id.String(), "--scope", "team", "--run", rid.String()}
	for _, confirm := range []string{"", "team:" + id.String(), "personal:" + id.String() + ":" + rid.String(), "team:" + id.String() + ":" + uuid.NewString()} {
		a := append(append([]string{}, args...), "--confirm", confirm)
		if err := runBotsCommand(context.Background(), c, a, io.Discard); err == nil {
			t.Fatalf("accepted confirmation %q", confirm)
		}
	}
	if posts != 0 {
		t.Fatal("unconfirmed mutation")
	}
	args = append(args, "--confirm", "team:"+id.String()+":"+rid.String())
	if err := runBotsCommand(context.Background(), c, args, io.Discard); err != nil {
		t.Fatal(err)
	}
	if posts != 1 {
		t.Fatalf("cancel count %d", posts)
	}
	posts = 0
	// Real command dispatcher under PTY, not merely a command handler invocation.
	cmd := exec.Command(os.Args[0], append([]string{"-test.run=^TestBotCLIProcess$", "--", "bots"}, args...)...)
	cmd.Env = append(os.Environ(), "ASTONISH_BOT_CLI_CHILD=1", "TERM=xterm-256color")
	pt, err := pty.Start(cmd)
	if err != nil {
		t.Fatal(err)
	}
	defer pt.Close()
	output, _ := io.ReadAll(pt)
	if err := cmd.Wait(); err != nil {
		t.Fatalf("PTY: %v %s", err, output)
	}
	if posts != 1 || !strings.Contains(string(output), rid.String()) {
		t.Fatalf("PTY count=%d output=%s", posts, output)
	}
}
