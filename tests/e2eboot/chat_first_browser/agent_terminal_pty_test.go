package main

import (
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

	cli "github.com/SAP/astonish/cmd/astonish"
	"github.com/SAP/astonish/pkg/client"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// This regression exercises real Execute/Bubble Tea/PTY output against a
// controlled read-only HTTP transport. It is NOT candidate worker acceptance
// and must never supply agent-terminal.json or a live acceptance receipt.
func TestAgentTerminalRendererProcess(t *testing.T) {
	if os.Getenv("ASTONISH_AGENT_RENDERER_CHILD") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			os.Args = append([]string{"astonish"}, os.Args[i+1:]...)
			break
		}
	}
	if err := cli.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

func TestAgentTerminalRealPTYDecoder(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("SUDO_USER", "")
	if err := os.MkdirAll(filepath.Join(home, "astonish"), 0700); err != nil {
		t.Fatal(err)
	}
	bot := store.Bot{ID: uuid.New(), HomeSessionID: uuid.New(), Name: "Decoder regression", Scope: store.BotScopePersonal, Status: store.BotDraft, Version: 1, Revision: 1}
	base := "/api/studio/bots/" + bot.ID.String()
	publish := make(chan struct{})
	replayed := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("unexpected mutation: %s", r.URL.Path)
			http.Error(w, "read only", 405)
			return
		}
		write := func(value any) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(value)
		}
		switch r.URL.Path {
		case base:
			write(bot)
		case "/api/studio/bots":
			write(map[string]any{"bots": []store.Bot{bot}})
		case base + "/snapshot":
			write(store.BotSnapshot{Bot: bot})
		case base + "/runs":
			write(map[string]any{"runs": []store.BotRun{}})
		case base + "/skills":
			write(store.BotSkillList{Skills: []store.BotSkill{}})
		case "/api/studio/sessions":
			write(map[string]any{"sessions": []any{}})
		case "/api/settings/providers/effective":
			write(map[string]any{})
		case base + "/sessions/" + bot.HomeSessionID.String() + "/events":
			w.Header().Set("Content-Type", "text/event-stream")
			report := fmt.Sprintf("id: %s:1\nevent: transcript\ndata: {\"author\":\"agent\",\"invocationId\":\"bot-report:regression\",\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"Actual terminal decoder result.\"}]}}\n\n", bot.HomeSessionID)
			select {
			case <-publish:
				fmt.Fprint(w, report) // second connection restores the same publication
				fmt.Fprintf(w, "id: %s:1\nevent: replay_complete\ndata: {}\n\n", bot.HomeSessionID)
				w.(http.Flusher).Flush()
			default:
				fmt.Fprintf(w, "id: %s:0\nevent: replay_complete\ndata: {}\n\n", bot.HomeSessionID)
				w.(http.Flusher).Flush()
				close(replayed)
				select {
				case <-publish:
					fmt.Fprint(w, report) // first connection receives a live update
					w.(http.Flusher).Flush()
				case <-r.Context().Done():
					return
				}
			}
			<-r.Context().Done()
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
	if err = tokens.Save(&client.Tokens{AccessToken: "renderer-only"}); err != nil {
		t.Fatal(err)
	}
	env := append(agentTerminalEnvironment(home), "ASTONISH_AGENT_RENDERER_CHILD=1")
	for connection := 0; connection < 2; connection++ {
		p, err := agentStartTerminal(os.Args[0], env, "-test.run=^TestAgentTerminalRendererProcess$", "--", "chat", "--bot", bot.ID.String())
		if err != nil {
			t.Fatal(err)
		}
		func() {
			defer p.close()
			if connection == 0 {
				select {
				case <-replayed:
				case <-time.After(10 * time.Second):
					t.Fatal("first renderer connection did not reach replay barrier")
				}
				if err := p.wait("/bots"); err != nil {
					t.Fatal(err)
				}
				if strings.Contains(p.text(), "Actual terminal decoder result.") {
					t.Fatal("result visible before live publication")
				}
				observedSend := agentLearningTerminalSend(func(key, message string) store.BotRun {
					if key != "skills-after-removal" || message != "existing request" {
						t.Fatal("terminal observation altered owner request")
					}
					close(publish) // one controlled publication, never a provider call
					return store.BotRun{ID: bot.ID, BotID: bot.ID}
				}, func(run store.BotRun) store.BotRun {
					if err := p.wait("Actual terminal decoder result."); err != nil {
						t.Fatal(err)
					}
					return run
				}, func(startWork func() error) (map[string]string, error) {
					select {
					case <-replayed:
					default:
						t.Fatal("manual reuse before real PTY replay barrier")
					}
					return nil, startWork()
				}, map[string]any{}, func() {}, func() {})
				if run := observedSend("skills-after-removal", "existing request"); run.BotID != bot.ID {
					t.Fatal("terminal wrapper lost the actual Bot run")
				}
			}
			for _, step := range []struct{ command, want string }{
				{"", "Actual terminal decoder result."},
				{"/bots status\r", "Next run:"},
				{"/bots skills\r", "No committed skills"},
				{"/bots\r", "Bots"},
			} {
				frame, err := p.frame(step.command, step.want)
				if err != nil {
					t.Fatal(err)
				}
				payload, err := json.Marshal(frame)
				if err != nil {
					t.Fatal(err)
				}
				python := exec.Command("python3", "-B", "-c", "import json,sys; from agent_terminal_contract import screen; print(screen(json.load(sys.stdin)))")
				path, err := filepath.Abs("..")
				if err != nil {
					t.Fatal(err)
				}
				python.Env = append(os.Environ(), "PYTHONPATH="+path)
				python.Stdin = strings.NewReader(string(payload))
				out, err := python.CombinedOutput()
				if err != nil {
					p.mu.Lock()
					raw, _ := json.Marshal(p.output.String())
					p.mu.Unlock()
					t.Fatalf("real PTY decode: %v\n%s\nwire=%s", err, out, raw)
				}
				if !strings.Contains(string(out), step.want) {
					t.Fatalf("%q absent from final visible screen: %s", step.want, out)
				}
				if step.command != "" {
					if err := p.send("\x1b"); err != nil {
						t.Fatal(err)
					}
					time.Sleep(250 * time.Millisecond)
				}
			}
		}()
	}
	t.Log("Real Execute/Bubble Tea PTY Home/status/skills/Bots bytes decoded on two connections; controlled transport only, not a candidate acceptance receipt")
}
