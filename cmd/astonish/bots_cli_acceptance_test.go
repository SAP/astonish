package astonish

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/client"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// Reuse TestBotCLIProcess from TestBotCLIAndPTYAcceptance: this crosses the
// real Execute dispatcher, remote config/token loading, argument parser and HTTP
// transport in a child process. Keep this independently runnable while the PTY
// surface is being changed; no launcher or TUI behavior is stubbed or suppressed.
func runBotIntentCLIProcess(t *testing.T, wantExit int, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], append([]string{"-test.run=^TestBotCLIProcess$", "--"}, args...)...)
	cmd.Env = append(os.Environ(), "ASTONISH_BOT_CLI_CHILD=1", "TERM=xterm-256color", "NO_COLOR=1")
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("CLI timed out: %v\n%s", args, out)
	}
	exit := 0
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("start CLI: %v", err)
		}
		exit = exitErr.ExitCode()
	}
	if exit != wantExit {
		t.Fatalf("%v: exit=%d want=%d\n%s", args, exit, wantExit, out)
	}
	return string(out)
}

func configureBotIntentCLIProcess(t *testing.T, url, token string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("SUDO_USER", "")
	if err := os.MkdirAll(filepath.Join(home, "astonish"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := client.SaveRemoteConfig(&client.RemoteConfig{URL: url}); err != nil {
		t.Fatal(err)
	}
	ts, err := client.NewTokenStore()
	if err != nil {
		t.Fatal(err)
	}
	if err := ts.Save(&client.Tokens{AccessToken: token}); err != nil {
		t.Fatal(err)
	}
}

func TestBotCLIExplicitIntentSubprocessAcceptance(t *testing.T) {
	for _, intent := range []struct {
		command, endpoint string
		status            store.BotStatus
	}{
		{"test-once", "test-run", store.BotDraft},
		{"conversation", "conversation", store.BotPaused},
	} {
		for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
			t.Run(intent.command+"/"+string(scope), func(t *testing.T) {
				id, runID := uuid.New(), uuid.New()
				otherScope := store.BotScopeTeam
				if scope == store.BotScopeTeam {
					otherScope = store.BotScopePersonal
				}
				for _, tc := range []struct {
					name                       string
					version, confirm           string
					status                     store.BotStatus
					responseStatus             int
					generatedKey, invalidToken bool
					wantExit, wantPosts        int
					want                       string
				}{
					{name: "reviewed", version: "7", confirm: string(scope) + ":" + id.String(), wantPosts: 1},
					{name: "generated-key", version: "7", confirm: string(scope) + ":" + id.String(), generatedKey: true, wantPosts: 1},
					{name: "missing-version", confirm: string(scope) + ":" + id.String(), wantExit: 1, want: "explicitly reviewed --version 7"},
					{name: "stale-version", version: "6", confirm: string(scope) + ":" + id.String(), wantExit: 1, want: "explicitly reviewed --version 7"},
					{name: "missing-confirm", version: "7", wantExit: 1, want: "scoped confirmation"},
					{name: "wrong-scope-confirm", version: "7", confirm: string(otherScope) + ":" + id.String(), wantExit: 1, want: "scoped confirmation"},
					{name: "wrong-bot-confirm", version: "7", confirm: string(scope) + ":" + uuid.NewString(), wantExit: 1, want: "scoped confirmation"},
					{name: "active-does-not-activate", version: "7", confirm: string(scope) + ":" + id.String(), status: store.BotActive, wantExit: 1, want: "no activation was performed"},
					{name: "server-forbidden", version: "7", confirm: string(scope) + ":" + id.String(), responseStatus: 403, wantPosts: 1, wantExit: 1, want: "403"},
					{name: "server-conflict", version: "7", confirm: string(scope) + ":" + id.String(), responseStatus: 409, wantPosts: 1, wantExit: 1, want: "409"},
					{name: "server-error", version: "7", confirm: string(scope) + ":" + id.String(), responseStatus: 500, wantPosts: 1, wantExit: 1, want: "500"},
					{name: "invalid-token", version: "7", confirm: string(scope) + ":" + id.String(), invalidToken: true, wantExit: 1, want: "login"},
				} {
					t.Run(tc.name, func(t *testing.T) {
						status := intent.status
						if tc.status != "" {
							status = tc.status
						}
						bot := store.Bot{ID: id, Name: "Reviewed Bot", Scope: scope, Status: status, Version: 7}
						var mu sync.Mutex
						var requests []string
						posts, authFailures := 0, 0
						server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
							mu.Lock()
							defer mu.Unlock()
							requests = append(requests, r.Method+" "+r.URL.Path)
							if r.Header.Get("Authorization") != "Bearer acceptance-token" {
								authFailures++
								http.Error(w, "unauthorized", http.StatusUnauthorized)
								return
							}
							if r.URL.RawQuery != "scope="+string(scope) {
								t.Errorf("scope/query changed: %s", r.URL)
							}
							w.Header().Set("Content-Type", "application/json")
							base := "/api/studio/bots/" + id.String()
							switch {
							case r.Method == http.MethodGet && r.URL.Path == base:
								_ = json.NewEncoder(w).Encode(bot)
							case r.Method == http.MethodPost && r.URL.Path == base+"/"+intent.endpoint:
								posts++
								var body map[string]any
								if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
									t.Error(err)
								}
								if len(body) != 3 || body["version"] != float64(7) || body["instructions"] != "Observe only; do not activate" {
									t.Errorf("unexpected intent DTO: %#v", body)
								}
								key, _ := body["idempotency_key"].(string)
								if tc.generatedKey {
									if _, err := uuid.Parse(key); err != nil {
										t.Errorf("generated key %q: %v", key, err)
									}
								} else if key != "reviewed-execution-key" {
									t.Errorf("key changed: %q", key)
								}
								if tc.responseStatus != 0 {
									http.Error(w, "execution rejected", tc.responseStatus)
									return
								}
								_ = json.NewEncoder(w).Encode(store.BotRun{ID: runID, BotID: id, Status: store.RunQueued})
							default:
								t.Errorf("unexpected endpoint (activation is forbidden): %s %s", r.Method, r.URL)
								http.Error(w, "unexpected endpoint", 404)
							}
						}))
						defer server.Close()
						token := "acceptance-token"
						if tc.invalidToken {
							token = "invalid"
						}
						configureBotIntentCLIProcess(t, server.URL, token)
						args := []string{"bots", intent.command, id.String(), "--scope", string(scope), "--instructions", "Observe only; do not activate", "--json"}
						if tc.version != "" {
							args = append(args, "--version", tc.version)
						}
						if tc.confirm != "" {
							args = append(args, "--confirm", tc.confirm)
						}
						if !tc.generatedKey {
							args = append(args, "--key", "reviewed-execution-key")
						}
						out := runBotIntentCLIProcess(t, tc.wantExit, args...)
						if tc.wantExit == 0 {
							var run store.BotRun
							if err := json.Unmarshal([]byte(out), &run); err != nil || run.ID != runID || run.BotID != id || run.Status != store.RunQueued {
								t.Fatalf("unexpected CLI JSON: %s (%v)", out, err)
							}
						} else if !strings.Contains(out, tc.want) {
							t.Fatalf("missing %q: %s", tc.want, out)
						}
						mu.Lock()
						defer mu.Unlock()
						if posts != tc.wantPosts || len(requests) != 1+tc.wantPosts {
							t.Errorf("posts=%d want=%d requests=%v", posts, tc.wantPosts, requests)
						}
						if tc.invalidToken && authFailures != 1 {
							t.Errorf("auth failure not exercised: %d", authFailures)
						}
					})
				}
			})
		}
	}
}

func TestBotCLIIntentAmbiguousNameSubprocessAcceptance(t *testing.T) {
	for _, command := range []string{"test-once", "conversation"} {
		t.Run(command, func(t *testing.T) {
			bots := []store.Bot{
				{ID: uuid.New(), Name: "Monitor", Scope: store.BotScopeTeam, Status: store.BotDraft},
				{ID: uuid.New(), Name: "Monitor", Scope: store.BotScopeTeam, Status: store.BotPaused},
			}
			var mu sync.Mutex
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				requests++
				if r.Method != http.MethodGet || r.URL.Path != "/api/studio/bots" || r.URL.Query().Get("scope") != "team" || r.Header.Get("Authorization") != "Bearer acceptance-token" {
					t.Errorf("unexpected ambiguous-name request: %s %s", r.Method, r.URL)
					http.Error(w, "unexpected request", 400)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"bots": bots})
			}))
			defer server.Close()
			configureBotIntentCLIProcess(t, server.URL, "acceptance-token")
			out := runBotIntentCLIProcess(t, 1, "bots", command, "Monitor", "--scope", "team", "--version", "7", "--confirm", "team:"+bots[0].ID.String())
			for _, want := range []string{"ambiguous", bots[0].ID.String(), bots[1].ID.String(), "team", string(store.BotDraft), string(store.BotPaused)} {
				if !strings.Contains(out, want) {
					t.Errorf("candidate detail %q missing: %s", want, out)
				}
			}
			mu.Lock()
			defer mu.Unlock()
			if requests != 1 {
				t.Errorf("ambiguous name must not mutate/resolve arbitrarily: requests=%d", requests)
			}
		})
	}
}

func TestBotCLIIntentLocalGatingSubprocessAcceptance(t *testing.T) {
	configureBotIntentCLIProcess(t, "http://127.0.0.1:1", "acceptance-token")
	if err := client.RemoveRemoteConfig(); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"test-once", "conversation"} {
		out := runBotIntentCLIProcess(t, 1, "bots", command, uuid.NewString(), "--version", "7", "--confirm", fmt.Sprintf("personal:%s", uuid.NewString()))
		if !strings.Contains(out, "login") {
			t.Fatalf("missing remote login gate: %s", out)
		}
	}
}
